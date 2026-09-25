package research

import (
	"errors"
	"testing"
	"time"
)

// manualClock is an injectable Clock for deterministic bucket tests.
type manualClock struct {
	now time.Time
}

func (c *manualClock) clock() Clock {
	return func() time.Time { return c.now }
}

func (c *manualClock) advance(d time.Duration) {
	c.now = c.now.Add(d)
}

func testManifestFor(intent Intent, primary string, chain ...string) BackendManifest {
	return BackendManifest{
		Name:          "research-" + string(intent),
		Version:       "4.0.0",
		Capabilities:  []string{"research." + string(intent)},
		Intent:        intent,
		Primary:       primary,
		FallbackChain: chain,
		RateLimits:    map[string]string{primary: "5/s"},
		TimeoutMs:     1000,
	}
}

func TestExample_RegisterGetRoundTrip(t *testing.T) {
	r := NewRegistry(nil)
	m := testManifestFor(IntentCVE, "osv.dev", "osv.dev", "nvd")
	if err := r.Register(m); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := r.Get(IntentCVE)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != m.Name || got.Primary != m.Primary {
		t.Fatalf("got %+v, want %+v", got, m)
	}
}

func TestExample_RegisterRejectsInvalid(t *testing.T) {
	r := NewRegistry(nil)
	m := testManifestFor(IntentCVE, "osv.dev", "osv.dev")
	m.TimeoutMs = 0
	if err := r.Register(m); !errors.Is(err, ErrManifestBadLimits) {
		t.Fatalf("err = %v, want ErrManifestBadLimits", err)
	}
}

func TestExample_RegisterRejectsDuplicate(t *testing.T) {
	r := NewRegistry(nil)
	m := testManifestFor(IntentCVE, "osv.dev", "osv.dev")
	if err := r.Register(m); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := r.Register(m); !errors.Is(err, ErrRegistryDuplicate) {
		t.Fatalf("err = %v, want ErrRegistryDuplicate", err)
	}
}

func TestExample_GetUnknownReturnsErr(t *testing.T) {
	r := NewRegistry(nil)
	if _, err := r.Get(IntentCVE); !errors.Is(err, ErrRegistryUnknown) {
		t.Fatalf("err = %v, want ErrRegistryUnknown", err)
	}
	if _, err := r.Get(Intent("nope")); !errors.Is(err, ErrRegistryUnknown) {
		t.Fatalf("err = %v, want ErrRegistryUnknown", err)
	}
}

func TestExample_ListOrdered(t *testing.T) {
	r := NewRegistry(nil)
	for _, i := range []Intent{IntentWeb, IntentCVE, IntentAcademic} {
		if err := r.Register(testManifestFor(i, "b", "b")); err != nil {
			t.Fatalf("Register %v: %v", i, err)
		}
	}
	list := r.List()
	if len(list) != 3 {
		t.Fatalf("len = %d, want 3", len(list))
	}
	if list[0].Intent != IntentAcademic || list[1].Intent != IntentCVE || list[2].Intent != IntentWeb {
		t.Fatalf("unordered: %v %v %v", list[0].Intent, list[1].Intent, list[2].Intent)
	}
}

func TestExample_ResolveBackendsUnknown(t *testing.T) {
	r := NewRegistry(nil)
	if _, err := r.ResolveBackends(IntentCVE); !errors.Is(err, ErrRegistryUnknown) {
		t.Fatalf("err = %v", err)
	}
	m := testManifestFor(IntentCVE, "osv.dev", "osv.dev", "nvd")
	if err := r.Register(m); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := r.ResolveBackends(IntentCVE)
	if err != nil || len(got) != 2 || got[0] != "osv.dev" {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestExample_NewBucketRejectsBadConfig(t *testing.T) {
	if _, err := NewBucket(0, 1, nil); !errors.Is(err, ErrBucketBadConfig) {
		t.Fatalf("err = %v", err)
	}
	if _, err := NewBucket(5, 0, nil); !errors.Is(err, ErrBucketBadConfig) {
		t.Fatalf("err = %v", err)
	}
}

func TestExample_BucketExhaustRefill(t *testing.T) {
	c := &manualClock{now: time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC)}
	b, err := NewBucket(2, 1, c.clock())
	if err != nil {
		t.Fatalf("NewBucket: %v", err)
	}
	if !b.Allow() || !b.Allow() {
		t.Fatal("burst of 2 denied")
	}
	if b.Allow() {
		t.Fatal("third call allowed without refill")
	}
	c.advance(3 * time.Second)
	if !b.Allow() {
		t.Fatal("refilled call denied")
	}
	if b.Tokens() != 1 {
		t.Fatalf("tokens = %d, want 1", b.Tokens())
	}
}

func TestExample_BucketCapsAtCapacity(t *testing.T) {
	c := &manualClock{now: time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC)}
	b, err := NewBucket(2, 100, c.clock())
	if err != nil {
		t.Fatalf("NewBucket: %v", err)
	}
	c.advance(time.Hour)
	if !b.Allow() || !b.Allow() {
		t.Fatal("burst denied after long idle")
	}
	if b.Allow() {
		t.Fatal("over-capacity call allowed")
	}
}

func TestExample_ConfigureBucketBadSpec(t *testing.T) {
	r := NewRegistry(nil)
	if err := r.ConfigureBucket("osv.dev", "5/d"); !errors.Is(err, ErrRegistryBadRate) {
		t.Fatalf("err = %v", err)
	}
	if err := r.ConfigureBucket("", "5/s"); !errors.Is(err, ErrRegistryBadRate) {
		t.Fatalf("err = %v", err)
	}
}

func TestExample_AllowUnknownBackend(t *testing.T) {
	r := NewRegistry(nil)
	if _, err := r.Allow("osv.dev"); !errors.Is(err, ErrRegistryUnknown) {
		t.Fatalf("err = %v", err)
	}
}

func TestExample_AllowEnforcesBudget(t *testing.T) {
	c := &manualClock{now: time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC)}
	r := NewRegistry(c.clock())
	if err := r.ConfigureBucket("osv.dev", "2/s"); err != nil {
		t.Fatalf("ConfigureBucket: %v", err)
	}
	ok, err := r.Allow("osv.dev")
	if err != nil || !ok {
		t.Fatalf("first allow = %v, %v", ok, err)
	}
	ok, err = r.Allow("osv.dev")
	if err != nil || !ok {
		t.Fatalf("second allow = %v, %v", ok, err)
	}
	ok, err = r.Allow("osv.dev")
	if err != nil || ok {
		t.Fatalf("third allow = %v, %v; want denied", ok, err)
	}
}
