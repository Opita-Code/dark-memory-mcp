package research

import (
	"testing"
	"time"

	"pgregory.net/rapid"
)

func TestProperty_RegisteredAlwaysGettable(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		r := NewRegistry(nil)
		i := rapid.SampledFrom(AllIntents()).Draw(t, "intent")
		m := testManifestFor(i, "b", "b")
		if err := r.Register(m); err != nil {
			t.Fatalf("Register: %v", err)
		}
		got, err := r.Get(i)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Intent != i {
			t.Fatalf("got %q want %q", got.Intent, i)
		}
	})
}

func TestProperty_DuplicateAlwaysRejected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		r := NewRegistry(nil)
		i := rapid.SampledFrom(AllIntents()).Draw(t, "intent")
		m := testManifestFor(i, "b", "b")
		if err := r.Register(m); err != nil {
			t.Fatalf("first Register: %v", err)
		}
		if err := r.Register(m); err == nil {
			t.Fatalf("duplicate %q accepted", i)
		}
	})
}

func TestProperty_BucketNeverExceedsCapacity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		c := &manualClock{now: time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC)}
		capacity := rapid.IntRange(1, 10).Draw(t, "capacity")
		b, err := NewBucket(capacity, 1000, c.clock())
		if err != nil {
			t.Fatalf("NewBucket: %v", err)
		}
		c.advance(time.Duration(rapid.IntRange(0, 3600).Draw(t, "idle")) * time.Second)
		allowed := 0
		for j := 0; j < capacity+5; j++ {
			if b.Allow() {
				allowed++
			}
		}
		if allowed > capacity {
			t.Fatalf("allowed %d over capacity %d", allowed, capacity)
		}
	})
}

func TestProperty_RefillIsMonotonic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		c := &manualClock{now: time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC)}
		b, err := NewBucket(5, 1, c.clock())
		if err != nil {
			t.Fatalf("NewBucket: %v", err)
		}
		for b.Allow() {
		}
		before := b.Tokens()
		c.advance(time.Duration(rapid.IntRange(1, 60).Draw(t, "wait")) * time.Second)
		b.Allow()
		if after := b.Tokens(); after < before {
			t.Fatalf("tokens went backwards: %d -> %d", before, after)
		}
	})
}

func TestProperty_DurationWindowBudgetHonored(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		c := &manualClock{now: time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC)}
		r := NewRegistry(c.clock())
		n := rapid.IntRange(1, 8).Draw(t, "n")
		spec := "5/30s"
		if rapid.Bool().Draw(t, "use-n") {
			spec = string(rune('0'+n)) + "/30s"
		} else {
			n = 5
		}
		if err := r.ConfigureBucket("b", spec); err != nil {
			t.Fatalf("ConfigureBucket(%q): %v", spec, err)
		}
		allowed := 0
		for j := 0; j < n+3; j++ {
			ok, err := r.Allow("b")
			if err != nil {
				t.Fatalf("Allow: %v", err)
			}
			if ok {
				allowed++
			}
		}
		if allowed != n {
			t.Fatalf("allowed %d, budget %d (%q)", allowed, n, spec)
		}
	})
}
