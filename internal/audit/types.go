// Package audit defines the write-audit types. INV-1 (write-path audit)
// from the constitution requires every Save* call to emit a row here.
package audit

// WriteEvent is one row in the write_audit table. Every Store.Save*
// method must emit exactly one WriteEvent (either directly via
// RecordWrite or via the WriteContext the impl carries).
type WriteEvent struct {
	ID                int64  `json:"id"`
	TableName         string `json:"table_name"`         // research_items | vibe_specs | ...
	RowID             int64  `json:"row_id"`             // the row just inserted/updated
	Actor             string `json:"actor"`              // tool name, or operator, or "auto-link-v2"
	SessionID         string `json:"session_id"`         // operational session
	WritePath         string `json:"write_path"`         // "SaveRun" | "dark_research_spec_create" | ...
	ContentSHA256     string `json:"content_sha256"`     // hash of the payload (or row snapshot)
	CanaryPresent     bool   `json:"canary_present"`     // derived signal — payload contained active canary?
	ConstitutionID    string `json:"constitution_id,omitempty"`
	ConstitutionVer   string `json:"constitution_ver,omitempty"`
	Notes             string `json:"notes,omitempty"`
	CreatedAt         string `json:"created_at"`
}

// ListFilters holds optional filters for ListWrites.
type ListFilters struct {
	Since     string // RFC3339, optional
	Actor     string
	WritePath string
	SessionID string
	Limit     int
}
