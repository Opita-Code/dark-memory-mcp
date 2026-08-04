// Command gen-metadata rewrites the release-facing metadata files
// (server.json, mcpb/manifest.json, npm/*/package.json) so their
// `version` field matches the current git tag.
//
// Why this exists: every release used to require hand-editing the
// version in ~10 files. This generator is the single release-time
// step — run `go generate` (or `go run ./cmd/gen-metadata`) after
// tagging, then commit the diff.
//
// Version resolution:
//
//  1. --version flag (explicit override; used by CI/release scripts)
//  2. `git describe --tags --abbrev=0` (the most recent tag, "v"
//     prefix stripped — release versions in the metadata files never
//     carry the "v")
//  3. fallback "dev" (never silently empty)
//
// The generator is idempotent: running it twice with the same version
// produces no diff. It only rewrites the `"version"` string fields;
// all other JSON content is preserved byte-for-byte.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// targets are the metadata files whose "version" field must match the
// release version. All paths are relative to the repo root.
var targets = []string{
	"server.json",
	"mcpb/manifest.json",
	"npm/wrapper/package.json",
	"npm/platform-linux-x64/package.json",
	"npm/platform-linux-arm64/package.json",
	"npm/platform-darwin-x64/package.json",
	"npm/platform-darwin-arm64/package.json",
	"npm/platform-win32-x64/package.json",
	"npm/platform-win32-arm64/package.json",
}

// versionField matches a JSON "version": "..." string field.
var versionField = regexp.MustCompile(`"version"\s*:\s*"[^"]*"`)

func main() {
	versionFlag := flag.String("version", "", "explicit version override (default: git describe)")
	flag.Parse()

	version := *versionFlag
	if version == "" {
		out, err := exec.Command("git", "describe", "--tags", "--abbrev=0").Output()
		if err != nil {
			fmt.Fprintf(os.Stderr, "gen-metadata: git describe failed (%v); using \"dev\"\n", err)
			version = "dev"
		} else {
			version = strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
		}
	}
	if version == "" {
		version = "dev"
	}

	// Locate the repo root: this file lives at
	// <root>/cmd/gen-metadata/main.go, so the root is three levels up
	// from the source file — independent of the working directory, so
	// `go generate ./...` works from any CWD.
	_, thisFile, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))

	replacement := fmt.Sprintf(`"version": "%s"`, version)
	var changed int
	for _, rel := range targets {
		path := filepath.Join(root, filepath.FromSlash(rel))
		raw, err := os.ReadFile(path)
		if err != nil {
			fatal("read %s: %v", rel, err)
		}
		matches := versionField.FindAllString(string(raw), -1)
		if len(matches) == 0 {
			fatal("%s: no \"version\" field found — file shape changed?", rel)
		}
		updated := versionField.ReplaceAllString(string(raw), replacement)
		if updated == string(raw) {
			fmt.Printf("gen-metadata: %s: already at %q\n", rel, version)
			continue
		}
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			fatal("write %s: %v", rel, err)
		}
		changed++
		fmt.Printf("gen-metadata: %s: version -> %q (%d field(s))\n", rel, version, len(matches))
	}
	fmt.Printf("gen-metadata: done. %d file(s) updated to %q.\n", changed, version)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "gen-metadata: "+format+"\n", args...)
	os.Exit(1)
}
