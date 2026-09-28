// Package docs_index makes operator-facing documentation
// discoverable via the agent_memory FTS5 index.
//
// # Why this package exists (PRE-1 C2)
//
// Without it, `docs/*.md` are inert files. The LLM (or the
// harness) has to know in advance which file to read, and
// `recall` returns nothing for the operator's natural-language
// queries like "how do I do X" or "what's the schema for Y".
//
// With this package, every fresh install of dark-memory-mcp
// gets 5 pinned `kind=link, tag=doc:<name>, doc-index:v1`
// rows pointing at the most important operator-facing docs.
// The loadout protocol can then recall them by concept
// ("operator manual", "schema", "judge pipeline") and the
// harness can read the actual file when it needs the
// authoritative content.
//
// # Design (PRE-1 C2)
//
//   - Idempotent: re-running with the same IndexVersion is a
//     no-op. The 5 rows are stable (same id, same content,
//     same pinned=true) so they don't churn the FTS5 index.
//   - Versioned: bumping IndexVersion (e.g. v1 → v2) forces
//     re-index of stale rows. The operator uses this when the
//     canonical doc set changes (e.g. we add 2 new docs).
//   - Defensive: a failure on one doc does not abort the
//     index. Each doc is processed in its own try; errors
//     are collected and returned as a slice.
//   - Audit-friendly: each Save / Update emits one
//     audit_log row (INV-1, via agent_memory.Save / Update).
//     The Index function itself does NOT emit a summary
//     audit row — the 5 individual rows are sufficient.
//
// # Operator (system vs human)
//
// The 5 indexed rows use `operator="system"` to separate
// them from operator-written rows. This is the same pattern
// v3.0 used for `seeded_*` rows. The audit Actor is also
// "system" — following the existing convention where
// audit_log.actor mirrors agent_memory.operator.
//
// # Future work
//
// If the doc set grows past ~20 docs, move the list to a
// manifest_v2.go file and add a `dark_memory_doc_index`
// MCP tool that re-runs Index on demand. For 5 docs,
// in-process + boot-time is the right shape.
package docs_index

import (
	"context"
	"fmt"
	"strings"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
)

// IndexVersion is the version of the doc index. Bump this
// (e.g. "v1" → "v2") when DocsToIndex changes (new doc
// added, doc renamed, doc content overhauled) and the
// indexer will re-flow stale rows on the next boot.
const IndexVersion = "v1"

// IndexTagPrefix is the tag prefix that identifies index
// rows in agent_memory. The full tag is
// "doc-index:<IndexVersion>" (e.g. "doc-index:v1").
const IndexTagPrefix = "doc-index:"

// IndexTag is the full index version tag.
const IndexTag = IndexTagPrefix + IndexVersion

// SystemOperator is the agent_memory.operator value used
// for the 5 indexed rows. "system" is a reserved namespace
// for rows written by dark-memory itself (not by humans).
const SystemOperator = "system"

// DocMeta is the metadata for one indexed doc. The fields
// here map 1:1 to the agent_memory Row the indexer writes;
// Title and Content are the discoverable surface, Path is
// for the harness to actually open the file.
type DocMeta struct {
	ID       string   // unique stable id, kebab-case, e.g. "RUNBOOK"
	Name     string   // file basename without .md, e.g. "RUNBOOK"
	Title    string   // human-readable title
	Path     string   // relative path to the doc from repo root
	Sections []string // top-level section names (for navigation)
	Tags     string   // extra tags, comma-separated (no leading/trailing comma)
	Content  string   // TL;DR + section list + path; this is what recall() returns
}

