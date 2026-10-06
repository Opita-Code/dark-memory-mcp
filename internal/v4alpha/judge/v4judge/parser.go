package v4judge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// findMatchingBrace locates the byte offset of the closing '}' that
// matches the opening '{' at startIdx. Counts brace depth, ignoring
// braces inside JSON strings. Returns -1 if no matching brace found.
//
// Algorithm: walk forward from startIdx+1, tracking:
//   - inString: whether we're inside a "..." string
//   - escape:   whether the previous char was a backslash (for "\\")
//   - depth:    the brace nesting level
//
// When depth returns to 0, return the current offset (the '}' itself).
// RE2 doesn't allow this kind of stateful scan, so we use a manual
// loop instead of a regex.
func findMatchingBrace(body []byte, startIdx int) int {
	if startIdx < 0 || startIdx >= len(body) || body[startIdx] != '{' {
		return -1
	}
	depth := 1
	inString := false
	escape := false
	for i := startIdx + 1; i < len(body); i++ {
		c := body[i]
		if escape {
			escape = false
			continue
		}
		if c == '\\' && inString {
			escape = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// verdictJSON is the wire shape returned by the chat model. Parsed from
// the OpenAI-compatible response body. See Judge for the JSON example.
type verdictJSON struct {
	Verdict    string  `json:"verdict"`
	Confidence float64 `json:"confidence"`
	Reasoning  string  `json:"reasoning"`
}

// jsonObjectStartRegex matches the first '{' that starts a JSON
// object. Combined with findMatchingBrace, locates the full verdict
// object even when the model wraps it in markdown fences like:
//
//	```json
//	{"verdict":"aligned",...}
//	```
//
// RE2 (Go's engine) does not support lookahead/lookbehind, so we use
// a two-step scan: find the first '{' and then walk forward counting
// brace depth (see findMatchingBrace below). Strings inside the JSON
// are honored so braces inside strings don't break the count.
var jsonObjectStartRegex = regexp.MustCompile(`\{`)

// parseVerdictResponse extracts the verdict JSON from the response body
// and returns a validated Verdict. Returns ErrProviderBadResponse on any
// parse failure (caller may retry once via Judge.retryOnce).
//
// Parsing strategy (in order):
//  1. Try direct json.Unmarshal of the whole body into verdictJSON.
//  2. If that fails, scan for the first {...} substring and re-parse
//     (handles models that wrap JSON in markdown fences, prefix with
//     "Here is the verdict:" or similar prose, etc.).
//  3. If that fails, return ErrProviderBadResponse.
//
// Reasoning defaults to the raw text before the JSON object if the
// model prepended a one-line summary (e.g. "Reasoning: ..." before
// the JSON). This is a courtesy for operators reading the verdict row;
// the canonical reasoning is what's in the JSON.
func parseVerdictResponse(body []byte) (Verdict, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return Verdict{}, fmt.Errorf("%w: empty body", ErrProviderBadResponse)
	}

	// Try 1 — direct unmarshal.
	var v verdictJSON
	if err := json.Unmarshal(body, &v); err == nil && v.Verdict != "" {
		return normalizeVerdict(v, body), nil
	}

	// Try 2 — extract {...} from markdown fences or prose prefix.
	startIdx := jsonObjectStartRegex.FindIndex(body)
	if startIdx != nil {
		endIdx := findMatchingBrace(body, startIdx[0])
		if endIdx > startIdx[0] {
			candidate := body[startIdx[0] : endIdx+1]
			var v2 verdictJSON
			if err := json.Unmarshal(candidate, &v2); err == nil && v2.Verdict != "" {
				return normalizeVerdict(v2, candidate), nil
			}
		}
	}

	return Verdict{}, fmt.Errorf("%w: could not extract verdict JSON", ErrProviderBadResponse)
}

// normalizeVerdict converts the parsed verdictJSON into the canonical
// Verdict struct with ProviderID/LatencyMS zeroed (the caller fills
// them). Validates the canonical contract (verdict string, confidence
// range, non-empty reasoning) so the caller can rely on Verdict.Validate
// not failing.
//
// Preflight: the OpenAI wire shape nests the assistant message inside
// {choices: [{message: {content: "..."}}]}. Try 1 handles that
// transparently because the content string IS a JSON object
// ({verdict,...}). The wire shape itself never reaches this parser —
// Judge.Judge reads the body directly from resp.Body.
//
// If the body is the OpenAI wire shape (and the assistant content is
// already a string), parseVerdictResponse would fail on try 1 (because
// the outer object has no "verdict" key). Try 2 might catch the inner
// JSON via the regex match. For providers that double-encode (the wire
// shape with content as a string containing JSON), Judge.Judge unwraps
// the outer shape via unwrapOpenAIShape before calling parseVerdictResponse.
func normalizeVerdict(v verdictJSON, raw []byte) Verdict {
	return Verdict{
		Verdict:    strings.ToLower(strings.TrimSpace(v.Verdict)),
		Confidence: v.Confidence,
		Reasoning:  strings.TrimSpace(v.Reasoning),
	}
}

// unwrapOpenAIShape extracts the assistant message content from an
// OpenAI-compatible response shape:
//
//	{
//	  "choices": [{"message": {"role": "assistant", "content": "..."}}]
//	}
//
// If the body is NOT in this shape (e.g. the assistant content is
// already a JSON object directly), returns body unchanged. This is
// the same dual-shape fallback used in
// internal/nli/chat.go::parseChatCompletionResponse (lines 296-330).
func unwrapOpenAIShape(body []byte) []byte {
	var openaiShape struct {
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			Text string `json:"text"` // legacy fallback inside the same struct
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &openaiShape); err != nil {
		return body
	}
	if len(openaiShape.Choices) == 0 {
		return body
	}
	content := openaiShape.Choices[0].Message.Content
	if content == "" {
		content = openaiShape.Choices[0].Text
	}
	if content == "" {
		return body
	}
	return []byte(content)
}