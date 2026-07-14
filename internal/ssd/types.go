// Package ssd defines the dark-ssd LLM-as-judge types. Schema: sdd_evaluations.
//
// SDD = Structured Disposition Determination. Every LLM-as-judge call
// (brand_match, compliance_check, drift_judge, grounding_check,
// pii_detect, prompt_injection_scan, consensus) persists one row here
// for auditability and calibration.
package ssd

// EvaluationType is the discriminator for SDDEvaluation rows.
type EvaluationType string

const (
	EvalBrandMatch           EvaluationType = "brand_match"
	EvalComplianceCheck      EvaluationType = "compliance_check"
	EvalDriftJudge           EvaluationType = "drift_judge"
	EvalGroundingCheck       EvaluationType = "grounding_check"
	EvalPIIDetect            EvaluationType = "pii_detect"
	EvalPromptInjectionScan  EvaluationType = "prompt_injection_scan"
	EvalConsensus            EvaluationType = "consensus"
)

// SDDEvaluation is one LLM-as-judge verdict. v3 added five constitution-
// aware columns (ConstitutionID, ConstitutionVersion, ActiveModsJSON,
// RefusedAttempts, RefusalPattern) so the audit trail can reproduce
// exactly which constitution + mods were active when the judge ran.
type SDDEvaluation struct {
	ID                  int64   `json:"id"`
	EvalType            string  `json:"eval_type"`
	TargetType          string  `json:"target_type"`
	TargetID            string  `json:"target_id"`
	VerdictJSON         string  `json:"verdict_json"`
	Confidence          float32 `json:"confidence"`
	PromptVersion       string  `json:"prompt_version,omitempty"`
	Model               string  `json:"model,omitempty"`
	ConstitutionID      string  `json:"constitution_id,omitempty"`
	ConstitutionVersion string  `json:"constitution_version,omitempty"`
	ActiveModsJSON      string  `json:"active_mods_json,omitempty"`
	RefusedAttempts     int     `json:"refused_attempts"`
	RefusalPattern      string  `json:"refusal_pattern,omitempty"`
	CreatedAt           string  `json:"created_at"`
}

// ListFilters holds optional filters for ListEvaluations.
type ListFilters struct {
	EvalType   string
	TargetType string
	Limit      int
}
