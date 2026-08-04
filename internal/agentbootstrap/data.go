// Package agentbootstrap - data.go: the bootstrap content template
// context.
//
// Every self-bootstrap content file (SYSTEM_PROMPT.md,
// COMPATIBILITY_MATRIX.md, install/*.md, companions/*.md) is served
// through the SAME template context (BootstrapData). Values are
// DERIVED from the codebase's single sources of truth by the caller
// (internal/tools.BuildBootstrapData): tool counts come from
// canonicalToolOrder, the schema version from the compiled-in
// migrations, the MCP version from internal/version. Nothing in this
// package hardcodes a count or a version — that is the entire point:
// adding a tool to registry.go or a migration to the sqlite driver
// must automatically propagate to every bootstrap document.
//
// The template contract:
//
//   - Files that contain Go template markers ({{...}}) are parsed and
//     executed with BootstrapData at serve time (see render.go).
//   - Files WITHOUT markers (e.g. an operator's DARK_AGENT_BOOTSTRAP_DIR
//     override written in plain markdown) are served verbatim. This
//     keeps the operator override fully backward-compatible: plain
//     markdown overrides keep working, templated overrides are honored.
package agentbootstrap

// BootstrapVersion is the content version of the dark-agent bootstrap
// material. Bump ONLY when the SHAPE of the content changes in a way
// harnesses must react to (new resource groups, new sections, a
// changed template contract) — NOT when tool counts or versions move.
// Tool counts and versions are injected at serve time; they never
// require a bootstrap version bump.
//
// Pre-v2.6.0 had no version marker. v2.6.0 introduced "Bootstrap
// version 1", v2.11.0 bumped to 3 (the +2 came from the ERROR_OBS +
// DELEGATION namespace sections being added to the operating manual).
const BootstrapVersion = 3

// BootstrapData is the template context injected into every bootstrap
// content file at serve time. All fields are populated from live
// sources of truth by tools.BuildBootstrapData (see
// internal/tools/bootstrap_data.go); agentbootstrap only defines the
// shape so the template can stay agnostic of where the values come
// from.
type BootstrapData struct {
	// Version is the MCP server version (version.Resolve().Version).
	Version string

	// BootstrapVersion is the content version marker (above).
	BootstrapVersion int

	// CanonicalTools is len(canonicalToolOrder): the total number of
	// canonical dark_memory_* tools.
	CanonicalTools int

	// SchemaVersion is the highest compiled-in migration version.
	SchemaVersion int

	// TotalResources is agentbootstrap.TotalResources(): fixed
	// resources + install guides + companion docs.
	TotalResources int

	// NamespaceCount is the number of namespaces in the canonical
	// surface (len of the namespace grouping).
	NamespaceCount int

	// NamespaceCounts maps namespace name (e.g. "AGENT_MEMORY") to
	// the number of tools in that namespace. Rendered into the
	// namespace overview table of SYSTEM_PROMPT.md.
	NamespaceCounts map[string]int

	// BuildDate is an optional RFC3339 build timestamp. Empty when
	// the caller has no build info.
	BuildDate string
}
