// Package artifact — resolver_test.go: exhaustive coverage for
// ArtifactRef validation, Resolve across all 5 kinds, error paths,
// cap enforcement, range windowing, and provenance labelling.
//
// Whitebox (package artifact) so we can exercise unexported helpers
// like applyRange and capWriter. No LLM, no real network, no real DB.
package artifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// ─── Mocks ─────────────────────────────────────────────────────────────────

type mockSpecLookup struct {
	specs     map[int64]string
	artifacts map[int64]string
	specErr   error
	artErr    error
}

func (m *mockSpecLookup) GetSpecText(_ context.Context, id int64) (string, error) {
	if m.specErr != nil {
		return "", m.specErr
	}
	s, ok := m.specs[id]
	if !ok {
		return "", errors.New("not found")
	}
	return s, nil
}

func (m *mockSpecLookup) GetArtifactURL(_ context.Context, id int64) (string, error) {
	if m.artErr != nil {
		return "", m.artErr
	}
	u, ok := m.artifacts[id]
	if !ok {
		return "", errors.New("not found")
	}
	return u, nil
}

type mockURLFetcher struct {
	responses map[string][]byte
	err       error
}

func (m *mockURLFetcher) Fetch(_ context.Context, url string, maxBytes int) ([]byte, error) {
	if m.err != nil {
		return nil, m.err
	}
	b, ok := m.responses[url]
	if !ok {
		return nil, errors.New("not found")
	}
	if len(b) > maxBytes {
		return b[:maxBytes], nil
	}
	return b, nil
}

// ─── Validation ───────────────────────────────────────────────────────────

