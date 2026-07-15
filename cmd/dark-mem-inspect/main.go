// Package main is the dark-mem-inspect binary — a read-only
// diagnostic tool for dark-memory-mcp. Prints a one-shot report of
// the active Store: driver, schema version, table list, per-table
// counts, active constitution + drift status, recent write_audit
// rows, and a canary presence flag.
//
// Unlike dark-mem-cli (which mutates state via migrate / vacuum),
// dark-mem-inspect is purely diagnostic — it never calls Store.Migrate
// or Store.Vacuum. Safe to run against production at any time.
//
// Usage:
//
//	dark-mem-inspect [--driver=...] [--dsn=...] [--depth=N] [--json]
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/safety"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
)

// Exit codes (matching dark-mem-cli).
const (
	exitOK         = 0
	exitRuntimeErr = 1
	exitUsageErr   = 2
)

// Version is set at build time via -ldflags.
var Version = "0.1.0-dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		// No args: run the diagnostic against the default store
		// (matches the "single-shot report" use case — operator just
		// types `dark-mem-inspect` and gets the report).
	} else if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		printUsage(stdout)
		return exitOK
	}
	if len(args) > 0 && (args[0] == "version" || args[0] == "--version" || args[0] == "-v") {
		fmt.Fprintf(stdout, "dark-mem-inspect %s\n", Version)
		return exitOK
	}

	driver := ""
	dsn := ""
	jsonOut := false
	depth := 10
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--driver" && i+1 < len(args):
			i++
			driver = args[i]
		case len(a) > 9 && a[:9] == "--driver=":
			driver = a[9:]
		case a == "--dsn" && i+1 < len(args):
			i++
			dsn = args[i]
		case len(a) > 6 && a[:6] == "--dsn=":
			dsn = a[6:]
		case a == "--json":
			jsonOut = true
		case a == "--depth" && i+1 < len(args):
			i++
			n := 0
			for _, c := range args[i] {
				if c < '0' || c > '9' {
					fmt.Fprintf(stderr, "dark-mem-inspect: --depth not an integer: %q\n", args[i])
					return exitUsageErr
				}
				n = n*10 + int(c-'0')
			}
			depth = n
		default:
			fmt.Fprintf(stderr, "dark-mem-inspect: unknown flag %q\n", a)
			return exitUsageErr
		}
	}

	if driver == "" {
		driver = os.Getenv("DARK_DB_DRIVER")
	}
	if driver == "" {
		driver = "sqlite"
	}
	if dsn == "" {
		dsn = os.Getenv("DARK_DB")
	}
	if dsn == "" {
		dsn = "dark.db"
	}
	switch driver {
	case "sqlite", "postgres":
		// ok
	default:
		fmt.Fprintf(stderr, "dark-mem-inspect: invalid driver %q\n", driver)
		return exitUsageErr
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg := store.Config{Driver: store.Driver(driver), DSN: dsn}
	st, err := runtime.Open(ctx, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "dark-mem-inspect: open store: %v\n", err)
		return exitRuntimeErr
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			fmt.Fprintf(stderr, "dark-mem-inspect: close: %v\n", cerr)
		}
	}()

	// Best-effort: surface canary without exposing the token. We
	// construct a fresh Holder so we don't depend on the orchestrator
	// package; the Store doesn't expose its canary directly.
	h := &safety.Holder{}
	canaryPresent := !h.Active().IsZero()

	version, vErr := st.SchemaVersion(ctx)
	migrations, mErr := st.MigrationStatus(ctx)
	stats, sErr := st.Stats(ctx)
	writes, wErr := st.ListWrites(ctx, audit.ListFilters{Limit: depth})
	activeID, activeVer, activeSHA := st.ActiveConstitution(ctx)

	report := inspectReport{
		GeneratedAt:         time.Now().UTC().Format(time.RFC3339),
		Driver:              st.DriverName(),
		DSN:                 dsn,
		CanaryPresent:       canaryPresent,
		SchemaVersion:       version,
		Tables:              nil,
		Migrations:          nil,
		ActiveConstitution:  activeID,
		ActiveConstitutionV: activeVer,
		ActiveConstitutionSHA: activeSHA,
		RecentWrites:        nil,
		Errors:              nil,
	}
	if stats != nil {
		report.Tables = stats.Tables
	}
	if migrations != nil {
		report.Migrations = migrations
	}
	if writes != nil {
		report.RecentWrites = make([]inspectWrite, 0, len(writes))
		for _, w := range writes {
			report.RecentWrites = append(report.RecentWrites, inspectWrite{
				ID:            w.ID,
				Table:         w.TableName,
				RowID:         w.RowID,
				Actor:         w.Actor,
				SessionID:     w.SessionID,
				WritePath:     w.WritePath,
				ContentSHA256: w.ContentSHA256,
				CanaryPresent: w.CanaryPresent,
				CreatedAt:     w.CreatedAt,
			})
		}
	}
	if vErr != nil {
		report.Errors = append(report.Errors, "schema_version: "+vErr.Error())
	}
	if mErr != nil {
		report.Errors = append(report.Errors, "migrations: "+mErr.Error())
	}
	if sErr != nil {
		report.Errors = append(report.Errors, "stats: "+sErr.Error())
	}
	if wErr != nil {
		report.Errors = append(report.Errors, "writes: "+wErr.Error())
	}

	if jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintf(stderr, "dark-mem-inspect: encode json: %v\n", err)
			return exitRuntimeErr
		}
		return exitOK
	}

	// Human-readable report.
	fmt.Fprintf(stdout, "dark-mem-inspect report\n")
	fmt.Fprintf(stdout, "=======================\n")
	fmt.Fprintf(stdout, "generated_at:       %s\n", report.GeneratedAt)
	fmt.Fprintf(stdout, "driver:             %s\n", report.Driver)
	fmt.Fprintf(stdout, "dsn:                %s\n", report.DSN)
	fmt.Fprintf(stdout, "canary_present:     %t\n", report.CanaryPresent)
	fmt.Fprintf(stdout, "schema_version:     %d\n", report.SchemaVersion)
	fmt.Fprintf(stdout, "constitution:       id=%s ver=%s sha=%s\n",
		report.ActiveConstitution, report.ActiveConstitutionV, report.ActiveConstitutionSHA)

	if len(report.Tables) > 0 {
		fmt.Fprintf(stdout, "\ntables (%d):\n", len(report.Tables))
		for _, t := range report.Tables {
			fmt.Fprintf(stdout, "  - %s\n", t)
		}
	}

	if len(report.Migrations) > 0 {
		fmt.Fprintf(stdout, "\nmigrations:\n")
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  V\tNAME\tAPPLIED\tAPPLIED_AT")
		for _, m := range report.Migrations {
			fmt.Fprintf(tw, "  %d\t%s\t%t\t%s\n", m.Version, m.Name, m.Applied, m.AppliedAt)
		}
		_ = tw.Flush()
	}

	if len(report.RecentWrites) > 0 {
		fmt.Fprintf(stdout, "\nrecent writes (%d):\n", len(report.RecentWrites))
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  ID\tTABLE\tROW\tACTOR\tSESSION\tPATH\tCANARY\tAT")
		for _, w := range report.RecentWrites {
			canary := "-"
			if w.CanaryPresent {
				canary = "HIT"
			}
			fmt.Fprintf(tw, "  %d\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n",
				w.ID, w.Table, w.RowID, w.Actor, w.SessionID, w.WritePath, canary, w.CreatedAt)
		}
		_ = tw.Flush()
	}

	if len(report.Errors) > 0 {
		fmt.Fprintf(stdout, "\nerrors (degraded report — %d):\n", len(report.Errors))
		for _, e := range report.Errors {
			fmt.Fprintf(stdout, "  - %s\n", e)
		}
	}
	return exitOK
}

