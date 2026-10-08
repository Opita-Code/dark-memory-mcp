// Package orchestration contains the workflow API that wraps the
// Store with safety (INV-3), audit (INV-1), and economy (Atlan).
// Each Orchestrator method is one workflow operation that the MCP
// server (or any external caller) can invoke. They are the typed,
// reusable counterpart of the untyped Store interface.
//
// Conventions (spec 173):
//   - All methods take ctx context.Context as first param.
//   - All methods validate inputs and return error typed by the
//     store layer (ErrSessionRequired, ErrInvalidArgument, etc.).
//   - All writes emit write_audit atomically with the data write
//     (Store enforces this; orchestrator just supplies WriteContext).
//   - All reads require an active project (Store.requireProject).
//
// Layering:
//
//	MCP server (Wave 4)
//	    |
//	    v
//	Orchestrator  <-- this package
//	    |
//	    v
//	Store (sqlite or postgres partial impl)
//
// Safety:
//   - The canary check (INV-3) is invoked inside Store.Save*, so
//     orchestrators don't need to call it explicitly. They DO need
//     to populate the WriteContext so the audit row carries the
//     orchestrator's actor name.
package orchestration

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/artifact"
	"github.com/dark-agents/dark-memory-mcp/internal/auditgate"
	"github.com/dark-agents/dark-memory-mcp/internal/nli"
	"github.com/dark-agents/dark-memory-mcp/internal/safety"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/event"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/judge/v4judge"
	"github.com/dark-agents/dark-memory-mcp/internal/vlp"
)

