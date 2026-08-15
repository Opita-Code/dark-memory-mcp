// judge_verdict_normalize_test.go — regression coverage for
// normalizeVerdictJSON (v2.21.2, spec 1205 P2).
//
// Background: MiniMax-M3 with thinking:adaptive answers in YAML or
// markdown (```yaml\nverdict: needs_human\n...```), NOT JSON. The
// pipeline's parseVerdict learned the lenient YAML path (spec 1200 P0)
// but every OTHER reader of sdd_evaluations.verdict_json was still
// JSON-strict — judgment_history (parseVerdictJSON), recall.DriftFrame
// (parseSDDVerdict), and any tool-level consumer all read "unknown"
// from a valid YAML verdict (verified against evals 983/996: drift
// report said needs_human but judgment_history said unknown).
// normalizeVerdictJSON fixes the write boundary so sdd_evaluations
// always carries JSON.
package orchestration

import (
	"encoding/json"
	"testing"
)

func TestNormalizeVerdictJSON_AlreadyJSON_Unchanged(t *testing.T) {
	raw := `{"verdict":"needs_human","reasoning":"insufficient information","confidence":0.7}`
	got := normalizeVerdictJSON(raw)
	if got != raw {
		t.Errorf("normalizeVerdictJSON(%q) = %q, want identity for valid JSON", raw, got)
	}
}

func TestNormalizeVerdictJSON_YAML_MapsToJSON(t *testing.T) {
	raw := "verdict: needs_human\nreasoning: insufficient information to ground verdict\n"
	got := normalizeVerdictJSON(raw)
	var v map[string]any
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("normalizeVerdictJSON YAML output must be JSON: %v\nraw out: %q", err, got)
	}
	if verdict, _ := v["verdict"].(string); verdict != "needs_human" {
		t.Errorf("verdict = %q, want needs_human (parsed from YAML)", verdict)
	}
	if reasoning, _ := v["reasoning"].(string); reasoning == "" {
		t.Errorf("reasoning must preserve the raw YAML reasoning, got %q", reasoning)
	}
}

func TestNormalizeVerdictJSON_MarkdownFence_MapsToJSON(t *testing.T) {
	raw := "```yaml\nverdict: aligned\nreasoning: artifact matches spec intent\n```"
	got := normalizeVerdictJSON(raw)
	var v map[string]any
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("normalizeVerdictJSON markdown output must be JSON: %v", err)
	}
	if verdict, _ := v["verdict"].(string); verdict != "aligned" {
		t.Errorf("verdict = %q, want aligned (parsed from markdown fence)", verdict)
	}
}

func TestNormalizeVerdictJSON_Unparseable_FailsSafeNeedsHuman(t *testing.T) {
	raw := "the model rambled and produced no structured verdict"
	got := normalizeVerdictJSON(raw)
	var v map[string]any
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("normalizeVerdictJSON fallback must still produce JSON: %v", err)
	}
	if verdict, _ := v["verdict"].(string); verdict != "needs_human" {
		t.Errorf("verdict = %q, want needs_human (parseVerdict fail-safe)", verdict)
	}
}

func TestNormalizeVerdictJSON_Empty_Unchanged(t *testing.T) {
	if got := normalizeVerdictJSON(""); got != "" {
		t.Errorf("normalizeVerdictJSON(\"\") = %q, want empty", got)
	}
}