// DocsToIndex is the canonical list of operator-facing docs
// the loadout protocol should always have access to. Edit
// here when adding / removing indexed docs. After editing,
// bump IndexVersion so existing installs re-index.
//
// Each doc's Content field is a 1-paragraph TL;DR + section
// list + path. The harness reads the actual file when it
// needs the authoritative content; recall() returns just
// the TL;DR + section list (so the FTS5 index stays small).
var DocsToIndex = []DocMeta{
	{
		ID:    "RUNBOOK",
		Name:  "RUNBOOK",
		Title: "dark-memory operator runbook",
		Path:  "docs/RUNBOOK.md",
		Sections: []string{
			"Quick start", "Boot sequence", "Daily ops",
			"Recovery", "Migrations", "Operator decisions",
		},
		Tags: "operator,runbook,ops",
		Content: "Primary operator manual for dark-memory-mcp. " +
			"Covers quick start, boot sequence, daily operations, " +
			"recovery procedures, migrations, and operator decision " +
			"logs. Load this when you don't know how to do X. " +
			"Path: docs/RUNBOOK.md. " +
			"Sections: Quick start | Boot sequence | Daily ops | " +
			"Recovery | Migrations | Operator decisions.",
	},
	{
		ID:    "INVARIANTS",
		Name:  "INVARIANTS",
		Title: "Eight operational invariants (INV-1 to INV-7)",
		Path:  "docs/INVARIANTS.md",
		Sections: []string{
			"INV-1 audit", "INV-2 operator scope", "INV-3 canary",
			"INV-4 schema", "INV-5 cache", "INV-6 governance",
			"INV-7 multi-tenant", "INV-8 session recovery",
		},
		Tags: "invariants,design,rules,adr",
		Content: "The eight non-negotiable rules every tool must obey. " +
			"INV-1 = audit row on every state mutation. " +
			"INV-2 = operator scope. " +
			"INV-3 = canary. " +
			"INV-4 = schema migrations. " +
			"INV-5 = cache. " +
			"INV-6 = drift governance. " +
			"INV-7 = multi-tenant (lands in 10b). " +
			"INV-8 = session recovery. " +
			"Load this when you need to know WHY a tool behaves a " +
			"certain way. " +
			"Path: docs/INVARIANTS.md.",
	},
	{
		ID:    "AGENT_MEMORY_SCHEMA",
		Name:  "AGENT_MEMORY_SCHEMA",
		Title: "agent_memory v4-alpha schema reference",
		Path:  "docs/AGENT_MEMORY_SCHEMA.md",
		Sections: []string{
			"Table schema", "Kinds", "Audit integration",
			"FTS5 sync", "Atomicity",
		},
		Tags: "schema,agent-memory,reference",
		Content: "v4-alpha agent_memory table reference. Covers the 8 " +
			"columns (id, operator, kind, title, content, tags, " +
			"pinned, created_at), the 7 canonical kinds (note, " +
			"observation, decision, finding, todo, link, context), " +
			"the FTS5 sync contract, and the atomicity guarantee. " +
			"Load this before writing or auditing any agent_memory " +
			"row. " +
			"Path: docs/AGENT_MEMORY_SCHEMA.md. " +
			"Sections: Table schema | Kinds | Audit integration | " +
			"FTS5 sync | Atomicity.",
	},
	{
		ID:    "V4_STATUS",
		Name:  "v4-status",
		Title: "v4-alpha current state of dark-memory",
		Path:  "docs/v4-status.md",
		Sections: []string{
			"TL;DR", "Tools inventory (38 of 57 after 10a)",
			"Deferred list (10b-10e)", "Schema version",
			"Pipeline", "What you can rely on",
			"Tier-1 sources", "Where to read next",
		},
		Tags: "status,roadmap,v4alpha,tools,alpha.4",
		Content: "Current state of the v4-alpha redesign. Lists which " +
			"38 of 57 canonical tools are shipped (after 10a) and " +
			"which 19 are deferred to 10b-10e. Schema version, " +
			"invariants status, pipeline architecture, and what you " +
			"can rely on as stable vs experimental. " +
			"Load this when you need to know 'is this tool shipped " +
			"yet?' or 'what version am I on?'. " +
			"Path: docs/v4-status.md. " +
			"Sections: TL;DR | Tools inventory | Deferred list | " +
			"Schema version | Pipeline | What you can rely on | " +
			"Tier-1 sources | Where to read next.",
	},
	{
		ID:    "JUDGE_PIPELINE_V4",
		Name:  "judge-pipeline-v4",
		Title: "Judge pipeline v4 operator guide",
		Path:  "docs/judge-pipeline-v4.md",
		Sections: []string{
			"Overview", "Edge cases (15 ECs)",
			"Personas (11 registered)", "Consensus N-shot",
			"Audit integration", "Extending the registry",
			"Deliberate-break discipline", "Tier-1 sources",
			"ADR-007 cross-ref",
		},
		Tags: "judge,llm,drift,personas,adr-007",
		Content: "Operator's guide to the v4 judge pipeline (ADR-007). " +
			"9 sections: overview, 15 edge cases (EC-001 to " +
			"EC-015), 11 registered personas, consensus N-shot, " +
			"audit integration, extending the persona registry, " +
			"deliberate-break discipline, tier-1 sources, ADR-007 " +
			"cross-ref. " +
			"Load this when calling dark_memory_judge, " +
			"dark_memory_consensus, or any persona-aware " +
			"eval_type. " +
			"Path: docs/judge-pipeline-v4.md. " +
			"Sections: Overview | Edge cases | Personas | Consensus | " +
			"Audit | Extending | Deliberate-break | Tier-1 sources | " +
			"ADR-007 cross-ref.",
	},
}

// DocError is the error returned for a single doc that
// failed to index. The Index function collects these
// (one per doc) and returns them as a slice.
type DocError struct {
	DocID string
	Op    string // "save" | "update" | "list"
	Err   error
}

func (e *DocError) Error() string {
	return fmt.Sprintf("docs_index: doc=%s op=%s: %v", e.DocID, e.Op, e.Err)
}

// Result is the structured return of Index.
type Result struct {
	Inserted int         // rows newly created
	Updated  int         // rows updated (content / tags / pinned changed)
	Skipped  int         // rows already up-to-date (idempotent no-op)
	Errors   []DocError  // one per doc that failed; non-fatal
	Indexed  int         // Inserted + Updated
	Version  string      // IndexVersion that was applied
}

