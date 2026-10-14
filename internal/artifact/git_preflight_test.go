// Package artifact — git_preflight_test.go: adversarial coverage for
// PreflightGitRef + the actionable resolveGit error (Phase 8 /
// Sesión 8, spec 1997, LUCIDEZ R12/R13/R16).
//
// Convention follows resolver_test.go: temp git repo, skip when git
// is absent. No LLM, no network, no DB.
package artifact

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initPreflightRepo builds a temp repo with two commits:
// hello.txt (both commits) + sub/gone.txt (first commit only, so
// the second commit has a real "path missing from SHA" case).
func initPreflightRepo(t *testing.T) (dir, sha1, sha2 string) {
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
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "gone.txt"), []byte("doomed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "first")
	sha1 = run("rev-parse", "HEAD")
	// Second commit: modify hello, delete sub/gone.txt.
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("rm", "-q", "sub/gone.txt")
	run("commit", "-q", "-am", "second")
	sha2 = run("rev-parse", "HEAD")
	return dir, sha1, sha2
}

func TestPreflight_ExistingPath(t *testing.T) {
	dir, _, sha2 := initPreflightRepo(t)
	rep := PreflightGitRef(context.Background(), dir, sha2, "hello.txt", 5)
	if !rep.Exists {
		t.Fatalf("Exists=false; actionable=%q", rep.ActionableError)
	}
	if rep.ActionableError != "" {
		t.Errorf("ActionableError should be empty on success; got %q", rep.ActionableError)
	}
	if len(rep.ResolvedSHA) != 40 || rep.ResolvedSHA != sha2 {
		t.Errorf("ResolvedSHA=%q want %q", rep.ResolvedSHA, sha2)
	}
	if rep.BlobSHA == "" {
		t.Error("BlobSHA empty")
	}
	if rep.SizeBytes != 3 { // "v2\n"
		t.Errorf("SizeBytes=%d want 3", rep.SizeBytes)
	}
	if rep.CurrentHEAD != sha2 {
		t.Errorf("CurrentHEAD=%q want HEAD=%q", rep.CurrentHEAD, sha2)
	}
	if rep.HeadsContaining < 1 {
		t.Errorf("HeadsContaining=%d want >=1 (main points at HEAD)", rep.HeadsContaining)
	}
}

func TestPreflight_ShortSHAExpands(t *testing.T) {
	dir, _, sha2 := initPreflightRepo(t)
	rep := PreflightGitRef(context.Background(), dir, sha2[:7], "hello.txt", 5)
	if !rep.Exists {
		t.Fatalf("Exists=false; actionable=%q", rep.ActionableError)
	}
	if rep.ResolvedSHA != sha2 {
		t.Errorf("short SHA should expand to %q; got %q", sha2, rep.ResolvedSHA)
	}
}

func TestPreflight_MissingPathFromSHA(t *testing.T) {
	// R1/R2 core case: sub/gone.txt exists on disk? No — it was
	// committed then deleted. It exists in sha1 but NOT in sha2
	// (HEAD). Request it at HEAD.
	dir, _, sha2 := initPreflightRepo(t)
	rep := PreflightGitRef(context.Background(), dir, sha2, "sub/gone.txt", 5)
	if rep.Exists {
		t.Fatal("Exists=true for path missing from SHA")
	}
	if rep.ActionableError == "" {
		t.Fatal("ActionableError empty on missing path (R13 violation)")
	}
	for _, want := range []string{"sub/gone.txt", sha2, "git rev-parse HEAD"} {
		if !strings.Contains(rep.ActionableError, want) {
			t.Errorf("actionable message missing %q; got: %s", want, rep.ActionableError)
		}
	}
}

func TestPreflight_MissingPathWithSuggestions(t *testing.T) {
	// hello.txt exists at internal root; request it under a wrong
	// dir with the right basename → suggester must surface it.
	dir, _, sha2 := initPreflightRepo(t)
	rep := PreflightGitRef(context.Background(), dir, sha2, "wrongdir/hello.txt", 5)
	if rep.Exists {
		t.Fatal("Exists=true for wrong-dir path")
	}
	if len(rep.Suggestions) == 0 {
		t.Fatal("expected ≥1 suggestion for basename hello.txt")
	}
	if rep.Suggestions[0] != "hello.txt" {
		t.Errorf("top suggestion=%q want hello.txt", rep.Suggestions[0])
	}
	if !strings.Contains(rep.ActionableError, "Did you mean") {
		t.Errorf("message should contain 'Did you mean'; got: %s", rep.ActionableError)
	}
}

func TestPreflight_MalformedSHA(t *testing.T) {
	// R1 core case: syntactically-hex but nonexistent SHA
	// (mirrors the Sesión 7 subagent-fabricated SHA incident).
	dir, _, _ := initPreflightRepo(t)
	rep := PreflightGitRef(context.Background(), dir, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "hello.txt", 5)
	if rep.Exists {
		t.Fatal("Exists=true for malformed SHA")
	}
	if !strings.Contains(rep.ActionableError, "deadbeef") {
		t.Errorf("message should echo the bad SHA; got: %s", rep.ActionableError)
	}
	if !strings.Contains(rep.ActionableError, "HEAD") || rep.CurrentHEAD == "" {
		t.Errorf("message should offer HEAD as fallback; got: %s", rep.ActionableError)
	}
}

