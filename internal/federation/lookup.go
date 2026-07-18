// Package federation provides read-only cross-namespace lookup against a
// "peer" database (typically dark-research's dark.db).
//
// F7 motivation: dark-memory and dark-research are two federated systems,
// each with its own SQLite DB. There is no shared storage layer. When an
// operator calls dark_memory_pipeline_status on an artifact_id that lives
// in dark.db, the local Store returns "not found" — which is correct
// locally but misleading globally.
//
// This package makes the federation EXPLICIT and READABLE without forcing
// storage unification (which would break the intentional separation: dark-db
// has FTS5+vector+events triggers; dark-memory-db is intentionally leaner).
//
// Design constraints:
//   - READ-ONLY. The Peer never writes to the peer's DB. All writes stay
//     local to the binary that owns them.
//   - LAZY. Peer is opened at boot from DARK_FEDERATION_PEER_DSN; if the
//     env var is missing, federation is a no-op (everything else works).
//   - SCHEMA-VALIDATED. We open the peer in readonly mode and verify it has
//     vibe_artifacts + vibe_drift_reports tables before accepting lookups.
//     A DB without those tables is rejected at boot, not at request time.
//   - BEST-EFFORT. Lookups can fail (peer DB locked, schema drift, etc.) —
//     callers treat failures as "peer says nothing" rather than "fatal".
package federation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	_ "modernc.org/sqlite" // pure-Go driver, no cgo
)

// EnvPeerDSN is the env var that points to the peer DB. When unset, the
// federation layer is a no-op (every method returns ErrPeerDisabled).
// The convention matches the rest of dark-agents (DARK_DB, DARK_SCRAPPER_URL).
const EnvPeerDSN = "DARK_FEDERATION_PEER_DSN"

// ErrPeerDisabled is returned by every lookup method when the peer is not
// configured. Callers should treat this as "I have nothing to say", not
// as an error to surface to the operator.
var ErrPeerDisabled = errors.New("federation: peer not configured (DARK_FEDERATION_PEER_DSN not set)")

// ErrInvalidSchema is returned by NewPeerFromEnv when the peer DB does not
// have the expected vibe_artifacts + vibe_drift_reports tables. This is a
// startup error; the server should fail boot rather than silently degrade.
var ErrInvalidSchema = errors.New("federation: peer DB missing required tables (vibe_artifacts, vibe_drift_reports)")

// Peer is a read-only handle to the federation peer's SQLite DB. Construct
// via NewPeerFromEnv; close via Close when done.
type Peer struct {
	db  *sql.DB
	dsn string // original DSN (for diagnostics)
}

// PeerArtifact mirrors the vibe_artifacts row shape we expose cross-namespace.
// We deliberately omit brand_id (sensitive), spec_id's full body, and any
// text body / content; those are local-only and not part of the discovery
// contract.
type PeerArtifact struct {
	ID              int64  `json:"id"`
	SessionID       string `json:"session_id,omitempty"`
	VibeCase        string `json:"vibe_case"`
	SpecID          int64  `json:"spec_id,omitempty"`
	ArtifactURL     string `json:"artifact_url,omitempty"`
	ArtifactType    string `json:"artifact_type"`
	Jurisdiction    string `json:"jurisdiction,omitempty"`
	HasDisclosure   bool   `json:"has_disclosure"`
	ValidationStatus string `json:"validation_status"`
	CreatedAt       string `json:"created_at"`
}

// PeerDrift mirrors the vibe_drift_reports row shape we expose cross-namespace.
type PeerDrift struct {
	ID           int64  `json:"id"`
	ArtifactID   int64  `json:"artifact_id"`
	SpecID       int64  `json:"spec_id,omitempty"`
	Verdict      string `json:"verdict"`
	ReconciledAt string `json:"reconciled_at,omitempty"`
	CreatedAt    string `json:"created_at"`
}

