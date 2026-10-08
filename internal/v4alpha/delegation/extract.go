// Package delegation — EXTRACT step (LLM-based sub-task decomposition).
//
// Per SPEC §3.1 §3.1 P1 (P1=C, EXTRACT intermediario): the EXTRACT step
// runs only when decideDelegation returns "delegate" AND one of:
//   - len(task) > 200
//   - vibe_case == "C7"
//
// EXTRACT calls the LLM with the judge-delegator persona (per SPEC §3.1
// §3.1 P2 = III) and parses the JSON response into []Subtask.
//
// On failure (LLM error, parse error, schema mismatch), the failure mode
// from types.go is attached and the verdict is set to "needs_human" with
// the corresponding alternatives[] (per SPEC P3).
//
// On hit of max retries (drift_detected x3), surfaces judge_drift_exhausted
// failure mode with alternatives[] (per SPEC P4 = drift_judge validation).
package delegation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/judge"
	"github.com/dark-agents/dark-memory-mcp/internal/vibecase"
)

// PersonaDelegatorID is the canonical persona id for the EXTRACT step.
// Per SPEC §3.1 §3.1 P2 = III: new persona, breaks Phase 6 §6.1 canon
// (13 → 14 personas total). Honest about it.
const PersonaDelegatorID = "judge-delegator"

// EvalTypeSubtaskExtraction is the eval_type label for the JUDGE step
// validation call. Per SPEC §3.1 §3.1: new eval_type, used as a label
// for the validation prompt + audit_log row kind=meta.
const EvalTypeSubtaskExtraction = "subtask_extraction"

// LLMExtractor is the minimal interface we need from judge.LLMClient.
// Defined here to avoid an import cycle (delegation imports judge, but
// we want the delegation package testable without the real HTTP client).
type LLMExtractor interface {
	Complete(ctx context.Context, req judge.LLMRequest) (*judge.LLMResponse, error)
}

// Extractor runs the EXTRACT step. Holds the LLM client + cache + options.
type Extractor struct {
	LLM       LLMExtractor
	Cache     *ExtractCache
	MemStore  AgentMemoryStore // best-effort persistent cache; nil = in-memory only
	Operator  string           // audit owner
	ProjectID string           // for cache key
}

