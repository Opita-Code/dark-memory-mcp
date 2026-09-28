// judge_util_* tools — the v4-alpha operator-facing utility surface
// (BUG-10 10a). These are the 6 deterministic primitives that
// complement the LLM-backed judge pipeline. Every tool is pure
// (no I/O, no time except trace IDs): the operator can rely on
// them for verification without worrying about side effects.
//
//   - dark_memory_judge_util_normalize         → T5 normalizer
//   - dark_memory_judge_util_validate_overrides → 10 override patterns
//   - dark_memory_judge_util_pattern_descriptions → list OP-1..OP-10
//   - dark_memory_judge_util_verify            → Ed25519 signature
//   - dark_memory_judge_util_verify_hash       → SHA-256 hash
//   - dark_memory_judge_util_trace             → W3C trace context
//   - dark_memory_judge_util_validate_trace    → validate traceparent
//
// All tools are read-only and emit no audit row (the operator is
// inspecting state, not changing it). The judge pipeline itself
// uses the same primitives internally (judge.Pipeline.Evaluate
// calls ValidateOverrides on every verdict for the EC-003
// short-circuit).
package mcp

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/judge"
)

const (
	judgeUtilNormalizeToolName     = "dark_memory_judge_util_normalize"
	judgeUtilValidateOverToolName  = "dark_memory_judge_util_validate_overrides"
	judgeUtilPatternDescToolName   = "dark_memory_judge_util_pattern_descriptions"
	judgeUtilVerifyToolName        = "dark_memory_judge_util_verify"
	judgeUtilVerifyHashToolName    = "dark_memory_judge_util_verify_hash"
	judgeUtilTraceToolName         = "dark_memory_judge_util_trace"
	judgeUtilValidateTraceToolName = "dark_memory_judge_util_validate_trace"
)

func registerJudgeUtilTools(s *Server) {
	registerJudgeUtilNormalize(s)
	registerJudgeUtilValidateOverrides(s)
	registerJudgeUtilPatternDescriptions(s)
	registerJudgeUtilVerify(s)
	registerJudgeUtilVerifyHash(s)
	registerJudgeUtilTrace(s)
	registerJudgeUtilValidateTrace(s)
}

// ---------- normalize ----------

type judgeUtilNormalizeInput struct {
	Text string `json:"text" jsonschema:"required" jsonschema_description:"Text to run through the T5 pipeline (NFKC + zero-width strip + unicode-escape decode + Cyrillic homoglyph map + lowercase + whitespace collapse)"`
}

type judgeUtilNormalizeOutput struct {
	Normalized string `json:"normalized"`
}

func registerJudgeUtilNormalize(s *Server) {
	tool := mcp.NewTool(judgeUtilNormalizeToolName,
		mcp.WithDescription("Run text through the T5 normalization pipeline. Operator-facing primitive for inspecting what the validator sees. Deterministic and pure."),
		mcp.WithInputSchema[judgeUtilNormalizeInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = ctx
		var in judgeUtilNormalizeInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if in.Text == "" {
			return mcp.NewToolResultError("text is required"), nil
		}
		return resultJSON(judgeUtilNormalizeOutput{Normalized: judge.NormalizeT5(in.Text)})
	})
}

// ---------- validate_overrides ----------

type judgeUtilValidateOverInput struct {
	Text string `json:"text" jsonschema:"required" jsonschema_description:"Text to scan for the 10 override patterns"`
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

func registerJudgeUtilValidateOverrides(s *Server) {
	tool := mcp.NewTool(judgeUtilValidateOverToolName,
		mcp.WithDescription("Scan text for the 10 prompt-injection override patterns. Returns the list of hits (each is a pattern id, severity, description, and a 40-char normalized context snippet). Severity=block triggers EC-003 short-circuit in the judge pipeline."),
		mcp.WithInputSchema[judgeUtilValidateOverInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = ctx
		var in judgeUtilValidateOverInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if in.Text == "" {
			return mcp.NewToolResultError("text is required"), nil
		}
		hits, err := judge.ValidateOverrides(in.Text)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("validate: %v", err)), nil
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
		return resultJSON(out)
	})
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

func registerJudgeUtilPatternDescriptions(s *Server) {
	tool := mcp.NewTool(judgeUtilPatternDescToolName,
		mcp.WithDescription("List the canonical 10 prompt-injection override patterns (OP-1..OP-10) with descriptions. Stable across versions."),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = ctx
		out := judgeUtilPatternDescOutput{Patterns: make([]judgeUtilOverridePatternJSON, 0, 10)}
		for _, p := range judge.PatternDescriptions() {
			out.Patterns = append(out.Patterns, judgeUtilOverridePatternJSON{
				ID:          p.ID,
				Pattern:     p.Pattern,
				Severity:    p.Severity,
				Description: p.Description,
			})
		}
		return resultJSON(out)
	})
}

