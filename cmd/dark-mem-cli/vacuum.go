// vacuum.go — dark-mem-cli vacuum subcommand. Runs Store.Vacuum with
// the operator-supplied policy (tables, days_old, dry_run).
//
// SQLite: reclaims space (VACUUM). Postgres: no-op (autovacuum handles
// GC) but the policy filters + dry-run still report counts.
//
// Usage:
//
//	dark-mem-cli vacuum [--driver=...] [--dsn=...] [--days-old=N]
//	                    [--tables=t1,t2,...] [--dry-run] [--json]
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

func runVacuum(ctx context.Context, args []string, stdout, stderr *os.File) int {
	flags, remaining, err := parseCommonFlags(args)
	if err != nil {
		fmt.Fprintf(stderr, "dark-mem-cli vacuum: %v\n", err)
		return exitUsageErr
	}

	policy, err := parseVacuumFlags(remaining)
	if err != nil {
		fmt.Fprintf(stderr, "dark-mem-cli vacuum: %v\n", err)
		return exitUsageErr
	}

	cfg, err := loadConfig(flags)
	if err != nil {
		fmt.Fprintf(stderr, "dark-mem-cli vacuum: %v\n", err)
		return exitUsageErr
	}

	st, err := openStore(ctx, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "dark-mem-cli vacuum: %v\n", err)
		return exitRuntimeErr
	}
	defer closeStore(st, stderr)

	stats, err := st.Vacuum(ctx, policy)
	if err != nil {
		fmt.Fprintf(stderr, "dark-mem-cli vacuum: %v\n", err)
		return exitRuntimeErr
	}

	if flags.JSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(stats); err != nil {
			fmt.Fprintf(stderr, "dark-mem-cli vacuum: encode json: %v\n", err)
			return exitRuntimeErr
		}
		return exitOK
	}

	if policy.DryRun {
		fmt.Fprintln(stdout, "dark-mem-cli vacuum: DRY RUN — no rows deleted, no space reclaimed")
	} else {
		fmt.Fprintf(stdout, "dark-mem-cli vacuum: reclaimed %d bytes across %d table(s), deleted %d row(s) in %s\n",
			stats.BytesReclaimed, len(stats.TablesVacuumed), stats.RowsDeleted, stats.Duration)
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  TABLE\tROWS_DELETED")
	for _, t := range stats.TablesVacuumed {
		fmt.Fprintf(tw, "  %s\t-\n", t)
	}
	_ = tw.Flush()
	return exitOK
}

// vacuumFlags is the parseVacuumFlags output (kept separate from
// store.VacuumPolicy so we can attach --dry-run / --days-old /
// --tables as discrete flags and validate them at parse time).
type vacuumFlags struct {
	DaysOld int
	Tables  []string
	DryRun  bool
}

func parseVacuumFlags(args []string) (store.VacuumPolicy, error) {
	p := store.VacuumPolicy{}
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--days-old" && i+1 < len(args):
			i++
			n, err := parseIntArg(args[i])
			if err != nil {
				return p, fmt.Errorf("--days-old: %w", err)
			}
			p.DaysOld = n
		case len(a) > 11 && a[:11] == "--days-old=":
			n, err := parseIntArg(a[11:])
			if err != nil {
				return p, fmt.Errorf("--days-old: %w", err)
			}
			p.DaysOld = n
		case a == "--tables" && i+1 < len(args):
			i++
			p.Tables = splitCSV(args[i])
		case len(a) > 9 && a[:9] == "--tables=":
			p.Tables = splitCSV(a[9:])
		case a == "--dry-run":
			p.DryRun = true
		default:
			return p, fmt.Errorf("unknown flag %q", a)
		}
		i++
	}
	return p, nil
}

func splitCSV(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseIntArg(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not an integer: %q", s)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}