func printUsage(w *os.File) {
	fmt.Fprintf(w, `dark-mem-inspect %s — read-only diagnostic for dark-memory-mcp

Usage:
  dark-mem-inspect [--driver=sqlite|postgres] [--dsn=PATH|URL]
                   [--depth=N] [--json]

Flags:
  --driver=sqlite|postgres   Default: $DARK_DB_DRIVER or sqlite.
  --dsn=<path|url>            Default: $DARK_DB or ./dark.db.
  --depth=N                   Number of recent write_audit rows to show (default 10).
  --json                      Emit JSON instead of human-readable report.

Reads: schema version, applied migrations, table list, per-table counts,
       active constitution + SHA, last N write_audit rows, canary presence.
Never writes: no migration, no vacuum, no modification of any kind.
Safe to run against production.
`, Version)
}

// inspectReport is the structured shape returned by --json. Mirrors
// what humans see, but in a parseable form for alerting / dashboards.
type inspectReport struct {
	GeneratedAt           string                `json:"generated_at"`
	Driver                string                `json:"driver"`
	DSN                   string                `json:"dsn"`
	CanaryPresent         bool                  `json:"canary_present"`
	SchemaVersion         int                   `json:"schema_version"`
	Tables                []string              `json:"tables"`
	Migrations            []store.MigrationStatus `json:"migrations"`
	ActiveConstitution    string                `json:"active_constitution_id"`
	ActiveConstitutionV   string                `json:"active_constitution_version"`
	ActiveConstitutionSHA string                `json:"active_constitution_sha256"`
	RecentWrites          []inspectWrite        `json:"recent_writes"`
	Errors                []string              `json:"errors,omitempty"`
}

// inspectWrite is one row from the recent_writes listing (subset of
// audit.WriteEvent, sufficient for a diagnostic report).
type inspectWrite struct {
	ID            int64  `json:"id"`
	Table         string `json:"table"`
	RowID         int64  `json:"row_id"`
	Actor         string `json:"actor"`
	SessionID     string `json:"session_id,omitempty"`
	WritePath     string `json:"write_path,omitempty"`
	ContentSHA256 string `json:"content_sha256,omitempty"`
	CanaryPresent bool   `json:"canary_present"`
	CreatedAt     string `json:"created_at"`
}