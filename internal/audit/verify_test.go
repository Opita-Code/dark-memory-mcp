// Package audit — verify_test.go: focused tests for VerifyJSONL,
// including the negative paths (broken chain, tampered field,
// unknown key, malformed row).
package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// exportAndSign is a test helper: emits a chain via Exporter and
// returns the JSONL bytes. Used to feed VerifyJSONL for the negative
// tests (mutate the bytes, then verify the mutation is caught).
func exportAndSign(t *testing.T, rows []WriteEvent) []byte {
	t.Helper()
	// Make sure rows are id DESC (the canonical lister order).
	for i := 0; i < len(rows)-1; i++ {
		if rows[i].ID < rows[i+1].ID {
			t.Fatalf("exportAndSign expects id DESC; got %d before %d at i=%d", rows[i].ID, rows[i+1].ID, i)
		}
	}
	l := &fakeLister{rows: rows}
	k := newExportKeyring(t)
	exp, err := NewExporter(l, k)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	var buf bytes.Buffer
	if _, err := exp.ExportJSONL(context.Background(), &buf, ExportOptions{}); err != nil {
		t.Fatalf("ExportJSONL: %v", err)
	}
	return buf.Bytes()
}

func TestVerifier_NewVerifier_NilKeyring(t *testing.T) {
	_, err := NewVerifier(nil)
	if err == nil {
		t.Fatal("expected error for nil keyring")
	}
}

func TestVerifier_NewVerifier_EmptyKeyring(t *testing.T) {
	_, err := NewVerifier(&Keyring{})
	if err == nil {
		t.Fatal("expected error for empty keyring")
	}
}

func TestVerifyBytes_NilReader(t *testing.T) {
	k := newExportKeyring(t)
	v, _ := NewVerifier(k)
	_, err := v.VerifyBytes(nil)
	if err == nil {
		t.Fatal("expected error for nil bytes")
	}
}

func TestVerifyBytes_EmptyStream(t *testing.T) {
	k := newExportKeyring(t)
	v, _ := NewVerifier(k)
	_, err := v.VerifyBytes([]byte{})
	if err == ErrEmptyStream {
		// expected
		return
	}
	// Empty input might pass through without hitting ErrEmptyStream
	// depending on the scanner's handling of EOF without newline.
	// Whatever happens, the report status should not be OK.
	rep, _ := v.VerifyBytes([]byte{})
	if rep != nil && rep.Status == StatusOK {
		t.Errorf("empty stream produced OK status")
	}
}

func TestVerifyBytes_OK(t *testing.T) {
	bytes := exportAndSign(t, []WriteEvent{makeRow(3), makeRow(2), makeRow(1)})
	k := newExportKeyring(t)
	v, _ := NewVerifier(k)
	rep, err := v.VerifyBytes(bytes)
	if err != nil {
		t.Fatalf("VerifyBytes: %v", err)
	}
	if rep.Status != StatusOK {
		t.Errorf("status = %q (reason: %s); want ok", rep.Status, rep.Reason)
	}
	if rep.RowsVerified != 3 {
		t.Errorf("rows_verified = %d; want 3", rep.RowsVerified)
	}
}

