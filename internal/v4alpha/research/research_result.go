// This file defines the Result envelope: what a research adapter hands
// back after it runs (items, sources, confidence) and the Redact pass
// every envelope goes through before it is logged or reaches LLM context
// (Tier-1 control 3: redact-before-log, mandatory on all output paths).
// The injection-scan gate that runs after Redact lives in research_gate.go
// (cycle 4); the registry that serves adapter manifests lives in
// research_registry.go (cycle 5).
package research

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Sentinel errors for Result/Item validation. All callers should use
// errors.Is.
var (
	// ErrEmptyResultQuery is returned when Result.Query is empty.
	ErrEmptyResultQuery = errors.New("research: result query is empty")

	// ErrResultBadIntent is returned when Result.Intent is not canonical.
	ErrResultBadIntent = errors.New("research: result intent is not canonical")

	// ErrEmptyItemTitle is returned when an Item.Title is empty.
	ErrEmptyItemTitle = errors.New("research: result item title is empty")

	// ErrItemBadURL is returned when an Item.URL is empty or not an
	// http(s) URL.
	ErrItemBadURL = errors.New("research: result item URL is not a valid http(s) URL")

	// ErrItemBadConfidence is returned when an Item.Confidence is NaN or
	// outside [0, 1].
	ErrItemBadConfidence = errors.New("research: result item confidence is outside [0, 1]")

	// ErrResultZeroTime is returned when Result.FetchedAt is zero.
	ErrResultZeroTime = errors.New("research: result fetch time is zero")

	// ErrResultBadSource is returned when a Result.Sources entry is blank.
	ErrResultBadSource = errors.New("research: result source entry is blank")
)

// Item is one research hit: a titled URL with a snippet and the
// backend-reported confidence in [0, 1].
type Item struct {
	// Title names the hit. Never empty.
	Title string

	// URL locates the hit. Must be an http(s) URL.
	URL string

	// Snippet is backend prose about the hit. May carry leaked secrets;
	// Redact before logging.
	Snippet string

	// Confidence scores the hit in [0, 1]. NaN is rejected.
	Confidence float64
}

// Result is one adapter run: the intent, the query, every hit, and the
// backends that answered (Sources, most-preferred first).
type Result struct {
	// Intent is the canonical intent that ran.
	Intent Intent

	// Query is the operator query that ran. Never empty.
	Query string

	// Items holds the hits. Empty is valid: a run may find nothing.
	Items []Item

	// Sources names the backends that answered, most-preferred first.
	Sources []string

	// FetchedAt stamps the run. Never zero.
	FetchedAt time.Time
}

// Validate enforces the structural invariants of a Result. It checks shape
// only: it never dials a backend and never re-runs the query.
func (r *Result) Validate() error {
	if r == nil {
		return ErrEmptyResultQuery
	}
	if !IsValidIntent(r.Intent) {
		return ErrResultBadIntent
	}
	if strings.TrimSpace(r.Query) == "" {
		return ErrEmptyResultQuery
	}
	for i := range r.Items {
		if err := r.Items[i].validate(); err != nil {
			return err
		}
	}
	for _, s := range r.Sources {
		if strings.TrimSpace(s) == "" {
			return ErrResultBadSource
		}
	}
	if r.FetchedAt.IsZero() {
		return ErrResultZeroTime
	}
	return nil
}

// validate enforces the structural invariants of one Item.
func (it *Item) validate() error {
	if strings.TrimSpace(it.Title) == "" {
		return ErrEmptyItemTitle
	}
	u, err := url.ParseRequestURI(strings.TrimSpace(it.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return ErrItemBadURL
	}
	if it.Confidence != it.Confidence || it.Confidence < 0 || it.Confidence > 1 {
		return ErrItemBadConfidence
	}
	return nil
}

// Redact patterns (Tier-1 control 3). Deliberately aggressive: anything
// shaped like a credential is masked before the envelope is logged or
// handed to LLM context. Legit prose (words under 32 chars, no key shape)
// passes through untouched.
var (
	bearerRe  = regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9\-._~+/=]{8,}`)
	keyValRe  = regexp.MustCompile(`(?i)(api[_-]?key|secret|token|password)\s*[:=]\s*\S+`)
	longTokRe = regexp.MustCompile(`\b[A-Za-z0-9_\-+/=]{32,}\b`)
	emailRe   = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
)

// RedactString masks credential-shaped substrings in s: Bearer tokens,
// key=value secrets, long token runs (>= 32 chars), and email addresses.
// Plain prose passes through byte-identical.
func RedactString(s string) string {
	s = bearerRe.ReplaceAllString(s, "Bearer [REDACTED]")
	s = keyValRe.ReplaceAllString(s, "$1=[REDACTED]")
	s = longTokRe.ReplaceAllString(s, "[TOKEN REDACTED]")
	s = emailRe.ReplaceAllString(s, "[EMAIL REDACTED]")
	return s
}

// RedactResult returns a copy of r with Query and every Item Title and
// Snippet run through RedactString. Query is covered because operator
// input can carry pasted secrets; Sources is left alone (backend names
// come from the adapter manifest, never from untrusted output). The
// receiver is never mutated: callers keep the raw envelope for audit
// and log only the redacted copy.
func RedactResult(r Result) Result {
	out := r
	out.Query = RedactString(r.Query)
	out.Items = make([]Item, len(r.Items))
	for i, it := range r.Items {
		it.Title = RedactString(it.Title)
		it.Snippet = RedactString(it.Snippet)
		out.Items[i] = it
	}
	return out
}
