// Package audit (v4alpha) — INV-1 enforcement primitive.
//
// INV-1 (operator id audit): every Save operation emits exactly one
// audit row tagged with the operator id, written via the audit.Writer.
// The audit_id is strictly monotonic — both per-process AND cross-
// process — backed by SQLite's AUTOINCREMENT (sqlite_sequence table,
// persistent across processes and crashes).
//
// # Atomicity contract (per internal/atomic convention)
//   - ONE constructor: NewWriter
//   - ONE write method: Write
//   - ONE schema function: CreateSchema
//   - THREE invariants enforced in Write:
//       1. audit_id > previous audit_id (monotonic, cross-process)
//       2. exactly one row inserted per call (atomicity)
//       3. operator (actor) is non-empty (INV-1 audit row is identifiable)
//
// # BUG-12 fix (2026-09-29, alpha.14)
//
// Pre-BUG-12, the Writer held an in-memory counter (w.seq) that it
// passed explicitly as audit_id in the INSERT. This was process-
// local monotonic but cross-process UNSAFE: two Writers writing to
// the same DB file could both compute seq=5 and the second INSERT
// would fail with PRIMARY KEY conflict.
//
// Post-BUG-12, the in-memory counter is gone. The INSERT omits
// audit_id and SQLite's AUTOINCREMENT assigns it (via the
// sqlite_sequence table). Read back via Result.LastInsertId().
// Cross-process monotonicity is guaranteed by SQLite itself.
//
// The mutex is kept for lastID + lastHash mirror updates (race-free
// reads of the in-process state).
//
// # Phase 2 fix (2026-09-29, alpha.15) — INV-12 hash chain
//
// Every Write now also writes prev_hash and row_hash on the audit
// row. row_hash is SHA-256 over a canonical encoding of the row
// (see canonical.go). prev_hash is row_hash of the row immediately
// preceding. The chain detects: modification (rewrite), deletion,
// forgery. It does NOT detect "rewrite the whole file from scratch"
// (out of scope — needs external trust anchor).
//
// Ed25519 signatures (ADR-017) are forward-compatible: a future
// `signature BLOB` column will sign row_hash. The canonical
// encoding in canonical.go is the single source of truth — Writer,
// WriteExec, and Verify all use ComputeRowHash so any drift breaks
// the chain (which is the point).
package audit

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// Writer emits INV-1 audit rows with a strictly monotonic audit_id
// (per-process AND cross-process, backed by SQLite AUTOINCREMENT).
//
// lastID is an in-memory mirror of the most recent id returned by
// LastInsertId. It is a strict under-approximation of MAX(audit_id)
// in the DB (other processes may have written more). It exists for
// fast LastID() reads (no DB roundtrip).
//
// lastHash (Phase 2, alpha.15) is the in-memory mirror of the most
// recent row_hash (32 bytes). It is the prev_hash for the NEXT
// Write from this Writer. Per-process mirror; bootstrapped from
// the most recent DB row on first Write. Cross-process correctness
// is enforced at verify time (§4.6 of SPEC-alpha-11-phase2).
type Writer struct {
	db       *sql.DB
	mu       sync.Mutex
	lastID   int64
	lastHash []byte // 32 bytes; nil until first Write or bootstrap
}

// NewWriter returns a Writer bound to the given DB. The DB must have
// had CreateSchema (or ApplyChainColumns on a pre-Phase-2 DB) called
// on it before any Write calls.
func NewWriter(db *sql.DB) *Writer {
	return &Writer{db: db}
}

