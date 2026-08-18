package judgeparse

import "testing"

// TestParsePipeline covers the vibe pipeline contract (was
// orchestration.parseVerdict / parseDriftVerdict): eval_type shapes,
// confidence floor 0.5, fail-safe needs_human, TD-J4 last-occurrence,
// fenced-JSON structured verdict (drift 1089).
func TestParsePipeline(t *testing.T) {
	cases := []struct {
		name string
		et   string
		blob string
		conf float32
		want string
	}{
		// eval_type-specific boolean shapes.
		{"grounding grounded", "grounding_check", `{"grounded":true}`, 0.9, Aligned},
		{"grounding not grounded", "grounding_check", `{"grounded":false}`, 0.9, DriftDetected},
		{"pii found", "pii_detect", `{"pii_found":true}`, 0.9, DriftDetected},
		{"pii clean", "pii_detect", `{"pii_found":false}`, 0.9, Aligned},
		{"injection found", "prompt_injection_scan", `{"injection_found":true}`, 0.9, DriftDetected},
		{"injection clean", "prompt_injection_scan", `{"injection_found":false}`, 0.9, Aligned},
		// brand_match / compliance_check verdict aliases.
		{"brand match", "brand_match", `{"verdict":"match"}`, 0.9, Aligned},
		{"brand drift", "brand_match", `{"verdict":"drift_detected"}`, 0.9, DriftDetected},
		{"compliance compliant", "compliance_check", `{"verdict":"compliant"}`, 0.9, Aligned},
		{"compliance non", "compliance_check", `{"verdict":"non_compliant"}`, 0.9, DriftDetected},
		// M6/M1/M7/M8 numeric/boolean shapes.
		{"spec_test_alignment pass", "spec_test_alignment", `{"alignment":0.9}`, 0.9, Aligned},
		{"spec_test_alignment fail", "spec_test_alignment", `{"alignment":0.4}`, 0.9, DriftDetected},
		{"mutation pass", "mutation_score_check", `{"pass":true}`, 0.9, Aligned},
		{"mutation fail", "mutation_score_check", `{"pass":false}`, 0.9, DriftDetected},
		{"security coverage pass", "security_coverage", `{"coverage":0.85}`, 0.9, Aligned},
		{"security coverage fail", "security_coverage", `{"coverage":0.5}`, 0.9, DriftDetected},
		{"resilience passed", "resilience_check", `{"passed":true}`, 0.9, Aligned},
		{"resilience failed", "resilience_check", `{"passed":false}`, 0.9, DriftDetected},
		// Generic canonical verdict + legacy aligned bool.
		{"drift aligned", "drift_judge", `{"verdict":"aligned"}`, 0.9, Aligned},
		{"drift needs_human", "drift_judge", `{"verdict":"needs_human"}`, 0.9, NeedsHuman},
		{"legacy aligned true", "drift_judge", `{"aligned":true}`, 0.9, Aligned},
		{"legacy aligned false", "drift_judge", `{"aligned":false}`, 0.9, DriftDetected},
		// Confidence floor < 0.5 → needs_human regardless of verdict.
		{"low conf aligned", "drift_judge", `{"verdict":"aligned"}`, 0.2, NeedsHuman},
		// Fenced JSON: structured top-level verdict must win (drift 1089).
		{"fenced aligned late drift quote", "drift_judge", "```json\n{\"verdict\":\"aligned\",\"confidence\":0.92,\"evidence\":[{\"quote\":\"{\\\"verdict\\\":\\\"drift_detected\\\"}\"}]}\n```", 0.92, Aligned},
		{"fenced needs_human early aligned quote", "drift_judge", "```json\n{\"verdict\":\"needs_human\",\"confidence\":0.7,\"evidence\":[{\"quote\":\"verdict: aligned\"}]}\n```", 0.7, NeedsHuman},
		// Fenced YAML still falls back.
		{"fenced yaml", "drift_judge", "```yaml\nverdict: needs_human\nreasoning: not grounded\n```", 0.9, NeedsHuman},
		// TD-J4 last-occurrence: quoted tokens cannot override the
		// judge's final conclusion, in every direction.
		{"tdj4 dir1 quote aligned final needs_human", "drift_judge", `The artifact states: "Expected verdict: aligned". However the spec body is not provided. ## Verdict: ` + "`needs_human`" + ``, 0.9, NeedsHuman},
		{"tdj4 dir2 quote needs_human final aligned", "drift_judge", `The artifact mentions "verdict: needs_human" as a hypothesis. Requirements R1-R6 are met. ## Verdict: aligned`, 0.9, Aligned},
		{"tdj4 dir3 quote needs_human final drift", "drift_judge", `quoted artifact text: "verdict: needs_human" ... ## Verdict: drift_detected`, 0.9, DriftDetected},
		// No token → fail-safe needs_human.
		{"no token failsafe", "drift_judge", "the judge wrote prose without any structured verdict", 0.9, NeedsHuman},
		{"empty failsafe", "drift_judge", "", 0.9, NeedsHuman},
		{"garbage failsafe", "drift_judge", "this is not a verdict at all", 0.9, NeedsHuman},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParsePipeline(tc.et, tc.blob, tc.conf); got != tc.want {
				t.Errorf("ParsePipeline(%q, %q, %v) = %q, want %q", tc.et, tc.blob, tc.conf, got, tc.want)
			}
		})
	}
}

