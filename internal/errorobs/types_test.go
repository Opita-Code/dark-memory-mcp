// Package errorobs — types_test.go: classification + sanitization
// + constructor tests (spec 757, Wave 5D).
package errorobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// fakeSentinel mirrors the store registration contract.
var fakeSentinel = errors.New("store: row not found")

func init() {
	RegisterSentinels([]SentinelRegistration{
		{Err: fakeSentinel, Domain: DomainStore, Code: "ErrNotFound"},
	})
}

func TestClassify_StoreSentinel(t *testing.T) {
	// Wrapped sentinel (the production shape: fmt.Errorf("...%w", err)).
	err := fmt.Errorf("GetSpec: %w", fakeSentinel)
	d, code, sev := Classify(err)
	if d != DomainStore {
		t.Errorf("domain = %s, want store", d)
	}
	if code != "ErrNotFound" {
		t.Errorf("code = %s, want ErrNotFound", code)
	}
	if sev != SeverityError {
		t.Errorf("severity = %s, want error", sev)
	}
}

func TestClassify_ContextDeadline(t *testing.T) {
	err := context.DeadlineExceeded
	d, code, _ := Classify(err)
	if d != DomainNetwork {
		t.Errorf("domain = %s, want network", d)
	}
	if code != "context_deadline_exceeded" {
		t.Errorf("code = %s, want context_deadline_exceeded", code)
	}
}

func TestClassify_ContextCanceled(t *testing.T) {
	err := context.Canceled
	d, code, _ := Classify(err)
	if d != DomainNetwork {
		t.Errorf("domain = %s, want network", d)
	}
	if code != "context_deadline_exceeded" {
		t.Errorf("code = %s, want context_deadline_exceeded", code)
	}
}

func TestClassify_Unknown(t *testing.T) {
	err := errors.New("weird thing happened")
	d, code, sev := Classify(err)
	if d != DomainUnknown {
		t.Errorf("domain = %s, want unknown", d)
	}
	if code != "ErrInternal" {
		t.Errorf("code = %s, want ErrInternal", code)
	}
	if sev != SeverityError {
		t.Errorf("severity = %s, want error", sev)
	}
}

func TestClassify_LLMHeuristic(t *testing.T) {
	err := fmt.Errorf("mindset_apply: no llm available: %w", errors.New("rate limit"))
	d, code, _ := Classify(err)
	if d != DomainLLM {
		t.Errorf("domain = %s, want llm", d)
	}
	if code != "ErrNoLLMAvailable" {
		t.Errorf("code = %s, want ErrNoLLMAvailable", code)
	}
}

// TestClassify_HeuristicBranches pins every message-prefix heuristic
// branch in Classify. Each return statement is a mutation point;
// removing one makes the function fall through to the default and
// return DomainUnknown/ErrInternal — a silent classification loss
// that the Error Observatory would then mis-categorize. Kills the
// statement-removal mutants in the switch chain.
func TestClassify_HeuristicBranches(t *testing.T) {
	cases := []struct {
		name     string
		msg      string
		wantD    Domain
		wantCode string
	}{
		{"gate_prefix", "gate: capability expired", DomainGate, "ErrGateRefusal"},
		{"gate_postcheck", "postcheck refused: drift", DomainGate, "ErrGateRefusal"},
		{"gate_precheck", "precheck: scope required", DomainGate, "ErrGateRefusal"},
		{"sweeper", "sweeper: session close failed", DomainSweep, "ErrSweep"},
		{"sweep_verb", "session sweep timed out", DomainSweep, "ErrSweep"},
		{"validation_invalid_argument", "invalid argument: tasks", DomainValidation, "ErrInvalidArgument"},
		{"validation_missing_field", "missing field: session_id", DomainValidation, "ErrInvalidArgument"},
		{"validation_required", "project is required", DomainValidation, "ErrInvalidArgument"},
		{"network_http", "http: connection reset", DomainNetwork, "ErrNetwork"},
		{"network_dial", "dial tcp 1.2.3.4:443: connect: refused", DomainNetwork, "ErrNetwork"},
		{"network_conn_refused", "connection refused", DomainNetwork, "ErrNetwork"},
		{"network_eof", "unexpected eof", DomainNetwork, "ErrNetwork"},
		{"store_prefix", "store: row not found", DomainStore, "ErrStore"},
		{"store_sqlite", "sqlite: constraint failed", DomainStore, "ErrStore"},
		{"store_postgres", "postgres: connection error", DomainStore, "ErrStore"},
		{"llm_llm_prefix", "llm: rate limit exceeded", DomainLLM, "ErrNoLLMAvailable"},
		{"llm_judge_error", "judge error: model unavailable", DomainLLM, "ErrNoLLMAvailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, code, _ := Classify(errors.New(tc.msg))
			if d != tc.wantD {
				t.Errorf("Classify(%q) domain = %s, want %s", tc.msg, d, tc.wantD)
			}
			if code != tc.wantCode {
				t.Errorf("Classify(%q) code = %s, want %s", tc.msg, code, tc.wantCode)
			}
		})
	}
}

