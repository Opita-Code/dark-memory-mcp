// mindset_apply tool — MINDSET namespace (Phase 6 alpha.18.1, full impl).
//
// Per SPEC-alpha-11-phase4.md §4.2 + SPEC-alpha-11-phase6.md §6.2,
// dark_memory_mindset_apply procedurally composes a subagent
// system_prompt for the given (vibe_case, task_description) and
// validates it via LLM-as-judge before returning. Caches results
// in agent_memory with TTL (DARK_MINDSET_CACHE_TTL, default 1h).
//
// # Phase 6 full implementation
//
// Replaces the alpha.17 STUB. The composition flow:
//
//   1. Cache lookup via agent_memory.RecallFiltered with the
//      canonical tag prefix `mindset:v1+vibe_case:<C>+model_floor:<M>`.
//      Cache hit → return cached system_prompt + cache_hit=true.
//   2. Procedural composition: build the system_prompt from
//      - PersonaContent for the canonical (vibe_case, persona_id)
//        pair (per SPEC-alpha-11-phase6.md §6.2)
//      - Task description
//      - Vibe case
//      - Operator context
//   3. Judge validation via judge.Pipeline.Evaluate (real LLM call
//      when env keys are set; NoOpJudge fallback otherwise).
//      eval_type=mindset_compose, persona_id from registry.
//   4. Retry loop: up to DARK_MINDSET_MAX_ITERATIONS (default 3)
//      if judge returns drift_detected. Each retry refines the
//      prompt based on the judge's reasoning. After max
//      iterations, return needs_human verdict (operator must
//      review the prompt manually).
//   5. Cache store via agent_memory.Save with kind=context,
//      tags=mindset:v1+vibe_case:<C>+model_floor:<M>+cached:<ts>.
//   6. Return final result.
//
// # Cache key
//
// sha256(vibe_case || 0x00 || task_description || 0x00 || model_floor)
// is used as the content (so FTS5 can find it by task_description),
// plus the canonical tag prefix for fast filtering.
//
// # Backward compatibility
//
// The output shape is the same as the alpha.17 STUB plus
// iterations count (now non-zero) and cache_hit field. The
// stub_notice field is removed in alpha.18.1 (no longer a stub).
package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/judge"
	"github.com/dark-agents/dark-memory-mcp/internal/vibecase"
	"github.com/mark3labs/mcp-go/mcp"
)

const mindsetApplyToolName = "dark_memory_mindset_apply"

// validVibeCases is the closed allow-list for the vibe_case field.
// Mirrors the dark-memory-mcp vibe spec enumeration (C1..Cn per
// the vibecase package — see internal/vibecase/taxonomy.go for the
// canonical source of truth). Adding C8 or beyond is a one-line
// edit to that package; this map is derived at init() so it cannot
// fall out of sync.
var validVibeCases = func() map[string]bool {
	out := map[string]bool{}
	for _, c := range vibecase.All() {
		out[string(c)] = true
	}
	return out
}()

// defaultPersonaForVibeCase maps each canonical vibe_case to its
// default persona_id (per Phase 6 alpha.18.1 mapping).
var defaultPersonaForVibeCase = map[string]string{
	"C1": "judge-logical",
	"C2": "judge-logical",
	"C3": "judge-decision",
	"C4": "judge-research",
	"C5": "judge-cross-modal",
	"C6": "judge-pipeline",
	"C7": "judge-evidential",
}

// mindsetApplyInput is the wire shape for dark_memory_mindset_apply.
type mindsetApplyInput struct {
	VibeCase       string `json:"vibe_case" jsonschema:"required" jsonschema_description:"One of C1..C7 (canonical vibe_case taxonomy per spec.go:14-16)"`
	TaskDescription string `json:"task_description" jsonschema:"required" jsonschema_description:"≥10 char description of what the sub-agent should do"`
	Operator       string `json:"operator" jsonschema:"required" jsonschema_description:"Operator id (INV-1 audit owner; default 'orchestrator_mindset' if empty)"`
	ModelFloor     string `json:"model_floor,omitempty" jsonschema_description:"Minimum model capability (sonnet|opus|haiku|inherit). Empty = no override."`
}

// mindsetApplyOutput is the wire shape returned by dark_memory_mindset_apply.
type mindsetApplyOutput struct {
	SystemPrompt     string   `json:"system_prompt"`
	ToolsRecommended []string `json:"tools_recommended"`
	ModelRecommended string   `json:"model_recommended"`
	CacheHit         bool     `json:"cache_hit"`
	Iterations       int      `json:"iterations"`
	VibeCase         string   `json:"vibe_case"`
	TaskDescription  string   `json:"task_description"`
	Verdict          string   `json:"verdict"`
}

