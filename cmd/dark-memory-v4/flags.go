// dark-memory-v4 common flag parsing + DSN resolution. Shared
// across migrate / schema-status / serve.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// goVersionString is read at package init from runtime.Version().
// Kept as a package var (not const) because runtime.Version() is
// not constant. Used by the boot report in serve.go.
var goVersionString = runtime.Version()

// commonFlags is the parsed flag set shared by every subcommand
// that touches the dark-db. Mirrors cmd/dark-mem-cli/commonFlags
// for muscle-memory transfer.
type commonFlags struct {
	DSN  string
	JSON bool
}

// parseCommonFlags extracts --dsn, --json from args. Returns the
// remaining args (subcommand-specific) and any parse error.
func parseCommonFlags(args []string) (*commonFlags, error) {
	f := &commonFlags{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--dsn" && i+1 < len(args):
			f.DSN = args[i+1]
			i++
		case len(a) > 6 && a[:6] == "--dsn=":
			f.DSN = a[6:]
		case a == "--json":
			f.JSON = true
		default:
			return nil, fmt.Errorf("unknown flag %q", a)
		}
	}
	return f, nil
}

// resolveDSN returns the DSN in priority order: --dsn flag >
// $DARK_DB > ./dark.db. The relative path is resolved against the
// current working directory so the operator's `cd db && serve`
// pattern works.
func resolveDSN(flags *commonFlags, stderr *os.File) (string, error) {
	dsn := flags.DSN
	if dsn == "" {
		dsn = os.Getenv("DARK_DB")
	}
	if dsn == "" {
		dsn = "dark.db"
	}
	if !filepath.IsAbs(dsn) {
		abs, err := filepath.Abs(dsn)
		if err != nil {
			return "", fmt.Errorf("resolve DSN %q: %w", dsn, err)
		}
		dsn = abs
	}
	return dsn, nil
}
