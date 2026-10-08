// Package vibeflow — context.go (L8.1b: persona-tier detection).
//
// This file adds the Layer 1.b capability: classifier behavior that
// adapts to the operator's context (tier, domain, language, device,
// rush mode). Together with classifier.go (L8.1a), it implements the
// 10-persona calibration from core/loop-8-design.md.
//
// # What is "Context"?
//
// Context is the harness-provided state about the operator. The
// classifier uses it to:
//
//  1. **Tier** (novice/intermediate/expert) — bias the per-vibe_case
//     thresholds. Novice operators get more "short" classifications
//     (they do more small tasks). Expert operators get more "long"
//     classifications (they do more planning).
//  2. **Domain** (code/marketing/...) — adjust signal weights. Code-y
//     signals (parens, paths) are neutral for code domain but signal
//     complexity for non-code domains (where the operator may be
//     asking a developer to implement something).
//  3. **Lang** (en/es-CO/...) — language detection. Used downstream
//     by L8.3 for block-content i18n. Not used in scoring yet.
//  4. **Device** (mobile/tablet/desktop) — affects block sizing in
//     L8.3. Not used in scoring here.
//  5. **RushMode** (bool) — operator is multitasking / under time
//     pressure. Affects block sizing in L8.3. Not used in scoring.
//
// # Where the Context comes from
//
// The harness (opencode, claude-code, etc.) provides the Context.
// Dark-memory does NOT infer it. Inference is the harness's job:
//
//   - Tier: operator config + session count + drift history
//   - Domain: spec.vibe_case + project tags
//   - Lang: message language detection (provided by classifier as
//     Features.LangDetected) OR operator config
//   - Device: HTTP user-agent or harness config
//   - RushMode: session behavior (inter-session gap, daily count, etc.)
//
// # Default behavior (L8.1a compatibility)
//
// When Context is not provided, the classifier uses
// DefaultContext = {TierIntermediate, DomainOther, LangEn,
// DeviceDesktop, RushMode false}. This matches the L8.1a behavior
// (no context = same as before).
package vibeflow

import (
	"regexp"
	"strings"
)

// Tier is the operator experience level.
type Tier string

const (
	// TierNovice — first-time user or in the first 3 sessions of a
	// project. Bias toward "short" classifications.
	TierNovice Tier = "novice"

	// TierIntermediate — default. 3-10 sessions in a project.
	TierIntermediate Tier = "intermediate"

	// TierExpert — >10 sessions, low drift history. Bias toward
	// "long" classifications.
	TierExpert Tier = "expert"
)

// Domain is the operator's work domain. Inferred from spec.vibe_case
// + project tags by the harness.
type Domain string

const (
	DomainCode       Domain = "code"
	DomainMarketing  Domain = "marketing"
	DomainSales      Domain = "sales"
	DomainHR         Domain = "hr"
	DomainEducation  Domain = "education"
	DomainHealthcare Domain = "healthcare"
	DomainLegal      Domain = "legal"
	DomainOther      Domain = "other"
)

// Lang is the BCP-47 language tag. Used downstream by L8.3 for
// block-content i18n.
type Lang string

const (
	LangEn   Lang = "en"
	LangEs   Lang = "es"
	LangEsCO Lang = "es-CO"
	LangPt   Lang = "pt"
)

// Device is the operator's device class.
type Device string

const (
	DeviceMobile  Device = "mobile"
	DeviceTablet  Device = "tablet"
	DeviceDesktop Device = "desktop"
)

// Context is the harness-provided operator state. Zero value is
// treated as DefaultContext (intermediate, other, en, desktop, no rush).
type Context struct {
	// Tier affects thresholds (see thresholdsFor).
	Tier Tier

	// Domain affects HasCodeSignals weight (only matters if != code).
	Domain Domain

	// Lang is the language for downstream block content (L8.3).
	Lang Lang

	// Device affects block sizing in L8.3.
	Device Device

	// RushMode affects block sizing in L8.3.
	RushMode bool
}

// DefaultContext is used when Context is zero or unspecified.
// Matches the L8.1a behavior (no context = same as before).
var DefaultContext = Context{
	Tier:     TierIntermediate,
	Domain:   DomainOther,
	Lang:     LangEn,
	Device:   DeviceDesktop,
	RushMode: false,
}

