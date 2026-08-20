// Package orchestration — drift_judge.go
//
// v2.20.0 T08 (spec 1276): artifact-anchored drift_judge pipeline.
// Replaces the caller-controlled Content path that the v2.19.x
// drift_judge exposed (judge_consensus.go:47, judge.go:42,
// publish_vibe.go:400-418).
//
// # Why this file exists
//
// The v2.19.x drift_judge took a `Content string` parameter from the
// caller. Any caller could submit "this drift is fine" as Content and
// the LLM would happily say "yes" — caller-controlled verdict.
//
// T08 fixes this by requiring an ArtifactRef. The orchestrator
// resolves the artifact via artifact.Resolver (T01) and scores the
// (premise=resolved body, hypothesis=spec intent) tuple through an
// nli.Provider (T05). The drift verdict is the canonical NLI label
// mapping:
//
//	entailment    → aligned       (artifact matches spec)
//	contradiction → drift_detected (artifact diverges from spec)
//	neutral       → needs_human   (model can't decide; operator review)
//
// # Backward compatibility
//
// `Judge` (in judge.go) still accepts Content for non-drift eval_types
// (brand_match, compliance_check, pii_detect, prompt_injection_scan,
// grounding_check). For drift_judge specifically, Judge emits a
// deprecation log + still runs (Phase 1 of the spec 1276 H1 ladder).
// At v2.22.0 the Content field is removed (Phase 2).
//
// # Hard invariants (sealed)
//
//  1. DriftJudge ALWAYS receives an ArtifactRef. Empty ArtifactRef
//     → errMissingField("artifact_ref").
//  2. DriftJudge ALWAYS resolves the artifact FIRST. If Resolve
//     fails, the verdict is "drift_detected" (the artifact cannot
//     match the spec if it doesn't exist) + Error Observatory record.
//  3. Score is called with premise=string(Resolved.Bytes) +
//     hypothesis=specIntent. The bytes are bounded by
//     nliCfg.MaxPremiseBytes (default 65536). If > cap →
//     ErrInputTooLarge → operator escalates.
//  4. ErrNoProvider (primary + fallback both failed) → verdict
//     "needs_human" + Error Observatory (infra, not drift).
//  5. ErrProviderBadResponse (contract bug) → verdict "needs_human"
//     + Error Observatory (model misbehaved).
//  6. The drift_judge does NOT log the artifact body or the AuthToken.
//     Latency + verdict + provider_id + SHA-256 are logged.
package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/artifact"
	"github.com/dark-agents/dark-memory-mcp/internal/errorobs"
	"github.com/dark-agents/dark-memory-mcp/internal/nli"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// DriftJudgeInput is the request to evaluate an artifact against a
// spec via the NLI pipeline. The caller MUST supply ArtifactRef — the
// drift_judge never accepts raw Content from the caller (Phase 2 of
// spec 1276 H1 removes the Content field at v2.22.0).
type DriftJudgeInput struct {
	ArtifactRef artifact.ArtifactRef // file/git_sha/url/spec_id/artifact_id
	SpecIntent  string               // the hypothesis (one-paragraph spec intent)
	EvalType    string               // "drift_judge" (default; sealed)
	TargetType  string               // "artifact" (default)
	TargetID    string               // audit grouping key
	VibeCase    string               // C1..C7 — G-Eval rubric extension
	PersonaID   string               // explicit persona id (v2.17.0)
	AgentID     string               // for agent-scoped memory enrichment
	NoEnrich    bool                 // skip prior-context enrichment
}

// DriftJudgeOutput is the canonical verdict + provenance chain.
// The Resolved SHA-256 is the source of truth — the artifact MAY be
// regenerated (different bytes) but the verdict is bound to the SHA.
type DriftJudgeOutput struct {
	VerdictJSON    string  // canonical JSON: {"verdict","confidence","reasoning","artifact_source","artifact_sha256","provider_id","nli_label","nli_confidence","model_rev"}
	Verdict        string  // "aligned" | "drift_detected" | "needs_human" | "skipped"
	Confidence     float32 // 0..1 from nli.Score; 0 if skipped
	ProviderID     string  // provenance
	ModelRev       string  // best-effort; empty if unknown
	NLILabel       string  // raw NLI label (entailment/contradiction/neutral)
	NLIConfidence  float64 // raw NLI confidence
	LatencyMS      int64   // wall-clock for the NLI Score
	ArtifactSource string  // artifact.Source.String()
	ArtifactSHA256 string  // hex
	ArtifactPath   string  // canonical identifier
	ArtifactTrunc  bool    // true if Range or MaxBytes cut off content
	Reasoning      string  // human-readable explanation
}

