// Package version is the single source of truth for the dark-memory-mcp
// version string. All three binaries (dark-mem-mcp, dark-mem-cli,
// dark-mem-inspect) resolve their reported version through Resolve().
//
// Per CONSTITUTION.md Rule 1, the resolution order is:
//
//	1. -ldflags "-X github.com/dark-agents/dark-memory-mcp/internal/version.buildVersion=<v>"
//	   injected at build time by `make release` (canonical path).
//	2. runtime/debug.ReadBuildInfo().Main.Version (set by `go install`
//	   from the module proxy; used by `make dev`).
//	3. Hardcoded devVersion = "dev" (emergency-only; marks IsDev=true).
//
// Path 3 emits the IsDev flag so the caller (dark_memory_health_ping)
// can surface a drift_warning without blocking boot.
//
// Enrichment fields (Commit, Dirty, BuildTime) are always taken from
// debug.ReadBuildInfo().Settings at runtime, regardless of which path
// produced the Version string. This is what the git_info block of
// health_ping reports.
package version

import (
	"runtime/debug"
	"strings"
	"sync"
)

// buildVersion is set at build time via -ldflags. The Makefile target
// `make release` injects the current git tag (e.g. "1.3.2"). The
// variable is unexported; -ldflags reaches it by its fully qualified
// package path.
//
// Empty by default. Any non-empty, non-"dev" value takes priority.
var buildVersion = ""

// devVersion is the hardcoded fallback. Distinct from "" so a forgotten
// -ldflags injection produces a visible "dev" instead of an empty
// string, which is easier to spot in operator scripts.
const devVersion = "dev"

var (
	resolveOnce = &sync.Once{}
	resolved    Resolved
)

// Resolved is the full version fingerprint returned by Resolve().
// The fields are populated in this order:
//
//   - Version: the human-readable version ("1.3.2", "1.3.2-dirty",
//     or "dev"). Empty iff Resolve has not been called yet.
//   - Commit: short git SHA, taken from vcs.revision. Empty when the
//     binary was built without VCS info (e.g. from a release tarball).
//   - Dirty: true when the working tree had uncommitted changes at
//     build time. Always false for release tarballs.
//   - BuildTime: RFC3339 timestamp from vcs.time. Empty when absent.
//   - Source: which resolution path produced the value. One of
//     "ldflags", "buildinfo", or "dev".
//   - IsDev: true iff Source == "dev" (the hardcoded fallback).
type Resolved struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	Dirty     bool   `json:"dirty,omitempty"`
	BuildTime string `json:"build_time,omitempty"`
	Source    string `json:"source"`
	IsDev     bool   `json:"is_dev,omitempty"`
}

// Resolve returns the resolved version fingerprint. Memoized: the
// first call performs the resolution; subsequent calls return the
// same Resolved. This is safe to call from any goroutine, including
// the boot sequence and the health_ping handler.
func Resolve() Resolved {
	resolveOnce.Do(func() {
		resolved = resolve()
	})
	return resolved
}

// resetMemoization is for tests only. It discards the memoized result
// so the next call to Resolve re-runs the resolution chain. Not
// exported; reachable from version_test.go via package-internal access.
func resetMemoization() {
	resolveOnce = &sync.Once{}
	resolved = Resolved{}
}

// resolve performs the actual resolution. Pulled out of Resolve so
// the sync.Once wrapper is the only public entry point.
func resolve() Resolved {
	r := Resolved{Source: "dev", Version: devVersion, IsDev: true}

	// Path 1: ldflags injection. Any non-empty, non-"dev" value wins.
	if v := strings.TrimSpace(buildVersion); v != "" && v != devVersion {
		r.Version = v
		r.Source = "ldflags"
		r.IsDev = false
		enrichFromBuildInfo(&r)
		return r
	}

	// Path 2: debug.ReadBuildInfo().Module.Version / Main.Version.
	// For `go install` the proxy sets Main.Version to "v1.3.2"; for
	// `go build` from a git checkout the value is "(devel)" and we
	// fall through to Path 3.
	if bi, ok := debug.ReadBuildInfo(); ok {
		mv := strings.TrimSpace(bi.Main.Version)
		if mv != "" && mv != "(devel)" {
			r.Version = strings.TrimPrefix(mv, "v")
			r.Source = "buildinfo"
			r.IsDev = false
		}
		enrichFromBuildInfo(&r)
	}

	// Path 3: r is already initialized to {Version: devVersion, Source: dev, IsDev: true}.
	return r
}

// enrichFromBuildInfo copies the VCS settings into r. Idempotent and
// safe to call from any of the three resolution paths.
func enrichFromBuildInfo(r *Resolved) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) >= 7 {
				r.Commit = s.Value[:7]
			} else {
				r.Commit = s.Value
			}
		case "vcs.modified":
			r.Dirty = s.Value == "true"
		case "vcs.time":
			r.BuildTime = s.Value
		}
	}
}

// String returns the Version field, or "dev" if Version is empty.
// Implements fmt.Stringer so `%s` formatting works as expected.
func (r Resolved) String() string {
	if r.Version == "" {
		return devVersion
	}
	return r.Version
}

// BuildVersion is the value of the -ldflags-injected variable. Exposed
// as a function (not a variable) so callers cannot accidentally
// overwrite it; buildVersion itself remains the only mutable target
// for `-ldflags -X`.
func BuildVersion() string { return buildVersion }
