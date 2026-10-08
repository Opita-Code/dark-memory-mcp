// Package vibeflow provides the ambient workflow gating logic for the
// C8 vibe-flow mode (vibe-loop-git Loop 7+).
//
// # Layer 1 — Heuristic upfront (Loop 8 / L8.1a)
//
// This package implements the Layer 1 heuristic: a deterministic,
// zero-LLM-call classifier that predicts task length from the operator's
// first message. The classifier runs in <1ms and returns one of
// {ClassShort, ClassMedium, ClassLong} with a confidence in [0, 1].
//
// # Theory
//
// The classifier follows the "fast and frugal" heuristic pattern
// (Gigerenzer, Todd, & ABC Research Group 1999, "Simple Heuristics That
// Make Us Smart") applied to AI agents: a few high-signal features with
// per-vibe_case thresholds, no probabilistic machinery. The deeper
// theoretical frame is Horvitz 1990 (anytime algorithms): produce a
// "best so far" answer at t=0, refine as more evidence arrives. Layer 1
// is the t=0 answer; Layer 2 (L8.2 lazy activation) refines it as the
// session progresses.
//
// # Per-vibe_case calibration
//
// Thresholds vary by vibe_case because the 10-persona analysis
// (core/loop-8-design.md §Validation summary) showed that C1 code has
// many short fixes, C4 research is almost never short, etc. Defaults
// live in DefaultThresholds and can be overridden per spec (deferred
// to L8.1b).
//
// # What this package is NOT
//
// This is a *first-pass* classifier. It does not call any LLM. It does
// not learn from past sessions. It does not detect language, device,
// or operator persona-tier (those are L8.1b). It is intentionally
// simple so it costs ~0 and fails open.
package vibeflow

import (
	"regexp"
	"strings"
)

// Class is the predicted task length for a session.
type Class string

const (
	// ClassShort — task likely <5min. Heuristic suggests minimal
	// vibe-flow intervention; Layer 2 will keep the session in
	// DEGRADED mode unless escalation signals fire.
	ClassShort Class = "short"

	// ClassMedium — task likely 5-30min. Default state; Layer 2
	// escalates on signals.
	ClassMedium Class = "medium"

	// ClassLong — task likely >30min. Heuristic suggests full
	// vibe-flow from the start; Layer 2 still tracks budget.
	ClassLong Class = "long"
)

// Features is the structured output of feature extraction. Exposed so
// callers can debug classification decisions (e.g. "why was this
// classified short?") and so future L8.1b work can add new features
// without breaking the public API.
type Features struct {
	// TokenCount is the whitespace-delimited word count. Used as a
	// coarse proxy for message length.
	TokenCount int

	// ShortKeywords are the short-signal keywords found in the
	// message ("just", "quickly", "typo", etc.). Empty if none.
	ShortKeywords []string

	// LongKeywords are the long-signal keywords found in the
	// message ("refactor", "design", "investigate", etc.). Empty if
	// none.
	LongKeywords []string

	// MultiStep is true if the message shows multi-step structure
	// (numbered list, "and then", "after that", "first... then").
	MultiStep bool

	// Imperative is true if the message looks like a directive
	// (no question mark, doesn't start with a question word). A
	// weak signal — operators often phrase requests as questions
	// ("can you refactor...").
	Imperative bool
}

// Thresholds defines the per-vibe_case scoring thresholds.
// `Short` is the minimum short_score to classify as SHORT.
// `Long` is the minimum long_score to classify as LONG.
// If both pass, the higher score wins; otherwise MEDIUM.
type Thresholds struct {
	Short float64
	Long  float64
}

// DefaultThresholds is the per-vibe_case threshold map. Per Loop 8
// design doc §"Per-vibe_case thresholds (L8.1a)".
//
// Calibration rationale (from 10-persona analysis + L8.1a tuning):
//
//	C1 code: low short threshold (many 1-function fixes),
//	         low long threshold (1 long keyword + multi-step passes,
//	         but "refactor" alone doesn't, since it can be 1 function).
//	C2 text: default (50/50 short/long).
//	C3 decision: very high short threshold (decisions are serious work),
//	             low long threshold (almost always long).
//	C4 research: very high short threshold (research is deep),
//	            very low long threshold (any research keyword = long,
//	            even with "just" or "quick" present).
//	C5-C6: default.
//	C7 multi: high short threshold, low long threshold.
//	C8 vibe-flow: default (meta-case).
//
// The "if only one passes, that one wins" logic (see Score) makes the
// per-vibe_case calibration matter: a high short threshold on C4
// means short_keywords don't override a single long_keyword.
var DefaultThresholds = map[string]Thresholds{
	"C1": {Short: 1.5, Long: 2.0},
	"C2": {Short: 3.0, Long: 2.0},
	"C3": {Short: 4.5, Long: 1.5},
	"C4": {Short: 5.0, Long: 1.5},
	"C5": {Short: 3.0, Long: 2.0},
	"C6": {Short: 3.0, Long: 2.0},
	"C7": {Short: 4.0, Long: 1.5},
	"C8": {Short: 3.0, Long: 2.0},
}

