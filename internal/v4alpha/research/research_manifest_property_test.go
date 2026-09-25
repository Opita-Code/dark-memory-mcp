package research

import (
	"testing"

	"pgregory.net/rapid"
)

func TestProperty_ValidManifestAlwaysValidates(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		m := validManifest()
		m.Name = rapid.StringMatching(`[a-z][a-z0-9-]{2,30}`).Draw(t, "name")
		m.TimeoutMs = rapid.IntRange(1, 300000).Draw(t, "timeout")
		if err := m.Validate(); err != nil {
			t.Fatalf("Validate(%+v): %v", m, err)
		}
	})
}

func TestProperty_PrimaryAlwaysFirst(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		m := validManifest()
		chain := rapid.SliceOfN(
			rapid.StringMatching(`[a-z][a-z0-9_.]{1,20}`),
			1, 5,
		).Draw(t, "chain")
		m.Primary = chain[0]
		m.FallbackChain = chain
		got := m.ResolveBackends()
		if len(got) == 0 || got[0] != m.Primary {
			t.Fatalf("primary %q not first in %v", m.Primary, got)
		}
		seen := map[string]bool{}
		for _, b := range got {
			if seen[b] {
				t.Fatalf("duplicate %q in %v", b, got)
			}
			seen[b] = true
		}
	})
}

func TestProperty_BadRateNeverValidates(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		bad := rapid.OneOf(
			rapid.StringMatching(`[0-9]+`),
			rapid.StringMatching(`[0-9]+/[smhdw]`),
			rapid.Just("0/s"),
			rapid.Just("-1/m"),
			rapid.Just("5/"),
			rapid.Just("/s"),
		).Draw(t, "rate")
		// Filter to values that are actually invalid so the property
		// asserts on the right set: "5/s" style strings drawn from the
		// [smhdw] class with a valid unit must be skipped.
		if _, _, ok := parseRate(bad); ok {
			t.Skip("drawn rate is valid; nothing to assert")
		}
		m := validManifest()
		m.RateLimits = map[string]string{"osv.dev": bad}
		if err := m.Validate(); err == nil {
			t.Fatalf("rate %q validated without error", bad)
		}
	})
}

func TestProperty_EmptyChainNeverValidates(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		m := validManifest()
		n := rapid.IntRange(0, 3).Draw(t, "n")
		m.FallbackChain = make([]string, n)
		for i := range m.FallbackChain {
			m.FallbackChain[i] = "backend-" + string(rune('a'+i))
		}
		m.Primary = "osv.dev"
		if err := m.Validate(); err == nil {
			t.Fatalf("chain %v without primary validated", m.FallbackChain)
		}
	})
}
