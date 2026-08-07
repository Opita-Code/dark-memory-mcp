// Package embedder defines the contract for pluggable text-embedding
// backends used by the future hybrid retrieval path (BM25 + vector +
// RRF fusion). v2.8.0-alpha ships the BM25-only path (FTS5); v2.8.0-alpha+
// will thread an Embedder through the search axis when an embedder is
// configured.
//
// # Activation model (deliberate opt-in)
//
// The .mcpb bundle does NOT bundle an embedding model. Operators who
// want hybrid retrieval opt in via env vars:
//
//	DARK_MEMORY_EMBEDDER=openai  → use OpenAI text-embedding-3-small (needs OPENAI_API_KEY)
//	DARK_MEMORY_EMBEDDER=onnx    → use ONNX Runtime + downloaded model
//	DARK_MEMORY_EMBEDDER=unset   → BM25-only (default, current behavior)
//	DARK_MEMORY_EMBEDDER=none    → explicit disable
//
// Why opt-in and not bundled:
//   - Bundle stays small (~50MB, not ~150MB with an embedding model).
//   - Operators without internet keep pure-local search.
//   - API-key-driven deployments (OpenAI/Anthropic) avoid the offline
//     download dependency.
//   - Privacy posture is configurable per deployment.
//
// # Lifecycle
//
//   - Factory reads the env once per Server boot.
//   - One Embedder instance per Server, reused across requests.
//   - Close() releases native resources (ONNX Runtime session etc.).
//
// # Trust boundary
//
// Embedders receive only the text passed by the caller. The Embedder
// MUST NOT log or persist the text outside of the in-process embedding
// call. The OpenAI adapter optionally uses an env-key to authenticate;
// the ONNX adapter downloads ONNX weights + tokenizer from a pinned
// HTTPS source (model file SHA verified post-download).
package embedder

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/dark-agents/dark-memory-mcp/internal/embedder/detect"
)

// Vec is one embedding vector. Length is implementation-defined (384
// for all-MiniLM-L6-v2, 1536 for OpenAI text-embedding-3-small). All
// callers MUST treat Vec as opaque float32 array; only store-side
// similarity code reads the underlying numbers.
type Vec []float32

// Dim returns the dimensionality of v. Returns 0 for nil/empty.
func (v Vec) Dim() int {
	return len(v)
}

// Embedder turns a batch of strings into vectors. Implementations
// MUST be safe for concurrent use (the Store may call Embed under a
// shared mutex during a write path).
//
// Kind is a stable identifier ("none" | "openai" | "onnx"). It is
// recorded in health_ping so operators can confirm which backend is
// active without reverse-engineering from the binary size.
//
// Dim is the static dimensionality. The none stub returns 0; ONNX
// returns 384; OpenAI returns 1536. Store-side code that stores Vec
// columns uses this to size the schema column.
//
// Embed returns one Vec per input text in the same order. Empty texts
// return an empty Vec slot (not an error) so the caller can hold
// parallel arrays. Context cancellation MUST be honored: long-running
// HTTP calls (OpenAI) and large-token ONNX inferences are cancelable.
//
// Close releases native resources. Calling Embed after Close is
// undefined; callers in tests should defer Close.
type Embedder interface {
	Kind() string
	Dim() int
	Embed(ctx context.Context, texts []string) ([]Vec, error)
	Close() error
}

// KindNone is the canonical identifier for the disabled stub. Always
// returned by None().Kind(). Recorded in health_ping and store audit
// rows so operators can see at a glance whether the hybrid axis is
// armed.
const KindNone = "none"

// KindOpenAI is the canonical identifier for the OpenAI adapter.
const KindOpenAI = "openai"

// KindONNX is the canonical identifier for the ONNX local adapter.
const KindONNX = "onnx"

// KindVoyage is the canonical identifier for the Voyage AI adapter.
// PR-2.1 (row 164 §2): preferred rung for claude-code harness.
const KindVoyage = "voyage"

// KindOllama is the canonical identifier for the local Ollama adapter.
// PR-2.1 (row 164 §2): preferred rung when OLLAMA_HOST is set or a
// local daemon is reachable on 127.0.0.1:11434.
const KindOllama = "ollama"