// Orchestrator is the typed workflow API. Construct with New().
type Orchestrator struct {
	Store    store.Store
	Safety   *safety.Holder
	now      func() time.Time  // injectable for tests
	backends []ResearchBackend // registered research backends (O3)
	selector LLMSelector       // LLM selector for O5 Judge
	vlpUC    *vlp.UseCase      // VLP state machine (v2.13.0: auto-drive vibe-loop)

	// v2.17.0 (spec 1155): Persona registry + prompt builder.
	personaRegistry *PersonaRegistry    // lazy-initialized
	personaBuilder  *JudgePromptBuilder // lazy-initialized, depends on registry

	// Phase 9 alpha.20 Chunk 8.2: when true, the lazy PersonaRegistry
	// constructor (ensurePersonaRegistry) passes IncludeV4Alpha=true so
	// the 6 v4-new personas from v4alpha/judge (PersonaContent registry)
	// are merged into the registry. The legacy 8-person compiled set
	// stays unchanged; the merge adds 6 more personas with Source
	// discriminator "v4alpha". Default false → operator opts in via
	// WithV4AlphaPersonas(true). Wired by cmd/dark-mem-mcp/legacy_main.go
	// at boot so dark_memory_judge_list_personas returns 14 instead of 8.
	includeV4AlphaPersonas bool

	// v2.20.0 T08 (spec 1276): NLI Router for drift_judge artifact
	// pipeline. Lazy-initialized via EnsureNLIRouter from
	// Project.NLIConfig (added in T07). When nil, DriftJudge uses the
	// nli.Provider returned by httptest or http.DefaultClient-backed
	// factories with Project.NLIConfig defaults (DeBERTa, no cache,
	// no fallback). Wiring:
	//
	//   DefaultFailoverClient's NLI variant will be wired here in a
	//   follow-up. For T08, we expose the setter so the harness can
	//   inject the production chain.
	nliProvider   nli.Provider
	nliHTTPClient nli.HFInferenceClient // injectable for tests

	// Phase 14 T-302: LLMJudge (internal/v4alpha/judge/v4judge) is
	// the primary drift verdict source. When bound, DriftJudge tries
	// it FIRST; on ErrNoLLMBound / ErrProviderUnavailable /
	// ErrProviderTimeout / ErrProviderRateLimited, the pipeline
	// falls through to the legacy NLI chain (T-301 invariant: NLI
	// preserved 100%). On ErrProviderBadResponse (contract bug), the
	// pipeline does NOT fall through — the model itself is wrong, so
	// needs_human + Error Observatory row is the right outcome.
	//
	// Built lazily by ensureLLMJudge from Project.NLIConfig when the
	// primary binding's provider_id has the "judge-" prefix. Wired by
	// main.go from the persisted NLIConfig JSON column. Tests inject
	// via WithLLMJudge.
	llmJudge *v4judge.LLMJudge

	// v2.20.0 T08: URLFetcher for the artifact resolver. nil →
	// URL/artifact_id resolution returns ErrNotConfigured (the
	// resolver surfaces this). Wired by the harness via
	// WithURLFetcher; default no-op for file/git_sha/spec_id kinds.
	urlFetcher artifact.URLFetcher

	// v2.20.0 T11 (spec 1276): Materializer for the publish_vibe
	// Content → ArtifactRef migration. When callers supply
	// in.Artifact.Text, the orchestrator materializes the text to a
	// content-addressed file (SHA-256 anchored) BEFORE forwarding to
	// the LLM-judge. The Materialized ArtifactRef is the audit-trail
	// pointer; the LLM-judge still receives Content (Phase 1
	// contract — at v2.22.0 Content is removed for these judges).
	//
	// nil → the helper falls back to artifact.MaterializeFromText
	// (env-driven BaseDir: DARK_MATERIALIZE_DIR / UserCacheDir /
	// TempDir). Wiring in production main.go uses WithMaterializer
	// to inject a stable BaseDir.
	materializer *artifact.Materializer

	// OnActiveSessionChanged (v2.1.3 cache-invalidation fix) is invoked
	// after every successful SetActiveSession / ClearActiveSession write
	// so external caches (specifically the gate's
	// StoreBackedActiveSessionResolver) can invalidate their stale
	// entries synchronously.
	//
	// Background: the resolver caches the lookup result for
	// DefaultActiveSessionCacheTTL (5s). The cache is keyed by
	// project_id. session_start's wire path is
	//   wrapHandler → GateMiddleware.Wrap → buildGateInput
	//   → resolver.ActiveSessionID → cache miss → DB lookup
	//   → Cache filled with whatever was in projects.active_session_id
	//     BEFORE SetActiveSession wrote the new value (i.e. empty
	//     for a fresh boot, or the previous session_id otherwise)
	//   → inner → orch.SessionStart → SetActiveSession writes the
	//     new value to DB → cache NOT invalidated.
	// The next tool call within the 5s TTL returns the stale value
	// (empty or wrong session_id) and the gate refuses with
	// ErrFrameStaleTooFar / ErrSessionNotFound.
	//
	// main.go wires this to resolver.Invalidate so the cache is
	// flushed synchronously after each write. nil is safe — the
	// orchestrator skips the call.
	OnActiveSessionChanged func(projectID string)

	// AuditGate (C6 dark-cli hookup, v3.0.0+): when non-nil, every
	// PublishVibe call MUST have a corresponding AuditRecord in the
	// gate's store with a publisher pubkey the gate trusts, OR the
	// publish is rejected. The gate is the consumer-side counterpart
	// of dark-cli's `internal/audit/` producer package. Wired by
	// main.go from DARK_AUDIT_DIR + trust-root config; nil means
	// "no gate configured → bypass (permissive)" so a vanilla
	// dark-memory-mcp install still works without dark-cli.
	AuditGate *auditgate.ReferenceGate

	// TrustRootsDir is the directory from which to load trust roots
	// (raw ed25519 pubkey files, one per file, basename is the id).
	// When set and non-empty, the orchestrator loads every *.pubkey
	// file in the directory into the gate's trust set. When empty,
	// the gate is configured with whatever roots the harness wired
	// via WithAuditGate directly. Wired by main.go from
	// DARK_TRUST_ROOTS_DIR or defaults.
	TrustRootsDir string

	// pendingAuditProvenance is set by checkAuditGate on accept and
	// read by PublishVibe when constructing the PublishResult.
	// Cleared via defer after the result is built. NOT safe for
	// concurrent publish_vibe calls (single-flight assumption;
	// parallel publishes would race on this field — guarded by the
	// caller not invoking vibe_publish concurrently).
	pendingAuditProvenance *auditgate.Provenance

	// Phase 12 T-103b: progress emitter for the async drift_judge
	// background path (runAsyncJudgePipeline). When non-nil, the
	// background goroutine emits 4 progress events through its
	// lifecycle (started → in_progress → completed/failed). When nil,
	// the async path runs identically but skips progress emission
	// (backward compat for callers that haven't opted in via
	// WithEventEmitter). Wired by main.go at boot.
	eventEmitter *event.DriftJudgeProgressEmitter
}

