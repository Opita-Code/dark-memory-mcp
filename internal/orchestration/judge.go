// O5: Judge — LLM-as-judge wrapper. Calls an LLMClient (selected
// via the orchestrator's LLMSelector — typically the harness's own
// LLM via env detection) and persists the verdict as an
// SDDEvaluation row in the active project.
//
// Philosophy (spec 173 O5):
//   - First instance of LLM is the same one the harness is using to
//     call the MCP tool (self-judge). Auto-detected via env vars
//     (ANTHROPIC_API_KEY / OPENAI_API_KEY / GEMINI_API_KEY /
//     DARK_SCRAPPER_URL).
//   - If no key is set anywhere, return a clear fallback error
//     pointing the operator to the env vars to set.
//   - When keys ARE set, an OSINTSelector chooses the best model
//     per eval_type (e.g. compliance_check prefers a strict model,
//     drift_judge prefers a reasoning model). The OSINT layer is
//     config-driven today and will be live-OSINT-fed in Wave 4+.
//
// Content safety (INV-3): if the active canary token is present in
// the content, Judge refuses with ErrCanaryInPayload — even when
// the LLM would happily score it.
package orchestration

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/errorobs"
	"github.com/dark-agents/dark-memory-mcp/internal/safety"
	"github.com/dark-agents/dark-memory-mcp/internal/ssd"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// JudgeInput is the request to run an LLM-as-judge call.
type JudgeInput struct {
	EvalType   string  `json:"eval_type"`   // brand_match | compliance_check | drift_judge | grounding_check | pii_detect | prompt_injection_scan | consensus
	TargetType string  `json:"target_type"` // brand | artifact | spec | claim | code | ...
	TargetID   string  `json:"target_id"`
	Content    string  `json:"content"`     // the text to evaluate
	Model      string  `json:"model,omitempty"` // optional override of the selector's pick
	// AgentID (v2.4.2) is the Mem0 agent_id (LLM identity) that owns
	// this judgment. Optional; resolved with priority (caller input >
	// projects.default_agent_id > ""). When set, brand_match and
	// compliance_check consult prior agent_memory rows authored by
	// this agent only (cross-agent leakage prevention; see v2.4.2
	// changelog). drift_judge enrichment lives in PublishVibe and
	// does NOT run here — drift_judge callers must go through
	// PublishVibe to get enriched prompts (deliberate scope).
	AgentID string `json:"agent_id,omitempty"`
	// NoEnrich (v2.4.2) is an opt-out escape hatch. Default false
	// (enrichment runs for brand_match + compliance_check). When
	// true, Judge skips the agent_memory enrichment step entirely
	// and passes the raw content to the LLM. Use this when:
	//   - the operator is testing the LLM in isolation
	//   - the content must not see prior context (sensitive audits)
	//   - debugging enrichment behavior
	NoEnrich bool `json:"no_enrich,omitempty"`
}

// JudgeOutput is the result of a Judge call.
type JudgeOutput struct {
	EvaluationID int64   `json:"evaluation_id"`
	VerdictJSON  string  `json:"verdict_json"`
	Confidence   float32 `json:"confidence"`
	Model        string  `json:"model"`
	Provider     string  `json:"provider"`
}

