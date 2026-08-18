// Package judgeparse centralizes parsing of LLM-as-judge verdict blobs.
//
// Prior to t3 (spec 1242) there were three divergent parsers with
// subtly different semantics:
//
//	orchestration.parseVerdict        — vibe pipeline (eval_type-aware,
//	                                     stripCodeFence, last-occurrence scan,
//	                                     fail-safe needs_human, floor 0.5)
//	drift.parseDecisionFromJudgeJSON  — M6 drift gate (bare-word legacy,
//	                                     conservative drift_detected default,
//	                                     floor 0.3, NO stripCodeFence → the
//	                                     fenced-JSON false drift_detected bug)
//	tools.parseVerdictJSON            — judgment_history (verbatim pass-through,
//	                                     YAML Contains-ordered → the TD-J4
//	                                     quoted-token bug still alive, unknown)
//
// All three now delegate here. The core Parse function takes Options so
// each caller preserves its documented contract; the shared machinery
// (structured JSON + stripCodeFence, eval-type shapes, last-occurrence
// canonical-token scan, bare-word normalization) lives in one place so a
// verdict parsing bug is fixed once, not three times.
package judgeparse

import (
	"encoding/json"
	"strings"
)

// Canonical three-state drift verdicts plus the two diagnostic states
// (skipped = no judge ran; unknown = no verdict found).
const (
	Aligned       = "aligned"
	DriftDetected = "drift_detected"
	NeedsHuman    = "needs_human"
	Skipped       = "skipped"
	Unknown       = "unknown"
)

// Options configures parse for one caller's documented contract.
type Options struct {
	// EvalType enables the eval_type-specific JSON shapes
	// (grounding_check, pii_detect, prompt_injection_scan,
	// brand_match, compliance_check, spec_test_alignment,
	// mutation_score_check, security_coverage, resilience_check).
	EvalType string

	// ConfidenceFloor: confidence < floor → NeedsHuman regardless of
	// the LLM's verdict. 0 disables the floor. The pipeline trusts the
	// judge above 0.5; the drift gate's historical floor was 0.3.
	ConfidenceFloor float32

	// UnknownVerdict is returned when no canonical token can be found.
	//   - NeedsHuman    (pipeline): infra/parse failure must surface for
	//     operator review, never as silent approval.
	//   - DriftDetected (drift gate): a misinterpreted verdict is more
	//     dangerous than a false positive (conservative fail-closed).
	//   - Unknown       (judgment_history): diagnostic read, no gate.
	UnknownVerdict string

	// EmptyVerdict is returned for an empty/whitespace-only blob.
	// Defaults to UnknownVerdict when unset.
	EmptyVerdict string

	// VerbatimFallback: when the structured "verdict" field holds a
	// non-canonical value (e.g. "match", "compliant"), return it
	// verbatim instead of falling through. judgment_history wants the
	// judge's raw value for diagnostics.
	VerbatimFallback bool

	// BareWord: accept the first line of a non-JSON blob as a bare
	// verdict word ("aligned", "drift_detected", "ok", ...).
	// Drift-gate legacy compatibility.
	BareWord bool
}

// ParsePipeline is the vibe pipeline's parser (was
// orchestration.parseVerdict / parseDriftVerdict). Eval_type-aware,
// confidence floor 0.5, fail-safe needs_human on parse failure.
func ParsePipeline(evalType, blob string, confidence float32) string {
	return parse(blob, confidence, Options{
		EvalType:        evalType,
		ConfidenceFloor: 0.5,
		UnknownVerdict:  NeedsHuman,
		EmptyVerdict:    NeedsHuman,
	})
}

// ParseDecision is the M6 drift gate's parser (was
// drift.parseDecisionFromJudgeJSON). Empty → skipped; bare-word
// legacy aliases; conservative drift_detected default; confidence
// floor 0.3 (historical drift-gate contract).
func ParseDecision(blob string, confidence float32) string {
	return parse(blob, confidence, Options{
		ConfidenceFloor: 0.3,
		UnknownVerdict:  DriftDetected,
		EmptyVerdict:    Skipped,
		BareWord:        true,
	})
}

