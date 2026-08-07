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
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// makePeerDB builds a temp SQLite DB with the minimum schema we need
// and pre-populates a few rows. Returns the path so the test can
// point the federation Peer at it via DARK_FEDERATION_PEER_DSN.
func makePeerDB(t *testing.T, rows []struct {
	ID               int64
	SessionID        string
	VibeCase         string
	SpecID           int64
	ArtifactURL      string
	ArtifactType     string
	Jurisdiction     string
	HasDisclosure    bool
	ValidationStatus string
	CreatedAt        string
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
			ID               int64
			SessionID        string
			VibeCase         string
			SpecID           int64
			ArtifactURL      string
			ArtifactType     string
			Jurisdiction     string
			HasDisclosure    bool
			ValidationStatus string
			CreatedAt        string
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
			ID               int64
			SessionID        string
			VibeCase         string
			SpecID           int64
			ArtifactURL      string
			ArtifactType     string
			Jurisdiction     string
			HasDisclosure    bool
			ValidationStatus string
			CreatedAt        string
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
			ID               int64
			SessionID        string
			VibeCase         string
			SpecID           int64
			ArtifactURL      string
			ArtifactType     string
			Jurisdiction     string
			HasDisclosure    bool
			ValidationStatus string
			CreatedAt        string
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
			ID               int64
			SessionID        string
			VibeCase         string
			SpecID           int64
			ArtifactURL      string
			ArtifactType     string
			Jurisdiction     string
			HasDisclosure    bool
			ValidationStatus string
			CreatedAt        string
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

// TestNewPeerFromEnv_DSNWithQueryString pins the readonly-DSN suffix
// logic: a DSN already containing "?" gets "&mode=ro"; a plain path
// gets "?mode=ro". Kills the readonlyDSN construction mutants.
func TestNewPeerFromEnv_DSNWithQueryString(t *testing.T) {
	unsetPeerEnv(t)
	path := makePeerDB(t, nil, nil)
	t.Setenv(EnvPeerDSN, path+"?_pragma=busy_timeout(5000)")
	p, err := NewPeerFromEnv()
	if err != nil {
		t.Fatalf("NewPeerFromEnv with query-string DSN: %v", err)
	}
	if p == nil || !p.IsEnabled() {
		t.Fatal("expected enabled peer for query-string DSN")
	}
	t.Cleanup(func() { _ = p.Close() })
}

// TestPeer_NilReceivers pins nil-safety of Close and DSN.
func TestPeer_NilReceivers(t *testing.T) {
	var nilPeer *Peer
	if err := nilPeer.Close(); err != nil {
		t.Errorf("nil.Close() = %v, want nil", err)
	}
	if got := nilPeer.DSN(); got != "" {
		t.Errorf("nil.DSN() = %q, want empty", got)
	}
}

// TestPeer_LookupArtifact_RealError pins the wrapped-error path:
// a query failure (e.g. dropped table) returns a wrapped error, NOT
// a silent (nil, nil).
func TestPeer_LookupArtifact_RealError(t *testing.T) {
	unsetPeerEnv(t)
	path := filepath.Join(t.TempDir(), "peer.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// No vibe_artifacts table → query fails.
	if _, err := db.Exec("CREATE TABLE unrelated (id INTEGER);"); err != nil {
		t.Fatalf("create: %v", err)
	}
	db.Close()
	t.Setenv(EnvPeerDSN, path)
	// NewPeerFromEnv would fail schema check; construct a peer directly
	// with a db pointing at the wrong-schema file.
	peerDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open peer: %v", err)
	}
	t.Cleanup(func() { _ = peerDB.Close() })
	p := &Peer{db: peerDB, dsn: path}
	if _, err := p.LookupArtifact(context.Background(), 1); err == nil {
		t.Error("expected error from bad-schema lookup, got nil")
	}
}

// TestPeer_LookupDrift_NoRows pins the (nil, nil) contract when the
// peer has no drift rows for the artifact.
func TestPeer_LookupDrift_NoRows(t *testing.T) {
	unsetPeerEnv(t)
	path := makePeerDB(t, nil, nil)
	t.Setenv(EnvPeerDSN, path)
	p, err := NewPeerFromEnv()
	if err != nil {
		t.Fatalf("NewPeerFromEnv: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	got, err := p.LookupDrift(context.Background(), 999)
	if err != nil {
		t.Fatalf("LookupDrift no-rows: %v", err)
	}
	if got != nil {
		t.Errorf("LookupDrift no-rows: got %+v, want nil", got)
	}
}

// TestPeer_LookupSessionArtifacts_Empty pins the empty-slice contract.
func TestPeer_LookupSessionArtifacts_Empty(t *testing.T) {
	unsetPeerEnv(t)
	path := makePeerDB(t, nil, nil)
	t.Setenv(EnvPeerDSN, path)
	p, err := NewPeerFromEnv()
	if err != nil {
		t.Fatalf("NewPeerFromEnv: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	got, err := p.LookupSessionArtifacts(context.Background(), "unknown-session", 10)
	if err != nil {
		t.Fatalf("LookupSessionArtifacts empty: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("LookupSessionArtifacts empty: got %d ids, want 0", len(got))
	}
}

// TestNewPeerFromEnv_PingFailure pins the db.Ping error path: a DSN
// pointing at a directory (not a SQLite file) makes sql.Open succeed
// but Ping fail with "unable to open database file". Kills the
// ping-error return-removal mutant.
func TestNewPeerFromEnv_PingFailure(t *testing.T) {
	unsetPeerEnv(t)
	dir := t.TempDir() // directory, not a file
	t.Setenv(EnvPeerDSN, dir)
	_, err := NewPeerFromEnv()
	if err == nil {
		t.Fatal("expected error for directory-as-DSN, got nil")
	}
	if !strings.Contains(err.Error(), "federation: ping peer") {
		t.Errorf("error should wrap ping failure, got: %v", err)
	}
}

// TestNewPeerFromEnv_ReadonlyMissingFile pins the mode=ro open path:
// a nonexistent file with mode=ro fails Ping (modernc/sqlite refuses
// to create in readonly mode). Kills the open/ping error mutants.
func TestNewPeerFromEnv_ReadonlyMissingFile(t *testing.T) {
	unsetPeerEnv(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist.db")
	t.Setenv(EnvPeerDSN, missing)
	_, err := NewPeerFromEnv()
	if err == nil {
		t.Fatal("expected error for missing readonly file, got nil")
	}
}

// TestPeer_LookupSessionArtifacts_DefaultLimit pins the limit<=0 →
// 100 default. Kills the `limit = 100` assignment-removal mutant.
func TestPeer_LookupSessionArtifacts_DefaultLimit(t *testing.T) {
	unsetPeerEnv(t)
	// Insert 3 artifacts for a session; a limit=0 call must return all 3
	// (default 100), proving the default assignment ran.
	path := makePeerDB(t,
		[]struct {
			ID               int64
			SessionID        string
			VibeCase         string
			SpecID           int64
			ArtifactURL      string
			ArtifactType     string
			Jurisdiction     string
			HasDisclosure    bool
			ValidationStatus string
			CreatedAt        string
		}{
			{ID: 1, SessionID: "sess-x", VibeCase: "C2", ArtifactType: "text", CreatedAt: "2026-07-17T00:00:00Z"},
			{ID: 2, SessionID: "sess-x", VibeCase: "C2", ArtifactType: "text", CreatedAt: "2026-07-17T00:00:01Z"},
			{ID: 3, SessionID: "sess-x", VibeCase: "C2", ArtifactType: "text", CreatedAt: "2026-07-17T00:00:02Z"},
		}, nil,
	)
	t.Setenv(EnvPeerDSN, path)
	p, err := NewPeerFromEnv()
	if err != nil {
		t.Fatalf("NewPeerFromEnv: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	got, err := p.LookupSessionArtifacts(context.Background(), "sess-x", 0)
	if err != nil {
		t.Fatalf("LookupSessionArtifacts limit=0: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("limit=0: got %d ids, want 3 (default 100)", len(got))
	}
}

// TestPeer_LookupSessionArtifacts_QueryError pins the query-error
// wrapped path: a peer DB without the table returns an error, not a
// silent empty list.
func TestPeer_LookupSessionArtifacts_QueryError(t *testing.T) {
	unsetPeerEnv(t)
	path := filepath.Join(t.TempDir(), "peer.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec("CREATE TABLE unrelated (id INTEGER);"); err != nil {
		t.Fatalf("create: %v", err)
	}
	db.Close()
	peerDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open peer: %v", err)
	}
	t.Cleanup(func() { _ = peerDB.Close() })
	p := &Peer{db: peerDB, dsn: path}
	if _, err := p.LookupSessionArtifacts(context.Background(), "sess-x", 10); err == nil {
		t.Error("expected query error for bad-schema peer, got nil")
	}
}

// --- Mutation-survivor killers (wave 2) ---

// TestBuildReadonlyDSN pins the readonly-DSN construction. The original
// code appended ?mode=ro to a bare path, which modernc/sqlite treats as
// part of the filename — the file gets CREATED on Ping, so the "readonly"
// was a no-op. A correct readonly DSN must be a file: URI with mode=ro
// plus the busy_timeout pragma the doc comment promises.
func TestBuildReadonlyDSN(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "bare path",
			in:   `C:\data\peer.db`,
			want: "file:" + `C:\data\peer.db` + "?_pragma=busy_timeout(5000)&mode=ro",
		},
		{
			name: "path with existing query",
			in:   "C:/data/peer.db?cache=shared",
			want: "file:C:/data/peer.db?cache=shared&_pragma=busy_timeout(5000)&mode=ro",
		},
		{
			name: "already file URI untouched",
			in:   "file:/data/peer.db?_pragma=busy_timeout(5000)&mode=ro",
			want: "file:/data/peer.db?_pragma=busy_timeout(5000)&mode=ro",
		},
		{
			name: "in-memory untouched",
			in:   ":memory:",
			want: ":memory:",
		},
		{
			name: "file memory untouched",
			in:   "file::memory:",
			want: "file::memory:",
		},
		{
			name: "empty untouched",
			in:   "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildReadonlyDSN(tc.in); got != tc.want {
				t.Errorf("buildReadonlyDSN(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNewPeerFromEnv_ReadonlyDoesNotCreateFile pins the OBSERVABLE
// readonly contract: after a failed NewPeerFromEnv on a missing file,
// the file must NOT exist on disk. The pre-fix code (bare path +
// ?mode=ro) created the file on Ping even in "readonly" mode; the fix
// (file: URI) leaves no file behind. This kills the readonlyDSN
// assignment-removal mutants (lookup.go.4/.6/.35 in the wave-2 run).
func TestNewPeerFromEnv_ReadonlyDoesNotCreateFile(t *testing.T) {
	unsetPeerEnv(t)
	dir := t.TempDir()
	missing := filepath.Join(dir, "must-not-exist.db")
	t.Setenv(EnvPeerDSN, missing)
	_, err := NewPeerFromEnv()
	if err == nil {
		t.Fatal("expected error for missing readonly file, got nil")
	}
	if _, statErr := os.Stat(missing); statErr == nil {
		t.Error("readonly DSN must not create the peer file, but it exists on disk")
	}
}

// TestNewPeerFromEnv_OneTableOnly pins the schema-count boundary: a peer
// DB with exactly ONE of the two required tables must be rejected with
// ErrInvalidSchema. Kills the `tableCount < 2` → `< 1` mutants
// (lookup.go.30/.39): with the mutant, tableCount=1 would pass and the
// peer would be accepted with a half-schema DB.
func TestNewPeerFromEnv_OneTableOnly(t *testing.T) {
	unsetPeerEnv(t)
	path := filepath.Join(t.TempDir(), "one-table.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec("CREATE TABLE vibe_artifacts (id INTEGER PRIMARY KEY);"); err != nil {
		t.Fatalf("create: %v", err)
	}
	db.Close()
	t.Setenv(EnvPeerDSN, path)
	_, err = NewPeerFromEnv()
	if err == nil {
		t.Fatal("expected ErrInvalidSchema for one-table DB, got nil")
	}
	if !errorsIs(err, ErrInvalidSchema) {
		t.Errorf("expected ErrInvalidSchema, got: %v", err)
	}
}

// TestPeer_LookupDrift_RealError pins the wrapped-error path for
// LookupDrift: a query failure must return a real error, not a silent
// (&PeerDrift{}, nil). Kills the return-removal mutant at lookup.go.18
// whose fall-through returns a zero-value PeerDrift with nil error —
// the exact "error swallowed" bug this campaign hunts.
func TestPeer_LookupDrift_RealError(t *testing.T) {
	unsetPeerEnv(t)
	path := filepath.Join(t.TempDir(), "peer.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// No vibe_drift_reports table → query fails.
	if _, err := db.Exec("CREATE TABLE unrelated (id INTEGER);"); err != nil {
		t.Fatalf("create: %v", err)
	}
	db.Close()
	peerDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open peer: %v", err)
	}
	t.Cleanup(func() { _ = peerDB.Close() })
	p := &Peer{db: peerDB, dsn: path}
	got, err := p.LookupDrift(context.Background(), 1)
	if err == nil {
		t.Error("expected error from bad-schema LookupDrift, got nil")
	}
	if got != nil {
		t.Errorf("on error, result must be nil, got %+v", got)
	}
}

// TestPeer_NilDB_PinsNullDBGuard kills the p.db nil-guard mutants:
// a non-nil *Peer with a nil *sql.DB must report IsEnabled=false
// (lookup.go.27) and Close() must not panic (lookup.go.29).
func TestPeer_NilDB_PinsNullDBGuard(t *testing.T) {
	p := &Peer{} // non-nil receiver, nil db
	if p.IsEnabled() {
		t.Error("Peer with nil db must report IsEnabled=false")
	}
	if err := p.Close(); err != nil {
		t.Errorf("Peer{}.Close() = %v, want nil", err)
	}
}

// TestPeer_LookupSessionArtifacts_ExactLimit pins the limit<=0 boundary:
// limit=1 must return at most 1 row. Kills the `limit <= 0` → `<= 1`
// mutant (lookup.go.32) which would promote a caller's explicit limit=1
// to the 100 default and return every row.
func TestPeer_LookupSessionArtifacts_ExactLimit(t *testing.T) {
	unsetPeerEnv(t)
	path := makePeerDB(t,
		[]struct {
			ID               int64
			SessionID        string
			VibeCase         string
			SpecID           int64
			ArtifactURL      string
			ArtifactType     string
			Jurisdiction     string
			HasDisclosure    bool
			ValidationStatus string
			CreatedAt        string
		}{
			{ID: 1, SessionID: "sess-x", VibeCase: "C2", ArtifactType: "text", CreatedAt: "2026-07-17T00:00:00Z"},
			{ID: 2, SessionID: "sess-x", VibeCase: "C2", ArtifactType: "text", CreatedAt: "2026-07-17T00:00:01Z"},
			{ID: 3, SessionID: "sess-x", VibeCase: "C2", ArtifactType: "text", CreatedAt: "2026-07-17T00:00:02Z"},
		}, nil,
	)
	t.Setenv(EnvPeerDSN, path)
	p, err := NewPeerFromEnv()
	if err != nil {
		t.Fatalf("NewPeerFromEnv: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	got, err := p.LookupSessionArtifacts(context.Background(), "sess-x", 1)
	if err != nil {
		t.Fatalf("LookupSessionArtifacts limit=1: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("limit=1: got %d ids, want exactly 1", len(got))
	}
}

// TestPeer_LookupSessionArtifacts_LimitCapsAt100 pins the default limit
// value: with 150 rows and limit=0, the result must be capped at 100,
// not 101. Kills the `limit = 100` → `101` mutant (lookup.go.34).
func TestPeer_LookupSessionArtifacts_LimitCapsAt100(t *testing.T) {
	unsetPeerEnv(t)
	// Build the peer DB with 150 rows using a single recursive-CTE INSERT
	// (fast: one Exec, not 150 round-trips) so the mutation harness does
	// not pay 33s per mutant for this test.
	path := filepath.Join(t.TempDir(), "peer.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
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
		WITH RECURSIVE seq(n) AS (
			SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n < 150
		)
		INSERT INTO vibe_artifacts (id, session_id, vibe_case, artifact_type, created_at)
		SELECT n, 'sess-x', 'C2', 'text', '2026-07-17T00:00:00Z' FROM seq;
	`
	if _, err := db.Exec(ddl); err != nil {
		t.Fatalf("create+seed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seed db: %v", err)
	}
	t.Setenv(EnvPeerDSN, path)
	p, err := NewPeerFromEnv()
	if err != nil {
		t.Fatalf("NewPeerFromEnv: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	got, err := p.LookupSessionArtifacts(context.Background(), "sess-x", 0)
	if err != nil {
		t.Fatalf("LookupSessionArtifacts limit=0: %v", err)
	}
	if len(got) != 100 {
		t.Errorf("limit=0 with 150 rows: got %d ids, want 100", len(got))
	}
}

// TestPeer_LookupSessionArtifacts_ScanError pins the rows.Scan error
// path: a non-integer id in the peer DB must produce a wrapped error,
// not a silent row with id=0. Kills the scan-error return-removal
// mutant (lookup.go.22) whose fall-through appends id=0 and returns
// a corrupted []int64 with nil error.
func TestPeer_LookupSessionArtifacts_ScanError(t *testing.T) {
	unsetPeerEnv(t)
	// In-memory DB: no file handle, no TempDir lock contention on Windows.
	db, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ddl := `
		CREATE TABLE vibe_artifacts (
			id TEXT PRIMARY KEY,
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
	`
	if _, err := db.Exec(ddl); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Insert a row whose id is a non-integer string → Scan(&id int64)
	// must fail. SQLite is dynamically typed so this is legal.
	if _, err := db.Exec(`INSERT INTO vibe_artifacts
		(id, session_id, vibe_case, artifact_type, created_at)
		VALUES ('not-an-int', 'sess-x', 'C2', 'text', '2026-07-17T00:00:00Z');`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	p := &Peer{db: db, dsn: ":memory:"}
	if _, err := p.LookupSessionArtifacts(context.Background(), "sess-x", 10); err == nil {
		t.Error("expected scan error for non-integer id, got nil")
	}
}
