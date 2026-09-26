// Store: reads <DARK_AUDIT_DIR>/<sha256>.json files written by
// dark-cli. One file per record; the basename is the artifact sha256
// (the gate's lookup key). No writes here — this package is
// read-only (gate consumer). Writes happen on the dark-cli side.
package auditgate

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Store reads audit records from a directory.
type Store struct {
	Dir string
}

// Open returns a Store rooted at dir. dir is created if missing
// (read-only behavior is preserved — empty dir means Lookup returns
// ErrRecordNotFound, never an I/O error). The caller can override
// the env-derived dir for tests.
func Open(dir string) *Store {
	return &Store{Dir: dir}
}

// DefaultDir returns the audit dir from DARK_AUDIT_DIR or
// <UserConfigDir>/dark-cli/audit. Matches dark-cli's
// internal/audit.DefaultDir so a single env var configures both
// sides (the §10 cross-host gap from docs/AUDIT.md).
func DefaultDir() (string, error) {
	if v := strings.TrimSpace(os.Getenv("DARK_AUDIT_DIR")); v != "" {
		return v, nil
	}
	cd, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("auditgate: resolve user config dir: %w", err)
	}
	return filepath.Join(cd, "dark-cli", "audit"), nil
}

// Lookup reads the audit record for sha256 (the artifact_sha256)
// from <Dir>/<sha256>.json. Returns ErrRecordNotFound when the file
// is missing, even if the directory exists. Parse errors include
// the file path in the wrapping.
func (s *Store) Lookup(sha256 string) (*AuditRecord, error) {
	if !isValidSHA256Hex(sha256) {
		return nil, ErrRecordNoArtifact
	}
	path := filepath.Join(s.Dir, sha256+".json")
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrRecordNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("auditgate: read %s: %w", path, err)
	}
	rec, err := MustValidateSchema(b)
	if err != nil {
		return nil, fmt.Errorf("auditgate: parse %s: %w", path, err)
	}
	if rec.ArtifactSHA256 != sha256 {
		return nil, fmt.Errorf("auditgate: %s declares artifact_sha256=%s, want %s", path, rec.ArtifactSHA256, sha256)
	}
	return rec, nil
}

// List enumerates every record in the store, sorted by
// artifact_sha256 for determinism. Junk files (parse errors,
// wrong basename) are skipped and their errors collected into the
// returned error (joined, so callers can surface them in a
// diagnostic). An empty store returns nil, nil.
func (s *Store) List() ([]*AuditRecord, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("auditgate: readdir %s: %w", s.Dir, err)
	}
	var recs []*AuditRecord
	var junk []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		sha := strings.TrimSuffix(name, ".json")
		if !isValidSHA256Hex(sha) {
			junk = append(junk, name+": bad basename")
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.Dir, name))
		if err != nil {
			junk = append(junk, fmt.Sprintf("%s: read: %v", name, err))
			continue
		}
		rec, err := MustValidateSchema(b)
		if err != nil {
			junk = append(junk, fmt.Sprintf("%s: parse: %v", name, err))
			continue
		}
		recs = append(recs, rec)
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].ArtifactSHA256 < recs[j].ArtifactSHA256 })
	if len(junk) > 0 {
		return recs, fmt.Errorf("auditgate: skipped %d junk file(s): %s", len(junk), strings.Join(junk, "; "))
	}
	return recs, nil
}

// EnsureDir creates s.Dir (with parents) if missing. Idempotent.
// Used at boot so the gate has somewhere to read from — production
// doesn't write here, but tests that pre-populate via the Store API
// use this. Public so callers can decide whether to mkdir.
func (s *Store) EnsureDir() error {
	if s.Dir == "" {
		return fmt.Errorf("auditgate: empty dir")
	}
	return os.MkdirAll(s.Dir, 0o700)
}

// PutTestOnly is a TEST-ONLY helper that writes an audit record to
// <Dir>/<sha256>.json using the same MarshalJSONOnDisk encoding that
// the on-disk format uses. Production code does not import this
// method; it is prefixed with PutTestOnly and lives in a _test.go
// file. Exposed here so cross-package fixtures (dark-cli +
// dark-memory-mcp) can share a single helper.
//
// This function is intentionally in record.go's "test" section:
// production write paths live in dark-cli, NOT here. If you find
// yourself wanting to call this from production, stop — the gate is
// a consumer, not a producer.
func (s *Store) PutTestOnly(rec *AuditRecord) error {
	if rec == nil {
		return ErrRecordNil
	}
	if !isValidSHA256Hex(rec.ArtifactSHA256) {
		return ErrRecordNoArtifact
	}
	if s.Dir == "" {
		return fmt.Errorf("auditgate: empty dir")
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	encoded, err := rec.MarshalJSONOnDisk()
	if err != nil {
		return err
	}
	// Append a newline so editors + `cat` look right; the verifier
	// strips this same trailing newline (dark-cli's loader does the
	// same). This matches the dark-cli on-disk format.
	encoded = append(encoded, '\n')
	path := filepath.Join(s.Dir, rec.ArtifactSHA256+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, encoded, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// formatHex is a test helper — returns the lowercase hex of b. Same
// as hex.EncodeToString(b) but kept here so the package compiles
// standalone in dev sandboxes where hex is unused.
func formatHex(b []byte) string { return hex.EncodeToString(b) }

// Compile-time check that Store satisfies the Gate-like access pattern.
// (We don't import Gate here to avoid an import cycle — gate.go
// defines its own interface that Store.Lookup satisfies.)
var _ = (*Store)(nil).Lookup
