// Package agentbootstrap - render_test.go: tests for template-aware
// content rendering (render.go).
//
// What this verifies:
//
//   - Every embedded content file that contains template markers
//     renders WITHOUT error under a realistic BootstrapData. A broken
//     template (typo in a field name, missing key, bad syntax) is a
//     programmer error caught here — the production serve path would
//     otherwise return an error on the first resources/read.
//   - Rendered output contains the injected values (so a template that
//     silently drops a placeholder is caught).
//   - Files WITHOUT template markers are served verbatim (override
//     backward-compat).
package agentbootstrap

import (
	"io/fs"
	"strings"
	"testing"
)

// testData is a realistic BootstrapData used by the render tests.
// Values are deliberately different from the live sources of truth so
// the test can assert the template actually INJECTED them (e.g. if
// BootstrapVersion were 3, a template hardcoding 3 would pass the
// assertion below — using a sentinel catches that).
func testData() BootstrapData {
	return BootstrapData{
		Version:          "999.0.0-test",
		BootstrapVersion: 99,
		CanonicalTools:   123,
		SchemaVersion:    77,
		TotalResources:   10,
		NamespaceCount:   17,
		NamespaceCounts: map[string]int{
			"PROJECT": 1, "SESSION": 4, "RESEARCH": 3, "AGENT_BOOTSTRAP": 3,
			"VIBE": 4, "CONTEXT": 4, "AGENT_MEMORY": 10, "MINDSET": 1,
			"DELEGATION": 1, "JUDGE": 3, "POLICY": 2, "OBSERVABILITY": 4,
			"ERROR_OBS": 4, "ADMIN": 3, "L6-VLP": 1, "EMBEDDER": 1,
		},
	}
}

// TestRender_EveryEmbeddedFileRenders asserts that every embedded
// content file renders without error under a sentinel BootstrapData,
// and that templated files actually contain the injected values.
//
// This is the drift guard for the template contract: if someone adds
// a {{.Field}} typo to SYSTEM_PROMPT.md or forgets a required key,
// this test fails at CI time, not at first resources/read in
// production.
func TestRender_EveryEmbeddedFileRenders(t *testing.T) {
	data := testData()

	files := []string{
		"SYSTEM_PROMPT.md",
		"COMPATIBILITY_MATRIX.md",
		"install/claude-desktop.md",
		"install/claude-code.md",
		"install/opencode.md",
		"install/cline.md",
		"install/cursor.md",
		"install/continue.md",
		"companions/dark-research.md",
		"companions/[FUTURE-MCP-N].md",
	}

	for _, path := range files {
		out, err := Render(embedded, path, data)
		if err != nil {
			t.Errorf("Render(%s): %v", path, err)
			continue
		}
		// Every templated file must show the injected version —
		// a file that hardcodes the version would not contain the
		// sentinel and the test catches the drift.
		if !strings.Contains(out, data.Version) {
			t.Errorf("Render(%s): rendered output missing injected version %q (file may hardcode the version)", path, data.Version)
		}
		if !strings.Contains(out, "Bootstrap version 99") {
			t.Errorf("Render(%s): rendered output missing injected BootstrapVersion (file may hardcode it)", path)
		}
	}
}

// TestRender_SYSTEM_PROMPT_KeyValues asserts the SYSTEM_PROMPT
// template injects the canonical tool count, schema version,
// namespace count and per-namespace counts.
func TestRender_SYSTEM_PROMPT_KeyValues(t *testing.T) {
	data := testData()
	out, err := Render(embedded, "SYSTEM_PROMPT.md", data)
	if err != nil {
		t.Fatalf("Render(SYSTEM_PROMPT.md): %v", err)
	}

	checks := []string{
		"MCP schema v77", // SchemaVersion injected
		"**123 tools**",  // CanonicalTools injected
		"Total: **123 canonical tools**",
		"across 17 namespaces",   // NamespaceCount injected
		"| `PROJECT` | 1 |",      // NamespaceCounts injected (PROJECT)
		"| `ERROR_OBS` | 4 |",    // NamespaceCounts injected (ERROR_OBS)
		"## 2. The 10 resources", // TotalResources injected
	}
	for _, want := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("SYSTEM_PROMPT.md rendered output missing %q", want)
		}
	}
	// The sentinel version must appear in the header.
	if !strings.Contains(out, "v999.0.0-test+") {
		t.Errorf("SYSTEM_PROMPT.md rendered output missing sentinel version")
	}
}

