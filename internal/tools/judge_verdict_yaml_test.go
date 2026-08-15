package tools

import "testing"

// TestParseVerdictJSON_YAML covers the spec 1205 P0 regression:
// MiniMax-M3 with thinking adaptive answers in YAML/markdown, NOT
// JSON. The pre-1205 parser returned "unknown" for any non-JSON blob,
// so a rich needs_human reasoning was recorded as "unknown" and the
// judgment_history lied about the judge's real decision.
func TestParseVerdictJSON_YAML(t *testing.T) {
	cases := []struct {
		name string
		blob string
		want string
	}{
		{
			name: "yaml needs_human",
			blob: "```yaml\nverdict: needs_human\nreasoning: insufficient information to ground verdict\n\nevaluation:\n  spec_intent_summary: |\n    Estandarizar el orden de uso de tools\n```",
			want: "needs_human",
		},
		{
			name: "yaml aligned",
			blob: "verdict: aligned\nconfidence: 0.9\nreasoning: spec satisfied",
			want: "aligned",
		},
		{
			name: "yaml drift_detected",
			blob: "verdict: drift_detected\nreasoning: artifact contradicts spec",
			want: "drift_detected",
		},
		{
			name: "markdown backtick inline",
			blob: "The verdict is `verdict:needs_human` after review",
			want: "needs_human",
		},
		{
			name: "json canonical",
			blob: `{"verdict":"needs_human","reasoning":"no spec"}`,
			want: "needs_human",
		},
		{
			name: "json aligned bool legacy",
			blob: `{"aligned":true,"confidence":0.9}`,
			want: "aligned",
		},
		{
			name: "empty blob",
			blob: "",
			want: "unknown",
		},
		{
			name: "garbage",
			blob: "this is not a verdict at all",
			want: "unknown",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseVerdictJSON(tc.blob); got != tc.want {
				t.Errorf("parseVerdictJSON(%q) = %q, want %q", tc.blob, got, tc.want)
			}
		})
	}
}

// TestParseVerdictJSON_JSONShapes guards the JSON-only shapes that
// must keep working (the fast path must not regress).
func TestParseVerdictJSON_JSONShapes(t *testing.T) {
	if got := parseVerdictJSON(`{"verdict":"drift_detected"}`); got != "drift_detected" {
		t.Errorf("drift_detected: got %q", got)
	}
	if got := parseVerdictJSON(`{"aligned":false}`); got != "drift_detected" {
		t.Errorf("aligned=false: got %q", got)
	}
	if got := parseVerdictJSON(`{"verdict":"match"}`); got != "match" {
		t.Errorf("verbatim pass-through: got %q", got)
	}
}
