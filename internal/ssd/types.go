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
	EvalBrandMatch          EvaluationType = "brand_match"
	EvalComplianceCheck     EvaluationType = "compliance_check"
	EvalDriftJudge          EvaluationType = "drift_judge"
	EvalGroundingCheck      EvaluationType = "grounding_check"
	EvalPIIDetect           EvaluationType = "pii_detect"
	EvalPromptInjectionScan EvaluationType = "prompt_injection_scan"
	EvalConsensus           EvaluationType = "consensus"
	// v2.7.0-alpha: mindset delegation primitive. MindsetCompose is
	// the GENERATIVE call (LLM synthesizes a subagent system_prompt
	// given vibe_case + task_description). MindsetQuality is the
	// VALIDATIVE call (LLM judges whether the proposed prompt is
	// well-formed per the 5 pass criteria). Both persist SDDEvaluation
	// rows for full audit trail of every composition iteration.
	EvalMindsetCompose EvaluationType = "mindset_compose"
	EvalMindsetQuality EvaluationType = "mindset_quality"
)

// SDDEvaluation is one LLM-as-judge verdict. v3 added five constitution-
// aware columns (ConstitutionID, ConstitutionVersion, ActiveModsJSON,
// RefusedAttempts, RefusalPattern) so the audit trail can reproduce
// exactly which constitution + mods were active when the judge ran.
//
// v29 (spec 1276, T10) added eight anchor + audit columns:
// MerkleRoot (chain position), ArtifactSource/ArtifactSHA256/
// ArtifactPath/ArtifactSize (which bytes were evaluated),
// ChunkIndex/ChunkTotal (which chunk in a consensus run),
// NLIProviderID (which model answered). The chunk_* fields are
// always 0 for non-consensus rows; the artifact_* and NLIProviderID
// fields are populated only by drift_judge + drift_judge_consensus.
// All eight columns are NULLABLE — pre-v29 rows have empty values.
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
	// PersonaID (v2.17.0, spec 1155) is the resolved persona used for
	// this evaluation. Empty for v2.16.0 evaluations (backward
	// compat). Lets operators audit which persona was applied to each
	// historical evaluation.
	PersonaID string `json:"persona_id,omitempty"`
	// v29 anchor + audit columns (spec 1276, T10).
	//
	// MerkleRoot is the 64-char hex hash embedded in the
	// sdd_evaluations chain. SaveSDDEvaluation computes it
	// atomically: SELECT last merkle_root → ComputeRoot(prev,
	// canonical) → INSERT. Empty for pre-v29 rows (legacy boundary
	// detected by VerifyEvalChain).
	MerkleRoot string `json:"merkle_root,omitempty"`
	// ArtifactSource is artifact.Source.String() for drift_judge
	// and drift_judge_consensus; empty for other eval types.
	// Possible values: "file", "git_sha", "url", "spec_id",
	// "artifact_id".
	ArtifactSource string `json:"artifact_source,omitempty"`
	// ArtifactSHA256 is the 64-char hex SHA-256 of the resolved
	// artifact body (the source of truth for "which bytes were
	// evaluated"). Empty for non-artifact eval types.
	ArtifactSHA256 string `json:"artifact_sha256,omitempty"`
	// ArtifactPath is the canonical path or URL that the resolver
	// fetched. Empty for non-artifact eval types.
	ArtifactPath string `json:"artifact_path,omitempty"`
	// ArtifactSize is the size of the resolved body in bytes at
	// evaluation time. 0 for non-artifact eval types.
	ArtifactSize int64 `json:"artifact_size,omitempty"`
	// ChunkIndex is 0 for non-consensus rows (single-shot, or whole
	// artifact); N (>= 1) for the N-th chunk in a consensus run.
	// The consensus row itself has ChunkIndex = 0 (disambiguated by
	// ChunkTotal > 0).
	ChunkIndex int `json:"chunk_index,omitempty"`
	// ChunkTotal is N for a consensus run (>= 1); 0 for non-
	// consensus. The combination (ChunkIndex, ChunkTotal) lets a
	// reader reconstruct the chunking strategy: (0, 0) → single
	// whole-artifact evaluation; (K, N) → K-th chunk of N in a
	// consensus run.
	ChunkTotal int `json:"chunk_total,omitempty"`
	// NLIProviderID is the nli.Provider.ID() that scored the
	// verdict. Empty for non-drift eval types.
	NLIProviderID string `json:"nli_provider_id,omitempty"`
	CreatedAt    string  `json:"created_at"`
}

// ListFilters holds optional filters for ListEvaluations.
type ListFilters struct {
	EvalType   string
	TargetType string
	TargetID   string // filter by target_id (e.g. specific spec_id)
	Limit      int
}