// DefaultThresholdsFallback is used when a vibe_case is not in
// DefaultThresholds. Same as C1 (code, the most common case in
// real usage) so that an unknown case behaves like a typical code
// task.
var DefaultThresholdsFallback = Thresholds{Short: 1.5, Long: 2.0}

// Short-signal keyword regexes. Compiled once at package init.
var shortKeywordRegexes = []*regexp.Regexp{
	regexp.MustCompile(`\bjust\b`),
	regexp.MustCompile(`\bquickly\b`),
	regexp.MustCompile(`\btypo\b`),
	regexp.MustCompile(`\bsmall\b`),
	regexp.MustCompile(`\bsingle\b`),
	regexp.MustCompile(`\bminor\b`),
	regexp.MustCompile(`\b1 line\b`),
	regexp.MustCompile(`\bone line\b`),
	regexp.MustCompile(`\bquick\b`),
	regexp.MustCompile(`\bbrief\b`),
	regexp.MustCompile(`\bsimple\b`),
}

// Long-signal keyword regexes. Compiled once at package init.
//
// The set is intentionally broad: it must catch C4 research signals
// ("papers", "literature", "summarize"), C1 design signals
// ("architecture", "comprehensive", "entire"), and operational signals
// ("investigate", "fix" patterns).
var longKeywordRegexes = []*regexp.Regexp{
	// C1 design / refactor
	regexp.MustCompile(`\brefactor\b`),
	regexp.MustCompile(`\bmigrate\b`),
	regexp.MustCompile(`\bmigration\b`),
	regexp.MustCompile(`\bdesign\b`),
	regexp.MustCompile(`\bimplement\b`),
	regexp.MustCompile(`\barchitect\b`),
	regexp.MustCompile(`\barchitecture\b`),
	regexp.MustCompile(`\bbuild\b`),
	regexp.MustCompile(`\bcreate\b`),
	regexp.MustCompile(`\bcomprehensive\b`),
	regexp.MustCompile(`\bthorough\b`),
	regexp.MustCompile(`\bcomplete\b`),
	regexp.MustCompile(`\bfull\b`),
	regexp.MustCompile(`\bentire\b`),
	regexp.MustCompile(`\bwhole\b`),
	regexp.MustCompile(`\bsystem\b`),
	regexp.MustCompile(`\bmodule\b`),
	regexp.MustCompile(`\bmodule-wide\b`),
	regexp.MustCompile(`\bcodebase\b`),
	regexp.MustCompile(`\bfrom scratch\b`),

	// C4 research / investigation
	regexp.MustCompile(`\bresearch\b`),
	regexp.MustCompile(`\binvestigate\b`),
	regexp.MustCompile(`\binvestigation\b`),
	regexp.MustCompile(`\bstudy\b`),
	regexp.MustCompile(`\bpapers\b`),
	regexp.MustCompile(`\bliterature\b`),
	regexp.MustCompile(`\bexplore\b`),
	regexp.MustCompile(`\bexploration\b`),
	regexp.MustCompile(`\bdiscover\b`),
	regexp.MustCompile(`\bsummarize\b`),
	regexp.MustCompile(`\bsummary\b`),
	regexp.MustCompile(`\bexplain\b`),
	regexp.MustCompile(`\banalyze\b`),
	regexp.MustCompile(`\banalysis\b`),
	regexp.MustCompile(`\btrade-off\b`),
	regexp.MustCompile(`\btradeoff\b`),
	regexp.MustCompile(`\btrade off\b`),
	regexp.MustCompile(`\bstate of the art\b`),
	regexp.MustCompile(`\bSOTA\b`),
	regexp.MustCompile(`\bsurvey\b`),
	regexp.MustCompile(`\bcomprehensive analysis\b`),

	// Multi-artifact / large-scope
	regexp.MustCompile(`\blanding page\b`),
	regexp.MustCompile(`\bcampaign\b`),
	regexp.MustCompile(`\bsignup flow\b`),
	regexp.MustCompile(`\bcheckout flow\b`),
	regexp.MustCompile(`\bapi layer\b`),
	regexp.MustCompile(`\bauth subsystem\b`),
	regexp.MustCompile(`\bauth\b`),
	regexp.MustCompile(`\btenants?\b`),
	regexp.MustCompile(`\bsessions?\b`),
}