func TestPreflight_StaleSHAUnreachable(t *testing.T) {
	// Amend away the second commit on a scratch branch so the
	// original sha2 becomes unreachable from any branch... simpler
	// deterministic variant: request sha1 (still reachable via
	// history — HeadsContaining counts branches pointing AT it,
	// so use a detached dangling commit instead).
	dir, _, _ := initPreflightRepo(t)
	// Create a dangling commit: commit on detached HEAD, then go back.
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
	run("checkout", "-q", "--detach", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "dangling.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "dangling.txt")
	run("commit", "-q", "-m", "dangling")
	dangling := run("rev-parse", "HEAD")
	run("checkout", "-q", "main")

	// dangling commit exists as an object but no branch points at it.
	rep := PreflightGitRef(context.Background(), dir, dangling, "dangling.txt", 5)
	if !rep.Exists {
		t.Fatalf("dangling commit should still resolve (object exists); actionable=%q", rep.ActionableError)
	}
	if rep.HeadsContaining != 0 {
		t.Errorf("HeadsContaining=%d want 0 (unreachable)", rep.HeadsContaining)
	}
}

func TestPreflight_NonGitRepo(t *testing.T) {
	dir := t.TempDir() // no git init
	rep := PreflightGitRef(context.Background(), dir, "HEAD", "x.go", 5)
	if rep.Exists {
		t.Fatal("Exists=true outside a git repo")
	}
	if !strings.Contains(rep.ActionableError, "not a readable git repo") {
		t.Errorf("message should name the repo problem; got: %s", rep.ActionableError)
	}
}

func TestPreflight_RootPath(t *testing.T) {
	dir, _, sha2 := initPreflightRepo(t)
	rep := PreflightGitRef(context.Background(), dir, sha2, ".", 5)
	if rep.Exists {
		t.Fatal("Exists=true for repo root")
	}
	if !strings.Contains(rep.ActionableError, "repo root") {
		t.Errorf("message should mention repo root; got: %s", rep.ActionableError)
	}
}

func TestPreflight_SuggestionLimitRespected(t *testing.T) {
	dir, _, sha2 := initPreflightRepo(t)
	rep := PreflightGitRef(context.Background(), dir, sha2, "elsewhere/hello.txt", 1)
	if len(rep.Suggestions) > 1 {
		t.Errorf("suggestions capped at 1; got %v", rep.Suggestions)
	}
}

// ─── resolveGit actionable error (R13) ───────────────────────────────────────

func TestResolveGit_ActionableErrorOnMissingPath(t *testing.T) {
	dir, _, sha2 := initPreflightRepo(t)
	r := &Resolver{}
	_, err := r.Resolve(context.Background(), ArtifactRef{
		Kind:    KindGitSHA,
		Path:    "wrongdir/hello.txt", // basename exists at root → suggestions fire
		GitSHA:  sha2,
		GitRepo: dir,
	})
	if err == nil {
		t.Fatal("expected error for missing path")
	}
	if !errors.Is(err, ErrUnresolved) {
		t.Errorf("error should wrap ErrUnresolved; got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"wrongdir/hello.txt", "Did you mean", "hello.txt", "git rev-parse HEAD"} {
		if !strings.Contains(msg, want) {
			t.Errorf("resolveGit error missing %q; got: %s", want, msg)
		}
	}
}

func TestResolveGit_ActionableErrorOnMissingPathNoSuggestions(t *testing.T) {
	dir, _, sha2 := initPreflightRepo(t)
	r := &Resolver{}
	_, err := r.Resolve(context.Background(), ArtifactRef{
		Kind:    KindGitSHA,
		Path:    "sub/gone.txt", // basename gone nowhere in HEAD
		GitSHA:  sha2,
		GitRepo: dir,
	})
	if err == nil {
		t.Fatal("expected error for missing path")
	}
	msg := err.Error()
	for _, want := range []string{"sub/gone.txt", "No same-basename path exists", "git rev-parse HEAD"} {
		if !strings.Contains(msg, want) {
			t.Errorf("resolveGit error missing %q; got: %s", want, msg)
		}
	}
}

func TestResolveGit_ActionableErrorOnBadSHA(t *testing.T) {
	dir, _, _ := initPreflightRepo(t)
	r := &Resolver{}
	_, err := r.Resolve(context.Background(), ArtifactRef{
		Kind:    KindGitSHA,
		Path:    "hello.txt",
		GitSHA:  "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		GitRepo: dir,
	})
	if err == nil {
		t.Fatal("expected error for bad SHA")
	}
	msg := err.Error()
	for _, want := range []string{"deadbeef", "HEAD"} {
		if !strings.Contains(msg, want) {
			t.Errorf("resolveGit error missing %q; got: %s", want, msg)
		}
	}
}