// ---------- verify (Ed25519) ----------

type judgeUtilVerifyInput struct {
	Content   string `json:"content" jsonschema:"required" jsonschema_description:"Raw content bytes (text) that were signed"`
	PublicKey string `json:"public_key" jsonschema:"required" jsonschema_description:"Ed25519 public key as 64-char hex (32 bytes)"`
	Signature string `json:"signature" jsonschema:"required" jsonschema_description:"Ed25519 signature as base64 (64 bytes decoded)"`
}

type judgeUtilVerifyOutput struct {
	Valid bool `json:"valid"`
}

func registerJudgeUtilVerify(s *Server) {
	tool := mcp.NewTool(judgeUtilVerifyToolName,
		mcp.WithDescription("Verify an Ed25519 signature over content. Returns valid=true on a clean match, valid=false otherwise. Malformed inputs return an error envelope."),
		mcp.WithInputSchema[judgeUtilVerifyInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = ctx
		var in judgeUtilVerifyInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if in.Content == "" || in.PublicKey == "" || in.Signature == "" {
			return mcp.NewToolResultError("content, public_key, signature are all required"), nil
		}
		if _, err := hex.DecodeString(in.PublicKey); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("public_key is not hex: %v", err)), nil
		}
		if _, err := base64.StdEncoding.DecodeString(in.Signature); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("signature is not base64: %v", err)), nil
		}
		if err := judge.VerifySignature(in.Content, in.PublicKey, in.Signature); err != nil {
			return resultJSON(judgeUtilVerifyOutput{Valid: false})
		}
		return resultJSON(judgeUtilVerifyOutput{Valid: true})
	})
}

// ---------- verify_hash (SHA-256) ----------

type judgeUtilVerifyHashInput struct {
	Content string `json:"content" jsonschema:"required" jsonschema_description:"Raw content bytes (text)"`
	HashHex string `json:"hash_hex" jsonschema:"required" jsonschema_description:"Expected SHA-256 hash as 64-char lowercase hex (256 bits)"`
}

type judgeUtilVerifyHashOutput struct {
	Match bool `json:"match"`
}

func registerJudgeUtilVerifyHash(s *Server) {
	tool := mcp.NewTool(judgeUtilVerifyHashToolName,
		mcp.WithDescription("Verify that the SHA-256 of content matches the supplied hex digest. Returns match=true on a clean match, match=false otherwise."),
		mcp.WithInputSchema[judgeUtilVerifyHashInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = ctx
		var in judgeUtilVerifyHashInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if in.Content == "" || in.HashHex == "" {
			return mcp.NewToolResultError("content and hash_hex are required"), nil
		}
		if err := judge.VerifyHash(in.Content, in.HashHex); err != nil {
			return resultJSON(judgeUtilVerifyHashOutput{Match: false})
		}
		return resultJSON(judgeUtilVerifyHashOutput{Match: true})
	})
}

// ---------- trace (W3C) ----------

type judgeUtilTraceOutput struct {
	TraceID     string `json:"trace_id"`
	SpanID      string `json:"span_id"`
	TraceFlags  string `json:"trace_flags"`
	TraceParent string `json:"traceparent"`
}

func registerJudgeUtilTrace(s *Server) {
	tool := mcp.NewTool(judgeUtilTraceToolName,
		mcp.WithDescription("Generate a fresh W3C Trace Context. Returns trace_id (32 hex), span_id (16 hex), trace_flags (01=sampled), and the canonical traceparent header."),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = ctx
		tc, err := judge.NewTraceContext()
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("trace: %v", err)), nil
		}
		return resultJSON(judgeUtilTraceOutput{
			TraceID:     tc.TraceID,
			SpanID:      tc.SpanID,
			TraceFlags:  tc.TraceFlags,
			TraceParent: tc.TraceParent,
		})
	})
}

// ---------- validate_trace ----------

type judgeUtilValidateTraceInput struct {
	TraceParent string `json:"traceparent" jsonschema:"required" jsonschema_description:"W3C traceparent header to validate"`
}

type judgeUtilValidateTraceOutput struct {
	Valid bool `json:"valid"`
}

func registerJudgeUtilValidateTrace(s *Server) {
	tool := mcp.NewTool(judgeUtilValidateTraceToolName,
		mcp.WithDescription("Validate a W3C traceparent string against the grammar. Returns valid=true on match. The W3C spec is case-insensitive for hex."),
		mcp.WithInputSchema[judgeUtilValidateTraceInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = ctx
		var in judgeUtilValidateTraceInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if in.TraceParent == "" {
			return mcp.NewToolResultError("traceparent is required"), nil
		}
		if err := judge.ValidateTraceParent(in.TraceParent); err != nil {
			return resultJSON(judgeUtilValidateTraceOutput{Valid: false})
		}
		return resultJSON(judgeUtilValidateTraceOutput{Valid: true})
	})
}
