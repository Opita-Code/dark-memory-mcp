// Package tools — vibe.go: the VIBE namespace (4 tools).
//
// Per RFC §5 / D-9:
//	dark_memory_vibe_publish
//	dark_memory_vibe_spec
//	dark_memory_pipeline_status
//	dark_memory_resolve_drift
//
// Maps to orchestrator O7 (PublishVibe), O12 (VibeSpec), O11
// (ResolveDrift) + 1 new read-only tool (pipeline_status reads the
// latest drift for an artifact_id).
package tools

import (
	"context"

	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// RegisterVibe wires the 4 VIBE tools into the registry.
func RegisterVibe(reg *Registry, orch *orchestration.Orchestrator, st store.Store) {
	// vibe_publish — wraps O7 PublishVibe meta-orchestrator.
	reg.Add(BindOrchestrator("vibe_publish",
		"Publish an artifact under a spec. Runs the full vibe-flow loop: spec_create + artifact_log + brand_match (optional) + compliance_check (optional) + drift_judge + drift_log. Returns verdict + next_action.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"artifact_url", "artifact_type", "vibe_case"},
			"properties": map[string]any{
				"vibe_case":     map[string]any{"type": "string", "enum": []string{"C1", "C2", "C3", "C4", "C5", "C6", "C7"}},
				"constitution":  map[string]any{"type": "string", "description": "Optional JSON constitution blob."},
				"spec":          map[string]any{"type": "string", "description": "Optional JSON intent blob."},
				"tasks":         map[string]any{"type": "string", "description": "Optional JSON tasks blob."},
				"artifact_url":  map[string]any{"type": "string"},
				"artifact_type": map[string]any{"type": "string", "enum": []string{"code", "text", "image", "video", "audio", "multi"}},
				"text":          map[string]any{"type": "string", "description": "Artifact body (required for drift_judge)."},
				"brand_id":      map[string]any{"type": "string"},
				"jurisdiction":  map[string]any{"type": "string"},
				"has_disclosure": map[string]any{"type": "boolean"},
				"auto_drift_check": map[string]any{"type": "boolean", "description": "Default true. Set false to skip drift_judge."},
				"session_id":    map[string]any{"type": "string"},
			},
		}),
		func(ctx context.Context, in orchestration.PublishVibeInput) (*orchestration.PublishResult, error) {
			return orch.PublishVibe(ctx, in)
		}))

	// vibe_spec — wraps O12 VibeSpec orchestrator.
	reg.Add(BindOrchestrator("vibe_spec",
		"Create a new spec with structured task validation (unique ids, no cycles, depends_on consistency).",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"vibe_case", "tasks"},
			"properties": map[string]any{
				"vibe_case":    map[string]any{"type": "string"},
				"constitution": map[string]any{"type": "string"},
				"spec":         map[string]any{"type": "string"},
				"tasks":        map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
				"session_id":   map[string]any{"type": "string"},
			},
		}),
		func(ctx context.Context, in orchestration.VibeSpecInput) (*orchestration.VibeSpecResult, error) {
			return orch.VibeSpec(ctx, in)
		}))

	// pipeline_status — read-only: latest drift for an artifact_id.
	reg.Add(BindStore("pipeline_status",
		"Return the latest drift report for an artifact_id (or nil if none). Read-only.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"artifact_id"},
			"properties": map[string]any{
				"artifact_id": map[string]any{"type": "integer"},
			},
		}),
		st,
		func(ctx context.Context, s store.Store, in PipelineStatusInput) (*PipelineStatusResult, error) {
			d, err := s.LatestDriftForArtifact(ctx, in.ArtifactID)
			if err != nil {
				return nil, err
			}
			if d == nil {
				return &PipelineStatusResult{ArtifactID: in.ArtifactID, HasDrift: false}, nil
			}
			return &PipelineStatusResult{
				ArtifactID:  in.ArtifactID,
				HasDrift:    true,
				DriftID:     d.ID,
				Verdict:     d.Verdict,
				SpecDiff:    d.SpecDiff,
				ReconciledAt: d.ReconciledAt,
				CreatedAt:   d.CreatedAt,
			}, nil
		}))

	// resolve_drift — wraps O11 ResolveDrift orchestrator.
	reg.Add(BindOrchestrator("resolve_drift",
		"Operator gate action on a drift report: accept (artifact correct as-is) or reject (artifact wrong).",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"drift_id", "decision", "operator_id"},
			"properties": map[string]any{
				"drift_id":    map[string]any{"type": "integer"},
				"decision":    map[string]any{"type": "string", "enum": []string{"accept", "reject"}},
				"operator_id": map[string]any{"type": "string"},
				"note":        map[string]any{"type": "string"},
			},
		}),
		func(ctx context.Context, in orchestration.ResolveDriftInput) (*orchestration.ResolveDriftResult, error) {
			return orch.ResolveDrift(ctx, in)
		}))
}

// PipelineStatusInput is the input for pipeline_status.
type PipelineStatusInput struct {
	ArtifactID int64 `json:"artifact_id"`
}

// PipelineStatusResult is the output for pipeline_status.
type PipelineStatusResult struct {
	ArtifactID   int64  `json:"artifact_id"`
	HasDrift     bool   `json:"has_drift"`
	DriftID      int64  `json:"drift_id,omitempty"`
	Verdict      string `json:"verdict,omitempty"`
	SpecDiff     string `json:"spec_diff,omitempty"`
	ReconciledAt string `json:"reconciled_at,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
}