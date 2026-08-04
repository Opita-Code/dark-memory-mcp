// Package orchestration — mindset_apply.go
//
// v2.7.0-alpha: Phase 1 delegation primitive. dark_memory_mindset_apply
// procedurally composes a subagent system_prompt given (vibe_case,
// task_description) and validates it via the LLM-as-judge before
// returning. Cached in agent_memory with kind=context, tags=mindset-cache,
// expires_at honored at lookup time.
//
// Wire contract (additive — see CHANGELOG):
//   - input  : { vibe_case, task_description, model_floor?, operator? }
//   - output : { system_prompt, source_mindset_id, match_score,
//                tools_recommended, model_recommended,
//                composition_iterations, judge_verdict,
//                cache_hit, category_used }
//
// Composition loop:
//   1. Cache check (sha256 of vibe_case+task+model_floor) — hit returns
//      immediately with cache_hit=true.
//   2. Compose (eval_type=mindset_compose) — LLM synthesizes JSON
//      {role, goal, backstory, constraints, tools, model}.
//   3. Validate (eval_type=mindset_quality) — LLM judges 5 pass criteria.
//   4. If aligned: cache + return.
//      If drift_detected: loop with judge feedback in next iteration.
//      If needs_human / errored: return best attempt with flag.
//   5. If max_iterations exhausted: return best attempt with needs_human
//      verdict.
//
// Both compose and validate calls persist SDDEvaluation rows
// (full audit trail of every LLM call).

package orchestration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/errorobs"
	"github.com/dark-agents/dark-memory-mcp/internal/ssd"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/vibecase"
)

// --- env resolution (mirror session_sweeper.envDuration pattern) ---

const (
	defaultMaxIterations = 3
	defaultTimeoutMS     = 15000
	defaultCacheTTLSec   = 3600
	tagMindsetCache      = "mindset-cache"
)

// resolveMaxIterations reads DARK_MINDSET_MAX_ITERATIONS. Default 3.
// Clamped to [1, 10] to prevent runaway loops.
func resolveMaxIterations() int {
	raw := strings.TrimSpace(os.Getenv("DARK_MINDSET_MAX_ITERATIONS"))
	if raw == "" {
		return defaultMaxIterations
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return defaultMaxIterations
	}
	if n > 10 {
		return 10
	}
	return n
}

// resolveTimeout reads DARK_MINDSET_TIMEOUT_MS. Default 15000ms.
// Clamped to [100, 120000]ms.
func resolveTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv("DARK_MINDSET_TIMEOUT_MS"))
	if raw == "" {
		return defaultTimeoutMS * time.Millisecond
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 100 {
		return defaultTimeoutMS * time.Millisecond
	}
	if n > 120000 {
		return 120 * time.Second
	}
	return time.Duration(n) * time.Millisecond
}

