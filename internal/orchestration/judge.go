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