// ErrDisabled is returned by the disabled stub's Embed method. It
// signals to callers that hybrid retrieval is intentionally off. The
// Store treats this as "fall back to BM25 only" rather than an error
// surfaced to the user — surfacing it would be noisy in setups where
// the operator has not configured any embedder.
var ErrDisabled = errors.New("embedder: disabled (DARK_MEMORY_EMBEDDER=none or unset)")

// ErrKeyMissing is returned when a key-required adapter is selected
// but its env-var key (e.g. OPENAI_API_KEY) is not set. The caller
// may choose to surface this or fall back to ErrDisabled.
var ErrKeyMissing = errors.New("embedder: required API key env var not set")

// DefaultKind reads $DARK_MEMORY_EMBEDDER and returns the canonical
// kind string. Unknown / empty / unset all yield KindNone. Lowercase
// comparison; whitespace trimmed. The function is pure (no I/O), safe
// to call from hot paths if needed.
//
// Recognized values: "none", "openai", "onnx". Case-insensitive.
// Anything else → none (fail safe: never silently upgrade to a paid
// or network-dependent adapter on a typo).
func DefaultKind() string {
	switch normalize(os.Getenv("DARK_MEMORY_EMBEDDER")) {
	case "openai":
		return KindOpenAI
	case "onnx", "onnx-local", "local":
		return KindONNX
	default:
		return KindNone
	}
}

// normalize lowercases and trims spaces — env vars are user-supplied
// and we don't want " OpenAI " to silently fall through to none.
func normalize(s string) string {
	if len(s) == 0 {
		return ""
	}
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	j := len(s)
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t') {
		j--
	}
	if i >= j {
		return ""
	}
	out := make([]byte, j-i)
	for k := 0; k < j-i; k++ {
		c := s[i+k]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[k] = c
	}
	return string(out)
}

// Factory returns the Embedder configured via $DARK_MEMORY_EMBEDDER.
// It is the single boot-time entry point; subsequent recall/search
// paths reuse the returned Embedder.
//
// When DefaultKind is KindNone, Factory returns the disabled stub and a
// nil error — the system stays BM25-only with no friction. When the
// kind requires a key (e.g. OpenAI) and the key is missing, the
// adapter returns its ErrKeyMissing error; the caller (recall
// orchestrator at the next integration commit) falls back to the
// disabled stub.
//
// Returns a *SyncEmbedder wrapper so concurrent use is safe even for
// adapters that aren't internally thread-safe (notably the ONNX
// session).
func Factory(ctx context.Context) (Embedder, error) {
	return FactoryAt(ctx, nil)
}

// FactoryAt is the integration seam: when nil != nil, the caller's
// options (model override, model-file path, base URL override for
// OpenAI-compatible providers) are layered in. Reserved for the next
// commit's RRF wiring; the current package has no adapters beyond
// none, so the seam is dormant but typed.
//
// Callers should pass nil; the function tolerates nil safely.
func FactoryAt(ctx context.Context, opts *FactoryOptions) (Embedder, error) {
	_ = opts
	_ = ctx
	inner, err := factoryInner()
	if err != nil {
		// Degrade graceful: any key-missing or unsupported kind falls
		// back to the disabled stub. Operators who want loud failure
		// can check DefaultKind at boot and bail there.
		return NewSync(None()), nil //nolint:nilerr // graceful fallback
	}
	return NewSync(inner), nil
}

// factoryInner builds the configured adapter without sync wrapping.
// Kept private so the only public entrypoint is Factory/FactoryAt.
//
// Adapters register themselves via RegisterAdapter in their init();
// this breaks the would-be import cycle (embedder ↔ openai / onnx
// / mock). The registry shape is private to embedder — adapter
// packages only see RegisterAdapter.
func factoryInner() (Embedder, error) {
	switch DefaultKind() {
	case KindNone:
		return None(), nil
	case KindOpenAI, KindONNX, KindMock:
		// Adapters register themselves in init() (see e.g. the
		// openai package's init).
		adapterMu.RLock()
		factory, ok := adapters[DefaultKind()]
		adapterMu.RUnlock()
		if !ok {
			// Adapter not registered (likely compile-time
			// pruning or a future kind string). Fall back
			// gracefully.
			return None(), nil
		}
		return factory(Options{})
	default:
		return None(), nil
	}
}

