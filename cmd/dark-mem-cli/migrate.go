// migrate.go — dark-mem-cli migrate subcommand. Runs pending schema
// migrations on the active Store and reports what was applied.
//
// Usage:
//
//	dark-mem-cli migrate [--driver=sqlite|postgres] [--dsn=PATH|URL] [--json]
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

func runMigrate(ctx context.Context, args []string, stdout, stderr *os.File) int {
	flags, _, err := parseCommonFlags(args)
	if err != nil {
		fmt.Fprintf(stderr, "dark-mem-cli migrate: %v\n", err)
		return exitUsageErr
	}

	cfg, err := loadConfig(flags)
	if err != nil {
		fmt.Fprintf(stderr, "dark-mem-cli migrate: %v\n", err)
		return exitUsageErr
	}

	st, err := openStore(ctx, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "dark-mem-cli migrate: %v\n", err)
		return exitRuntimeErr
	}
	defer closeStore(st, stderr)

	before, err := st.MigrationStatus(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "dark-mem-cli migrate: status before: %v\n", err)
		return exitRuntimeErr
	}

	if err := st.Migrate(ctx); err != nil {
		fmt.Fprintf(stderr, "dark-mem-cli migrate: apply: %v\n", err)
		return exitRuntimeErr
	}

	after, err := st.MigrationStatus(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "dark-mem-cli migrate: status after: %v\n", err)
		return exitRuntimeErr
	}

	applied := diffMigrationStatus(before, after)
	beforeVersion := versionFromStatus(before)
	afterVersion := versionFromStatus(after)

	if flags.JSON {
		out := migrateJSON{
			BeforeVersion: beforeVersion,
			AfterVersion:  afterVersion,
			Applied:       applied,
			AppliedCount:  len(applied),
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			fmt.Fprintf(stderr, "dark-mem-cli migrate: encode json: %v\n", err)
			return exitRuntimeErr
		}
		return exitOK
	}

	if len(applied) == 0 {
		fmt.Fprintf(stdout, "dark-mem-cli migrate: nothing to apply (schema version=%d)\n", afterVersion)
		return exitOK
	}

	fmt.Fprintf(stdout, "dark-mem-cli migrate: applied %d migration(s); schema %d → %d\n",
		len(applied), beforeVersion, afterVersion)
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  VERSION\tNAME\tAPPLIED_AT")
	for _, s := range after {
		for _, a := range applied {
			if s.Name == a {
				fmt.Fprintf(tw, "  %d\t%s\t%s\n", s.Version, s.Name, s.AppliedAt)
				break
			}
		}
	}
	_ = tw.Flush()
	return exitOK
}

type migrateJSON struct {
	BeforeVersion int      `json:"before_version"`
	AfterVersion  int      `json:"after_version"`
	Applied       []string `json:"applied"`
	AppliedCount  int      `json:"applied_count"`
}

func diffMigrationStatus(before, after []store.MigrationStatus) []string {
	beforeSet := make(map[string]bool, len(before))
	for _, s := range before {
		if s.Applied {
			beforeSet[s.Name] = true
		}
	}
	applied := make([]string, 0)
	for _, s := range after {
		if s.Applied && !beforeSet[s.Name] {
			applied = append(applied, s.Name)
		}
	}
	return applied
}

func versionFromStatus(statuses []store.MigrationStatus) int {
	max := 0
	for _, s := range statuses {
		if s.Version > max {
			max = s.Version
		}
	}
	return max
}