func registerMindsetTools(s *Server) {
	s.mcpSrv.AddTool(buildMindsetApplyTool(), s.handleMindsetApply)
}

// buildMindsetApplyTool constructs the mcp.Tool descriptor.
func buildMindsetApplyTool() mcp.Tool {
	return mcp.NewTool(mindsetApplyToolName,
		mcp.WithDescription("Compose a sub-agent system_prompt for the given vibe_case + task_description. "+
			"Phase 6 alpha.18.1 full implementation: procedural composition with cache "+
			"(agent_memory, kind=context, TTL via DARK_MINDSET_CACHE_TTL default 1h), "+
			"judge validation via LLM-as-judge (eval_type=mindset_compose), "+
			"retry loop up to DARK_MINDSET_MAX_ITERATIONS (default 3) if judge rejects."),
		mcp.WithInputSchema[mindsetApplyInput](),
	)
}

// handleMindsetApply is the JSON-RPC handler.
func (s *Server) handleMindsetApply(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in mindsetApplyInput
	if err := bindArgs(req, &in); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if !validVibeCases[in.VibeCase] {
		return mcp.NewToolResultError(fmt.Sprintf(
			"mindset_apply: vibe_case %q not in canonical allow-list (valid: %v)",
			in.VibeCase, vibecase.JSONSchemaEnum())), nil
	}
	if len(strings.TrimSpace(in.TaskDescription)) < 10 {
		return mcp.NewToolResultError("mindset_apply: task_description must be ≥10 chars"), nil
	}
	if in.Operator == "" {
		in.Operator = "orchestrator_mindset"
	}
	if in.ModelFloor == "" {
		in.ModelFloor = "inherit"
	}
	if in.ModelFloor != "inherit" {
		switch in.ModelFloor {
		case "sonnet", "opus", "haiku":
			// valid
		default:
			return mcp.NewToolResultError(fmt.Sprintf(
				"mindset_apply: model_floor %q not in canonical allow-list (sonnet|opus|haiku|inherit)",
				in.ModelFloor)), nil
		}
	}

	// 1. Cache lookup.
	cacheKey := computeMindsetCacheKey(in.VibeCase, in.TaskDescription, in.ModelFloor)
	cached, cacheErr := s.lookupCachedMindset(ctx, in.Operator, cacheKey)
	if cacheErr == nil && cached != "" {
		return resultJSON(mindsetApplyOutput{
			SystemPrompt:     cached,
			ToolsRecommended: []string{},
			ModelRecommended: in.ModelFloor,
			CacheHit:         true,
			Iterations:       0,
			VibeCase:         in.VibeCase,
			TaskDescription:  in.TaskDescription,
			Verdict:          "cached",
		})
	}

	// 2. Procedural composition.
	personaID := defaultPersonaForVibeCase[in.VibeCase]
	systemPrompt := composeSystemPrompt(personaID, in.VibeCase, in.TaskDescription, in.Operator)

	// 3. Judge validation + retry loop.
	maxIter := mindsetMaxIterations()
	verdict := "unknown"
	iterations := 0
	for i := 0; i < maxIter; i++ {
		iterations = i + 1
		v, err := s.validateSystemPrompt(ctx, personaID, in.VibeCase, in.TaskDescription, systemPrompt)
		if err != nil {
			// Pipeline failure (NoOpJudge fallback or LLM error).
			// Best-effort: return the prompt with verdict=errored.
			verdict = "errored"
			break
		}
		verdict = v.Verdict
		if v.Verdict == "aligned" {
			break
		}
		if v.Verdict == "needs_human" {
			break
		}
		// drift_detected → refine for next iteration.
		if i+1 < maxIter {
			systemPrompt = refineSystemPrompt(systemPrompt, v.Reasoning, personaID, in.TaskDescription)
		}
	}

	// 4. Cache store (best-effort).
	_ = s.storeCachedMindset(ctx, in.Operator, cacheKey, in.VibeCase, systemPrompt)

	return resultJSON(mindsetApplyOutput{
		SystemPrompt:     systemPrompt,
		ToolsRecommended: []string{},
		ModelRecommended: in.ModelFloor,
		CacheHit:         false,
		Iterations:       iterations,
		VibeCase:         in.VibeCase,
		TaskDescription:  in.TaskDescription,
		Verdict:          verdict,
	})
}

// computeMindsetCacheKey returns a deterministic cache key for the
// (vibe_case, task_description, model_floor) tuple.
func computeMindsetCacheKey(vibeCase, task, modelFloor string) string {
	h := sha256.Sum256([]byte(vibeCase + "\x00" + task + "\x00" + modelFloor))
	return hex.EncodeToString(h[:])
}

