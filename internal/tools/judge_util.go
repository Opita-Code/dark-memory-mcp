// Package tools — judge_util.go: the 7 JUDGE_UTIL tools exposed in v3
// single-binary mode (Phase 11 T-402, 2026-10-03).
//
// The 7 deterministic primitives that complement the LLM-backed judge
// pipeline. They live in `internal/v4alpha/judge` (the canonical
// implementation, also wired into the v4alpha MCP server) — this file
// is a thin v3 wrapper that exposes them via the v3 `Registry` so
// the v3 single-binary mode gets the same surface (closes OD7
// tests (d) + (i) — they were "⚠️ partial" because the v4alpha MCP
// server was only reachable with DARK_MEM_BRIDGE=v4alpha).
//
// Wire shape matches `internal/v4alpha/transport/mcp/judge_util.go`
// byte-for-byte where the surface overlaps, so a v3 caller can use
// these tools interchangeably with v4alpha callers.
//
// Tools (all read-only, pure, deterministic except T5 which uses
// crypto/rand for trace IDs):
//
//   - dark_memory_judge_util_normalize         → T5 normalizer
//   - dark_memory_judge_util_validate_overrides → 10 override patterns
//   - dark_memory_judge_util_pattern_descriptions → list OP-1..OP-10
//   - dark_memory_judge_util_verify            → Ed25519 signature
//   - dark_memory_judge_util_verify_hash       → SHA-256 hash
//   - dark_memory_judge_util_trace             → W3C trace context
//   - dark_memory_judge_util_validate_trace    → validate traceparent
//
// No audit row emitted (the operator is inspecting state, not
// changing it). The judge pipeline itself uses the same primitives
// internally (judge.Pipeline.Evaluate calls ValidateOverrides on
// every verdict for the EC-003 short-circuit).
package tools

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/judge"
)

// RegisterJudgeUtil wires the 7 JUDGE_UTIL tools into the registry.
// Pure functions — no Store, no Orchestrator, no Audit needed.
func RegisterJudgeUtil(reg *Registry) {
	reg.Add(registerJudgeUtilNormalize())
	reg.Add(registerJudgeUtilValidateOverrides())
	reg.Add(registerJudgeUtilPatternDescriptions())
	reg.Add(registerJudgeUtilVerify())
	reg.Add(registerJudgeUtilVerifyHash())
	reg.Add(registerJudgeUtilTrace())
	reg.Add(registerJudgeUtilValidateTrace())
}

// ---------- normalize ----------

type judgeUtilNormalizeInput struct {
	Text string `json:"text"`
}

type judgeUtilNormalizeOutput struct {
	Normalized string `json:"normalized"`
}

func registerJudgeUtilNormalize() *Tool {
	return BindSimple(
		"judge_util_normalize",
		"Run text through the T5 normalization pipeline (NFKC + zero-width strip + unicode-escape decode + Cyrillic homoglyph map + lowercase + whitespace collapse). Operator-facing primitive for inspecting what the validator sees. Deterministic and pure.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"text"},
			"properties": map[string]any{
				"text": map[string]any{"type": "string", "description": "Text to normalize"},
			},
		}),
		func(ctx context.Context, raw json.RawMessage) (*ToolResponse, error) {
			var in judgeUtilNormalizeInput
			if err := json.Unmarshal(raw, &in); err != nil {
				return &ToolResponse{Error: &ToolError{Code: "ErrInvalidArgument", Message: err.Error()}}, nil
			}
			if in.Text == "" {
				return &ToolResponse{Error: &ToolError{Code: "ErrInvalidArgument", Message: "text is required"}}, nil
			}
			return &ToolResponse{Data: judgeUtilNormalizeOutput{Normalized: judge.NormalizeT5(in.Text)}}, nil
		},
	)
}

// ---------- validate_overrides ----------

type judgeUtilValidateOverInput struct {
	Text string `json:"text"`
}

type judgeUtilValidateOverOutput struct {
	Hits []judgeUtilOverrideHitJSON `json:"hits"`
}

type judgeUtilOverrideHitJSON struct {
	PatternID   string `json:"pattern_id"`
	Pattern     string `json:"pattern"`
	Severity    string `json:"severity"`
	Description string `json:"description"`
	Snippet     string `json:"snippet"`
}

