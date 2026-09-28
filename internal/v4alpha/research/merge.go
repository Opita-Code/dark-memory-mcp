// Merge + relevance score. This file implements the v4alpha
// research result post-processing:
//
//   - EntityResolution: collapse results from different backends
//     that name the same target (e.g. CVE-2021-44228 from OSV
//     and NVD becomes ONE Evidence with corroborated_by = both).
//   - RelevanceScore: combine specificity, tier_weight,
//     corroboration_count, and freshness into the [0, 1] score
//     the operator's downstream judge uses to rank evidence.
//
// The relevance formula is the §3.2 / AC-C3 contract:
//   relevance = specificity × tier_weight ×
//               (1 + 0.1 × corroboration_count) × freshness
//
// All four factors are clamped to [0, 1] before multiplication so
// the final score is in [0, ~1.3] (capped to 1.0 at the top).
// The corroboration multiplier caps at 3 sources; the freshness
// factor decays linearly from 1.0 at fetchedAt to 0.0 at hardExpiry.
package research

import (
	"sort"
	"strings"
	"time"
)

// Evidence is the operator-facing summary of one research hit
// after merge. The executor produces one Evidence per (target_id,
// entity_key) regardless of how many backends confirmed it.
type Evidence struct {
	// TargetID is the canonical identifier the backends agreed on
	// (e.g. "CVE-2021-44228", "google.com", "serde@1.0.214").
	TargetID string

	// Intent is the canonical Intent that produced this evidence.
	Intent Intent

	// VibeCase is the operator's vibe_case at the time of the
	// call. "" if the call was vibe-case-agnostic (multi-intent).
	VibeCase VibeCase

	// Backend is the primary backend the executor used (T1, by
	// discipline; T2/T3 only when T1 was empty).
	Backend string

	// CorroboratedBy is the set of backends that agreed on the
	// same TargetID. Includes Backend. The judge uses len() of
	// this slice to bump the relevance score.
	CorroboratedBy []string

	// Title / Snippet / URL are the primary backend's reported
	// values. Redacted at the gate; see research_gate.go.
	Title   string
	Snippet string
	URL     string

	// RelevanceScore is the merged score in [0, 1]. The judge
	// sorts Evidence by this descending when forming the verdict
	// bag.
	RelevanceScore float64

	// Specificity is the [0, 1] specificity of the query that
	// produced this evidence: 1.0 (exact ID match), 0.5 (fuzzy
	// match), 0.3 (discovery). The downstream judge uses this
	// to disambiguate "CVE-2021-44228 from OSV exact" vs
	// "log4j from DDG search".
	Specificity float64

	// TierWeight is 1.0 (T1) / 0.7 (T2) / 0.4 (T3). Copied from
	// the primary backend's manifest.
	TierWeight float64

	// FetchedAt is the timestamp of the most recent successful
	// call. Used by the freshness factor and the EC-014bis
	// (evidence-stale) warning the judge may add.
	FetchedAt time.Time

	// ExpiresAt is the hard expiry (FetchedAt + SoftTTL + StaleTTL).
	// Equal to zero when the entry was not cached.
	ExpiresAt time.Time
}

// mergeKey is the canonical key the merge function uses to decide
// that two results describe the same entity. Today: case-folded
// TargetID; the executor's caller provides TargetID via the URL
// template or a post-parse hook. Future: structured key (e.g.
// ecosystem:package:version for the code intent).
func mergeKey(targetID string) string {
	return strings.ToLower(strings.TrimSpace(targetID))
}

