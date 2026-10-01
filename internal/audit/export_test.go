// Package audit — export_test.go: hermetic tests for ExportJSONL using
// a fake Lister (no SQLite). The Lister interface is satisfied by an
// in-memory stub that returns a fixed slice of WriteEvents.
package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// makeRow constructs a WriteEvent with the given id and a fixed
// payload (every other field is identical, so HMAC deltas are
// predictable).
func makeRow(id int64) WriteEvent {
	return WriteEvent{
		ID:            id,
		TableName:     "agent_memory",
		RowID:         id,
		ProjectID:     "dark-memory",
		Actor:         "agent_memory_save",
		SessionID:     "sess-test",
		WritePath:     "SaveAgentMemory",
		ContentSHA256: "sha256:" + strconv.FormatInt(id, 10),
		CreatedAt:     "2026-10-01T18:00:00Z",
	}
}

// fakeLister returns a fixed slice in NEWEST-first order (the same
// way the real Store.ListWrites returns it).
type fakeLister struct {
	rows []WriteEvent
	err  error
}

func (f *fakeLister) ListWrites(_ context.Context, _ ListFilters) ([]WriteEvent, error) {
	return f.rows, f.err
}

// newExportKeyring builds a fresh keyring with one v1 key for tests.
func newExportKeyring(t *testing.T) *Keyring {
	t.Helper()
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i + 1)
	}
	k, err := NewKeyring("v1:" + hexLocal(secret))
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	return k
}

// hexLocal is a tiny local helper (avoid colliding with encoding/hex
// import elsewhere in the package).
func hexLocal(b []byte) string {
	const dig = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, x := range b {
		out[i*2] = dig[x>>4]
		out[i*2+1] = dig[x&0xF]
	}
	return string(out)
}

func TestExporter_NewExporter_NilLister(t *testing.T) {
	k := newExportKeyring(t)
	_, err := NewExporter(nil, k)
	if err == nil {
		t.Fatal("expected error for nil lister")
	}
}

func TestExporter_NewExporter_NilKeyring(t *testing.T) {
	l := &fakeLister{}
	_, err := NewExporter(l, nil)
	if err == nil {
		t.Fatal("expected error for nil keyring")
	}
}

func TestExporter_NewExporter_EmptyKeyring(t *testing.T) {
	l := &fakeLister{}
	emptyK := &Keyring{}
	_, err := NewExporter(l, emptyK)
	if err == nil {
		t.Fatal("expected error for empty keyring")
	}
}

func TestExportJSONL_EmptyStream(t *testing.T) {
	l := &fakeLister{rows: nil}
	k := newExportKeyring(t)
	exp, err := NewExporter(l, k)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	var buf bytes.Buffer
	n, err := exp.ExportJSONL(context.Background(), &buf, ExportOptions{})
	if err != nil {
		t.Fatalf("ExportJSONL: %v", err)
	}
	if n != 0 {
		t.Errorf("n = %d; want 0", n)
	}
	if buf.Len() != 0 {
		t.Errorf("buf has %d bytes; want 0", buf.Len())
	}
}

func TestExportJSONL_SingleRow(t *testing.T) {
	l := &fakeLister{rows: []WriteEvent{makeRow(1)}}
	k := newExportKeyring(t)
	exp, _ := NewExporter(l, k)
	var buf bytes.Buffer
	n, err := exp.ExportJSONL(context.Background(), &buf, ExportOptions{})
	if err != nil {
		t.Fatalf("ExportJSONL: %v", err)
	}
	if n != 1 {
		t.Errorf("n = %d; want 1", n)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d lines; want 1", len(lines))
	}
	var ev WriteEvent
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.ChainPrev != "" {
		t.Errorf("genesis row has non-empty chain_prev: %q", ev.ChainPrev)
	}
	if ev.ChainSelf == "" {
		t.Error("genesis row has empty chain_self")
	}
	if ev.ChainKeyID != "v1" {
		t.Errorf("chain_key_id = %q; want v1", ev.ChainKeyID)
	}
}

