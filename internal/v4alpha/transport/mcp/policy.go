// policy_* tools — read-only views over the constitution and
// the active policy snapshot. The v4 has no policy_registry
// table yet (deferred to BUG-9); these tools derive everything
// from the audit_log + schema_migrations + sqlite_master.
//
//   - dark_memory_active_policy   — current active policy snapshot
//   - dark_memory_load_constitution — load the active constitution
//
// Both tools are read-only and have no side effects.
package mcp

import (
	"context"
	"database/sql"

	"github.com/mark3labs/mcp-go/mcp"
)

const (
	activePolicyToolName    = "dark_memory_active_policy"
	loadConstitutionToolName = "dark_memory_load_constitution"
)

func registerPolicyTools(s *Server) {
	registerActivePolicy(s)
	registerLoadConstitution(s)
}

// --- active_policy ---

type activePolicyInput struct{}

type activePolicyOutput struct {
	Driver         string   `json:"driver"`
	SchemaVersion  string   `json:"schema_version"`
	AppliedAt      string   `json:"applied_at"`
	MigrationsCount int     `json:"migrations_count"`
	ConstitutionID string   `json:"constitution_id,omitempty"`
	ConstitutionVer string  `json:"constitution_version,omitempty"`
	Active         bool     `json:"active"`
	Notes          []string `json:"notes,omitempty"`
}

func registerActivePolicy(s *Server) {
	tool := mcp.NewTool(activePolicyToolName,
		mcp.WithDescription("Current active policy snapshot. Read-only."),
		mcp.WithInputSchema[activePolicyInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		out := activePolicyOutput{
			Driver: "sqlite",
			Active: true,
		}
		// Last applied migration.
		var version string
		var appliedAt string
		row := s.db.QueryRowContext(ctx,
			`SELECT version, applied_at FROM schema_migrations ORDER BY id DESC LIMIT 1`)
		if err := row.Scan(&version, &appliedAt); err == nil {
			out.SchemaVersion = version
			out.AppliedAt = appliedAt
		}
		// Total migrations count.
		_ = s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM schema_migrations`).Scan(&out.MigrationsCount)
		// Constitution: v4-alpha.1 default (no constitution table yet).
		out.ConstitutionID = "dark-cli/v4-alpha.1"
		out.ConstitutionVer = "v4-alpha.1"
		out.Notes = []string{
			"v4-alpha.1 has no policy_registry table; constitution is hard-coded to the v4 distribution default.",
			"Full policy_registry lands in BUG-9.",
		}
		return resultJSON(out)
	})
}

// --- load_constitution ---

// loadConstitutionOutput mirrors the v3.0 constitution row shape
// (label, source, parsed_json). The v4-alpha.1 returns a hard-
// coded stub; the real parser lands in BUG-9.
type loadConstitutionInput struct {
	ConstitutionID string `json:"constitution_id,omitempty" jsonschema_description:"Defaults to the v4-alpha.1 distribution constitution"`
	Version        string `json:"version,omitempty" jsonschema_description:"Defaults to latest"`
}

type loadConstitutionOutput struct {
	ConstitutionID string         `json:"constitution_id"`
	Version        string         `json:"version"`
	Source         string         `json:"source"`
	Parsed         map[string]any `json:"parsed"`
}

func registerLoadConstitution(s *Server) {
	tool := mcp.NewTool(loadConstitutionToolName,
		mcp.WithDescription("Load a constitution by id+version. v4-alpha.1 returns the hard-coded default."),
		mcp.WithInputSchema[loadConstitutionInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in loadConstitutionInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		constitutionID := in.ConstitutionID
		if constitutionID == "" {
			constitutionID = "dark-cli/v4-alpha.1"
		}
		version := in.Version
		if version == "" {
			version = "v4-alpha.1"
		}
		out := loadConstitutionOutput{
			ConstitutionID: constitutionID,
			Version:        version,
			Source:         "embedded:dark-cli/v4-alpha.1",
			Parsed: map[string]any{
				"invariants": map[string]bool{
					"INV-1":  true, // audit_log is durable + non-rewriteable
					"INV-7":  true, // project_id filter on reads
					"INV-16": true, // dark-db concurrency contract
					"INV-17": true, // agent_memory canonical kind + operator scope
				},
				"mods": map[string]any{
					"canary":  false,
					"redteam": false,
				},
				"driver": "sqlite",
				"policy_notes": []string{
					"Default behavior: tools mutate the DB; the audit_log captures every change.",
					"FTS5 sync inside SERIALIZABLE Tx prevents read-after-write inconsistency.",
				},
			},
		}
		return resultJSON(out)
	})
}

// --- helper: schema_migrations existence check used by both tools ---

// schemaMigrationsExists is exported for tests; production tools
// always assume the table exists (CreateSchema applied).
func (s *Server) schemaMigrationsExists(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`).Scan(&n)
	if err == sql.ErrNoRows || err != nil {
		return false, err
	}
	return n > 0, nil
}