// multiStepRegex detects multi-step structure in a message.
//
// Matches:
//   - "and then", "and after", "after that"
//   - "and <verb>" (propose, suggest, implement, build, test, verify, analyze)
//   - "first... then" / "firstly" / "secondly" / "finally"
//   - "step 1" / "step N"
//   - numbered list ("1. do X")
//   - comma-separated list of actions ("X, Y, Z") with 3+ items
var multiStepRegex = regexp.MustCompile(
	`(?i)(` +
		`\band then\b` + `|` +
		`\band after\b` + `|` +
		`\bafter that\b` + `|` +
		`\band (propose|suggest|implement|build|create|test|verify|analyze|investigate|design|refactor|migrate)\b` + `|` +
		`\bfirst\b.*\bthen\b` + `|` +
		`\bfirstly\b` + `|` +
		`\bsecondly\b` + `|` +
		`\bfinally\b` + `|` +
		`\bstep \d+\b` + `|` +
		`\d+\.\s+\w+` + // numbered list: "1. do X"
		`)`,
)

// questionWordRegex matches the start of a question-form message.
var questionWordRegex = regexp.MustCompile(
	`^\s*(what|how|why|when|where|who|which|can|could|would|should|is|are|do|does|did|will|may|might)\b`,
)

// Classify returns the predicted class and confidence for a message.
// It is the public entry point of the heuristic upfront (Layer 1).
//
// `message` is the operator's first message in the session.
// `vibeCase` is the active C1..C8 vibe_case for the project.
// Unknown vibe_cases fall back to DefaultThresholdsFallback.
//
// Returns (class, confidence, features). Confidence is in [0, 1] and
// reflects how decisively the scores separated.
func Classify(message string, vibeCase string) (Class, float64, Features) {
	feats := ExtractFeatures(message)
	t, ok := DefaultThresholds[vibeCase]
	if !ok {
		t = DefaultThresholdsFallback
	}
	class := Score(feats, t)
	conf := confidenceFromFeatures(feats, t, class)
	return class, conf, feats
}

// Score applies the threshold logic to features. Exposed for
// unit-testability and for L8.1b overrides.
//
// Logic:
//  1. Compute short_score and long_score from the features.
//  2. shortPasses := shortScore >= t.Short
//  3. longPasses  := longScore  >= t.Long
//  4. If only one passes, that class wins (no score comparison needed).
//  5. If both pass, the higher score wins (tie-break: long).
//  6. If neither passes, return ClassMedium.
//
// This logic is critical for the per-vibe_case calibration to work.
// For C4, short_threshold is high (5.0) so short_keywords rarely pass
// even when present; meanwhile a single long_keyword crosses the low
// long_threshold (1.5). This means "just research on X" classifies
// as long for C4, not short.
func Score(feats Features, t Thresholds) Class {
	shortScore := shortScoreFromFeatures(feats)
	longScore := longScoreFromFeatures(feats)

	shortPasses := shortScore >= t.Short
	longPasses := longScore >= t.Long

	switch {
	case shortPasses && !longPasses:
		return ClassShort
	case longPasses && !shortPasses:
		return ClassLong
	case shortPasses && longPasses:
		if shortScore > longScore {
			return ClassShort
		}
		return ClassLong
	default:
		return ClassMedium
	}
}

// ExtractFeatures extracts the 5 features from a message. Exposed for
// testability and for downstream consumers (L8.1b, debug logs).
func ExtractFeatures(message string) Features {
	msg := strings.ToLower(message)
	return Features{
		TokenCount:    tokenCount(message),
		ShortKeywords: matchKeywords(msg, shortKeywordRegexes),
		LongKeywords:  matchKeywords(msg, longKeywordRegexes),
		MultiStep:     multiStepRegex.MatchString(msg),
		Imperative:    isImperative(message),
	}
}

// tokenCount counts whitespace-delimited tokens. Rough approximation
// of LLM-token count (which would be ~75% of this for English).
func tokenCount(message string) int {
	fields := strings.Fields(message)
	return len(fields)
}