// TestParseDecision covers the M6 drift gate contract (was
// drift.parseDecisionFromJudgeJSON): empty → skipped, bare-word
// aliases, conservative drift_detected default, confidence floor 0.3.
func TestParseDecision(t *testing.T) {
	cases := []struct {
		name string
		blob string
		conf float32
		want string
	}{
		{"structured aligned", `{"verdict":"aligned","confidence":0.92}`, 0.92, Aligned},
		{"structured drift_detected", `{"verdict":"drift_detected","confidence":0.85}`, 0.85, DriftDetected},
		{"structured needs_human", `{"verdict":"needs_human","confidence":0.7}`, 0.7, NeedsHuman},
		{"structured with reasoning", `{"verdict":"aligned","reasoning":"spec matches","confidence":0.9}`, 0.9, Aligned},
		{"bare aligned", "aligned\nreasoning here", 0.9, Aligned},
		{"bare drift", "drift_detected", 0.9, DriftDetected},
		{"bare needs_human", "needs_human", 0.9, NeedsHuman},
		// Confidence floor 0.3 (drift-gate historical contract).
		{"aligned low conf", `{"verdict":"aligned","confidence":0.1}`, 0.1, NeedsHuman},
		{"aligned conf 0.3 exact", `{"verdict":"aligned","confidence":0.3}`, 0.3, Aligned},
		{"aligned conf 0.2999", `{"verdict":"aligned","confidence":0.2999}`, 0.2999, NeedsHuman},
		{"aligned conf 0.3001", `{"verdict":"aligned","confidence":0.3001}`, 0.3001, Aligned},
		// Unknown value → conservative drift_detected.
		{"unknown conservative", `{"verdict":"weird_value"}`, 0.9, DriftDetected},
		{"garbage conservative", "this is not a verdict at all", 0.9, DriftDetected},
		// Empty/whitespace → skipped.
		{"empty skipped", "", 0, Skipped},
		{"whitespace skipped", "   \n\t  ", 0, Skipped},
		// REGRESSION (bug fixed by t3): fenced JSON used to land in the
		// bare-word branch → "```json" → conservative drift_detected.
		// Now stripCodeFence runs before Unmarshal → aligned.
		{"fenced json aligned", "```json\n{\"verdict\":\"aligned\",\"confidence\":0.9}\n```", 0.9, Aligned},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseDecision(tc.blob, tc.conf); got != tc.want {
				t.Errorf("ParseDecision(%q, %v) = %q, want %q", tc.blob, tc.conf, got, tc.want)
			}
		})
	}
}

