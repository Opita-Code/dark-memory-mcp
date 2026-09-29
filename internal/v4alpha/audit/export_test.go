package audit

import "database/sql"

// Test-only exports (Go's export_test.go pattern).
//
// The Writer package does not expose its internal *sql.DB because
// production callers never need direct DB access — they use Write /
// WriteExec / Verify. But L2 chain tests DO need to do destructive
// operations (UPDATE row_hash to fake values, DELETE rows, etc.)
// for the deliberate-break calibration (dark-testing §6 Q7).
//
// The export_test.go convention lets the _test package see
// otherwise-private fields and methods. This file is built ONLY
// when compiling tests (regular `go build` ignores it; `go test`
// includes it in the test binary).
//
// Usage in tests:
//
//	db := w.DBG()  // returns the Writer's underlying *sql.DB

// DBG returns the Writer's underlying *sql.DB. Test-only — for
// production code, use Write / WriteExec / Verify.
func (w *Writer) DBG() *sql.DB {
	return w.db
}