// Package distribution: v2.6.1 race condition fix tests.
//
// Background: when both publish-npm.yml and publish-mcp-registry.yml
// trigger on a tag push, they run in parallel. The registry workflow
// invokes `./mcp-publisher publish`, which validates server.json's
// packages[].version against the npm registry's index. npm has a
// 5-30s CDN propagation delay after a publish completes. The race
// window: registry workflow reaches `publish` before npm's CDN has
// indexed the new version, so mcp-publisher returns:
//
//   NPM package '@opitacode/dark-memory-mcp' exists, but version
//   '2.6.0' was not found (status: 404).
//
// Fix (operator-approved decision agent_memory id=80): wrap the
// `./mcp-publisher publish` call in a retry loop — up to 3 attempts
// with 30s sleeps. This catches the propagation race AND any other
// transient registry/CDN issue. Total worst-case runtime ~90s on
// the retry path; typical case (npm propagates <30s) succeeds on
// the first retry.
//
// Why retry and not `workflow_run`: workflow_run would force the
// registry workflow onto a different trigger and break manual
// workflow_dispatch reruns (used after a bad server.json push).
// Retry inside the existing step preserves both trigger paths.
//
// These tests verify the structural shape of the fix is in place.
// Runtime verification happens on every v2.6.1+ release.
package distribution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestV261_RegistryPublishRetryLoop asserts the publish step in
// publish-mcp-registry.yml is wrapped in a retry loop (up to 3
// attempts with 30s sleeps). Catches:
//   - Someone removes the retry loop (regression to v2.6.0 race).
//   - Someone shrinks MAX_ATTEMPTS below 3 (no longer tolerates
//     npm propagation delay).
//   - Someone shrinks RETRY_DELAY below 15s (too short for slow
//     npm CDN propagation).
func TestV261_RegistryPublishRetryLoop(t *testing.T) {
	path := filepath.Join(repoRoot(t), ".github", "workflows", "publish-mcp-registry.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read publish-mcp-registry.yml: %v", err)
	}
	content := string(raw)

	// 1. The publish step must use a retry loop, not a bare call.
	if !strings.Contains(content, "mcp-publisher publish") {
		t.Errorf("publish-mcp-registry.yml must invoke `./mcp-publisher publish`")
	}
	if !strings.Contains(content, "MAX_ATTEMPTS") {
		t.Errorf("publish-mcp-registry.yml publish step must wrap mcp-publisher " +
			"in a MAX_ATTEMPTS retry loop (v2.6.1 race condition fix)")
	}
	if !strings.Contains(content, "RETRY_DELAY") {
		t.Errorf("publish-mcp-registry.yml publish step must use a RETRY_DELAY " +
			"sleep between attempts (npm CDN propagation delay)")
	}
	if !strings.Contains(content, "seq 1 $MAX_ATTEMPTS") &&
		!strings.Contains(content, "seq 1 \"$MAX_ATTEMPTS\"") {
		t.Errorf("publish-mcp-registry.yml publish step must iterate over " +
			"$MAX_ATTEMPTS (regex match failed; check the for-loop syntax)")
	}

	// 2. MAX_ATTEMPTS must be >= 3 to tolerate npm's 5-30s propagation.
	//    We assert on the literal "3" rather than parsing the YAML,
	//    because GitHub Actions interprets the value at runtime and
	//    a regex parse would just duplicate the literal.
	if !strings.Contains(content, "MAX_ATTEMPTS=3") {
		t.Errorf("publish-mcp-registry.yml must set MAX_ATTEMPTS=3 " +
			"(smaller values no longer tolerate npm's typical propagation delay)")
	}

	// 3. RETRY_DELAY must be >= 15s. npm's CDN propagation is normally
	//    <30s but the typical "wait 30s" is conservative; we enforce
	//    the floor at 15 to keep some headroom for the common case.
	if !strings.Contains(content, "RETRY_DELAY=30") &&
		!strings.Contains(content, "RETRY_DELAY=20") &&
		!strings.Contains(content, "RETRY_DELAY=15") {
		t.Errorf("publish-mcp-registry.yml must set RETRY_DELAY >= 15 " +
			"(npm CDN propagation is typically 5-30s; smaller values reintroduce the race)")
	}

	// 4. Sanity: the retry loop must NOT silently swallow the final
	//    failure. After MAX_ATTEMPTS exhausted, the step must exit 1
	//    so the CI run fails (operator gets a clear signal to retry).
	if !strings.Contains(content, "exit 1") {
		t.Errorf("publish-mcp-registry.yml publish step must exit 1 after " +
			"MAX_ATTEMPTS exhausted (silent failure would let a bad publish " +
			"go unnoticed)")
	}
}
