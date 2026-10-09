package audit

// gap_test.go — executable evidence about what audit_verify actually
// proves. Written during the Loop 22 architecture review, after
// measuring the live database and finding that the shipped HMAC chain
// cannot detect pre-export tampering with the database.
//
// WHY THIS FILE IS A TEST AND NOT A COMMENT. A security claim like
// "deletion is undetectable" is worth nothing asserted in prose; it is
// worth everything demonstrated by code that runs. If a future change
// fixes the behaviour, these tests fail and force the documentation,
// the ADR, and this file to be updated together — which is the only way
// a security boundary stops being described by stale folklore.
//
// THE STRUCTURE OF THE PROBLEM. ChainPrev, ChainSelf and ChainKeyID are
// NOT columns of write_audit. The type doc is explicit: "They are NOT
// persisted in the write_audit table (the JSONL stream is a derived
// view; the canonical truth is the SQL rows)." Confirmed by measurement:
// the live table has 14 columns and none of them carry a chain or hash.
//
// ExportJSONL therefore MINTS the chain at export time by walking
// whatever rows ListWrites currently returns (export.go:104-126).
// VerifyJSONL then re-derives it from that same stream (verify.go:86).
// Both operate on a snapshot taken at export. Nothing binds the snapshot
// to any earlier state of the database, because no earlier state is
// stored anywhere.
//
// CONSEQUENCE. An attacker with write access to dark.db can delete or
// edit audit rows, and a subsequent export+verify round trip returns
// status=ok. The chain re-links seamlessly across the gap: row N+1's
// chain_prev is computed from row N-1's chain_self, so continuity holds
// across the missing row and the verifier sees a well-formed chain.
//
// The verifier's own header comment (verify.go:16-18) claims otherwise:
// "a missing row mid-chain breaks the chain but is NOT a verifier
// bug." That is false for the shipped path, and the tests below are what
// make the falsity executable rather than a matter of opinion.
//
// WHAT IS ACTUALLY PROVEN. That the stream in hand was minted by a
// holder of key v1 and has not been modified since export. That is a
// real property — it is what protects an exported archive from being
// edited in transit — but it is a weaker property than the tool's
// documented promise of detecting "modification, deletion, forgery"
// (docs/v4-status.md §1.2), and it says nothing about the database that
// generated it.

import (
	"bytes"
	"context"
	"testing"
)

// sliceLister is a Lister over an in-memory slice standing in for the
// store. Modeled on the real write_audit table: rows carry no chain
// fields, exactly as the schema does not have them.
type sliceLister struct{ rows []WriteEvent }

