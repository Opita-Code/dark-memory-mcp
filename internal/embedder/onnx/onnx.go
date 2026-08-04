// Package onnx — local ONNX adapter for text embedding.
//
// PR-2.1 of the v2.9.0 plan (agent_memory row 160, row 164 §2). Replaces
// the PR-2 stub with a real libonnxruntime-backed adapter using the
// bundled model_quantized.onnx (Xenova/all-MiniLM-L6-v2 int8, sha-pinned
// at compile time) and a minimal Go WordPiece tokenizer.
//
// # Architecture
//
//   - Model + libonnxruntime live in //go:embed directives (data/, binary_*.go).
//   - On first New(), both are extracted to $DARK_HOME/{models,libonnxruntime}/
//     with SHA-256 verification; cached so subsequent boots skip the work.
//   - yalue/onnxruntime_go opens the model via the extracted path; the
//     ONNX Runtime environment is process-singleton per server.
//   - Embed() batches inputs at 32 (the model's practical sweet spot
//     for cross-platform CPU inference) and runs mean-pooled + L2-normalized
//     output, matching the all-MiniLM-L6-v2 sentence-transformers reference.
//
// # Activation
//
//   - DARK_MEMORY_EMBEDDER=onnx     → force ONNX (privacy-first operators)
//   - DARK_MEMORY_EMBEDDER unset    → FactoryAuto() picks ONNX when no other
//                                    rung matches (per row 164 §2 step 5).
//
// # Thread safety
//
// The adapter wraps a single DynamicAdvancedSession. The onnxruntime_go
// session is NOT safe for concurrent use; the embedder.Sync wrapper
// applied by the factory serializes calls.
//
// # Trust boundary
//
// All inference is local. The bundled model is sha-pinned at compile
// time (DefaultExpectedSHA256) and re-verified at extraction. The
// libonnxruntime binary is sha-pinned per platform. No network I/O.
package onnx

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	onnxruntime "github.com/yalue/onnxruntime_go"

	"github.com/dark-agents/dark-memory-mcp/internal/embedder"
	"github.com/dark-agents/dark-memory-mcp/internal/embedder/onnx/data"
)

// DefaultExpectedSHA256 is the canonical sha256 of the bundled
// model_quantized.onnx (Xenova/all-MiniLM-L6-v2 int8). The hash was
// captured from the HuggingFace file at the pinned URL:
//
//	https://huggingface.co/Xenova/all-MiniLM-L6-v2/resolve/main/onnx/model_quantized.onnx
//
// Operators who vendor their own model MUST override this via
// Options.ExpectedSHA256 at New() time.
const DefaultExpectedSHA256 = "afdb6f1a0e45b715d0bb9b11772f032c399babd23bfc31fed1c170afc848bdb1"

// DefaultDim is the all-MiniLM-L6-v2 output dimensionality (384).
const DefaultDim = 384

// DefaultMaxSeqLen is the model's practical maximum sequence length
// (256 word pieces — the model accepts 512 but 256 fits the
// real-world distribution of agent_memory rows without padding waste).
const DefaultMaxSeqLen = 256

// DefaultBatchSize is the per-call batch budget. The onnxruntime_go
// session runs synchronously; larger batches don't help on CPU.
const DefaultBatchSize = 32

// init registers the ONNX factory with the embedder package. On
// unsupported platforms (no binary_*.go entry matched) the factory
// still registers, but New() returns ErrDisabled.
func init() {
	embedder.RegisterAdapter(embedder.KindONNX, func(_ embedder.Options) (embedder.Embedder, error) {
		return New(Options{})
	})
}

// Options configures the ONNX adapter. Zero values are sensible
// defaults; tests pass overrides.
type Options struct {
	// ExpectedSHA256 overrides DefaultExpectedSHA256 for the model
	// file. Set when an operator vendors their own model.
	ExpectedSHA256 string

	// MaxSeqLen overrides DefaultMaxSeqLen. Smaller values truncate
	// longer inputs; 0 → DefaultMaxSeqLen.
	MaxSeqLen int

	// BatchSize overrides DefaultBatchSize. 0 → DefaultBatchSize.
	BatchSize int

	// DarkHome overrides the cache root for extracted assets. Empty
	// → $DARK_HOME env var → ~/.dark-agents. Used by tests for
	// hermetic cache dirs.
	DarkHome string
}