// ParseHistoryVerdict is judgment_history's parser (was
// tools.parseVerdictJSON). Empty → unknown; verbatim pass-through for
// non-canonical verdict values; unknown default (diagnostic read, no
// gate). Shares the last-occurrence scan, so a quoted verdict token in
// the judge's reasoning can never override the recorded verdict.
func ParseHistoryVerdict(blob string) string {
	return parse(blob, 0, Options{
		UnknownVerdict:   Unknown,
		EmptyVerdict:     Unknown,
		VerbatimFallback: true,
	})
}

// parse is the single canonical judge-verdict parser. Order of
// precedence:
//
//  1. Empty blob → EmptyVerdict.
//  2. Confidence floor (when configured) → NeedsHuman.
//  3. Structured JSON (fence-stripped) — eval_type-specific shapes,
//     then generic canonical "verdict", then legacy "aligned" bool.
//  4. Explicit "## Verdict:" header (TD-J4 v3) — case-insensitive,
//     tolerant of markdown decorations. Closes the residual edge
//     case where the judge puts the conclusion FIRST and a
//     contradicting artifact quote LAST (last-occurrence picked the
//     contradicting quote).
//  5. Bare-word first line (drift gate only).
//  6. Last-occurrence canonical-token scan over the raw text.
//  7. UnknownVerdict (never a wrong verdict).
func parse(blob string, confidence float32, opts Options) string {
	trimmed := strings.TrimSpace(blob)
	if trimmed == "" {
		if opts.EmptyVerdict != "" {
			return opts.EmptyVerdict
		}
		return opts.UnknownVerdict
	}

	if opts.ConfidenceFloor > 0 && confidence < opts.ConfidenceFloor {
		return NeedsHuman
	}

	var v map[string]any
	if err := json.Unmarshal([]byte(stripCodeFence(blob)), &v); err == nil {
		// Eval_type-specific boolean/numeric shapes (non-verdict
		// judges). Kept in step with defaultSystemForEval in
		// llm_client.go — every shape there must be handled here.
		switch opts.EvalType {
		case "grounding_check":
			if grounded, ok := v["grounded"].(bool); ok {
				if grounded {
					return Aligned
				}
				return DriftDetected
			}
		case "pii_detect":
			if found, ok := v["pii_found"].(bool); ok {
				if found {
					// PII present = the content fails the check.
					return DriftDetected
				}
				return Aligned
			}
		case "prompt_injection_scan":
			if found, ok := v["injection_found"].(bool); ok {
				if found {
					return DriftDetected
				}
				return Aligned
			}
		case "brand_match":
			// brand_match emits {"verdict":"match"|"drift_detected"}.
			if verdict, ok := v["verdict"].(string); ok {
				switch verdict {
				case "match":
					return Aligned
				case "drift_detected":
					return DriftDetected
				}
			}
		case "compliance_check":
			// compliance_check emits
			// {"verdict":"compliant"|"non_compliant"}.
			if verdict, ok := v["verdict"].(string); ok {
				switch verdict {
				case "compliant":
					return Aligned
				case "non_compliant":
					return DriftDetected
				}
			}
		case "spec_test_alignment":
			// M6 — alignment = tests_verifying_spec_claims /
			// spec_claims. >= 0.7 passes.
			if a, ok := numericBool(v, "alignment"); ok {
				if a >= 0.7 {
					return Aligned
				}
				return DriftDetected
			}
		case "mutation_score_check":
			// M1 — mutation_score = mutants_killed / total_mutants.
			// The judge emits "pass" (score >= threshold).
			if pass, ok := v["pass"].(bool); ok {
				if pass {
					return Aligned
				}
				return DriftDetected
			}
		case "security_coverage":
			// M7 — security_coverage = owasp_vectors_with_tests /
			// total. >= 0.8 passes.
			if c, ok := numericBool(v, "coverage"); ok {
				if c >= 0.8 {
					return Aligned
				}
				return DriftDetected
			}
		case "resilience_check":
			// M8 — resilience_score = chaos_experiments_passed /
			// run. passed=true → aligned.
			if passed, ok := v["passed"].(bool); ok {
				if passed {
					return Aligned
				}
				return DriftDetected
			}
		}
		// Generic canonical "verdict" string shape (drift_judge,
		// mindset_quality, test_quality_review, oracle_quality, ...).
		if verdict, ok := v["verdict"].(string); ok {
			switch verdict {
			case Aligned, DriftDetected, NeedsHuman:
				return verdict
			default:
				if opts.VerbatimFallback {
					return verdict
				}
			}
		}
		// Legacy shape: boolean "aligned" field.
		if aligned, ok := v["aligned"].(bool); ok {
			if aligned {
				return Aligned
			}
			return DriftDetected
		}
	}

	// Explicit "## Verdict:" header (TD-J4 v3). The header is the
	// documented fallback the LLM produces when JSON output fails
	// (judge_prompt_builder.go schema). Prefer the explicit header
	// over inline mentions — closes the residual edge case where
	// the judge puts the conclusion FIRST and a contradicting
	// artifact quote LAST (last-occurrence alone would pick the
	// contradicting quote). Returns "" when no header is found
	// or the word after the header doesn't map to a canonical
	// verdict.
	if d := parseVerdictHeader(blob); d != "" {
		return d
	}

	// Bare-word legacy (drift gate): first line of a non-JSON blob.
	if opts.BareWord {
		first := strings.SplitN(trimmed, "\n", 2)[0]
		first = strings.TrimSpace(strings.TrimRight(first, ",.;:"))
		if d := normalizeWord(first); d != "" {
			return d
		}
	}

	// Lenient fallback: last-occurrence canonical-token scan (TD-J4).
	if d := scanCanonicalTokens(blob); d != "" {
		return d
	}
	return opts.UnknownVerdict
}

