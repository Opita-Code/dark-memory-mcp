package embedder

import (
	"testing"
)

// TestDefaultKind_RecognizesOllama pins the fix that made the local
// Ollama adapter reachable at all.
//
// MEASURED el 2026-11-18: DefaultKind() recognized none|openai|onnx and
// nothing else, so "ollama" fell into the fail-safe default and became
// KindNone. The Ollama adapter existed, was written, had tests, and its
// init() registered the factory — but there was NO way to ask for it
// by name, and the only route left was the harness detector, which
// reaches Ollama LAST and maps opencode to openai.
func TestDefaultKind_RecognizesOllama(t *testing.T) {
	cases := map[string]string{
		"ollama":     KindOllama,
		"OLLAMA":     KindOllama,
		"  ollama  ": KindOllama,
		"openai":     KindOpenAI,
		"onnx":       KindONNX,
		"onnx-local": KindONNX,
		"local":      KindONNX,
		"none":       KindNone,
		"":           KindNone,
		// A typo must NOT silently pick a network-dependent adapter.
		"olama":   KindNone,
		"onnnx":   KindNone,
		"unknown": KindNone,
	}
	for in, want := range cases {
		t.Setenv("DARK_MEMORY_EMBEDDER", in)
		if got := DefaultKind(); got != want {
			t.Errorf("DefaultKind() with %q = %q, want %q", in, got, want)
		}
	}
}

// TestDefaultKind_UnsetIsNone pins that the zero state is the disabled
// stub: a server built without asking the environment must not start
// reaching a network-dependent adapter.
func TestDefaultKind_UnsetIsNone(t *testing.T) {
	t.Setenv("DARK_MEMORY_EMBEDDER", "")
	if got := DefaultKind(); got != KindNone {
		t.Errorf("DefaultKind() with the var unset = %q, want %q", got, KindNone)
	}
}

// TestFactoryAuto_LocalOllamaIsARealRung is the end-to-end check of the
// fix inside the boot ladder.
//
// The ladder used to be: manual override → harness preference →
// bundled ONNX → OPENAI_API_KEY → stub. With opencode detected, the
// harness rung asks for "openai", which fails without a key; the ONNX
// rung is not registered (it needs cgo); and the server fell to the
// stub. A local Ollama daemon — local, offline, no key, no egress —
// was unreachable from every angle.
//
// This test asserts the rung exists and is reachable by NAME, which is
// the contract an operator relies on.
func TestFactoryAuto_LocalOllamaIsARealRung(t *testing.T) {
	// The adapter is registered by internal/embedder/adapters, which the
	// binary imports. This package's own tests do NOT import it (that
	// would be a cycle), so the test asserts the CONTRACT of the
	// ladder, not the presence of the adapter.
	//
	// What matters here: asking for "ollama" is a valid, recognised
	// kind — i.e. the ladder does not treat it as a typo and silently
	// fall back to the disabled stub.
	t.Setenv("DARK_MEMORY_EMBEDDER", "ollama")
	if got := DefaultKind(); got != KindOllama {
		t.Fatalf("DefaultKind() = %q, want %q: the ladder must be able to ask for ollama by name", got, KindOllama)
	}
}