// CreateSchema creates the audit_log table. Idempotent (IF NOT EXISTS).
// Called once at session-store initialization.
//
// Schema note: session_id is a nullable TEXT column. Empty string is
// stored as NULL to keep the COUNT/WHERE queries clean (sessions vs.
// non-session writes). AUTOINCREMENT (not just INTEGER PRIMARY KEY)
// is critical for cross-process monotonicity — without AUTOINCREMENT,
// SQLite may recycle ids after the max value, breaking audit log
// ordering. See https://www.sqlite.org/autoinc.html for the precise
// semantics.
//
// Phase 2 columns (alpha.15): prev_hash BLOB (32B) and row_hash
// BLOB (32B). Both nullable; legacy rows have NULL for both (the
// verify tool treats NULL as a "trust anchor" — chain picks up at
// the first non-NULL row).
func CreateSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS audit_log (
			audit_id   INTEGER PRIMARY KEY AUTOINCREMENT,
			actor      TEXT    NOT NULL CHECK (actor <> ''),
			session_id TEXT,
			payload    BLOB,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			prev_hash  BLOB,
			row_hash   BLOB
		)
	`)
	if err != nil {
		return fmt.Errorf("audit CreateSchema: %w", err)
	}
	return nil
}

// ApplyChainColumns adds the prev_hash and row_hash columns to a
// pre-Phase-2 audit_log table (the legacy schema from alpha.14
// and earlier). Idempotent: uses pragma_table_info to detect
// presence and skip if the column already exists.
//
// New DBs should use CreateSchema (which includes the columns
// directly). ApplyChainColumns is the migration path for existing
// DBs that already contain audit rows.
//
// Both ALTER TABLE ADD COLUMN calls are O(1) in SQLite — only the
// schema (sqlite_schema B-tree) is modified, not the data. No
// backfill is needed (legacy rows have NULL row_hash; verify
// treats NULL as a trust anchor).
func ApplyChainColumns(ctx context.Context, db *sql.DB) error {
	for _, col := range []string{"prev_hash", "row_hash"} {
		var n int
		if err := db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM pragma_table_info('audit_log') WHERE name = ?",
			col,
		).Scan(&n); err != nil {
			return fmt.Errorf("audit ApplyChainColumns (pragma %s): %w", col, err)
		}
		if n > 0 {
			continue // already present
		}
		if _, err := db.ExecContext(ctx,
			"ALTER TABLE audit_log ADD COLUMN "+col+" BLOB",
		); err != nil {
			return fmt.Errorf("audit ApplyChainColumns (add %s): %w", col, err)
		}
	}
	return nil
}

// Write inserts one audit row, computes its SHA-256 row_hash, and
// links it to the previous row via prev_hash (Phase 2, INV-12).
//
// Returns the new audit_id (strictly greater than any prior audit_id
// in the audit_log table — both per-process and cross-process,
// backed by SQLite AUTOINCREMENT).
//
// INV-1 enforcement: every Write emits exactly one row tagged with
// the actor (operator id).
//
// sessionID is optional. Empty string means "not associated with a
// session" (e.g., standalone writes). For session-bound writes
// (Start/Heartbeat/Close), pass the session.ID so downstream Summary
// queries can JOIN cleanly without LIKE substring matching.
//
// # Phase 2 chain flow (§4.2 of SPEC-alpha-11-phase2)
//
//  1. Generate created_at in Go (UTC RFC3339Nano). Same value is
//     stored in the row AND used in the hash, so Write and Verify
//     agree on the bytes.
//  2. Resolve prev_hash (lastHash mirror or bootstrap from DB).
//  3. INSERT row with prev_hash and created_at set, row_hash=NULL.
//  4. Compute row_hash = SHA256(prev_hash || audit_id || actor
//     || 0x00 || session_id || 0x00 || payload || 0x00 ||
//     created_at || 0x00) — see canonical.go.
//  5. UPDATE row_hash on the same row.
//  6. Update in-memory mirrors (lastID, lastHash).
//
// Performance: 2 queries per Write (INSERT + UPDATE), vs 3 for the
// "SELECT created_at from DB" pattern. The Go-side created_at
// generation is microsecond-cheap and eliminates the per-Write
// SELECT (the documented 1-2ms regression in §11.3 of the spec).
//
// Determinism: created_at is generated in Go (time.Now().UTC()).
// The hash depends on this value being the same in Write (when
// it's stored) and Verify (when it's read back). Since Verify
// reads whatever the row stored, the bytes are identical.
//
// If the UPDATE in step 5 fails (rare; disk-full mid-write), the
// row exists with NULL row_hash. Verify treats NULL as a trust
// anchor (the row IS in the chain as a "no hash" entry); the next
// Write produces a normal chain. Operator can investigate via
// SELECT * FROM audit_log WHERE row_hash IS NULL.
func (w *Writer) Write(ctx context.Context, actor, sessionID string, payload []byte) (int64, error) {
	if actor == "" {
		return 0, fmt.Errorf("audit Write: actor must be non-empty (INV-1)")
	}
	var sessionIDArg interface{}
	if sessionID == "" {
		sessionIDArg = nil
	} else {
		sessionIDArg = sessionID
	}
	// No projectID — Write is the legacy 4-arg form. The audit_log
	// row gets project_id='default' via the column DEFAULT. Callers
	// that need a specific project (e.g., project.Store.Create) use
	// WriteWithProject. The canonical hash is the SAME — project_id
	// is metadata, not part of the chain (Phase 2 §3.2 invariant).

	// 1. Generate created_at in Go. Same value goes into both the
	// INSERT and the row_hash input.
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)

	w.mu.Lock()
	defer w.mu.Unlock()

	// 2. Resolve prev_hash (hot path: mirror; cold: bootstrap from DB).
	prevHash, err := w.resolvePrevHashLocked(ctx)
	if err != nil {
		return 0, fmt.Errorf("audit Write resolvePrevHash: %w", err)
	}

	// 3. INSERT with prev_hash and created_at (row_hash populated
	// in step 5). project_id='default' via column DEFAULT — legacy
	// callers don't need to know about the namespace primitive.
	res, err := w.db.ExecContext(ctx,
		"INSERT INTO audit_log (actor, session_id, payload, prev_hash, created_at) VALUES (?, ?, ?, ?, ?)",
		actor, sessionIDArg, payload, prevHash, createdAt,
	)
	if err != nil {
		return 0, fmt.Errorf("audit Write insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		// Rare: INSERT succeeded but LastInsertId is not supported
		// by the driver (shouldn't happen with modernc.org/sqlite).
		// The row IS in the DB; we just can't tell the caller what
		// id it got. Surface the error so the caller knows.
		return 0, fmt.Errorf("audit Write LastInsertId: %w", err)
	}

	// 4. Compute row_hash (pure function — canonical.go). Uses the
	// SAME createdAt we just stored.
	rowHash := ComputeRowHash(prevHash, id, actor, sessionID, payload, createdAt)

	// 5. UPDATE row_hash on the new row.
	if _, err := w.db.ExecContext(ctx,
		"UPDATE audit_log SET row_hash = ? WHERE audit_id = ?",
		rowHash[:], id,
	); err != nil {
		return id, fmt.Errorf("audit Write update row_hash: %w", err)
	}

	// 6. Update mirrors.
	w.lastID = id
	w.lastHash = rowHash[:]
	return id, nil
}

// WriteWithProject is the project-tagged variant of Write (Phase 4
// Chunk 4.1). Same canonical hash (project_id is metadata, NOT
// part of the chain — Phase 2 §3.2 invariant); same prev_hash
// resolution; same row_hash computation. The ONLY difference is
// the INSERT includes the project_id column.
//
// Why a separate method (not a Write 5th arg):
//   - 16 existing callers would have to be updated to pass ""
//     for projectID. That's a noise commit that adds no semantic
//     change.
//   - Old callers' audit rows get project_id='default' via the
//     column DEFAULT clause. New callers (e.g., project.Store.
//     Create) use WriteWithProject to stamp a specific project.
//   - The hash chain is backward-compatible: any pre-Phase-4
//     audit row continues to verify (project_id was never part
//     of the hash).
//
// projectID is required (non-empty) — INV-1 requires every audit
// row to identify its scope. Use "default" for system writes.
func (w *Writer) WriteWithProject(ctx context.Context, actor, sessionID, projectID string, payload []byte) (int64, error) {
	if actor == "" {
		return 0, fmt.Errorf("audit WriteWithProject: actor must be non-empty (INV-1)")
	}
	if projectID == "" {
		return 0, fmt.Errorf("audit WriteWithProject: projectID must be non-empty (INV-1)")
	}
	var sessionIDArg interface{}
	if sessionID == "" {
		sessionIDArg = nil
	} else {
		sessionIDArg = sessionID
	}

	// 1. Generate created_at in Go. Same value goes into both the
	// INSERT and the row_hash input.
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)

	w.mu.Lock()
	defer w.mu.Unlock()

	// 2. Resolve prev_hash (hot path: mirror; cold: bootstrap from DB).
	prevHash, err := w.resolvePrevHashLocked(ctx)
	if err != nil {
		return 0, fmt.Errorf("audit Write resolvePrevHash: %w", err)
	}

	// 3. INSERT with prev_hash, project_id, and created_at (row_hash
	// populated in step 5). project_id is the namespace primitive
	// (Phase 4); it is queryable metadata but NOT part of the
	// canonical hash (Phase 2 §3.2 invariant).
	res, err := w.db.ExecContext(ctx,
		"INSERT INTO audit_log (actor, session_id, project_id, payload, prev_hash, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		actor, sessionIDArg, projectID, payload, prevHash, createdAt,
	)
	if err != nil {
		return 0, fmt.Errorf("audit WriteWithProject insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		// Rare: INSERT succeeded but LastInsertId is not supported.
		// Surface the error so the caller knows the row is in the
		// DB but we couldn't tell them the id.
		return 0, fmt.Errorf("audit WriteWithProject LastInsertId: %w", err)
	}

	// 4. Compute row_hash (pure function — canonical.go). Same
	// canonical hash as Write (project_id is metadata).
	rowHash := ComputeRowHash(prevHash, id, actor, sessionID, payload, createdAt)

	// 5. UPDATE row_hash on the new row.
	if _, err := w.db.ExecContext(ctx,
		"UPDATE audit_log SET row_hash = ? WHERE audit_id = ?",
		rowHash[:], id,
	); err != nil {
		return id, fmt.Errorf("audit WriteWithProject update row_hash: %w", err)
	}

	// 6. Update mirrors.
	w.lastID = id
	w.lastHash = rowHash[:]
	return id, nil
}

// resolvePrevHashLocked returns the prev_hash for the next Write.
// Hot path: returns lastHash mirror (32 bytes).
// Cold start: SELECT row_hash FROM audit_log ORDER BY audit_id DESC LIMIT 1.
// Empty: returns zeroHash (32 zero bytes).
// Legacy (all rows have NULL row_hash): returns zeroHash (chain
// picks up at first post-Phase-2 Write).
//
// MUST be called with w.mu held.
func (w *Writer) resolvePrevHashLocked(ctx context.Context) ([]byte, error) {
	if w.lastHash != nil {
		return w.lastHash, nil
	}
	var prev []byte
	err := w.db.QueryRowContext(ctx,
		"SELECT row_hash FROM audit_log ORDER BY audit_id DESC LIMIT 1",
	).Scan(&prev)
	if err == sql.ErrNoRows {
		return ZeroHash(), nil
	}
	if err != nil {
		return nil, err
	}
	if prev == nil {
		return ZeroHash(), nil
	}
	return prev, nil
}

// LastID returns the most recent audit_id emitted by THIS Writer
// process, or 0 if no Write has been performed yet. Diagnostic
// helper used by tests and operators to inspect this process's
// audit position.
//
// IMPORTANT: this is a per-process mirror. If other processes have
// written to the same audit_log DB, their ids are NOT reflected
// here. For the global truth, query MAX(audit_id) directly.
//
// session.Summary.AuditIDAtClose uses this value as a diagnostic
// snapshot; it is not the canonical cross-process truth.
func (w *Writer) LastID() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastID
}

// LastHash returns the row_hash of the most recent row emitted by
// THIS Writer process, or nil if no Write has been performed yet.
// 32 bytes. Used by tests and operators; not part of the public
// API contract. Same per-process caveat as LastID — for the global
// truth, query the DB directly.
func (w *Writer) LastHash() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Return a copy so callers cannot mutate the canonical mirror.
	out := make([]byte, len(w.lastHash))
	copy(out, w.lastHash)
	return out
}
