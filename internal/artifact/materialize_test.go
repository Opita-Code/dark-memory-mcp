package artifact

// Tests for Materializer + MaterializeFromText. T03 of spec 1276 v2.20.0.
//
// Coverage:
//   - Materialize: basic / idempotent / SHA-256 in path / different
//     sourceTags / permission 0600 / oversize / path traversal /
//     empty text / empty sourceTag / BaseDir required / content exact.
//   - Materialize + Resolver integration: SHA from materialize ==
//     SHA from resolver (end-to-end anchor).
//   - CleanupExpired: removes old files / keeps recent / counts across
//     subdirs / empty BaseDir / fresh empty dir (count=0).
//   - Concurrent Materialize: race-clean, all refs identical.
//   - Atomic write: no temp files leaked.
//   - MaterializeFromText: env var DARK_MATERIALIZE_DIR / fallback to
//     cache dir when env unset.
//
// Mutation score target: M1 ≥75%.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- Materialize ----------------------------------------------------------

func TestMaterializer_Materialize_Basic(t *testing.T) {
	m := &Materializer{BaseDir: t.TempDir()}
	ref, err := m.Materialize(context.Background(), "hello world", "test")
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if ref.Kind != KindFile {
		t.Errorf("Kind: got %v, want %v", ref.Kind, KindFile)
	}
	if ref.Path == "" {
		t.Error("Path empty")
	}
	if _, err := os.Stat(ref.Path); err != nil {
		t.Errorf("stat ref.Path: %v", err)
	}
	body, err := os.ReadFile(ref.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "hello world" {
		t.Errorf("body: %q, want %q", body, "hello world")
	}
}

func TestMaterializer_Materialize_Idempotent(t *testing.T) {
	m := &Materializer{BaseDir: t.TempDir()}
	ref1, err1 := m.Materialize(context.Background(), "hello", "test")
	ref2, err2 := m.Materialize(context.Background(), "hello", "test")
	if err1 != nil || err2 != nil {
		t.Fatalf("err1=%v err2=%v", err1, err2)
	}
	if ref1.Path != ref2.Path {
		t.Errorf("idempotent: ref1=%v ref2=%v", ref1, ref2)
	}
}

func TestMaterializer_Materialize_SHA256InPath(t *testing.T) {
	m := &Materializer{BaseDir: t.TempDir()}
	text := "this is a test"
	ref, err := m.Materialize(context.Background(), text, "test")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(ref.Path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	want := hex.EncodeToString(sum[:])
	if !strings.HasSuffix(ref.Path, want+".txt") {
		t.Errorf("filename: %q, want suffix %q.txt", ref.Path, want)
	}
}

func TestMaterializer_Materialize_DifferentSourceTags_DifferentPaths(t *testing.T) {
	m := &Materializer{BaseDir: t.TempDir()}
	ref1, _ := m.Materialize(context.Background(), "hello", "tag1")
	ref2, _ := m.Materialize(context.Background(), "hello", "tag2")
	if ref1.Path == ref2.Path {
		t.Errorf("different tags should produce different paths: %s vs %s", ref1.Path, ref2.Path)
	}
}

func TestMaterializer_Materialize_Permission0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission semantics not applicable on Windows")
	}
	m := &Materializer{BaseDir: t.TempDir()}
	ref, err := m.Materialize(context.Background(), "secret", "secret")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(ref.Path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm: got %o, want 600", perm)
	}
}

func TestMaterializer_Materialize_DirPermission0700(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission semantics not applicable on Windows")
	}
	m := &Materializer{BaseDir: t.TempDir()}
	ref, err := m.Materialize(context.Background(), "x", "mytag")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(ref.Path)
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir perm: got %o, want 700", perm)
	}
}

