package mcp

import "database/sql"

// Test-only exports (Go's export_test.go pattern).
//
// Production callers should use the tool API (tools/call). But
// some L2 chain tests need direct DB access for the deliberate-
// break calibration (dark-testing §6 Q7) — e.g., corrupting a
// row_hash before calling dark_memory_audit_verify.
//
// Usage in tests:
//
//	db := srv.DBG()  // returns the Server's underlying *sql.DB

// DBG returns the Server's underlying *sql.DB. Test-only — for
// production code, use the tools/* handlers.
func (s *Server) DBG() *sql.DB {
	return s.db
}