// Judge calls an LLM to evaluate Content against the eval_type
// schema. The verdict is persisted as an SDDEvaluation row.
//
// v2.4.2: for brand_match + compliance_check, the content is enriched
// with prior agent_memory hits (kind=decision for brand_match;
// kind=decision + finding for compliance_check) filtered by the
// resolved agent_id (priority: caller AgentID > projects.default_agent_id
// > ""). drift_judge enrichment is NOT performed here — it lives in
// PublishVibe (deliberate scope; drift_judge callers must use
// PublishVibe to get enriched prompts). NoEnrich=true opts out of
// enrichment entirely (raw content passes through).
//
// Returns:
//   - ErrInvalidArgument if Content is empty.
//   - ErrSessionRequired if no active project.
//   - ErrCanaryInPayload if Content contains the active canary.
//   - ErrNoLLMAvailable if no LLM client is available.
//   - The underlying error from the LLM client otherwise.
func (o *Orchestrator) Judge(ctx context.Context, in JudgeInput) (*JudgeOutput, error) {
	if strings.TrimSpace(in.Content) == "" {
		return nil, errMissingField("content")
	}
	if strings.TrimSpace(in.EvalType) == "" {
		return nil, errMissingField("eval_type")
	}

	// Canary check (INV-3). Refuse payload with canary token even
	// before calling the LLM.
	if !o.Safety.Active().IsZero() && o.Safety.Active().Match(in.Content) {
		return nil, fmt.Errorf("%w: judge content contains canary token", store.ErrCanaryInPayload)
	}

	// v2.4.2: enrich content with prior agent_memory hits for
	// brand_match + compliance_check (judges are blind otherwise).
	// Resolves agent_id via the v2.4.1 priority chain. Best-effort:
	// errors are swallowed — Judge runs even if agent_memory is
	// broken (same fail-safe contract as v2.4.0 drift_judge
	// enrichment in PublishVibe). NoEnrich=true opts out.
	//
	// Strategy: PINNED memories (not BM25). Brand and compliance
	// decisions are operator-curated — the operator pinned them
	// because they're important. BM25 against the artifact text
	// is fragile for these judges (compliance text rarely shares
	// keywords with the artifact under review). Pinned gives
	// predictable, curated context. Drift_judge keeps BM25 in
	// PublishVibe (different code path) because drift detection
	// benefits from SPEC-relevant context.
	if !in.NoEnrich {
		if kinds := kindsForEnrichment(in.EvalType); len(kinds) > 0 {
			activeAgentID := o.resolveActiveAgentID(ctx, in.AgentID)
			in.Content = o.enrichForVibePinned(ctx, in.Content, kinds, activeAgentID, 5)
		}
	}

	// Pick the LLM client via the selector.
	selector := o.ensureLLMSelector()
	client, err := selector.Select(ctx, in.EvalType)
	if err != nil {
		return nil, fmt.Errorf("judge: select llm: %w", err)
	}

	// Resolve the model. Caller override > OSINT catalog recommendation
	// (only if the provider is in the catalog) > empty (client
	// auto-configures).
	model := in.Model
	if model == "" {
		if provider := providerFromName(client.Name()); provider != "" {
			model = selector.RecommendedModelFor(provider, in.EvalType)
		}
	}

	// Build the judge request.
	req := JudgeRequest{
		EvalType:   in.EvalType,
		TargetType: in.TargetType,
		TargetID:   in.TargetID,
		Content:    in.Content,
		Model:      model,
	}

	resp, err := client.Judge(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("judge: llm call: %w", err)
	}

	// Persist SDDEvaluation.
	wc := store.WriteContext{
		Actor:     "orchestrator_judge",
		WritePath: "Judge",
	}
	now := o.now().Format(time.RFC3339Nano)
	eval := &ssd.SDDEvaluation{
		EvalType:      in.EvalType,
		TargetType:    in.TargetType,
		TargetID:      in.TargetID,
		VerdictJSON:   resp.VerdictJSON,
		Confidence:    resp.Confidence,
		Model:         resp.Model,
		ConstitutionID: wc.ConstitutionID,
		CreatedAt:     now,
	}
	evalID, err := o.Store.SaveSDDEvaluation(ctx, wc, eval)
	if err != nil {
		return nil, fmt.Errorf("judge: save sdd evaluation: %w", err)
	}

	return &JudgeOutput{
		EvaluationID: evalID,
		VerdictJSON:  resp.VerdictJSON,
		Confidence:   resp.Confidence,
		Model:        resp.Model,
		Provider:     resp.Provider,
	}, nil
}

// Void-import guards for future use.
var _ = safety.Holder{}

// providerFromName extracts the provider key from an LLMClient name.
// Convention: LLMClient names are "self_harness_<provider>" or
// "darkscrapper_<provider>" or arbitrary (mock_*, etc.). When the
// name doesn't follow the convention, returns "" — caller falls
// through to client auto-config.
func providerFromName(name string) string {
	const prefix = "self_harness_"
	if len(name) > len(prefix) && name[:len(prefix)] == prefix {
		return name[len(prefix):]
	}
	const dsPrefix = "darkscrapper_"
	if len(name) > len(dsPrefix) && name[:len(dsPrefix)] == dsPrefix {
		return name[len(dsPrefix):]
	}
	return ""
}

