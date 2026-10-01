// Package audit — export.go: stream write_audit rows as JSONL with
// per-row HMAC chain integrity (ADR-016 transparency log).
//
// Output format (one JSON object per line, "\n"-terminated):
//
//	{"id":1,"table_name":"agent_memory","row_id":42,...,"chain_prev":"","chain_self":"abc...","chain_key_id":"v1"}
//	{"id":2,"table_name":"agent_memory","row_id":43,...,"chain_prev":"abc...","chain_self":"def...","chain_key_id":"v1"}
//	...
//
// The stream is monotonically ordered by `id ASC` (the canonical write
// order from ListWrites). Operators consume it with `audit_verify` to
// re-derive the chain and detect tampering.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ExportOptions controls one ExportJSONL invocation. The zero value
// emits ALL rows for ALL projects with no filter and uses the keyring's
// primary key id.
type ExportOptions struct {
	// ProjectID filters by project_id; empty = all projects.
	ProjectID string
	// SinceID filters id > SinceID; 0 = all rows.
	SinceID int64
	// Actor filters by actor; empty = all.
	Actor string
	// WritePath filters by write_path; empty = all.
	WritePath string
	// SessionID filters by session_id; empty = all.
	SessionID string
	// Limit caps the row count; 0 = no limit.
	Limit int
	// KeyID selects which keyring key to stamp on chain_key_id; empty =
	// the keyring's primary (first iterated).
	KeyID string
}

// Exporter wraps a Lister (typically store.Store.ListWrites) + a
// Keyring and emits a HMAC-chained JSONL stream.
type Exporter struct {
	lister  Lister
	keyring *Keyring
}

// Lister is the minimal subset of store.Store the exporter depends on.
// Defining the interface locally keeps the audit package decoupled from
// the store package and lets tests inject fakes.
type Lister interface {
	ListWrites(ctx context.Context, f ListFilters) ([]WriteEvent, error)
}

// NewExporter constructs an Exporter. The keyring must contain at
// least one key.
func NewExporter(lister Lister, keyring *Keyring) (*Exporter, error) {
	if lister == nil {
		return nil, errors.New("audit: NewExporter: nil lister")
	}
	if keyring == nil {
		return nil, errors.New("audit: NewExporter: nil keyring")
	}
	if _, _, ok := keyring.Primary(); !ok {
		return nil, errors.New("audit: NewExporter: empty keyring")
	}
	return &Exporter{lister: lister, keyring: keyring}, nil
}

// ExportJSONL streams the audit chain to w. Returns the number of rows
// emitted. Caller is responsible for closing w if needed (this fn does
// not close).
//
// The chain is computed in id-ASC order (the natural order from
// ListWrites DESC → reverse). When the lister returns zero rows, the
// emitted stream is empty and nil error is returned.
func (e *Exporter) ExportJSONL(ctx context.Context, w io.Writer, opts ExportOptions) (int, error) {
	if w == nil {
		return 0, errors.New("audit: ExportJSONL: nil writer")
	}
	// Resolve keyring entry by KeyID (or primary).
	keyID, secret, ok := e.resolveKey(opts.KeyID)
	if !ok {
		return 0, fmt.Errorf("audit: ExportJSONL: key %q not in keyring", opts.KeyID)
	}
	rows, err := e.lister.ListWrites(ctx, ListFilters{
		ProjectID: opts.ProjectID,
		SinceID:   opts.SinceID,
		Actor:     opts.Actor,
		WritePath: opts.WritePath,
		SessionID: opts.SessionID,
		Limit:     opts.Limit,
	})
	if err != nil {
		return 0, fmt.Errorf("audit: ExportJSONL: ListWrites: %w", err)
	}
	// ListWrites returns id DESC; reverse to id ASC for canonical chain.
	reverseInPlace(rows)
	var prevChainSelf string
	for i := range rows {
		ev := rows[i]
		_, self, err := ComputeChain(secret, prevChainSelf, ev)
		if err != nil {
			return i, fmt.Errorf("audit: ExportJSONL: ComputeChain row id=%d: %w", ev.ID, err)
		}
		ev.ChainPrev = prevChainSelf
		ev.ChainSelf = self
		ev.ChainKeyID = keyID
		// Marshal each row as a single-line JSON (no trailing \n in
		// the value — we add the line terminator below).
		bytes, err := json.Marshal(ev)
		if err != nil {
			return i, fmt.Errorf("audit: ExportJSONL: marshal row id=%d: %w", ev.ID, err)
		}
		if _, err := w.Write(bytes); err != nil {
			return i, fmt.Errorf("audit: ExportJSONL: write row id=%d: %w", ev.ID, err)
		}
		if _, err := io.WriteString(w, "\n"); err != nil {
			return i, fmt.Errorf("audit: ExportJSONL: write newline: %w", err)
		}
		prevChainSelf = self
	}
	return len(rows), nil
}

// resolveKey returns (id, secret) for the requested keyID, or the
// primary when keyID is empty. Returns ok=false when the requested id
// is non-empty and not in the keyring.
func (e *Exporter) resolveKey(keyID string) (string, []byte, bool) {
	if keyID != "" {
		secret, ok := e.keyring.Get(keyID)
		if !ok {
			return "", nil, false
		}
		return keyID, secret, true
	}
	return e.keyring.Primary()
}

// reverseInPlace reverses s in-place. Cheap O(n) swap.
func reverseInPlace(s []WriteEvent) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

// CountLines returns the number of non-empty lines in b (newline-
// delimited). Useful for tests + sanity checks.
func CountLines(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	s := string(b)
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}