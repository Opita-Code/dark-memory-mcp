// Package constitution defines the constitution system types. The
// constitution loader (this module + internal/constitution/loader) parses
// TOML files and persists them in the constitutions table so the same
// constitution is applied consistently across restarts.
package constitution

// Constitution is one (constitution_id, version) manifest. UNIQUE
// (constitution_id, version) at the DB layer means the same constitution
// can have multiple versions over time, and sdd_evaluations.constitution_id
// + constitution_version together reproduce the exact manifest in effect.
type Constitution struct {
	ID             int64  `json:"id"`
	ConstitutionID string `json:"constitution_id"`
	Version        string `json:"version"`
	Label          string `json:"label,omitempty"`
	Source         string `json:"source"`
	FilePath       string `json:"file_path"`
	ParsedJSON     string `json:"parsed_json"`
	SHA256         string `json:"sha256"`
	Enabled        bool   `json:"enabled"`
	CreatedAt      string `json:"created_at"`
	ActivatedAt    string `json:"activated_at,omitempty"`
}

// ActiveRef is the small projection used to populate ssd_evaluations
// audit fields.
type ActiveRef struct {
	ID      string
	Version string
}

// IsZero reports whether the ref is empty.
func (r ActiveRef) IsZero() bool { return r.ID == "" && r.Version == "" }