// FactoryAuto is the v2.9.1-alpha PR-2.1 boot-time entry point. It
// walks the harness-aware ladder per row 164 §2 and picks the first
// rung whose adapter constructor succeeds.
//
// Order of precedence (v2.9.1-alpha PR-2.1):
//
//  1. DARK_MEMORY_EMBEDDER manual override (any kind, registered).
//  2. Harness-detected preferred rung (claude-code → voyage;
//     opencode/codex → openai; ollama → ollama).
//  3. Bundled ONNX (always available, offline default).
//  4. OPENAI_API_KEY last rung (backward compat with PR-2).
//  5. None() stub (BM25-only path; the only path that "succeeds"
//     with zero configuration).
//
// Unlike Factory(), FactoryAuto intentionally uses the harness's
// existing env state (per row 163 + row 164 §1: "we never ask for new
// API keys"). The .mcpb installer wires the harness's existing
// OPENAI_API_KEY into the dark-memory process at install time; the
// user does NOT type the key in chat.
//
// Returns a *SyncEmbedder wrapper so concurrent use is safe.
//
// Never returns an error — every fall-through is to a working stub.
// Operators who want loud failure should check DefaultKind or the
// result of Factory at boot and bail there.
func FactoryAuto() Embedder {
	// 1. Manual override (DARK_MEMORY_EMBEDDER).
	if k := DefaultKind(); k != KindNone {
		if e, err := tryKind(k); err == nil {
			return NewSync(e)
		}
	}

	// 2. Harness-detected preferred rung.
	harness := detect.Probe()
	if pref := harness.PreferredEmbedder(); pref != "none" {
		if e, err := tryKind(pref); err == nil {
			return NewSync(e)
		}
	}

	// 3. Bundled ONNX (offline default; row 163 amendment).
	if e, err := tryKind(KindONNX); err == nil {
		return NewSync(e)
	}

	// 4. OPENAI_API_KEY last rung (backward compat with PR-2).
	if os.Getenv("OPENAI_API_KEY") != "" {
		if e, err := tryKind(KindOpenAI); err == nil {
			return NewSync(e)
		}
	}

	// 5. Stub (always succeeds).
	return NewSync(None())
}

// tryKind attempts to instantiate the registered adapter for kind.
// Returns (Embedder, nil) on success; (nil, error) if the kind is
// not registered OR the adapter constructor returned an error
// (typically ErrKeyMissing, ErrDisabled, or a real failure).
//
// Used by FactoryAuto to walk the ladder rung-by-rung.
func tryKind(kind string) (Embedder, error) {
	adapterMu.RLock()
	factory, ok := adapters[kind]
	adapterMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("embedder: kind %q not registered", kind)
	}
	return factory(Options{})
}

// KindMock is the canonical identifier for the deterministic test
// adapter (internal/embedder/mock). Never honored by FactoryAuto;
// only meaningful to tests that pre-build an Embedder directly.
const KindMock = "mock"

// Options is the per-adapter option bag passed at New() time. The
// factory uses a fresh zero value for env-driven defaults; tests
// may pass a custom Options for fine control. Adapter packages
// type-assert to their own Options struct.
type Options struct{}

// adapters is the registry mapping kind string → factory function.
// Adapters call RegisterAdapter(kind, factory) in their init(); the
// factory is consulted only via factoryInner / FactoryAuto.
var (
	adapterMu sync.RWMutex
	adapters  = map[string]func(Options) (Embedder, error){}
)

// RegisterAdapter records a factory under kind. The embedder
// factory calls this when DefaultKind() returns kind. Re-registering
// the same kind replaces the prior factory (intentional; lets tests
// override the production factory with a deterministic mock).
//
// Adapter packages should call this from init() — see
// internal/embedder/openai/openai.go for the canonical example.
func RegisterAdapter(kind string, factory func(Options) (Embedder, error)) {
	adapterMu.Lock()
	defer adapterMu.Unlock()
	adapters[kind] = factory
}

