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
	"fmt"

	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/vibecase"
)

// vibeSpecTaskSchema is the per-item JSON Schema for the `tasks`
// array of vibe_spec / vibe_publish. Strict by design: any field
// outside this list is rejected at unmarshal time (F35 — see CHANGELOG
// v1.2.0), so the harness cannot silently drop or coerce unknown
// fields, and the operator gets a precise *json.UnmarshalTypeError
// pointing at the offending key.
//
// Mapping to orchestration.VibeSpecTask (see internal/orchestration/
// vibe_spec.go): ID is the canonical task id; Description is the
// human-readable task body; DependsOn is the list of in-spec task ids
// this task blocks on (plus "ext:..." refs); Owner is the optional
// operator alias responsible for the task.
var vibeSpecTaskSchema = map[string]any{
	"type": "object",
	"additionalProperties": false,
	"required": []string{"id", "description"},
	"properties": map[string]any{
		"id":          map[string]any{"type": "string", "minLength": 1},
		"description": map[string]any{"type": "string", "minLength": 1},
		"depends_on": map[string]any{
			"type":  "array",
			"items": map[string]any{"type": "string"},
		},
		"owner": map[string]any{"type": "string"},
	},
}

// RegisterVibe wires the 4 VIBE tools into the registry.
func RegisterVibe(reg *Registry, orch *orchestration.Orchestrator, st store.Store) {
	// vibe_publish — wraps O7 PublishVibe meta-orchestrator.
	//
	// Schema correctness note (F33 — see CHANGELOG v1.2.0): the
	// PublishVibeInput struct (internal/orchestration/publish_vibe.go)
	// nests the spec body under a `spec` object and the artifact body
	// under an `artifact` object. Earlier versions of this schema
	// declared those sub-trees as flat top-level strings, which made
	// every harness call fail with "cannot unmarshal string into Go
	// struct field PublishVibeInput.spec of type
	// orchestration.PublishSpecInput". The fix is to declare the
	// nesting explicitly in the JSON Schema so the harness serializes
	// the input with the same shape the Go server expects.
	reg.Add(BindOrchestrator("vibe_publish",
		"Publish an artifact under a spec. Runs the full vibe-flow loop: spec_create + artifact_log + brand_match (optional) + compliance_check (optional) + drift_judge + drift_log. Returns verdict + next_action.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"artifact", "spec"},
			"properties": map[string]any{
				"spec": map[string]any{
					"type":     "object",
					"required": []string{"vibe_case"},
					"additionalProperties": false,
					"properties": map[string]any{
						"vibe_case": map[string]any{
							"type": "string",
							// v1.4.1: enum now derived from the
							// canonical vibecase package — single
							// source of truth shared with vibe_spec.
							"enum": vibecase.JSONSchemaEnum(),
						},
						"constitution": map[string]any{"type": "string", "description": "Optional JSON constitution blob."},
						"spec":         map[string]any{"type": "string", "description": "Optional JSON intent blob."},
						"tasks": map[string]any{
							"type":  "string",
							"description": "Optional JSON-encoded array of VibeSpecTask objects. Pass the array as a JSON string; see vibe_spec for the structured form.",
						},
					},
				},
				"artifact": map[string]any{
					"type":     "object",
					"required": []string{"artifact_url", "artifact_type"},
					"additionalProperties": false,
					"properties": map[string]any{
						"artifact_url":   map[string]any{"type": "string"},
						"artifact_type":  map[string]any{"type": "string", "enum": []string{"code", "text", "image", "video", "audio", "multi"}},
						"text":           map[string]any{"type": "string", "description": "Artifact body (required for drift_judge)."},
						"brand_id":       map[string]any{"type": "string"},
						"jurisdiction":   map[string]any{"type": "string"},
						"has_disclosure": map[string]any{"type": "boolean"},
					},
				},
				"auto_drift_check": map[string]any{"type": "boolean", "description": "Default true. Set false to skip drift_judge."},
				"session_id":       map[string]any{"type": "string"},
			},
		}),
		func(ctx context.Context, in orchestration.PublishVibeInput) (*orchestration.PublishResult, error) {
			return orch.PublishVibe(ctx, in)
		}))

	// vibe_spec — wraps O12 VibeSpec orchestrator.
	//
	// Schema correctness note (F33 — see CHANGELOG v1.2.0): the items
	// schema is now strict (`additionalProperties: false` + explicit
	// property list) so the harness cannot silently drop or coerce
	// fields like `title`, `status`, or `priority` that the Go struct
	// does not accept. Earlier versions used `{"type":"object"}`
	// with no property list, which led to confusing unmarshal errors
	// ("cannot unmarshal string into depends_on of type []string")
	// when callers passed task objects with extra fields.
	//
	// F36 (v1.2.1): `tasks` accepts EITHER `type: "array"` (preferred,
	// strictly validated against vibeSpecTaskSchema) OR `type: "string"`
	// (a JSON-encoded array, kept for compatibility with the gemela
	// `dark_research_spec_create` tool whose `tasks` field is an opaque
	// string). The orchestrator's parseTasksField dispatches on the
	// payload's leading byte.
	reg.Add(BindOrchestrator("vibe_spec",
		"Create a new spec with structured task validation (unique ids, no cycles, depends_on consistency).",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"vibe_case", "tasks"},
			"additionalProperties": false,
			"properties": map[string]any{
				// v1.4.1: vibe_spec now enforces the same C1..C7
				// enum constraint as vibe_publish, closing the
				// asymmetry. Both layers derive from vibecase.
				"vibe_case": map[string]any{
					"type": "string",
					"enum": vibecase.JSONSchemaEnum(),
				},
				"constitution": map[string]any{"type": "string"},
				"spec":         map[string]any{"type": "string"},
				"tasks": map[string]any{
					"anyOf": []any{
						// Form A: JSON array (preferred).
						map[string]any{
							"type":     "array",
							"minItems": 1,
							"items":    vibeSpecTaskSchema,
						},
						// Form B: JSON-encoded string of an array
						// (legacy dark_research_spec_create compatibility).
						map[string]any{"type": "string", "minLength": 2},
					},
				},
				"session_id": map[string]any{"type": "string"},
			},
		}),
		func(ctx context.Context, in orchestration.VibeSpecInput) (*orchestration.VibeSpecResult, error) {
			return orch.VibeSpec(ctx, in)
		}))

	// pipeline_status — read-only: latest drift for an artifact_id.
	// F7 enhancement: when no local drift is found AND a federation peer
	// is configured, attempt a peer lookup and surface the result in a
	// `cross_namespace_hint` field. The local response shape is
	// backward-compatible (the new field is omitempty).
	reg.Add(BindStore("pipeline_status",
		"Return the latest drift report for an artifact_id (or nil if none). Read-only. With DARK_FEDERATION_PEER_DSN configured, also probes the dark-research peer when the local lookup misses.",
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
			if d != nil {
				return &PipelineStatusResult{
					ArtifactID:  in.ArtifactID,
					HasDrift:    true,
					DriftID:     d.ID,
					Verdict:     d.Verdict,
					SpecDiff:    d.SpecDiff,
					ReconciledAt: d.ReconciledAt,
					CreatedAt:   d.CreatedAt,
				}, nil
			}
			// Local miss. If a federation peer is configured, attempt
			// a cross-namespace lookup so the operator learns WHERE
			// the artifact actually lives (without merging data).
			res := &PipelineStatusResult{ArtifactID: in.ArtifactID, HasDrift: false}
			peer := GetFederationPeer()
			if peer != nil && peer.IsEnabled() {
				art, perr := peer.LookupArtifact(ctx, in.ArtifactID)
				if perr == nil && art != nil {
					res.CrossNamespaceHint = fmt.Sprintf(
						"artifact_id=%d not found in local dark-memory.db, but the dark-research peer reports it exists (peer_validation_status=%s, peer_artifact_url=%q). Use dark_memory_federation_lookup or dark_research_pipeline_status to inspect cross-namespace.",
						in.ArtifactID, art.ValidationStatus, art.ArtifactURL,
					)
				}
			}
			return res, nil
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
	ArtifactID         int64  `json:"artifact_id"`
	HasDrift           bool   `json:"has_drift"`
	DriftID            int64  `json:"drift_id,omitempty"`
	Verdict            string `json:"verdict,omitempty"`
	SpecDiff           string `json:"spec_diff,omitempty"`
	ReconciledAt       string `json:"reconciled_at,omitempty"`
	CreatedAt          string `json:"created_at,omitempty"`
	// CrossNamespaceHint is populated ONLY when (a) no local drift was
	// found, AND (b) a federation peer is configured, AND (c) the peer
	// reports the artifact exists. It is a metadata-only signal: no peer
	// data is copied into the local response. F7.
	CrossNamespaceHint string `json:"cross_namespace_hint,omitempty"`
}