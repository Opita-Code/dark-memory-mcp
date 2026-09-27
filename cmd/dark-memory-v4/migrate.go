// dark-memory-v4 migrate subcommand — applies CreateSchema for every
// v4alpha package against the dark-db at the configured DSN.
//
// Each CreateSchema function is idempotent (CREATE TABLE IF NOT
// EXISTS), so running migrate on an already-initialised DB is safe
// and a no-op for existing tables. Schema versions are stamped via
// the standard schema_migrations table for ordering, but the v4alpha
// packages do not depend on that table for correctness — their
// CREATE TABLE IF NOT EXISTS statements handle ordering naturally.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/judge"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/manifest"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/session"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/vibe"
)

// schemaVersion is the v4alpha marker stamped into schema_migrations
// on a successful migrate. Bumped whenever a CreateSchema changes
// shape or adds a column.
const schemaVersion = "v4alpha/2026-09-27/002" // C3: + sdd_evaluations

// runMigrate applies every v4alpha CreateSchema in dependency order.
// Order matters: session must precede audit (audit.Write emits FK-like
// references to session); manifest depends on session (cap tokens
// reference operator ids); vibe depends on session (spec/artifacts
// reference session_id).
func runMigrate(ctx context.Context, args []string, stdout, stderr *os.File) int {
	flags, err := parseCommonFlags(args)
	if err != nil {
		fmt.Fprintln(stderr, "dark-memory-v4: migrate:", err)
		return exitUsageErr
	}
	dsn, err := resolveDSN(flags, stderr)
	if err != nil {
		return exitUsageErr
	}

	db, err := store.OpenSQLite(ctx, dsn)
	if err != nil {
		fmt.Fprintf(stderr, "dark-memory-v4: migrate: open %s: %v\n", dsn, err)
		return exitRuntimeErr
	}
	defer db.Close()

	steps := migrateSteps(db)
	for _, s := range steps {
		if err := s.Fn(ctx); err != nil {
			fmt.Fprintf(stderr, "dark-memory-v4: migrate: %s: %v\n", s.Name, err)
			return exitRuntimeErr
		}
		if !flags.JSON {
			fmt.Fprintf(stdout, "  ok  %s\n", s.Name)
		}
	}

	// Stamp the schema_migrations marker so schema-status can
	// confirm the initialised state. This is informational, not
	// authoritative; the per-table CREATE TABLE IF NOT EXISTS
	// statements are the source of truth.
	if err := stampSchemaVersion(ctx, db, schemaVersion); err != nil {
		fmt.Fprintf(stderr, "dark-memory-v4: migrate: stamp version: %v\n", err)
		return exitRuntimeErr
	}

	if flags.JSON {
		names := make([]string, len(steps))
		for i, s := range steps {
			names[i] = s.Name
		}
		out := map[string]any{
			"dsn":            dsn,
			"schema_version": schemaVersion,
			"steps":          names,
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			fmt.Fprintf(stderr, "dark-memory-v4: migrate: json: %v\n", err)
			return exitRuntimeErr
		}
	} else {
		fmt.Fprintf(stdout, "dark-memory-v4: migrate ok at version %s\n", schemaVersion)
	}
	return exitOK
}

// migrateStep is one CreateSchema invocation. Exported as a type
// so serve.go's applyAllSchemas can mirror the order without
// duplicating the function literals.
type migrateStep struct {
	Name string
	Fn   func(context.Context) error
}

// migrateSteps returns the canonical ordered list. Order matches
// the dependency graph: session first, audit second, manifest
// third, vibe last. Mirrored in serve.go::applyAllSchemas.
func migrateSteps(db *sql.DB) []migrateStep {
	return []migrateStep{
		{"audit", func(ctx context.Context) error { return audit.CreateSchema(db) }},
		{"session", func(ctx context.Context) error { return session.CreateSchema(db) }},
		{"agent_memory", func(ctx context.Context) error { return agent_memory.CreateSchema(db) }},
		{"manifest/cap", func(ctx context.Context) error { return manifest.CreateCapSchema(ctx, db) }},
		{"manifest/meta", func(ctx context.Context) error { return manifest.CreateManifestSchema(ctx, db) }},
		{"vibe/spec", func(ctx context.Context) error { return vibe.CreateSpecSchema(db) }},
		{"vibe/artifact", func(ctx context.Context) error { return vibe.CreateArtifactSchema(db) }},
		{"vibe/drift", func(ctx context.Context) error { return vibe.CreateDriftSchema(db) }},
		{"judge", func(ctx context.Context) error { return judge.CreateSchema(db) }}, // ADR-007 C3
	}
}

func stampSchemaVersion(ctx context.Context, db *sql.DB, version string) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version   TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		);
		INSERT OR IGNORE INTO schema_migrations(version) VALUES (?);
	`, version)
	if err != nil {
		return fmt.Errorf("stamp schema_migrations: %w", err)
	}
	return nil
}

// sentinel error used by migrate's JSON branch when the encoder
// silently fails after a successful migrate.
var errEncode = errors.New("encode")
