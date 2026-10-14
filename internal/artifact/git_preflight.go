// Package artifact — git_preflight.go: pre-flight validation for
// git_sha ArtifactRefs (Phase 8 / Sesión 8, 2026-10-14, spec 1997).
//
// # Why this file exists
//
// The drift_judge pipeline resolves artifact_ref via
// `git cat-file -p <sha>:<path>` (resolveGit). When the operator
// passes a malformed SHA, a SHA from a re-written branch, or a path
// that does not exist in the SHA, resolution failed with the generic
// message
//
//	artifact: unresolved: git cat-file <sha>:<path>: exit status 128
//	  (stderr: fatal: path '<path>' exists on disk, but not in '<sha>')
//
// which does not say WHAT failed nor how to fix it (production
// Error Observatory IDs 539 + 540, count=2 each, unresolved).
//
// Worse, PublishVibe persisted the spec row + artifact row BEFORE
// the drift check ran, so every typo left garbage rows behind
// (artifacts 1658/1660 + drifts 1585/1587 were pollution, not
// signal). R12 (no garbage rows) requires validation BEFORE the
// first write.
//
// This file provides the single source of truth used by BOTH the
// pre-flight (PublishVibe calls PreflightGitRef before SaveSpec)
// and the resolver's own error path (resolveGit calls it to build
// the actionable message when cat-file fails). One helper, two
// call sites, zero new canonical tools (LUCIDEZ R11–R17 gate:
// surface stays frozen).
package artifact

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// PreflightReport is the outcome of PreflightGitRef. Exists=true
// means `<sha>:<path>` resolves to a blob; callers proceed.
// Exists=false means the caller must surface ActionableError
// INSTEAD of persisting anything (R12).
type PreflightReport struct {
	// ResolvedSHA is the full 40-char commit SHA after git
	// expansion of short prefixes. Empty when the SHA itself is
	// malformed (see ActionableError).
	ResolvedSHA string
	// PathEcho echoes the requested path (cleaned).
	PathEcho string
	// Exists is true when the blob resolves.
	Exists bool
	// BlobSHA + SizeBytes describe the blob; set when Exists=true.
	BlobSHA   string
	SizeBytes int64
	// Suggestions lists same-basename paths that DO exist in the
	// SHA (up to limit); set when Exists=false and the SHA parsed.
	Suggestions []string
	// CurrentHEAD is HEAD's full SHA in the repo; always populated
	// when the repo is readable. Gives the operator a stable
	// reference point ("did you mean HEAD?").
	CurrentHEAD string
	// HeadsContaining is the count of local branches containing
	// ResolvedSHA. 0 = unreachable (rewritten branch, amend,
	// force-push) — the most common cause of "exists on disk but
	// not in <sha>".
	HeadsContaining int
	// ActionableError is the human + machine readable message to
	// surface when Exists=false. Contains: what failed, the
	// received SHA vs HEAD, suggestions, and the exact recovery
	// recipe (copy-pasteable git command). Never empty when
	// Exists=false.
	ActionableError string
}