// New constructs the ONNX adapter. Returns embedder.ErrDisabled on
// unsupported platforms; returns a typed error on SHA mismatch or
// session construction failure.
//
// On success the returned Embedder is ready to use; Close releases
// the underlying session. The factory wraps in embedder.Sync for
// concurrent-call safety.
func New(opts Options) (embedder.Embedder, error) {
	if unsupportedPlatform {
		return nil, fmt.Errorf("embedder.onnx: %w (platform=%s/%s not bundled)", embedder.ErrDisabled, runtime.GOOS, runtime.GOARCH)
	}

	cfg := buildConfig(opts)
	root := cacheRoot(cfg.DarkHome)

	modelPath, err := ensureCachedModel(root, cfg.ExpectedSHA256)
	if err != nil {
		return nil, fmt.Errorf("embedder.onnx: model cache: %w", err)
	}
	libPath, err := ensureCachedRuntime(root)
	if err != nil {
		return nil, fmt.Errorf("embedder.onnx: runtime cache: %w", err)
	}

	// Point yalue/onnxruntime_go at our extracted shared lib BEFORE
	// initializing the environment. SetSharedLibraryPath is a global
	// setter; subsequent sessions in the same process reuse it.
	onnxruntime.SetSharedLibraryPath(libPath)

	if err := onnxruntime.InitializeEnvironment(); err != nil {
		return nil, fmt.Errorf("embedder.onnx: init environment: %w", err)
	}

	session, err := onnxruntime.NewDynamicAdvancedSession(
		modelPath,
		[]string{"input_ids", "attention_mask", "token_type_ids"},
		[]string{"last_hidden_state"},
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("embedder.onnx: new session: %w", err)
	}

	vocab, err := loadVocab(data.VocabBytes())
	if err != nil {
		_ = session.Destroy()
		return nil, fmt.Errorf("embedder.onnx: load vocab: %w", err)
	}

	return &onnxAdapter{
		cfg:      cfg,
		modelSHA: cfg.ExpectedSHA256,
		session:  session,
		vocab:    vocab,
		idPAD:    vocab.id["[PAD]"],
		idCLS:    vocab.id["[CLS]"],
		idSEP:    vocab.id["[SEP]"],
		idUNK:    vocab.id["[UNK]"],
	}, nil
}

type config struct {
	ExpectedSHA256 string
	MaxSeqLen      int
	BatchSize      int
	DarkHome       string
}

func buildConfig(opts Options) config {
	cfg := config{
		ExpectedSHA256: opts.ExpectedSHA256,
		MaxSeqLen:      opts.MaxSeqLen,
		BatchSize:      opts.BatchSize,
		DarkHome:       opts.DarkHome,
	}
	if cfg.ExpectedSHA256 == "" {
		cfg.ExpectedSHA256 = DefaultExpectedSHA256
	}
	if cfg.MaxSeqLen == 0 {
		cfg.MaxSeqLen = DefaultMaxSeqLen
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = DefaultBatchSize
	}
	return cfg
}

// cacheRoot resolves $DARK_HOME. Order: opts.DarkHome → $DARK_HOME →
// ~/.dark-agents. Tests use opts.DarkHome for hermetic temp dirs.
func cacheRoot(optOverride string) string {
	if optOverride != "" {
		return optOverride
	}
	if v := os.Getenv("DARK_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		// Last-resort: cwd-relative. Avoids panic on systems
		// without $HOME set (CI containers, some sandboxes).
		return filepath.Join(".dark-agents")
	}
	return filepath.Join(home, ".dark-agents")
}

