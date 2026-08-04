// Package onnx — tests for the bundled-ONNX adapter.
//
// On non-Windows / non-Linux-amd64 / non-Darwin-arm64 platforms
// (i.e., the unsupported-platform stub), New() must return
// embedder.ErrDisabled. The integration tests (real model load +
// inference) require a working ONNX runtime and the bundled .dll/
// .so/.dylib; they're skipped in this file and covered manually on
// each platform's CI run.

package onnx

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/embedder"
)

// TestNew_UnsupportedPlatform verifies that New() returns
// embedder.ErrDisabled (wrapped with platform context) on builds
// without a bundled runtime. On supported platforms (the current
// CI matrix), this test is skipped.
func TestNew_UnsupportedPlatform(t *testing.T) {
	if !unsupportedPlatform {
		t.Skipf("skipping: platform %s/%s has bundled runtime", runtime.GOOS, runtime.GOARCH)
	}
	_, err := New(Options{})
	if err == nil {
		t.Fatal("New on unsupported platform: expected error, got nil")
	}
	if !errors.Is(err, embedder.ErrDisabled) {
		t.Fatalf("expected ErrDisabled, got %v", err)
	}
	if !strings.Contains(err.Error(), runtime.GOOS) {
		t.Errorf("error should mention GOOS=%s, got: %v", runtime.GOOS, err)
	}
}

// TestBuildConfig_Defaults verifies zero-value Options produce the
// canonical defaults (compile-time pinned SHA, 256 max seq len,
// batch size 32).
func TestBuildConfig_Defaults(t *testing.T) {
	cfg := buildConfig(Options{})
	if cfg.ExpectedSHA256 != DefaultExpectedSHA256 {
		t.Errorf("ExpectedSHA256=%s, want %s", cfg.ExpectedSHA256, DefaultExpectedSHA256)
	}
	if cfg.MaxSeqLen != DefaultMaxSeqLen {
		t.Errorf("MaxSeqLen=%d, want %d", cfg.MaxSeqLen, DefaultMaxSeqLen)
	}
	if cfg.BatchSize != DefaultBatchSize {
		t.Errorf("BatchSize=%d, want %d", cfg.BatchSize, DefaultBatchSize)
	}
}

// TestBuildConfig_Overrides verifies Options overrides take effect.
func TestBuildConfig_Overrides(t *testing.T) {
	cfg := buildConfig(Options{
		ExpectedSHA256: "abc123",
		MaxSeqLen:      128,
		BatchSize:      16,
	})
	if cfg.ExpectedSHA256 != "abc123" {
		t.Errorf("ExpectedSHA256=%s, want abc123", cfg.ExpectedSHA256)
	}
	if cfg.MaxSeqLen != 128 {
		t.Errorf("MaxSeqLen=%d, want 128", cfg.MaxSeqLen)
	}
	if cfg.BatchSize != 16 {
		t.Errorf("BatchSize=%d, want 16", cfg.BatchSize)
	}
}