// TestRender_NoMarkersServedVerbatim asserts that content without
// template markers is returned byte-for-byte — the operator override
// backward-compat path.
func TestRender_NoMarkersServedVerbatim(t *testing.T) {
	// "{{" would trigger template parsing; use a file with NO markers.
	plain := []byte("# Plain override doc\n\nNo markers here.\n")
	out, err := RenderBytes(plain, "override.md", testData())
	if err != nil {
		t.Fatalf("RenderBytes(plain): %v", err)
	}
	if out != string(plain) {
		t.Errorf("plain content altered: got %q, want %q", out, string(plain))
	}

	// And a marker-containing override IS rendered (operator can inject
	// live values into their own docs).
	marked := []byte("Version: {{.Version}}")
	out2, err := RenderBytes(marked, "override-tmpl.md", testData())
	if err != nil {
		t.Fatalf("RenderBytes(marked): %v", err)
	}
	if !strings.Contains(out2, "999.0.0-test") {
		t.Errorf("marked override not rendered: %q", out2)
	}
}

// TestRender_UnknownKeyFails asserts that a template referencing a
// field that does not exist on BootstrapData fails loudly (missingkey=error).
func TestRender_UnknownKeyFails(t *testing.T) {
	marked := []byte("{{.DoesNotExist}}")
	if _, err := RenderBytes(marked, "bad.md", testData()); err == nil {
		t.Fatal("expected error for unknown template key, got nil")
	}
}

// --- Wave 3 mutation-killing coverage (2026-08-07) ---
// render.go.0 removes the fs.ReadFile error return; render.go.2 removes
// the template.Parse error return. Both are REAL error paths that the
// original suite never exercised (it only rendered existing files and
// the missingkey=error path, which is an Execute error, not Parse).

func TestMT_Render_ReadErrorFails(t *testing.T) {
	if _, err := Render(missingFS{}, "does-not-exist.md", testData()); err == nil {
		t.Fatal("expected error reading missing path, got nil")
	}
}

func TestMT_RenderBytes_ParseErrorFails(t *testing.T) {
	// Unclosed action = parse error (not an unknown-key error).
	marked := []byte("{{.Field")
	if _, err := RenderBytes(marked, "broken.md", testData()); err == nil {
		t.Fatal("expected parse error for unclosed template action, got nil")
	}
}

// missingFS is an fs.FS whose Open always fails (forces fs.ReadFile error).
type missingFS struct{}

func (missingFS) Open(name string) (fs.File, error) { return nil, fs.ErrNotExist }

// assertAllFilesExist is a helper the render test relies on: every
// file in the embedded fs must be covered by the template render list
// above (a new .md dropped into data/ without a render test is
// unchecked).
func TestRender_EmbeddedFileListComplete(t *testing.T) {
	covered := map[string]bool{
		"SYSTEM_PROMPT.md": true, "COMPATIBILITY_MATRIX.md": true,
		"install/claude-desktop.md": true, "install/claude-code.md": true,
		"install/opencode.md": true, "install/cline.md": true,
		"install/cursor.md": true, "install/continue.md": true,
		"companions/dark-research.md": true, "companions/[FUTURE-MCP-N].md": true,
	}
	err := fs.WalkDir(embedded, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !covered[p] {
			t.Errorf("embedded file %q not covered by TestRender_EveryEmbeddedFileRenders", p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir: %v", err)
	}
}
