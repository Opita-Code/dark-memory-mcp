package research

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func validResult() Result {
	return Result{
		Intent: IntentCVE,
		Query:  "CVE-2026-1234 fix version",
		Items: []Item{
			{
				Title:      "NVD entry",
				URL:        "https://nvd.nist.gov/vuln/detail/CVE-2026-1234",
				Snippet:    "Affects libfoo before 1.2.3.",
				Confidence: 0.95,
			},
		},
		Sources:   []string{"osv.dev", "nvd"},
		FetchedAt: time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC),
	}
}

func TestExample_ResultValidateAcceptsValid(t *testing.T) {
	r := validResult()
	if err := r.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestExample_ResultValidateAcceptsEmptyItems(t *testing.T) {
	r := validResult()
	r.Items = nil
	if err := r.Validate(); err != nil {
		t.Fatalf("empty items err = %v, want nil", err)
	}
}

func TestExample_ResultValidateRejectsNil(t *testing.T) {
	var r *Result
	if err := r.Validate(); !errors.Is(err, ErrEmptyResultQuery) {
		t.Fatalf("err = %v, want ErrEmptyResultQuery", err)
	}
}

func TestExample_ResultValidateRejectsBadIntent(t *testing.T) {
	r := validResult()
	r.Intent = Intent("sidecar")
	if err := r.Validate(); !errors.Is(err, ErrResultBadIntent) {
		t.Fatalf("err = %v, want ErrResultBadIntent", err)
	}
}

func TestExample_ResultValidateRejectsEmptyQuery(t *testing.T) {
	r := validResult()
	r.Query = "  "
	if err := r.Validate(); !errors.Is(err, ErrEmptyResultQuery) {
		t.Fatalf("err = %v, want ErrEmptyResultQuery", err)
	}
}

func TestExample_ResultValidateRejectsBlankSource(t *testing.T) {
	r := validResult()
	r.Sources = []string{"osv.dev", "  "}
	if err := r.Validate(); !errors.Is(err, ErrResultBadSource) {
		t.Fatalf("err = %v, want ErrResultBadSource", err)
	}
}

func TestExample_ResultValidateRejectsZeroTime(t *testing.T) {
	r := validResult()
	r.FetchedAt = time.Time{}
	if err := r.Validate(); !errors.Is(err, ErrResultZeroTime) {
		t.Fatalf("err = %v, want ErrResultZeroTime", err)
	}
}

func TestExample_ItemValidateRejectsEmptyTitle(t *testing.T) {
	r := validResult()
	r.Items[0].Title = ""
	if err := r.Validate(); !errors.Is(err, ErrEmptyItemTitle) {
		t.Fatalf("err = %v, want ErrEmptyItemTitle", err)
	}
}

func TestExample_ItemValidateRejectsBadURL(t *testing.T) {
	for _, bad := range []string{"", "notaurl", "ftp://files/x", "javascript:alert(1)", "https://"} {
		r := validResult()
		r.Items[0].URL = bad
		if err := r.Validate(); !errors.Is(err, ErrItemBadURL) {
			t.Fatalf("url %q err = %v, want ErrItemBadURL", bad, err)
		}
	}
}

func TestExample_ItemValidateRejectsBadConfidence(t *testing.T) {
	for _, bad := range []float64{math.NaN(), -0.1, 1.1, math.Inf(1)} {
		r := validResult()
		r.Items[0].Confidence = bad
		if err := r.Validate(); !errors.Is(err, ErrItemBadConfidence) {
			t.Fatalf("conf %v err = %v, want ErrItemBadConfidence", bad, err)
		}
	}
}

func TestExample_ItemValidateAcceptsBoundaries(t *testing.T) {
	for _, ok := range []float64{0, 1, 0.5, -0.0} {
		r := validResult()
		r.Items[0].Confidence = ok
		if err := r.Validate(); err != nil {
			t.Fatalf("conf %v err = %v, want nil", ok, err)
		}
	}
}

func TestExample_RedactStringPassesProseThrough(t *testing.T) {
	prose := "Affects libfoo before 1.2.3, fixed in the vendor advisory."
	if got := RedactString(prose); got != prose {
		t.Fatalf("prose changed: %q", got)
	}
}

func TestExample_RedactStringMasksBearer(t *testing.T) {
	got := RedactString("call with Bearer abcdefgh12345678 please")
	if strings.Contains(got, "abcdefgh12345678") {
		t.Fatalf("bearer leaked: %q", got)
	}
	if !strings.Contains(got, "Bearer [REDACTED]") {
		t.Fatalf("no marker: %q", got)
	}
}

func TestExample_RedactStringMasksKeyValue(t *testing.T) {
	got := RedactString("config api_key=supersecretvalue here")
	if strings.Contains(got, "supersecretvalue") {
		t.Fatalf("key leaked: %q", got)
	}
}

func TestExample_RedactStringMasksLongToken(t *testing.T) {
	tok := strings.Repeat("a", 40)
	got := RedactString("leaked " + tok + " end")
	if strings.Contains(got, tok) {
		t.Fatalf("token leaked: %q", got)
	}
}

func TestExample_RedactStringMasksEmail(t *testing.T) {
	got := RedactString("contact jdoe@example.com for details")
	if strings.Contains(got, "jdoe@example.com") {
		t.Fatalf("email leaked: %q", got)
	}
}

func TestExample_RedactResultDoesNotMutate(t *testing.T) {
	r := validResult()
	r.Items[0].Snippet = "Bearer abcdefgh12345678 stays raw here"
	red := RedactResult(r)
	if !strings.Contains(r.Items[0].Snippet, "abcdefgh12345678") {
		t.Fatal("receiver mutated; RedactResult must copy")
	}
	if strings.Contains(red.Items[0].Snippet, "abcdefgh12345678") {
		t.Fatalf("redacted copy leaks: %q", red.Items[0].Snippet)
	}
	r.Query = "lookup with Bearer abcdefgh12345678 inside"
	red = RedactResult(r)
	if strings.Contains(red.Query, "abcdefgh12345678") {
		t.Fatalf("redacted query leaks: %q", red.Query)
	}
	if err := red.Validate(); err != nil {
		t.Fatalf("redacted copy invalid: %v", err)
	}
}
