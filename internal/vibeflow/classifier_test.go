// Package vibeflow_test — classifier_test.go covers the Layer 1
// heuristic upfront (Loop 8 / L8.1a). The tests pin the public
// contract:
//
//   - Classify(message, vibeCase) returns one of 3 classes + confidence.
//   - 8 vibe_cases each have their own thresholds (DefaultThresholds).
//   - Unknown vibe_cases fall back to DefaultThresholdsFallback.
//   - Features are extracted deterministically (zero LLM calls).
//   - Confidence is in [0, 1] and reflects score separation.
//   - Edge cases (empty, very long, only keywords) are handled.
package vibeflow_test

import (
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

// TestDefaultThresholds_HasAllVibeCases pins the 8-entry default map.
// Removing a case is a breaking change; adding a new case is the
// L8.1b / L8.2 / L8.3 follow-up.
func TestDefaultThresholds_HasAllVibeCases(t *testing.T) {
	want := []string{"C1", "C2", "C3", "C4", "C5", "C6", "C7", "C8"}
	if got := len(vibeflow.DefaultThresholds); got != 8 {
		t.Fatalf("DefaultThresholds: want 8 entries, got %d", got)
	}
	for _, k := range want {
		if _, ok := vibeflow.DefaultThresholds[k]; !ok {
			t.Errorf("DefaultThresholds missing key %q", k)
		}
	}
}

// TestClassify_Short_TypicalC1 — tactical / fix mode.
func TestClassify_Short_TypicalC1(t *testing.T) {
	msgs := []string{
		"just fix this typo",
		"rename this var",
		"add a docstring to foo",
		"small change in the loop",
		"quickly check this",
		"single line edit",
	}
	for _, msg := range msgs {
		class, conf, _ := vibeflow.Classify(msg, "C1")
		if class != vibeflow.ClassShort {
			t.Errorf("C1 %q: want short, got %q (conf=%.2f)", msg, class, conf)
		}
	}
}

// TestClassify_Long_TypicalC1 — refactor / design.
func TestClassify_Long_TypicalC1(t *testing.T) {
	msgs := []string{
		"refactor the entire auth subsystem to use the new pattern",
		"design the migration to v2 with backward compatibility",
		"investigate the performance issue in the API layer and propose a fix",
	}
	for _, msg := range msgs {
		class, _, _ := vibeflow.Classify(msg, "C1")
		if class != vibeflow.ClassLong {
			t.Errorf("C1 %q: want long, got %q", msg, class)
		}
	}
}

// TestClassify_Long_ResistantC4 — research is almost never short.
func TestClassify_Long_ResistantC4(t *testing.T) {
	// Even with "just" and "small" present, C4 should classify as long.
	// "just" is a short_keyword, but with C4's short threshold of 5.0,
	// it cannot pass.
	msgs := []string{
		"research SOTA on vector databases 2026",
		"find me papers on prompt caching",
		"investigate the literature on conformal prediction",
		"just a quick research on the topic", // even "just" + "quick" cannot pass C4 short threshold of 5.0
	}
	for _, msg := range msgs {
		class, _, _ := vibeflow.Classify(msg, "C4")
		if class != vibeflow.ClassLong {
			t.Errorf("C4 %q: want long, got %q", msg, class)
		}
	}
}

// TestClassify_AlwaysShortC4 — even with no keywords, C4 tends long
// when message is multi-sentence.
func TestClassify_LongMultiSentenceC4(t *testing.T) {
	msg := "I need to understand the current state of the art on this topic. " +
		"Please find authoritative sources, summarize the main approaches, " +
		"and explain the trade-offs."
	class, _, _ := vibeflow.Classify(msg, "C4")
	if class != vibeflow.ClassLong {
		t.Errorf("C4 multi-sentence: want long, got %q", class)
	}
}

// TestClassify_MediumC2 — text is balanced 50/50.
func TestClassify_MediumC2(t *testing.T) {
	// "Write a hero section" is a typical C2 text task, balanced.
	msgs := []string{
		"write a hero section for the landing",
		"rewrite this paragraph",
		"draft a release note",
	}
	for _, msg := range msgs {
		class, _, _ := vibeflow.Classify(msg, "C2")
		// These should be MEDIUM (no strong short or long signal).
		if class != vibeflow.ClassMedium {
			t.Errorf("C2 %q: want medium, got %q", msg, class)
		}
	}
}

// TestClassify_UnknownVibeCase_UsesFallback — unknown case falls back.
func TestClassify_UnknownVibeCase_UsesFallback(t *testing.T) {
	class, _, _ := vibeflow.Classify("just fix this", "C99")
	if class != vibeflow.ClassShort {
		t.Errorf("C99 fallback: want short, got %q", class)
	}
	// Same message in C99 with long keywords should still be medium
	// (fallback thresholds are 3.0/3.0).
	class, _, _ = vibeflow.Classify("refactor and migrate the system", "C99")
	if class != vibeflow.ClassLong {
		t.Errorf("C99 fallback with long: want long, got %q", class)
	}
}

// TestExtractFeatures_TokenCount — whitespace-delimited word count.
func TestExtractFeatures_TokenCount(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"hello", 1},
		{"hello world", 2},
		{"  spaced  out  ", 2},
		{"line1\nline2", 2},
		{"with\ttab", 2},
	}
	for _, c := range cases {
		feats := vibeflow.ExtractFeatures(c.in)
		if feats.TokenCount != c.want {
			t.Errorf("ExtractFeatures(%q).TokenCount: want %d, got %d", c.in, c.want, feats.TokenCount)
		}
	}
}

