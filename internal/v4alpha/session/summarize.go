// summarize.go (v4alpha) — PRE-1 C4.
//
// Summarize produces a handoff document for a session.
// Use cases:
//   1. Cross-session continuity: the harness restarts, the
//      operator hands off; summarize shows what was loaded
//      and what was done in the last session.
//   2. Postmortem: after a needs_human verdict, the operator
//      asks "what happened?" and gets a markdown summary.
//   3. Audit support: every audit_log row in the session is
//      surfaced for operator review (INV-1).
//
// Scope (v1):
//   - Session metadata (id, operator, project_id, status,
//     started_at, last_heartbeat_at, closed_at).
//   - Pinned rows (kind=link pinned=true, surfaced first).
//   - Open todos (kind=todo; resolved-state check is TODO
//     in a future C4 enhancement when todo has a status field).
//   - Recent writes (audit_log rows where session_id = X,
//     last 100).
//   - Skills loaded (agent_memory rows tagged skill_loaded:v1).
//
// Out of scope (v1, documented for future):
//   - Drift reports (vibe_drifts.session_id column doesn't
//     exist; lands in BUG-10 10b's schema migration).
//   - Judge verdicts (sdd_evaluations.session_id column
//     doesn't exist; same migration).
//   - JSON output (markdown v1; json v2).
//   - include filter (v1 returns everything; future can scope).
//
// Atomicity contract:
//   - ONE entry point: Summarize
//   - ONE writer: SkillLoad (via agent_memory.Save)
//   - ZERO schema changes (queries read-only).
//
// INV-1 audit integration: Summarize is read-only; it does
// NOT emit audit rows. SkillLoad emits one audit row per
// call (via agent_memory.Save).
package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	am "github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
)

// Errors specific to summarize + skill_loaded.
var (
	ErrEmptySkill = errors.New("summarize: skill_name must be non-empty")
	ErrNoSession  = errors.New("summarize: session_id required")
)

// HandoffSummary is the handoff document for one session.
// (Named HandoffSummary to avoid clash with the
// session.Store.Summary returned by Close, which holds
// per-session write/run/item counts.)
type HandoffSummary struct {
	Session      *Session         `json:"session"`
	PinnedRows   []am.Row         `json:"pinned_rows"`
	OpenTodos    []am.Row         `json:"open_todos"`
	AuditRows    []AuditRow       `json:"audit_rows"`
	SkillsLoaded []SkillLoad      `json:"skills_loaded"`
	GeneratedAt  time.Time        `json:"generated_at"`
}

// AuditRow is one row from audit_log, projected for the
// summary. Payload is the raw JSON BLOB (caller decides to
// parse or display).
type AuditRow struct {
	AuditID   int64  `json:"audit_id"`
	Actor     string `json:"actor"`
	Payload   string `json:"payload"` // string form of the BLOB
	CreatedAt string `json:"created_at"`
}

// SkillLoad is one parsed skill_loaded row.
type SkillLoad struct {
	SkillName string `json:"skill_name"`
	Version   string `json:"version,omitempty"`
	Source    string `json:"source,omitempty"`
	LoadedAt  string `json:"loaded_at"`
	RowID     int64  `json:"row_id"`
}

// SkillLoadedTagPrefix is the tag prefix that summarize
// uses to find skill_loaded events. Same prefix the
// harness uses when calling skill_loaded.
const SkillLoadedTagPrefix = "skill_loaded:"

// SkillLoadedTagVersion is the version of the convention.
// Bumping this tag invalidates old skill_loaded rows (they
// stay in agent_memory but become invisible to summarize).
const SkillLoadedTagVersion = "v1"