// thresholdsFor returns the per-vibe_case thresholds adjusted for tier.
//
// Tier modifiers:
//
//	Novice:       Short *= 0.7 (easier short), Long *= 1.5 (harder long)
//	Intermediate: no change
//	Expert:       Short *= 1.5 (harder short), Long *= 0.7 (easier long)
//
// Rationale: novice operators do more small tasks (typos, single
// function fixes) so the heuristic should default to "short" more
// often. Expert operators do more planning and design (sessions
// >30min) so the heuristic should default to "long" more often.
//
// Multipliers are intentionally modest (0.7 / 1.5). Larger multipliers
// would make per-vibe_case calibration ineffective.
func thresholdsFor(vibeCase string, ctx Context) Thresholds {
	base, ok := DefaultThresholds[vibeCase]
	if !ok {
		base = DefaultThresholdsFallback
	}
	var sMult, lMult float64 = 1.0, 1.0
	switch ctx.Tier {
	case TierNovice:
		sMult, lMult = 0.7, 1.5
	case TierExpert:
		sMult, lMult = 1.5, 0.7
	}
	return Thresholds{
		Short: base.Short * sMult,
		Long:  base.Long * lMult,
	}
}

// New L8.1b message-derived features. They extend the L8.1a Features
// struct (which already has TokenCount, ShortKeywords, LongKeywords,
// MultiStep, Imperative).
//
// These are added to the Features struct (not Context) because they
// are extracted FROM the message text. Context fields (Tier, Domain,
// etc.) come from external harness state.

// codeSignalsRegex detects code-y patterns in the message:
//   - Parens with content: func(), method(), $var
//   - File paths: /foo/bar, ./baz, src/main.go, *.ts
//   - Shell commands: $ npm, git commit, make build
//   - Code blocks: ```code```
//   - Common extensions: .go, .ts, .tsx, .js, .py, .rs, .md
// codeFencePattern is the three-backtick code block fence. Defined
// separately because Go raw strings cannot contain backticks.
const codeFencePattern = "`" + "`" + "`"

var codeSignalsRegex = regexp.MustCompile(
	`(?i)(` +
		`[a-zA-Z_][a-zA-Z0-9_]*\([^)]*\)` + `|` + // func()
		`[a-zA-Z_][a-zA-Z0-9_]*\[[^\]]*\]` + `|` + // arr[0]
		`[./][a-zA-Z0-9_./-]+\.[a-z]{2,4}\b` + `|` + // ./foo.go (with ext)
		`/[a-zA-Z][a-zA-Z0-9_./-]+/[a-zA-Z0-9_./-]+` + `|` + // /usr/local/bin (path, 2+ components)
		`\$\s+[a-z]+\b` + `|` + // $ command
		`\b(make|git|npm|yarn|go run|docker|kubectl)\s+[a-z]` + `|` + // command word
		`\.(go|ts|tsx|js|jsx|py|rs|md|json|yaml|yml|sql|sh)\b` + `|` + // extensions
		regexp.QuoteMeta(codeFencePattern) + // three backticks (code block fence)
		`)`,
)

// panicKeywordsRegex detects urgent / "debug" language. Used as a
// long signal because panic = typically a long task (debugging,
// incident response).
//
// Patterns:
//   - "broken", "crash", "down", "failing"
//   - "fix NOW", "fix this ASAP", "urgent", "URGENT"
//   - "outage", "incident", "rollback"
//
// Note: high-uppercase detection is done in hasPanicKeywords() with
// a separate character-by-character check. We don't use a regex
// `[A-Z]{4,}` because with `(?i)` (case-insensitive) that would
// match any 4+ letter sequence in lowercase too (e.g., "typo",
// "this", "investigate"). The hasPanicKeywords function is more
// precise.
var panicKeywordsRegex = regexp.MustCompile(
	`(?i)(` +
		`\b(broken|crash(es|ed)?|down|failing|failed|failure|outage|incident|rollback)\b` + `|` +
		`\b(urgent|asap|emergency)\b` + `|` +
		`\b(fix (now|asap|this (now|immediately)))\b` + `|` +
		`\b(prod(uction)? (is )?down)\b` +
		`)`,
)

// multiArtifactRegex detects when the message references multiple
// files or specs. Indicates a longer task.
//
// Patterns:
//   - 2+ file extensions
//   - 2+ file paths
//   - "files", "specs", "modules" with quantifiers
//   - numbered list with file-like entries: "1. foo.go 2. bar.ts"
var multiArtifactRegex = regexp.MustCompile(
	`(?i)(` +
		`(?:\.[a-z]{2,4}\b.*){2,}` + `|` + // 2+ file extensions
		`(?:[/][a-zA-Z0-9_./-]+\.[a-z]{2,4}.*){2,}` + `|` + // 2+ file paths
		`\b(all|every|both)\s+(files?|specs?|modules?|components?)\b` + `|` + // "all files"
		`\b(multiple|several)\s+(files?|specs?|modules?)\b` + `|` + // "multiple files"
		`\b\d+\s+files?\b` + // "5 files"
		`)`,
)

