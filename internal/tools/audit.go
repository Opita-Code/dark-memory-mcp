// Package tools — audit.go: dark_memory_audit_export +
// dark_memory_audit_verify (Phase 9 alpha.20 Chunk 8.5, ADR-016 +
// ADR-018). Operators can dump the write_audit chain as a HMAC-signed
// JSONL stream and verify it offline (or via this tool).
//
// Wire shape:
//
//	dark_memory_audit_export
//	  in:  { project_id?, since_id?, actor?, write_path?, session_id?,
//	      limit?, key_id? }
//	  out: { row_count: N, key_id: "v1", bytes: "<JSONL stream, base64-or-utf8?>" }
//
//	  Decision: emit raw JSONL bytes inside a JSON string field. The
//	  operator saves the string to a file and pipes it back to
//	  audit_verify. JSON-in-JSON requires escaping, but the alternative
//	  (base64) would break the round-trip diff-tool workflow.
//
//	dark_memory_audit_verify
//	  in:  { stream: "<JSONL stream, raw string>", key_id? }
//	  out: { status: "ok" | "broken" | "unknown_key" | "malformed",
//	      rows_verified: N, first_bad_id?: id, reason?: "..." }
//
// Both tools are read-only and produce audit-side audit rows
// (dark_memory_audit_export emits a write_audit_export row, and
// dark_memory_audit_verify emits a write_audit_verify row) so that
// even the act of dumping is auditable (INV-1 compliance).
package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// RegisterAudit wires the 2 AUDIT tools into the registry. Called
// from RegisterObservabilityWithKeyring (where the audit tools
// naturally live — they are part of the audit/OBSERVABILITY surface
// per spec §3.5).
//
// kr may be nil — when nil, the tools still register (canonical
// surface requirement), but calls return a clear error explaining
// that DARK_AUDIT_HMAC_KEY must be set. This keeps the canonical
// tool count stable across operators with/without the env var.
func RegisterAudit(reg *Registry, st store.Store, kr *audit.Keyring) error {
	if reg == nil {
		return errors.New("tools: RegisterAudit: nil registry")
	}
	if st == nil {
		return errors.New("tools: RegisterAudit: nil store")
	}
	// kr may be nil — the tools will refuse calls when it's missing.

	// audit_export — emits a HMAC-chained JSONL stream of write_audit
	// rows for offline verification.
	reg.Add(BindStore("audit_export",
		"Export the write_audit chain as a HMAC-chained JSONL stream (ADR-016). Operators can save the stream to disk and verify it offline via dark_memory_audit_verify or any tool that recomputes HMAC-SHA256 over (chain_prev || canonical_row_bytes). Read-only; emits a write_audit_export audit row. Requires DARK_AUDIT_HMAC_KEY env var to be set at boot — otherwise returns ErrAuditNoKeyring.",
		MustJSONSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"project_id": map[string]any{"type": "string", "description": "Filter by project_id. Empty = all projects."},
				"since_id":   map[string]any{"type": "integer", "description": "id > this value; 0 = all rows."},
				"actor":      map[string]any{"type": "string", "description": "Filter by actor."},
				"write_path": map[string]any{"type": "string", "description": "Filter by write_path."},
				"session_id": map[string]any{"type": "string", "description": "Filter by session_id."},
				"limit":      map[string]any{"type": "integer", "description": "Max rows. 0 = no limit."},
				"key_id":     map[string]any{"type": "string", "description": "Key id to stamp on chain_key_id (must be in keyring). Default v1."},
			},
		}),
		st,
		func(ctx context.Context, s store.Store, in AuditExportInput) (*AuditExportResult, error) {
			if kr == nil {
				return nil, ErrAuditNoKeyring
			}
			exp, err := audit.NewExporter(s, kr)
			if err != nil {
				return nil, fmt.Errorf("audit_export: %w", err)
			}
			var buf bytes.Buffer
			n, err := exp.ExportJSONL(ctx, &buf, audit.ExportOptions{
				ProjectID: in.ProjectID,
				SinceID:   in.SinceID,
				Actor:     in.Actor,
				WritePath: in.WritePath,
				SessionID: in.SessionID,
				Limit:     in.Limit,
				KeyID:     in.KeyID,
			})
			if err != nil {
				return nil, fmt.Errorf("audit_export: %w", err)
			}
			keyID := in.KeyID
			if keyID == "" {
				if id, _, ok := kr.Primary(); ok {
					keyID = id
				}
			}
			return &AuditExportResult{
				RowCount: n,
				KeyID:    keyID,
				Stream:   buf.String(),
			}, nil
		}))

	// audit_verify — re-derives the HMAC chain from a JSONL stream
	// and reports the verification status.
	reg.Add(BindStore("audit_verify",
		"Verify a HMAC-chained JSONL audit stream against the local keyring (ADR-018). Returns one of: ok | broken | unknown_key | malformed. On broken/unknown_key/malformed, first_bad_id points to the first row that failed verification. Read-only; emits a write_audit_verify audit row. Requires DARK_AUDIT_HMAC_KEY env var to be set at boot — otherwise returns ErrAuditNoKeyring.",
		MustJSONSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"stream": map[string]any{"type": "string", "description": "JSONL stream from dark_memory_audit_export (raw string, not base64)."},
				"key_id": map[string]any{"type": "string", "description": "Override the key id to use (must be in keyring). Empty = use whatever the stream declares."},
			},
			"required": []string{"stream"},
		}),
		st,
		func(ctx context.Context, s store.Store, in AuditVerifyInput) (*AuditVerifyResult, error) {
			if kr == nil {
				return nil, ErrAuditNoKeyring
			}
			if strings.TrimSpace(in.Stream) == "" {
				return nil, errors.New("audit_verify: empty stream")
			}
			v, err := audit.NewVerifier(kr)
			if err != nil {
				return nil, fmt.Errorf("audit_verify: %w", err)
			}
			rep, err := v.VerifyBytes([]byte(in.Stream))
			if err != nil && rep == nil {
				// Scanner/IO failure — no rows processed yet.
				return nil, fmt.Errorf("audit_verify: %w", err)
			}
			if rep == nil {
				return nil, errors.New("audit_verify: nil report")
			}
			return &AuditVerifyResult{
				Status:       rep.Status,
				RowsVerified: rep.RowsVerified,
				FirstBadID:   rep.FirstBadID,
				Reason:       rep.Reason,
				KeyID:        rep.KeyID,
			}, nil
		}))

	return nil
}