// WithEventEmitter attaches the Phase 12 T-103b progress emitter
// used by runAsyncJudgePipeline. The emitter fires 4 progress events
// through the async drift_judge lifecycle so operators polling
// pipeline_status(artifact_id=X) get visibility into the background
// goroutine. Nil-safe: when nil, the async path runs identically
// without emitting progress (backward compat for callers that
// haven't opted in). Wired by main.go at boot.
func (o *Orchestrator) WithEventEmitter(e *event.DriftJudgeProgressEmitter) *Orchestrator {
	o.eventEmitter = e
	return o
}

// New constructs an Orchestrator with the given Store and Safety
// holder. Safety may be nil (orchestrator will construct an empty
// Holder). now is injectable for deterministic tests; pass nil to
// default to time.Now. Use WithBackends to register research backends
// and WithLLMSelector to wire the Judge pipeline.
func New(s store.Store, safe *safety.Holder) *Orchestrator {
	if safe == nil {
		h := &safety.Holder{}
		safe = h
	}
	return &Orchestrator{
		Store:  s,
		Safety: safe,
		now:    func() time.Time { return time.Now().UTC() },
	}
}

// WithVLP attaches the VLP state machine (UseCase) to the orchestrator.
// When set, PublishVibe / VibeSpec / SessionStart auto-emit VLP events
// so the vibe-loop state stays in sync with data-plane operations.
// Nil-safe: operations skip VLP emission when uc is nil.
func (o *Orchestrator) WithVLP(uc *vlp.UseCase) *Orchestrator {
	o.vlpUC = uc
	return o
}

// WithLLMSelector attaches an LLMSelector to the orchestrator. Used
// by O5 Judge. This is the PRIMARY mechanism: the harness wires its
// cloud LLM at boot time (spec 173 O5, v2.11.1).
//
// When no selector is injected, ensureLLMSelector (the SECONDARY
// fallback) auto-detects the harness LLM from env vars at call time.
// Operators who want explicit control should call WithLLMSelector
// at boot; operators without harness injection get the env-var bridge.
func (o *Orchestrator) WithLLMSelector(s LLMSelector) *Orchestrator {
	o.selector = s
	return o
}

// WithNLIRouter attaches an nli.Provider to the orchestrator. Used
// by O8 DriftJudge (v2.20.0 T08, spec 1276) — the artifact-anchored
// drift_judge pipeline resolves the artifact via artifact.Resolver
// and scores the (premise, hypothesis) pair through this Provider.
//
// When no Provider is injected, EnsureNLIRouter falls back to
// construction from Project.NLIConfig (T07). The fallback path
// reads the active project's NLIConfig, builds the Router + Cache +
// Provider chain, and caches the result. nil nliHTTPClient means
// the chain uses http.DefaultClient.
func (o *Orchestrator) WithNLIRouter(p nli.Provider) *Orchestrator {
	o.nliProvider = p
	return o
}

