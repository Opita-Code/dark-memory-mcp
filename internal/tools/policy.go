// Package tools — policy.go: the POLICY namespace (2 tools).
//
// Per RFC §5 / D-9:
//	dark_memory_active_policy
//	dark_memory_load_constitution
//
// Maps to orchestrator O9 (ActivePolicy) + 1 new read-only tool
// (load_constitution fetches a constitution row by id+ver via
// Store.GetConstitution).
package tools

import (
	"context"

	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// RegisterPolicy wires the 2 POLICY tools into the registry.
func RegisterPolicy(reg *Registry, orch *orchestration.Orchestrator, st store.Store) {
	// active_policy — wraps O9 ActivePolicy orchestrator.
	reg.Add(BindOrchestrator("active_policy",
		"Return the active policy snapshot: constitution id+ver+sha, drift status, active mods, canary presence. Read-only.",
		MustJSONSchema(map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
		func(ctx context.Context, in struct{}) (*orchestration.ActivePolicyResult, error) {
			return orch.ActivePolicy(ctx)
		}))

	// load_constitution — read-only fetch of a constitution by id+ver.
	reg.Add(BindStore("load_constitution",
		"Return the full constitution row (label, source, parsed JSON) for the given id+version. Read-only.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"constitution_id"},
			"properties": map[string]any{
				"constitution_id": map[string]any{"type": "string"},
				"version":         map[string]any{"type": "string", "description": "Version string. Empty = latest."},
			},
		}),
		st,
		func(ctx context.Context, s store.Store, in LoadConstitutionInput) (*LoadConstitutionResult, error) {
			c, err := s.GetConstitution(ctx, in.ConstitutionID, in.Version)
			if err != nil {
				return nil, err
			}
			if c == nil {
				return nil, store.ErrNotFound
			}
			return &LoadConstitutionResult{
				ConstitutionID: c.ConstitutionID,
				Version:        c.Version,
				Label:          c.Label,
				Source:         c.Source,
				ParsedJSON:     c.ParsedJSON,
				SHA256:         c.SHA256,
				CreatedAt:      c.CreatedAt,
			}, nil
		}))
}

// LoadConstitutionInput is the input for load_constitution.
type LoadConstitutionInput struct {
	ConstitutionID string `json:"constitution_id"`
	Version        string `json:"version,omitempty"`
}

// LoadConstitutionResult is the output for load_constitution.
type LoadConstitutionResult struct {
	ConstitutionID string `json:"constitution_id"`
	Version        string `json:"version"`
	Label          string `json:"label,omitempty"`
	Source         string `json:"source,omitempty"`
	ParsedJSON     string `json:"parsed_json,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
}