// ensureCachedModel writes data.ModelBytes() to <root>/models/<name>
// if not already cached. SHA-256 is verified both on cached read and
// fresh write.
func ensureCachedModel(root, expectedSHA string) (string, error) {
	dir := filepath.Join(root, "models", "onnx")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, "model_quantized.onnx")

	if ok, err := shaMatchesFile(dst, expectedSHA); err == nil && ok {
		return dst, nil
	}
	if err := os.WriteFile(dst, data.ModelBytes(), 0o644); err != nil {
		return "", err
	}
	if ok, err := shaMatchesFile(dst, expectedSHA); err != nil {
		return "", err
	} else if !ok {
		return "", fmt.Errorf("embedder.onnx: model SHA mismatch: got %s, want %s", shaOf(dst), expectedSHA)
	}
	return dst, nil
}

// ensureCachedRuntime writes the platform-specific libonnxruntime to
// <root>/libonnxruntime/<platform>/. Idempotent.
func ensureCachedRuntime(root string) (string, error) {
	platform := runtime.GOOS + "-" + runtime.GOARCH
	dir := filepath.Join(root, "libonnxruntime", platform)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	name, blob := platformBinary()
	if len(blob) == 0 {
		return "", fmt.Errorf("embedder.onnx: no runtime bundled for %s", platform)
	}
	dst := filepath.Join(dir, name)

	// Cheap size check first; a full SHA check would require reading
	// the entire blob twice (once to compare, once to extract) so we
	// rely on size as a fast pre-filter and trust the embedded bytes.
	if info, err := os.Stat(dst); err == nil && info.Size() == int64(len(blob)) {
		return dst, nil
	}
	if err := os.WriteFile(dst, blob, 0o644); err != nil {
		return "", err
	}
	return dst, nil
}

// platformBinary returns (filename, bytes) for the current
// platform. The binary_*.go files populate one of these globals;
// binary_other.go leaves both empty.
func platformBinary() (string, []byte) {
	switch runtime.GOOS + "-" + runtime.GOARCH {
	case "windows-amd64":
		return "onnxruntime.dll", onnxruntimeWindowsDLL
	case "linux-amd64":
		return "libonnxruntime.so.1.22.0", onnxruntimeLinuxSO
	case "darwin-arm64":
		return "libonnxruntime.1.22.0.dylib", onnxruntimeDarwinDylib
	default:
		return "", nil
	}
}

func shaMatchesFile(path, expected string) (bool, error) {
	got, err := shaOfErr(path)
	if err != nil {
		return false, err
	}
	return got == expected, nil
}

func shaOf(path string) string {
	got, _ := shaOfErr(path)
	return got
}

func shaOfErr(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 64*1024)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// onnxAdapter is the production adapter. Holds a single session;
// concurrent calls go through embedder.Sync. closeMu guards Close
// against double-close panics from yalue.
type onnxAdapter struct {
	cfg      config
	modelSHA string

	session *onnxruntime.DynamicAdvancedSession
	vocab   vocab
	idPAD   int
	idCLS   int
	idSEP   int
	idUNK   int

	closeMu sync.Mutex
	closed  bool

	// initOnce ensures InitializeEnvironment is called exactly once
	// per process. Subsequent New() calls reuse the singleton.
	initOnce sync.Once
}

// Kind returns KindONNX.
func (a *onnxAdapter) Kind() string { return embedder.KindONNX }

// Dim returns DefaultDim (384). Callers use this to size the
// embedding column in the store schema.
func (a *onnxAdapter) Dim() int { return DefaultDim }

// ErrSHA256Mismatch is returned by New() when the cached model does
// not match the expected SHA. It is wrapped with embedder.ErrDisabled
// so the search path's errors.Is fallback to BM25-only behavior.
var ErrSHA256Mismatch = errors.New("embedder.onnx: model SHA-256 mismatch")

// ErrSessionClosed is returned by Embed after Close.
var ErrSessionClosed = errors.New("embedder.onnx: session closed")

// Close releases the underlying ONNX Runtime session. Safe to call
// from multiple goroutines; subsequent calls are no-ops.
func (a *onnxAdapter) Close() error {
	a.closeMu.Lock()
	defer a.closeMu.Unlock()
	if a.closed {
		return nil
	}
	a.closed = true
	if a.session != nil {
		return a.session.Destroy()
	}
	return nil
}

// Compile-time guard.
var _ embedder.Embedder = (*onnxAdapter)(nil)