func registerJudgeUtilValidateOverrides() *Tool {
	return BindSimple(
		"judge_util_validate_overrides",
		"Scan text for the 10 prompt-injection override patterns. Returns the list of hits (each is a pattern id, severity, description, and a 40-char normalized context snippet). Severity=block triggers EC-003 short-circuit in the judge pipeline.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"text"},
			"properties": map[string]any{
				"text": map[string]any{"type": "string", "description": "Text to scan for the 10 override patterns"},
			},
		}),
		func(ctx context.Context, raw json.RawMessage) (*ToolResponse, error) {
			var in judgeUtilValidateOverInput
			if err := json.Unmarshal(raw, &in); err != nil {
				return &ToolResponse{Error: &ToolError{Code: "ErrInvalidArgument", Message: err.Error()}}, nil
			}
			if in.Text == "" {
				return &ToolResponse{Error: &ToolError{Code: "ErrInvalidArgument", Message: "text is required"}}, nil
			}
			hits, err := judge.ValidateOverrides(in.Text)
			if err != nil {
				return &ToolResponse{Error: &ToolError{Code: "ErrInternal", Message: fmt.Sprintf("validate: %v", err)}}, nil
			}
			out := judgeUtilValidateOverOutput{Hits: make([]judgeUtilOverrideHitJSON, 0, len(hits))}
			for _, h := range hits {
				out.Hits = append(out.Hits, judgeUtilOverrideHitJSON{
					PatternID:   h.PatternID,
					Pattern:     h.Pattern,
					Severity:    h.Severity,
					Description: h.Description,
					Snippet:     h.Snippet,
				})
			}
			return &ToolResponse{Data: out}, nil
		},
	)
}

// ---------- pattern_descriptions ----------

type judgeUtilPatternDescOutput struct {
	Patterns []judgeUtilOverridePatternJSON `json:"patterns"`
}

type judgeUtilOverridePatternJSON struct {
	ID          string `json:"id"`
	Pattern     string `json:"pattern"`
	Severity    string `json:"severity"`
	Description string `json:"description"`
}

func registerJudgeUtilPatternDescriptions() *Tool {
	return BindSimple(
		"judge_util_pattern_descriptions",
		"List the canonical 10 prompt-injection override patterns (OP-1..OP-10) with descriptions. Stable across versions.",
		MustJSONSchema(map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
		func(ctx context.Context, raw json.RawMessage) (*ToolResponse, error) {
			out := judgeUtilPatternDescOutput{Patterns: make([]judgeUtilOverridePatternJSON, 0, 10)}
			for _, p := range judge.PatternDescriptions() {
				out.Patterns = append(out.Patterns, judgeUtilOverridePatternJSON{
					ID:          p.ID,
					Pattern:     p.Pattern,
					Severity:    p.Severity,
					Description: p.Description,
				})
			}
			return &ToolResponse{Data: out}, nil
		},
	)
}

// ---------- verify (Ed25519) ----------

type judgeUtilVerifyInput struct {
	Content   string `json:"content"`
	PublicKey string `json:"public_key"`
	Signature string `json:"signature"`
}

type judgeUtilVerifyOutput struct {
	Valid bool `json:"valid"`
}

func registerJudgeUtilVerify() *Tool {
	return BindSimple(
		"judge_util_verify",
		"Verify an Ed25519 signature over content. Returns valid=true on a clean match, valid=false otherwise. Malformed inputs return an error envelope.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"content", "public_key", "signature"},
			"properties": map[string]any{
				"content":   map[string]any{"type": "string", "description": "Raw content bytes (text) that were signed"},
				"public_key": map[string]any{"type": "string", "description": "Ed25519 public key as 64-char hex (32 bytes)"},
				"signature":  map[string]any{"type": "string", "description": "Ed25519 signature as base64 (64 bytes decoded)"},
			},
		}),
		func(ctx context.Context, raw json.RawMessage) (*ToolResponse, error) {
			var in judgeUtilVerifyInput
			if err := json.Unmarshal(raw, &in); err != nil {
				return &ToolResponse{Error: &ToolError{Code: "ErrInvalidArgument", Message: err.Error()}}, nil
			}
			if in.Content == "" || in.PublicKey == "" || in.Signature == "" {
				return &ToolResponse{Error: &ToolError{Code: "ErrInvalidArgument", Message: "content, public_key, signature are all required"}}, nil
			}
			if _, err := hex.DecodeString(in.PublicKey); err != nil {
				return &ToolResponse{Error: &ToolError{Code: "ErrInvalidArgument", Message: fmt.Sprintf("public_key is not hex: %v", err)}}, nil
			}
			if _, err := base64.StdEncoding.DecodeString(in.Signature); err != nil {
				return &ToolResponse{Error: &ToolError{Code: "ErrInvalidArgument", Message: fmt.Sprintf("signature is not base64: %v", err)}}, nil
			}
			if err := judge.VerifySignature(in.Content, in.PublicKey, in.Signature); err != nil {
				return &ToolResponse{Data: judgeUtilVerifyOutput{Valid: false}}, nil
			}
			return &ToolResponse{Data: judgeUtilVerifyOutput{Valid: true}}, nil
		},
	)
}

