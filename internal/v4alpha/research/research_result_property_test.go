package research

import (
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func TestProperty_ValidResultAlwaysValidates(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		r := validResult()
		r.Query = rapid.StringMatching(`[A-Za-z0-9][A-Za-z0-9 .\-]{1,40}`).Draw(t, "query")
		r.Items[0].Confidence = rapid.Float64Range(0, 1).Draw(t, "conf")
		if err := r.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})
}

func TestProperty_RedactNeverLeaksBearer(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		secret := rapid.StringMatching(`[A-Za-z0-9]{8,24}`).Draw(t, "secret")
		in := "prefix Bearer " + secret + " suffix"
		got := RedactString(in)
		if strings.Contains(got, secret) {
			t.Fatalf("bearer %q leaked in %q", secret, got)
		}
	})
}

func TestProperty_RedactNeverLeaksEmail(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		user := rapid.StringMatching(`[a-z]{3,10}`).Draw(t, "user")
		mail := user + "@example.com"
		got := RedactString("contact " + mail + " now")
		if strings.Contains(got, mail) {
			t.Fatalf("email %q leaked in %q", mail, got)
		}
	})
}

func TestProperty_RedactIsIdempotent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.StringMatching(`[A-Za-z0-9 @._\-=:]{0,80}`).Draw(t, "in")
		once := RedactString(in)
		if twice := RedactString(once); twice != once {
			t.Fatalf("not idempotent: %q -> %q -> %q", in, once, twice)
		}
	})
}

func TestProperty_RedactedResultValidates(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		r := validResult()
		r.Items[0].Snippet = rapid.StringMatching(`[A-Za-z0-9 .\-]{1,60}`).Draw(t, "snippet")
		red := RedactResult(r)
		if err := red.Validate(); err != nil {
			t.Fatalf("redacted invalid: %v", err)
		}
	})
}

func TestProperty_OutOfRangeConfidenceAlwaysRejected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		delta := rapid.Float64Range(0.001, 1000).Draw(t, "delta")
		conf := 1 + delta
		if rapid.Bool().Draw(t, "neg") {
			conf = -delta
		}
		r := validResult()
		r.Items[0].Confidence = conf
		r.FetchedAt = time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC)
		if err := r.Validate(); err == nil {
			t.Fatalf("conf %v validated without error", conf)
		}
	})
}