// resolveCacheTTL reads DARK_MINDSET_CACHE_TTL (seconds). Default 3600s.
// Clamped to [60, 86400]s.
func resolveCacheTTL() time.Duration {
	raw := strings.TrimSpace(os.Getenv("DARK_MINDSET_CACHE_TTL"))
	if raw == "" {
		return defaultCacheTTLSec * time.Second
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 60 {
		return defaultCacheTTLSec * time.Second
	}
	if n > 86400 {
		return 24 * time.Hour
	}
	return time.Duration(n) * time.Second
}

// --- input / output ---

// MindsetApplyInput is the request to compose + validate a subagent system_prompt.
//
// Operator is optional and defaults to "orchestrator_mindset". It's
// used for the write_audit row on cache writes (INV-1).
//
// v2.8.0-alpha C2: SpawnSubagent + SubagentID are the subagent-scope-
// handoff knobs. When SpawnSubagent=true, after a successful composition
// the orchestrator registers the subagent_id in the active_subagents
// table (TTL 1h default) so that subsequent agent_memory_save calls
// from the subagent get tagged with the subagent_id (NOT the
// principal's agent_id). This quarantines subagent memory from the
// principal's ContextRecap (defense-in-depth vs arxiv:2605.08460).
// Gated by DARK_MEMORY_V280=1; when off, the fields are accepted but
// ignored.
type MindsetApplyInput struct {
	VibeCase        string `json:"vibe_case"`
	TaskDescription string `json:"task_description"`
	ModelFloor      string `json:"model_floor,omitempty"`
	Operator        string `json:"operator,omitempty"`
	// SpawnSubagent (v2.8.0-alpha C2) — when true, registers the
	// subagent_id in active_subagents after composition. Default false.
	SpawnSubagent bool `json:"spawn_subagent,omitempty"`
	// SubagentID (v2.8.0-alpha C2) — opaque uuid identifying the
	// subagent. Required when SpawnSubagent=true. The subagent's
	// agent_memory_save calls will be tagged with this id.
	SubagentID string `json:"subagent_id,omitempty"`
}

// MindsetApplyOutput is the composed system_prompt + the validation verdict.
// Cache hits return with CacheHit=true; procedural returns with CacheHit=false.
type MindsetApplyOutput struct {
	SystemPrompt          string              `json:"system_prompt"`
	SourceMindsetID       int64               `json:"source_mindset_id"` // 0 if procedural; cache row id if hit
	MatchScore            float32             `json:"match_score"`       // 1.0 if cache hit; 0.0 if procedural
	ToolsRecommended      []string            `json:"tools_recommended"`
	ModelRecommended      string              `json:"model_recommended"`
	CompositionIterations int                 `json:"composition_iterations"`
	JudgeVerdict          MindsetJudgeVerdict `json:"judge_verdict"`
	CacheHit              bool                `json:"cache_hit"`
	CategoryUsed          string              `json:"category_used"`
	// SubagentID (v2.8.0-alpha C2) — echoes the registered subagent_id
	// (only set when SpawnSubagent=true on the input AND registration
	// succeeded). Empty string otherwise.
	SubagentID string `json:"subagent_id,omitempty"`
	// ParentAgentID (v2.8.0-alpha C2) — echoes the principal's resolved
	// agent_id at spawn time. Useful for audit + write-back to the
	// harness's subagent tool as "this is your principal's id".
	ParentAgentID string `json:"parent_agent_id,omitempty"`
}

// MindsetJudgeVerdict is the validator's response (parsed from
// eval_type=mindset_quality VerdictJSON).
type MindsetJudgeVerdict struct {
	Verdict        string   `json:"verdict"`        // aligned | drift_detected | needs_human | errored
	Confidence     float32  `json:"confidence"`
	Reasoning      string   `json:"reasoning"`
	CriteriaFailed []string `json:"criteria_failed,omitempty"`
}

// mindsetAttempt is the LLM-composed mindset (one per iteration).
type mindsetAttempt struct {
	Role              string   `json:"role"`
	Goal              string   `json:"goal"`
	Backstory         string   `json:"backstory"`
	Constraints       []string `json:"constraints"`
	ToolsRecommended  []string `json:"tools_recommended"`
	ModelRecommended  string   `json:"model_recommended"`
	Iteration         int      `json:"iteration"`
	JudgeFeedbackFrom string   `json:"judge_feedback_from,omitempty"`
}

// --- main method ---

// MindsetApply composes + validates a subagent system_prompt for the
// given (vibe_case, task_description). Cache hit returns immediately.
// Cache miss runs the composition loop up to DARK_MINDSET_MAX_ITERATIONS
// times; on each iteration, compose + validate via Judge. The final
// attempt is cached (regardless of verdict) so the next identical call
// can short-circuit.
//
// Failures:
//   - ErrInvalidArgument if vibe_case is invalid (via vibecase.Parse).
//   - ErrInvalidArgument if task_description is empty.
//   - ctx deadline exceeded if total time > DARK_MINDSET_TIMEOUT_MS.
//   - ErrNoLLMAvailable if no LLM key detected.
//   - ErrCanaryInPayload if task_description contains the canary (INV-3).
func (o *Orchestrator) MindsetApply(ctx context.Context, in MindsetApplyInput) (*MindsetApplyOutput, error) {
	// 1. Validate inputs.
	case_, err := vibecase.Parse(in.VibeCase)
	if err != nil {
		return nil, fmt.Errorf("mindset_apply: %w", err)
	}
	if strings.TrimSpace(in.TaskDescription) == "" {
		return nil, errMissingField("task_description")
	}
	if strings.TrimSpace(in.Operator) == "" {
		in.Operator = "orchestrator_mindset"
	}

	// 2. Apply timeout.
	timeout := resolveTimeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	maxIter := resolveMaxIterations()
	cacheTTL := resolveCacheTTL()
	key := cacheKey(string(case_), in.TaskDescription, in.ModelFloor)

	// 3. Canary check (INV-3).
	if !o.Safety.Active().IsZero() && o.Safety.Active().Match(in.TaskDescription) {
		return nil, fmt.Errorf("%w: task_description contains canary token", store.ErrCanaryInPayload)
	}

	// 4. Cache check.
	if hit, ok := o.mindsetCacheLookup(ctx, key); ok && hit != nil {
		log.Printf("dark-mem-mcp: mindset_apply cache_hit key=%s id=%d", key, hit.ID)
		return cacheHitOutput(hit), nil
	}

	// 5. Composition loop.
	category := PickCategoryKey(string(case_), in.TaskDescription)
	var (
		lastAttempt *mindsetAttempt
		lastVerdict MindsetJudgeVerdict
	)

	for iter := 1; iter <= maxIter; iter++ {
		// Check timeout between iterations.
		if err := ctx.Err(); err != nil {
			lastVerdict = wrapTimeoutVerdict(lastVerdict, iter-1, err)
			break
		}

		// 5a. Compose.
		composed, cerr := o.mindsetCompose(ctx, case_, in, category, lastAttempt, lastVerdict, iter)
		if cerr != nil {
			return nil, fmt.Errorf("mindset_apply: compose iter %d: %w", iter, cerr)
		}
		lastAttempt = composed

		// 5b. Validate.
		verdict, verr := o.mindsetValidate(ctx, case_, in, composed)
		if verr != nil {
			// Judge error: return last attempt with errored flag.
			// v2.11.0 (spec 757): was log-only; now also durable.
			log.Printf("dark-mem-mcp: mindset_apply judge_error iter=%d err=%v", iter, verr)
			o.RecordError(ctx, "mindset_apply", "", fmt.Errorf("mindset_validate iter %d: %w", iter, verr), errorobs.SeverityWarn)
			lastVerdict = MindsetJudgeVerdict{Verdict: "errored", Reasoning: verr.Error()}
			break
		}
		lastVerdict = verdict

		if verdict.Verdict == "aligned" {
			// 6. Cache + return.
			out, err := o.cacheAndReturn(ctx, composed, verdict, iter, category, cacheTTL, key, in.Operator)
			return o.applySubagentHandoff(ctx, out, in), err
		}
		if verdict.Verdict == "needs_human" {
			// Return best attempt; cache so next call doesn't re-run.
			out, err := o.cacheAndReturn(ctx, composed, verdict, iter, category, cacheTTL, key, in.Operator)
			return o.applySubagentHandoff(ctx, out, in), err
		}
		// drift_detected → loop with feedback
	}

	// 7. Loop exhausted OR timeout OR errored. Return best attempt with flag.
	if lastVerdict.Verdict == "" {
		lastVerdict = MindsetJudgeVerdict{
			Verdict:   "needs_human",
			Reasoning: fmt.Sprintf("composition loop produced no verdict after %d iterations", maxIter),
		}
	} else if lastVerdict.Verdict != "errored" {
		// Convert drift_detected at exhaustion to needs_human.
		lastVerdict.Verdict = "needs_human"
		lastVerdict.Reasoning = fmt.Sprintf("max iterations (%d) exhausted; %s", maxIter, lastVerdict.Reasoning)
	}
	out, err := o.cacheAndReturn(ctx, lastAttempt, lastVerdict, maxIter, category, cacheTTL, key, in.Operator)
	return o.applySubagentHandoff(ctx, out, in), err
}

// applySubagentHandoff is the v2.8.0-alpha C2 hook. When the caller
// passed SpawnSubagent=true (and DARK_MEMORY_V280=1), it registers
// the subagent in active_subagents so subsequent agent_memory_save
// calls from the subagent get tagged with the subagent_id instead
// of the principal's agent_id. Defense-in-depth against
// arxiv:2605.08460 inheritance attacks — see agent_memory.go and
// the design doc artifact id=804.
//
// Best-effort: registration failure is logged but the result is
// still returned (the composition was the important part).
//
// When SpawnSubagent=true but SubagentID is empty, returns an error
// (the caller MUST supply the subagent_id — we won't generate one
// because the principal's harness needs the id to inject into the
// subagent tool call).
func (o *Orchestrator) applySubagentHandoff(ctx context.Context, out *MindsetApplyOutput, in MindsetApplyInput) *MindsetApplyOutput {
	if out == nil {
		return nil
	}
	if !v280Enabled() || !in.SpawnSubagent {
		return out
	}
	if strings.TrimSpace(in.SubagentID) == "" {
		// Caller error — they must supply subagent_id when
		// spawn_subagent=true. Return the composition result
		// unchanged but set ParentAgentID empty so they can detect.
		log.Printf("dark-mem-mcp: mindset_apply spawn_subagent=true but subagent_id empty; skipping registration")
		return out
	}
	// Resolve the principal's agent_id for audit provenance.
	parentAgentID := o.resolveActiveAgentID(ctx, "")
	operator := in.Operator
	if operator == "" {
		operator = o.activeOperator(ctx)
	}
	if operator == "" {
		log.Printf("dark-mem-mcp: mindset_apply spawn_subagent: no active operator; skipping registration")
		return out
	}
	wc := store.WriteContext{
		Actor:     operator,
		WritePath: "MindsetApplySpawnSubagent",
	}
	row := &store.ActiveSubagent{
		Operator:      operator,
		SubagentID:    in.SubagentID,
		ParentAgentID: parentAgentID,
		TTLSeconds:    3600,
	}
	if _, err := o.Store.SetActiveSubagent(ctx, wc, row); err != nil {
		log.Printf("dark-mem-mcp: mindset_apply spawn_subagent register err=%v (subagent_id=%s)", err, in.SubagentID)
		// v2.11.0 (spec 757): durable capture (was log-only).
		o.RecordError(ctx, "mindset_apply", "", fmt.Errorf("spawn_subagent register %s: %w", in.SubagentID, err), errorobs.SeverityWarn)
		return out
	}
	out.SubagentID = in.SubagentID
	out.ParentAgentID = parentAgentID
	return out
}

// --- composition step ---

// mindsetCompose calls the Judge with eval_type=mindset_compose. The
// Judge's defaultSystemForEval sets JSON-output mode; we provide the
// meta-prompt as user content with previous-attempt feedback embedded.
func (o *Orchestrator) mindsetCompose(
	ctx context.Context,
	case_ vibecase.Case,
	in MindsetApplyInput,
	category string,
	lastAttempt *mindsetAttempt,
	lastVerdict MindsetJudgeVerdict,
	iter int,
) (*mindsetAttempt, error) {
	// Render meta-prompt with current inputs.
	userMsg := renderComposePrompt(case_, in, category, lastAttempt, lastVerdict, iter)

	// Call Judge (eval_type=mindset_compose) — this persists an SDDEvaluation
	// row as a side-effect, giving full audit trail of every composition attempt.
	jOut, err := o.Judge(ctx, JudgeInput{
		EvalType:   string(ssd.EvalMindsetCompose),
		TargetType: "mindset_compose",
		TargetID:   fmt.Sprintf("mindset:%s:%d", cacheKey(string(case_), in.TaskDescription, in.ModelFloor), iter),
		Content:    userMsg,
	})
	if err != nil {
		return nil, err
	}

	// Parse the composed JSON.
	var parsed mindsetAttempt
	parsed.Iteration = iter
	if jOut.VerdictJSON != "" {
		// The "verdict" slot carries the proposed mindset as JSON.
		if err := json.Unmarshal([]byte(jOut.VerdictJSON), &parsed); err != nil {
			// Best-effort: try to extract JSON from a prose wrapper.
			cleaned := extractFirstJSONObject(jOut.VerdictJSON)
			if cleaned == "" {
				return nil, fmt.Errorf("compose: parse verdict_json as mindset: %w (raw=%q)", err, truncateForErr(jOut.VerdictJSON, 200))
			}
			if err2 := json.Unmarshal([]byte(cleaned), &parsed); err2 != nil {
				return nil, fmt.Errorf("compose: parse extracted JSON: %w (raw=%q)", err2, truncateForErr(jOut.VerdictJSON, 200))
			}
		}
	}
	if parsed.Role == "" {
		return nil, fmt.Errorf("compose: empty role (verdict_json=%q)", truncateForErr(jOut.VerdictJSON, 200))
	}
	if parsed.JudgeFeedbackFrom == "" && lastVerdict.Reasoning != "" && iter > 1 {
		parsed.JudgeFeedbackFrom = lastVerdict.Reasoning
	}
	return &parsed, nil
}

// --- validation step ---

// mindsetValidate calls Judge with eval_type=mindset_quality. Returns
// the parsed MindsetJudgeVerdict (or error if judge call fails).
func (o *Orchestrator) mindsetValidate(
	ctx context.Context,
	case_ vibecase.Case,
	in MindsetApplyInput,
	composed *mindsetAttempt,
) (MindsetJudgeVerdict, error) {
	// Build the proposed system_prompt from the composed mindset.
	proposed := composeSystemPrompt(composed)

	// Render judge prompt.
	userMsg := renderQualityPrompt(case_, in, proposed)

	jOut, err := o.Judge(ctx, JudgeInput{
		EvalType:   string(ssd.EvalMindsetQuality),
		TargetType: "mindset_quality",
		TargetID:   fmt.Sprintf("mindset:%s:%d", cacheKey(string(case_), in.TaskDescription, in.ModelFloor), composed.Iteration),
		Content:    userMsg,
	})
	if err != nil {
		return MindsetJudgeVerdict{}, err
	}

	var v MindsetJudgeVerdict
	if err := json.Unmarshal([]byte(jOut.VerdictJSON), &v); err != nil {
		// Try extract.
		cleaned := extractFirstJSONObject(jOut.VerdictJSON)
		if cleaned == "" {
			return MindsetJudgeVerdict{}, fmt.Errorf("validate: parse verdict_json: %w (raw=%q)", err, truncateForErr(jOut.VerdictJSON, 200))
		}
		if err2 := json.Unmarshal([]byte(cleaned), &v); err2 != nil {
			return MindsetJudgeVerdict{}, fmt.Errorf("validate: parse extracted JSON: %w (raw=%q)", err2, truncateForErr(jOut.VerdictJSON, 200))
		}
	}
	v.Confidence = jOut.Confidence // override with judge-reported confidence
	if v.Verdict == "" {
		v.Verdict = "drift_detected"
		v.Reasoning = "judge returned empty verdict; defaulting to drift_detected"
	}
	return v, nil
}

// --- cache ---

// cacheKey returns sha256(vibe_case || 0x00 || task || 0x00 || model_floor).
// First 32 hex chars = 128 bits — collision-resistant for cache purposes.
func cacheKey(vibeCase, task, modelFloor string) string {
	h := sha256.New()
	h.Write([]byte(vibeCase))
	h.Write([]byte{0})
	h.Write([]byte(task))
	h.Write([]byte{0})
	h.Write([]byte(modelFloor))
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// mindsetCacheLookup searches agent_memory for a cache hit. We post-filter
// by tag + expires_at because the Store's SearchAgentMemory doesn't filter
// on either today (audit-confirmed gap; sweeper is a future-work item).
//
// Returns (row, true) on hit, (nil, false) on miss. Errors are swallowed
// (best-effort cache — caller proceeds to composition loop).
func (o *Orchestrator) mindsetCacheLookup(ctx context.Context, key string) (*agentmemory.AgentMemory, bool) {
	hits, err := o.Store.SearchAgentMemory(ctx, agentmemory.SearchFilters{
		Query: key,
		Kind:  agentmemory.KindContext,
		Limit: 5,
	})
	if err != nil {
		log.Printf("dark-mem-mcp: mindset_apply cache_lookup err=%v (proceeding to composition)", err)
		// v2.11.0 (spec 757): durable capture (was log-only).
		o.RecordError(ctx, "mindset_apply", "", fmt.Errorf("cache_lookup: %w", err), errorobs.SeverityWarn)
		return nil, false
	}
	now := time.Now().UTC()
	for i := range hits {
		h := &hits[i]
		// Post-filter: must have mindset-cache tag and not expired.
		if !strings.Contains(strings.ToLower(","+h.Tags+","), ","+tagMindsetCache+",") {
			continue
		}
		if h.ExpiresAt != "" {
			t, perr := time.Parse(time.RFC3339Nano, h.ExpiresAt)
			if perr == nil && t.Before(now) {
				continue // expired
			}
		}
		return &h.AgentMemory, true
	}
	return nil, false
}

// cacheAndReturn persists the composed mindset to agent_memory and returns
// the final MindsetApplyOutput. cacheTTL bounds the row's lifetime.
func (o *Orchestrator) cacheAndReturn(
	ctx context.Context,
	composed *mindsetAttempt,
	verdict MindsetJudgeVerdict,
	iter int,
	category string,
	cacheTTL time.Duration,
	key string,
	operator string,
) (*MindsetApplyOutput, error) {
	if composed == nil {
		// Defensive: no composed attempt to cache.
		return &MindsetApplyOutput{
			SystemPrompt:          "You are a focused task-execution specialist. Execute the given task with discipline; do not branch into adjacent concerns.",
			SourceMindsetID:       0,
			MatchScore:            0,
			ToolsRecommended:      []string{},
			ModelRecommended:      "sonnet",
			CompositionIterations: iter,
			JudgeVerdict:          verdict,
			CacheHit:              false,
			CategoryUsed:          category,
		}, nil
	}

	// Build the system_prompt from the composed mindset.
	systemPrompt := composeSystemPrompt(composed)

	// Build tags: cache tag + category + key for exact lookup.
	tags := strings.Join([]string{tagMindsetCache, category, "key:" + key}, ",")

	// Serialize composed as JSON for storage (so a future read can recover it).
	contentJSON, _ := json.Marshal(composed)

	wc := store.WriteContext{
		Actor:     operator,
		WritePath: "MindsetApplyCache",
	}
	m := &agentmemory.AgentMemory{
		Operator:  operator,
		Kind:      agentmemory.KindContext,
		Title:     fmt.Sprintf("mindset-cache:%s:%s", category, verdict.Verdict),
		Content:   string(contentJSON),
		Tags:      tags,
		ExpiresAt: time.Now().UTC().Add(cacheTTL).Format(time.RFC3339Nano),
	}
	id, err := o.Store.SaveAgentMemory(ctx, wc, m)
	if err != nil {
		// Cache write failure is non-fatal — return the result anyway.
		log.Printf("dark-mem-mcp: mindset_apply cache_save err=%v (returning uncached)", err)
		// v2.11.0 (spec 757): durable capture (was log-only).
		o.RecordError(ctx, "mindset_apply", "", fmt.Errorf("cache_save: %w", err), errorobs.SeverityWarn)
		id = 0
	}

	return &MindsetApplyOutput{
		SystemPrompt:          systemPrompt,
		SourceMindsetID:       id,
		MatchScore:            0,
		ToolsRecommended:      composed.ToolsRecommended,
		ModelRecommended:      composed.ModelRecommended,
		CompositionIterations: iter,
		JudgeVerdict:          verdict,
		CacheHit:              false,
		CategoryUsed:          category,
	}, nil
}

// cacheHitOutput builds the MindsetApplyOutput from a cache row. The row's
// content is the JSON-encoded mindsetAttempt; we recover fields from it.
func cacheHitOutput(hit *agentmemory.AgentMemory) *MindsetApplyOutput {
	var attempt mindsetAttempt
	if err := json.Unmarshal([]byte(hit.Content), &attempt); err != nil {
		// Content is corrupted — return a minimal result.
		return &MindsetApplyOutput{
			SystemPrompt:          "You are a focused task-execution specialist. (cache content corrupt; using fallback.)",
			SourceMindsetID:       hit.ID,
			MatchScore:            1.0,
			ToolsRecommended:      []string{},
			ModelRecommended:      "sonnet",
			CompositionIterations: 0,
			JudgeVerdict:          MindsetJudgeVerdict{Verdict: "aligned", Confidence: 1.0, Reasoning: "cache hit (content recovered as plain text)"},
			CacheHit:              true,
			CategoryUsed:          firstTagValue(hit.Tags, "c3+generalist"),
		}
	}
	return &MindsetApplyOutput{
		SystemPrompt:          composeSystemPrompt(&attempt),
		SourceMindsetID:       hit.ID,
		MatchScore:            1.0,
		ToolsRecommended:      attempt.ToolsRecommended,
		ModelRecommended:      attempt.ModelRecommended,
		CompositionIterations: 0,
		JudgeVerdict:          MindsetJudgeVerdict{Verdict: "aligned", Confidence: 1.0, Reasoning: "cache hit"},
		CacheHit:              true,
		CategoryUsed:          firstTagValue(hit.Tags, "c3+generalist"),
	}
}

// --- prompt rendering ---

// renderComposePrompt renders the meta-prompt with current inputs.
func renderComposePrompt(case_ vibecase.Case, in MindsetApplyInput, category string, lastAttempt *mindsetAttempt, lastVerdict MindsetJudgeVerdict, iter int) string {
	var b strings.Builder
	content := MetaPromptCompose
	content = strings.ReplaceAll(content, "{{VIBE_CASE}}", string(case_))
	content = strings.ReplaceAll(content, "{{TASK_DESCRIPTION}}", in.TaskDescription)
	content = strings.ReplaceAll(content, "{{MODEL_FLOOR}}", in.ModelFloor)
	content = strings.ReplaceAll(content, "{{CATEGORY}}", CategoryHint(category))

	if iter > 1 && lastAttempt != nil && lastVerdict.Reasoning != "" {
		// Build history block.
		var hist strings.Builder
		fmt.Fprintf(&hist, "Attempt %d:\n", iter-1)
		hist.WriteString("  role: " + lastAttempt.Role + "\n")
		hist.WriteString("  goal: " + lastAttempt.Goal + "\n")
		hist.WriteString("  backstory: " + truncateForErr(lastAttempt.Backstory, 200) + "\n")
		hist.WriteString("  constraints: " + strings.Join(lastAttempt.Constraints, " | ") + "\n")
		hist.WriteString("  tools_recommended: " + strings.Join(lastAttempt.ToolsRecommended, ", ") + "\n")
		hist.WriteString("  model_recommended: " + lastAttempt.ModelRecommended + "\n")
		hist.WriteString("Judge feedback (criteria_failed + reasoning):\n")
		hist.WriteString("  criteria_failed: " + strings.Join(lastVerdict.CriteriaFailed, ", ") + "\n")
		hist.WriteString("  reasoning: " + lastVerdict.Reasoning + "\n")
		content = strings.ReplaceAll(content, "{{IF PREVIOUS ATTEMPTS}}", "")
		content = strings.ReplaceAll(content, "{{END}}", "")
		content = strings.ReplaceAll(content, "{{PREVIOUS_ATTEMPTS_HISTORY}}", hist.String())
	} else {
		// First iteration: strip the entire conditional block including
		// surrounding newlines so the rendered prompt reads cleanly.
		content = stripConditionalBlock(content, "{{IF PREVIOUS ATTEMPTS}}", "{{END}}")
		content = strings.ReplaceAll(content, "{{PREVIOUS_ATTEMPTS_HISTORY}}", "")
	}

	b.WriteString(content)
	return b.String()
}

// stripConditionalBlock removes the inclusive range [open, close] from s,
// plus any leading/trailing newlines adjacent to the removed block.
// Used by renderComposePrompt to drop history blocks on first iteration.
func stripConditionalBlock(s, open, close string) string {
	start := strings.Index(s, open)
	if start == -1 {
		return s
	}
	end := strings.Index(s[start:], close)
	if end == -1 {
		return s
	}
	end += start + len(close)
	// Eat trailing newline.
	if end < len(s) && s[end] == '\n' {
		end++
	}
	// Eat leading newline.
	if start > 0 && s[start-1] == '\n' {
		start--
	}
	return s[:start] + s[end:]
}

// renderQualityPrompt renders the judge prompt with the proposed system_prompt.
func renderQualityPrompt(case_ vibecase.Case, in MindsetApplyInput, proposedSystemPrompt string) string {
	content := MetaPromptQuality
	content = strings.ReplaceAll(content, "{{VIBE_CASE}}", string(case_))
	content = strings.ReplaceAll(content, "{{TASK_DESCRIPTION}}", in.TaskDescription)
	content = strings.ReplaceAll(content, "{{PROPOSED_SYSTEM_PROMPT}}", proposedSystemPrompt)
	return content
}

// composeSystemPrompt assembles the final system_prompt from a composed
// mindset. The shape is:
//
//	You are <role>.
//
//	<goal>.
//
//	<backstory>
//
//	Constraints:
//	- <c1>
//	- <c2>
//	...
//
//	Recommended tools: <t1>, <t2>, ...
func composeSystemPrompt(m *mindsetAttempt) string {
	if m == nil {
		return ""
	}
	var b strings.Builder
	if m.Role != "" {
		b.WriteString("You are ")
		b.WriteString(m.Role)
		b.WriteString(".\n\n")
	}
	if m.Goal != "" {
		b.WriteString(m.Goal)
		b.WriteString("\n\n")
	}
	if m.Backstory != "" {
		b.WriteString(m.Backstory)
		b.WriteString("\n\n")
	}
	if len(m.Constraints) > 0 {
		b.WriteString("Constraints:\n")
		for _, c := range m.Constraints {
			b.WriteString("- ")
			b.WriteString(c)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(m.ToolsRecommended) > 0 {
		b.WriteString("Recommended tools: ")
		b.WriteString(strings.Join(m.ToolsRecommended, ", "))
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

// --- small helpers ---

// extractFirstJSONObject finds the first balanced JSON object in s.
// Returns "" if none found. Best-effort — not a full parser.
func extractFirstJSONObject(s string) string {
	start := strings.Index(s, "{")
	if start == -1 {
		return ""
	}
	depth := 0
	inString := false
	escape := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if escape {
			escape = false
			continue
		}
		if c == '\\' {
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
		if c == '{' {
			depth++
		} else if c == '}' {
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// firstTagValue extracts the first category-shaped tag from a comma-separated
// tag string. Used for cache hit output where we want to surface the
// category the cached mindset was generated under.
func firstTagValue(tags string, fallback string) string {
	for _, t := range strings.Split(tags, ",") {
		t = strings.TrimSpace(t)
		if strings.HasPrefix(t, "c1/") || strings.HasPrefix(t, "c2/") || strings.HasPrefix(t, "c3+/") {
			return t
		}
	}
	return fallback
}

// wrapTimeoutVerdict converts a deadline-exceeded error into a needs_human
// verdict for the loop-exhaustion branch.
func wrapTimeoutVerdict(prev MindsetJudgeVerdict, iter int, err error) MindsetJudgeVerdict {
	return MindsetJudgeVerdict{
		Verdict:   "needs_human",
		Confidence: prev.Confidence,
		Reasoning: fmt.Sprintf("timeout after %d iterations: %v", iter, err),
	}
}