// WithLLMJudge attaches a v4judge.LLMJudge to the orchestrator.
// Used by Phase 14 T-302 (drift_judge dual-path selector).
//
// When non-null, DriftJudge tries the LLMJudge FIRST and uses its
// output as the canonical verdict. On fall-through errors
// (ErrNoLLMBound / ErrProviderUnavailable / ErrProviderTimeout /
// ErrProviderRateLimited / ErrInputTooLarge), the pipeline falls back
// to the legacy NLI chain (preserved 100% per T-301 invariant). On
// ErrProviderBadResponse (contract bug), the pipeline does NOT fall
// through — returns needs_human + Error Observatory row.
//
// When no LLMJudge is injected, ensureLLMJudge (the lazy fallback)
// constructs one from Project.NLIConfig when the primary binding's
// provider_id has the "judge-" prefix. nil → no LLM step at all
// (drift_judge is purely NLI, backward compat).
//
// Production wiring is main.go at boot from the persisted
// NLIConfig JSON column. Tests inject via this setter.
func (o *Orchestrator) WithLLMJudge(j *v4judge.LLMJudge) *Orchestrator {
	o.llmJudge = j
	return o
}

// WithNLIHTTPClient injects the HTTP client used by the NLI Provider
// factories (DeBERTaProvider, MiniCheckProvider). nil →
// http.DefaultClient. For tests, pass an httptest.Server-bound client.
func (o *Orchestrator) WithNLIHTTPClient(hc nli.HFInferenceClient) *Orchestrator {
	o.nliHTTPClient = hc
	return o
}

// EnsureNLIRouter returns the orchestrator's NLI Provider. When the
// orchestrator was bootstrapped with WithNLIRouter, the injected
// Provider is returned unconditionally. Otherwise, the Provider is
// built lazily from the active project's NLIConfig (T07).
//
// Returns (nil, nil) when the project has no NLIConfig or the config
// is disabled — DriftJudge uses the defaults over its own
// constructed chain (deberta-v3-large-mnli, no cache, no fallback).
//
// Returns an error when the project's NLIConfig is enabled but
// Provider construction fails (unknown provider_id, invalid endpoint,
// etc.). The orchestrator records the error in the Error Observatory.
func (o *Orchestrator) EnsureNLIRouter(ctx context.Context) (nli.Provider, error) {
	if o.nliProvider != nil {
		return o.nliProvider, nil
	}
	activeProject := o.Store.ActiveProject()
	if activeProject == "" {
		return nil, nil
	}
	// GetProjectRaw: the NLI/LLM judge needs the real bearer.
	// Sealed exception -- see store.Store.GetProjectRaw.
	proj, err := o.Store.GetProjectRaw(ctx, activeProject)
	if err != nil {
		return nil, err
	}
	if proj == nil || proj.NLIConfig == nil || !proj.NLIConfig.Enabled {
		return nil, nil
	}
	p, err := nliProviderForConfig(ctx, proj.NLIConfig, o.nliHTTPClient)
	if err != nil {
		return nil, err
	}
	o.nliProvider = p
	return p, nil
}