// Summarize returns a Summary for the given sessionID.
// Reads session row from sessionStore, queries agent_memory
// for pinned/todos/skills, queries audit_log for recent
// writes. Read-only; emits no audit rows.
//
// INV-7 note: sessionStore.Get bypasses the project_id
// check (intentional — summarize is operator-facing and
// the operator knows their session id). For tenant-isolated
// access, use sessionStore.Read(sessionID, projectID).
func Summarize(
	ctx context.Context,
	sessionID string,
	sessionStore *Store,
	amStore *am.Store,
	db *sql.DB,
) (*HandoffSummary, error) {
	if sessionID == "" {
		return nil, ErrNoSession
	}
	if sessionStore == nil || amStore == nil || db == nil {
		return nil, fmt.Errorf("summarize: nil store(s)")
	}

	// 1. Read the session row.
	sess, err := sessionStore.Get(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("summarize session read: %w", err)
	}

	// 2. Pinned rows for this operator.
	pinnedTrue := true
	pinnedRows, err := amStore.ListFiltered(ctx, sess.Operator, am.ListFilter{
		Pinned: &pinnedTrue,
	}, 50)
	if err != nil {
		return nil, fmt.Errorf("summarize pinned: %w", err)
	}

	// 3. Open todos for this operator (kind=todo, all).
	// v1: no status field, so all kind=todo rows are "open".
	// A future C4 enhancement adds a status discriminator.
	openTodos, err := amStore.ListFiltered(ctx, sess.Operator, am.ListFilter{
		Kind: "todo",
	}, 50)
	if err != nil {
		return nil, fmt.Errorf("summarize todos: %w", err)
	}

	// 4. Recent writes (audit_log rows for this session).
	auditRows, err := queryAuditRows(ctx, db, sessionID, 100)
	if err != nil {
		return nil, fmt.Errorf("summarize audit: %w", err)
	}

	// 5. Skills loaded (tag_prefix=skill_loaded:).
	// Uses RecallFiltered (not ListFiltered) because the
	// tag_prefix filter is only in RecallFilter (PRE-1 C1).
	// The FTS5 query is non-empty (RecallFiltered returns
	// nil on empty query by design); "skill" matches any
	// skill_loaded row whose content mentions "skill".
	skillRows, err := amStore.RecallFiltered(ctx, sess.Operator, "skill", am.RecallFilter{
		TagPrefix: SkillLoadedTagPrefix,
	}, 50)
	if err != nil {
		return nil, fmt.Errorf("summarize skills: %w", err)
	}
	skillsLoaded := make([]SkillLoad, 0, len(skillRows))
	for _, r := range skillRows {
		sl := parseSkillLoad(r)
		skillsLoaded = append(skillsLoaded, sl)
	}

	return &HandoffSummary{
		Session:      sess,
		PinnedRows:   pinnedRows,
		OpenTodos:    openTodos,
		AuditRows:    auditRows,
		SkillsLoaded: skillsLoaded,
		GeneratedAt:  time.Now().UTC(),
	}, nil
}

