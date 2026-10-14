// Package orchestration — publish_vibe_preflight_test.go: R12
// regression (Phase 8 / Sesión 8, spec 1997, LUCIDEZ R12/R16).
//
// INVARIANT: an invalid git_sha ArtifactRef creates ZERO persistent
// state — no spec row, no artifact row, no drift row. PublishVibe
// used to persist spec + artifact BEFORE the drift pipeline failed
// resolution, so every operator typo (malformed SHA, path missing
// from the SHA) left garbage rows behind (production artifacts
// 1658/1660 + drifts 1585/1587 were pollution, not signal).
//
// Uses a real SQLite store in a temp dir (same pattern as
// error_observatory_test.go) + a temp git repo for the ref target.
package orchestration

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/artifact"
	"github.com/dark-agents/dark-memory-mcp/internal/safety"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

// artifactRefForTest builds a git_sha ArtifactRef for pre-flight tests.
func artifactRefForTest(repo, sha, path string) artifact.ArtifactRef {
	return artifact.ArtifactRef{
		Kind:    artifact.KindGitSHA,
		GitRepo: repo,
		GitSHA:  sha,
		Path:    path,
	}
}

// newPreflightTestRepo builds a temp git repo with one committed file.
func newPreflightTestRepo(t *testing.T) (dir, sha string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	dir = t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out := &bytes.Buffer{}
		cmd.Stdout = out
		cmd.Stderr = out
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(out.String())
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "real.go"), []byte("package real\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "real.go")
	run("commit", "-q", "-m", "init")
	return dir, run("rev-parse", "HEAD")
}

func newPreflightTestOrchestrator(t *testing.T, ctx context.Context) (*Orchestrator, store.Store) {
	t.Helper()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(t.TempDir(), "preflight.db"),
		WALMode:     true,
		ForeignKeys: true,
	}
	st, err := sqlite.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}
	orch := New(st, &safety.Holder{})
	if _, err := orch.SessionStart(ctx, SessionStartInput{
		Operator:  "tester",
		ProjectID: "default",
		Notes:     "preflight test",
	}); err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	return orch, st
}

func countSpecsArtifacts(t *testing.T, ctx context.Context, st store.Store) (specs, artifacts int) {
	t.Helper()
	ss, err := st.ListSpecs(ctx, vibeflow.SpecListFilters{})
	if err != nil {
		t.Fatalf("ListSpecs: %v", err)
	}
	as, err := st.ListArtifacts(ctx, vibeflow.ArtifactListFilters{})
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	return len(ss), len(as)
}

func publishWithGitRef(t *testing.T, ctx context.Context, orch *Orchestrator, repo, sha, path string) error {
	t.Helper()
	tru := true
	ref := artifactRefForTest(repo, sha, path)
	_, err := orch.PublishVibe(ctx, PublishVibeInput{
		Spec: PublishSpecInput{
			VibeCase: "C1",
			Spec:     `{"intent":"preflight test"}`,
		},
		Artifact: PublishArtifactInput{
			ArtifactType: "code",
			ArtifactURL:  "git:" + sha + "/" + path,
			ArtifactRef:  &ref,
		},
		AutoDriftCheck: &tru,
		SessionID:      "sess-preflight",
	})
	return err
}

// TestPublishVibe_Preflight_BadSHACreatesZeroRows: malformed SHA →
// error + zero persistent state (R12).
func TestPublishVibe_Preflight_BadSHACreatesZeroRows(t *testing.T) {
	ctx := context.Background()
	orch, st := newPreflightTestOrchestrator(t, ctx)
	repo, _ := newPreflightTestRepo(t)
	s0, a0 := countSpecsArtifacts(t, ctx, st)

	err := publishWithGitRef(t, ctx, orch, repo, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "real.go")
	if err == nil {
		t.Fatal("expected error for malformed SHA")
	}
	if !strings.Contains(err.Error(), "deadbeef") || !strings.Contains(err.Error(), "git rev-parse HEAD") {
		t.Errorf("error should be actionable (bad SHA + recovery recipe); got: %v", err)
	}
	s1, a1 := countSpecsArtifacts(t, ctx, st)
	if s1 != s0 || a1 != a0 {
		t.Errorf("R12 VIOLATION: invalid ref created state: specs %d→%d, artifacts %d→%d", s0, s1, a0, a1)
	}
}

// TestPublishVibe_Preflight_MissingPathCreatesZeroRows: path absent
// from the SHA → error + zero persistent state (R12).
func TestPublishVibe_Preflight_MissingPathCreatesZeroRows(t *testing.T) {
	ctx := context.Background()
	orch, st := newPreflightTestOrchestrator(t, ctx)
	repo, sha := newPreflightTestRepo(t)
	s0, a0 := countSpecsArtifacts(t, ctx, st)

	err := publishWithGitRef(t, ctx, orch, repo, sha, "nope/missing.go")
	if err == nil {
		t.Fatal("expected error for missing path")
	}
	if !strings.Contains(err.Error(), "nope/missing.go") {
		t.Errorf("error should name the missing path; got: %v", err)
	}
	s1, a1 := countSpecsArtifacts(t, ctx, st)
	if s1 != s0 || a1 != a0 {
		t.Errorf("R12 VIOLATION: invalid ref created state: specs %d→%d, artifacts %d→%d", s0, s1, a0, a1)
	}
}

// TestPublishVibe_Preflight_ValidRefProceeds: a good ref passes
// pre-flight (proves the gate is not a blanket reject). The full
// pipeline needs an LLM; we only assert we get PAST pre-flight —
// i.e. the error (if any) is NOT the pre-flight actionable error.
// To keep this hermetic without LLM keys, AutoDriftCheck=false so
// no judge runs; success = no error + exactly one new spec+artifact.
func TestPublishVibe_Preflight_ValidRefProceeds(t *testing.T) {
	ctx := context.Background()
	orch, st := newPreflightTestOrchestrator(t, ctx)
	repo, sha := newPreflightTestRepo(t)
	s0, a0 := countSpecsArtifacts(t, ctx, st)

	fals := false
	ref := artifactRefForTest(repo, sha, "real.go")
	out, err := orch.PublishVibe(ctx, PublishVibeInput{
		Spec: PublishSpecInput{
			VibeCase: "C1",
			Spec:     `{"intent":"preflight valid-ref test"}`,
		},
		Artifact: PublishArtifactInput{
			ArtifactType: "code",
			ArtifactURL:  "git:" + sha + "/real.go",
			ArtifactRef:  &ref,
		},
		AutoDriftCheck: &fals,
		SessionID:      "sess-preflight",
	})
	if err != nil {
		t.Fatalf("PublishVibe with valid ref: %v", err)
	}
	if out == nil || out.ArtifactID == 0 || out.SpecID == 0 {
		t.Fatalf("expected persisted spec+artifact; got %+v", out)
	}
	s1, a1 := countSpecsArtifacts(t, ctx, st)
	if s1 != s0+1 || a1 != a0+1 {
		t.Errorf("valid ref should create exactly one spec+artifact: specs %d→%d, artifacts %d→%d", s0, s1, a0, a1)
	}
}
