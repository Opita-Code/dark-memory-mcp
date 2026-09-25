package research

import (
	"errors"
	"testing"
)

func TestExample_AllIntentsHas18(t *testing.T) {
	all := AllIntents()
	if len(all) != 18 {
		t.Fatalf("len(AllIntents()) = %d, want 18", len(all))
	}
	seen := map[Intent]bool{}
	for _, i := range all {
		if seen[i] {
			t.Fatalf("duplicate intent %q", i)
		}
		seen[i] = true
	}
}

func TestExample_AllIntentsReturnsCopy(t *testing.T) {
	all := AllIntents()
	all[0] = Intent("tampered")
	again := AllIntents()
	if again[0] == Intent("tampered") {
		t.Fatal("AllIntents leaks mutable package state")
	}
}

func TestExample_IsValidIntentAcceptsCanonical(t *testing.T) {
	for _, i := range AllIntents() {
		if !IsValidIntent(i) {
			t.Fatalf("IsValidIntent(%q) = false, want true", i)
		}
	}
}

func TestExample_IsValidIntentRejectsGarbage(t *testing.T) {
	for _, bad := range []Intent{"", "WEB", " Web", "sqlmap", "sidecar", "research"} {
		if IsValidIntent(bad) {
			t.Fatalf("IsValidIntent(%q) = true, want false", bad)
		}
	}
}

func TestExample_ParseIntentAcceptsCanonical(t *testing.T) {
	for _, i := range AllIntents() {
		got, err := ParseIntent(string(i))
		if err != nil {
			t.Fatalf("ParseIntent(%q): %v", i, err)
		}
		if got != i {
			t.Fatalf("ParseIntent(%q) = %q, want %q", i, got, i)
		}
	}
}

func TestExample_ParseIntentNormalizes(t *testing.T) {
	got, err := ParseIntent("  CVE ")
	if err != nil {
		t.Fatalf("ParseIntent: %v", err)
	}
	if got != IntentCVE {
		t.Fatalf("got %q, want %q", got, IntentCVE)
	}
	got, err = ParseIntent("Web_Fetch")
	if err != nil {
		t.Fatalf("ParseIntent: %v", err)
	}
	if got != IntentWebFetch {
		t.Fatalf("got %q, want %q", got, IntentWebFetch)
	}
}

func TestExample_ParseIntentRejectsEmpty(t *testing.T) {
	for _, raw := range []string{"", "   ", "\t\n "} {
		_, err := ParseIntent(raw)
		if !errors.Is(err, ErrEmptyIntent) {
			t.Fatalf("ParseIntent(%q) err = %v, want ErrEmptyIntent", raw, err)
		}
	}
}

func TestExample_ParseIntentRejectsUnknown(t *testing.T) {
	_, err := ParseIntent("sidecar")
	if !errors.Is(err, ErrUnknownIntent) {
		t.Fatalf("err = %v, want ErrUnknownIntent", err)
	}
	_, err = ParseIntent("web ")
	if err != nil {
		t.Fatalf("trailing space should normalize, got %v", err)
	}
	_, err = ParseIntent("web-search")
	if !errors.Is(err, ErrUnknownIntent) {
		t.Fatalf("dash form err = %v, want ErrUnknownIntent", err)
	}
}
