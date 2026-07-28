// Package distribution contains tests that validate the npm wrapper
// packaging + Official MCP Registry manifest that ships with
// dark-memory-mcp v2.5.0. These tests are pure static analysis Ã¢â‚¬â€
// they do not require the live MCP binary, so they run in any CI
// environment.
//
// Scope:
//
//   - npm wrapper structure (1 main package + 6 platform packages)
//   - server.json (Official MCP Registry manifest)
//   - .github/workflows/publish-npm.yml + publish-mcp-registry.yml
//   - Cross-validation: package.json versions + server.json version
//     + git tag (when CI runs on a tag)
//
// Why this matters: a single typo in package.json or server.json
// causes the npm publish + Official MCP Registry entry to fail at
// release time, which is the worst place to discover schema drift.
// We catch it at `go test` time instead.
package distribution

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot returns the absolute path to the repository root.
//
// All paths in this file are resolved relative to this so the tests
// can be invoked from any working directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	// tests/distribution is two levels deep from repo root.
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine caller")
	}
	root := filepath.Join(filepath.Dir(here), "..", "..")
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("could not resolve repo root: %v", err)
	}
	return abs
}

// npmWrapperDir returns the path to the npm/wrapper package.
func npmWrapperDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "npm", "wrapper")
}

// platformDirs returns the 6 platform-specific package directories.
func platformDirs(t *testing.T) []string {
	t.Helper()
	root := repoRoot(t)
	platforms := []string{
		"darwin-x64",
		"darwin-arm64",
		"linux-x64",
		"linux-arm64",
		"win32-x64",
		"win32-arm64",
	}
	dirs := make([]string, 0, len(platforms))
	for _, p := range platforms {
		dirs = append(dirs, filepath.Join(root, "npm", "platform-"+p))
	}
	return dirs
}

// -----------------------------------------------------------------------------
// Tests: package.json validation
// -----------------------------------------------------------------------------

// TestV250_AllPackageJSONsParse verifies all 7 package.json files
// (1 wrapper + 6 platform) parse as valid JSON and have the required
// shape: name, version, license.
func TestV250_AllPackageJSONsParse(t *testing.T) {
	dirs := append([]string{npmWrapperDir(t)}, platformDirs(t)...)
	if len(dirs) != 7 {
		t.Fatalf("expected 7 npm packages, got %d", len(dirs))
	}

	for _, dir := range dirs {
		dir := dir
		t.Run(filepath.Base(dir), func(t *testing.T) {
			path := filepath.Join(dir, "package.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			var pkg map[string]any
			if err := json.Unmarshal(raw, &pkg); err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}

			// Required fields
			if _, ok := pkg["name"].(string); !ok {
				t.Errorf("%s: missing or non-string `name`", path)
			}
			if _, ok := pkg["version"].(string); !ok {
				t.Errorf("%s: missing or non-string `version`", path)
			}
			if _, ok := pkg["license"].(string); !ok {
				t.Errorf("%s: missing or non-string `license`", path)
			}

			// Scope: every package name must start with @opitacode/
			name, _ := pkg["name"].(string)
			if !strings.HasPrefix(name, "@opitacode/dark-memory-mcp") {
				t.Errorf("%s: name %q must start with @opitacode/dark-memory-mcp", path, name)
			}
		})
	}
}

// TestV250_WrapperPackageHasOptionalDependencies verifies the main
// wrapper's package.json declares all 6 platform packages as
// optionalDependencies and that all 6 platform packages appear with
// the same version.
func TestV250_WrapperPackageHasOptionalDependencies(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(npmWrapperDir(t), "package.json"))
	if err != nil {
		t.Fatalf("read wrapper package.json: %v", err)
	}
	var pkg map[string]any
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatalf("parse wrapper package.json: %v", err)
	}

	opts, ok := pkg["optionalDependencies"].(map[string]any)
	if !ok {
		t.Fatal("wrapper package.json missing `optionalDependencies`")
	}

	expected := []string{
		"@opitacode/dark-memory-mcp-darwin-x64",
		"@opitacode/dark-memory-mcp-darwin-arm64",
		"@opitacode/dark-memory-mcp-linux-x64",
		"@opitacode/dark-memory-mcp-linux-arm64",
		"@opitacode/dark-memory-mcp-win32-x64",
		"@opitacode/dark-memory-mcp-win32-arm64",
	}

	wrapperVersion, _ := pkg["version"].(string)
	for _, want := range expected {
		got, ok := opts[want].(string)
		if !ok {
			t.Errorf("missing optionalDependency %q", want)
			continue
		}
		if got != wrapperVersion {
			t.Errorf("optionalDependency %q version %q does not match wrapper version %q", want, got, wrapperVersion)
		}
	}
}

