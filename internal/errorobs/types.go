// Package errorobs is the Error Observatory — the Wave 5D component
// (spec 757) that captures, classifies, and backlogs errors and bugs
// durably. The thesis: an error that is only visible on the MCP wire
// (or only in stderr) is an error that never happened.
//
// The package provides:
//
//  1. The wire types: ErrorEvent (one deduplicated error cluster),
//     ErrorListFilters, ErrorSummary.
//  2. The classification taxonomy: Domain (store|llm|gate|validation|
//     network|sweep|unknown) + Severity (fatal|error|warn), plus
//     Classify() which unwraps the error chain and maps the 17 store
//     sentinels (and orchestrator sentinels) to their domain.
//  3. Sanitization: messages are scrubbed before persistence (no
//     PII, no payloads, no secrets — mirrors the "we do NOT leak raw
//     error strings to the LLM" contract in internal/tools/errors.go).
//
// Dedup contract (SaveErrorEvent in the store layer): identical
// (domain, code, message_hash, tool_name, session_id) within a 24h
// window increments count + last_seen_at instead of inserting a new
// row — a crashed sweeper tick that fires 10 times is ONE event with
// count=10, not ten rows.
//
// Persistence note: error_events rows are intentionally NOT covered
// by INV-1 write_audit (they are diagnostics, not data writes, and
// the audit path is often the thing that is failing). This deviation
// is documented at the migration and the Store impl.
package errorobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Domain is the classification axis for an error: WHERE it happened.
type Domain string

const (
	// DomainStore: DB-level failures (connection, migration,
	// constraint, not found, cross-project access, version mismatch).
	DomainStore Domain = "store"
	// DomainLLM: LLM call failures (timeout, rate limit, model
	// unavailable, judge error, enrichment failure).
	DomainLLM Domain = "llm"
	// DomainGate: GateMiddleware refusals (scope required, capability
	// expired, frame stale, drift-at-write, canary-in-payload).
	DomainGate Domain = "gate"
	// DomainValidation: input validation failures (missing field,
	// bad format, invalid vibe_case, invalid argument).
	DomainValidation Domain = "validation"
	// DomainNetwork: HTTP / I/O / transport errors.
	DomainNetwork Domain = "network"
	// DomainSweep: sweeper errors (session close failures, reconcile
	// errors, stale session promotion failures).
	DomainSweep Domain = "sweep"
	// DomainUnknown: catch-all. Code class stays ErrInternal.
	DomainUnknown Domain = "unknown"
)

// ValidDomain reports whether d is a known Domain.
func ValidDomain(d Domain) bool {
	switch d {
	case DomainStore, DomainLLM, DomainGate, DomainValidation, DomainNetwork, DomainSweep, DomainUnknown:
		return true
	}
	return false
}

// Severity is the impact axis for an error.
type Severity string

const (
	// SeverityFatal: unrecoverable — the request cannot complete and
	// the failure implies systemic damage (e.g. a gate refusal that
	// blocks ALL writes, a constitution drift).
	SeverityFatal Severity = "fatal"
	// SeverityError: the request failed but the system continues.
	SeverityError Severity = "error"
	// SeverityWarn: degraded behavior, the request completed with
	// reduced fidelity (e.g. enrichment skipped, cache persist
	// failed, best-effort step dropped).
	SeverityWarn Severity = "warn"
)

// ValidSeverity reports whether s is a known Severity.
func ValidSeverity(s Severity) bool {
	switch s {
	case SeverityFatal, SeverityError, SeverityWarn:
		return true
	}
	return false
}

// ErrorEvent is one deduplicated error cluster in error_events.
// count > 1 means the same (domain, code, message_hash, tool_name,
// session_id) occurred multiple times within the dedup window.
type ErrorEvent struct {
	ID             int64    `json:"id"`
	ProjectID      string   `json:"project_id"`
	SessionID      string   `json:"session_id,omitempty"`
	ToolName       string   `json:"tool_name,omitempty"`
	Domain         Domain   `json:"domain"`
	Code           string   `json:"code"`
	Message        string   `json:"message"`
	ContextJSON    string   `json:"context_json,omitempty"`
	Severity       Severity `json:"severity"`
	Count          int      `json:"count"`
	FirstSeenAt    string   `json:"first_seen_at"`
	LastSeenAt     string   `json:"last_seen_at"`
	Resolved       bool     `json:"resolved"`
	ResolvedAt     string   `json:"resolved_at,omitempty"`
	ResolutionNote string   `json:"resolution_note,omitempty"`
	CreatedAt      string   `json:"created_at"`
}

