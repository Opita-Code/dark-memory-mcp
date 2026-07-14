// Package vibeflow defines the vibe-flow types persisted by Dark Memory MCP.
// Schema: vibe_specs, vibe_brands, vibe_compliance, vibe_artifacts, vibe_drift_reports.
//
// These types are the boundary between the LLM-facing tool surface and
// the persistence layer. JSON fields are passed through as opaque strings
// so the schema can evolve without DB migrations.
package vibeflow

// Spec is a declarative intent: constitution (rules) + spec (what) +
// tasks (ordered work units). Persisted before generation; reconciled with
// artifacts via drift_reports. Maps to DB table vibe_specs.
type Spec struct {
	ID           int64  `json:"id"`
	VibeCase     string `json:"vibe_case"`
	SessionID    string `json:"session_id,omitempty"`
	Constitution string `json:"constitution,omitempty"`
	Spec         string `json:"spec,omitempty"`
	Tasks        string `json:"tasks,omitempty"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

// BrandGuide captures voice + visual + narrative + compliance for one
// brand. Single-row per brand_id (upsert). Maps to DB table vibe_brands.
type BrandGuide struct {
	BrandID    string `json:"brand_id"`
	Voice      string `json:"voice,omitempty"`
	Visual     string `json:"visual,omitempty"`
	Narrative  string `json:"narrative,omitempty"`
	Compliance string `json:"compliance,omitempty"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

// ComplianceRule captures jurisdiction-specific rules. Maps to DB
// table vibe_compliance.
type ComplianceRule struct {
	Jurisdiction string `json:"jurisdiction"`
	Rules        string `json:"rules"`
	EffectiveAt  string `json:"effective_at,omitempty"`
	SourceURL    string `json:"source_url,omitempty"`
	CreatedAt    string `json:"created_at"`
}

// Artifact is one generated output (code, text, image, video, audio,
// multi-modal). Maps to DB table vibe_artifacts.
type Artifact struct {
	ID               int64  `json:"id"`
	SessionID        string `json:"session_id,omitempty"`
	VibeCase         string `json:"vibe_case"`
	SpecID           int64  `json:"spec_id,omitempty"`
	ArtifactURL      string `json:"artifact_url,omitempty"`
	ArtifactType     string `json:"artifact_type"`
	BrandID          string `json:"brand_id,omitempty"`
	Jurisdiction     string `json:"jurisdiction,omitempty"`
	HasDisclosure    bool   `json:"has_disclosure"`
	ValidationStatus string `json:"validation_status"`
	CreatedAt        string `json:"created_at"`
}

// ArtifactUpdate is a partial update for an artifact. Nil pointer = leave
// unchanged; non-nil pointer = set to that value.
type ArtifactUpdate struct {
	SessionID        *string
	SpecID           *int64
	ArtifactURL      *string
	BrandID          *string
	Jurisdiction     *string
	HasDisclosure    *bool
	ValidationStatus *string
}

// DriftReport records a comparison between a Spec and a generated
// Artifact. Verdict aligned means the artifact matches the spec;
// drift_detected means the artifact diverged (reconcile needed);
// needs_human means the LLM-as-judge was not confident. Maps to DB
// table vibe_drift_reports.
type DriftReport struct {
	ID             int64  `json:"id"`
	ArtifactID     int64  `json:"artifact_id"`
	SpecID         int64  `json:"spec_id,omitempty"`
	Verdict        string `json:"verdict"`
	SpecDiff       string `json:"spec_diff,omitempty"`
	JudgeReasoning string `json:"judge_reasoning,omitempty"`
	ReconciledAt   string `json:"reconciled_at,omitempty"`
	CreatedAt      string `json:"created_at"`
}

// ArtifactListFilters holds optional filters for ListArtifacts.
type ArtifactListFilters struct {
	VibeCase     string
	BrandID      string
	Jurisdiction string
	SessionID    string
	Status       string
	Limit        int
}

// SpecListFilters holds optional filters for ListSpecs.
type SpecListFilters struct {
	VibeCase  string
	SessionID string
	Limit     int
}