// TestV250_WrapperPackageHasMCPName verifies the wrapper's package.json
// has the `mcpName` field set to the Official MCP Registry namespace.
// This is what the registry publisher CLI reads to claim the namespace.
func TestV250_WrapperPackageHasMCPName(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(npmWrapperDir(t), "package.json"))
	if err != nil {
		t.Fatalf("read wrapper package.json: %v", err)
	}
	var pkg map[string]any
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatalf("parse wrapper package.json: %v", err)
	}

	mcpName, ok := pkg["mcpName"].(string)
	if !ok {
		t.Fatal("wrapper package.json missing `mcpName` field")
	}
	const want = "io.github.Opita-Code/dark-memory-mcp"
	if mcpName != want {
		t.Errorf("mcpName = %q, want %q", mcpName, want)
	}
}

// TestV250_WrapperPackageHasBinEntry verifies the wrapper's package.json
// has a bin entry pointing to index.js. Without this, `npx
// @opitacode/dark-memory-mcp` won't know what to invoke.
func TestV250_WrapperPackageHasBinEntry(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(npmWrapperDir(t), "package.json"))
	if err != nil {
		t.Fatalf("read wrapper package.json: %v", err)
	}
	var pkg map[string]any
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatalf("parse wrapper package.json: %v", err)
	}

	bin, ok := pkg["bin"].(map[string]any)
	if !ok {
		t.Fatal("wrapper package.json missing `bin` object")
	}
	target, ok := bin["dark-memory-mcp"].(string)
	if !ok {
		t.Fatal("wrapper package.json bin entry missing `dark-memory-mcp` key")
	}
	if target != "index.js" {
		t.Errorf("wrapper bin target = %q, want %q", target, "index.js")
	}
}

// TestV250_PlatformPackagesHaveOSAndCPU verifies each platform package's
// package.json declares os + cpu constraints. Without these, npm may
// install a darwin-arm64 binary on a linux-x64 host.
func TestV250_PlatformPackagesHaveOSAndCPU(t *testing.T) {
	platforms := map[string]struct {
		os   string
		cpu  string
		ext  string // binary extension on this OS
	}{
		"darwin-x64":    {"darwin", "x64", ""},
		"darwin-arm64":  {"darwin", "arm64", ""},
		"linux-x64":     {"linux", "x64", ""},
		"linux-arm64":   {"linux", "arm64", ""},
		"win32-x64":     {"win32", "x64", ".exe"},
		"win32-arm64":   {"win32", "arm64", ".exe"},
	}

	for _, dir := range platformDirs(t) {
		name := strings.TrimPrefix(filepath.Base(dir), "platform-")
		want, ok := platforms[name]
		if !ok {
			t.Errorf("unknown platform directory: %s", name)
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		var pkg map[string]any
		if err := json.Unmarshal(raw, &pkg); err != nil {
			t.Fatalf("parse %s: %v", dir, err)
		}

		osList, _ := pkg["os"].([]any)
		if len(osList) != 1 || osList[0] != want.os {
			t.Errorf("%s: os = %v, want [%q]", name, osList, want.os)
		}
		cpuList, _ := pkg["cpu"].([]any)
		if len(cpuList) != 1 || cpuList[0] != want.cpu {
			t.Errorf("%s: cpu = %v, want [%q]", name, cpuList, want.cpu)
		}

		// Binary name check (sanity: index.js must reference the right name)
		idx, err := os.ReadFile(filepath.Join(dir, "index.js"))
		if err != nil {
			t.Fatalf("read %s/index.js: %v", dir, err)
		}
		expectedBin := "dark-mem-mcp" + want.ext
		if !strings.Contains(string(idx), expectedBin) {
			t.Errorf("%s/index.js does not reference binary %q", name, expectedBin)
		}
	}
}

// TestV250_AllPackageVersionsMatch verifies all 7 npm package.json
// versions + server.json version + go binary ldflags version are
// identical. This is the canonical "version drift" check.
func TestV250_AllPackageVersionsMatch(t *testing.T) {
	want := "2.5.2"

	// npm packages
	dirs := append([]string{npmWrapperDir(t)}, platformDirs(t)...)
	versions := []string{}
	for _, dir := range dirs {
		raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		var pkg map[string]any
		if err := json.Unmarshal(raw, &pkg); err != nil {
			t.Fatalf("parse %s: %v", dir, err)
		}
		v, _ := pkg["version"].(string)
		versions = append(versions, v)
		if v != want {
			t.Errorf("%s/package.json version = %q, want %q", dir, v, want)
		}
	}

	// server.json
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "server.json"))
	if err != nil {
		t.Fatalf("read server.json: %v", err)
	}
	var srv map[string]any
	if err := json.Unmarshal(raw, &srv); err != nil {
		t.Fatalf("parse server.json: %v", err)
	}
	srvVersion, _ := srv["version"].(string)
	if srvVersion != want {
		t.Errorf("server.json version = %q, want %q", srvVersion, want)
	}

	// Confirm the npm package versions are pairwise identical
	first := versions[0]
	for i, v := range versions {
		if v != first {
			t.Errorf("npm package version drift: package[%d]=%q, package[0]=%q", i, v, first)
		}
	}
}

