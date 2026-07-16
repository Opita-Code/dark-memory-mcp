// Package audit defines the write-audit types. INV-1 (write-path audit)
// from the constitution requires every Save* call to emit a row here.
//
// INV-7 (multi-tenancy by project) is now enforced at the audit layer
// since migration v10 (debt-elimination commit). Every audit row carries
// project_id; ListWrites filters by project_id when the caller sets
// ActiveProject.
package audit

// WriteEvent is one row in the write_audit table. Every Store.Save*
// method must emit exactly one WriteEvent (either directly via
// RecordWrite or via the WriteContext the impl carries).
//
// ProjectID is populated by the Store impl from either wc.ProjectID
// (if set) or s.ActiveProject() (fallback). Empty string is only
// allowed for global tables (vibe_compliance, constitutions, mods).
type WriteEvent struct {
	ID                int64  `json:"id"`
	TableName         string `json:"table_name"`         // research_items | vibe_specs | ...
	RowID             int64  `json:"row_id"`             // the row just inserted/updated
	ProjectID         string `json:"project_id"`         // INV-7 — from wc.ProjectID or ActiveProject()
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
//
// ProjectID: when non-empty, ListWrites filters to rows in that project
// (INV-7). Store impl may also enforce this filter automatically based
// on the active project (read-side tenant isolation).
type ListFilters struct {
	Since     string // RFC3339, optional
	Actor     string
	WritePath string
	SessionID string
	ProjectID string // INV-7 — empty = caller accepts cross-project rows
	Limit     int
}