// registeredAdapter returns a fresh Embedder instance from the
// registered factory for kind. Used by FactoryAuto only; never
// errors on missing factory — falls through to None() at the
// call site.
func registeredAdapter(kind string) Embedder {
	adapterMu.RLock()
	factory, ok := adapters[kind]
	adapterMu.RUnlock()
	if !ok {
		return nil
	}
	e, err := factory(Options{})
	if err != nil {
		return None()
	}
	return e
}

// ResetAdapterRegistry clears the registry. For tests only — never
// call from production code. The reset allows a test to assert
// against a fresh kind→factory mapping without inheriting whatever
// the previous test registered.
//
// Not exported via any user-facing code path; tests use it via
// embedder_reset_test.go.
func resetAdapterRegistry() {
	adapterMu.Lock()
	defer adapterMu.Unlock()
	adapters = map[string]func(Options) (Embedder, error){}
}

// FactoryOptions is the integration seam for adapter overrides. The
// type is declared now so callers in later commits can pass options
// without breaking the public API.
type FactoryOptions struct {
	// OpenAI: API key. Empty → reads OPENAI_API_KEY from env.
	OpenAIAPIKey string

	// OpenAI: model. Empty → "text-embedding-3-small".
	OpenAIModel string

	// ONNX: model file path. Empty → downloads on first use to
	// $DARK_HOME/models/.
	ONNXModelPath string

	// ONNX: tokenizer JSON path. Empty → downloads alongside the model.
	ONNXTokenizerPath string
}

// None returns the disabled embedder. The factory returns this when
// $DARK_MEMORY_EMBEDDER is unset, empty, or "none". The returned
// value is safe for concurrent use; Close is a no-op.
//
// The implementation lives next to its consumers in this package so
// that the embedder contract has no internal import cycles (the
// alternative — a separate internal/embedder/none package — creates
// a cycle the moment any future adapter wants to call back into
// embedder for helpers).
func None() Embedder { return &disabled{} }

// disabled is the no-I/O fallback embedder. Its Embed method always
// returns ErrDisabled; Close is a no-op. Implementation is inlined
// so future OpenAI / ONNX adapters can share the embedder package
// without an import cycle.
type disabled struct{}

// Kind returns embedder.KindNone. Recorded in health_ping and store
// audit rows.
func (d *disabled) Kind() string { return KindNone }

// Dim returns 0 (no dimensionality). The store side treats 0 as "this
// embedder is disabled; do not provision embedding columns".
func (d *disabled) Dim() int { return 0 }

// Embed returns ErrDisabled. The Store's search path matches on
// errors.Is to decide "no vector axis, fall back to BM5" and never
// surfaces the error to the user.
func (d *disabled) Embed(_ context.Context, texts []string) ([]Vec, error) {
	_ = texts
	return nil, ErrDisabled
}

// Close is a no-op. The disabled embedder has no resources to release.
func (d *disabled) Close() error { return nil }

// Sync wraps any Embedder with a mutex so concurrent use is safe even
// when the underlying impl is not internally synchronized (e.g. a
// single ONNX Runtime session shared across goroutines).
//
// OpenAI's HTTP client is safe for concurrent use but a wrapper costs
// ~50ns per call — negligible vs. network RTT. The ONNX session is
// the case that actually needs this.
type Sync struct {
	mu sync.Mutex
	e  Embedder
}

// NewSync wraps e in a Sync.
func NewSync(e Embedder) *Sync {
	return &Sync{e: e}
}

// Kind returns the wrapped embedder's kind.
func (s *Sync) Kind() string { return s.e.Kind() }

// Dim returns the wrapped embedder's dim.
func (s *Sync) Dim() int { return s.e.Dim() }

// Embed serializes calls. Context is unchanged — callers may cancel
// mid-call but the next call from another goroutine doesn't start
// until the previous one returned.
func (s *Sync) Embed(ctx context.Context, texts []string) ([]Vec, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.e.Embed(ctx, texts)
}

// Close closes the wrapped embedder. After Close, further calls
// return errors from the inner Embedder.
func (s *Sync) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.e.Close()
}

// Compile-time guard: the disabled stub satisfies the Embedder
// contract so any reference to embedder.Embedder can name *disabled.
var _ Embedder = (*disabled)(nil)