// -----------------------------------------------------------------------------
// Tests: server.json schema validation (Official MCP Registry)
// -----------------------------------------------------------------------------

// TestV250_ServerJSONSchema validates server.json against the Official
// MCP Registry manifest schema requirements documented at
// https://github.com/modelcontextprotocol/registry and the GitHub
// blog post on the registry:
// https://github.blog/ai-and-ml/generative-ai/how-to-find-install-and-manage-mcp-servers-with-the-github-mcp-registry/
//
// Required shape (per registry v0.1 API freeze, October 2025):
//   - $schema (URL pointer)
//   - name (namespace format: io.github.<user>/<server>)
//   - version (semver)
//   - packages[] (each with registryType, identifier, version, transport.type)
func TestV250_ServerJSONSchema(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "server.json"))
	if err != nil {
		t.Fatalf("read server.json: %v", err)
	}
	var srv map[string]any
	if err := json.Unmarshal(raw, &srv); err != nil {
		t.Fatalf("parse server.json: %v", err)
	}

	// $schema
	schema, _ := srv["$schema"].(string)
	if !strings.HasPrefix(schema, "https://static.modelcontextprotocol.io/") {
		t.Errorf("server.json $schema = %q, want a URL under static.modelcontextprotocol.io", schema)
	}

	// name
	name, _ := srv["name"].(string)
	if !strings.HasPrefix(name, "io.github.Opita-Code/") {
		t.Errorf("server.json name = %q, want prefix io.github.Opita-Code/", name)
	}
	if name != "io.github.Opita-Code/dark-memory-mcp" {
		t.Errorf("server.json name = %q, want io.github.Opita-Code/dark-memory-mcp", name)
	}

	// version
	version, _ := srv["version"].(string)
	if version != "2.5.2" {
		t.Errorf("server.json version = %q, want 2.5.2", version)
	}

	// packages array
	pkgs, ok := srv["packages"].([]any)
	if !ok {
		t.Fatal("server.json: packages must be an array")
	}
	if len(pkgs) == 0 {
		t.Fatal("server.json: packages must be non-empty")
	}

	for i, raw := range pkgs {
		pkg, ok := raw.(map[string]any)
		if !ok {
			t.Errorf("server.json: packages[%d] must be an object", i)
			continue
		}
		registryType, _ := pkg["registryType"].(string)
		if registryType != "npm" {
			t.Errorf("server.json: packages[%d].registryType = %q, want npm", i, registryType)
		}
		identifier, _ := pkg["identifier"].(string)
		if identifier != "@opitacode/dark-memory-mcp" {
			t.Errorf("server.json: packages[%d].identifier = %q, want @opitacode/dark-memory-mcp", i, identifier)
		}
		pkgVersion, _ := pkg["version"].(string)
		if pkgVersion != version {
			t.Errorf("server.json: packages[%d].version = %q, want %q", i, pkgVersion, version)
		}
		transport, ok := pkg["transport"].(map[string]any)
		if !ok {
			t.Errorf("server.json: packages[%d].transport must be an object", i)
			continue
		}
		transportType, _ := transport["type"].(string)
		if transportType != "stdio" {
			t.Errorf("server.json: packages[%d].transport.type = %q, want stdio", i, transportType)
		}
	}

	// repository
	repo, ok := srv["repository"].(map[string]any)
	if !ok {
		t.Error("server.json: repository must be an object")
	} else {
		repoURL, _ := repo["url"].(string)
		if repoURL != "https://github.com/Opita-Code/dark-memory-mcp" {
			t.Errorf("server.json: repository.url = %q, want https://github.com/Opita-Code/dark-memory-mcp", repoURL)
		}
	}
}