func TestClassify_Nil(t *testing.T) {
	d, code, sev := Classify(nil)
	if d != DomainUnknown || code != "nil" || sev != SeverityWarn {
		t.Errorf("nil classify = (%s, %s, %s), want (unknown, nil, warn)", d, code, sev)
	}
}

func TestSanitize_WhitespaceCollapseAndCap(t *testing.T) {
	msg := "  line1\n\tline2\n   line3  "
	got := Sanitize(msg)
	if got != "line1 line2 line3" {
		t.Errorf("Sanitize = %q, want %q", got, "line1 line2 line3")
	}

	long := make([]byte, 1000)
	for i := range long {
		long[i] = 'a'
	}
	got = Sanitize(string(long))
	if len(got) != 512 {
		t.Errorf("Sanitize cap = %d, want 512", len(got))
	}
}

func TestSanitize_Empty(t *testing.T) {
	if got := Sanitize(""); got != "" {
		t.Errorf("Sanitize('') = %q, want empty", got)
	}
}

// TestSanitize_ExactBoundary pins the 512 cap at the boundary:
// 512 chars stays, 513 truncates. Kills mutations that flip the
// comparison (>= would truncate 512 to 511, removal would keep 513).
func TestSanitize_ExactBoundary(t *testing.T) {
	makeMsg := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = 'x'
		}
		return string(b)
	}
	if got := Sanitize(makeMsg(512)); len(got) != 512 {
		t.Errorf("512 chars: got len %d, want 512", len(got))
	}
	if got := Sanitize(makeMsg(513)); len(got) != 512 {
		t.Errorf("513 chars: got len %d, want 512", len(got))
	}
}

func TestMessageHash_Stable(t *testing.T) {
	a := MessageHash("some error message")
	b := MessageHash("some error message")
	if a != b {
		t.Errorf("hash not stable: %s vs %s", a, b)
	}
	if a == "" {
		t.Error("hash is empty")
	}
}

func TestNew_AppliesDefaults(t *testing.T) {
	e := New("default", "sess-1", "memory_state", fakeSentinel)
	if e.Domain != DomainStore {
		t.Errorf("domain = %s, want store", e.Domain)
	}
	if e.Code != "ErrNotFound" {
		t.Errorf("code = %s, want ErrNotFound", e.Code)
	}
	if e.Count != 1 {
		t.Errorf("count = %d, want 1", e.Count)
	}
	if e.ProjectID != "default" {
		t.Errorf("project = %s, want default", e.ProjectID)
	}
	if e.Message == "" {
		t.Error("message empty")
	}
}

func TestWithSeverityAndContext(t *testing.T) {
	e := New("default", "", "t", fakeSentinel)
	fatal := e.WithSeverity(SeverityFatal)
	if fatal.Severity != SeverityFatal {
		t.Errorf("severity = %s, want fatal", fatal.Severity)
	}
	if e.Severity != SeverityError {
		t.Errorf("original mutated: severity = %s, want error (immutable copy)", e.Severity)
	}

	withCtx := e.WithContext(`{"spec_id": 1}`)
	if withCtx.ContextJSON != `{"spec_id": 1}` {
		t.Errorf("context = %q", withCtx.ContextJSON)
	}
	if e.ContextJSON != "" {
		t.Errorf("original mutated: context = %q", e.ContextJSON)
	}
}

