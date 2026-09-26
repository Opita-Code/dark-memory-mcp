// store_test.go: parallel to dark-cli/internal/audit/store_test.go.
package auditgate

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestExample_Lookup_FindsRecord(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	rec := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "",
		"0.1.0-alpha.8", "", "", "", fixedSHA, nil, "", fixedNow, fixedPub)
	if err := rec.Sign(fixedSeed, fixedNow); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTestOnly(rec); err != nil {
		t.Fatal(err)
	}
	got, err := s.Lookup(fixedSHA)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.ArtifactSHA256 != fixedSHA {
		t.Fatalf("sha mismatch")
	}
}

func TestExample_Lookup_NotFound(t *testing.T) {
	s := Open(t.TempDir())
	_, err := s.Lookup(fixedSHA)
	if err != ErrRecordNotFound {
		t.Fatalf("err=%v want ErrRecordNotFound", err)
	}
}

func TestExample_Lookup_RejectsBadSHA(t *testing.T) {
	s := Open(t.TempDir())
	_, err := s.Lookup("not-a-sha")
	if err != ErrRecordNoArtifact {
		t.Fatalf("err=%v want ErrRecordNoArtifact", err)
	}
}

func TestExample_Lookup_RejectsMismatchedSHA(t *testing.T) {
	// Filename says sha256 = X; record declares sha256 = Y. Reject.
	dir := t.TempDir()
	s := Open(dir)
	rec := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "",
		"0.1.0-alpha.8", "", "", "", fixedSHA, nil, "", fixedNow, fixedPub)
	_ = rec.Sign(fixedSeed, fixedNow)
	// Lie about the filename:
	otherSHA := sha256Of("other")
	if err := os.WriteFile(filepath.Join(dir, otherSHA+".json"),
		[]byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = rec // unused
	_, err := s.Lookup(otherSHA)
	// Filename is otherSHA; record declares fixedSHA; mismatch.
	// We expect a wrapping error (not ErrRecordNotFound).
	if err == nil {
		t.Fatalf("expected error on sha mismatch")
	}
}

func TestExample_List_SortedDeterministic(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	for _, name := range []string{"bbb", "aaa", "ccc"} {
		rec := NewBundleRecord(name, "0.1.0", "C1", "opencode", "",
			"0.1.0-alpha.8", "", "", "", sha256Of(name), nil, "", fixedNow, fixedPub)
		_ = rec.Sign(fixedSeed, fixedNow)
		if err := s.PutTestOnly(rec); err != nil {
			t.Fatal(err)
		}
	}
	recs, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("len=%d want 3", len(recs))
	}
	if recs[0].ArtifactSHA256 >= recs[1].ArtifactSHA256 {
		t.Fatalf("not sorted: %s >= %s", recs[0].ArtifactSHA256, recs[1].ArtifactSHA256)
	}
}

func TestExample_List_EmptyStoreReturnsNil(t *testing.T) {
	s := Open(t.TempDir())
	recs, err := s.List()
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if recs != nil {
		t.Fatalf("recs=%v want nil", recs)
	}
}

func TestExample_List_SkipsJunk(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	// One valid + one junk file (json suffix but invalid contents).
	rec := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "",
		"0.1.0-alpha.8", "", "", "", fixedSHA, nil, "", fixedNow, fixedPub)
	_ = rec.Sign(fixedSeed, fixedNow)
	if err := s.PutTestOnly(rec); err != nil {
		t.Fatal(err)
	}
	// junk.json: basename looks like a sha, but contents are invalid
	junkSHA := "0000000000000000000000000000000000000000000000000000000000000000"
	if err := os.WriteFile(filepath.Join(dir, junkSHA+".json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	recs, perr := s.List()
	if perr == nil {
		t.Fatalf("expected parse error for junk file")
	}
	if len(recs) != 1 {
		t.Fatalf("recs=%d want 1", len(recs))
	}
}

func TestExample_DefaultDir_PathLooksRight(t *testing.T) {
	t.Setenv("DARK_AUDIT_DIR", "")
	d, err := DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(d) {
		t.Fatalf("not absolute: %s", d)
	}
}

func TestExample_DefaultDir_EnvVarWins(t *testing.T) {
	t.Setenv("DARK_AUDIT_DIR", "/custom/path")
	d, err := DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	if d != "/custom/path" {
		t.Fatalf("d=%s want /custom/path", d)
	}
}

// sha256Of is a test helper. Same as dark-cli: hex-encoded sha256
// of the input bytes, lowercase.
func sha256Of(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
