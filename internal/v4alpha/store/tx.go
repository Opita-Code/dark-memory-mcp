package store

import (
	"context"
	"database/sql"
	"fmt"
)

// WithTx runs fn inside a SERIALIZABLE transaction and commits iff fn
// returns nil. On panic or fn error, the transaction is rolled back and
// the panic is re-raised unchanged.
//
// Why SERIALIZABLE? SQLite's default DEFERRED mode means a tx starts
// with no lock and only acquires one at the first read or write. That
// is fine for single-statement work but lets concurrent writers race
// in the read-modify-write gap (the BUG-5 Revoke race in cap_store.go
// was exactly this). SERIALIZABLE forces BEGIN to take the write lock
// immediately, eliminating the race window.
//
// Why centralise? Three reasons:
//   - One place that owns the rollback-on-panic contract; callers
//     cannot forget it.
//   - The isolation level is a project-wide invariant for dark-db;
//     scattering sql.LevelSerializable across packages invites drift.
//   - Tests can use the same helper as production, which keeps the
//     audit trail honest.
func WithTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) (err error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("store: begin tx: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			// Best-effort rollback; commit would fail because the
			// tx is in a panic state, but we still want to undo.
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
			return
		}
		if cerr := tx.Commit(); cerr != nil {
			err = fmt.Errorf("store: commit: %w", cerr)
		}
	}()
	return fn(tx)
}
