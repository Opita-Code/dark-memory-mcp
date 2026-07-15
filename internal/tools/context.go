// Package tools — context.go: the CONTEXT namespace (3 tools).
//
// Per RFC §5 / D-9:
//	dark_memory_artifact_context
//	dark_memory_spec_context
//	dark_memory_session_context
//
// All three are NEW read-only context projections (RFC D-5: "context
// objects, not row dumps"). They return a subset of the underlying
// row shaped for LLM consumption — fewer fields, friendlier names,
// and the canary state surfaced if relevant.
package tools

import (
	"context"

	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

// RegisterContext wires the 3 CONTEXT tools into the registry.
func RegisterContext(reg *Registry, _ /* orch */ interface{}, st store.Store) {
	// artifact_context — fetch an artifact row, project to a context shape.
	reg.Add(BindStore("artifact_context",
		"Return the context projection of an artifact: id, type, spec, brand, jurisdiction, disclosure, validation status, timestamps. Read-only.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"artifact_id"},
			"properties": map[string]any{
				"artifact_id": map[string]any{"type": "integer"},
			},
		}),
		st,
		func(ctx context.Context, s store.Store, in ArtifactContextInput) (*ArtifactContextResult, error) {
			a, err := s.GetArtifact(ctx, in.ArtifactID)
			if err != nil {
				return nil, err
			}
			if a == nil {
				return nil, store.ErrNotFound
			}
			return artifactContextFromRow(a), nil
		}))

	// spec_context — fetch a spec row, project to a context shape.
	reg.Add(BindStore("spec_context",
		"Return the context projection of a spec: id, case, constitution id+ver, intent (truncated), task count. Read-only.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"spec_id"},
			"properties": map[string]any{
				"spec_id": map[string]any{"type": "integer"},
			},
		}),
		st,
		func(ctx context.Context, s store.Store, in SpecContextInput) (*SpecContextResult, error) {
			sp, err := s.GetSpec(ctx, in.SpecID)
			if err != nil {
				return nil, err
			}
			if sp == nil {
				return nil, store.ErrNotFound
			}
			return specContextFromRow(sp), nil
		}))

	// session_context — fetch a session row, project to a context shape.
	reg.Add(BindStore("session_context",
		"Return the context projection of a session. Read-only.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"session_id"},
			"properties": map[string]any{
				"session_id": map[string]any{"type": "string"},
			},
		}),
		st,
		func(ctx context.Context, s store.Store, in SessionContextInput) (*SessionStatusResult, error) {
			sess, err := s.GetSession(ctx, in.SessionID)
			if err != nil {
				return nil, err
			}
			if sess == nil {
				return nil, store.ErrNotFound
			}
			return sessionStatusFromSession(sess), nil
		}))
}

// ArtifactContextInput is the input for artifact_context.
type ArtifactContextInput struct {
	ArtifactID int64 `json:"artifact_id"`
}

// ArtifactContextResult is the context projection of an artifact.
type ArtifactContextResult struct {
	ArtifactID      int64  `json:"artifact_id"`
	ArtifactURL     string `json:"artifact_url"`
	ArtifactType    string `json:"artifact_type"`
	SpecID          int64  `json:"spec_id"`
	BrandID         string `json:"brand_id,omitempty"`
	Jurisdiction    string `json:"jurisdiction,omitempty"`
	HasDisclosure   bool   `json:"has_disclosure"`
	ValidationStatus string `json:"validation_status"`
	SessionID       string `json:"session_id,omitempty"`
	CreatedAt       string `json:"created_at"`
}

func artifactContextFromRow(a *vibeflow.Artifact) *ArtifactContextResult {
	return &ArtifactContextResult{
		ArtifactID:       a.ID,
		ArtifactURL:      a.ArtifactURL,
		ArtifactType:     a.ArtifactType,
		SpecID:           a.SpecID,
		BrandID:          a.BrandID,
		Jurisdiction:     a.Jurisdiction,
		HasDisclosure:    a.HasDisclosure,
		ValidationStatus: a.ValidationStatus,
		SessionID:        a.SessionID,
		CreatedAt:        a.CreatedAt,
	}
}

// SpecContextInput is the input for spec_context.
type SpecContextInput struct {
	SpecID int64 `json:"spec_id"`
}

// SpecContextResult is the context projection of a spec. The intent
// field is truncated to 500 chars to keep the context window small;
// callers wanting the full intent should use vibe_spec → orchestrator
// VibeSpec → GetSpec.
type SpecContextResult struct {
	SpecID         int64  `json:"spec_id"`
	VibeCase       string `json:"vibe_case"`
	Constitution   string `json:"constitution,omitempty"`
	IntentPreview  string `json:"intent_preview,omitempty"` // first 500 chars of spec.Spec
	TaskCount      int    `json:"task_count"`
	CreatedAt      string `json:"created_at"`
}

func specContextFromRow(sp *vibeflow.Spec) *SpecContextResult {
	const previewLen = 500
	preview := sp.Spec
	if len(preview) > previewLen {
		preview = preview[:previewLen] + "..."
	}
	return &SpecContextResult{
		SpecID:        sp.ID,
		VibeCase:      sp.VibeCase,
		Constitution:  sp.Constitution,
		IntentPreview: preview,
		TaskCount:     countJSONTasks(sp.Tasks),
		CreatedAt:     sp.CreatedAt,
	}
}

// SessionContextInput is the input for session_context.
type SessionContextInput struct {
	SessionID string `json:"session_id"`
}

// countJSONTasks returns the task count from a JSON tasks blob. If
// the blob is empty or malformed, returns 0. Cheap parser: just
// counts "id" keys at top level.
func countJSONTasks(blob string) int {
	if blob == "" {
		return 0
	}
	// Lightweight: count occurrences of `"id":`. Not exact but cheap
	// and adequate for a context projection (the full count is
	// available via vibe_spec).
	n := 0
	for i := 0; i+5 < len(blob); i++ {
		if blob[i] == '"' && blob[i+1] == 'i' && blob[i+2] == 'd' && blob[i+3] == '"' && blob[i+4] == ':' {
			n++
		}
	}
	return n
}

// silence unused import (session is imported via sessionStatusFromSession)
var _ = session.StatusActive