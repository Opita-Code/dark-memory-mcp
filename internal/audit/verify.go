// Package audit — verify.go: re-derive the HMAC chain from a JSONL
// stream and detect tampering (ADR-018 audit verify tool).
//
// Failure modes the verifier detects:
//
//   - Row missing chain_prev / chain_self / chain_key_id fields →
//     MalformedError.
//   - chain_key_id not present in the verifier's keyring →
//     UnknownKeyError (operator must update the keyring).
//   - chain_prev of row N+1 != chain_self of row N → ChainBrokenError
//     (row N or earlier was tampered with, OR an export + import
//     mismatched the keyring, OR the row was deleted).
//   - chain_self of any row doesn't match the recomputed HMAC over
//     (prev_chain_self + canonical_bytes) → HMACMismatchError.
//
// The verifier is best-effort w.r.t. RowID gaps in the underlying DB:
// a missing row mid-chain breaks the chain but is NOT a verifier bug.
// It only reports the chain state from the JSONL stream it was given.
package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Verification status: one of OK / Broken / UnknownKey / Malformed.
const (
	StatusOK         = "ok"
	StatusBroken     = "broken"     // chain_prev mismatch or HMAC mismatch
	StatusUnknownKey = "unknown_key" // chain_key_id not in keyring
	StatusMalformed  = "malformed"  // row missing required fields
)

// VerifierReport summarizes a VerifyJSONL run.
type VerifierReport struct {
	Status       string `json:"status"`        // OK | Broken | UnknownKey | Malformed
	RowsVerified int    `json:"rows_verified"` // count of rows successfully verified
	FirstBadID   int64  `json:"first_bad_id,omitempty"`
	Reason       string `json:"reason,omitempty"`
	KeyID        string `json:"key_id,omitempty"` // the key id used (when status=ok)
}

// ErrChainBroken is returned by VerifyJSONL when chain_prev of row N+1
// doesn't equal chain_self of row N. Also returned when the HMAC self
// doesn't match the recomputed one.
var (
	ErrChainBroken     = errors.New("audit: chain broken")
	ErrUnknownKey      = errors.New("audit: chain_key_id not in keyring")
	ErrMalformed       = errors.New("audit: malformed row")
	ErrEmptyStream     = errors.New("audit: empty stream")
	ErrZeroRowsTrusted = errors.New("audit: zero rows is not a valid chain")
)

// Verifier reads a JSONL stream and re-derives the HMAC chain.
type Verifier struct {
	keyring *Keyring
}

// NewVerifier constructs a Verifier. The keyring must contain at least
// one key.
func NewVerifier(keyring *Keyring) (*Verifier, error) {
	if keyring == nil {
		return nil, errors.New("audit: NewVerifier: nil keyring")
	}
	if _, _, ok := keyring.Primary(); !ok {
		return nil, errors.New("audit: NewVerifier: empty keyring")
	}
	return &Verifier{keyring: keyring}, nil
}

