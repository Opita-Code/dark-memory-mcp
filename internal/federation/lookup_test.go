// Package federation tests. We use a temp SQLite file per test to
// avoid the modernc/sqlite "shared cache" gotcha; each test gets its
// own peer DB with a minimal vibe_artifacts + vibe_drift_reports schema
// (matching the schema in dark-research's dark.db so the validation
// check passes).
package federation

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// makePeerDB builds a temp SQLite DB with the minimum schema we need
// and pre-populates a few rows. Returns the path so the test can
// point the federation Peer at it via DARK_FEDERATION_PEER_DSN.
func makePeerDB(t *testing.T, rows []struct {
	ID              int64
	SessionID       string
	VibeCase        string
	SpecID          int64
	ArtifactURL     string
	ArtifactType    string
	Jurisdiction    string
	HasDisclosure   bool
	ValidationStatus string
	CreatedAt       string
}, drifts []struct {
	ID           int64
	ArtifactID   int64
	SpecID       int64
	Verdict      string
	ReconciledAt string
	CreatedAt    string
}) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "peer.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	ddl := `
		CREATE TABLE vibe_artifacts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT,
			vibe_case TEXT NOT NULL,
			spec_id INTEGER,
			artifact_url TEXT,
			artifact_type TEXT NOT NULL,
			brand_id TEXT,
			jurisdiction TEXT,
			has_disclosure INTEGER NOT NULL DEFAULT 0,
			validation_status TEXT NOT NULL DEFAULT 'pending',
			created_at TEXT NOT NULL,
			project_id TEXT NOT NULL DEFAULT 'default'
		);
		CREATE TABLE vibe_drift_reports (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			artifact_id INTEGER NOT NULL,
			spec_id INTEGER,
			verdict TEXT NOT NULL,
			judge_reasoning TEXT,
			reconciled_at TEXT,
			created_at TEXT NOT NULL,
			project_id TEXT NOT NULL DEFAULT 'default'
		);
	`
	if _, err := db.Exec(ddl); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	for _, r := range rows {
		_, err := db.Exec(`INSERT INTO vibe_artifacts
			(id, session_id, vibe_case, spec_id, artifact_url, artifact_type, jurisdiction, has_disclosure, validation_status, created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`,
			r.ID, r.SessionID, r.VibeCase, r.SpecID, r.ArtifactURL, r.ArtifactType,
			r.Jurisdiction, r.HasDisclosure, r.ValidationStatus, r.CreatedAt)
		if err != nil {
			t.Fatalf("insert artifact: %v", err)
		}
	}
	for _, d := range drifts {
		_, err := db.Exec(`INSERT INTO vibe_drift_reports
			(id, artifact_id, spec_id, verdict, reconciled_at, created_at)
			VALUES (?,?,?,?,?,?)`,
			d.ID, d.ArtifactID, d.SpecID, d.Verdict, d.ReconciledAt, d.CreatedAt)
		if err != nil {
			t.Fatalf("insert drift: %v", err)
		}
	}
	return path
}

// unsetPeerEnv makes sure DARK_FEDERATION_PEER_DSN is unset for the test.
func unsetPeerEnv(t *testing.T) {
	t.Helper()
	old, hadOld := os.LookupEnv(EnvPeerDSN)
	if err := os.Unsetenv(EnvPeerDSN); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	t.Cleanup(func() {
		if hadOld {
			_ = os.Setenv(EnvPeerDSN, old)
		}
	})
}

// TestNewPeerFromEnv_Disabled verifies that no env var yields (nil, nil).
func TestNewPeerFromEnv_Disabled(t *testing.T) {
	unsetPeerEnv(t)
	p, err := NewPeerFromEnv()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if p != nil {
		t.Fatalf("expected nil peer when env unset, got %v", p)
	}
	if p.IsEnabled() {
		t.Errorf("nil peer must report IsEnabled=false")
	}
}

// TestNewPeerFromEnv_InvalidSchema verifies rejection when DB lacks the
// required tables. This is a startup error (not a runtime no-op).
func TestNewPeerFromEnv_InvalidSchema(t *testing.T) {
	unsetPeerEnv(t)
	// Create a SQLite file with the WRONG schema (no vibe_artifacts).
	path := filepath.Join(t.TempDir(), "bad.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec("CREATE TABLE some_other_table (id INTEGER);"); err != nil {
		t.Fatalf("create: %v", err)
	}
	db.Close()

	t.Setenv(EnvPeerDSN, path)
	_, err = NewPeerFromEnv()
	if err == nil {
		t.Fatal("expected error for invalid schema, got nil")
	}
	if !errorsIs(err, ErrInvalidSchema) {
		t.Errorf("expected ErrInvalidSchema, got: %v", err)
	}
}

// TestNewPeerFromEnv_Valid exercises the happy path: real schema, valid
// rows, peer opens and IsEnabled()=true.
func TestNewPeerFromEnv_Valid(t *testing.T) {
	unsetPeerEnv(t)
	path := makePeerDB(t,
		[]struct {
			ID              int64
			SessionID       string
			VibeCase        string
			SpecID          int64
			ArtifactURL     string
			ArtifactType    string
			Jurisdiction    string
			HasDisclosure   bool
			ValidationStatus string
			CreatedAt       string
		}{
			{ID: 1, SessionID: "sess-a", VibeCase: "C2", ArtifactURL: "https://example/a", ArtifactType: "text", ValidationStatus: "passed", CreatedAt: "2026-07-17T00:00:00Z"},
		},
		[]struct {
			ID           int64
			ArtifactID   int64
			SpecID       int64
			Verdict      string
			ReconciledAt string
			CreatedAt    string
		}{
			{ID: 10, ArtifactID: 1, Verdict: "aligned", CreatedAt: "2026-07-17T00:00:01Z"},
		},
	)
	t.Setenv(EnvPeerDSN, path)

	p, err := NewPeerFromEnv()
	if err != nil {
		t.Fatalf("NewPeerFromEnv: %v", err)
	}
	if !p.IsEnabled() {
		t.Fatal("expected IsEnabled=true")
	}
	if p.DSN() != path {
		t.Errorf("DSN: want %q, got %q", path, p.DSN())
	}
	t.Cleanup(func() { _ = p.Close() })
}

// TestPeer_LookupArtifact_Hit verifies the cross-namespace artifact lookup.
func TestPeer_LookupArtifact_Hit(t *testing.T) {
	unsetPeerEnv(t)
	path := makePeerDB(t,
		[]struct {
			ID              int64
			SessionID       string
			VibeCase        string
			SpecID          int64
			ArtifactURL     string
			ArtifactType    string
			Jurisdiction    string
			HasDisclosure   bool
			ValidationStatus string
			CreatedAt       string
		}{
			{ID: 42, SessionID: "sess-x", VibeCase: "C4", SpecID: 7, ArtifactURL: "https://example/x.png", ArtifactType: "image", Jurisdiction: "EU", HasDisclosure: true, ValidationStatus: "passed", CreatedAt: "2026-07-17T01:02:03Z"},
		},
		nil,
	)
	t.Setenv(EnvPeerDSN, path)

	p, err := NewPeerFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })

	got, err := p.LookupArtifact(context.Background(), 42)
	if err != nil {
		t.Fatalf("LookupArtifact: %v", err)
	}
	if got == nil {
		t.Fatal("expected hit, got nil")
	}
	if got.ID != 42 || got.SessionID != "sess-x" || got.VibeCase != "C4" ||
		got.ArtifactURL != "https://example/x.png" || got.ArtifactType != "image" ||
		got.Jurisdiction != "EU" || !got.HasDisclosure || got.ValidationStatus != "passed" {
		t.Errorf("mismatch: %+v", got)
	}
}

// TestPeer_LookupArtifact_Miss verifies that a missing row returns (nil, nil).
func TestPeer_LookupArtifact_Miss(t *testing.T) {
	unsetPeerEnv(t)
	path := makePeerDB(t, nil, nil)
	t.Setenv(EnvPeerDSN, path)

	p, _ := NewPeerFromEnv()
	t.Cleanup(func() { _ = p.Close() })

	got, err := p.LookupArtifact(context.Background(), 9999)
	if err != nil {
		t.Fatalf("miss should not be an error, got: %v", err)
	}
	if got != nil {
		t.Errorf("miss should be nil, got: %+v", got)
	}
}

// TestPeer_LookupDrift_NewestFirst verifies that drift lookup returns the
// latest report by id (ORDER BY id DESC LIMIT 1).
func TestPeer_LookupDrift_NewestFirst(t *testing.T) {
	unsetPeerEnv(t)
	path := makePeerDB(t,
		[]struct {
			ID              int64
			SessionID       string
			VibeCase        string
			SpecID          int64
			ArtifactURL     string
			ArtifactType    string
			Jurisdiction    string
			HasDisclosure   bool
			ValidationStatus string
			CreatedAt       string
		}{
			{ID: 5, VibeCase: "C2", ArtifactType: "text", ValidationStatus: "pending", CreatedAt: "2026-07-17T00:00:00Z"},
		},
		[]struct {
			ID           int64
			ArtifactID   int64
			SpecID       int64
			Verdict      string
			ReconciledAt string
			CreatedAt    string
		}{
			{ID: 100, ArtifactID: 5, Verdict: "drift_detected", CreatedAt: "2026-07-17T00:00:01Z"},
			{ID: 101, ArtifactID: 5, Verdict: "aligned", ReconciledAt: "2026-07-17T00:00:02Z", CreatedAt: "2026-07-17T00:00:02Z"},
		},
	)
	t.Setenv(EnvPeerDSN, path)
	p, _ := NewPeerFromEnv()
	t.Cleanup(func() { _ = p.Close() })

	got, err := p.LookupDrift(context.Background(), 5)
	if err != nil {
		t.Fatalf("LookupDrift: %v", err)
	}
	if got == nil || got.ID != 101 || got.Verdict != "aligned" {
		t.Errorf("want id=101/aligned (newest), got %+v", got)
	}
}

// TestPeer_LookupSessionArtifacts verifies session-id scoping.
func TestPeer_LookupSessionArtifacts(t *testing.T) {
	unsetPeerEnv(t)
	path := makePeerDB(t,
		[]struct {
			ID              int64
			SessionID       string
			VibeCase        string
			SpecID          int64
			ArtifactURL     string
			ArtifactType    string
			Jurisdiction    string
			HasDisclosure   bool
			ValidationStatus string
			CreatedAt       string
		}{
			{ID: 1, SessionID: "sess-a", VibeCase: "C2", ArtifactType: "text", CreatedAt: "2026-07-17T00:00:00Z"},
			{ID: 2, SessionID: "sess-a", VibeCase: "C2", ArtifactType: "text", CreatedAt: "2026-07-17T00:00:01Z"},
			{ID: 3, SessionID: "sess-b", VibeCase: "C2", ArtifactType: "text", CreatedAt: "2026-07-17T00:00:02Z"},
		},
		nil,
	)
	t.Setenv(EnvPeerDSN, path)
	p, _ := NewPeerFromEnv()
	t.Cleanup(func() { _ = p.Close() })

	got, err := p.LookupSessionArtifacts(context.Background(), "sess-a", 10)
	if err != nil {
		t.Fatalf("LookupSessionArtifacts: %v", err)
	}
	if len(got) != 2 || got[0] != 2 || got[1] != 1 {
		t.Errorf("want [2,1] (newest first), got %v", got)
	}
}

// TestPeer_LookupDisabled ensures every lookup short-circuits to
// ErrPeerDisabled when no peer is configured.
func TestPeer_LookupDisabled(t *testing.T) {
	var nilPeer *Peer
	if _, err := nilPeer.LookupArtifact(context.Background(), 1); !errorsIs(err, ErrPeerDisabled) {
		t.Errorf("LookupArtifact on nil: want ErrPeerDisabled, got %v", err)
	}
	if _, err := nilPeer.LookupDrift(context.Background(), 1); !errorsIs(err, ErrPeerDisabled) {
		t.Errorf("LookupDrift on nil: want ErrPeerDisabled, got %v", err)
	}
	if _, err := nilPeer.LookupSessionArtifacts(context.Background(), "x", 10); !errorsIs(err, ErrPeerDisabled) {
		t.Errorf("LookupSessionArtifacts on nil: want ErrPeerDisabled, got %v", err)
	}
}

// errorsIs is a tiny shim to keep this test file's imports tidy.
func errorsIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		if u, ok := err.(unwrapper); ok {
			err = u.Unwrap()
		} else {
			return false
		}
	}
	return false
}