func TestMaterializer_Materialize_TooLarge(t *testing.T) {
	m := &Materializer{BaseDir: t.TempDir()}
	text := strings.Repeat("x", int(HardMaxBytes)+1)
	_, err := m.Materialize(context.Background(), text, "big")
	if !errors.Is(err, ErrMaterializeTooLarge) {
		t.Errorf("err: %v, want ErrMaterializeTooLarge", err)
	}
}

func TestMaterializer_Materialize_ExactlyHardMaxBytes_Allowed(t *testing.T) {
	m := &Materializer{BaseDir: t.TempDir()}
	text := strings.Repeat("x", int(HardMaxBytes))
	_, err := m.Materialize(context.Background(), text, "exact")
	if err != nil {
		t.Errorf("HardMaxBytes should be allowed: %v", err)
	}
}

func TestMaterializer_Materialize_PathTraversal_Slash(t *testing.T) {
	m := &Materializer{BaseDir: t.TempDir()}
	base := m.BaseDir
	ref, err := m.Materialize(context.Background(), "x", "/etc/passwd")
	if err != nil {
		t.Fatal(err)
	}
	// Path must be under BaseDir (no escape via /etc/passwd).
	if !strings.HasPrefix(ref.Path, base) {
		t.Errorf("path escapes BaseDir: %s not under %s", ref.Path, base)
	}
	// Subdir must NOT contain a separator — sanitization collapsed
	// the absolute path into a single subdirectory name.
	subdir := filepath.Dir(ref.Path)
	rel, err := filepath.Rel(base, subdir)
	if err != nil {
		t.Fatalf("filepath.Rel: %v", err)
	}
	if strings.Contains(rel, string(filepath.Separator)) {
		t.Errorf("sanitized subdir contains separator: %q (sanitization failed)", rel)
	}
}

func TestMaterializer_Materialize_PathTraversal_DotDot(t *testing.T) {
	m := &Materializer{BaseDir: t.TempDir()}
	base := m.BaseDir
	ref, err := m.Materialize(context.Background(), "x", "../../etc/passwd")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref.Path, base) {
		t.Errorf("path escapes BaseDir: %s not under %s", ref.Path, base)
	}
	if strings.Contains(ref.Path, "..") {
		t.Errorf("path contains '..': %s", ref.Path)
	}
}

func TestMaterializer_Materialize_PathTraversal_Backslash(t *testing.T) {
	m := &Materializer{BaseDir: t.TempDir()}
	base := m.BaseDir
	ref, err := m.Materialize(context.Background(), "x", `..\..\windows\system32`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref.Path, base) {
		t.Errorf("path escapes BaseDir: %s not under %s", ref.Path, base)
	}
}

