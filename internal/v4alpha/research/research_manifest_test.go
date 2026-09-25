package research

import (
	"errors"
	"testing"
)

func validManifest() *BackendManifest {
	return &BackendManifest{
		Name:         "research-cve",
		Version:      "4.0.0",
		Capabilities: []string{"research.cve"},
		Intent:       IntentCVE,
		Primary:      "osv.dev",
		FallbackChain: []string{
			"osv.dev",
			"nvd",
			"gh_advisory",
			"cisa_kev",
		},
		RateLimits: map[string]string{
			"osv.dev":  "5/s",
			"nvd":      "300/h",
			"cisa_kev": "60/m",
		},
		TimeoutMs: 30000,
	}
}

func TestExample_ManifestValidateAcceptsValid(t *testing.T) {
	if err := validManifest().Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestExample_ManifestValidateRejectsNil(t *testing.T) {
	var m *BackendManifest
	if err := m.Validate(); !errors.Is(err, ErrEmptyManifestName) {
		t.Fatalf("err = %v, want ErrEmptyManifestName", err)
	}
}

func TestExample_ManifestValidateRejectsEmptyName(t *testing.T) {
	m := validManifest()
	m.Name = "  "
	if err := m.Validate(); !errors.Is(err, ErrEmptyManifestName) {
		t.Fatalf("err = %v, want ErrEmptyManifestName", err)
	}
	m = validManifest()
	m.Version = ""
	if err := m.Validate(); !errors.Is(err, ErrEmptyManifestName) {
		t.Fatalf("err = %v, want ErrEmptyManifestName", err)
	}
}

func TestExample_ManifestValidateRejectsEmptyCaps(t *testing.T) {
	m := validManifest()
	m.Capabilities = nil
	if err := m.Validate(); !errors.Is(err, ErrEmptyManifestCapabilities) {
		t.Fatalf("err = %v, want ErrEmptyManifestCapabilities", err)
	}
	m = validManifest()
	m.Capabilities = []string{"research.cve", ""}
	if err := m.Validate(); !errors.Is(err, ErrEmptyManifestCapabilities) {
		t.Fatalf("err = %v, want ErrEmptyManifestCapabilities", err)
	}
}

func TestExample_ManifestValidateRejectsBadIntent(t *testing.T) {
	m := validManifest()
	m.Intent = Intent("sidecar")
	if err := m.Validate(); !errors.Is(err, ErrManifestBadIntent) {
		t.Fatalf("err = %v, want ErrManifestBadIntent", err)
	}
}

func TestExample_ManifestValidateRejectsBadChain(t *testing.T) {
	m := validManifest()
	m.FallbackChain = nil
	if err := m.Validate(); !errors.Is(err, ErrManifestBadChain) {
		t.Fatalf("empty chain err = %v", err)
	}
	m = validManifest()
	m.Primary = "nvd-mirror"
	if err := m.Validate(); !errors.Is(err, ErrManifestBadChain) {
		t.Fatalf("primary-missing err = %v", err)
	}
	m = validManifest()
	m.Primary = ""
	if err := m.Validate(); !errors.Is(err, ErrManifestBadChain) {
		t.Fatalf("empty primary err = %v", err)
	}
	m = validManifest()
	m.FallbackChain = []string{"osv.dev", ""}
	if err := m.Validate(); !errors.Is(err, ErrManifestBadChain) {
		t.Fatalf("blank entry err = %v", err)
	}
}

func TestExample_ManifestValidateRejectsBadLimits(t *testing.T) {
	m := validManifest()
	m.TimeoutMs = 0
	if err := m.Validate(); !errors.Is(err, ErrManifestBadLimits) {
		t.Fatalf("zero timeout err = %v", err)
	}
	if err := func() error {
		m := validManifest()
		m.RateLimits = map[string]string{"nvd": "5/30s"}
		return m.Validate()
	}(); err != nil {
		t.Fatalf("duration window 5/30s err = %v, want nil", err)
	}
	for _, bad := range map[string]string{
		"no-slash":    "5s",
		"zero-count":  "0/s",
		"neg-count":   "-3/m",
		"bad-unit":    "5/d",
		"zero-window": "5/0s",
		"empty-unit":  "5/",
		"empty-count": "/s",
	} {
		m = validManifest()
		m.RateLimits = map[string]string{"osv.dev": bad}
		if err := m.Validate(); !errors.Is(err, ErrManifestBadLimits) {
			t.Fatalf("%s (%q) err = %v, want ErrManifestBadLimits", bad, bad, err)
		}
	}
	m = validManifest()
	m.RateLimits = map[string]string{"": "5/s"}
	if err := m.Validate(); !errors.Is(err, ErrManifestBadLimits) {
		t.Fatalf("empty backend err = %v", err)
	}
}

func TestExample_ResolveBackendsPrimaryFirst(t *testing.T) {
	m := validManifest()
	got := m.ResolveBackends()
	want := []string{"osv.dev", "nvd", "gh_advisory", "cisa_kev"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestExample_ResolveBackendsDedups(t *testing.T) {
	m := validManifest()
	m.Primary = "nvd"
	m.FallbackChain = []string{"osv.dev", "nvd", "osv.dev", "cisa_kev"}
	got := m.ResolveBackends()
	want := []string{"nvd", "osv.dev", "cisa_kev"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