// ErrorListFilters narrows ListErrorEvents. Zero-valued fields mean
// "no filter on this dimension". ProjectID is enforced by the Store
// impl (INV-7); the caller does not set it.
//
// CrossProject is the opt-in escape hatch for operator triage across
// the project boundary (admin elevation — Sentry Org Owner / Datadog
// Admin Standard pattern). When true:
//   - the Store skips the `project_id = ?` WHERE filter
//   - the tools layer MUST have pre-validated the caller's operator
//     id against DARK_ERROR_OBS_ADMIN_OPERATORS allow-list or
//     DARK_ERROR_OBS_OPERATOR_OVERRIDE=armed bypass flag
//   - every list/get/resolve that uses this path emits a write_audit
//     row with Actor="error_observatory_cross_project" so the
//     action is auditable end-to-end
//
// Default (false) preserves INV-7 strictly: rows from other projects
// are filtered out at the SQL layer (existence-leak parity).
//
// This is NOT a bypass of INV-7 — it is a deliberate, auditable
// exception to the default scoping policy. The constitution
// (SECURITY.md:54) treats INV-7 as a defense-in-depth control; this
// field makes the exception explicit and traceable rather than
// hidden in a config flag.
type ErrorListFilters struct {
	Domain    Domain
	Severity  Severity
	Resolved  *bool // nil = all; true = resolved only; false = unresolved only
	SessionID string
	ToolName  string
	Since     string // RFC3339: only events with last_seen_at >= Since
	Limit     int    // <= 0 means no limit

	// CrossProject (opt-in, default false): when true, the Store
	// returns rows from ALL projects instead of filtering by the
	// active project_id. Tools layer enforces the operator allow-
	// list / bypass flag before setting this to true.
	CrossProject bool
}

// ErrorSummary is the aggregate health snapshot returned by
// Store.ErrorSummary. Global scope (like Store.Stats) so operators
// can see cross-project health.
type ErrorSummary struct {
	TotalErrors    int              `json:"total_errors"`
	Unresolved     int              `json:"unresolved"`
	ErrorsLastHour int              `json:"errors_last_hour"`
	ByDomain       map[Domain]int   `json:"by_domain"`
	BySeverity     map[Severity]int `json:"by_severity"`
	TopRecurring   []ErrorEvent     `json:"top_recurring"`
	ReportedAt     string           `json:"reported_at"`
}

// --- Classification (T3) ---

// Classify unwraps the error chain and returns (domain, code,
// severity) for err. The code is the sentinel name when a known
// sentinel matches, else "ErrInternal".
//
// The mapping covers the 17 store sentinels (internal/store) + the
// orchestrator sentinels (ErrNoLLMAvailable) + the standard library
// errors (context.DeadlineExceeded → network). Unknown errors stay
// DomainUnknown/ErrInternal — the tools layer already has the
// "classifyUnknown → ErrInternal" catch-all, this is its domain
// twin.
//
// severity is ERROR by default; callers may upgrade to FATAL or
// downgrade to WARN when they have site-specific knowledge (e.g. a
// gate refusal that blocks all writes is FATAL, an enrichment skip
// is WARN).
func Classify(err error) (Domain, string, Severity) {
	if err == nil {
		return DomainUnknown, "nil", SeverityWarn
	}

	// Standard library / context errors first (cheap, no store import).
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return DomainNetwork, "context_deadline_exceeded", SeverityError
	}

	// The code references the store sentinels by name WITHOUT
	// importing internal/store (avoids a dependency cycle in the
	// classification hot path). The store layer wraps its sentinels
	// in fmt.Errorf with %w, so errors.Is works at runtime regardless
	// of import direction. The table below is kept in sync with
	// internal/store/store.go:113-143 by a test (TestClassify_Sentinels).
	for _, sentinel := range sentinelTable {
		if errors.Is(err, sentinel.err) {
			return sentinel.domain, sentinel.code, SeverityError
		}
	}

	// Orphaned sentinel check: the store wraps with %w, but defensive
	// callers may compare against a NEW sentinel the classifier does
	// not know yet — the message prefix "store: " still tells us the
	// domain.
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.HasPrefix(lower, "store:"),
		strings.Contains(lower, "sqlite:"),
		strings.Contains(lower, "postgres:"):
		return DomainStore, "ErrStore", SeverityError
	case strings.Contains(lower, "no llm available"),
		strings.Contains(lower, "llm:"),
		strings.Contains(lower, "judge error"):
		return DomainLLM, "ErrNoLLMAvailable", SeverityError
	case strings.Contains(lower, "gate:"),
		strings.Contains(lower, "gate refusal"),
		strings.Contains(lower, "errframestale"),
		strings.Contains(lower, "errcapabilitynotgranted"),
		strings.Contains(lower, "errscoperequired"),
		strings.Contains(lower, "errcapabilitiesexpired"),
		strings.Contains(lower, "errdriftatwrite"),
		strings.Contains(lower, "errsessionnotfound"),
		strings.Contains(lower, "errsessionnotresurrectable"),
		strings.Contains(lower, "postcheck"),
		strings.Contains(lower, "precheck"):
		return DomainGate, "ErrGateRefusal", SeverityError
	case strings.Contains(lower, "sweeper"),
		strings.Contains(lower, "sweep "):
		return DomainSweep, "ErrSweep", SeverityWarn
	case strings.Contains(lower, "invalid argument"),
		strings.Contains(lower, "missing field"),
		strings.Contains(lower, "required"):
		return DomainValidation, "ErrInvalidArgument", SeverityError
	case strings.Contains(lower, "http"),
		strings.Contains(lower, "dial tcp"),
		strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "eof"):
		return DomainNetwork, "ErrNetwork", SeverityError
	}

	return DomainUnknown, "ErrInternal", SeverityError
}