// queryAuditRows reads up to limit rows from audit_log where
// session_id = sessionID, ordered by audit_id DESC (newest first).
func queryAuditRows(ctx context.Context, db *sql.DB, sessionID string, limit int) ([]AuditRow, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT audit_id, actor, payload, created_at
		FROM audit_log
		WHERE session_id = ?
		ORDER BY audit_id DESC
		LIMIT ?
	`, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("summarize audit query: %w", err)
	}
	defer rows.Close()

	out := make([]AuditRow, 0, 32)
	for rows.Next() {
		var ar AuditRow
		var payload []byte
		if err := rows.Scan(&ar.AuditID, &ar.Actor, &payload, &ar.CreatedAt); err != nil {
			return nil, fmt.Errorf("summarize audit scan: %w", err)
		}
		ar.Payload = string(payload)
		out = append(out, ar)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("summarize audit rows: %w", err)
	}
	return out, nil
}

// parseSkillLoad extracts SkillName, Version, Source, LoadedAt
// from a kind=observation row. The convention:
//
//   tags CSV:    "skill_loaded:v1,skill:<name>,version:<v>,source:<src>"
//   title:       "Skill <name> loaded"
//   content:     "Skill <name> v<v> loaded from <src> at <RFC3339>"
//
// Missing fields are returned as empty strings (not errors).
// The tag prefix `skill_loaded:` is mandatory; the rest is
// best-effort parsing.
func parseSkillLoad(r am.Row) SkillLoad {
	sl := SkillLoad{
		LoadedAt: r.CreatedAt.Format(time.RFC3339),
		RowID:    r.ID,
	}

	// Parse tags CSV.
	tags := strings.Split(r.Tags, ",")
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		switch {
		case strings.HasPrefix(tag, "skill:"):
			sl.SkillName = strings.TrimPrefix(tag, "skill:")
		case strings.HasPrefix(tag, "version:"):
			sl.Version = strings.TrimPrefix(tag, "version:")
		case strings.HasPrefix(tag, "source:"):
			sl.Source = strings.TrimPrefix(tag, "source:")
		}
	}
	// Fallback: extract skill_name from title if not in tags.
	if sl.SkillName == "" && strings.HasPrefix(r.Title, "Skill ") {
		// "Skill dark-memory loaded" → "dark-memory"
		rest := strings.TrimPrefix(r.Title, "Skill ")
		if idx := strings.Index(rest, " loaded"); idx > 0 {
			sl.SkillName = rest[:idx]
		}
	}
	return sl
}

// SkillLoad records a skill load event in agent_memory.
// This is the convention promoted by PRE-1 C2 L2 ("system-
// generated rows are a clean pattern") and formalized in
// PRE-1 C4.
//
// The convention:
//   operator: session's operator (INV-1 attribution)
//   kind:     "observation"
//   title:    "Skill <name> loaded"
//   content:  "Skill <name> v<version> loaded from <source> at <RFC3339>"
//   tags:     "skill_loaded:v1,skill:<name>,version:<v>,source:<src>"
//   pinned:   false (per skill_loaded convention)
//   bind_session: true
//
// Skill_loaded rows surface in dark_memory_summarize_session
// via RecallFiltered(tag_prefix="skill_loaded:").
func RecordSkillLoad(
	ctx context.Context,
	amStore *am.Store,
	auditMeta *am.Audit,
	operator string,
	skillName string,
	version string,
	source string,
) (int64, error) {
	if skillName == "" {
		return 0, ErrEmptySkill
	}
	if operator == "" {
		return 0, fmt.Errorf("skill_loaded: operator required")
	}
	if amStore == nil {
		return 0, fmt.Errorf("skill_loaded: nil am store")
	}

	tags := []string{
		SkillLoadedTagPrefix + SkillLoadedTagVersion,
		"skill:" + skillName,
	}
	if version != "" {
		tags = append(tags, "version:"+version)
	}
	if source != "" {
		tags = append(tags, "source:"+source)
	}
	tagsCSV := strings.Join(tags, ",")

	now := time.Now().UTC().Format(time.RFC3339)
	contentParts := []string{fmt.Sprintf("Skill %s loaded", skillName)}
	if version != "" {
		contentParts = append(contentParts, "v"+version)
	}
	contentParts = append(contentParts, "at "+now)
	if source != "" {
		contentParts = append(contentParts, "from "+source)
	}
	content := strings.Join(contentParts, " ")

	id, err := amStore.Save(
		ctx, auditMeta, operator, "observation",
		fmt.Sprintf("Skill %s loaded", skillName),
		content, tagsCSV, false,
	)
	if err != nil {
		return 0, fmt.Errorf("skill_loaded save: %w", err)
	}
	return id, nil
}

// Markdown formats the HandoffSummary as a markdown handoff doc.
// Six sections, in this order:
//   1. Session metadata
//   2. Pinned rows
//   3. Open todos
//   4. Recent writes (audit_log)
//   5. Skills loaded
//
// Drift reports and judge verdicts are out of scope for v1
// (no session_id column on those tables yet); the markdown
// notes this explicitly so the operator knows what's missing.
func (s *HandoffSummary) Markdown() string {
	var b strings.Builder
	b.WriteString("# Session summary\n\n")

	// Section 1: Session metadata.
	b.WriteString("## 1. Session metadata\n\n")
	b.WriteString(fmt.Sprintf("- **Session ID**: `%s`\n", s.Session.ID))
	b.WriteString(fmt.Sprintf("- **Operator**: `%s`\n", s.Session.Operator))
	b.WriteString(fmt.Sprintf("- **Project**: `%s`\n", s.Session.ProjectID))
	b.WriteString(fmt.Sprintf("- **Status**: `%s`\n", s.Session.Status))
	b.WriteString(fmt.Sprintf("- **Started**: `%s`\n", s.Session.StartedAt.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("- **Last heartbeat**: `%s`\n", s.Session.LastHeartbeatAt.Format(time.RFC3339)))
	if s.Session.ClosedAt != nil {
		b.WriteString(fmt.Sprintf("- **Closed**: `%s`\n", s.Session.ClosedAt.Format(time.RFC3339)))
	}
	b.WriteString(fmt.Sprintf("- **Generated at**: `%s`\n\n", s.GeneratedAt.Format(time.RFC3339)))

	// Section 2: Pinned rows.
	b.WriteString(fmt.Sprintf("## 2. Pinned rows (%d)\n\n", len(s.PinnedRows)))
	if len(s.PinnedRows) == 0 {
		b.WriteString("_No pinned rows for this operator._\n\n")
	} else {
		for _, r := range s.PinnedRows {
			b.WriteString(fmt.Sprintf("- **%s** (id=%d, kind=%s)\n", r.Title, r.ID, r.Kind))
			first := firstLine(r.Content)
			if first != "" {
				b.WriteString(fmt.Sprintf("  %s\n", first))
			}
		}
		b.WriteString("\n")
	}

	// Section 3: Open todos.
	b.WriteString(fmt.Sprintf("## 3. Open todos (%d)\n\n", len(s.OpenTodos)))
	if len(s.OpenTodos) == 0 {
		b.WriteString("_No open todos for this operator._\n\n")
	} else {
		for _, r := range s.OpenTodos {
			b.WriteString(fmt.Sprintf("- **%s** (id=%d, created=%s)\n", r.Title, r.ID, r.CreatedAt))
			first := firstLine(r.Content)
			if first != "" {
				b.WriteString(fmt.Sprintf("  %s\n", first))
			}
		}
		b.WriteString("\n")
	}

	// Section 4: Recent writes.
	b.WriteString(fmt.Sprintf("## 4. Recent writes (audit_log) (%d)\n\n", len(s.AuditRows)))
	if len(s.AuditRows) == 0 {
		b.WriteString("_No audit rows for this session._\n\n")
	} else {
		for _, ar := range s.AuditRows {
			b.WriteString(fmt.Sprintf("- audit_id=%d actor=%s at %s\n", ar.AuditID, ar.Actor, ar.CreatedAt))
			if ar.Payload != "" {
				b.WriteString(fmt.Sprintf("  `%s`\n", truncateForSummary(ar.Payload, 200)))
			}
		}
		b.WriteString("\n")
	}

	// Section 5: Skills loaded.
	b.WriteString(fmt.Sprintf("## 5. Skills loaded (%d)\n\n", len(s.SkillsLoaded)))
	if len(s.SkillsLoaded) == 0 {
		b.WriteString("_No skill_loaded rows for this session._\n\n")
	} else {
		for _, sl := range s.SkillsLoaded {
ver := formatVersion(sl.Version)
		src := ""
		if sl.Source != "" {
			src = " from " + sl.Source
		}
			b.WriteString(fmt.Sprintf("- **%s**%s%s (row_id=%d, loaded_at=%s)\n",
				sl.SkillName, ver, src, sl.RowID, sl.LoadedAt))
		}
		b.WriteString("\n")
	}

	// Footer: what's NOT in this summary.
	b.WriteString("## What's NOT in this summary (v1 scope)\n\n")
	b.WriteString("- Drift reports (vibe_drifts.session_id column not yet added; planned for BUG-10 10b).\n")
	b.WriteString("- Judge verdicts (sdd_evaluations.session_id column not yet added; same migration).\n")
	b.WriteString("- JSON output format (markdown v1 only).\n")
	b.WriteString("- Include filter (v1 returns everything; future can scope).\n\n")

	b.WriteString(fmt.Sprintf("---\n_Generated by dark_memory_summarize_session at %s_\n",
		s.GeneratedAt.Format(time.RFC3339)))

	return b.String()
}

// formatVersion returns the version formatted with a
// single "v" prefix. If the version already starts with
// "v", the prefix is not duplicated.
func formatVersion(v string) string {
	if v == "" {
		return ""
	}
	if strings.HasPrefix(v, "v") {
		return " " + v
	}
	return " v" + v
}

// firstLine returns the first line of a string, trimmed.
// Empty string returns empty.
func firstLine(s string) string {
	for i, c := range s {
		if c == '\n' {
			return strings.TrimSpace(s[:i])
		}
	}
	return strings.TrimSpace(s)
}

// truncateForSummary truncates a string to max bytes for
// the audit_log display. Naive: cut at last space before
// max, append "…" if truncated. Multi-byte safe-ish
// (operates on bytes; the payload is ASCII JSON BLOB so
// this is fine).
func truncateForSummary(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && s[cut-1] != ' ' {
		cut--
	}
	if cut == 0 {
		cut = max
	}
	return s[:cut] + "…"
}

// Guard against unused import if the audit package is not
// directly referenced (it's used by callers via the audit
// writer in session.Store). This avoids a Go vet warning.
var _ = audit.Writer{}