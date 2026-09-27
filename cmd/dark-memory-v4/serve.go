// dark-memory-v4 serve subcommand — boot the v4 MCP server.
//
// BUG-6 (2026-09-27): this is the SKELETON boot. It:
//
//  1. Opens the dark-db through store.OpenSQLite (INV-16 — the
//     BUG-5 pragma set + bounded pool).
//  2. Runs CreateSchema for every v4alpha package (idempotent;
//     safe on a fresh DB and on an already-initialised one).
//  3. Installs a SIGINT/SIGTERM handler that closes the DB and
//     surfaces the elapsed uptime on exit.
//  4. Blocks until the context is cancelled.
//
// BUG-7 will wire the JSON-RPC transport (mcp-go) + 75-tool
// registry. Until then `serve` is a readiness/liveness probe: it
// proves the boot path works end-to-end (INV-16 pragmas applied,
// all schemas applied, shutdown clean) without serving any
// requests. This is the same staged rollout the v3.0 dark-mem-mcp
// used (main.go → legacyMain → dispatcher → bridge/daemon, spec
// 1176 §4.10).
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/manifest"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/session"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/vibe"
)

func runServe(ctx context.Context, args []string, stdout, stderr *os.File) int {
	flags, err := parseCommonFlags(args)
	if err != nil {
		fmt.Fprintln(stderr, "dark-memory-v4: serve:", err)
		return exitUsageErr
	}
	dsn, err := resolveDSN(flags, stderr)
	if err != nil {
		return exitUsageErr
	}

	// 1. Open dark-db with INV-16 pragmas + bounded pool.
	db, err := store.OpenSQLite(ctx, dsn)
	if err != nil {
		fmt.Fprintf(stderr, "dark-memory-v4: serve: open %s: %v\n", dsn, err)
		return exitRuntimeErr
	}

	// 2. Run every CreateSchema (idempotent).
	if err := applyAllSchemas(ctx, db); err != nil {
		fmt.Fprintf(stderr, "dark-memory-v4: serve: schema: %v\n", err)
		_ = db.Close()
		return exitRuntimeErr
	}

	// 3. Stamp the schema_migrations marker so a follow-up
	// `dark-memory-v4 schema-status` confirms the boot.
	if err := stampSchemaVersion(ctx, db, schemaVersion); err != nil {
		fmt.Fprintf(stderr, "dark-memory-v4: serve: stamp version: %v\n", err)
		_ = db.Close()
		return exitRuntimeErr
	}

	startedAt := time.Now().UTC()
	bootReport := bootStatus{
		DSN:            dsn,
		SchemaVersion:  schemaVersion,
		StartedAt:      startedAt.Format(time.RFC3339Nano),
		GoVersion:      goVersion(),
		ServerVersion:  Version,
		Operator:       defaultOperator(),
		Notes:          []string{"BUG-6 skeleton — JSON-RPC transport lands in BUG-7"},
	}

	if flags.JSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(bootReport)
	} else {
		fmt.Fprintf(stdout, "dark-memory-v4 serve\n")
		fmt.Fprintf(stdout, "  dsn             %s\n", bootReport.DSN)
		fmt.Fprintf(stdout, "  schema_version  %s\n", bootReport.SchemaVersion)
		fmt.Fprintf(stdout, "  go_version      %s\n", bootReport.GoVersion)
		fmt.Fprintf(stdout, "  server_version  %s\n", bootReport.ServerVersion)
		fmt.Fprintf(stdout, "  operator        %s\n", bootReport.Operator)
		fmt.Fprintf(stdout, "  started_at      %s\n", bootReport.StartedAt)
		fmt.Fprintf(stdout, "  ready\n")
		fmt.Fprintf(stdout, "  (waiting for SIGINT/SIGTERM — JSON-RPC transport lands in BUG-7)\n")
	}

	// 4. Block until ctx is cancelled (SIGINT/SIGTERM via the
	// NotifyContext in run()).
	<-ctx.Done()
	elapsed := time.Since(startedAt)
	fmt.Fprintf(stderr, "dark-memory-v4: serve: shutdown after %s (%v)\n",
		elapsed.Truncate(time.Millisecond), ctx.Err())
	if err := db.Close(); err != nil {
		fmt.Fprintf(stderr, "dark-memory-v4: serve: close: %v\n", err)
		return exitRuntimeErr
	}
	return exitOK
}

// applyAllSchemas runs every v4alpha CreateSchema. Extracted so
// migrate.go can reuse it.
func applyAllSchemas(ctx context.Context, db *sql.DB) error {
	if err := audit.CreateSchema(db); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	if err := session.CreateSchema(db); err != nil {
		return fmt.Errorf("session: %w", err)
	}
	if err := manifest.CreateCapSchema(ctx, db); err != nil {
		return fmt.Errorf("manifest/cap: %w", err)
	}
	if err := manifest.CreateManifestSchema(ctx, db); err != nil {
		return fmt.Errorf("manifest/meta: %w", err)
	}
	if err := vibe.CreateSpecSchema(db); err != nil {
		return fmt.Errorf("vibe/spec: %w", err)
	}
	if err := vibe.CreateArtifactSchema(db); err != nil {
		return fmt.Errorf("vibe/artifact: %w", err)
	}
	if err := vibe.CreateDriftSchema(db); err != nil {
		return fmt.Errorf("vibe/drift: %w", err)
	}
	return nil
}

type bootStatus struct {
	DSN           string   `json:"dsn"`
	SchemaVersion string   `json:"schema_version"`
	StartedAt     string   `json:"started_at"`
	GoVersion     string   `json:"go_version"`
	ServerVersion string   `json:"server_version"`
	Operator      string   `json:"operator"`
	Notes         []string `json:"notes"`
}

func goVersion() string {
	// Cheap: runtime.Version() returns "go1.25.5" on the build host.
	// Embedded in the boot report so the operator can pin to a
	// specific build without inspecting the binary.
	return goVersionString
}

func defaultOperator() string {
	if v := os.Getenv("DARK_OPERATOR"); v != "" {
		return v
	}
	return "dark-agent"
}