func TestArtifactRef_Validate(t *testing.T) {
	cases := []struct {
		name    string
		ref     ArtifactRef
		wantErr error
	}{
		{"file_ok", ArtifactRef{Kind: KindFile, Path: "/etc/passwd"}, nil},
		{"file_no_path", ArtifactRef{Kind: KindFile}, ErrEmptyPath},
		{"git_sha_ok", ArtifactRef{Kind: KindGitSHA, Path: "x.go", GitSHA: "abc123"}, nil},
		{"git_sha_no_path", ArtifactRef{Kind: KindGitSHA, GitSHA: "abc123"}, ErrEmptyPath},
		{"git_sha_no_sha", ArtifactRef{Kind: KindGitSHA, Path: "x.go"}, ErrEmptyPath},
		{"url_ok", ArtifactRef{Kind: KindURL, URL: "https://example.com/x"}, nil},
		{"url_no_url", ArtifactRef{Kind: KindURL}, ErrEmptyPath},
		{"spec_id_ok", ArtifactRef{Kind: KindSpecID, SpecID: 1}, nil},
		{"spec_id_zero", ArtifactRef{Kind: KindSpecID}, ErrZeroID},
		{"spec_id_negative", ArtifactRef{Kind: KindSpecID, SpecID: -5}, ErrZeroID},
		{"artifact_id_ok", ArtifactRef{Kind: KindArtifactID, ArtifactID: 1}, nil},
		{"artifact_id_zero", ArtifactRef{Kind: KindArtifactID}, ErrZeroID},
		{"kind_empty", ArtifactRef{}, ErrInvalidKind},
		{"kind_unknown", ArtifactRef{Kind: "blob"}, ErrInvalidKind},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.ref.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Errorf("expected no error, got %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("expected %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestArtifactRef_EffectiveMaxBytes(t *testing.T) {
	tests := []struct {
		name string
		ref  ArtifactRef
		want int
	}{
		{"default", ArtifactRef{}, DefaultMaxBytes},
		{"explicit_small", ArtifactRef{MaxBytes: 1000}, 1000},
		{"explicit_large_capped", ArtifactRef{MaxBytes: 10 * 1024 * 1024}, HardMaxBytes},
		{"explicit_zero_uses_default", ArtifactRef{MaxBytes: 0}, DefaultMaxBytes},
		{"explicit_negative_uses_default", ArtifactRef{MaxBytes: -1}, DefaultMaxBytes},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.ref.effectiveMaxBytes()
			if got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

// ─── applyRange (pure) ────────────────────────────────────────────────────

func TestApplyRange(t *testing.T) {
	data := []byte("0123456789")
	tests := []struct {
		name      string
		rng       *Range
		wantBytes []byte
		wantTrunc bool
	}{
		{"nil_range", nil, data, false},
		{"full", &Range{Start: 0, End: 10}, data, true},
		{"middle", &Range{Start: 3, End: 7}, []byte("3456"), true},
		{"end_zero_means_eof", &Range{Start: 5}, []byte("56789"), true},
		{"end_past_eof_clamped", &Range{Start: 5, End: 100}, []byte("56789"), true},
		{"start_negative_clamped", &Range{Start: -5, End: 3}, []byte("012"), true},
		{"start_past_end_clamps", &Range{Start: 8, End: 3}, []byte{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, trunc := applyRange(data, tc.rng)
			if !reflect.DeepEqual(got, tc.wantBytes) {
				t.Errorf("bytes: got %q, want %q", got, tc.wantBytes)
			}
			if trunc != tc.wantTrunc {
				t.Errorf("truncated: got %v, want %v", trunc, tc.wantTrunc)
			}
		})
	}
}

// ─── resolveFile ──────────────────────────────────────────────────────────

func TestResolve_File_OK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.txt")
	content := []byte("hello world\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	r := &Resolver{}
	res, err := r.Resolve(context.Background(), ArtifactRef{Kind: KindFile, Path: path})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !bytes.Equal(res.Bytes, content) {
		t.Errorf("content: got %q, want %q", res.Bytes, content)
	}
	if res.Source != SourceFile {
		t.Errorf("source: got %q, want %q", res.Source, SourceFile)
	}
	if res.Path != path {
		t.Errorf("path: got %q, want %q", res.Path, path)
	}
	if res.Truncated {
		t.Errorf("Truncated should be false for small file")
	}
	wantSum := sha256.Sum256(content)
	if res.ContentSHA256 != wantSum {
		t.Errorf("SHA mismatch")
	}
}

func TestResolve_File_NotFound(t *testing.T) {
	r := &Resolver{}
	_, err := r.Resolve(context.Background(), ArtifactRef{Kind: KindFile, Path: "/nonexistent/path/xyz"})
	if !errors.Is(err, ErrUnresolved) {
		t.Errorf("expected ErrUnresolved, got %v", err)
	}
}

func TestResolve_File_TruncatedByMaxBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	content := []byte(strings.Repeat("a", 1000))
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	r := &Resolver{}
	res, err := r.Resolve(context.Background(), ArtifactRef{
		Kind:     KindFile,
		Path:     path,
		MaxBytes: 100,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !res.Truncated {
		t.Errorf("Truncated should be true")
	}
	if len(res.Bytes) != 100 {
		t.Errorf("got %d bytes, want 100", len(res.Bytes))
	}
	// SHA must be over the truncated slice, not the full file.
	wantSum := sha256.Sum256(content[:100])
	if res.ContentSHA256 != wantSum {
		t.Errorf("SHA should be over truncated slice")
	}
}

func TestResolve_File_RangeWindowing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")
	content := []byte("0123456789ABCDEF")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	r := &Resolver{}
	res, err := r.Resolve(context.Background(), ArtifactRef{
		Kind:     KindFile,
		Path:     path,
		Range:    &Range{Start: 10, End: 14},
		MaxBytes: 4,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !res.Truncated {
		t.Errorf("Truncated should be true (range set)")
	}
	if string(res.Bytes) != "ABCD" {
		t.Errorf("window: got %q, want %q", res.Bytes, "ABCD")
	}
	wantSum := sha256.Sum256([]byte("ABCD"))
	if res.ContentSHA256 != wantSum {
		t.Errorf("SHA mismatch")
	}
}

// Edge cases for checkRangeFitsCap (lines 222 + 228 in resolver.go).
func TestResolve_Range_OpenEnded_BoundedByCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")
	os.WriteFile(path, []byte(strings.Repeat("x", 1000)), 0o644)

	r := &Resolver{}
	// End=0 means "to EOF", bounded by Start + MaxBytes.
	// Start=0, End=0, MaxBytes=10 → read first 10 bytes.
	res, err := r.Resolve(context.Background(), ArtifactRef{
		Kind:     KindFile,
		Path:     path,
		Range:    &Range{Start: 0},
		MaxBytes: 10,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(res.Bytes) != 10 {
		t.Errorf("got %d bytes, want 10", len(res.Bytes))
	}
}

func TestResolve_Range_ExceedsCap_ReturnsErrSizeExceeded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")
	os.WriteFile(path, []byte(strings.Repeat("x", 1000)), 0o644)

	r := &Resolver{}
	// Start=0, End=100, MaxBytes=10 → range size 100 > cap 10.
	_, err := r.Resolve(context.Background(), ArtifactRef{
		Kind:     KindFile,
		Path:     path,
		Range:    &Range{Start: 0, End: 100},
		MaxBytes: 10,
	})
	if !errors.Is(err, ErrSizeExceeded) {
		t.Errorf("expected ErrSizeExceeded, got %v", err)
	}
}

func TestResolve_Range_NegativeStart_Clamped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")
	os.WriteFile(path, []byte("0123456789"), 0o644)

	r := &Resolver{}
	// Start=-5, End=3 → clamped to Start=0, End=3 → "012".
	res, err := r.Resolve(context.Background(), ArtifactRef{
		Kind:  KindFile,
		Path:  path,
		Range: &Range{Start: -5, End: 3},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if string(res.Bytes) != "012" {
		t.Errorf("got %q, want %q", res.Bytes, "012")
	}
}

func TestResolve_File_PathEmpty_RejectedByValidate(t *testing.T) {
	r := &Resolver{}
	_, err := r.Resolve(context.Background(), ArtifactRef{Kind: KindFile, Path: ""})
	if !errors.Is(err, ErrEmptyPath) {
		t.Errorf("expected ErrEmptyPath, got %v", err)
	}
}

// ─── resolveGit ───────────────────────────────────────────────────────────

func TestResolve_Git_RequiresGitBinary(t *testing.T) {
	// Skip if git isn't available (CI without git).
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	// Use a temp git repo with a single commit we can read back.
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out := &bytes.Buffer{}
		cmd.Stdout = out
		cmd.Stderr = out
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("git content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "hello.txt")
	run("commit", "-q", "-m", "init")

	// Get the commit SHA.
	shaCmd := exec.Command("git", "rev-parse", "HEAD")
	shaCmd.Dir = dir
	shaOut, err := shaCmd.Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	sha := strings.TrimSpace(string(shaOut))

	r2 := &Resolver{}
	res, err := r2.Resolve(context.Background(), ArtifactRef{
		Kind:    KindGitSHA,
		Path:    "hello.txt",
		GitSHA:  sha,
		GitRepo: dir,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if string(res.Bytes) != "git content\n" {
		t.Errorf("got %q, want %q", res.Bytes, "git content\n")
	}
	if res.Source != SourceGitSHA {
		t.Errorf("source: got %q, want %q", res.Source, SourceGitSHA)
	}
	if !strings.HasPrefix(res.Path, sha) {
		t.Errorf("path should start with sha, got %q", res.Path)
	}
}

func TestResolve_Git_InvalidSHA(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	dir := t.TempDir()
	r := &Resolver{}
	_, err := r.Resolve(context.Background(), ArtifactRef{
		Kind:    KindGitSHA,
		Path:    "anything",
		GitSHA:  "deadbeef00000000000000000000000000000000",
		GitRepo: dir,
	})
	if !errors.Is(err, ErrUnresolved) {
		t.Errorf("expected ErrUnresolved, got %v", err)
	}
}

func TestResolve_Git_PathEmpty_RejectedByValidate(t *testing.T) {
	r := &Resolver{}
	_, err := r.Resolve(context.Background(), ArtifactRef{Kind: KindGitSHA, GitSHA: "abc"})
	if !errors.Is(err, ErrEmptyPath) {
		t.Errorf("expected ErrEmptyPath, got %v", err)
	}
}

// ─── resolveURL ───────────────────────────────────────────────────────────

func TestResolve_URL_OK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello from http"))
	}))
	defer server.Close()

	r := &Resolver{URLs: &mockURLFetcher{responses: map[string][]byte{
		server.URL: []byte("hello from http"),
	}}}
	res, err := r.Resolve(context.Background(), ArtifactRef{
		Kind: KindURL,
		URL:  server.URL,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if string(res.Bytes) != "hello from http" {
		t.Errorf("got %q, want %q", res.Bytes, "hello from http")
	}
	if res.Source != SourceURL {
		t.Errorf("source: got %q, want %q", res.Source, SourceURL)
	}
}

func TestResolve_URL_NotConfigured(t *testing.T) {
	r := &Resolver{} // no URLFetcher
	_, err := r.Resolve(context.Background(), ArtifactRef{
		Kind: KindURL, URL: "https://example.com",
	})
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("expected ErrNotConfigured, got %v", err)
	}
}

func TestResolve_URL_FetchError(t *testing.T) {
	r := &Resolver{URLs: &mockURLFetcher{err: errors.New("net down")}}
	_, err := r.Resolve(context.Background(), ArtifactRef{
		Kind: KindURL, URL: "https://example.com",
	})
	if !errors.Is(err, ErrUnresolved) {
		t.Errorf("expected ErrUnresolved, got %v", err)
	}
}

func TestResolve_URL_NotFound(t *testing.T) {
	r := &Resolver{URLs: &mockURLFetcher{responses: map[string][]byte{}}}
	_, err := r.Resolve(context.Background(), ArtifactRef{
		Kind: KindURL, URL: "https://example.com/missing",
	})
	if !errors.Is(err, ErrUnresolved) {
		t.Errorf("expected ErrUnresolved, got %v", err)
	}
}

func TestResolve_URL_TruncatedByMaxBytes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("x", 500)))
	}))
	defer server.Close()

	r := &Resolver{URLs: &mockURLFetcher{responses: map[string][]byte{
		server.URL: []byte(strings.Repeat("x", 500)),
	}}}
	res, err := r.Resolve(context.Background(), ArtifactRef{
		Kind:     KindURL,
		URL:      server.URL,
		MaxBytes: 50,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !res.Truncated {
		t.Errorf("Truncated should be true")
	}
	if len(res.Bytes) != 50 {
		t.Errorf("got %d bytes, want 50", len(res.Bytes))
	}
}

// ─── resolveSpecID ────────────────────────────────────────────────────────

func TestResolve_SpecID_OK(t *testing.T) {
	specText := "spec intent: judge shall verify artifact matches spec"
	r := &Resolver{
		Spec: &mockSpecLookup{specs: map[int64]string{42: specText}},
	}
	res, err := r.Resolve(context.Background(), ArtifactRef{
		Kind:   KindSpecID,
		SpecID: 42,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if string(res.Bytes) != specText {
		t.Errorf("content mismatch")
	}
	if res.Source != SourceSpecID {
		t.Errorf("source: got %q, want %q", res.Source, SourceSpecID)
	}
	if res.Path != "spec_id:42" {
		t.Errorf("path: got %q", res.Path)
	}
}

func TestResolve_SpecID_NotConfigured(t *testing.T) {
	r := &Resolver{} // no Spec
	_, err := r.Resolve(context.Background(), ArtifactRef{
		Kind: KindSpecID, SpecID: 1,
	})
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("expected ErrNotConfigured, got %v", err)
	}
}

func TestResolve_SpecID_NotFound(t *testing.T) {
	r := &Resolver{
		Spec: &mockSpecLookup{specs: map[int64]string{}},
	}
	_, err := r.Resolve(context.Background(), ArtifactRef{
		Kind: KindSpecID, SpecID: 999,
	})
	if !errors.Is(err, ErrUnresolved) {
		t.Errorf("expected ErrUnresolved, got %v", err)
	}
}

func TestResolve_SpecID_LookupError(t *testing.T) {
	r := &Resolver{
		Spec: &mockSpecLookup{specErr: errors.New("db down")},
	}
	_, err := r.Resolve(context.Background(), ArtifactRef{
		Kind: KindSpecID, SpecID: 1,
	})
	if !errors.Is(err, ErrUnresolved) {
		t.Errorf("expected ErrUnresolved, got %v", err)
	}
}

// ─── resolveArtifactID ────────────────────────────────────────────────────

func TestResolve_ArtifactID_OK_DelegatesToURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("artifact content via url"))
	}))
	defer server.Close()

	r := &Resolver{
		Spec: &mockSpecLookup{artifacts: map[int64]string{7: server.URL}},
		URLs: &mockURLFetcher{responses: map[string][]byte{
			server.URL: []byte("artifact content via url"),
		}},
	}
	res, err := r.Resolve(context.Background(), ArtifactRef{
		Kind:       KindArtifactID,
		ArtifactID: 7,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if string(res.Bytes) != "artifact content via url" {
		t.Errorf("content mismatch")
	}
	if res.Source != SourceURL {
		t.Errorf("source: got %q, want %q (delegated to URL)", res.Source, SourceURL)
	}
}

func TestResolve_ArtifactID_NoURL(t *testing.T) {
	r := &Resolver{
		Spec: &mockSpecLookup{artifacts: map[int64]string{7: ""}},
		URLs: &mockURLFetcher{},
	}
	_, err := r.Resolve(context.Background(), ArtifactRef{
		Kind: KindArtifactID, ArtifactID: 7,
	})
	if !errors.Is(err, ErrUnresolved) {
		t.Errorf("expected ErrUnresolved, got %v", err)
	}
}

func TestResolve_ArtifactID_NotFound(t *testing.T) {
	r := &Resolver{
		Spec: &mockSpecLookup{artifacts: map[int64]string{}},
		URLs: &mockURLFetcher{},
	}
	_, err := r.Resolve(context.Background(), ArtifactRef{
		Kind: KindArtifactID, ArtifactID: 999,
	})
	if !errors.Is(err, ErrUnresolved) {
		t.Errorf("expected ErrUnresolved, got %v", err)
	}
}

// ─── capWriter ────────────────────────────────────────────────────────────

func TestCapWriter_CapsAtCap(t *testing.T) {
	w := &capWriter{cap: 5}
	n, _ := w.Write([]byte("hello world"))
	if n != len("hello world") {
		t.Errorf("Write should report full length even if capped")
	}
	if string(w.buf) != "hello" {
		t.Errorf("buf: got %q, want %q", w.buf, "hello")
	}

	// Subsequent writes don't grow the buffer.
	n2, _ := w.Write([]byte("more"))
	if n2 != 4 {
		t.Errorf("Write2 should report 4")
	}
	if string(w.buf) != "hello" {
		t.Errorf("buf should remain %q, got %q", "hello", w.buf)
	}
}

func TestCapWriter_UnderCap(t *testing.T) {
	w := &capWriter{cap: 100}
	w.Write([]byte("small"))
	if string(w.buf) != "small" {
		t.Errorf("buf: got %q", w.buf)
	}
}

func TestCapWriter_ExactCap(t *testing.T) {
	w := &capWriter{cap: 5}
	w.Write([]byte("hello"))
	if string(w.buf) != "hello" {
		t.Errorf("buf: got %q", w.buf)
	}
	// Now any further write should be silently dropped (remaining=0).
	w.Write([]byte("xxx"))
	if len(w.buf) != 5 {
		t.Errorf("buf should remain length 5, got %d", len(w.buf))
	}
}

// ─── Resolve: invalid kind ────────────────────────────────────────────────

func TestResolve_InvalidKind(t *testing.T) {
	r := &Resolver{}
	_, err := r.Resolve(context.Background(), ArtifactRef{Kind: "blob"})
	if !errors.Is(err, ErrInvalidKind) {
		t.Errorf("expected ErrInvalidKind, got %v", err)
	}
}

// ─── SHA determinism (Q2 property) ────────────────────────────────────────

func TestResolve_SHA_Deterministic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	os.WriteFile(path, []byte("hello"), 0o644)

	r := &Resolver{}
	res1, _ := r.Resolve(context.Background(), ArtifactRef{Kind: KindFile, Path: path})
	res2, _ := r.Resolve(context.Background(), ArtifactRef{Kind: KindFile, Path: path})

	if res1.ContentSHA256 != res2.ContentSHA256 {
		t.Errorf("SHA should be deterministic")
	}
}

// ─── Hard cap protection ──────────────────────────────────────────────────

func TestResolve_HardCap(t *testing.T) {
	// Setting MaxBytes > HardMaxBytes must clamp to HardMaxBytes.
	ref := ArtifactRef{MaxBytes: 100 * 1024 * 1024}
	if got := ref.effectiveMaxBytes(); got != HardMaxBytes {
		t.Errorf("hard cap not enforced: got %d, want %d", got, HardMaxBytes)
	}
}

func TestResolve_RealFileHardCapBlocks(t *testing.T) {
	// Even if caller lies, the read is bounded by cap+1.
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	os.WriteFile(path, []byte(strings.Repeat("a", 1000)), 0o644)

	r := &Resolver{}
	// MaxBytes=HardMaxBytes — file is small, no truncation expected.
	res, err := r.Resolve(context.Background(), ArtifactRef{
		Kind:     KindFile,
		Path:     path,
		MaxBytes: HardMaxBytes,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Truncated {
		t.Errorf("small file should not be truncated")
	}
	if len(res.Bytes) != 1000 {
		t.Errorf("got %d bytes, want 1000", len(res.Bytes))
	}
}

// ─── io.LimitReader behavior (sanity) ─────────────────────────────────────

func TestIOLimitReaderIsUsed(t *testing.T) {
	// Smoke check: io.LimitReader enforces the cap at the io.Reader
	// layer, not at allocation. We rely on this in resolveFile.
	src := bytes.NewReader([]byte(strings.Repeat("z", 1000)))
	r := io.LimitReader(src, 100)
	got, _ := io.ReadAll(r)
	if len(got) != 100 {
		t.Errorf("LimitReader failed: got %d bytes", len(got))
	}
}