func TestMaterializer_Materialize_EmptyText(t *testing.T) {
	m := &Materializer{BaseDir: t.TempDir()}
	ref, err := m.Materialize(context.Background(), "", "empty")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(ref.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 0 {
		t.Errorf("body length: %d, want 0", len(body))
	}
}

func TestMaterializer_Materialize_EmptySourceTag_DefaultsToDefault(t *testing.T) {
	m := &Materializer{BaseDir: t.TempDir()}
	ref, err := m.Materialize(context.Background(), "x", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ref.Path, string(filepath.Separator)+"default"+string(filepath.Separator)) {
		t.Errorf("empty tag should default to 'default', got %s", ref.Path)
	}
}

func TestMaterializer_Materialize_EmptyBaseDir_ReturnsError(t *testing.T) {
	m := &Materializer{}
	_, err := m.Materialize(context.Background(), "x", "tag")
	if err == nil {
		t.Error("expected error for empty BaseDir")
	}
}

func TestMaterializer_Materialize_ContentExact_NoExtraNewline(t *testing.T) {
	m := &Materializer{BaseDir: t.TempDir()}
	text := "hello\nworld\n"
	ref, err := m.Materialize(context.Background(), text, "test")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(ref.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != text {
		t.Errorf("body: %q, want %q", body, text)
	}
}

func TestMaterializer_Materialize_UnicodePreserved(t *testing.T) {
	m := &Materializer{BaseDir: t.TempDir()}
	text := "héllo wörld 中文 🎉"
	ref, err := m.Materialize(context.Background(), text, "unicode")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(ref.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != text {
		t.Errorf("body: %q, want %q", body, text)
	}
}

// --- Integration with Resolver (end-to-end anchor) ------------------------

func TestMaterializer_ReadableByResolver_SHAAnchor(t *testing.T) {
	m := &Materializer{BaseDir: t.TempDir()}
	text := "anchor end-to-end"
	ref, err := m.Materialize(context.Background(), text, "test")
	if err != nil {
		t.Fatal(err)
	}

	// Compute expected SHA from text directly.
	want := sha256.Sum256([]byte(text))

	// Resolver reads the file via KindFile and gets the SAME SHA.
	r := &Resolver{}
	resolved, err := r.Resolve(context.Background(), ref)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.ContentSHA256 != want {
		t.Errorf("SHA mismatch: resolver=%x materialize=%x", resolved.ContentSHA256, want)
	}
	if string(resolved.Bytes) != text {
		t.Errorf("bytes: %q, want %q", resolved.Bytes, text)
	}
}

// --- CleanupExpired -------------------------------------------------------

func TestMaterializer_CleanupExpired_RemovesOldFiles(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := &Materializer{
		BaseDir: t.TempDir(),
		Now:     func() time.Time { return now },
	}
	ref, _ := m.Materialize(context.Background(), "x", "tag")
	old := now.Add(-2 * time.Hour)
	if err := os.Chtimes(ref.Path, old, old); err != nil {
		t.Fatal(err)
	}
	removed, err := m.CleanupExpired(context.Background(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed: %d, want 1", removed)
	}
	if _, err := os.Stat(ref.Path); !os.IsNotExist(err) {
		t.Errorf("file should not exist after cleanup: %v", err)
	}
}

func TestMaterializer_CleanupExpired_KeepsRecentFiles(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := &Materializer{
		BaseDir: t.TempDir(),
		Now:     func() time.Time { return now },
	}
	ref, _ := m.Materialize(context.Background(), "x", "tag")
	recent := now.Add(-30 * time.Minute) // 30 min ago, ttl=1h → keep
	if err := os.Chtimes(ref.Path, recent, recent); err != nil {
		t.Fatal(err)
	}
	removed, err := m.CleanupExpired(context.Background(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Errorf("removed: %d, want 0", removed)
	}
	if _, err := os.Stat(ref.Path); err != nil {
		t.Errorf("file should still exist: %v", err)
	}
}

func TestMaterializer_CleanupExpired_CountsAcrossSubdirs(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := &Materializer{
		BaseDir: t.TempDir(),
		Now:     func() time.Time { return now },
	}
	r1, _ := m.Materialize(context.Background(), "a", "tag1")
	r2, _ := m.Materialize(context.Background(), "b", "tag1")
	r3, _ := m.Materialize(context.Background(), "c", "tag2")
	old := now.Add(-2 * time.Hour)
	for _, r := range []ArtifactRef{r1, r2, r3} {
		if err := os.Chtimes(r.Path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := m.CleanupExpired(context.Background(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 3 {
		t.Errorf("removed: %d, want 3", removed)
	}
}

func TestMaterializer_CleanupExpired_EmptyBaseDir_ReturnsError(t *testing.T) {
	m := &Materializer{}
	_, err := m.CleanupExpired(context.Background(), time.Hour)
	if err == nil {
		t.Error("expected error")
	}
}

func TestMaterializer_CleanupExpired_FreshDir_CountsZero(t *testing.T) {
	m := &Materializer{BaseDir: t.TempDir()}
	removed, err := m.CleanupExpired(context.Background(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Errorf("removed: %d, want 0", removed)
	}
}

// --- Concurrency + Atomicity ----------------------------------------------

func TestMaterializer_Concurrent_SameInput_AllIdenticalPath(t *testing.T) {
	m := &Materializer{BaseDir: t.TempDir()}
	text := "concurrent test"
	n := 100
	refs := make([]ArtifactRef, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			refs[i], errs[i] = m.Materialize(context.Background(), text, "concurrent")
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Errorf("err[%d]: %v", i, errs[i])
		}
		if refs[i].Path != refs[0].Path {
			t.Errorf("ref[%d].Path (%q) != ref[0].Path (%q)", i, refs[i].Path, refs[0].Path)
		}
	}
}

func TestMaterializer_Concurrent_DifferentInputs_AllDistinct(t *testing.T) {
	m := &Materializer{BaseDir: t.TempDir()}
	n := 50
	refs := make([]ArtifactRef, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Distinct per-goroutine text: "input-N-randomN"
			text := fmt.Sprintf("input-%d-%d", i, i*7)
			refs[i], errs[i] = m.Materialize(context.Background(), text, "distinct")
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Errorf("err[%d]: %v", i, errs[i])
		}
		if seen[refs[i].Path] {
			t.Errorf("duplicate path: %s", refs[i].Path)
		}
		seen[refs[i].Path] = true
	}
}

func TestMaterializer_AtomicWrite_NoTempFilesLeaked(t *testing.T) {
	m := &Materializer{BaseDir: t.TempDir()}
	ref, err := m.Materialize(context.Background(), "x", "tag")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(ref.Path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("temp file leaked: %s", e.Name())
		}
	}
}

// --- MaterializeFromText (env-driven convenience) --------------------------

func TestMaterializeFromText_UsesEnvVar(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DARK_MATERIALIZE_DIR", dir)
	ref, err := MaterializeFromText(context.Background(), "hello-env", "test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref.Path, dir) {
		t.Errorf("path: %s, want prefix %s", ref.Path, dir)
	}
	if _, err := os.Stat(ref.Path); err != nil {
		t.Errorf("file not found: %v", err)
	}
}

func TestMaterializeFromText_EmptyEnv_UsesCacheDir(t *testing.T) {
	t.Setenv("DARK_MATERIALIZE_DIR", "")
	ref, err := MaterializeFromText(context.Background(), "cache-fallback", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ref.Path); err != nil {
		t.Errorf("file not found: %v", err)
	}
	// Cleanup: remove the file we created in the user cache.
	t.Cleanup(func() { _ = os.RemoveAll(ref.Path) })
}

func TestMaterializeFromText_EmptyEnv_CreatesFileInSomeValidDir(t *testing.T) {
	t.Setenv("DARK_MATERIALIZE_DIR", "")
	ref, err := MaterializeFromText(context.Background(), "path-check", "test")
	if err != nil {
		t.Fatal(err)
	}
	// The path must be absolute and parent dir must exist.
	if !filepath.IsAbs(ref.Path) {
		t.Errorf("path should be absolute: %s", ref.Path)
	}
	dir := filepath.Dir(ref.Path)
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("parent dir should exist: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(ref.Path) })
}

func TestSanitizeSourceTag_Table(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		// Identity for safe inputs.
		{"normal", "normal"},
		{"my-tag-123", "my-tag-123"},

		// Path separators neutralized.
		{"with/slash", "with_slash"},
		{`with\backslash`, "with_backslash"},
		{"/", "_"},
		{`\`, "_"},

		// ".." segments neutralized (one underscore per "..").
		{"with..dot", "with_dot"},
		{"..", "_"},
		{"../", "__"},
		{"../../etc", "____etc"},

		// Empty defaults to "default".
		{"", "default"},
	}
	for _, tt := range tests {
		got := sanitizeSourceTag(tt.in)
		if got != tt.want {
			t.Errorf("sanitizeSourceTag(%q): got %q, want %q", tt.in, got, tt.want)
		}
	}
}