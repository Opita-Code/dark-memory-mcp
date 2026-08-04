// Package errorobs — types_test.go: classification + sanitization
// + constructor tests (spec 757, Wave 5D).
package errorobs

import (
	"context"
	"errors"
	"fmt"
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
