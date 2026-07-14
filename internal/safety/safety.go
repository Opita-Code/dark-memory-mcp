// Package safety provides the canary token, payload hashing, and injection-
// marker detection that back the 6 operational invariants from the
// Dark Memory MCP constitution.
//
// The library has NO dependency on the LLM layer; safety is mechanical
// (string/regex operations) and works on any payload.
package safety

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// CanaryToken is a unique secret string. It is embedded in system
// prompts (by external callers — this package does NOT inject into
// prompts itself) and used to detect:
//   (a) System prompt leakage (the LLM should never reveal it).
//   (b) Constitution extraction attempts.
//   (c) Cross-tool contamination.
//
// INV-3 (canary in writes) requires any Save* call to validate that the
// payload does NOT contain the active canary. This is a structural
// defense against the dark-recall v2 plugin auto-injecting content that
// smuggles a canary into a future LLM call.
type CanaryToken string

// NewCanary generates a fresh canary. 128 bits of entropy.
func NewCanary() CanaryToken {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure on supported platforms is catastrophic.
		// Fall back to time-based so we still have a unique-ish value.
		return CanaryToken(fmt.Sprintf("DARK_MEM_CANARY_FALLBACK_%d", unixNanoFallback()))
	}
	return CanaryToken("DARK_MEMORY_CANARY_" + hex.EncodeToString(b[:]))
}

// String returns the canary value. Used to embed it in the system prompt.
func (c CanaryToken) String() string { return string(c) }

// IsZero reports whether the canary is the zero value.
func (c CanaryToken) IsZero() bool { return c == "" }

// Match reports whether payload contains the canary.
func (c CanaryToken) Match(payload string) bool {
	if c.IsZero() {
		return false
	}
	return strings.Contains(payload, string(c))
}

// Holder stores the active canary in a concurrency-safe way. Multiple
// goroutines may read the canary (e.g., validators) while one
// goroutine sets it at startup.
type Holder struct {
	mu  sync.RWMutex
	can CanaryToken
}

// Set installs c as the active canary.
func (h *Holder) Set(c CanaryToken) {
	h.mu.Lock()
	h.can = c
	h.mu.Unlock()
}

// Active returns the active canary (zero value if unset).
func (h *Holder) Active() CanaryToken {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.can
}

// ValidatePayload returns ErrCanaryInPayload if the payload contains the
// active canary. Returns nil if no canary is set or the payload is clean.
func (h *Holder) ValidatePayload(payload string) error {
	c := h.Active()
	if c.IsZero() {
		return nil
	}
	if c.Match(payload) {
		return ErrCanaryInPayload
	}
	return nil
}

// ErrCanaryInPayload is returned by ValidatePayload when the payload
// contains the active canary. Mirrors store.ErrCanaryInPayload — kept
// in this package so safety doesn't import store (which would create
// a cycle: store imports safety for hashing).
var ErrCanaryInPayload = errors.New("safety: payload contains active canary token")

func unixNanoFallback() int64 {
	// Lightweight monotonic-ish value without importing time at the top
	// (the safety package is meant to be lightweight and dependency-free).
	// The fallback path should never run on supported platforms.
	var b [8]byte
	_, _ = rand.Read(b[:])
	var n int64
	for _, v := range b {
		n = n<<8 | int64(v)
	}
	return n
}

// HashPayload returns SHA-256(payload) hex. Used by INV-1 to populate
// write_audit.content_sha256.
func HashPayload(payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// HashBytes returns SHA-256(b) hex.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// injectionMarkers is the regex set used by INV-6 to refuse mod content
// that smells like prompt injection. Same set as dark-research-mcp's
// safety/defense.go but kept here so Dark Memory MCP has no dependency
// on dark-research-mcp.
var injectionMarkers = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bignore (?:all |previous |prior )?instructions?\b`),
	regexp.MustCompile(`(?i)\byou are now (?:DAN|a[^.]{0,30}without (?:rules|restrictions))\b`),
	regexp.MustCompile(`(?i)\b(?:new|updated?) system prompt\b`),
	regexp.MustCompile(`(?i)\bdisregard (?:all |previous )?(?:safety|security|ethical) (?:rules?|guidelines?|policies?)\b`),
	regexp.MustCompile(`(?i)\[SYSTEM\]|<\|im_start\|>system|<\|im_end\|>`),
	regexp.MustCompile(`(?i)\bact as (?:a )?(?:different|other|new) (?:AI|assistant|model)\b`),
	regexp.MustCompile(`(?i)\bforget (?:everything|all|your) (?:above|prior|previous)\b`),
	regexp.MustCompile(`(?i)\boverride (?:safety|refusal|system)\b`),
	regexp.MustCompile(`(?i)\bjailbreak\b`),
	regexp.MustCompile(`(?i)\bpretend (?:to be|you are)\b`),
}

// CheckPayload scans payload for injection markers. Returns the matched
// regex strings (empty slice if clean). The LLM-as-judge may also use
// this for content moderation outside the mod loader.
func CheckPayload(payload string) []string {
	hits := make([]string, 0, 4)
	for _, re := range injectionMarkers {
		if re.MatchString(payload) {
			hits = append(hits, re.String())
		}
	}
	return hits
}