// TestCacheRoot_Priority verifies the lookup chain:
// opts.DarkHome > $DARK_HOME > ~/.dark-agents.
func TestCacheRoot_Priority(t *testing.T) {
	// 1. optOverride wins.
	override := "/tmp/test-override"
	if got := cacheRoot(override); got != override {
		t.Errorf("optOverride=%s, want %s", got, override)
	}
	// 2. $DARK_HOME wins when optOverride empty.
	t.Setenv("DARK_HOME", "/tmp/dark-home-env")
	if got := cacheRoot(""); got != "/tmp/dark-home-env" {
		t.Errorf("DARK_HOME=%s, want /tmp/dark-home-env", got)
	}
	// 3. Falls through to ~/.dark-agents when both empty.
	t.Setenv("DARK_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows uses USERPROFILE
	if got := cacheRoot(""); !strings.HasSuffix(got, ".dark-agents") {
		t.Errorf("fallback=%s, want suffix .dark-agents", got)
	}
}

// TestLoadVocab_Valid covers the happy-path vocab loader using a
// minimal in-memory vocab (the bundled one is too large to embed
// in this test).
func TestLoadVocab_Valid(t *testing.T) {
	in := "[PAD]\n[UNK]\n[CLS]\n[SEP]\nhello"
	v, err := loadVocab([]byte(in))
	if err != nil {
		t.Fatalf("loadVocab: %v", err)
	}
	if v.id["[PAD]"] != 0 || v.id["[UNK]"] != 1 || v.id["[CLS]"] != 2 || v.id["[SEP]"] != 3 || v.id["hello"] != 4 {
		t.Errorf("vocab mapping wrong: %+v", v.id)
	}
}

// TestLoadVocab_Duplicate covers the duplicate-token detection.
func TestLoadVocab_Duplicate(t *testing.T) {
	in := "a\nb\na\n"
	_, err := loadVocab([]byte(in))
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

// TestLoadVocab_Empty covers the empty-line detection.
func TestLoadVocab_Empty(t *testing.T) {
	in := "a\n\nb\n"
	_, err := loadVocab([]byte(in))
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty-line error, got %v", err)
	}
}

// TestBasicTokenize covers the BERT-style basic tokenizer.
func TestBasicTokenize(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"hello world", []string{"hello", "world"}},
		{"don't run", []string{"don", "'", "t", "run"}},
		{"RUN", []string{"run"}}, // lowercased
	}
	for _, tc := range cases {
		got := basicTokenize(tc.in)
		if !equalStrings(got, tc.want) {
			t.Errorf("basicTokenize(%q)=%v, want %v", tc.in, got, tc.want)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestMeanPool covers the mean-pool reduction with attention mask.
func TestMeanPool(t *testing.T) {
	// hidden = [1, 2, 3, 4, 5, 6] (1 token, dim=3 ... wait that's 2 tokens of dim 3)
	// Simpler: 1 token, dim=3, mask all-1.
	hidden := []float32{1, 2, 3}
	mask := []int64{1}
	out := meanPool(hidden, 0, 1, mask, 3)
	for i, v := range []float32{1, 2, 3} {
		if out[i] != v {
			t.Errorf("out[%d]=%f, want %f", i, out[i], v)
		}
	}
}

// TestMeanPool_Masked covers the mask=0 case (skip token).
func TestMeanPool_Masked(t *testing.T) {
	// 2 tokens of dim=2. First token is masked (0); second contributes.
	hidden := []float32{99, 99, 4, 6}
	mask := []int64{0, 1}
	out := meanPool(hidden, 0, 2, mask, 2)
	if out[0] != 4 || out[1] != 6 {
		t.Errorf("meanPool with mask: got %v, want [4 6]", out)
	}
}

// TestL2Normalize verifies the L2 normalization.
func TestL2Normalize(t *testing.T) {
	v := embedder.Vec{3, 4} // norm=5
	out := l2Normalize(v)
	if len(out) != 2 {
		t.Fatalf("len=%d, want 2", len(out))
	}
	// 3/5, 4/5.
	if !floatEq(out[0], 0.6, 1e-5) || !floatEq(out[1], 0.8, 1e-5) {
		t.Errorf("l2Normalize(3,4)=%v, want [0.6 0.8]", out)
	}
}

// TestL2Normalize_Zero covers the zero-vector pass-through.
func TestL2Normalize_Zero(t *testing.T) {
	v := embedder.Vec{0, 0, 0}
	out := l2Normalize(v)
	for i, x := range out {
		if x != 0 {
			t.Errorf("zero vector out[%d]=%f, want 0", i, x)
		}
	}
}

func floatEq(a, b, eps float32) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < eps
}

// TestSqrt covers the small sqrt helper.
func TestSqrt(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{0, 0},
		{4, 2},
		{9, 3},
		{2, 1.4142135623730951},
	}
	for _, tc := range cases {
		got := sqrt(tc.in)
		if abs(got-tc.want) > 1e-6 {
			t.Errorf("sqrt(%v)=%v, want %v", tc.in, got, tc.want)
		}
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// Compile-time guard for the ErrSessionClosed sentinel usage.
var _ error = ErrSessionClosed

// TestEmbed_MultipleNewCalls is the regression test for the
// process-singleton ONNX Runtime environment: New() must be callable
// multiple times in the same process (each call creates its own
// session; the environment inits exactly once). Before the envOnce
// fix, the second New() failed with "onnxruntime has already been
// initialized" — this is what CI caught (the suite runs all tests in
// one process, so TestEmbedAfterClose + TestEmbed_RealModel + this
// test all call New() back to back).
func TestEmbed_MultipleNewCalls(t *testing.T) {
	if unsupportedPlatform {
		t.Skipf("skipping: platform %s/%s unsupported", runtime.GOOS, runtime.GOARCH)
	}
	if runtime.GOOS == "windows" {
		t.Skipf("skipping on Windows: yalue runtime holds a DLL handle that prevents t.TempDir cleanup")
	}

	// Two independent adapters in the same process, each with its own
	// cache dir. Both must construct; the second must NOT trip the
	// "already been initialized" singleton guard.
	a1, err := New(Options{DarkHome: t.TempDir()})
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	defer a1.Close()

	a2, err := New(Options{DarkHome: t.TempDir()})
	if err != nil {
		t.Fatalf("second New (same process): %v", err)
	}
	defer a2.Close()

	// Both sessions must actually run inference.
	for i, a := range []embedder.Embedder{a1, a2} {
		vecs, err := a.Embed(context.Background(), []string{"hello"})
		if err != nil {
			t.Fatalf("adapter %d Embed: %v", i, err)
		}
		if len(vecs) != 1 || len(vecs[0]) != DefaultDim {
			t.Fatalf("adapter %d: got %d vecs dim %d, want 1 vec dim %d", i, len(vecs), len(vecs[0]), DefaultDim)
		}
	}
}

// TestEmbedAfterClose exercises the close-state error path.
// On unsupported platforms this is skipped (we never get an instance).
// On Windows the yalue runtime holds a DLL handle that prevents the
// test's t.TempDir() cleanup from succeeding; skip there too (the
// CI matrix runs Linux/macOS for the runtime tests).
func TestEmbedAfterClose(t *testing.T) {
	if unsupportedPlatform {
		t.Skipf("skipping: platform %s/%s unsupported", runtime.GOOS, runtime.GOARCH)
	}
	if runtime.GOOS == "windows" {
		t.Skipf("skipping on Windows: yalue runtime holds a DLL handle that prevents t.TempDir cleanup")
	}
	a, err := New(Options{DarkHome: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err = a.Embed(context.Background(), []string{"hi"})
	if !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("expected ErrSessionClosed, got %v", err)
	}
	// Double-close is a no-op.
	if err := a.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestEmbed_RealModel exercises the full ONNX inference pipeline:
// load model + libonnxruntime, create session, tokenize + embed a
// real input, verify L2-normalized output.
//
// Skipped on unsupported platforms + Windows (DLL lock; see above).
// CI Linux/macOS runs this end-to-end.
//
// First call is ~5s (model extract + vocab load + session create);
// subsequent calls are ~10ms per text on a modern CPU.
func TestEmbed_RealModel(t *testing.T) {
	if unsupportedPlatform {
		t.Skipf("skipping: platform %s/%s unsupported", runtime.GOOS, runtime.GOARCH)
	}
	if runtime.GOOS == "windows" {
		t.Skipf("skipping on Windows: yalue runtime holds a DLL handle that prevents t.TempDir cleanup")
	}
	a, err := New(Options{DarkHome: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer a.Close()

	vecs, err := a.Embed(context.Background(), []string{"hello world"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 1 {
		t.Fatalf("got %d vecs, want 1", len(vecs))
	}
	if len(vecs[0]) != DefaultDim {
		t.Errorf("dim=%d, want %d", len(vecs[0]), DefaultDim)
	}
	// Verify L2 norm ≈ 1.
	var sum float64
	for _, x := range vecs[0] {
		sum += float64(x) * float64(x)
	}
	if sum < 0.99 || sum > 1.01 {
		t.Errorf("L2 norm^2=%f, want ~1.0", sum)
	}
}