// langDetectRegexes detect the language of a message by characteristic
// patterns. We detect Spanish (Latin American) first because of the
// opita/Huila market.
//
// Three sets of regexes:
//
//  1. "Unique" signals are exclusive to that language and never
//     appear in the other (e.g., "obrigado" is unambiguously PT,
//     "gracias" or "arregla" are unambiguously ES). Used FIRST to
//     break ties.
//
//  2. "Strong" signals are common in that language but also appear
//     in the other (e.g., "olá" is also a Spanish interjection,
//     "quiero" is also PT). Used as secondary signal.
//
//  3. "Weak" signals are shared or generic (e.g., "por favor",
//     articles, "este/esta"). Used as fallback.
//
// Order of detection:
//  1. Unique Portuguese → pt
//  2. Unique Spanish → es (then check Huila)
//  3. Strong Portuguese → pt
//  4. Strong Spanish → es (then check Huila)
//  5. Weak Portuguese (only if no Spanish at all) → pt
//  6. Weak Spanish → es (then check Huila)
//  7. Default → en
var (
	spanishUniqueRegex = regexp.MustCompile(
		`(?i)(` +
			`\b(gracias|adiós|hala|halar|arreglar|arregla|esto|eso|aquello)\b` + `|` +
			`\b(pero|porque|sin|con|para|muy|mucho|poco)\b` +
			`)`,
	)
	portugueseUniqueRegex = regexp.MustCompile(
		`(?i)(` +
			`\b(obrigado|obrigada|tudo|isto|isso|aquilo|conserta|consertar)\b` + `|` +
			`\b(não|nao|está|voce)\b` +
			`)`,
	)
	spanishStrongRegex = regexp.MustCompile(
		`(?i)(` +
			`\b(hola|buenos d[ií]as|buenas tardes|buenas noches)\b` + `|` +
			`\b(necesito|necesitamos|quiero|queremos|puedo|podemos|debo|debemos|tengo que|tenemos que)\b` + `|` +
			`\b(c[oó]mo|cu[aá]l|cu[aá]les|d[oó]nde|cu[aá]ndo|por qu[eé])\b` +
			`)`,
	)
	portugueseStrongRegex = regexp.MustCompile(
		`(?i)(` +
			`\b(olá|tchau|bom dia|boa tarde|boa noite)\b` + `|` +
			`\b(preciso|precisamos|quero|queremos|posso|podemos|tenho que|temos que)\b` + `|` +
			`\b(como|qual|quais|onde|quando)\b` +
			`)`,
	)
	spanishWeakRegex = regexp.MustCompile(
		`(?i)(` +
			`\b(por favor|este|esta|estos|estas|esa|esos|esas|aquel|aquella)\b` + `|` +
			`\b(el|la|los|las|un|una|unos|unas)\s+\w+` +
			`)`,
	)
	portugueseWeakRegex = regexp.MustCompile(
		`(?i)(` +
			`\b(por favor|este|esta|estes|estas|essa|esses|essas)\b` + `|` +
			`\b(o|a|os|as|um|uma|uns|umas)\s+\w+` +
			`)`,
	)
)

// hasCodeSignals — extracted feature. True if the message has code-y
// patterns (parens, paths, commands).
func hasCodeSignals(message string) bool {
	return codeSignalsRegex.MatchString(message)
}

// hasPanicKeywords — extracted feature. True if the message has
// urgent / debug language OR high uppercase ratio.
func hasPanicKeywords(message string) bool {
	if panicKeywordsRegex.MatchString(message) {
		return true
	}
	// Also detect high uppercase ratio as a panic signal.
	letters := 0
	upper := 0
	for _, r := range message {
		if r >= 'A' && r <= 'Z' {
			upper++
			letters++
		} else if r >= 'a' && r <= 'z' {
			letters++
		}
	}
	if letters > 10 && float64(upper)/float64(letters) > 0.5 {
		return true
	}
	return false
}

// hasMultiArtifact — extracted feature. True if the message references
// multiple files/specs.
func hasMultiArtifact(message string) bool {
	return multiArtifactRegex.MatchString(message)
}

// huilaLangRegex is the package-level regex for Huila-specific Spanish
// markers. Distinguished from generic Spanish (es) by the use of
// "sumercé", "pues", "ve" (as in "ve y traime"), "mijo"/"mijito".
//
// We can't use \b...\b around these because Go's regex engine only
// treats ASCII letters as word characters. The word "sumercé"
// contains "é" which is non-ASCII, so the trailing \b doesn't behave
// as expected. Instead, we use (?:^|\W) at the start and (?:$|\W) at
// the end to handle word boundaries in a unicode-safe way.
var huilaLangRegex = regexp.MustCompile(
	`(?i)(?:^|\W)(sumerc[eé]|pues|ve|mijo|mijito)(?:$|\W)`,
)