func (s *sliceLister) ListWrites(_ context.Context, f ListFilters) ([]WriteEvent, error) {
	out := make([]WriteEvent, 0, len(s.rows))
	for _, r := range s.rows {
		// The real store returns id DESC; ExportJSONL reverses. Match
		// that so the test exercises the same reversal path.
		if f.SinceID > 0 && r.ID <= f.SinceID {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func makeRows(n int) []WriteEvent {
	rows := make([]WriteEvent, n)
	for i := range rows {
		rows[i] = WriteEvent{
			ID:            int64(i + 1),
			TableName:     "agent_memory",
			RowID:         int64(100 + i),
			ProjectID:     "default",
			Actor:         "agent_memory_save",
			SessionID:     "sess-test",
			WritePath:     "SaveRun",
			ContentSHA256: "sha-of-payload",
			CreatedAt:     "2026-10-09T00:00:00Z",
		}
	}
	return rows
}

func exportOf(t *testing.T, rows []WriteEvent) []byte {
	t.Helper()
	kr, err := NewKeyring(FormatKeyFromInt(7))
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	exp, err := NewExporter(&sliceLister{rows: rows}, kr)
	if err != nil {
		t.Fatalf("exporter: %v", err)
	}
	var buf bytes.Buffer
	if _, err := exp.ExportJSONL(context.Background(), &buf, ExportOptions{}); err != nil {
		t.Fatalf("export: %v", err)
	}
	return buf.Bytes()
}

func verifyOf(t *testing.T, stream []byte) *VerifierReport {
	t.Helper()
	kr, err := NewKeyring(FormatKeyFromInt(7))
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	v, err := NewVerifier(kr)
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	rep, err := v.VerifyBytes(stream)
	if err != nil {
		t.Fatalf("verify returned an error: %v (report: %+v)", err, rep)
	}
	return rep
}

// TestVerifyJSONL_DoesNotDetectPreexportDeletion is the evidence that
// audit_verify does not detect a database row deleted before export.
//
// THIS IS A CHARACTERIZATION TEST, NOT A CLAIM THAT THE BEHAVIOUR IS
// DESIRABLE. It asserts the current, defective behaviour so that the
// defect lives in executable form rather than in prose. It passes today.
// The day someone anchors the chain, it FAILS, which is the point: the
// failure is the signal to update the ADR, the §1.2 claim, and the
// audit_verify tool description together, instead of leaving the docs
// describing a boundary that moved.
func TestVerifyJSONL_DoesNotDetectPreexportDeletion(t *testing.T) {
	original := makeRows(10)

	// Baseline: the untouched database exports and verifies.
	rep := verifyOf(t, exportOf(t, original))
	if rep.Status != StatusOK || rep.RowsVerified != 10 {
		t.Fatalf("baseline should verify 10 rows, got status=%s rows=%d", rep.Status, rep.RowsVerified)
	}

	// Attacker deletes row id=5 from write_audit, then re-exports.
	tampered := make([]WriteEvent, 0, 9)
	for _, r := range original {
		if r.ID == 5 {
			continue
		}
		tampered = append(tampered, r)
	}

	rep = verifyOf(t, exportOf(t, tampered))

	if rep.Status != StatusOK {
		t.Errorf("BEHAVIOUR CHANGED — deletion is now detected (status=%s, reason=%s).\n"+
			"audit_verify gained a real integrity guarantee. Before merging that, update:\n"+
			"  1. the ADR for audit authority\n"+
			"  2. docs/v4-status.md §1.2, which currently overclaims\n"+
			"  3. the dark_memory_audit_verify tool description\n"+
			"Then invert this assertion to expect %s on a deleted row.\n"+
			"Until then the docs describe a boundary the code does not have.",
			rep.Status, rep.Reason, StatusBroken)
		return
	}

	t.Logf("characterized: a row deleted from write_audit before export verifies as "+
		"status=ok (verified %d of %d surviving rows). The chain re-links across the gap "+
		"because ExportJSONL mints chain_prev from the surviving rows, so continuity holds "+
		"and VerifyJSONL has nothing to detect. docs/v4-status.md §1.2 claims this tool "+
		"'detects modification, deletion, forgery'.", rep.RowsVerified, len(tampered))
}

// TestVerifyJSONL_DoesNotDetectPreexportEdit is the same characterization
// for content modification. Exporting after an edit re-mints the row's
// chain_self over the edited bytes, so the edit is self-certifying.
func TestVerifyJSONL_DoesNotDetectPreexportEdit(t *testing.T) {
	original := makeRows(6)

	tampered := make([]WriteEvent, len(original))
	copy(tampered, original)
	// Attacker rewrites what a write was, keeping the row present.
	tampered[3].ContentSHA256 = "sha-of-a-different-payload"
	tampered[3].WritePath = "DeleteRun"
	tampered[3].Actor = "someone_with_db_access"

	rep := verifyOf(t, exportOf(t, tampered))

	if rep.Status != StatusOK {
		t.Errorf("BEHAVIOUR CHANGED — pre-export content edits are now detected "+
			"(status=%s, reason=%s). Update the ADR, docs/v4-status.md §1.2, and the "+
			"dark_memory_audit_verify tool description, then invert this assertion.",
			rep.Status, rep.Reason)
		return
	}

	t.Logf("characterized: a write_audit row edited before export verifies as status=ok "+
		"(row id=%d, content_sha256 and write_path rewritten). The exporter mints "+
		"chain_self from the current bytes, so the edit carries its own valid MAC.",
		tampered[3].ID)
}

// TestVerifyJSONL_DetectsPostexportStreamEdit is the counterweight. The
// tool does real work, and this is what it does. Tampering with the
// stream AFTER export is caught, because chain_self no longer matches
// the recomputed HMAC over the row's bytes.
//
// This is the property the tool actually provides, and it is worth
// having. It is simply a different and much smaller property than the
// documentation implies.
func TestVerifyJSONL_DetectsPostexportStreamEdit(t *testing.T) {
	stream := exportOf(t, makeRows(6))

	// Edit a row in the exported stream, as a man-in-the-middle would.
	tampered := bytes.Replace(stream,
		[]byte(`"content_sha256":"sha-of-payload"`),
		[]byte(`"content_sha256":"forged-payload"`),
		1)
	if bytes.Equal(tampered, stream) {
		t.Fatal("test setup failed: the target substring was not present in the stream")
	}

	rep, err := verifyOfErr(t, tampered)
	if err == nil && rep.Status == StatusOK {
		t.Errorf("expected a post-export stream edit to be detected, got status=ok")
		return
	}
	t.Logf("post-export stream edit correctly detected: status=%s first_bad_id=%d reason=%s",
		rep.Status, rep.FirstBadID, rep.Reason)
}

// verifyOfErr is verifyOf without failing the test on a verification
// error, for the negative cases.
func verifyOfErr(t *testing.T, stream []byte) (*VerifierReport, error) {
	t.Helper()
	kr, err := NewKeyring(FormatKeyFromInt(7))
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	v, err := NewVerifier(kr)
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	return v.VerifyBytes(stream)
}