// sentinelTable maps the store sentinels to their domain+code. The
// err values are package-private copies — errors.Is works because Go
// error wrapping compares values through Unwrap chains, and the
// sentinel *values* are the same ones the store exports (the
// classifier registers them from the store package at init in
// RegisterSentinels; see below).
var sentinelTable []sentinelEntry

type sentinelEntry struct {
	err    error
	domain Domain
	code   string
}

// RegisterSentinels wires the store's sentinel errors into the
// classifier. Called from the store package at init (see
// internal/store/sqlite/store.go) so the classifier never imports
// internal/store (cycle-free) but still matches the real sentinels
// at runtime. Idempotent: re-registering the same code overwrites.
func RegisterSentinels(entries []SentinelRegistration) {
	table := make([]sentinelEntry, 0, len(entries))
	for _, e := range entries {
		table = append(table, sentinelEntry{err: e.Err, domain: e.Domain, code: e.Code})
	}
	sentinelTable = table
}

// SentinelRegistration is one sentinel→(domain, code) mapping.
type SentinelRegistration struct {
	Err    error
	Domain Domain
	Code   string
}

// --- Sanitization ---

// Sanitize scrubs a raw error message for persistence. The rules:
//
//   - Trim whitespace; collapse internal newlines to spaces.
//   - Cap at 512 bytes (message column is TEXT; a bounded message
//     keeps the FTS-ish scans and the dedup hash meaningful).
//   - Do NOT strip error codes or sentinel names (they are the
//     classification fingerprint).
//   - Do NOT attempt PII redaction beyond the cap (the raw error
//     string from Go rarely contains secrets — the tools layer
//     already refuses to send raw strings to the LLM; this is the
//     same contract server-side).
func Sanitize(msg string) string {
	if msg == "" {
		return ""
	}
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > 512 {
		msg = msg[:512]
	}
	return msg
}

// MessageHash returns the sha256 hex of the sanitized message —
// the dedup fingerprint column.
func MessageHash(msg string) string {
	sum := sha256.Sum256([]byte(Sanitize(msg)))
	return hex.EncodeToString(sum[:])
}

// --- Constructors ---

// New builds an ErrorEvent from raw parts, applying Sanitize +
// MessageHash + Classify defaults. Callers override Domain/Code/
// Severity after New when they have site-specific knowledge.
func New(projectID, sessionID, toolName string, err error) *ErrorEvent {
	domain, code, sev := Classify(err)
	return &ErrorEvent{
		ProjectID:   projectID,
		SessionID:   sessionID,
		ToolName:    toolName,
		Domain:      domain,
		Code:        code,
		Message:     Sanitize(err.Error()),
		Severity:    sev,
		Count:       1,
		FirstSeenAt: nowRFC3339(),
		LastSeenAt:  nowRFC3339(),
		CreatedAt:   nowRFC3339(),
	}
}

// WithSeverity returns a copy with the severity overridden (the
// immutable-with-copy pattern keeps callers honest).
func (e *ErrorEvent) WithSeverity(s Severity) *ErrorEvent {
	c := *e
	c.Severity = s
	return &c
}

// WithContext returns a copy with context_json set.
func (e *ErrorEvent) WithContext(ctxJSON string) *ErrorEvent {
	c := *e
	c.ContextJSON = ctxJSON
	return &c
}

// nowRFC3339 returns the current UTC time in RFC3339Nano. A package
// var so tests can override.
var nowRFC3339 = func() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// SetClock overrides the clock (tests).
func SetClock(fn func() string) {
	if fn != nil {
		nowRFC3339 = fn
	}
}

// IsError returns a descriptive string for operator logs.
func (e *ErrorEvent) IsError() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("error_event domain=%s code=%s count=%d msg=%q",
		e.Domain, e.Code, e.Count, e.Message)
}