// DriftJudge is the artifact-anchored drift_judge pipeline. See
// package doc for the invariants.
func (o *Orchestrator) DriftJudge(ctx context.Context, in DriftJudgeInput) (*DriftJudgeOutput, error) {
	// 1. Validate (sealed: ArtifactRef is required).
	if in.EvalType == "" {
		in.EvalType = "drift_judge"
	}
	if in.EvalType != "drift_judge" {
		return nil, errMissingField("eval_type (must be drift_judge)")
	}
	if in.TargetType == "" {
		in.TargetType = "artifact"
	}
	if err := in.ArtifactRef.Validate(); err != nil {
		return nil, errMissingField("artifact_ref")
	}
	if strings.TrimSpace(in.SpecIntent) == "" {
		return nil, errMissingField("spec_intent")
	}

	// 2. Canary check (INV-3) on the spec intent. The artifact body
	// is checked post-Resolve.
	if !o.Safety.Active().IsZero() {
		if o.Safety.Active().Match(in.SpecIntent) {
			return nil, fmt.Errorf("%w: drift_judge spec_intent contains canary token", store.ErrCanaryInPayload)
		}
	}

	// 3. Resolve the artifact via artifact.Resolver.
	resolver := o.ensureDriftJudgeResolver()
	resolved, err := resolver.Resolve(ctx, in.ArtifactRef)
	if err != nil {
		// Artifact resolution failed → cannot match the spec. The
		// drift verdict is "drift_detected" (the artifact does not
		// exist in the form the caller claims). Error Observatory
		// gets a row so the operator can audit.
		o.RecordError(ctx, "drift_judge", o.activeSessionID(ctx),
			fmt.Errorf("resolve artifact: %w", err), errorobs.SeverityWarn)
		out := &DriftJudgeOutput{
			Verdict:    "drift_detected",
			Confidence: 0,
			Reasoning:  fmt.Sprintf("artifact resolution failed: %v", err),
		}
		out.VerdictJSON = formatDriftJudgeVerdictJSON(out, nil)
		return out, nil
	}

	// 4. Canary on the resolved body (post-Resolve).
	if !o.Safety.Active().IsZero() && o.Safety.Active().Match(string(resolved.Bytes)) {
		return nil, fmt.Errorf("%w: drift_judge artifact body contains canary token", store.ErrCanaryInPayload)
	}

	// 5. Resolve the NLI Provider. Returns (nil, nil) → default
	// chain (DeBERTa-only, no cache, no fallback).
	provider, err := o.EnsureNLIRouter(ctx)
	if err != nil {
		o.RecordError(ctx, "drift_judge", o.activeSessionID(ctx),
			fmt.Errorf("resolve nli provider: %w", err), errorobs.SeverityError)
		out := &DriftJudgeOutput{
			Verdict:    "needs_human",
			Confidence: 0,
			Reasoning:  fmt.Sprintf("nli provider unavailable: %v", err),
		}
		out.VerdictJSON = formatDriftJudgeVerdictJSON(out, resolved)
		return out, nil
	}
	if provider == nil {
		// T08 default: build a DeBERTa-only chain with project defaults.
		// This is the safety net: a project without NLIConfig still
		// gets a working drift_judge (or ErrNoLLMAvailable if no key).
		provider, err = o.defaultDriftJudgeProvider(ctx)
		if err != nil {
			o.RecordError(ctx, "drift_judge", o.activeSessionID(ctx),
				fmt.Errorf("default nli provider: %w", err), errorobs.SeverityError)
			out := &DriftJudgeOutput{
				Verdict:    "needs_human",
				Confidence: 0,
				Reasoning:  fmt.Sprintf("nli provider unavailable: %v", err),
			}
			out.VerdictJSON = formatDriftJudgeVerdictJSON(out, resolved)
			return out, nil
		}
	}

	// 6. Score (premise, hypothesis). The Router enforces size caps
	// (MaxPremiseBytes / MaxHypothesisBytes). Oversized artifact
	// body → ErrInputTooLarge → operator escalates.
	scoreStart := time.Now()
	score, scoreErr := provider.Score(ctx, string(resolved.Bytes), in.SpecIntent)
	scoreLatency := time.Since(scoreStart).Milliseconds()
	if scoreErr != nil {
		return o.handleScoreError(ctx, scoreErr, resolved, scoreLatency)
	}

	// 7. Map NLI label → canonical verdict.
	verdict := nliLabelToDriftVerdict(score.Label)
	confidence := float32(score.Confidence)

	out := &DriftJudgeOutput{
		Verdict:        verdict,
		Confidence:     confidence,
		ProviderID:     score.ProviderID,
		ModelRev:       score.ModelRev,
		NLILabel:       string(score.Label),
		NLIConfidence:  score.Confidence,
		LatencyMS:      score.LatencyMS,
		ArtifactSource: string(resolved.Source),
		ArtifactSHA256: hexBytes(resolved.ContentSHA256),
		ArtifactPath:   resolved.Path,
		ArtifactTrunc:  resolved.Truncated,
		Reasoning: fmt.Sprintf("nli=%s conf=%.3f prov=%s sha=%s",
			score.Label, score.Confidence, score.ProviderID, hexBytes(resolved.ContentSHA256)),
	}
	out.VerdictJSON = formatDriftJudgeVerdictJSON(out, resolved)
	return out, nil
}

