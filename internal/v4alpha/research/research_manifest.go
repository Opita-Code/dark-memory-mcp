// This file defines the BackendManifest type: what a v4-native research
// adapter declares about itself (which backend it prefers, where it falls
// back to, and what rate budget each backend gets). The shape mirrors the
// backend manifest example in ARCHITECTURE-V4.md section 4.3. Result
// envelopes live in research_result.go (cycle 3); the adapter registry
// that serves these manifests lives in research_registry.go (cycle 5).
package research

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// Sentinel errors for BackendManifest.Validate. All callers should use
// errors.Is.
var (
	// ErrEmptyManifestName is returned when Name or Version is empty.
	ErrEmptyManifestName = errors.New("research: backend manifest name or version is empty")

	// ErrEmptyManifestCapabilities is returned when Capabilities is empty
	// or holds an empty entry.
	ErrEmptyManifestCapabilities = errors.New("research: backend manifest has no capabilities")

	// ErrManifestBadIntent is returned when Intent is not canonical.
	ErrManifestBadIntent = errors.New("research: backend manifest intent is not canonical")

	// ErrManifestBadChain is returned when FallbackChain is empty, Primary
	// is empty, or Primary names no entry of FallbackChain.
	ErrManifestBadChain = errors.New("research: backend manifest fallback chain is empty or misses primary")

	// ErrManifestBadLimits is returned when TimeoutMs is not positive or
	// any RateLimits entry is malformed. Rate values use "<n>/<window>"
	// (e.g. "5/s", "5/30s"); see RateLimits for the window grammar.
	ErrManifestBadLimits = errors.New("research: backend manifest timeout or rate limit is malformed")
)

// BackendManifest declares one research adapter: the intent it serves,
// the backend it prefers, the ordered backends it falls back to, and the
// per-backend rate budget. HTTP backends land in a later slice; this type
// only describes the plan, it never dials.
type BackendManifest struct {
	// Name is the adapter name, e.g. "research-cve".
	Name string

	// Version is the adapter version, e.g. "4.0.0".
	Version string

	// Capabilities lists the capability grants, e.g. ["research.cve"].
	Capabilities []string

	// Intent is the canonical research intent this adapter serves.
	Intent Intent

	// Primary is the preferred backend, e.g. "osv.dev". It must name an
	// entry of FallbackChain.
	Primary string

	// FallbackChain orders backends from most to least preferred.
	FallbackChain []string

	// RateLimits maps backend name to budget as "<n>/<window>", e.g.
	// {"osv.dev": "5/s", "nvd": "5/30s", "cisa_kev": "60/m"}. The window
	// is a bare unit (s, m, h, meaning one unit) or a Go duration
	// ("30s", "5m"). This mirrors the backend manifest example in
	// ARCHITECTURE-V4.md section 4.3.
	RateLimits map[string]string

	// TimeoutMs bounds a single backend call in milliseconds.
	TimeoutMs int
}

// Validate enforces the structural invariants of a BackendManifest. It
// checks shape only: it never dials a backend and never reads the network.
func (m *BackendManifest) Validate() error {
	if m == nil {
		return ErrEmptyManifestName
	}
	if strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.Version) == "" {
		return ErrEmptyManifestName
	}
	if len(m.Capabilities) == 0 {
		return ErrEmptyManifestCapabilities
	}
	for _, c := range m.Capabilities {
		if strings.TrimSpace(c) == "" {
			return ErrEmptyManifestCapabilities
		}
	}
	if !IsValidIntent(m.Intent) {
		return ErrManifestBadIntent
	}
	if strings.TrimSpace(m.Primary) == "" || len(m.FallbackChain) == 0 {
		return ErrManifestBadChain
	}
	found := false
	for _, b := range m.FallbackChain {
		if strings.TrimSpace(b) == "" {
			return ErrManifestBadChain
		}
		if b == m.Primary {
			found = true
		}
	}
	if !found {
		return ErrManifestBadChain
	}
	if m.TimeoutMs <= 0 {
		return ErrManifestBadLimits
	}
	for backend, spec := range m.RateLimits {
		if strings.TrimSpace(backend) == "" {
			return ErrManifestBadLimits
		}
		if _, _, ok := parseRate(spec); !ok {
			return ErrManifestBadLimits
		}
	}
	return nil
}

// ResolveBackends returns the ordered backend list with Primary first,
// followed by the rest of FallbackChain in declaration order with
// duplicates removed. Callers try each entry in order until one answers.
func (m *BackendManifest) ResolveBackends() []string {
	seen := map[string]bool{m.Primary: true}
	out := []string{m.Primary}
	for _, b := range m.FallbackChain {
		if !seen[b] {
			seen[b] = true
			out = append(out, b)
		}
	}
	return out
}

// parseRate splits a "<n>/<window>" budget into count and window duration.
// The window is a bare unit ("s", "m", or "h", meaning one unit) or a Go
// duration ("30s", "5m"). It reports ok=false when the shape is wrong so
// Validate can map any failure to ErrManifestBadLimits; the boolean keeps
// the contract honest (no detail is produced, so none is swallowed).
func parseRate(spec string) (count int, window time.Duration, ok bool) {
	parts := strings.Split(spec, "/")
	if len(parts) != 2 {
		return 0, 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || n <= 0 {
		return 0, 0, false
	}
	raw := strings.TrimSpace(parts[1])
	switch raw {
	case "s":
		return n, time.Second, true
	case "m":
		return n, time.Minute, true
	case "h":
		return n, time.Hour, true
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, 0, false
	}
	return n, d, true
}