func TestVerifyBytes_TamperedField_Caught(t *testing.T) {
	// Export, then mutate the actor of the MIDDLE row, then verify.
	in := exportAndSign(t, []WriteEvent{makeRow(3), makeRow(2), makeRow(1)})
	lines := strings.Split(strings.TrimRight(string(in), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines; want 3", len(lines))
	}
	var mid WriteEvent
	if err := json.Unmarshal([]byte(lines[1]), &mid); err != nil {
		t.Fatalf("unmarshal middle row: %v", err)
	}
	mid.Actor = "attacker"
	tampered, _ := json.Marshal(mid)
	lines[1] = string(tampered)
	mutated := []byte(strings.Join(lines, "\n") + "\n")

	k := newExportKeyring(t)
	v, _ := NewVerifier(k)
	rep, err := v.VerifyBytes(mutated)
	if err == nil {
		t.Fatal("VerifyBytes returned nil error on tampered chain")
	}
	if rep.Status != StatusBroken {
		t.Errorf("status = %q; want broken (reason: %s)", rep.Status, rep.Reason)
	}
	if rep.FirstBadID != 2 {
		t.Errorf("first_bad_id = %d; want 2", rep.FirstBadID)
	}
	if !strings.Contains(rep.Reason, "HMAC mismatch") {
		t.Errorf("reason = %q; want contains 'HMAC mismatch'", rep.Reason)
	}
}

func TestVerifyBytes_GapInChain_Caught(t *testing.T) {
	// Export 3 rows, then DROP the middle row, then verify.
	in := exportAndSign(t, []WriteEvent{makeRow(3), makeRow(2), makeRow(1)})
	lines := strings.Split(strings.TrimRight(string(in), "\n"), "\n")
	// Drop lines[1] (id=2) → row id=3 now has chain_prev pointing to
	// row id=1's chain_self, which doesn't match row id=3's chain_prev.
	without := []string{lines[0], lines[2]}
	mutated := []byte(strings.Join(without, "\n") + "\n")

	k := newExportKeyring(t)
	v, _ := NewVerifier(k)
	rep, err := v.VerifyBytes(mutated)
	if err == nil {
		t.Fatal("VerifyBytes returned nil error on gap in chain")
	}
	if rep.Status != StatusBroken {
		t.Errorf("status = %q; want broken (reason: %s)", rep.Status, rep.Reason)
	}
	if rep.FirstBadID != 3 {
		t.Errorf("first_bad_id = %d; want 3 (the row that follows the gap)", rep.FirstBadID)
	}
	if !strings.Contains(rep.Reason, "chain_prev mismatch") {
		t.Errorf("reason = %q; want contains 'chain_prev mismatch'", rep.Reason)
	}
}

func TestVerifyBytes_UnknownKey_Caught(t *testing.T) {
	// Export with keyring A, then try to verify with a keyring that
	// doesn't know the key id.
	in := exportAndSign(t, []WriteEvent{makeRow(2), makeRow(1)})

	// Build a keyring with NO keys (operator rotated the key but lost
	// the old one — common recovery scenario).
	rotated, err := NewKeyring("v2:" + hexLocal(make([]byte, 32)))
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	v, _ := NewVerifier(rotated)
	rep, err := v.VerifyBytes(in)
	if err == nil {
		t.Fatal("VerifyBytes returned nil error with missing key")
	}
	if rep.Status != StatusUnknownKey {
		t.Errorf("status = %q; want unknown_key (reason: %s)", rep.Status, rep.Reason)
	}
}

func TestVerifyBytes_MalformedJSON_Caught(t *testing.T) {
	// Hand-craft a single bad-JSON stream.
	in := []byte(`{"id":1,"broken-json` + "\n")
	k := newExportKeyring(t)
	v, _ := NewVerifier(k)
	rep, err := v.VerifyBytes(in)
	if err == nil {
		t.Fatal("VerifyBytes returned nil error on malformed JSON")
	}
	if rep.Status != StatusMalformed {
		t.Errorf("status = %q; want malformed (reason: %s)", rep.Status, rep.Reason)
	}
}

func TestVerifyBytes_MissingChainFields_Caught(t *testing.T) {
	// Hand-craft a row with no chain fields.
	in := []byte(`{"id":1,"table_name":"agent_memory","row_id":1}` + "\n")
	k := newExportKeyring(t)
	v, _ := NewVerifier(k)
	rep, err := v.VerifyBytes(in)
	if err == nil {
		t.Fatal("VerifyBytes returned nil error on missing chain fields")
	}
	if rep.Status != StatusMalformed {
		t.Errorf("status = %q; want malformed (reason: %s)", rep.Status, rep.Reason)
	}
}

func TestVerifyBytes_KeyRotation_ForwardCompat(t *testing.T) {
	// Export 2 rows with key v1. Build a keyring that knows BOTH v1
	// (for verifying the existing rows) AND v2 (for future rows).
	// The exporter stamps chain_key_id="v1" so verify should pass.
	in := exportAndSign(t, []WriteEvent{makeRow(2), makeRow(1)})

	// Build a keyring with both keys.
	secret1 := make([]byte, 32)
	for i := range secret1 {
		secret1[i] = byte(i + 1)
	}
	secret2 := make([]byte, 32)
	for i := range secret2 {
		secret2[i] = byte((i + 100) % 256)
	}
	both, err := NewKeyring(
		"v1:"+hexLocal(secret1),
		"v2:"+hexLocal(secret2),
	)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	v, _ := NewVerifier(both)
	rep, err := v.VerifyBytes(in)
	if err != nil {
		t.Fatalf("VerifyBytes: %v", err)
	}
	if rep.Status != StatusOK {
		t.Errorf("status = %q (reason: %s); want ok", rep.Status, rep.Reason)
	}
	if rep.KeyID != "v1" {
		t.Errorf("key_id = %q; want v1 (the key id the exporter used)", rep.KeyID)
	}
}

func TestVerifyBytes_RowIDOutOfOrder_Succeeds(t *testing.T) {
	// VerifyJSONL doesn't care about row order as long as chain_prev
	// is correct. Feed rows out-of-order from the underlying storage
	// (id ASC this time) and verify.
	rows := []WriteEvent{makeRow(1), makeRow(2), makeRow(3)}
	l := &fakeLister{rows: rows}
	k := newExportKeyring(t)
	exp, _ := NewExporter(l, k)
	var buf bytes.Buffer
	if _, err := exp.ExportJSONL(context.Background(), &buf, ExportOptions{}); err != nil {
		t.Fatalf("ExportJSONL: %v", err)
	}
	v, _ := NewVerifier(k)
	rep, err := v.VerifyBytes(buf.Bytes())
	if err != nil {
		t.Fatalf("VerifyBytes: %v", err)
	}
	if rep.Status != StatusOK {
		t.Errorf("status = %q; want ok", rep.Status)
	}
	if rep.RowsVerified != 3 {
		t.Errorf("rows_verified = %d; want 3", rep.RowsVerified)
	}
}

func TestVerifyJSONL_NilReader(t *testing.T) {
	k := newExportKeyring(t)
	v, _ := NewVerifier(k)
	_, err := v.VerifyJSONL(nil)
	if err == nil {
		t.Fatal("expected error for nil reader")
	}
}

func TestVerifyBytes_SkipsBlankLines(t *testing.T) {
	// Insert a blank line between two valid rows. Verifier should
	// skip it and verify both rows.
	in := exportAndSign(t, []WriteEvent{makeRow(2), makeRow(1)})
	// Insert a blank line after the first newline.
	idx := bytes.IndexByte(in, '\n')
	if idx < 0 {
		t.Fatal("no newline in export")
	}
	withBlank := append(in[:idx+1], append([]byte("\n"), in[idx+1:]...)...)
	k := newExportKeyring(t)
	v, _ := NewVerifier(k)
	rep, err := v.VerifyBytes(withBlank)
	if err != nil {
		t.Fatalf("VerifyBytes: %v", err)
	}
	if rep.Status != StatusOK {
		t.Errorf("status = %q; want ok (reason: %s)", rep.Status, rep.Reason)
	}
	if rep.RowsVerified != 2 {
		t.Errorf("rows_verified = %d; want 2", rep.RowsVerified)
	}
}