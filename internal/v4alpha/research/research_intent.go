// Package research provides v4-native research adapters for dark-memory
// v4.0 (M4 bedrock): the 18 capabilities of the v3 research server become
// native tools under internal/v4alpha/research/, each declaring a backend
// manifest with a fallback chain (see ARCHITECTURE-V4.md sections 2.4.2
// and 4). This slice covers intents, manifests, results, the output gate,
// and the adapter registry. No network calls live here: adapters declare
// manifests only; HTTP backends land in a later slice.
//
// This file defines the Intent taxonomy: the canonical set of research
// intents every adapter, manifest, result, and gate call names. The
// backend manifest layer lives in research_manifest.go (cycle 2).
package research

import (
	"errors"
	"strings"
)

// Sentinel errors for Intent parsing. All callers should use errors.Is.
var (
	// ErrEmptyIntent is returned when the raw intent string is empty
	// or whitespace-only.
	ErrEmptyIntent = errors.New("research: intent is empty")

	// ErrUnknownIntent is returned when the raw string names no
	// canonical intent.
	ErrUnknownIntent = errors.New("research: unknown intent")
)

// Intent names one of the 18 canonical v4 research capabilities.
// The zero value ("") is never valid; use ParseIntent to build one
// from operator input.
type Intent string

// Canonical v4 research intents (ARCHITECTURE-V4.md section 2.4.2).
// The first 13 are intent backends with a fallback chain; multi fans
// out across backends; the last 4 are standalone tools.
const (
	IntentWeb                  Intent = "web"
	IntentAcademic             Intent = "academic"
	IntentCode                 Intent = "code"
	IntentCVE                  Intent = "cve"
	IntentDomain               Intent = "domain"
	IntentDNS                  Intent = "dns"
	IntentCert                 Intent = "cert"
	IntentIP                   Intent = "ip"
	IntentThreat               Intent = "threat"
	IntentEmail                Intent = "email"
	IntentDark                 Intent = "dark"
	IntentGeo                  Intent = "geo"
	IntentNews                 Intent = "news"
	IntentMulti                Intent = "multi"
	IntentWebSearch            Intent = "web_search"
	IntentWebFetch             Intent = "web_fetch"
	IntentURLExtractComponents Intent = "url_extract_components"
	IntentTextAnonymize        Intent = "text_anonymize"
)

// canonicalIntents lists every valid Intent in declaration order.
var canonicalIntents = []Intent{
	IntentWeb,
	IntentAcademic,
	IntentCode,
	IntentCVE,
	IntentDomain,
	IntentDNS,
	IntentCert,
	IntentIP,
	IntentThreat,
	IntentEmail,
	IntentDark,
	IntentGeo,
	IntentNews,
	IntentMulti,
	IntentWebSearch,
	IntentWebFetch,
	IntentURLExtractComponents,
	IntentTextAnonymize,
}

// AllIntents returns the 18 canonical intents in declaration order.
// The returned slice is a copy; mutating it does not affect the package.
func AllIntents() []Intent {
	out := make([]Intent, len(canonicalIntents))
	copy(out, canonicalIntents)
	return out
}

// IsValidIntent reports whether i is one of the 18 canonical intents.
func IsValidIntent(i Intent) bool {
	for _, c := range canonicalIntents {
		if i == c {
			return true
		}
	}
	return false
}

// ParseIntent normalizes raw operator input (trims surrounding whitespace,
// lowercases) and maps it to a canonical Intent. It returns ErrEmptyIntent
// for empty input and ErrUnknownIntent for anything outside the canonical
// set of 18.
func ParseIntent(raw string) (Intent, error) {
	norm := Intent(strings.ToLower(strings.TrimSpace(raw)))
	if norm == "" {
		return "", ErrEmptyIntent
	}
	if !IsValidIntent(norm) {
		return "", ErrUnknownIntent
	}
	return norm, nil
}