// Index inserts / updates the 5 index rows in agent_memory.
// Idempotent: re-running with the same IndexVersion is a no-op.
// Bumping IndexVersion triggers re-index of all rows.
//
// The function is defensive: a failure on one doc does not
// abort the index. The caller (NewServer) decides whether
// the errors are fatal or whether to log and continue.
//
// Returns a Result with counts + errors. Never returns
// (nil, error) — always returns a Result so the caller can
// inspect the partial state.
func Index(ctx context.Context, store *agent_memory.Store) (*Result, error) {
	if store == nil {
		return nil, fmt.Errorf("docs_index.Index: store is nil")
	}
	if testModeSkip {
		return &Result{Version: IndexVersion, Skipped: len(DocsToIndex)}, nil
	}
	result := &Result{Version: IndexVersion}

	// Load all "system" rows in one shot. We expect at most ~5-50
	// system rows; the limit of 200 is a safety cap.
	existing, err := store.List(ctx, SystemOperator, 200)
	if err != nil {
		return result, fmt.Errorf("docs_index.Index: list system rows: %w", err)
	}

	for _, d := range DocsToIndex {
		existingID, existingVer, found := findExisting(existing, d.Name)
		fullTags := buildTags(d)

		if !found {
			// Insert new row.
			auditMeta := &agent_memory.Audit{
				Actor:     SystemOperator,
				SessionID: "",
				ProjectID: "",
			}
			_, err := store.Save(ctx, auditMeta, SystemOperator,
				agent_memory.KindLink, d.Title, d.Content, fullTags, true)
			if err != nil {
				result.Errors = append(result.Errors, DocError{
					DocID: d.ID, Op: "save", Err: err,
				})
				continue
			}
			result.Inserted++
			continue
		}

		// Row exists. Check if the version is current.
		if existingVer == IndexVersion {
			// Up-to-date. Skip.
			result.Skipped++
			continue
		}

		// Stale version (or no version tag) — update.
		auditMeta := &agent_memory.Audit{
			Actor:     SystemOperator,
			SessionID: "",
			ProjectID: "",
		}
		title := d.Title
		content := d.Content
		tags := fullTags
		pinned := true
		err := store.Update(ctx, auditMeta, existingID, &title, &content, &tags, &pinned)
		if err != nil {
			result.Errors = append(result.Errors, DocError{
				DocID: d.ID, Op: "update", Err: err,
			})
			continue
		}
		result.Updated++
	}

	result.Indexed = result.Inserted + result.Updated
	return result, nil
}

// findExisting scans the existing "system" rows for one whose
// tags CSV contains "doc:<name>". Returns the row id, the
// current IndexVersion tag (e.g. "v1" or ""), and a found flag.
func findExisting(rows []agent_memory.Row, name string) (int64, string, bool) {
	wantTag := "doc:" + name
	for _, r := range rows {
		if !strings.Contains(r.Tags, wantTag) {
			continue
		}
		// Found. Extract the current IndexVersion.
		ver := extractIndexVersion(r.Tags)
		return r.ID, ver, true
	}
	return 0, "", false
}

// extractIndexVersion returns the IndexVersion tag value
// (e.g. "v1") from a tags CSV, or "" if not present.
func extractIndexVersion(tagsCSV string) string {
	parts := strings.Split(tagsCSV, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, IndexTagPrefix) {
			return strings.TrimPrefix(p, IndexTagPrefix)
		}
	}
	return ""
}

// buildTags builds the full tags CSV for one indexed doc.
// Format: "doc-index:v1, doc:<name>, <extra tags>"
// Always starts with the version tag so findExisting works
// reliably and so the version is always queryable.
func buildTags(d DocMeta) string {
	parts := []string{IndexTag, "doc:" + d.Name}
	if d.Tags != "" {
		for _, t := range strings.Split(d.Tags, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, ",")
}

// AllIndexedIDs returns the canonical list of doc ids in
// DocsToIndex. Useful for tests and for the loadout
// protocol to validate that all expected rows exist.
func AllIndexedIDs() []string {
	ids := make([]string, 0, len(DocsToIndex))
	for _, d := range DocsToIndex {
		ids = append(ids, d.ID)
	}
	return ids
}

// testModeSkip is the test-mode flag. When true, Index
// returns a synthetic "all skipped" Result without touching
// the store. This lets transport-layer tests control which
// row ids are reserved (avoids "id=1 is the docs_index
// RUNBOOK, not the test's first row" surprises).
//
// Set via SetTestMode(true) from TestMain in test files.
// NOT for production use.
var testModeSkip bool

// SetTestMode toggles the test-mode flag. Returns the
// previous value so tests can save/restore the global.
func SetTestMode(skip bool) bool {
	prev := testModeSkip
	testModeSkip = skip
	return prev
}