// ---------- verify_hash (SHA-256) ----------

type judgeUtilVerifyHashInput struct {
	Content string `json:"content"`
	HashHex string `json:"hash_hex"`
}

type judgeUtilVerifyHashOutput struct {
	Match bool `json:"match"`
}

func registerJudgeUtilVerifyHash() *Tool {
	return BindSimple(
		"judge_util_verify_hash",
		"Verify that the SHA-256 of content matches the supplied hex digest. Returns match=true on a clean match, match=false otherwise.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"content", "hash_hex"},
			"properties": map[string]any{
				"content":  map[string]any{"type": "string", "description": "Raw content bytes (text)"},
				"hash_hex": map[string]any{"type": "string", "description": "Expected SHA-256 hash as 64-char lowercase hex (256 bits)"},
			},
		}),
		func(ctx context.Context, raw json.RawMessage) (*ToolResponse, error) {
			var in judgeUtilVerifyHashInput
			if err := json.Unmarshal(raw, &in); err != nil {
				return &ToolResponse{Error: &ToolError{Code: "ErrInvalidArgument", Message: err.Error()}}, nil
			}
			if in.Content == "" || in.HashHex == "" {
				return &ToolResponse{Error: &ToolError{Code: "ErrInvalidArgument", Message: "content and hash_hex are required"}}, nil
			}
			if err := judge.VerifyHash(in.Content, in.HashHex); err != nil {
				return &ToolResponse{Data: judgeUtilVerifyHashOutput{Match: false}}, nil
			}
			return &ToolResponse{Data: judgeUtilVerifyHashOutput{Match: true}}, nil
		},
	)
}

// ---------- trace (W3C) ----------

type judgeUtilTraceOutput struct {
	TraceID     string `json:"trace_id"`
	SpanID      string `json:"span_id"`
	TraceFlags  string `json:"trace_flags"`
	TraceParent string `json:"traceparent"`
}

func registerJudgeUtilTrace() *Tool {
	return BindSimple(
		"judge_util_trace",
		"Generate a fresh W3C Trace Context. Returns trace_id (32 hex), span_id (16 hex), trace_flags (01=sampled), and the canonical traceparent header.",
		MustJSONSchema(map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
		func(ctx context.Context, raw json.RawMessage) (*ToolResponse, error) {
			tc, err := judge.NewTraceContext()
			if err != nil {
				return &ToolResponse{Error: &ToolError{Code: "ErrInternal", Message: fmt.Sprintf("trace: %v", err)}}, nil
			}
			return &ToolResponse{Data: judgeUtilTraceOutput{
				TraceID:     tc.TraceID,
				SpanID:      tc.SpanID,
				TraceFlags:  tc.TraceFlags,
				TraceParent: tc.TraceParent,
			}}, nil
		},
	)
}

// ---------- validate_trace ----------

type judgeUtilValidateTraceInput struct {
	TraceParent string `json:"traceparent"`
}

type judgeUtilValidateTraceOutput struct {
	Valid bool `json:"valid"`
}

func registerJudgeUtilValidateTrace() *Tool {
	return BindSimple(
		"judge_util_validate_trace",
		"Validate a W3C traceparent string against the grammar. Returns valid=true on match. The W3C spec is case-insensitive for hex.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"traceparent"},
			"properties": map[string]any{
				"traceparent": map[string]any{"type": "string", "description": "W3C traceparent header to validate"},
			},
		}),
		func(ctx context.Context, raw json.RawMessage) (*ToolResponse, error) {
			var in judgeUtilValidateTraceInput
			if err := json.Unmarshal(raw, &in); err != nil {
				return &ToolResponse{Error: &ToolError{Code: "ErrInvalidArgument", Message: err.Error()}}, nil
			}
			if in.TraceParent == "" {
				return &ToolResponse{Error: &ToolError{Code: "ErrInvalidArgument", Message: "traceparent is required"}}, nil
			}
			if err := judge.ValidateTraceParent(in.TraceParent); err != nil {
				return &ToolResponse{Data: judgeUtilValidateTraceOutput{Valid: false}}, nil
			}
			return &ToolResponse{Data: judgeUtilValidateTraceOutput{Valid: true}}, nil
		},
	)
}