package orchestration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTransport_LoadArtifact_RealFile verifies the Transport loads
// a real file from disk and computes SHA256 correctly.
func TestTransport_LoadArtifact_RealFile(t *testing.T) {
	// Create a temp file with > MinArtifactBytes
	dir := t.TempDir()
	path := filepath.Join(dir, "test.md")
	content := strings.Repeat("line content\n", 1000) // ~13KB, > MinArtifactBytes
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	tr := NewTransport()
	la, err := tr.LoadArtifact(path)
	if err != nil {
		t.Fatalf("LoadArtifact: %v", err)
	}
	if la.Path != path {
		t.Errorf("Path = %q, want %q", la.Path, path)
	}
	if len(la.Bytes) == 0 {
		t.Error("Bytes is empty")
	}
	if la.Sha256Hex == "" {
		t.Error("Sha256Hex is empty")
	}
	if la.LineCount < 1 {
		t.Errorf("LineCount = %d, want >= 1", la.LineCount)
	}
	if la.SourceOrigin != SourceFilesystem {
		t.Errorf("SourceOrigin = %q, want %q", la.SourceOrigin, SourceFilesystem)
	}
}

// TestTransport_LoadArtifact_TooSmall verifies that files smaller
// than MinArtifactBytes are rejected with ErrArtifactTooSmall.
func TestTransport_LoadArtifact_TooSmall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "small.md")
	if err := os.WriteFile(path, []byte("small"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	tr := NewTransport()
	_, err := tr.LoadArtifact(path)
	if err == nil {
		t.Fatal("expected ErrArtifactTooSmall")
	}
	if !errors.Is(err, ErrArtifactTooSmall) {
		t.Errorf("expected ErrArtifactTooSmall, got: %v", err)
	}
}

// TestTransport_LoadArtifact_EmptyPath rejects empty path.
func TestTransport_LoadArtifact_EmptyPath(t *testing.T) {
	tr := NewTransport()
	_, err := tr.LoadArtifact("")
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

// TestTransport_LoadArtifact_NonExistent rejects missing file.
func TestTransport_LoadArtifact_NonExistent(t *testing.T) {
	tr := NewTransport()
	_, err := tr.LoadArtifact("/nonexistent/path/to/file.md")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

// TestLoadedArtifact_ReadLine verifies line index.
func TestLoadedArtifact_ReadLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lines.md")
	// Need > MinArtifactBytes (5000). Build content with first 4
	// lines being the test targets and padding to satisfy the size
	// guard.
	content := "first\nsecond\nthird\nfourth\n" + strings.Repeat("padding line for size guard\n", 500)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	tr := NewTransport()
	la, err := tr.LoadArtifact(path)
	if err != nil {
		t.Fatalf("LoadArtifact: %v", err)
	}

	tests := []struct {
		line int
		want string
	}{
		{1, "first"},
		{2, "second"},
		{3, "third"},
		{4, "fourth"},
	}
	for _, tc := range tests {
		got, err := la.ReadLine(tc.line)
		if err != nil {
			t.Errorf("ReadLine(%d): %v", tc.line, err)
		}
		if got != tc.want {
			t.Errorf("ReadLine(%d) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

// TestLoadedArtifact_ReadLine_OutOfRange rejects invalid line.
func TestLoadedArtifact_ReadLine_OutOfRange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lines.md")
	// Need > MinArtifactBytes (5000). Use 600 lines of "x" (~1200
	// bytes is too small; bump to 6000 bytes).
	big := strings.Repeat("x\n", 1500) // ~3000 bytes + newlines = ~4500 still too small
	// bump further
	big = strings.Repeat("x", 10000) + "\n" // 10001 bytes
	if err := os.WriteFile(path, []byte(big), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	tr := NewTransport()
	la, err := tr.LoadArtifact(path)
	if err != nil {
		t.Fatalf("LoadArtifact: %v", err)
	}

	if _, err := la.ReadLine(0); err == nil {
		t.Error("expected error for line 0")
	}
	if _, err := la.ReadLine(la.LineCount + 1); err == nil {
		t.Error("expected error for line beyond LineCount")
	}
}

// TestLoadedArtifact_Sha256 verifies SHA256 is computed correctly.
func TestLoadedArtifact_Sha256(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hashed.md")
	content := strings.Repeat("a", 6000) // 6000 bytes > MinArtifactBytes
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	tr := NewTransport()
	la, err := tr.LoadArtifact(path)
	if err != nil {
		t.Fatalf("LoadArtifact: %v", err)
	}

	if la.Sha256() == "" {
		t.Error("Sha256() returned empty")
	}
	if la.Sha256() != la.Sha256Hex {
		t.Errorf("Sha256() = %q, Sha256Hex = %q", la.Sha256(), la.Sha256Hex)
	}
}