// ensureLLMJudge is the lazy fallback for the Phase 14 T-302 dual-path
// selector. It builds an LLMJudge from the active project's NLIConfig
// when the primary binding's provider_id has the "judge-" prefix.
//
// Behavior:
//   - llmJudge pre-injected via WithLLMJudge → returns it (test path).
//   - active project has no NLIConfig / disabled → returns nil
//     (drift_judge is purely NLI, backward compat).
//   - primary provider_id does NOT start with "judge-" → returns nil
//     (operator opted out of LLMJudge; drift_judge is purely NLI).
//   - primary provider_id starts with "judge-" → builds LLMJudge from
//     the primary config (http.DefaultClient for production). Caches
//     on the orchestrator so the build only runs once per boot.
//
// The wire format is OpenAI-compatible /v1/chat/completions
// (mirrors internal/nli/chat.go::ChatProvider), so the same endpoint +
// auth token work for both the NLI chat-* path and the LLMJudge judge-*
// path. The primary distinction is the ProviderID prefix: chat-* routes
// to NLIChat (SNLI-2018 task), judge-* routes to LLMJudge (drift-judge
// task with JSON verdict shape).
//
// Returns (nil, nil) when no LLM path applies. Returns an error when
// LLMJudge construction itself fails (provider_id has judge- prefix but
// endpoint is malformed, etc.) — callers must decide whether to fall
// through to NLI or surface the error.
func (o *Orchestrator) ensureLLMJudge(ctx context.Context) (*v4judge.LLMJudge, error) {
	if o.llmJudge != nil {
		return o.llmJudge, nil
	}
	activeProject := o.Store.ActiveProject()
	if activeProject == "" {
		return nil, nil
	}
	// GetProjectRaw: the NLI/LLM judge needs the real bearer.
	// Sealed exception -- see store.Store.GetProjectRaw.
	proj, err := o.Store.GetProjectRaw(ctx, activeProject)
	if err != nil {
		return nil, err
	}
	if proj == nil || proj.NLIConfig == nil || !proj.NLIConfig.Enabled {
		return nil, nil
	}
	if !strings.HasPrefix(proj.NLIConfig.Primary.ProviderID, "judge-") {
		return nil, nil
	}
	pc := v4judge.ProviderConfig{
		ProviderID: proj.NLIConfig.Primary.ProviderID,
		Endpoint:   proj.NLIConfig.Primary.Endpoint,
		AuthToken:  proj.NLIConfig.Primary.AuthToken,
		TimeoutMS:  proj.NLIConfig.Primary.TimeoutMS,
		ModelRev:   proj.NLIConfig.Primary.ModelRev,
	}
	j, err := v4judge.NewLLMJudge(pc, http.DefaultClient)
	if err != nil {
		return nil, fmt.Errorf("ensure llm judge: %w", err)
	}
	o.llmJudge = j
	return j, nil
}

// WithURLFetcher injects the URLFetcher used by the artifact
// resolver (T08). Production wiring is the T02 SSRFGuard-backed
// fetcher; tests can inject a stub. nil → URL/artifact_id kinds
// return ErrNotConfigured (the resolver surfaces this).
func (o *Orchestrator) WithURLFetcher(u artifact.URLFetcher) *Orchestrator {
	o.urlFetcher = u
	return o
}

// WithMaterializer injects the Materializer used by the publish_vibe
// Content → ArtifactRef migration (T11, spec 1276). Production
// wiring uses a stable BaseDir (e.g. $DARK_DATA_DIR/materialized/);
// tests inject a temp dir. nil → materializeForPublish falls back
// to artifact.MaterializeFromText (env-driven).
func (o *Orchestrator) WithMaterializer(m *artifact.Materializer) *Orchestrator {
	o.materializer = m
	return o
}

// WithAuditGate injects the gate that PublishVibe checks before
// persisting an artifact. nil → permissive (gate bypassed).
//
// The gate is the consumer-side counterpart of dark-cli's
// `internal/audit/` producer package. See
// /dark-cli/docs/AUDIT.md for the full protocol. Wired by main.go
// from DARK_AUDIT_DIR (store dir) + the harness's trust-root set.
// Tests inject a ReferenceGate pointed at a t.TempDir() store.
//
// TrustRootsDir is also set here when non-empty so the gate's trust
// set is loaded from disk at first publish (not at construction
// time — operators can drop new pubkey files without restarting
// the MCP).
func (o *Orchestrator) WithAuditGate(g *auditgate.ReferenceGate, trustRootsDir string) *Orchestrator {
	o.AuditGate = g
	o.TrustRootsDir = trustRootsDir
	return o
}