// matchKeywords returns the unique keywords that match in the message,
// in the order they appear in the text (left-to-right). Lowercase
// comparison is done by the caller.
func matchKeywords(msgLower string, regexes []*regexp.Regexp) []string {
	type hit struct {
		pos  int
		text string
	}
	var hits []hit
	for _, re := range regexes {
		loc := re.FindStringIndex(msgLower)
		if loc == nil {
			continue
		}
		hits = append(hits, hit{pos: loc[0], text: msgLower[loc[0]:loc[1]]})
	}
	// Sort by position (stable: preserves regex order for ties).
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j-1].pos > hits[j].pos; j-- {
			hits[j-1], hits[j] = hits[j], hits[j-1]
		}
	}
	// Dedupe by text (keep first occurrence).
	seen := make(map[string]bool)
	var out []string
	for _, h := range hits {
		if !seen[h.text] {
			seen[h.text] = true
			out = append(out, h.text)
		}
	}
	return out
}

// isImperative returns true if the message is shaped like a directive:
// no question mark AND does not start with a question word. This is a
// weak signal but it's a low-weight feature; it just nudges the score.
func isImperative(message string) bool {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return false
	}
	if strings.HasSuffix(trimmed, "?") {
		return false
	}
	if questionWordRegex.MatchString(trimmed) {
		return false
	}
	return true
}

// shortScoreFromFeatures computes the short-signal score (Layer 1
// heuristic). Per the design doc §Threshold, the formula is
// calibrated so that the per-vibe_case short_threshold can produce
// the right classification even when short keywords are present
// alongside a single long keyword (e.g. "just research on X" should
// classify as long for C4, not short).
//
// Key constraints derived from the 10-persona analysis:
//
//  1. Cap short keyword count at 2. Common short words ("just",
//     "quickly", "small") stack easily and would otherwise inflate
//     short_score above long_score for short messages with a single
//     long keyword.
//  2. Token count weight is moderate (1.5) so that an imperative
//     short message without keywords still passes the C1 short
//     threshold.
//  3. Imperative bonus is small (0.3) since it is a weak signal.
//
// Final formula:
//
//	short_score = (token_count < 20) * 1.5
//	            + min(2, len(short_keywords)) * 1.0
//	            + (imperative ? 0.3 : 0)
func shortScoreFromFeatures(feats Features) float64 {
	var s float64
	if feats.TokenCount > 0 && feats.TokenCount < 20 {
		s += 1.5
	}
	kwCount := len(feats.ShortKeywords)
	if kwCount > 2 {
		kwCount = 2
	}
	s += float64(kwCount) * 1.0
	if feats.Imperative {
		s += 0.3
	}
	return s
}

// longScoreFromFeatures computes the long-signal score (Layer 1
// heuristic). Per the design doc §Threshold:
//
//	long_score = (token_count > 50) * 2 + len(long_keywords) * 1.5 + multi_step * 1
//
// Plus a small bonus for long token count in the 30-50 range
// (intermediate-long, low weight: +1.0).
func longScoreFromFeatures(feats Features) float64 {
	var s float64
	if feats.TokenCount > 50 {
		s += 2.0
	} else if feats.TokenCount >= 30 {
		s += 1.0
	}
	s += float64(len(feats.LongKeywords)) * 1.5
	if feats.MultiStep {
		s += 1.0
	}
	return s
}

// confidenceFromFeatures returns a confidence in [0, 1] reflecting
// how decisively the classification separated.
//
// High confidence: scores differ by >= 2.0 AND both cross thresholds.
// Low confidence: scores are close or neither crosses its threshold.
func confidenceFromFeatures(feats Features, t Thresholds, class Class) float64 {
	shortScore := shortScoreFromFeatures(feats)
	longScore := longScoreFromFeatures(feats)

	// Margin: how much did the winning score beat the losing one?
	margin := shortScore - longScore
	if margin < 0 {
		margin = -margin
	}

	// Did both scores cross their thresholds? If not, the
	// classification is less decisive.
	shortCrossed := shortScore >= t.Short
	longCrossed := longScore >= t.Long

	var conf float64
	switch {
	case margin >= 3.0 && (shortCrossed || longCrossed):
		conf = 0.95
	case margin >= 2.0:
		conf = 0.85
	case margin >= 1.0:
		conf = 0.7
	case margin >= 0.5:
		conf = 0.55
	default:
		conf = 0.4
	}

	// Penalize confidence if neither threshold was crossed (forced
	// medium).
	if class == ClassMedium && !shortCrossed && !longCrossed {
		conf *= 0.6
	}

	return conf
}
