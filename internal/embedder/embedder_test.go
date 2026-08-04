// Package embedder — embedder_test.go: the public contract tests for
// the embedding factory and env-reading helper.
//
// Tests in this file are deliberately independent of the actual
// adapter implementations (openai.go / onnx.go) — those adapters land
// in follow-up commits and get their own test files. The tests here
// cover ONLY the dispatch contract: what kind wins for each env var,
// what the factory returns for unknown values, what graceful fallback
// means in practice.
//
// Run with `go test ./internal/embedder/...`.
package embedder

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestDefaultKind_RecognizedValues locks the public dispatch table.
// Each recognized env-var value MUST map to its canonical kind string.
// If you add a new adapter, ADD A TEST CASE in this table; do not
// silently extend DefaultKind.
func TestDefaultKind_RecognizedValues(t *testing.T) {
	cases := []struct {
		env  string
		want string
		desc string
	}{
		{"", KindNone, "empty defaults to none"},
		{"none", KindNone, "explicit none"},
		{"NONE", KindNone, "case-insensitive"},
		{" None ", KindNone, "whitespace trimmed"},
		{"openai", KindOpenAI, "explicit openai"},
		{"OPENAI", KindOpenAI, "case-insensitive openai"},
		{"  OpenAI  ", KindOpenAI, "whitespace + case-insensitive openai"},
		{"onnx", KindONNX, "explicit onnx"},
		{"ONNX", KindONNX, "case-insensitive onnx"},
		{"onnx-local", KindONNX, "alias: onnx-local"},
		{"local", KindONNX, "alias: local"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.env+"_"+tc.desc, func(t *testing.T) {
			t.Setenv("DARK_MEMORY_EMBEDDER", tc.env)
			got := DefaultKind()
			if got != tc.want {
				t.Fatalf("DefaultKind() = %q, want %q (env=%q)", got, tc.want, tc.env)
			}
		})
	}
}

// TestDefaultKind_FailSafeOnTypo locks the failure mode: unknown
// values must NOT silently upgrade to a paid / network-dependent
// adapter. Defaulting to none is the contract.
func TestDefaultKind_FailSafeOnTypo(t *testing.T) {
	typos := []string{
		"openai-beta", // never a thing
		"cohere",
		"voyage",
		"anthropic",
		"ONNX-FULL",
		"open ai", // space inside, not at edges
		"yes",
		"1",
	}
	for _, typo := range typos {
		typo := typo
		t.Run("typo="+typo, func(t *testing.T) {
			t.Setenv("DARK_MEMORY_EMBEDDER", typo)
			if got := DefaultKind(); got != KindNone {
				t.Fatalf("DefaultKind(%q) = %q, must default to %q on typo", typo, got, KindNone)
			}
		})
	}
}

// TestFactory_DefaultReturnsNone confirms the empty / unset / "none"
// paths all yield a working disabled embedder. The user-visible
// behavior: the factory never errors on a disabled config; the
// embedder simply reports ErrDisabled when Embed is called.
func TestFactory_DefaultReturnsNone(t *testing.T) {
	t.Setenv("DARK_MEMORY_EMBEDDER", "")
	got, err := Factory(context.Background())
	if err != nil {
		t.Fatalf("Factory: unexpected err on unset env: %v", err)
	}
	if got.Kind() != KindNone {
		t.Fatalf("Factory: Kind = %q, want %q", got.Kind(), KindNone)
	}
	if got.Dim() != 0 {
		t.Fatalf("Factory: Dim = %d, want 0 for disabled", got.Dim())
	}
	if _, err := got.Embed(context.Background(), []string{"hello"}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("Embed: want ErrDisabled, got %v", err)
	}
	if err := got.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestFactory_OpenAIAndONNX_DegradeGracefully locks the integration
// promise: until the openai.go / onnx.go adapters land (in a
// follow-up commit), requesting them via env returns the none stub
// instead of an error. Operators who set the env early do not get a
// hard failure today; they get BM25 only with a silent fallback.
// This test will be REPLACED with TestFactory_OpenAI / TestFactory_ONNX
// when those packages ship, so the operators who do configure them
// get real vector coverage.
func TestFactory_OpenAIAndONNX_DegradeGracefully(t *testing.T) {
	for _, kind := range []string{KindOpenAI, KindONNX, "openai", "ONNX"} {
		kind := kind
		t.Run("kind="+kind, func(t *testing.T) {
			t.Setenv("DARK_MEMORY_EMBEDDER", kind)
			got, err := Factory(context.Background())
			if err != nil {
				// Per the Factory contract, no err on the public path —
				// but we'd log the underlying reason during boot if
				// callers want to debug.
				t.Logf("Factory: %v (acceptable: graceful fallback to none)", err)
			}
			if got == nil {
				t.Fatalf("Factory: got nil embedder")
			}
			if got.Kind() != KindNone {
				// This is the soft-fallback assertion: until the
				// adapters ship, the dispatch falls back to none
				// rather than failing closed.
				t.Fatalf("Factory: Kind = %q, want %q (adapters pending)", got.Kind(), KindNone)
			}
		})
	}
}

// TestNewSync_KindAndDimPassthrough confirms the Sync wrapper
// doesn't change observable metadata; only Embed / Close acquire
// the mutex.
func TestNewSync_KindAndDimPassthrough(t *testing.T) {
	inner := None()
	s := NewSync(inner)
	if s.Kind() != inner.Kind() {
		t.Fatalf("Sync.Kind = %q, want %q", s.Kind(), inner.Kind())
	}
	if s.Dim() != inner.Dim() {
		t.Fatalf("Sync.Dim = %d, want %d", s.Dim(), inner.Dim())
	}
}

// TestNewSync_ConcurrentEmbed guards the post-condition that future
// ONNX adapters (single-threaded by default) can be wrapped without
// callers having to add their own locking. The test runs N goroutines
// calling Embed concurrently and asserts no data race (run with
// `-race` to detect); the disabled embedder's Embed is pure fail,
// so 100% of the goroutines see ErrDisabled in a stable order.
func TestNewSync_ConcurrentEmbed(t *testing.T) {
	s := NewSync(None())
	const N = 64
	errs := make([]error, N)
	done := make(chan struct{})
	for i := 0; i < N; i++ {
		i := i
		go func() {
			_, e := s.Embed(context.Background(), []string{"x"})
			errs[i] = e
			done <- struct{}{}
		}()
	}
	for i := 0; i < N; i++ {
		<-done
	}
	for i, e := range errs {
		if !errors.Is(e, ErrDisabled) {
			t.Fatalf("goroutine %d: err = %v, want ErrDisabled", i, e)
		}
	}
}

// TestNormalize covers the trim+lowercase helper used by DefaultKind.
// Most of the trim+lowercase behavior is exercised by
// TestDefaultKind_RecognizedValues; this test adds explicit edge
// cases that table-driven values don't reach.
func TestNormalize(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"   ", ""},
		{"\t \t", ""},
		{"hello", "hello"},
		{"HELLO", "hello"},
		{"  MixedCase  ", "mixedcase"},
		{"trail   ", "trail"},
		{"   lead", "lead"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run("in="+strings.ReplaceAll(tc.in, " ", "_"), func(t *testing.T) {
			got := normalize(tc.in)
			if got != tc.want {
				t.Fatalf("normalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