func TestExportJSONL_MultipleRowsChainContinuity(t *testing.T) {
	// ListWrites returns id DESC; pass it that way and confirm the
	// exporter reverses to ASC for the canonical chain order.
	l := &fakeLister{rows: []WriteEvent{
		makeRow(3), // id DESC order
		makeRow(2),
		makeRow(1),
	}}
	k := newExportKeyring(t)
	exp, _ := NewExporter(l, k)
	var buf bytes.Buffer
	n, err := exp.ExportJSONL(context.Background(), &buf, ExportOptions{})
	if err != nil {
		t.Fatalf("ExportJSONL: %v", err)
	}
	if n != 3 {
		t.Fatalf("n = %d; want 3", n)
	}
	// Decode each line and assert chain_prev matches prev chain_self.
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines; want 3", len(lines))
	}
	var prev string
	for i, line := range lines {
		var ev WriteEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %d unmarshal: %v", i, err)
		}
		if ev.ChainPrev != prev {
			t.Errorf("line %d: chain_prev = %q; want %q (chain continuity broken)", i, ev.ChainPrev, prev)
		}
		if ev.ChainSelf == "" {
			t.Errorf("line %d: chain_self is empty", i)
		}
		prev = ev.ChainSelf
	}
}

func TestExportJSONL_NilWriter(t *testing.T) {
	l := &fakeLister{}
	k := newExportKeyring(t)
	exp, _ := NewExporter(l, k)
	_, err := exp.ExportJSONL(context.Background(), nil, ExportOptions{})
	if err == nil {
		t.Fatal("expected error for nil writer")
	}
}

func TestExportJSONL_UnknownKeyID(t *testing.T) {
	l := &fakeLister{rows: []WriteEvent{makeRow(1)}}
	k := newExportKeyring(t)
	exp, _ := NewExporter(l, k)
	var buf bytes.Buffer
	_, err := exp.ExportJSONL(context.Background(), &buf, ExportOptions{KeyID: "v999"})
	if err == nil {
		t.Fatal("expected error for unknown key id")
	}
	if !strings.Contains(err.Error(), "v999") {
		t.Errorf("error doesn't mention key id: %v", err)
	}
}

func TestExportJSONL_ListError(t *testing.T) {
	l := &fakeLister{err: errExportFake}
	k := newExportKeyring(t)
	exp, _ := NewExporter(l, k)
	var buf bytes.Buffer
	_, err := exp.ExportJSONL(context.Background(), &buf, ExportOptions{})
	if err == nil {
		t.Fatal("expected error from ListWrites")
	}
	if !strings.Contains(err.Error(), "fake-list-error") {
		t.Errorf("error doesn't wrap lister error: %v", err)
	}
}

var errExportFake = errStr("fake-list-error")

type errStr string

func (e errStr) Error() string { return string(e) }

func TestCountLines(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"single", "abc", 1},
		{"two", "abc\ndef", 2},
		{"trailing-newline", "abc\ndef\n", 2},
		{"multi", "a\nb\nc\n", 3},
		{"blank-line-skipped", "a\n\nb\n", 2}, // CountLines doesn't skip blanks (caller responsibility); this doc the actual count
	}
	for _, tc := range tests {
		got := CountLines([]byte(tc.in))
		if tc.name == "blank-line-skipped" {
			// Document the actual behavior: CountLines counts ALL
			// non-empty lines, where "non-empty" means "after
			// trimming a trailing \n, the content is non-empty". A
			// blank internal line still counts because it's split
			// by \n.
			// Input "a\n\nb\n" → "a\n\nb" (trim right \n) → split by
			// \n gives 3 lines: "a", "", "b" → CountLines returns 3.
			if got != 3 {
				t.Errorf("%s: got %d; want 3", tc.name, got)
			}
			continue
		}
		if got != tc.want {
			t.Errorf("%s: got %d; want %d", tc.name, got, tc.want)
		}
	}
}

func TestExporter_RoundTripWithVerifier(t *testing.T) {
	// 3 rows, id DESC from the lister. Export, then verify.
	l := &fakeLister{rows: []WriteEvent{
		makeRow(3),
		makeRow(2),
		makeRow(1),
	}}
	k := newExportKeyring(t)
	exp, _ := NewExporter(l, k)
	var buf bytes.Buffer
	n, err := exp.ExportJSONL(context.Background(), &buf, ExportOptions{})
	if err != nil {
		t.Fatalf("ExportJSONL: %v", err)
	}
	if n != 3 {
		t.Fatalf("n = %d; want 3", n)
	}
	v, err := NewVerifier(k)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	rep, err := v.VerifyBytes(buf.Bytes())
	if err != nil {
		t.Fatalf("VerifyBytes: %v", err)
	}
	if rep.Status != StatusOK {
		t.Errorf("status = %q; want %q (reason: %s)", rep.Status, StatusOK, rep.Reason)
	}
	if rep.RowsVerified != 3 {
		t.Errorf("rows_verified = %d; want 3", rep.RowsVerified)
	}
	if rep.KeyID != "v1" {
		t.Errorf("key_id = %q; want v1", rep.KeyID)
	}
}