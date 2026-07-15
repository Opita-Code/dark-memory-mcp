// schema_status.go — dark-mem-cli schema-status subcommand. Prints
// the current schema version, applied migrations, and table list
// (read-only).
//
// Usage:
//
//	dark-mem-cli schema-status [--driver=...] [--dsn=...] [--json]
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
)

func runSchemaStatus(ctx context.Context, args []string, stdout, stderr *os.File) int {
	flags, _, err := parseCommonFlags(args)
	if err != nil {
		fmt.Fprintf(stderr, "dark-mem-cli schema-status: %v\n", err)
		return exitUsageErr
	}

	cfg, err := loadConfig(flags)
	if err != nil {
		fmt.Fprintf(stderr, "dark-mem-cli schema-status: %v\n", err)
		return exitUsageErr
	}

	st, err := openStore(ctx, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "dark-mem-cli schema-status: %v\n", err)
		return exitRuntimeErr
	}
	defer closeStore(st, stderr)

	version, err := st.SchemaVersion(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "dark-mem-cli schema-status: version: %v\n", err)
		return exitRuntimeErr
	}
	migrations, err := st.MigrationStatus(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "dark-mem-cli schema-status: migrations: %v\n", err)
		return exitRuntimeErr
	}
	stats, err := st.Stats(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "dark-mem-cli schema-status: stats: %v\n", err)
		return exitRuntimeErr
	}

	if flags.JSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(map[string]any{
			"driver":         st.DriverName(),
			"schema_version": version,
			"tables":         stats.Tables,
			"migrations":     migrations,
		}); err != nil {
			fmt.Fprintf(stderr, "dark-mem-cli schema-status: encode json: %v\n", err)
			return exitRuntimeErr
		}
		return exitOK
	}

	fmt.Fprintf(stdout, "driver: %s\n", st.DriverName())
	fmt.Fprintf(stdout, "schema_version: %d\n", version)
	fmt.Fprintf(stdout, "tables (%d):\n", len(stats.Tables))
	for _, t := range stats.Tables {
		fmt.Fprintf(stdout, "  - %s\n", t)
	}

	fmt.Fprintf(stdout, "\nmigrations (%d total):\n", len(migrations))
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  VERSION\tNAME\tAPPLIED\tAPPLIED_AT")
	applied := 0
	for _, m := range migrations {
		appliedAt := "-"
		if m.Applied {
			appliedAt = m.AppliedAt
			applied++
		}
		fmt.Fprintf(tw, "  %d\t%s\t%t\t%s\n", m.Version, m.Name, m.Applied, appliedAt)
	}
	_ = tw.Flush()
	fmt.Fprintf(stdout, "\napplied: %d / %d\n", applied, len(migrations))
	return exitOK
}