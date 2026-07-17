// Package wire - envelope.go: shared JSON-RPC envelope helpers.
//
// mcp-go's stdio transport wraps every tools/call response in:
//
//	{"result":{"content":[{"type":"text","text":"<JSON ToolResponse>"}],"isError":false}}
//
// The wire tests parse this envelope repeatedly. Centralizing the
// parsing here means a future mcp-go change (e.g. dropping the
// content array in favour of structured_content) is a single-file
// edit. Keep the helper pure: it does no I/O and depends only on
// encoding/json + testing.
package wire

import (
	"encoding/json"
	"fmt"
	"testing"
)

// toolResponseEnvelope is the JSON-RPC shape mcp-go v0.56.0 emits
// for a successful tools/call. The text block carries our
// internal ToolResponse (data + audit + next + error) serialized
// as a single JSON string. Future-proof: if mcp-go ever adds an
// `isError: true` block to the result, the same parser handles it
// (the LLM harness can branch on that flag).
type toolResponseEnvelope struct {
	Result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError,omitempty"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// unwrapToolResponse pulls the inner ToolResponse JSON out of the
// mcp-go content envelope. Returns the inner ToolResponse bytes
// (i.e. the contents of content[0].text) so the caller can
// json.Unmarshal into the typed Result struct.
//
// On a top-level RPC error (no result), wraps it as a Go error so
// callers can fail fast without re-parsing.
func unwrapToolResponse(t *testing.T, raw []byte) (json.RawMessage, error) {
	t.Helper()
	var envelope toolResponseEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("envelope unmarshal: %w", err)
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("rpc error code=%d message=%q", envelope.Error.Code, envelope.Error.Message)
	}
	if len(envelope.Result.Content) == 0 {
		return nil, fmt.Errorf("no content blocks: %s", raw)
	}
	if envelope.Result.Content[0].Type != "text" {
		return nil, fmt.Errorf("unexpected content type %q (want \"text\"); mcp-go shape may have changed", envelope.Result.Content[0].Type)
	}
	return json.RawMessage(envelope.Result.Content[0].Text), nil
}

// unwrapToolResponseData is the common pattern after unwrap:
// the inner ToolResponse is `{"data": <typed>, "audit": ..., ...}`.
// This helper unmarshals both layers in one call.
//
// Returns the raw bytes of `.data` (suitable for unmarshalling
// into the caller's typed struct) and the parent ToolResponse
// envelope's `isError` flag (for callers that want to branch on
// the harness-visible error marker).
func unwrapToolResponseData(t *testing.T, raw []byte) (json.RawMessage, bool, error) {
	t.Helper()
	inner, err := unwrapToolResponse(t, raw)
	if err != nil {
		return nil, false, err
	}
	var respEnvelope struct {
		Data    json.RawMessage `json:"data"`
		IsError bool            `json:"isError,omitempty"`
	}
	if err := json.Unmarshal(inner, &respEnvelope); err != nil {
		return nil, false, fmt.Errorf("ToolResponse unmarshal: %w\n  body=%s", err, inner)
	}
	return respEnvelope.Data, respEnvelope.IsError, nil
}