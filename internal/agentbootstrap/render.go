// Package agentbootstrap - render.go: template-aware content serving.
//
// Serves bootstrap content files through a Go text/template when the
// file contains template markers ({{...}}), and verbatim otherwise.
//
// Why templates instead of static files:
//
//   - The old contract embedded static .md files with hardcoded tool
//     counts, namespace counts, schema versions and MCP versions.
//     Every release required manually editing N documents to keep them
//     in sync with internal/tools/registry.go, the migrations, and the
//     version package — a recurring source of drift (stale counts in
//     README, install guides, the operating manual).
//   - With templates, the counts/versions are injected at serve time
//     from the live sources of truth. Adding a tool to
//     canonicalToolOrder or a migration to the sqlite driver updates
//     every bootstrap document automatically. Zero manual sync.
//
// Override compatibility:
//
//   - DARK_AGENT_BOOTSTRAP_DIR overrides may contain plain markdown
//     files (no template markers). They are served verbatim — the
//     override contract is unchanged.
//   - Overrides that DO contain template markers are rendered with
//     the same BootstrapData as the embedded content, so operators can
//     inject the live values into their own documents.
package agentbootstrap

import (
	"bytes"
	"fmt"
	"io/fs"
	"text/template"
)

// Render reads the content file at path from fsys and returns it
// either rendered (if it contains Go template markers) or verbatim
// (if it does not).
//
// Rendering is deterministic and side-effect-free. A template parse
// or execute error is returned wrapped with the file path so the
// caller can surface exactly which document is broken; the embedded
// content is tested at build time (fs_test.go renders every embedded
// file), so a broken template is a programmer error caught by CI, not
// a runtime surprise.
func Render(fsys fs.FS, path string, data BootstrapData) (string, error) {
	raw, err := fs.ReadFile(fsys, path)
	if err != nil {
		return "", fmt.Errorf("agentbootstrap: read %s: %w", path, err)
	}
	return RenderBytes(raw, path, data)
}

// RenderBytes renders raw content with data. Exported so tests can
// exercise template rendering without constructing an fs.FS.
func RenderBytes(raw []byte, name string, data BootstrapData) (string, error) {
	if !bytes.Contains(raw, []byte("{{")) {
		return string(raw), nil
	}

	tmpl, err := template.New(name).Option("missingkey=error").Parse(string(raw))
	if err != nil {
		return "", fmt.Errorf("agentbootstrap: template %s: %w", name, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("agentbootstrap: render %s: %w", name, err)
	}
	return buf.String(), nil
}