// VerifyJSONL reads the entire stream from r, re-derives the HMAC
// chain, and returns a VerifierReport.
//
// The first row's chain_prev must be "" (genesis). Any subsequent row
// must have chain_prev == previous row's chain_self.
//
// On the FIRST failure (malformed / unknown key / chain broken / HMAC
// mismatch), verification STOPS and the report is filled with the
// failing row's id + reason. Rows before the failure are counted in
// RowsVerified.
func (v *Verifier) VerifyJSONL(r io.Reader) (*VerifierReport, error) {
	if r == nil {
		return nil, errors.New("audit: VerifyJSONL: nil reader")
	}
	scanner := bufio.NewScanner(r)
	// Allow long lines (large JSONs are common).
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	rep := &VerifierReport{}
	var prevChainSelf string
	rowNum := 0
	for scanner.Scan() {
		rowNum++
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue // skip blank lines
		}
		var ev WriteEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			rep.Status = StatusMalformed
			rep.FirstBadID = ev.ID // 0 when unmarshal failed before id was parsed
			rep.Reason = fmt.Sprintf("row %d: unmarshal: %s", rowNum, redactJSONError(err))
			return rep, fmt.Errorf("%w: row %d unmarshal: %s", ErrMalformed, rowNum, redactJSONError(err))
		}
		// Required fields present?
		if ev.ChainSelf == "" || ev.ChainKeyID == "" {
			rep.Status = StatusMalformed
			rep.FirstBadID = ev.ID
			rep.Reason = fmt.Sprintf("row id=%d: missing chain_self or chain_key_id", ev.ID)
			return rep, fmt.Errorf("%w: row id=%d missing chain_self or chain_key_id", ErrMalformed, ev.ID)
		}
		// Lookup the secret for this row's key id.
		secret, ok := v.keyring.Get(ev.ChainKeyID)
		if !ok {
			rep.Status = StatusUnknownKey
			rep.FirstBadID = ev.ID
			rep.Reason = fmt.Sprintf("row id=%d: chain_key_id=%q not in keyring", ev.ID, ev.ChainKeyID)
			return rep, fmt.Errorf("%w: row id=%d chain_key_id=%q", ErrUnknownKey, ev.ID, ev.ChainKeyID)
		}
		// chain_prev continuity (genesis = "" for row 1).
		if ev.ChainPrev != prevChainSelf {
			rep.Status = StatusBroken
			rep.FirstBadID = ev.ID
			rep.Reason = fmt.Sprintf("row id=%d: chain_prev mismatch (expected %q, got %q)", ev.ID, prevChainSelf, ev.ChainPrev)
			return rep, fmt.Errorf("%w: row id=%d chain_prev mismatch", ErrChainBroken, ev.ID)
		}
		// HMAC self verification.
		ok, err := VerifyChain(secret, prevChainSelf, ev)
		if err != nil {
			rep.Status = StatusBroken
			rep.FirstBadID = ev.ID
			rep.Reason = fmt.Sprintf("row id=%d: VerifyChain marshal: %s", ev.ID, err)
			return rep, fmt.Errorf("%w: row id=%d VerifyChain: %s", ErrChainBroken, ev.ID, err)
		}
		if !ok {
			rep.Status = StatusBroken
			rep.FirstBadID = ev.ID
			rep.Reason = fmt.Sprintf("row id=%d: HMAC mismatch (key %q)", ev.ID, ev.ChainKeyID)
			return rep, fmt.Errorf("%w: row id=%d HMAC mismatch", ErrChainBroken, ev.ID)
		}
		rep.RowsVerified++
		rep.KeyID = ev.ChainKeyID
		prevChainSelf = ev.ChainSelf
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("audit: VerifyJSONL: scan: %w", err)
	}
	if rowNum == 0 {
		return nil, ErrEmptyStream
	}
	if rep.RowsVerified == 0 {
		return nil, ErrZeroRowsTrusted
	}
	rep.Status = StatusOK
	return rep, nil
}

// VerifyBytes is a convenience wrapper around VerifyJSONL for a byte
// slice (the most common case: an exported JSONL file).
func (v *Verifier) VerifyBytes(b []byte) (*VerifierReport, error) {
	return v.VerifyJSONL(bytes.NewReader(b))
}

// redactJSONError strips hex/byte values from json.Unmarshal errors so
// they log cleanly without leaking internal field values.
func redactJSONError(err error) string {
	s := err.Error()
	// "json: cannot unmarshal X into Go struct field Y of type Z" — X
	// may contain user data. Replace anything inside backticks.
	for {
		start := strings.IndexByte(s, '`')
		if start < 0 {
			break
		}
		end := strings.IndexByte(s[start+1:], '`')
		if end < 0 {
			break
		}
		s = s[:start+1] + "***" + s[start+1+end+1:]
	}
	return s
}