// TestErrorEvent_JSONRoundTrip pins the wire contract of ErrorEvent:
// every field must survive a marshal → unmarshal round-trip. This
// kills struct-field mutations (removing ID/ProjectID/SessionID/etc.)
// that no classification test catches — the ERROR_OBS tools surface
// ErrorEvent over JSON-RPC, so a dropped field is a wire regression.
func TestErrorEvent_JSONRoundTrip(t *testing.T) {
	in := ErrorEvent{
		ID:             42,
		ProjectID:      "default",
		SessionID:      "sess-abc",
		ToolName:       "memory_state",
		Domain:         DomainStore,
		Code:           "ErrNotFound",
		Message:        "row not found",
		ContextJSON:    `{"spec_id": 1}`,
		Severity:       SeverityError,
		Count:          3,
		FirstSeenAt:    "2026-08-06T00:00:00Z",
		LastSeenAt:     "2026-08-06T01:00:00Z",
		Resolved:       true,
		ResolvedAt:     "2026-08-06T02:00:00Z",
		ResolutionNote: "triaged",
		CreatedAt:      "2026-08-06T00:00:00Z",
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	// Assert exact wire keys — a struct-field json tag mutation
	// (removing `json:"id"` → key becomes "ID") survives a symmetric
	// round-trip but breaks the wire contract the harness sees.
	for _, key := range []string{
		`"id":42`, `"project_id":"default"`, `"session_id":"sess-abc"`,
		`"tool_name":"memory_state"`, `"domain":"store"`, `"code":"ErrNotFound"`,
		`"severity":"error"`, `"count":3`, `"resolved":true`,
	} {
		if !bytes.Contains(data, []byte(key)) {
			t.Errorf("Marshal output missing %s — JSON tag mutated?\n got: %s", key, data)
		}
	}
	var out ErrorEvent
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", out, in)
	}
}

// TestSetClock_OverridesTimestamps verifies SetClock actually
// replaces the clock used by New(). Kills the assignment-removal
// mutant (nowRFC3339 = fn → no-op).
func TestSetClock_OverridesTimestamps(t *testing.T) {
	SetClock(func() string { return "2026-01-01T00:00:00.000000000Z" })
	defer func() { SetClock(nil) }() // restore real clock; nil resets to default

	e := New("default", "sess-1", "tool", fakeSentinel)
	if e.CreatedAt != "2026-01-01T00:00:00.000000000Z" {
		t.Errorf("CreatedAt = %q, want injected clock value", e.CreatedAt)
	}
	if e.FirstSeenAt != "2026-01-01T00:00:00.000000000Z" {
		t.Errorf("FirstSeenAt = %q, want injected clock value", e.FirstSeenAt)
	}
	if e.LastSeenAt != "2026-01-01T00:00:00.000000000Z" {
		t.Errorf("LastSeenAt = %q, want injected clock value", e.LastSeenAt)
	}
}

// TestIsError_NilReceiver pins the nil-safe contract of IsError.
// Kills the nil-check return removal mutant.
func TestIsError_NilReceiver(t *testing.T) {
	var e *ErrorEvent
	if got := e.IsError(); got != "" {
		t.Errorf("nil.IsError() = %q, want empty", got)
	}
}

// TestIsError_NonNil pins the log-line shape.
func TestIsError_NonNil(t *testing.T) {
	e := &ErrorEvent{Domain: DomainStore, Code: "ErrNotFound", Count: 2, Message: "row missing"}
	got := e.IsError()
	for _, want := range []string{"domain=store", "code=ErrNotFound", "count=2", `msg="row missing"`} {
		if !strings.Contains(got, want) {
			t.Errorf("IsError() = %q, missing %q", got, want)
		}
	}
}

func TestValidDomainAndSeverity(t *testing.T) {
	for _, d := range []Domain{DomainStore, DomainLLM, DomainGate, DomainValidation, DomainNetwork, DomainSweep, DomainUnknown} {
		if !ValidDomain(d) {
			t.Errorf("ValidDomain(%s) = false, want true", d)
		}
	}
	if ValidDomain("bogus") {
		t.Error("ValidDomain(bogus) = true, want false")
	}
	for _, s := range []Severity{SeverityFatal, SeverityError, SeverityWarn} {
		if !ValidSeverity(s) {
			t.Errorf("ValidSeverity(%s) = false, want true", s)
		}
	}
	if ValidSeverity("bogus") {
		t.Error("ValidSeverity(bogus) = true, want false")
	}
}