// Extract runs the EXTRACT pipeline for the given (vibeCase, task).
// Returns an ExtractResult with decision="delegate" + the subtasks + verdict.
//
// On failure, returns ExtractResult{Verdict:"needs_human", Alternatives:<from failure mode>}.
//
// Pure orchestration: cache lookup → LLM call → parse → validate → drift_judge.
// Side effects: writes cache entries (in-memory + best-effort persistent).
func (c *Extractor) Extract(ctx context.Context, vibeCase, task string) (ExtractResult, error) {
	if c == nil || c.LLM == nil {
		return needsHumanFor("llm_network_error",
			"delegate_intent: no LLM client wired (judge.NewRealLLMClient failed at boot)"), nil
	}
	if task == "" {
		return ExtractResult{}, errors.New("delegation: empty task")
	}
	if !validVibeCaseForExtract(vibeCase) {
		return ExtractResult{}, fmt.Errorf("delegation: invalid vibe_case %q (valid: %v)", vibeCase, vibecase.JSONSchemaEnum())
	}

	cacheKey := CacheKey(vibeCase, task, c.ProjectID, "inherit")

	// 1. Cache lookup (in-memory first).
	if cached := c.Cache.Get(cacheKey); cached != nil {
		return *cached, nil
	}
	// 2. Persistent cache lookup (best-effort).
	if c.MemStore != nil {
		if persisted, _ := PersistentLookup(ctx, c.MemStore, c.Operator, cacheKey); persisted != nil {
			c.Cache.Set(cacheKey, *persisted)
			return *persisted, nil
		}
	}

	// 3. LLM call (with retry loop on drift_detected).
	var (
		lastResult ExtractResult
		refined     = ""
	)
	for attempt := 0; attempt <= MaxRetries; attempt++ {
		req := buildExtractRequest(vibeCase, task, refined)
		resp, err := c.LLM.Complete(ctx, req)
		if err != nil {
			// LLM call failed (network / rate limit / timeout).
			return needsHumanFor(classifyLLMError(err),
				fmt.Sprintf("EXTRACT: LLM call failed (attempt %d/%d): %v", attempt+1, MaxRetries+1, err)), nil
		}
		if resp == nil || strings.TrimSpace(resp.Content) == "" {
			return needsHumanFor("llm_parse_error",
				fmt.Sprintf("EXTRACT: empty LLM response (attempt %d/%d)", attempt+1, MaxRetries+1)), nil
		}

		// 4. Parse JSON response.
		parsed, parseErr := parseExtractResponse(resp.Content)
		if parseErr != nil {
			return needsHumanFor("llm_parse_error",
				fmt.Sprintf("EXTRACT: parse failed (attempt %d/%d): %v", attempt+1, MaxRetries+1, parseErr)), nil
		}

		// 5. Validate subtasks (cap, min_length, topo-sort).
		converted := make([]Subtask, len(parsed.Subtasks))
		for i, raw := range parsed.Subtasks {
			converted[i] = Subtask{
				ID:           raw.ID,
				Description:  raw.Description,
				Dependencies: raw.Dependencies,
				Model:        "inherit",
				Tools:        []string{},
			}
		}
		valResult := ValidateSubtasks(converted)
		if valResult.Err != nil {
			return needsHumanFor("topo_cycle",
				fmt.Sprintf("EXTRACT: %v (attempt %d/%d)", valResult.Err, attempt+1, MaxRetries+1)), nil
		}

		// 6. drift_judge validation (per SPEC P4).
		judgeVerdict, judgeReason, judgeErr := c.validateSubtasks(ctx, vibeCase, task, valResult.Subtasks)
		if judgeErr != nil {
			return needsHumanFor(classifyLLMError(judgeErr),
				fmt.Sprintf("EXTRACT: judge validation call error: %v", judgeErr)), nil
		}

		lastResult = ExtractResult{
			Decision:  parsed.Decision,
			Subtasks:  valResult.Subtasks,
			Reasoning: fmt.Sprintf("EXTRACT: %s | JUDGE: %s", parsed.Reasoning, judgeReason),
			CacheHit:  false,
			Verdict:   judgeVerdict,
		}

		if judgeVerdict == "aligned" {
			break
		}
		if judgeVerdict == "needs_human" {
			return needsHumanFor("judge_needs_human", lastResult.Reasoning), nil
		}
		// drift_detected: refine + retry.
		if attempt < MaxRetries {
			refined = refineExtractPrompt(judgeReason)
		}
	}

	// Loop exhausted with drift_detected.
	if lastResult.Verdict == "drift_detected" {
		return needsHumanFor("judge_drift_exhausted",
			fmt.Sprintf("EXTRACT: drift_detected on all %d attempts", MaxRetries+1)), nil
	}

	// 7. Cache store (in-memory + best-effort persistent).
	c.Cache.Set(cacheKey, lastResult)
	if c.MemStore != nil {
		_ = PersistentStore(ctx, c.MemStore, c.Operator, cacheKey, vibeCase, lastResult)
	}

	return lastResult, nil
}

// ---------- Helpers ----------

// validVibeCaseForExtract delegates to the canonical vibecase package.
// Adding C8 or beyond is a one-line edit to internal/vibecase/taxonomy.go.
func validVibeCaseForExtract(c string) bool {
	return vibecase.IsValid(c)
}

// needsHumanFor builds a needs_human ExtractResult with the failure mode's
// alternatives attached. Pure helper.
func needsHumanFor(failureID, detail string) ExtractResult {
	fm, _ := FailureModeByID(failureID)
	return ExtractResult{
		Verdict:      "needs_human",
		Reasoning:    detail,
		Alternatives: fm.Alternatives,
	}
}