// handleScoreError classifies a score error and returns a verdict
// plus a persisted Error Observatory row. The verdict is determined
// by the error class:
//
//	ErrInputTooLarge       → needs_human (artifact too big for NLI)
//	ErrProviderTimeout     → needs_human (model slow; operator retry)
//	ErrProviderUnavailable → needs_human (model down; operator retry)
//	ErrProviderRateLimited → needs_human (rate limit; operator retry)
//	ErrProviderBadResponse → needs_human (model contract bug)
//	ErrNoProvider          → needs_human (router exhausted)
//	default                → needs_human (unknown; operator review)
func (o *Orchestrator) handleScoreError(ctx context.Context, scoreErr error, resolved *artifact.Resolved, latencyMS int64) (*DriftJudgeOutput, error) {
	severity := errorobs.SeverityError
	switch {
	case errors.Is(scoreErr, nli.ErrInputTooLarge):
		severity = errorobs.SeverityWarn // operator can shrink artifact
	case errors.Is(scoreErr, nli.ErrProviderBadResponse):
		// Contract bug — model itself is wrong. Configuration fixes
		// this, not retries.
	case errors.Is(scoreErr, nli.ErrNoProvider):
		// Both providers exhausted. Operator action needed.
	}
	o.RecordError(ctx, "drift_judge", o.activeSessionID(ctx), scoreErr, severity)

	out := &DriftJudgeOutput{
		Verdict:    "needs_human",
		Confidence: 0,
		LatencyMS:  latencyMS,
		Reasoning:  fmt.Sprintf("nli score failed: %v", scoreErr),
	}
	if resolved != nil {
		out.ArtifactSource = string(resolved.Source)
		out.ArtifactSHA256 = hexBytes(resolved.ContentSHA256)
		out.ArtifactPath = resolved.Path
		out.ArtifactTrunc = resolved.Truncated
	}
	out.VerdictJSON = formatDriftJudgeVerdictJSON(out, resolved)
	return out, nil
}

// ensureDriftJudgeResolver returns the artifact.Resolver used by
// DriftJudge, constructed lazily. The Resolver is wired with the
// orchestrator's Store (for spec_id/artifact_id resolution) and the
// harness-injected URLFetcher (for URL/artifact_id fetches).
//
// The resolver is rebuilt on every call — it's a thin struct
// (Spec + URLs fields), so the allocation is cheap. We do NOT
// cache the resolver because the URLFetcher may be swapped at
// runtime (T12+ wiring).
func (o *Orchestrator) ensureDriftJudgeResolver() *artifact.Resolver {
	return &artifact.Resolver{
		Spec: NewStoreSpecLookup(o.Store),
		URLs: o.urlFetcher,
	}
}

// urlFetcher is the harness-injected URLFetcher. nil → URL/artifact_id
// resolution returns ErrNotConfigured (the resolver surfaces this).
// Wiring in T12 (MCP tool layer).
//
// The setter is nil-safe. Use WithURLFetcher to inject it.
var _ artifact.URLFetcher // interface assertion guard

// activeSessionID returns the current open session id (operator
// audit grouping). Returns "" if no active session — the Error
// Observatory tolerates empty session IDs.
func (o *Orchestrator) activeSessionID(ctx context.Context) string {
	activeProject := o.Store.ActiveProject()
	if activeProject == "" {
		return ""
	}
	sid, err := o.Store.GetActiveSession(ctx, activeProject)
	if err != nil {
		return ""
	}
	return sid
}