// TestParseHistoryVerdict covers judgment_history (was
// tools.parseVerdictJSON): empty → unknown, verbatim pass-through,
// YAML/markdown fallback, unknown default.
func TestParseHistoryVerdict(t *testing.T) {
	cases := []struct {
		name string
		blob string
		want string
	}{
		{"yaml needs_human", "```yaml\nverdict: needs_human\nreasoning: insufficient information to ground verdict\n\nevaluation:\n  spec_intent_summary: |\n    Estandarizar el orden de uso de tools\n```", NeedsHuman},
		{"yaml aligned", "verdict: aligned\nconfidence: 0.9\nreasoning: spec satisfied", Aligned},
		{"yaml drift_detected", "verdict: drift_detected\nreasoning: artifact contradicts spec", DriftDetected},
		{"markdown backtick inline", "The verdict is `verdict:needs_human` after review", NeedsHuman},
		{"json canonical", `{"verdict":"needs_human","reasoning":"no spec"}`, NeedsHuman},
		{"json aligned bool legacy", `{"aligned":true,"confidence":0.9}`, Aligned},
		{"json aligned bool false", `{"aligned":false}`, DriftDetected},
		{"json drift_detected", `{"verdict":"drift_detected"}`, DriftDetected},
		// Verbatim pass-through for non-canonical verdict values.
		{"json verbatim match", `{"verdict":"match"}`, "match"},
		{"empty unknown", "", Unknown},
		{"garbage unknown", "this is not a verdict at all", Unknown},
		// REGRESSION (bug fixed by t3): the old Contains-ordered YAML
		// fallback matched a QUOTED "verdict: aligned" token before the
		// judge's real needs_human conclusion → recorded verdict lied.
		// Last-occurrence scan makes the final verdict win.
		{"tdj4 quoted aligned final needs_human", `The judge quoted the artifact: "verdict: aligned". However the spec body is not provided. ## Verdict: needs_human`, NeedsHuman},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseHistoryVerdict(tc.blob); got != tc.want {
				t.Errorf("ParseHistoryVerdict(%q) = %q, want %q", tc.blob, got, tc.want)
			}
		})
	}
}

// TestNormalizeWord covers the bare-word alias map (drift gate legacy).
func TestNormalizeWord(t *testing.T) {
	cases := []struct {
		word string
		want string
	}{
		{"aligned", Aligned},
		{"ALIGNED", Aligned},
		{"ok", Aligned},
		{"pass", Aligned},
		{"match", Aligned},
		{"drift_detected", DriftDetected},
		{"drift", DriftDetected},
		{"fail", DriftDetected},
		{"mismatch", DriftDetected},
		{"needs_human", NeedsHuman},
		{"human", NeedsHuman},
		{"review", NeedsHuman},
		{"uncertain", NeedsHuman},
		{"weird", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := normalizeWord(tc.word); got != tc.want {
			t.Errorf("normalizeWord(%q) = %q, want %q", tc.word, got, tc.want)
		}
	}
}

// TestParsePipeline_TDJ4_v3_HeaderPrecedesLastOccurrence pins the
// TD-J4 v3 heuristic: when the judge emits "## Verdict: <word>" the
// verdict from the explicit header wins over any contradicting
// artifact quote that appears LATER in the blob. Last-occurrence
// alone (TD-J4 v1) would pick the contradicting quote in the
// conclusion-first, contradicting-quote-last pattern; the explicit
// header is more reliable than the heuristic.
//
// The header is the documented fallback the LLM produces when JSON
// output fails (judge_prompt_builder.go schema). All three
// directions must work (any verdict conclusion can be contradicted
// by any later verdict-shaped substring).
func TestParsePipeline_TDJ4_v3_HeaderPrecedesLastOccurrence(t *testing.T) {
	cases := []struct {
		name string
		blob string
		want string
	}{
		// Direction 1: needs_human conclusion, later "verdict: aligned" quote.
		{
			name: "v3_header_needs_human_later_aligned_quote",
			blob: "## Verdict: needs_human. The artifact mentions 'verdict: aligned' as a hypothesis.",
			want: NeedsHuman,
		},
		// Direction 2: aligned conclusion, later "verdict: drift_detected" quote.
		{
			name: "v3_header_aligned_later_drift_quote",
			blob: "## Verdict: aligned. Looking at the artifact: 'verdict: drift_detected'.",
			want: Aligned,
		},
		// Direction 3: drift_detected conclusion, later "verdict: needs_human" quote.
		{
			name: "v3_header_drift_later_needs_human_quote",
			blob: "## Verdict: drift_detected. The artifact states 'verdict: needs_human'.",
			want: DriftDetected,
		},
		// Backtick-wrapped value (markdown form).
		{
			name: "v3_header_backtick_needs_human_later_aligned_quote",
			blob: "## Verdict: `needs_human`. The artifact mentions 'verdict: aligned'.",
			want: NeedsHuman,
		},
		// Header in uppercase (case-insensitive).
		{
			name: "v3_header_uppercase_VERDICT_later_quote",
			blob: "## VERDICT: aligned. The artifact mentions 'verdict: needs_human'.",
			want: Aligned,
		},
		// Header followed by trailing punctuation.
		{
			name: "v3_header_trailing_period_later_quote",
			blob: "## Verdict: needs_human. The artifact mentions 'verdict: aligned'.",
			want: NeedsHuman,
		},
		// No header — fall back to last-occurrence (backward compat).
		{
			name: "v3_no_header_falls_back_to_last_occurrence",
			blob: "The artifact mentions 'verdict: needs_human' as a hypothesis. Requirements R1-R6 are met. ## Verdict: aligned",
			want: Aligned, // last-occurrence wins (this is the dir2 case from v1, still works)
		},
		// Header with unknown word — fall back to last-occurrence.
		{
			name: "v3_header_unknown_word_falls_back",
			blob: "## Verdict: maybe. The artifact mentions 'verdict: aligned'.",
			want: Aligned, // last-occurrence picks aligned
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParsePipeline("drift_judge", tc.blob, 0.9); got != tc.want {
				t.Errorf("ParsePipeline(%q) = %q, want %q", tc.blob, got, tc.want)
			}
		})
	}
}