// loadAuditGateRoots returns the trust roots the orchestrator should
// configure the gate with. If TrustRootsDir is set, load every
// *.pubkey file in the directory (raw 32-byte ed25519 pubkey, one
// per file). Returns nil on any error — the caller falls back to
// "no trust roots → gate rejects all" (fail closed).
func (o *Orchestrator) loadAuditGateRoots() []ed25519.PublicKey {
	if o.TrustRootsDir == "" {
		return nil
	}
	roots, err := loadPubkeysFromDir(o.TrustRootsDir)
	if err != nil {
		log.Printf("dark-mem-mcp: load trust roots from %s: %v (gate will reject all)", o.TrustRootsDir, err)
		return nil
	}
	return roots
}

// materializeForPublish is the T11 bridge that converts caller-
// supplied text into a content-addressed ArtifactRef. It is the
// central audit-trail fix for the publish_vibe judge pipeline:
//
//  1. If a Materializer is injected → use it (stable BaseDir).
//  2. Otherwise → fall back to artifact.MaterializeFromText
//     (env-driven: DARK_MATERIALIZE_DIR / UserCacheDir / TempDir).
//  3. Idempotent: same text + sourceTag → same ArtifactRef.
//  4. Atomic write: readers never see partial bytes (T03 contract).
//  5. HardMaxBytes (4 MiB) enforced at entry.
//
// Returns:
//   - ArtifactRef{Kind: KindFile, Path: <sha256>.txt} on success.
//   - ErrMaterializeTooLarge if text > HardMaxBytes (4 MiB).
//   - Other errors wrapped: "publish_vibe: materialize: %w".
//
// Phase 1 (v2.20.0): the artifact_anchored path runs alongside the
// existing Content path. The LLM-judge still sees Content (Phase 1
// backward compat). At v2.22.0 (Phase 2) the Content param is removed
// for these judges and only ArtifactRef is accepted.
func (o *Orchestrator) materializeForPublish(ctx context.Context, text, sourceTag string) (artifact.ArtifactRef, error) {
	if o.materializer != nil {
		ref, err := o.materializer.Materialize(ctx, text, sourceTag)
		if err != nil {
			return artifact.ArtifactRef{}, fmt.Errorf("publish_vibe: materialize: %w", err)
		}
		return ref, nil
	}
	ref, err := artifact.MaterializeFromText(ctx, text, sourceTag)
	if err != nil {
		return artifact.ArtifactRef{}, fmt.Errorf("publish_vibe: materialize: %w", err)
	}
	return ref, nil
}

// defaultDriftJudgeProvider builds the fallback NLI Provider used
// when the project has no NLIConfig (or NLIConfig.Enabled=false).
// Returns a DeBERTa-only chain with package defaults. The HF API
// endpoint is read from HUGGINGFACE_TOKEN env var (the operator
// must supply it for the chain to work).
//
// Returns ErrNoLLMAvailable (wrapped) when no token is set — the
// drift_judge will surface this as verdict=needs_human per the
// DriftJudge contract.
func (o *Orchestrator) defaultDriftJudgeProvider(ctx context.Context) (nli.Provider, error) {
	// The default is DeBERTa-v3-large-mnli via the public HF Inference
	// endpoint. AuthToken is read from the operator's env (HUGGINGFACE_TOKEN
	// or the canonical alias).
	// T08 default: no endpoint, no auth token means ErrNoLLMAvailable
	// which surfaces as verdict=needs_human. The operator can configure
	// Project.NLIConfig.Primary with a real endpoint to go live.
	return nil, errMissingField("project.nli_config — no project NLI override registered; configure Project.NLIConfig.Primary.Endpoint + AuthToken")
}

// ensureLLMSelector lazily returns the LLM selector.
//
// Primary: harness injection via WithLLMSelector (checked first).
// Secondary (spec 1188 / v2.20.0): DefaultFailoverClient — the
// health-aware failover chain over the canonical catalog, backed by
// the OS keyring (with env-var fallback). This REPLACED the old
// first-match-wins NewSelfHarnessClient: when a provider dies, the
// next healthy keyed provider answers instead of degrading to
// needs_human.
//
// Falls back to ErrNoLLMAvailable if no LLM is reachable through
// either mechanism.
func (o *Orchestrator) ensureLLMSelector() LLMSelector {
	if o.selector != nil {
		return o.selector
	}
	// Secondary: the default failover chain. If no key is set, the
	// selector returns ErrNoLLMAvailable on Select — same behavior
	// as the primary nil-injection path.
	failover, _ := DefaultFailoverClient()
	return NewOSINTSelector(failover)
}