// nliLabelToDriftVerdict maps the canonical NLI label to the drift
// verdict. Sealed mapping:
//
//	entailment    → aligned
//	contradiction → drift_detected
//	neutral       → needs_human
//
// Other / unknown labels → needs_human (defensive: don't claim
// aligned for an unmapped label).
func nliLabelToDriftVerdict(label nli.Label) string {
	switch label {
	case nli.LabelEntailment:
		return "aligned"
	case nli.LabelContradiction:
		return "drift_detected"
	case nli.LabelNeutral:
		return "needs_human"
	default:
		return "needs_human"
	}
}

// hexBytes encodes a 32-byte SHA-256 as a 64-char hex string.
func hexBytes(b [32]byte) string {
	const hextable = "0123456789abcdef"
	out := make([]byte, 64)
	for i := 0; i < 32; i++ {
		out[i*2] = hextable[b[i]>>4]
		out[i*2+1] = hextable[b[i]&0x0f]
	}
	return string(out)
}

// formatDriftJudgeVerdictJSON produces the canonical verdict JSON
// that the judgment_history row + downstream readers consume.
// Shape:
//
//	{
//	  "verdict": "aligned",
//	  "confidence": 0.92,
//	  "reasoning": "nli=entailment conf=0.920 prov=deberta-v3-large-mnli sha=abc...",
//	  "artifact_source": "file",
//	  "artifact_sha256": "abc...",
//	  "artifact_path": "/path/to/file",
//	  "artifact_truncated": false,
//	  "provider_id": "deberta-v3-large-mnli",
//	  "model_rev": "abc123",
//	  "nli_label": "entailment",
//	  "nli_confidence": 0.92,
//	  "latency_ms": 1234
//	}
func formatDriftJudgeVerdictJSON(out *DriftJudgeOutput, resolved *artifact.Resolved) string {
	// Build a stable, lex-ordered JSON. We hand-roll to avoid
	// encoding/json dependencies in the hot path (mirrors the
	// pattern in judge_consensus.go).
	var b strings.Builder
	b.WriteString("{")
	b.WriteString(`"verdict":"`)
	b.WriteString(out.Verdict)
	b.WriteString(`",`)
	b.WriteString(`"confidence":`)
	b.WriteString(fmt.Sprintf("%.4f", out.Confidence))
	b.WriteString(",")
	if out.Reasoning != "" {
		b.WriteString(`"reasoning":`)
		writeJSONString(&b, out.Reasoning)
		b.WriteString(",")
	}
	if resolved != nil {
		if out.ArtifactSource != "" {
			b.WriteString(`"artifact_source":`)
			writeJSONString(&b, out.ArtifactSource)
			b.WriteString(",")
		}
		if out.ArtifactSHA256 != "" {
			b.WriteString(`"artifact_sha256":`)
			writeJSONString(&b, out.ArtifactSHA256)
			b.WriteString(",")
		}
		if out.ArtifactPath != "" {
			b.WriteString(`"artifact_path":`)
			writeJSONString(&b, out.ArtifactPath)
			b.WriteString(",")
		}
		b.WriteString(`"artifact_truncated":`)
		if out.ArtifactTrunc {
			b.WriteString("true,")
		} else {
			b.WriteString("false,")
		}
	}
	if out.ProviderID != "" {
		b.WriteString(`"provider_id":`)
		writeJSONString(&b, out.ProviderID)
		b.WriteString(",")
	}
	if out.ModelRev != "" {
		b.WriteString(`"model_rev":`)
		writeJSONString(&b, out.ModelRev)
		b.WriteString(",")
	}
	if out.NLILabel != "" {
		b.WriteString(`"nli_label":`)
		writeJSONString(&b, out.NLILabel)
		b.WriteString(",")
	}
	if out.NLIConfidence > 0 {
		b.WriteString(`"nli_confidence":`)
		b.WriteString(fmt.Sprintf("%.4f", out.NLIConfidence))
		b.WriteString(",")
	}
	if out.LatencyMS > 0 {
		b.WriteString(`"latency_ms":`)
		b.WriteString(fmt.Sprintf("%d", out.LatencyMS))
	}
	// Strip trailing comma if present.
	s := b.String()
	if strings.HasSuffix(s, ",") {
		s = s[:len(s)-1]
	}
	return s + "}"
}

// writeJSONString writes a JSON-escaped string value.
func writeJSONString(b *strings.Builder, s string) {
	b.WriteString(`"`)
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteString(`"`)
}