// classifyLLMError maps a LLM error to a failure mode ID. Best-effort:
// uses error string matching for common patterns.
func classifyLLMError(err error) string {
	if err == nil {
		return ""
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "429") || strings.Contains(s, "rate limit"):
		return "llm_rate_limit"
	case strings.Contains(s, "timeout") || strings.Contains(s, "deadline exceeded"):
		return "llm_network_error"
	case strings.Contains(s, "connection refused") || strings.Contains(s, "no such host"):
		return "llm_network_error"
	default:
		return "llm_network_error"
	}
}

// buildExtractRequest builds the LLM request for the EXTRACT call.
// Uses the judge-delegator persona's PromptTemplate + a phase-specific
// extraction instruction. Pure function.
func buildExtractRequest(vibeCase, task, refinedFrom string) judge.LLMRequest {
	persona := judge.LookupPersonaContent(PersonaDelegatorID)
	var voice string
	if persona != nil {
		voice = persona.PromptTemplate
	}
	if voice == "" {
		voice = "You are a task decomposition agent."
	}

	system := voice + "\n\n" +
		"Your task: given a compound task description and a vibe_case, extract 0..8 " +
		"non-overlapping subtasks that together cover the original task. " +
		"Each subtask description must be ≥10 chars. " +
		"Prefer independent subtasks (empty dependencies) when possible.\n\n" +
		"Output JSON schema:\n" +
		`{"decision": "inline"|"delegate"|"refused", "reasoning": "...", "subtasks": [{"id": "subtask-N", "description": "...", "dependencies": []}]}` +
		"\n\n" +
		"Constraints:\n" +
		"- subtasks: 0..8 (cap 8)\n" +
		"- each description: ≥10 chars\n" +
		"- no cycles in dependencies\n" +
		"- prefer independent subtasks\n" +
		"- if single-step: decision=inline, 1 subtask = whole task\n" +
		"- if impossible/out-of-scope: decision=refused, 0 subtasks"

	if refinedFrom != "" {
		system += "\n\n## Refinement note (from drift_judge)\n" + refinedFrom
	}

	schema := `{
  "type": "object",
  "required": ["decision", "reasoning", "subtasks"],
  "properties": {
    "decision": {"type": "string", "enum": ["inline", "delegate", "refused"]},
    "reasoning": {"type": "string"},
    "subtasks": {
      "type": "array",
      "maxItems": 8,
      "items": {
        "type": "object",
        "required": ["description"],
        "properties": {
          "id": {"type": "string"},
          "description": {"type": "string", "minLength": 10},
          "dependencies": {"type": "array", "items": {"type": "string"}}
        }
      }
    }
  }
}`

	user := fmt.Sprintf(
		"Vibe case: %s.\nTask description:\n%s\n\nDecompose into atomic subtasks.",
		vibeCase, task,
	)

	return judge.LLMRequest{
		SystemPrompt: system,
		UserPrompt:   user,
		Temperature:  0.0,
		MaxTokens:    2048,
		TopP:         1.0,
		Seed:         0,
		ResponseFormat: judge.ResponseFormat{
			Type:   "json_schema",
			Schema: schema,
		},
	}
}

// extractResponse is the parsed LLM JSON output.
type extractResponse struct {
	Decision  string `json:"decision"`
	Reasoning string `json:"reasoning"`
	Subtasks  []struct {
		ID           string   `json:"id"`
		Description  string   `json:"description"`
		Dependencies []string `json:"dependencies"`
	} `json:"subtasks"`
}

// parseExtractResponse decodes the LLM's JSON. Pure function.
func parseExtractResponse(content string) (*extractResponse, error) {
	var out extractResponse
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		return nil, fmt.Errorf("json decode: %w", err)
	}
	if out.Decision == "" {
		return nil, errors.New("missing decision field")
	}
	switch out.Decision {
	case "inline", "delegate", "refused":
		// ok
	default:
		return nil, fmt.Errorf("invalid decision %q (must be inline|delegate|refused)", out.Decision)
	}
	return &out, nil
}

