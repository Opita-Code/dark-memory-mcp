// Package research defines the research-layer types persisted by Dark Memory MCP.
// Schema: research_runs, research_items, research_links.
//
// Cross-references:
//   - Store interface in github.com/dark-agents/dark-memory-mcp/internal/store
//   - SQLite DDL v1 in github.com/dark-agents/dark-memory-mcp/internal/migrate/sqlite/v1
//   - Postgres DDL v1 in github.com/dark-agents/dark-memory-mcp/internal/migrate/postgres/v1
package research

import "time"

// ResearchRun is one query executed by the router. Saved along with
// its Items in a single transaction.
type ResearchRun struct {
	ID            int64          `json:"id"`
	SessionID     string         `json:"session_id,omitempty"`
	Query         string         `json:"query"`
	Intent        string         `json:"intent"`
	BackendUsed   string         `json:"backend_used,omitempty"`
	BackendsTried []string       `json:"backends_tried,omitempty"`
	TookMs        int64          `json:"took_ms"`
	ConfidenceAvg float32        `json:"confidence_avg"`
	Items         []Item         `json:"items,omitempty"`
	Errors        []BackendError `json:"errors,omitempty"`
	CreatedAt     string         `json:"created_at"`
}

// Item is one search result, persisted as part of a ResearchRun.
type Item struct {
	ID          int64   `json:"id"`
	RunID       int64   `json:"run_id"`
	Title       string  `json:"title"`
	URL         string  `json:"url,omitempty"`
	Snippet     string  `json:"snippet,omitempty"`
	Source      string  `json:"source"`
	Confidence  float32 `json:"confidence"`
	FreshnessAt string  `json:"freshness_at,omitempty"` // RFC 3339; empty if unknown
	Lang        string  `json:"lang,omitempty"`
	Raw         string  `json:"raw,omitempty"` // JSON blob
	CreatedAt   string  `json:"created_at"`
}

// BackendError mirrors research.BackendError but lives in this package
// to avoid import cycles (research imports mem, not the other way).
type BackendError struct {
	Backend string `json:"backend"`
	Err     string `json:"err"`
}

// Link is one cross-reference from a research_item to a target (attack,
// cve, technique, paper, mod). Persisted in research_links.
type Link struct {
	ID             int64  `json:"id"`
	ResearchItemID int64  `json:"research_item_id"`
	TargetType     string `json:"target_type"` // attack | cve | technique | paper | mod
	TargetID       string `json:"target_id"`
	Note           string `json:"note,omitempty"`
	Source         string `json:"source,omitempty"` // "user" | "auto-link-v2" | "operator"
	Confidence     float32 `json:"confidence,omitempty"`
	CreatedAt      string `json:"created_at"`
}

// SessionScope controls whether Recall() filters by session_id.
// "all"   — cross-session (default; matches existing dark-research-mcp behavior)
// "self"  — only items whose run.session_id matches the caller
// ""      — alias for "all"
type SessionScope string

const (
	SessionScopeAll  SessionScope = "all"
	SessionScopeSelf SessionScope = "self"
)

// Status is the aggregate stats returned by Store.ResearchStatus.
type Status struct {
	RunsTotal       int            `json:"runs_total"`
	ItemsTotal      int            `json:"items_total"`
	LinksTotal      int            `json:"links_total"`
	IntentHistogram map[string]int `json:"intent_histogram"`
	SourceHistogram map[string]int `json:"source_histogram"`
	OldestRun       string         `json:"oldest_run,omitempty"`
	NewestRun       string         `json:"newest_run,omitempty"`
}

// RecallOptions tunes a Recall call.
type RecallOptions struct {
	Query       string
	Intent      string // optional intent filter
	Source      string // optional source filter
	SessionID   string // required when SessionScope == self
	SessionScope SessionScope
	Limit       int
}

// time alias to avoid importing time in callers that don't need it
var _ = time.Time{}
