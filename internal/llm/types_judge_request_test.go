package llm

import (
	"strings"
	"testing"
)

// TestJudgeRequest_ComposeUserContent_LegacyEmptyUserPrompt — TD-J6
// backward compatibility: empty UserPrompt keeps the legacy behavior
// (raw content only).
func TestJudgeRequest_ComposeUserContent_LegacyEmptyUserPrompt(t *testing.T) {
	req := JudgeRequest{Content: "artifact body"}
	if got := req.ComposeUserContent(); got != "artifact body" {
		t.Errorf("legacy path changed: got %q, want raw content", got)
	}
}

// TestJudgeRequest_ComposeUserContent_PrependsUserPrompt — TD-J6:
// when the composed user prompt (spec intent + output schema) is
// present, it precedes the raw content so the judge receives both
// halves of the spec-vs-artifact pair.
func TestJudgeRequest_ComposeUserContent_PrependsUserPrompt(t *testing.T) {
	req := JudgeRequest{UserPrompt: "spec intent + output schema", Content: "artifact body"}
	got := req.ComposeUserContent()
	if !strings.HasPrefix(got, "spec intent + output schema") {
		t.Errorf("must start with UserPrompt: %q", got)
	}
	if !strings.Contains(got, "## Content (verbatim)") {
		t.Errorf("must include the Content section marker: %q", got)
	}
	if !strings.HasSuffix(got, "artifact body") {
		t.Errorf("must end with the raw Content: %q", got)
	}
}