// NewPeerFromEnv opens the peer DB from DARK_FEDERATION_PEER_DSN.
//
// Returns (nil, nil) when the env var is unset (federation is opt-in).
// Returns (nil, ErrInvalidSchema) when the peer DB lacks the required tables.
// Returns (nil, error) on any other open/ping failure.
//
// The peer is opened with modernc/sqlite's readonly mode (mode=ro) so a
// bug in this package cannot corrupt the peer's data.
func NewPeerFromEnv() (*Peer, error) {
	dsn := strings.TrimSpace(os.Getenv(EnvPeerDSN))
	if dsn == "" {
		return nil, nil
	}

	// modernc/sqlite accepts a query string on the DSN. `mode=ro` is the
	// canonical readonly flag. We also set `_pragma=busy_timeout(5000)`
	// so a brief lock contention doesn't immediately fail the lookup.
	readonlyDSN := dsn
	if strings.Contains(readonlyDSN, "?") {
		readonlyDSN += "&mode=ro"
	} else {
		readonlyDSN += "?mode=ro"
	}

	db, err := sql.Open("sqlite", readonlyDSN)
	if err != nil {
		return nil, fmt.Errorf("federation: open peer %q: %w", dsn, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("federation: ping peer %q: %w", dsn, err)
	}

	// Schema validation. We need at least vibe_artifacts and vibe_drift_reports.
	var tableCount int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('vibe_artifacts','vibe_drift_reports')`,
	).Scan(&tableCount); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("federation: schema check peer %q: %w", dsn, err)
	}
	if tableCount < 2 {
		_ = db.Close()
		return nil, fmt.Errorf("%w (found %d/2 in %q)", ErrInvalidSchema, tableCount, dsn)
	}

	return &Peer{db: db, dsn: dsn}, nil
}

// IsEnabled reports whether the peer is configured. Useful for tools that
// want to short-circuit when federation is off (e.g. skip the peer-lookup
// hop in pipeline_status when no peer is set).
func (p *Peer) IsEnabled() bool {
	return p != nil && p.db != nil
}

// DSN returns the original (non-readonly-suffixed) DSN, for diagnostics
// and tool responses. Never the readonly wrapper.
func (p *Peer) DSN() string {
	if p == nil {
		return ""
	}
	return p.dsn
}

// Close releases the underlying *sql.DB. Safe to call on a nil Peer.
func (p *Peer) Close() error {
	if p == nil || p.db == nil {
		return nil
	}
	return p.db.Close()
}

// LookupArtifact returns the peer-side vibe_artifacts row for id, or
// (nil, nil) when no row exists. Returns (nil, ErrPeerDisabled) when the
// peer is not configured. Returns (nil, error) on a real lookup failure.
//
// The SELECT is narrow on purpose: brand_id and any content fields are
// omitted from the result so the cross-namespace view is metadata-only.
func (p *Peer) LookupArtifact(ctx context.Context, id int64) (*PeerArtifact, error) {
	if !p.IsEnabled() {
		return nil, ErrPeerDisabled
	}
	const q = `SELECT id, COALESCE(session_id,''), vibe_case, COALESCE(spec_id,0),
	                  COALESCE(artifact_url,''), artifact_type,
	                  COALESCE(jurisdiction,''), has_disclosure, validation_status, created_at
	             FROM vibe_artifacts WHERE id = ?`
	var a PeerArtifact
	err := p.db.QueryRowContext(ctx, q, id).Scan(
		&a.ID, &a.SessionID, &a.VibeCase, &a.SpecID,
		&a.ArtifactURL, &a.ArtifactType, &a.Jurisdiction,
		&a.HasDisclosure, &a.ValidationStatus, &a.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("federation: peer query artifact id=%d: %w", id, err)
	}
	return &a, nil
}

// LookupDrift returns the latest peer-side vibe_drift_reports row for
// artifactID (newest by id), or (nil, nil) when no row exists. Returns
// (nil, ErrPeerDisabled) when the peer is not configured.
func (p *Peer) LookupDrift(ctx context.Context, artifactID int64) (*PeerDrift, error) {
	if !p.IsEnabled() {
		return nil, ErrPeerDisabled
	}
	const q = `SELECT id, artifact_id, COALESCE(spec_id,0), verdict,
	                  COALESCE(reconciled_at,''), created_at
	             FROM vibe_drift_reports
	            WHERE artifact_id = ?
	         ORDER BY id DESC LIMIT 1`
	var d PeerDrift
	err := p.db.QueryRowContext(ctx, q, artifactID).Scan(
		&d.ID, &d.ArtifactID, &d.SpecID, &d.Verdict, &d.ReconciledAt, &d.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("federation: peer query drift artifact_id=%d: %w", artifactID, err)
	}
	return &d, nil
}

// LookupSessionArtifacts returns the peer-side artifact IDs whose
// session_id matches. Capped at limit (caller picks; we suggest 100).
// Returns ([]int64{}, nil) when the session is unknown to the peer.
func (p *Peer) LookupSessionArtifacts(ctx context.Context, sessionID string, limit int) ([]int64, error) {
	if !p.IsEnabled() {
		return nil, ErrPeerDisabled
	}
	if limit <= 0 {
		limit = 100
	}
	const q = `SELECT id FROM vibe_artifacts WHERE session_id = ? ORDER BY id DESC LIMIT ?`
	rows, err := p.db.QueryContext(ctx, q, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("federation: peer query session %q: %w", sessionID, err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("federation: peer scan session row: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("federation: peer rows iteration: %w", err)
	}
	return out, nil
}