// TestExtractFeatures_ShortKeywords — finds short-signal keywords.
func TestExtractFeatures_ShortKeywords(t *testing.T) {
	feats := vibeflow.ExtractFeatures("just quickly fix this typo, single small thing")
	want := []string{"just", "quickly", "typo", "single", "small"}
	if !equalStrings(feats.ShortKeywords, want) {
		t.Errorf("ShortKeywords: want %v, got %v", want, feats.ShortKeywords)
	}
}

// TestExtractFeatures_LongKeywords — finds long-signal keywords.
func TestExtractFeatures_LongKeywords(t *testing.T) {
	feats := vibeflow.ExtractFeatures("refactor and design the migration, then investigate")
	want := []string{"refactor", "design", "migration", "investigate"}
	if !equalStrings(feats.LongKeywords, want) {
		t.Errorf("LongKeywords: want %v, got %v", want, feats.LongKeywords)
	}
}

// TestExtractFeatures_MultiStep — detects multi-step structure.
func TestExtractFeatures_MultiStep(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"just fix this", false},
		{"first do X, then do Y", true},
		{"step 1: collect", true},
		{"1. do this", true},
		{"and then we go", true},
		{"after that we sleep", true},
	}
	for _, c := range cases {
		feats := vibeflow.ExtractFeatures(c.in)
		if feats.MultiStep != c.want {
			t.Errorf("ExtractFeatures(%q).MultiStep: want %v, got %v", c.in, c.want, feats.MultiStep)
		}
	}
}

// TestExtractFeatures_Imperative — directive form.
func TestExtractFeatures_Imperative(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"fix this bug", true},
		{"please refactor the module", true},
		{"can you fix this?", false},                 // question
		{"what should I do here?", false},            // question word
		{"", false},                                  // empty
		{"why is this broken?", false},               // question word + mark
	}
	for _, c := range cases {
		feats := vibeflow.ExtractFeatures(c.in)
		if feats.Imperative != c.want {
			t.Errorf("ExtractFeatures(%q).Imperative: want %v, got %v", c.in, c.want, feats.Imperative)
		}
	}
}

// TestScore_ShortThreshold — short score >= threshold => short.
func TestScore_ShortThreshold(t *testing.T) {
	feats := vibeflow.Features{
		TokenCount:    5,  // <20 => +2
		ShortKeywords: []string{"just", "typo"}, // 2 * 1.5 = +3
		Imperative:    true,                    // +0.5
	} // shortScore = 5.5
	t1 := vibeflow.Thresholds{Short: 3.0, Long: 3.0}
	if got := vibeflow.Score(feats, t1); got != vibeflow.ClassShort {
		t.Errorf("Score(short_feats, t1): want short, got %q", got)
	}
	// Higher short threshold => medium
	t2 := vibeflow.Thresholds{Short: 10.0, Long: 3.0}
	if got := vibeflow.Score(feats, t2); got != vibeflow.ClassMedium {
		t.Errorf("Score(short_feats, t2=high_short): want medium, got %q", got)
	}
}

// TestScore_LongThreshold — long score >= threshold => long.
func TestScore_LongThreshold(t *testing.T) {
	feats := vibeflow.Features{
		TokenCount:   80,                  // >50 => +2
		LongKeywords: []string{"refactor", "design", "investigate"}, // 3 * 1.5 = +4.5
		MultiStep:    true,                // +1
	} // longScore = 7.5
	t1 := vibeflow.Thresholds{Short: 3.0, Long: 3.0}
	if got := vibeflow.Score(feats, t1); got != vibeflow.ClassLong {
		t.Errorf("Score(long_feats, t1): want long, got %q", got)
	}
	// Higher long threshold => medium
	t2 := vibeflow.Thresholds{Short: 3.0, Long: 10.0}
	if got := vibeflow.Score(feats, t2); got != vibeflow.ClassMedium {
		t.Errorf("Score(long_feats, t2=high_long): want medium, got %q", got)
	}
}