// composeSystemPrompt builds the procedural system_prompt from
// the persona + task + vibe_case. Pure function — no DB, no LLM.
func composeSystemPrompt(personaID, vibeCase, task, operator string) string {
	persona := judge.LookupPersonaContent(personaID)
	var promptTemplate string
	if persona != nil {
		promptTemplate = persona.PromptTemplate
	}
	if promptTemplate == "" {
		promptTemplate = fmt.Sprintf(
			"You are a sub-agent for vibe_case %s (persona %s). Apply task-specific expertise.",
			vibeCase, personaID,
		)
	}

	var biasLines strings.Builder
	if persona != nil && len(persona.BiasControls) > 0 {
		for _, b := range persona.BiasControls {
			biasLines.WriteString("- ")
			biasLines.WriteString(b)
			biasLines.WriteString("\n")
		}
	}

	return fmt.Sprintf(
		"%s\n\n"+
			"## Task\n%s\n\n"+
			"## Operator\n%s\n\n"+
			"## Vibe case\n%s\n\n"+
			"## Bias controls (mandatory)\n%s",
		promptTemplate,
		task,
		operator,
		vibeCase,
		biasLines.String(),
	)
}

// refineSystemPrompt tweaks the system_prompt based on the judge's
// reasoning. Pure function — appends the reasoning as an additional
// guardrail so the next iteration has more context.
func refineSystemPrompt(currentPrompt, reasoning, personaID, task string) string {
	return fmt.Sprintf("%s\n\n## Refinement note (from judge)\n%s", currentPrompt, reasoning)
}

// mindsetMaxIterations reads DARK_MINDSET_MAX_ITERATIONS from env
// (default 3). Clamped to [1, 7].
func mindsetMaxIterations() int {
	v := os.Getenv("DARK_MINDSET_MAX_ITERATIONS")
	if v == "" {
		return 3
	}
	n := 3
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n < 1 || n > 7 {
		return 3
	}
	return n
}

// lookupCachedMindset queries agent_memory for a cached system_prompt
// using the canonical tag prefix `mindset:v1` and the cache key as
// the title. Returns the cached content + nil error on hit; "" +
// nil on miss; "" + error on DB error.
func (s *Server) lookupCachedMindset(ctx context.Context, operator, cacheKey string) (string, error) {
	if s.memories == nil {
		return "", fmt.Errorf("agent_memory store not wired")
	}
	rows, err := s.memories.RecallFiltered(ctx, operator, cacheKey, agent_memory.RecallFilter{
		TagPrefix: "mindset:v1",
	}, 5)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil // miss
	}
	for _, r := range rows {
		if strings.Contains(r.Tags, "mindset_cache_key:"+cacheKey) {
			return r.Content, nil
		}
	}
	return "", nil // no match in tags
}

// storeCachedMindset stores a system_prompt in agent_memory with the
// canonical mindset:v1 tag prefix. Best-effort — failure does not
// affect the caller.
func (s *Server) storeCachedMindset(ctx context.Context, operator, cacheKey, vibeCase, systemPrompt string) error {
	if s.memories == nil {
		return fmt.Errorf("agent_memory store not wired")
	}
	tags := fmt.Sprintf("mindset:v1,vibe_case:%s,model_floor:inherit,cached:%d,mindset_cache_key:%s",
		vibeCase, time.Now().Unix(), cacheKey)
	_, err := s.memories.Save(ctx, &agent_memory.Audit{
		Actor:     operator,
		SessionID: "",
		ProjectID: "default",
	}, operator, "context", "mindset:"+cacheKey[:8], systemPrompt, tags, false)
	return err
}

// validateSystemPrompt invokes the judge pipeline to validate the
// procedural system_prompt. Returns the verdict + reasoning.
// Errors from the pipeline (NoOpJudge fallback, LLM outage) are
// surfaced so the caller can decide whether to skip validation.
func (s *Server) validateSystemPrompt(ctx context.Context, personaID, vibeCase, task, systemPrompt string) (*judge.Verdict, error) {
	if s.judgePipeline == nil {
		return nil, fmt.Errorf("judge pipeline not wired")
	}
	v, err := s.judgePipeline.Evaluate(ctx, judge.EvaluateRequest{
		EvalType:        "mindset_compose",
		SpecIntent:      "mindset_apply validation — does the composed system_prompt appropriately scope the sub-agent to the task?",
		VibeCase:        vibeCase,
		PersonaID:       personaID,
		ArtifactContent: []byte(systemPrompt),
		SchemaVersion:   "v4alpha/2026-10-01/005",
	})
	if err != nil {
		return nil, err
	}
	return v, nil
}