// ensurePersonaRegistry lazily constructs the PersonaRegistry from
// the compiled default personas (plus any Markdown overrides from
// $DARK_JUDGE_PERSONAS_DIR). The registry is read-only after this
// returns.
//
// Phase 9 alpha.20 Chunk 8.2: when WithV4AlphaPersonas(true) was
// called, the registry also merges the 6 v4-new personas from
// v4alpha/judge.PersonaContent. The 14-persona surface (8 v2 + 6
// v4-new) closes Phase 8 e2e critical finding #2 (row 2323 — T3).
//
// On construction error (e.g., missing judge-logical fallback), the
// error is returned to the caller; the registry is NOT cached on
// error so the next call retries.
func (o *Orchestrator) ensurePersonaRegistry() (*PersonaRegistry, error) {
	if o.personaRegistry != nil {
		return o.personaRegistry, nil
	}
	r, err := NewPersonaRegistry(RegistryOptions{
		IncludeMarkdownOverrides: true,
		IncludeV4Alpha:          o.includeV4AlphaPersonas,
	})
	if err != nil {
		return nil, err
	}
	o.personaRegistry = r
	return r, nil
}

// WithV4AlphaPersonas controls whether the lazy PersonaRegistry
// includes the 6 v4-new personas from v4alpha/judge (Phase 9 alpha.20
// Chunk 8.2). When enabled, dark_memory_judge_list_personas returns
// 14 personas (8 v2 compiled + 6 v4alpha) with Source discriminator
// "v4alpha" for the v4-new entries.
//
// Default false → operator opts in. The v3 binary's legacy_main.go
// enables it at boot so the persona registry surface matches what
// the v4alpha Server advertises.
//
// Nil-safe: when called with enabled=false, the registry behaves as
// the legacy 8-person compiled set. Idempotent — calling multiple
// times with the same value is a no-op.
func (o *Orchestrator) WithV4AlphaPersonas(enabled bool) *Orchestrator {
	o.includeV4AlphaPersonas = enabled
	return o
}

// V4AlphaPersonasEnabled reports whether the lazy registry will
// include v4alpha personas when constructed. Returns the value set
// by WithV4AlphaPersonas (default false).
func (o *Orchestrator) V4AlphaPersonasEnabled() bool {
	return o.includeV4AlphaPersonas
}

// ensurePersonaBuilder lazily returns the JudgePromptBuilder backed by
// the orchestrator's PersonaRegistry. Errors from registry
// construction are propagated.
func (o *Orchestrator) ensurePersonaBuilder() (*JudgePromptBuilder, error) {
	if o.personaBuilder != nil {
		return o.personaBuilder, nil
	}
	r, err := o.ensurePersonaRegistry()
	if err != nil {
		return nil, err
	}
	o.personaBuilder = NewJudgePromptBuilder(r)
	return o.personaBuilder, nil
}

// PersonaRegistry returns the orchestrator's PersonaRegistry (lazy-initialized).
// Useful for tests that want to inspect the registry.
func (o *Orchestrator) PersonaRegistry() (*PersonaRegistry, error) {
	return o.ensurePersonaRegistry()
}

// fieldError carries the offending field name AND the sentinel it
// wraps, so the tools layer (ToToolError) can populate the structured
// ToolError.Field field path for the operator. F35 wire-propagation.
//
// The error string is preserved for backward-compat fallback with
// log scrapers that grep on `Errorf("%w: %s is required", ...)`, but
// callers SHOULD errors.As(err, &fieldError{}) instead of string-parsing.
type fieldError struct {
	store error
	Field string
}