// PreflightGitRef validates that `<sha>:<path>` resolves in repo.
// repo empty = "." (same default as resolveGit's cmd.Dir behavior).
// limit caps suggestions (0 = default 5, max 20).
//
// Pure read-only: runs `git rev-parse`, `git branch --points-at`,
// `git cat-file -e`, `git ls-tree`. No writes anywhere.
func PreflightGitRef(ctx context.Context, repo, sha, path string, limit int) PreflightReport {
	if repo == "" {
		repo = "."
	}
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}
	cleanPath := filepath.ToSlash(filepath.Clean(path))
	cleanPath = strings.TrimLeft(cleanPath, "/")
	rep := PreflightReport{PathEcho: cleanPath}

	headSHA, err := gitOut(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		rep.ActionableError = fmt.Sprintf("artifact: pre-flight: not a readable git repo %q: %v. "+
			"Set artifact_ref.git_repo to the repo working dir, or use kind=file for working-tree bytes.", repo, err)
		return rep
	}
	rep.CurrentHEAD = headSHA

	want := sha
	if want == "" {
		want = "HEAD"
	}
	resolved, err := gitOut(ctx, repo, "rev-parse", "--verify", want+"^{commit}")
	if err != nil {
		resolved, err = gitOut(ctx, repo, "rev-parse", "--verify", want)
		if err != nil {
			rep.ResolvedSHA = headSHA
			rep.ActionableError = fmt.Sprintf("artifact: pre-flight: git rev-parse %q failed: %v. "+
				"HEAD is %s. Check the SHA is reachable (no amend / force-push since it was copied). "+
				"Recover: git rev-parse HEAD  # then use the full SHA it prints", want, err, headSHA)
			return rep
		}
	}
	rep.ResolvedSHA = resolved

	if branches, _ := gitOut(ctx, repo, "branch", "--points-at", resolved, "--list"); strings.TrimSpace(branches) != "" {
		rep.HeadsContaining = len(strings.Split(strings.TrimSpace(branches), "\n"))
	}

	if cleanPath == "" || cleanPath == "." {
		rep.ActionableError = fmt.Sprintf("artifact: pre-flight: path %q resolves to repo root; provide a file path "+
			"(e.g. internal/orchestration/orchestrator.go). SHA %s, HEAD %s.", path, resolved, headSHA)
		return rep
	}

	if err := gitRun(ctx, repo, "cat-file", "-e", resolved+":"+cleanPath); err == nil {
		rep.Exists = true
		if entry, lerr := gitOut(ctx, repo, "ls-tree", "-l", resolved, "--", cleanPath); lerr == nil {
			if parts := strings.Fields(entry); len(parts) >= 4 {
				rep.BlobSHA = parts[2]
				fmt.Sscanf(parts[3], "%d", &rep.SizeBytes)
			}
		}
		return rep
	}

	sugg, _ := suggestSameBasename(ctx, repo, resolved, cleanPath, limit)
	rep.Suggestions = sugg

	var sb strings.Builder
	fmt.Fprintf(&sb, "artifact: pre-flight: path %q does not exist in %s. ", cleanPath, resolved)
	if resolved != headSHA {
		fmt.Fprintf(&sb, "HEAD is %s (different commit). ", headSHA)
	}
	if rep.HeadsContaining == 0 {
		fmt.Fprintf(&sb, "SHA is unreachable from any local branch (rewritten branch / amend / force-push?). ")
	}
	if len(sugg) > 0 {
		fmt.Fprintf(&sb, "Did you mean one of: %s? ", strings.Join(sugg, ", "))
	} else {
		fmt.Fprintf(&sb, "No same-basename path exists in this commit. ")
	}
	fmt.Fprintf(&sb, "Recover: git rev-parse HEAD  # use the printed SHA, it always contains your committed files")
	rep.ActionableError = sb.String()
	return rep
}

// suggestSameBasename lists up-to-limit paths in the commit's tree
// sharing target's basename. Same-directory matches rank first.
func suggestSameBasename(ctx context.Context, repo, sha, target string, limit int) ([]string, error) {
	base := filepath.Base(target)
	all, err := gitOut(ctx, repo, "ls-tree", "-r", "--name-only", sha)
	if err != nil || all == "" {
		return nil, err
	}
	targetDir := filepath.ToSlash(filepath.Dir(target))
	type scored struct {
		path  string
		score int
	}
	var ss []scored
	for _, p := range strings.Split(all, "\n") {
		if p == "" || filepath.Base(p) != base {
			continue
		}
		s := scored{path: p, score: 2}
		if filepath.ToSlash(filepath.Dir(p)) == targetDir {
			s.score += 3
		}
		ss = append(ss, s)
	}
	for i := 1; i < len(ss); i++ {
		for j := i; j > 0; j-- {
			a, b := ss[j-1], ss[j]
			if a.score < b.score ||
				(a.score == b.score && len(a.path) > len(b.path)) ||
				(a.score == b.score && len(a.path) == len(b.path) && a.path > b.path) {
				ss[j-1], ss[j] = b, a
			} else {
				break
			}
		}
	}
	if len(ss) > limit {
		ss = ss[:limit]
	}
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.path
	}
	return out, nil
}

// gitOut runs git and returns trimmed stdout; error wraps stderr.
func gitOut(ctx context.Context, repo string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errStr := strings.TrimSpace(stderr.String())
		if errStr == "" {
			return strings.TrimSpace(stdout.String()), err
		}
		return strings.TrimSpace(stdout.String()), fmt.Errorf("%v (stderr: %s)", err, errStr)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// gitRun discards output; only the exit code matters.
func gitRun(ctx context.Context, repo string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
	return cmd.Run()
}