// TestParseVerdictHeader covers the TD-J4 v3 helper directly so a
// future regression in parse() ordering (e.g. moving the header
// check AFTER last-occurrence) would be caught even if the
// integration tests above stay green.
func TestParseVerdictHeader(t *testing.T) {
	cases := []struct {
		name string
		blob string
		want string
	}{
		{"basic aligned", "## Verdict: aligned", Aligned},
		{"basic drift_detected", "## Verdict: drift_detected", DriftDetected},
		{"basic needs_human", "## Verdict: needs_human", NeedsHuman},
		{"backtick needs_human", "## Verdict: `needs_human`", NeedsHuman},
		{"backtick aligned", "## Verdict: `aligned`", Aligned},
		{"uppercase", "## VERDICT: aligned", Aligned},
		{"trailing period", "## Verdict: needs_human.", NeedsHuman},
		{"trailing semicolon", "## Verdict: drift_detected;", DriftDetected},
		{"bold markdown", "## Verdict: **aligned**", Aligned},
		{"blank after colon", "## Verdict: ", ""},
		{"unknown word", "## Verdict: maybe", ""},
		{"no header", "some prose without a verdict header", ""},
		{"header but no colon", "## Verdict aligned", ""},
		{"header mid-text not at start", "intro prose ## Verdict: aligned", Aligned},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseVerdictHeader(tc.blob); got != tc.want {
				t.Errorf("parseVerdictHeader(%q) = %q, want %q", tc.blob, got, tc.want)
			}
		})
	}
}

// TestStripCodeFence covers the fence-stripping helper (drift 1089).
func TestStripCodeFence(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"json fence", "```json\n{\"verdict\":\"aligned\"}\n```", `{"verdict":"aligned"}`},
		{"yaml fence", "```yaml\nverdict: needs_human\n```", "verdict: needs_human"},
		{"no fence", `{"verdict":"aligned"}`, `{"verdict":"aligned"}`},
		{"whitespace", "  {\"verdict\":\"aligned\"}  ", `{"verdict":"aligned"}`},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripCodeFence(tc.in); got != tc.want {
				t.Errorf("stripCodeFence(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