// detectLanguage — extracted feature. Returns the BCP-47 tag of the
// message's language, or empty if uncertain.
//
// Order of detection (see comment on the regex vars above):
//  1. Unique Portuguese → pt
//  2. Unique Spanish → es (then check Huila)
//  3. Strong Portuguese → pt
//  4. Strong Spanish → es (then check Huila)
//  5. Weak Portuguese (only if no Spanish at all) → pt
//  6. Weak Spanish → es (then check Huila)
//  7. Default → en
func detectLanguage(message string) Lang {
	msg := strings.ToLower(message)
	if portugueseUniqueRegex.MatchString(msg) {
		return LangPt
	}
	if spanishUniqueRegex.MatchString(msg) {
		if huilaLangRegex.MatchString(msg) {
			return LangEsCO
		}
		return LangEs
	}
	if portugueseStrongRegex.MatchString(msg) {
		return LangPt
	}
	if spanishStrongRegex.MatchString(msg) {
		if huilaLangRegex.MatchString(msg) {
			return LangEsCO
		}
		return LangEs
	}
	// For weak signals, prefer Spanish (most common in our market)
	// unless there's NO Spanish signal at all and only Portuguese.
	if spanishWeakRegex.MatchString(msg) {
		if huilaLangRegex.MatchString(msg) {
			return LangEsCO
		}
		return LangEs
	}
	if portugueseWeakRegex.MatchString(msg) {
		return LangPt
	}
	return LangEn
}

// ClassifyWithContext is the L8.1b public entry point. It accepts
// a Context and uses it to bias the per-vibe_case thresholds.
//
// Returns (class, confidence, features). Confidence is in [0, 1].
// The features include both L8.1a (TokenCount, ShortKeywords, etc.)
// and L8.1b (HasCodeSignals, PanicKeywords, MultiArtifact, LangDetected).
func ClassifyWithContext(message string, vibeCase string, ctx Context) (Class, float64, Features) {
	feats := ExtractFeatures(message)
	feats.LangDetected = detectLanguage(message)
	feats.HasCodeSignals = hasCodeSignals(message)
	feats.PanicKeywords = hasPanicKeywords(message)
	feats.MultiArtifact = hasMultiArtifact(message)

	th := thresholdsFor(vibeCase, ctx)
	shortScore, longScore := scoreWithContext(feats, ctx)
	class := classifyScores(shortScore, longScore, th)
	conf := confidenceFromFeaturesWithContext(feats, th, ctx, class)
	return class, conf, feats
}

// classifyScores is a pure helper: given short_score, long_score, and
// thresholds, return the Class. Used by both L8.1a Score (via
// shortScoreFromFeatures/longScoreFromFeatures) and L8.1b
// ClassifyWithContext (via scoreWithContext).
func classifyScores(shortScore, longScore float64, t Thresholds) Class {
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

// scoreWithContext is the L8.1b-aware scoring function. It calls
// the L8.1a score (short_score, long_score) and adds context-aware
// modifiers. Returns the modified scores, NOT the class.
//
// Modifiers applied to long_score:
//
//  1. PanicKeywords: +1.5 (urgency = long task).
//  2. MultiArtifact: +1.5 (multiple files/specs = long task).
//  3. HasCodeSignals in non-code domain: +1.0 (a non-code operator
//     asking for code is more complex than usual).
func scoreWithContext(feats Features, ctx Context) (shortScore, longScore float64) {
	shortScore = shortScoreFromFeatures(feats)
	longScore = longScoreFromFeatures(feats)

	if feats.PanicKeywords {
		longScore += 1.5
	}
	if feats.MultiArtifact {
		longScore += 1.5
	}
	if feats.HasCodeSignals && ctx.Domain != "" && ctx.Domain != DomainCode {
		longScore += 1.0
	}
	return
}

// confidenceFromFeaturesWithContext — L8.1b version. Same logic as
// L8.1a but uses the context-aware thresholds and scores.
func confidenceFromFeaturesWithContext(feats Features, t Thresholds, ctx Context, class Class) float64 {
	shortScore, longScore := scoreWithContext(feats, ctx)

	margin := shortScore - longScore
	if margin < 0 {
		margin = -margin
	}

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

	if class == ClassMedium && !shortCrossed && !longCrossed {
		conf *= 0.6
	}

	return conf
}