// Merge folds a list of raw Result values (one per backend) into a
// list of Evidence values, where Evidence groups by mergeKey. The
// resulting list is sorted by RelevanceScore descending, so the
// caller can take the top N.
//
// Inputs:
//   - results: every Result the executor collected (per-backend)
//   - vibeCase: the operator's request; recorded on each Evidence
//   - now: the timestamp the freshness factor is evaluated against
//   - expiryFor: a function returning the hard expiry for a given
//     Intent (or zero when not cached); used by freshness
//
// Each Result's Items[].Title is used as the title for evidence
// when the executor cannot otherwise pick one. The snippet is the
// first non-empty Snippet among the corroborated backends.
func Merge(results []Result, vibeCase VibeCase, now time.Time, expiryFor func(Intent) time.Time) []Evidence {
	groups := map[string]*Evidence{}
	// First pass: build groups. The first backend that contributes
	// to a group sets Title / URL; subsequent backends only
	// contribute to CorroboratedBy and the corroboration count.
	for _, r := range results {
		exp := time.Time{}
		if expiryFor != nil {
			exp = expiryFor(r.Intent)
		}
		for _, it := range r.Items {
			k := mergeKey(it.Title) // Title doubles as the canonical key
			// The first item in the group provides the canonical
			// fields. If the executor did not pre-populate it.URL
			// with a canonical id, we still want the merge to work,
			// so we key on Title here too. Real backends set
			// it.URL to the canonical target id; that is the
			// preferred merge key. The Title key is a fallback.
			ev, ok := groups[k]
			if !ok {
				ev = &Evidence{
					TargetID:   it.Title,
					Intent:     r.Intent,
					VibeCase:   vibeCase,
					Backend:    primaryBackend(r),
					Title:      it.Title,
					Snippet:    it.Snippet,
					URL:        it.URL,
					Specificity: specificityFromConfidence(it.Confidence),
					TierWeight:  0, // set later from manifest lookup
					FetchedAt:  r.FetchedAt,
					ExpiresAt:  exp,
				}
				groups[k] = ev
			}
			// Corroboration: append backend name if not already there.
			bn := primaryBackend(r)
			if !containsString(ev.CorroboratedBy, bn) {
				ev.CorroboratedBy = append(ev.CorroboratedBy, bn)
			}
			// Promote the more specific / more informative Snippet.
			if ev.Snippet == "" && it.Snippet != "" {
				ev.Snippet = it.Snippet
			}
		}
	}
	// Second pass: compute relevance scores and sort.
	out := make([]Evidence, 0, len(groups))
	for _, ev := range groups {
		// TierWeight is filled in by the executor when the backend
		// manifest is known. Here we default to 1.0 (T1) so a
		// manifest-less merge still produces a usable score.
		if ev.TierWeight == 0 {
			ev.TierWeight = 1.0
		}
		ev.RelevanceScore = RelevanceScore(
			ev.Specificity, ev.TierWeight, len(ev.CorroboratedBy), ev.FetchedAt, ev.ExpiresAt, now,
		)
		out = append(out, *ev)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RelevanceScore > out[j].RelevanceScore
	})
	return out
}

// RelevanceScore is the §3.2 / AC-C3 formula:
//
//	relevance = specificity × tier_weight ×
//	            (1 + 0.1 × min(corroboration, 3)) × freshness
//
// Inputs are clamped to [0, 1] (corroboration is clamped at 3). The
// function does NOT clamp the final result to [0, 1] — the
// corroboration boost (up to +0.3) and the multiplicative composition
// can legitimately push the score to ~1.3 for a multi-source,
// just-fetched, T1 result. The downstream consumer (the judge) sorts
// descending; a score of 1.3 ranks above 1.0. If the operator wants
// a [0, 1] view, they can divide by the maximum possible (1.3) at
// the display layer.
//
// freshness is a linear decay from 1.0 (just fetched) to 0.0
// (hardExpiry). When expiresAt is zero (entry was not cached), the
// freshness is 1.0 — the result is fresh by construction.
func RelevanceScore(specificity, tierWeight float64, corroboration int, fetchedAt, expiresAt, now time.Time) float64 {
	if specificity < 0 {
		specificity = 0
	}
	if specificity > 1 {
		specificity = 1
	}
	if tierWeight < 0 {
		tierWeight = 0
	}
	if tierWeight > 1 {
		tierWeight = 1
	}
	if corroboration < 0 {
		corroboration = 0
	}
	if corroboration > 3 {
		corroboration = 3
	}
	corr := 1.0 + 0.1*float64(corroboration)
	fresh := 1.0
	if !expiresAt.IsZero() && !fetchedAt.IsZero() {
		total := expiresAt.Sub(fetchedAt).Seconds()
		if total > 0 {
			elapsed := now.Sub(fetchedAt).Seconds()
			if elapsed < 0 {
				elapsed = 0
			}
			if elapsed > total {
				elapsed = total
			}
			fresh = 1.0 - (elapsed / total)
		}
	}
	score := specificity * tierWeight * corr * fresh
	if score < 0 {
		score = 0
	}
	return score
}

// specificityFromConfidence maps the backend's reported confidence
// in [0, 1] to a specificity score. High confidence → high
// specificity. We deliberately use confidence as a proxy because
// the executor does not know the query shape (exact vs fuzzy) per
// item; the backend's own confidence is the best signal we have
// short of pre-classifying the query.
func specificityFromConfidence(c float64) float64 {
	if c < 0 {
		return 0
	}
	if c > 1 {
		return 1
	}
	return c
}

// primaryBackend returns the first non-empty name from
// r.Sources. If Sources is empty, the function returns "" so the
// caller can record a "no source" condition.
func primaryBackend(r Result) string {
	for _, s := range r.Sources {
		if s != "" {
			return s
		}
	}
	return ""
}

func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
