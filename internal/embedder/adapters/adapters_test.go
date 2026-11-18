package adapters

import (
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/embedder"
)

// TestTheAdaptersAreRegistered is the test that pins the ROOT bug.
//
// MEASURED el 2026-11-18: every adapter registers its factory in its
// own init(), and an init() only runs if the package is IMPORTED.
// Nothing imported them. So tryKind() could never find a factory,
// whatever DARK_MEMORY_EMBEDDER said, and every server booted with
// the disabled stub (kind="none", dim=0): the hybrid retrieval axis
// (Mode=vector|rrf) was dead code in every deployment.
//
// This package exists to make the import explicit, and this test
// exists to make the omission impossible to repeat silently.
func TestTheAdaptersAreRegistered(t *testing.T) {
	for _, kind := range []string{
		embedder.KindOllama,
		embedder.KindOpenAI,
		embedder.KindVoyage,
	} {
		if !embedder.AdapterRegistered(kind) {
			t.Errorf("the %q adapter is NOT registered: importing this package must be enough to make it reachable", kind)
		}
	}
}

// TestONNXIsNotRegisteredByDefault pins the cgo decision.
//
// The ONNX adapter needs github.com/yalue/onnxruntime_go, which needs
// cgo and therefore a C toolchain. Importing it unconditionally would
// break the build on every machine without a C compiler — including the
// CI runners and the release builders, which have none. MEASURED on
// 2026-11-18: the parent module's own build fails with "build
// constraints exclude all Go files in
// github.com/yalue/onnxruntime_go" for exactly this reason.
//
// It is deliberately NOT behind a build tag either: that would need the
// dependency added to cmd/dark-mem-mcp's own go.mod and go.sum, and
// the resulting binary could not be built or verified anywhere in this
// project's CI — a path nobody has ever compiled. An operator who
// wants the local ONNX adapter adds the import themselves and owns that
// build.
func TestONNXIsNotRegisteredByDefault(t *testing.T) {
	if embedder.AdapterRegistered(embedder.KindONNX) {
		t.Error("the onnx adapter is registered in the default build: it needs cgo and would break the build on machines without a C toolchain")
	}
}