// normalizeWord maps a free-text verdict word to one of the three
// canonical decisions, or "" when the word is unknown. Legacy aliases
// come from drift.parseDecisionFromJudgeJSON (ok/pass/match →
// aligned; drift/fail/mismatch → drift_detected; human/review/
// uncertain → needs_human). The confidence floor is applied by parse
// globally, so no word-level confidence branch lives here.
func normalizeWord(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "aligned", "ok", "pass", "match":
		return Aligned
	case "drift_detected", "drift", "fail", "mismatch":
		return DriftDetected
	case "needs_human", "human", "review", "uncertain":
		return NeedsHuman
	default:
		return ""
	}
}

// stripCodeFence removes a ```json ``` (or ```yaml / ```markdown)
// wrapper from the judge's raw output so the structured verdict inside
// can be parsed as JSON. Returns the input unchanged when no fence is
// present. TD-J4 evolution (drift 1089): without this, the fallback
// scans the entire output including the judge's evidence quotes.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop the opening ```lang line.
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	// Drop the closing ``` (and any trailing newline before it).
	if i := strings.LastIndex(s, "```"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// parseVerdictHeader extracts a verdict from an explicit markdown
// header like "## Verdict: aligned" — case-insensitive, tolerant of
// backticks / bold / trailing punctuation. Returns "" when no
// header is found or the word after the header doesn't map to a
// canonical verdict.
//
// TD-J4 v3 (2026-08-18, sess-f28534325d0f2496, commit pending):
// closes the residual edge case where last-occurrence picks a
// contradicting artifact quote when the judge puts the conclusion
// FIRST. The pattern "## Verdict:" is the documented fallback the
// LLM produces when JSON output fails (per judge_prompt_builder.go
// schema — every judge prompt ends with the JSON schema inside a
// markdown code fence, and MiniMax-M3 sometimes emits the verdict
// as a "## Verdict: ..." header inline with reasoning prose).
//
// Without this step, inputs like
//
//	## Verdict: needs_human. The artifact mentions 'verdict: aligned'.
//
// return Aligned (last-occurrence of verdict:aligned wins).
// With this step, the explicit header is preferred over inline
// mentions and the parser returns the correct NeedsHuman.
func parseVerdictHeader(blob string) string {
	const header = "## verdict:"
	lower := strings.ToLower(blob)
	idx := strings.Index(lower, header)
	if idx < 0 {
		return ""
	}
	// Use len(header) on the original blob — idx is byte-aligned so
	// the slice is identical regardless of case.
	after := blob[idx+len(header):]
	// Take the rest of the line (verdict is typically on the same
	// line as the header).
	if lineEnd := strings.Index(after, "\n"); lineEnd >= 0 {
		after = after[:lineEnd]
	}
	after = strings.TrimSpace(after)
	if after == "" {
		return ""
	}
	// First word, stripped of markdown decorations (backticks, bold,
	// trailing punctuation, semicolons, periods).
	firstWord := strings.SplitN(after, " ", 2)[0]
	firstWord = strings.Trim(firstWord, ",.;:`*\"")
	if firstWord == "" {
		return ""
	}
	return normalizeWord(firstWord)
}

// scanCanonicalTokens is the TD-J4 last-occurrence fallback: match the
// canonical verdict token with the GREATEST index in a
// whitespace-collapsed, lowercased, de-backticked copy of the raw
// blob. Last-occurrence semantics mean a verdict-shaped substring
// QUOTED in the judge's own reasoning/evidence can never win over the
// judge's final conclusion — the TD-J4 false-aligned bypass (drift
// 1083) and the fenced-JSON false drift_detected (drift 1089).
// Returns "" when no token is found; the caller's UnknownVerdict
// applies (parse/infra failure must surface, never silent approval).
func scanCanonicalTokens(blob string) string {
	normalized := strings.ToLower(blob)
	var b strings.Builder
	b.Grow(len(normalized))
	prevSpace := false
	for _, r := range normalized {
		isSpace := r == ' ' || r == '\t' || r == '\n' || r == '\r'
		if isSpace {
			if !prevSpace {
				b.WriteRune(' ')
			}
			prevSpace = true
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	// Collapse whitespace around colons. The first ReplaceAll handles
	// `: ` (colon followed by space); the second handles ` :` (space
	// followed by colon). Strip backticks and `**` bold markers BEFORE
	// the colon-collapse so `verdict:` `aligned`` (markdown backtick
	// form) can bind.
	b2 := strings.ReplaceAll(b.String(), "`", "")
	b2 = strings.ReplaceAll(b2, "**", "")
	compact := strings.ReplaceAll(b2, ": ", ":")
	compact = strings.ReplaceAll(compact, " :", ":")

	canonicalTokens := []struct {
		token   string
		verdict string
	}{
		{`"verdict":"aligned"`, Aligned},
		{`verdict:"aligned"`, Aligned},
		{`verdict:aligned`, Aligned},
		{`"aligned":true`, Aligned},
		{`"drift":false`, Aligned},
		{`"verdict":"needs_human"`, NeedsHuman},
		{`verdict:"needs_human"`, NeedsHuman},
		{`verdict:needs_human`, NeedsHuman},
		{`"verdict":"drift_detected"`, DriftDetected},
		{`verdict:"drift_detected"`, DriftDetected},
		{`verdict:drift_detected`, DriftDetected},
	}
	bestVerdict := ""
	bestIdx := -1
	for _, c := range canonicalTokens {
		if idx := strings.LastIndex(compact, c.token); idx > bestIdx {
			bestIdx = idx
			bestVerdict = c.verdict
		}
	}
	return bestVerdict
}

// numericBool reads a float field from a map[string]any (the shape
// json.Unmarshal produces). Accepts float64 (the JSON default),
// json.Number, and int (for tests constructing the map by hand).
// Returns ok=false when the field is absent or not numeric.
func numericBool(v map[string]any, key string) (float64, bool) {
	raw, ok := v[key]
	if !ok {
		return 0, false
	}
	switch n := raw.(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}