func (e *fieldError) Error() string {
	if e.Field == "" {
		return e.store.Error()
	}
	return e.store.Error() + ": field=" + e.Field
}

func (e *fieldError) Unwrap() error { return e.store }

// errMissingField produces a structured error that carries the field
// name. The tools layer extracts it via errors.As and populates
// ToolError.Field so the harness's error path renders a precise
// fix-up hint instead of a generic message.
func errMissingField(field string) error {
	return &fieldError{store: store.ErrInvalidArgument, Field: field}
}

// emitVLP fires a VLP event against a session. Best-effort: no VLP
// wired (uc nil), no session_id, or state-already-advanced
// (ErrInvalidTransition) are all silent no-ops. Other errors are
// logged but never fail the caller — VLP is a companion, not a gate.
// Callers use this after their data-plane operations succeed so the
// vibe-loop state stays in sync without requiring a separate harness
// call to vlp_handle_event.
func (o *Orchestrator) emitVLP(ctx context.Context, sessionID, actor string, event vlp.Event) {
	o.emitVLPWithVerdict(ctx, sessionID, actor, event, vlp.VerdictUnknown)
}

// emitVLPWithVerdict is emitVLP with a verdict payload (for drift_log).
func (o *Orchestrator) emitVLPWithVerdict(ctx context.Context, sessionID, actor string, event vlp.Event, verdict vlp.Verdict) {
	if o.vlpUC == nil || sessionID == "" {
		return
	}
	wc := store.WriteContext{
		Actor:     actor,
		SessionID: sessionID,
		WritePath: "emitVLP:" + event.String(),
	}
	_, err := o.vlpUC.HandleEvent(ctx, wc, sessionID, event, verdict, "")
	if err != nil {
		// ErrInvalidTransition means the harness already advanced the
		// VLP manually — not an error, just a no-op.
		var invalidTransition vlp.ErrInvalidTransition
		if errors.As(err, &invalidTransition) {
			return
		}
		// Other errors (store failures, missing session, etc.) are
		// logged but don't fail the caller. VLP is best-effort.
		log.Printf("dark-mem-mcp: emitVLP session=%s event=%s verdict=%s: %v",
			sessionID, event, verdict, err)
	}
}

// verdictToVLP maps a publish verdict string to the VLP Verdict enum.
func verdictToVLP(v string) vlp.Verdict {
	switch v {
	case "aligned":
		return vlp.VerdictAligned
	case "drift_detected":
		return vlp.VerdictDriftDetected
	case "needs_human":
		return vlp.VerdictNeedsHuman
	default:
		return vlp.VerdictUnknown
	}
}

// loadPubkeysFromDir reads every *.pubkey file in dir and returns
// them as ed25519.PublicKey values. Each file is the raw 32-byte
// ed25519 public key (matches dark-cli's templates/trust/*.pubkey
// format). Files that are not exactly 32 bytes or that cannot be
// read are logged and skipped. Files whose basename starts with a
// dot (.) are skipped (operators can disable a root by renaming it).
//
// On directory-not-found error, returns (nil, nil) — caller treats
// "no roots dir" as "no roots configured" (fail closed if the gate
// is otherwise configured).
func loadPubkeysFromDir(dir string) ([]ed25519.PublicKey, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("readdir %s: %w", dir, err)
	}
	var out []ed25519.PublicKey
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if !strings.HasSuffix(name, ".pubkey") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			log.Printf("dark-mem-mcp: skip trust root %s: read: %v", name, err)
			continue
		}
		if len(b) != ed25519.PublicKeySize {
			log.Printf("dark-mem-mcp: skip trust root %s: got %d bytes, want %d", name, len(b), ed25519.PublicKeySize)
			continue
		}
		out = append(out, ed25519.PublicKey(b))
	}
	return out, nil
}