// validateSubtasks invokes the JUDGE step: drift_judge validates the
// extracted subtasks. Returns (verdict, reasoning, error).
//
// We call the LLMClient directly (not the full Pipeline) because the
// validation is a simple "is this extraction good?" prompt — we don't
// need the full 6-step pipeline with evidence extraction + ECs.
func (c *Extractor) validateSubtasks(ctx context.Context, vibeCase, task string, subtasks []Subtask) (string, string, error) {
	req := buildValidateRequest(vibeCase, task, subtasks)
	resp, err := c.LLM.Complete(ctx, req)
	if err != nil {
		return "", "", err
	}
	if resp == nil || strings.TrimSpace(resp.Content) == "" {
		return "needs_human", "JUDGE: empty response", nil
	}
	return parseValidateResponse(resp.Content)
}

// buildValidateRequest builds the JUDGE LLM request.
func buildValidateRequest(vibeCase, task string, subtasks []Subtask) judge.LLMRequest {
	persona := judge.LookupPersonaContent(PersonaDelegatorID)
	var voice string
	if persona != nil {
		voice = persona.PromptTemplate
	}
	if voice == "" {
		voice = "You are a task decomposition validator."
	}

	// Serialize subtasks as a simple list.
	var sb strings.Builder
	for i, st := range subtasks {
		fmt.Fprintf(&sb, "- subtask-%d: %s", i+1, st.Description)
		if len(st.Dependencies) > 0 {
			fmt.Fprintf(&sb, " (deps: %s)", strings.Join(st.Dependencies, ", "))
		}
		sb.WriteString("\n")
	}

	system := voice + "\n\n" +
		"Your task: validate whether the proposed subtask extraction is correct. " +
		"Check: (1) subtasks cover the original task, (2) no overlaps between subtasks, " +
		"(3) dependencies are valid (no cycles), (4) granularity is appropriate " +
		"(not too granular like 'write hello world' → 'write h' + 'write world').\n\n" +
		"Output JSON schema:\n" +
		`{"verdict": "aligned"|"drift_detected"|"needs_human", "reasoning": "..."}`

	user := fmt.Sprintf(
		"Original task (vibe_case=%s):\n%s\n\nProposed subtasks:\n%s\n\nValidate.",
		vibeCase, task, sb.String(),
	)

	schema := `{
  "type": "object",
  "required": ["verdict", "reasoning"],
  "properties": {
    "verdict": {"type": "string", "enum": ["aligned", "drift_detected", "needs_human"]},
    "reasoning": {"type": "string"}
  }
}`

	return judge.LLMRequest{
		SystemPrompt: system,
		UserPrompt:   user,
		Temperature:  0.0,
		MaxTokens:    1024,
		TopP:         1.0,
		Seed:         0,
		ResponseFormat: judge.ResponseFormat{
			Type:   "json_schema",
			Schema: schema,
		},
	}
}

// parseValidateResponse decodes the JUDGE JSON response.
func parseValidateResponse(content string) (string, string, error) {
	var out struct {
		Verdict   string `json:"verdict"`
		Reasoning string `json:"reasoning"`
	}
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		return "needs_human", fmt.Sprintf("JUDGE: parse failed: %v", err), nil
	}
	switch out.Verdict {
	case "aligned", "drift_detected":
		return out.Verdict, out.Reasoning, nil
	default:
		return "needs_human", "JUDGE: verdict missing or invalid (treated as needs_human)", nil
	}
}

// refineExtractPrompt tweaks the EXTRACT prompt based on the judge's
// reasoning. Pure function — appends the reasoning as a refinement
// note. Mirrors mindset.go's refineSystemPrompt pattern.
func refineExtractPrompt(judgeReason string) string {
	return fmt.Sprintf("Previous extraction was drift_detected by judge: %s\n\nRefine to address the judge's concerns while preserving coverage.",
		judgeReason)
}

// compile-time guard: Ensure time package is used (avoid unused-import lint).
var _ = time.Now