// TestScore_TieBreaker — when both pass, the higher wins.
func TestScore_TieBreaker(t *testing.T) {
	feats := vibeflow.Features{
		TokenCount:    80,
		ShortKeywords: []string{"just"},
		LongKeywords:  []string{"refactor", "design"},
	} // shortScore = 1*1.5 = 1.5, longScore = 2+3 = 5
	th := vibeflow.Thresholds{Short: 1.0, Long: 1.0}
	if got := vibeflow.Score(feats, th); got != vibeflow.ClassLong {
		t.Errorf("Score(tied_feats): want long (higher), got %q", got)
	}
}

// TestConfidence_InRange — confidence is always in [0, 1].
func TestConfidence_InRange(t *testing.T) {
	cases := []struct {
		msg  string
		vibe string
	}{
		{"just fix this typo", "C1"},
		{"refactor the entire auth subsystem", "C1"},
		{"research SOTA on vector DBs", "C4"},
		{"write a hero section", "C2"},
		{"", "C1"},
		{"a", "C1"},
	}
	for _, c := range cases {
		_, conf, _ := vibeflow.Classify(c.msg, c.vibe)
		if conf < 0.0 || conf > 1.0 {
			t.Errorf("Classify(%q, %q).conf: want [0,1], got %.2f", c.msg, c.vibe, conf)
		}
	}
}

// TestConfidence_HighOnClearSignals — high confidence when both
// short_score and long_score are decisively different.
func TestConfidence_HighOnClearSignals(t *testing.T) {
	// Clear short: many short keywords, low token count, no long.
	_, conf, _ := vibeflow.Classify("just fix this small typo quickly", "C1")
	if conf < 0.7 {
		t.Errorf("clear short: want high conf, got %.2f", conf)
	}
	// Clear long: many long keywords, multi-step.
	_, conf, _ = vibeflow.Classify(
		"refactor and design the migration. step 1: analyze, step 2: implement, step 3: test",
		"C1",
	)
	if conf < 0.7 {
		t.Errorf("clear long: want high conf, got %.2f", conf)
	}
}

// TestClassify_EmptyMessage — empty message is medium with low conf.
func TestClassify_EmptyMessage(t *testing.T) {
	class, conf, _ := vibeflow.Classify("", "C1")
	if class != vibeflow.ClassMedium {
		t.Errorf("empty: want medium, got %q", class)
	}
	if conf > 0.5 {
		t.Errorf("empty: want low conf, got %.2f", conf)
	}
}

// TestClassify_VeryLongMessage — message >100 tokens trends long.
func TestClassify_VeryLongMessage(t *testing.T) {
	msg := strings.Repeat("refactor ", 30) + "and then design and then build and then test"
	class, _, _ := vibeflow.Classify(msg, "C1")
	if class != vibeflow.ClassLong {
		t.Errorf("very long: want long, got %q", class)
	}
}

// TestClassify_CaseInsensitiveKeywords — keywords are case-insensitive.
func TestClassify_CaseInsensitiveKeywords(t *testing.T) {
	// Mixed case
	_, conf, _ := vibeflow.Classify("JUST FIX THIS TYPO", "C1")
	if conf < 0.7 {
		t.Errorf("uppercase short: want high conf, got %.2f", conf)
	}
	_, conf, _ = vibeflow.Classify("REFACTOR the entire system", "C1")
	if conf < 0.5 {
		t.Errorf("uppercase long: want reasonable conf, got %.2f", conf)
	}
}

// TestClassify_PerVibeCase_C3 — decision is rarely short.
func TestClassify_PerVibeCase_C3(t *testing.T) {
	// A typical "decide between X and Y" message.
	class, _, _ := vibeflow.Classify("decide between PostgreSQL and SQLite for the new tenant", "C3")
	if class == vibeflow.ClassShort {
		t.Errorf("C3 decision: should not be short, got %q", class)
	}
}

// TestClassify_PerVibeCase_C7 — multi is generally long.
func TestClassify_PerVibeCase_C7(t *testing.T) {
	// C7 multi-modal task with structure.
	class, _, _ := vibeflow.Classify(
		"create a landing page: image, hero copy, signup flow, mobile and desktop variants",
		"C7",
	)
	if class != vibeflow.ClassLong {
		t.Errorf("C7 multi: want long, got %q", class)
	}
}

// equalStrings is a helper to compare keyword slices (order-insensitive
// within expected tolerance, but in practice the order is stable).
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