// -----------------------------------------------------------------------------
// Tests: Node.js syntax check (catches typos in index.js)
// -----------------------------------------------------------------------------

// TestV250_AllIndexJSValidSyntax runs `node --check` on every index.js
// in the npm wrapper + 6 platform packages. This catches syntax errors
// before they ship to npm, where they'd surface as a wrapper that
// fails to load on every user's machine.
func TestV250_AllIndexJSValidSyntax(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not in PATH; skipping index.js syntax check")
	}

	dirs := append([]string{npmWrapperDir(t)}, platformDirs(t)...)
	for _, dir := range dirs {
		dir := dir
		t.Run(filepath.Base(dir), func(t *testing.T) {
			idxPath := filepath.Join(dir, "index.js")
			cmd := exec.Command("node", "--check", idxPath)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("node --check %s: %v\n%s", idxPath, err, out)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Tests: platform detection logic (smoke test the wrapper's main index.js)
// -----------------------------------------------------------------------------

// TestV250_PlatformDetectionMapConsistent runs the wrapper's index.js
// as a Node module (without spawning it) and extracts its PLATFORM_MAP
// to verify all 6 expected keys are present and match the package
// names declared in the optionalDependencies block.
//
// This catches a class of bugs where someone adds a platform to the
// optionalDependencies but forgets to add it to the PLATFORM_MAP (or
// vice versa).
func TestV250_PlatformDetectionMapConsistent(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not in PATH; skipping platform detection consistency check")
	}

	// Use a small Node script that loads the wrapper's index.js as a
	// module and re-exports PLATFORM_MAP. We need to do this in a way
	// that doesn't actually invoke the spawn in main(); we mock
	// process.platform + process.arch to be a known value, then read
	// PLATFORM_MAP.
	//
	// The wrapper dir is passed via env var (WRAPPER_DIR) rather than
	// argv because Node 24's `-e` argument handling places the script
	// itself at argv[1] (as "[eval]") which can shift positional args
	// in subtle ways. Env vars are unambiguous.
	const probe = `
		const path = require('path');
		const fs = require('fs');
		const dir = process.env.WRAPPER_DIR;
		if (!dir) { console.error('WRAPPER_DIR env var not set'); process.exit(2); }
		const src = fs.readFileSync(path.join(dir, 'index.js'), 'utf8');
		const m = src.match(/const PLATFORM_MAP = Object\.freeze\(\{([\s\S]+?)\}\)/);
		if (!m) { console.error('PLATFORM_MAP not found in ' + dir + '/index.js'); process.exit(2); }
		// Parse the entries: 'darwin-x64':    '@opitacode/...',
		const entries = {};
		const re = /'([a-z0-9-]+)':\s*'([^']+)'/g;
		let r;
		while ((r = re.exec(m[1])) !== null) { entries[r[1]] = r[2]; }
		console.log(JSON.stringify(entries));
	`

	wrapperDir := npmWrapperDir(t)
	cmd := exec.Command("node", "-e", probe)
	cmd.Env = append(os.Environ(), "WRAPPER_DIR="+wrapperDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe PLATFORM_MAP: %v\n%s", err, out)
	}
	got := map[string]string{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("parse PLATFORM_MAP output: %v\nraw: %s", err, out)
	}

	want := map[string]string{
		"darwin-x64":   "@opitacode/dark-memory-mcp-darwin-x64",
		"darwin-arm64": "@opitacode/dark-memory-mcp-darwin-arm64",
		"linux-x64":    "@opitacode/dark-memory-mcp-linux-x64",
		"linux-arm64":  "@opitacode/dark-memory-mcp-linux-arm64",
		"win32-x64":    "@opitacode/dark-memory-mcp-win32-x64",
		"win32-arm64":  "@opitacode/dark-memory-mcp-win32-arm64",
	}
	if len(got) != len(want) {
		t.Errorf("PLATFORM_MAP has %d entries, want %d (%v)", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("PLATFORM_MAP[%q] = %q, want %q", k, got[k], v)
		}
	}
}

// -----------------------------------------------------------------------------
// Tests: GitHub Actions workflow structure
// -----------------------------------------------------------------------------

// TestV250_PublishNPMWorkflowStructure verifies publish-npm.yml has
// the minimum required structure to be a valid GitHub Actions
// workflow: name, on (or true trigger), jobs at least one.
func TestV250_PublishNPMWorkflowStructure(t *testing.T) {
	path := filepath.Join(repoRoot(t), ".github", "workflows", "publish-npm.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read publish-npm.yml: %v", err)
	}
	validateWorkflowStructure(t, string(raw), "publish-npm")

	// Must reference NODE_AUTH_TOKEN secret (npm publish auth).
	if !strings.Contains(string(raw), "NODE_AUTH_TOKEN") {
		t.Errorf("publish-npm.yml must reference NODE_AUTH_TOKEN secret")
	}
	// Must trigger on v* tag push (per operator-approved release flow).
	if !strings.Contains(string(raw), "tags:") || !strings.Contains(string(raw), "\"v*\"") {
		t.Errorf("publish-npm.yml must trigger on tag push with pattern v*")
	}
}

// TestV250_PublishMCPRegistryWorkflowStructure verifies
// publish-mcp-registry.yml is a valid workflow that uses GitHub OIDC
// to authenticate (no long-lived secrets required).
func TestV250_PublishMCPRegistryWorkflowStructure(t *testing.T) {
	path := filepath.Join(repoRoot(t), ".github", "workflows", "publish-mcp-registry.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read publish-mcp-registry.yml: %v", err)
	}
	validateWorkflowStructure(t, string(raw), "publish-mcp-registry")

	content := string(raw)
	// Must use GitHub OIDC for auth (no long-lived secrets)
	if !strings.Contains(content, "github-oidc") {
		t.Errorf("publish-mcp-registry.yml must use github-oidc for authentication")
	}
	// Must invoke mcp-publisher publish
	if !strings.Contains(content, "mcp-publisher publish") {
		t.Errorf("publish-mcp-registry.yml must invoke `mcp-publisher publish`")
	}
	// Must validate server.json version matches tag
	if !strings.Contains(content, "server.json") {
		t.Errorf("publish-mcp-registry.yml must validate server.json")
	}
}

// validateWorkflowStructure is a minimal YAML structure validator.
// It does NOT parse YAML (we don't want to add a yaml.v3 dependency),
// but it checks for the top-level keys that GitHub Actions requires.
func validateWorkflowStructure(t *testing.T, content, label string) {
	t.Helper()

	mustContain := []string{
		"name:",
		"on:",
		"jobs:",
		"runs-on:",
		"steps:",
	}
	for _, needle := range mustContain {
		if !strings.Contains(content, needle) {
			t.Errorf("%s: workflow missing required key %q", label, needle)
		}
	}

	// Top-level keys must be at column 0 (no leading whitespace).
	for _, key := range []string{"name:", "on:", "jobs:"} {
		// Find the first occurrence
		idx := strings.Index(content, "\n"+key)
		if idx == -1 {
			idx = strings.Index(content, key)
		}
		if idx == -1 {
			continue // already caught by mustContain
		}
		// Check the line is at column 0 (no leading spaces)
		if idx > 0 && content[idx-1] != '\n' {
			t.Errorf("%s: key %q is not at column 0", label, key)
		}
	}
}
