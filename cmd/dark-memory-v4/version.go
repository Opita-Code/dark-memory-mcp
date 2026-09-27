// Version resolution for dark-memory-v4. The Version string is
// stamped at build time via -ldflags:
//
//	go build -ldflags "-X main.versionLD=$(git describe --tags --abbrev=0)"
//
// Per the dark-cli / dark-mem-cli convention (CONSTITUTION.md Rule
// 1), the ldflags path is canonical. When unset, the resolver
// falls back to debug.ReadBuildInfo (for `go install`) and then to
// the "dev" sentinel (for emergency debug builds; emits IsDev=true
// so the operator sees a drift_warning).
package main

import (
	"runtime/debug"
	"strings"
)

// versionLD is the ldflags target. Keep it private (lowercase) so
// the harness cannot accidentally read it before ResolveVersion
// runs (the public Version var is the resolved form, not the raw
// input).
var versionLD = ""

// VersionInfo captures the resolved version + provenance.
type VersionInfo struct {
	Raw    string // full string printed by `dark-memory-v4 version`
	IsDev  bool   // true when no ldflags, no ReadBuildInfo, or both are "devel"
	Source string // "ldflags" | "buildinfo" | "sentinel"
}

// String implements fmt.Stringer for convenient printing.
func (v VersionInfo) String() string { return v.Raw }

// Version is the resolved version string. Set at package init by
// calling ResolveVersion, which reads versionLD (the ldflags
// target) and falls back to debug.ReadBuildInfo. Using an
// intermediate package var avoids an init cycle: Version depends
// on ResolveVersion, which depends on versionLD — Version and
// versionLD are independent.
//
// The actual declaration lives in main.go (var Version = ...),
// so this file holds the resolver only.


// ResolveVersion returns the best-known version string.
func ResolveVersion() VersionInfo {
	if versionLD != "" && versionLD != "dev" {
		return VersionInfo{Raw: versionLD, IsDev: false, Source: "ldflags"}
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return VersionInfo{Raw: bi.Main.Version, IsDev: false, Source: "buildinfo"}
	}
	return VersionInfo{Raw: "v4-alpha.1-dev", IsDev: true, Source: "sentinel"}
}

// extractVCSCommit returns the VCS commit hash if embedded by
// ReadBuildInfo. Empty when the binary was built without
// -buildvcs=true (rare; ldflags carries the canonical version).
func extractVCSCommit() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" && !strings.Contains(s.Value, "dirty") {
			return s.Value
		}
	}
	return ""
}