// ErrAuditNoKeyring is returned when audit_export or audit_verify is
// called but the server has no HMAC keyring configured (DARK_AUDIT_HMAC_KEY
// not set at boot). Operators can either set the env var + restart, or
// use dark_memory_writes (the unauthenticated read-only audit list) for
// ad-hoc inspection.
var ErrAuditNoKeyring = errors.New("audit: no HMAC keyring configured (set DARK_AUDIT_HMAC_KEY env var; format: id:hexsecret, comma-separated for rotation)")

// AuditExportInput is the input for audit_export.
type AuditExportInput struct {
	ProjectID string `json:"project_id,omitempty"`
	SinceID   int64  `json:"since_id,omitempty"`
	Actor     string `json:"actor,omitempty"`
	WritePath string `json:"write_path,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	KeyID     string `json:"key_id,omitempty"`
}

// AuditExportResult is the output for audit_export.
type AuditExportResult struct {
	RowCount int    `json:"row_count"`
	KeyID    string `json:"key_id"`
	Stream   string `json:"stream"` // raw JSONL, one row per line
}

// AuditVerifyInput is the input for audit_verify.
type AuditVerifyInput struct {
	Stream string `json:"stream"`
	KeyID  string `json:"key_id,omitempty"`
}

// AuditVerifyResult is the output for audit_verify.
type AuditVerifyResult struct {
	Status       string `json:"status"`        // ok | broken | unknown_key | malformed
	RowsVerified int    `json:"rows_verified"` // 0 on any failure
	FirstBadID   int64  `json:"first_bad_id,omitempty"`
	Reason       string `json:"reason,omitempty"`
	KeyID        string `json:"key_id,omitempty"`
}