// kindsForEnrichment returns the agent_memory kinds to inject as
// prior context for a given eval_type. Empty slice means no
// enrichment (eval_type is pattern-matching or out-of-scope).
//
// v2.4.2: only brand_match + compliance_check are enriched here.
// drift_judge enrichment lives in PublishVibe (v2.4.0) — that
// integration is tested and stable, so we do not duplicate it
// (single code path per judge). pii_detect + prompt_injection_scan
// are pattern-matching and don't benefit from RAG.
// grounding_check is out-of-scope for v2.4.2 (no operator ask).
//
// Returns a fresh slice (caller may mutate without affecting other
// eval_types).
func kindsForEnrichment(evalType string) []string {
	switch evalType {
	case "brand_match":
		// Brand decisions are the canonical "this brand says X" knowledge.
		return []string{"decision"}
	case "compliance_check":
		// Compliance decisions (jurisdictional rules) + findings
		// (previously flagged issues) — both relevant to compliance.
		return []string{"decision", "finding"}
	default:
		// drift_judge (lives in PublishVibe), pii_detect, prompt_injection_scan,
		// grounding_check, and unknown eval_types: no enrichment here.
		return nil
	}
}

// enrichForVibePinned prepends pinned agent_memory hits of the
// supplied kinds (filtered by agent_id) to the base content. Used
// by v2.4.2 brand_match + compliance_check enrichment.
//
// Why PINNED instead of BM25 search (which v2.4.0 drift_judge uses)?
//   - Brand decisions are operator-curated: the operator pinned
//     them because they're the brand canon. Pinned = explicit
//     operator intent, not just text similarity.
//   - Compliance decisions + findings are similar: operator pinned
//     them because they're jurisdictional canon.
//   - BM25 against the artifact text is fragile for these judges:
//     a compliance decision like "GDPR Article 13 disclosure" rarely
//     shares keywords with the artifact copy being reviewed. Pinned
//     gives predictable, curated context. Drift_judge keeps BM25
//     (in PublishVibe) because drift detection benefits from
//     SPEC-relevant context, not pinned canon.
//
// Best-effort: errors are swallowed — Judge runs even if agent_memory
// is broken (same fail-safe contract as v2.4.0 enrichment). If no
// pinned rows match, returns base unchanged.
func (o *Orchestrator) enrichForVibePinned(ctx context.Context, base string, kinds []string, agentID string, limit int) string {
	if strings.TrimSpace(base) == "" {
		return base
	}
	if len(kinds) == 0 {
		return base
	}
	if limit <= 0 {
		limit = 5
	}
	// Collect pinned rows across all kinds, up to limit total.
	var rows []agentmemory.AgentMemory
	for _, k := range kinds {
		pinned, err := o.listPinnedForVibe(ctx, k, agentID, limit)
		if err != nil {
			// Best-effort: log via the silence and continue. If
			// agent_memory is broken, return base unchanged (the
			// caller will see raw content).
			// v2.11.0 (spec 757): was silent; now durable (Warn —
			// enrichment degraded, judge still runs).
			o.RecordError(ctx, "judge", "", fmt.Errorf("enrichForVibePinned kind=%s: %w", k, err), errorobs.SeverityWarn)
			return base
		}
		rows = append(rows, pinned...)
		if len(rows) >= limit {
			break
		}
	}
	if len(rows) == 0 {
		return base
	}
	// Trim to limit (since we may have collected more than limit
	// across kinds).
	if len(rows) > limit {
		rows = rows[:limit]
	}
	// Convert AgentMemory rows to SearchHit so we can reuse
	// formatHitsForContext (same output shape as v2.4.0).
	hits := make([]agentmemory.SearchHit, 0, len(rows))
	for _, r := range rows {
		hits = append(hits, agentmemory.SearchHit{
			AgentMemory: r,
			Rank:        0, // not relevant for pinned (not a search)
		})
	}
	block := formatHitsForContext(hits)
	if block == "" {
		return base
	}
	return block + "\n\n=== Artifact under review ===\n\n" + base
}