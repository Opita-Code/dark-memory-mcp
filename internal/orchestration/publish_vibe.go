// O7: PublishVibe — the canonical "publish a generated artifact" entry
// point. Composes the full vibe-flow loop: spec_create + artifact_log +
// optional brand_match + optional compliance_check + drift_judge +
// drift_log. Returns a verdict + a NextAction hint so the calling
// agent can branch on aligned / reconcile / human_gate.
//
// This is a META-orchestrator: it calls SaveSpec, SaveArtifact, Judge
// (multiple times), and SaveDriftReport. Each call goes through the
// Store, which preserves INV-1 (write-path audit) atomically.
//
// INV-3 (canary): if any of the judge-side payloads (artifact.Text)
// contains the active canary, the canary check in Judge refuses the
// call before the LLM is touched. PublishVibe surfaces that error.
//
// INV-7 (per-project scoping): requires an active project. SaveSpec /
// SaveArtifact / SaveDriftReport all run with the project_id stamped
// by the Store layer.
//
// LLM availability: when no LLM is configured (test env, or operator
// running dark-memory-mcp without a key), drift_judge cannot run. In
// that case PublishVibe still persists the spec + artifact, AND it
// emits a drift_log row with verdict="drift_detected" and a
// judge_reasoning explaining the skip, so the audit trail is complete.
// NextAction becomes "human_gate" — the operator must run the judge
// manually or attach a key.
package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/errorobs"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/vibecase"
	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

// PublishSpecInput is the spec half of a PublishVibe call. The caller
// supplies the intent (what to build) and the constitution (rules to
// respect). Both are persisted as opaque JSON strings.
type PublishSpecInput struct {
	VibeCase     string `json:"vibe_case"`
	Constitution string `json:"constitution,omitempty"`
	Spec         string `json:"spec,omitempty"`
	Tasks        string `json:"tasks,omitempty"`
}

// PublishArtifactInput is the artifact half of a PublishVibe call. Text
// is the body of the artifact (used for drift_judge + brand_match +
// compliance_check). URL is the canonical location.
//
// v2.8.0-alpha A1: AutoSaveDecision (default true when
// DARK_MEMORY_V280=1) triggers a kind=decision agent_memory row when
// drift_judge verdict=aligned. Set false to suppress per-call.
type PublishArtifactInput struct {
	ArtifactType  string `json:"artifact_type"`            // code|text|image|video|audio|multi
	ArtifactURL   string `json:"artifact_url"`             // where it lives
	Text          string `json:"text,omitempty"`           // body, for judges
	BrandID       string `json:"brand_id,omitempty"`       // triggers brand_match if set
	Jurisdiction  string `json:"jurisdiction,omitempty"`   // triggers compliance_check if set
	HasDisclosure bool   `json:"has_disclosure,omitempty"` // EU AI Act flag for synthetic media
	// AutoSaveDecision (v2.8.0-alpha A1) — when true (default when
	// DARK_MEMORY_V280=1) AND verdict=aligned, auto-create a
	// kind=decision agent_memory row tagged with the spec id. Set
	// to false to suppress per-call (the auto-save might add noise
	// for trivial decisions).
	AutoSaveDecision bool `json:"auto_save_decision,omitempty"`
}

// PublishVibeInput is the full request to publish one artifact under
// one spec.
type PublishVibeInput struct {
	Spec     PublishSpecInput     `json:"spec"`
	Artifact PublishArtifactInput `json:"artifact"`
	// AutoDriftCheck: pointer so we can distinguish "unset" (default
	// true — run drift_judge) from "explicitly false" (skip). The
	// bool zero value would otherwise default to false and surprise
	// callers who don't set the field.
	AutoDriftCheck *bool  `json:"auto_drift_check,omitempty"`
	SessionID      string `json:"session_id,omitempty"` // recorded on the artifact for INV-2
	// AgentID (v2.4.1) is the Mem0 agent_id (LLM identity) that
	// owns this artifact. Optional; resolved with priority
	// (caller input > projects.default_agent_id > ""). When set,
	// the drift_judge enrichment prepends only agent_memory rows
	// authored by this agent_id (cross-agent leakage prevention;
	// see v2.4.0 -> v2.4.1 changelog).
	AgentID string `json:"agent_id,omitempty"`
}

// PublishResult is what PublishVibe returns. Every ID is the row ID in
// the corresponding table. Verdict + Confidence reflect the drift
// judgment (or "skipped" if AutoDriftCheck=false). NextAction tells
// the calling agent what to do next.
type PublishResult struct {
	SpecID           int64   `json:"spec_id"`
	ArtifactID       int64   `json:"artifact_id"`
	DriftID          int64   `json:"drift_id,omitempty"`
	BrandEvalID      int64   `json:"brand_eval_id,omitempty"`
	ComplianceEvalID int64   `json:"compliance_eval_id,omitempty"`
	Verdict          string  `json:"verdict"`     // aligned | drift_detected | needs_human | skipped
	Confidence       float32 `json:"confidence"`  // 0..1; 0 if skipped or no-LLM
	NextAction       string  `json:"next_action"` // publish | reconcile | human_gate
	Reasoning        string  `json:"reasoning"`   // human-readable explanation
	// ActiveAgentID (v2.4.1) echoes the resolved agent_id used for
	// drift_judge enrichment. Empty when no agent_id is configured.
	ActiveAgentID string `json:"active_agent_id,omitempty"`
	// AutoSavedDecisionID (v2.8.0-alpha A1) is the row id of the
	// kind=decision agent_memory row auto-saved when verdict=aligned.
	// 0 when no row was saved (verdict != aligned, AutoSaveDecision=false,
	// V280 flag off, or save failed).
	AutoSavedDecisionID int64 `json:"auto_saved_decision_id,omitempty"`
	// AutoArchivedTodoIDs (v2.8.0-alpha A4) is the list of kind=todo
	// row ids auto-archived when verdict=aligned (todos tied to the
	// spec id are closed). Empty when no todos existed or verdict !=
	// aligned.
	AutoArchivedTodoIDs []int64 `json:"auto_archived_todo_ids,omitempty"`
}

// PublishVibe is the canonical publish entry point. See package doc.
func (o *Orchestrator) PublishVibe(ctx context.Context, in PublishVibeInput) (*PublishResult, error) {
	// 1. Validate required fields.
	if strings.TrimSpace(in.Artifact.ArtifactURL) == "" {
		return nil, errMissingField("artifact.artifact_url")
	}
	if strings.TrimSpace(in.Artifact.ArtifactType) == "" {
		return nil, errMissingField("artifact.artifact_type")
	}
	if strings.TrimSpace(in.Spec.VibeCase) == "" {
		return nil, errMissingField("spec.vibe_case")
	}
	// v1.4.1: vibe_case must be a canonical C1..C7 identifier.
	// Defense in depth: the JSON Schema layer (internal/tools/vibe.go)
	// already validates via the enum constraint, but the orchestrator
	// re-validates so direct orchestrator calls (and any future
	// non-MCP transport) cannot persist an unknown case. Both layers
	// derive from internal/vibecase — single source of truth.
	if _, err := vibecase.Parse(in.Spec.VibeCase); err != nil {
		return nil, store.NewFieldError(store.ErrInvalidArgument, "spec.vibe_case: "+err.Error())
	}

	// 2. Persist the spec. SaveSpec enforces INV-1 (write_audit) and
	// INV-7 (project_id tagging) inside the Store layer.
	now := o.now().Format(time.RFC3339Nano)
	spec := &vibeflow.Spec{
		VibeCase:     in.Spec.VibeCase,
		Constitution: in.Spec.Constitution,
		Spec:         in.Spec.Spec,
		Tasks:        in.Spec.Tasks,
		CreatedAt:    now,
	}
	wc := store.WriteContext{
		Actor:     "orchestrator_publish_vibe",
		SessionID: in.SessionID,
		WritePath: "PublishVibe",
	}
	specID, err := o.Store.SaveSpec(ctx, wc, spec)
	if err != nil {
		return nil, fmt.Errorf("publish_vibe: save spec: %w", err)
	}

	// 3. Persist the artifact, linked to spec_id.
	artifact := &vibeflow.Artifact{
		SessionID:        in.SessionID,
		VibeCase:         in.Spec.VibeCase,
		SpecID:           specID,
		ArtifactURL:      in.Artifact.ArtifactURL,
		ArtifactType:     in.Artifact.ArtifactType,
		BrandID:          in.Artifact.BrandID,
		Jurisdiction:     in.Artifact.Jurisdiction,
		HasDisclosure:    in.Artifact.HasDisclosure,
		ValidationStatus: "pending",
		CreatedAt:        now,
	}
	artifactID, err := o.Store.SaveArtifact(ctx, wc, artifact)
	if err != nil {
		return nil, fmt.Errorf("publish_vibe: save artifact: %w", err)
	}

	result := &PublishResult{
		SpecID:     specID,
		ArtifactID: artifactID,
		Verdict:    "needs_human", // pessimistic default; updated after drift
		NextAction: "human_gate",
		Reasoning:  "drift check pending",
	}

	// v2.4.1: resolve the active agent_id for drift_judge enrichment.
	// Priority: caller input > projects.default_agent_id > "".
	// Empty agent_id means "no agent filter" — drift_judge sees
	// agent_memory rows across all agents in the project (v2.4.0
	// behavior). Best-effort: resolver swallows errors and falls
	// back to "" on Store failure.
	activeAgentID := o.resolveActiveAgentID(ctx, in.AgentID)
	result.ActiveAgentID = activeAgentID

	// 4. Optional brand_match. Only runs if both BrandID and Text are
	// set. Judge failures (incl. canary rejection) are recorded in
	// the drift reasoning but do not abort publish — the artifact is
	// already persisted; the brand issue is reported alongside.
	var brandEvalID int64
	if in.Artifact.BrandID != "" && in.Artifact.Text != "" {
		out, err := o.Judge(ctx, JudgeInput{
			EvalType:   "brand_match",
			TargetType: "artifact",
			TargetID:   fmt.Sprintf("artifact_%d", artifactID),
			Content:    in.Artifact.Text,
		})
		if err != nil {
			result.Reasoning = fmt.Sprintf("brand_match failed: %v", err)
			// v2.11.0 (spec 757): was appended to Reasoning only (a
			// silent-ish discard); now also captured durably. LLM
			// domain — the judge call itself failed.
			o.RecordError(ctx, "publish_vibe", in.SessionID, fmt.Errorf("brand_match: %w", err), errorobs.SeverityWarn)
		} else {
			brandEvalID = out.EvaluationID
			if out.Confidence < 0.5 {
				result.Reasoning = fmt.Sprintf("brand_match low confidence (%f); drift_verdict will fall back to needs_human", out.Confidence)
			}
		}
	}
	result.BrandEvalID = brandEvalID

	// 5. Optional compliance_check. Same pattern.
	var compEvalID int64
	if in.Artifact.Jurisdiction != "" && in.Artifact.Text != "" {
		out, err := o.Judge(ctx, JudgeInput{
			EvalType:   "compliance_check",
			TargetType: "artifact",
			TargetID:   fmt.Sprintf("artifact_%d", artifactID),
			Content:    in.Artifact.Text,
		})
		if err != nil {
			result.Reasoning = result.Reasoning + "; compliance_check failed: " + err.Error()
			// v2.11.0 (spec 757): durable capture (was string-only).
			o.RecordError(ctx, "publish_vibe", in.SessionID, fmt.Errorf("compliance_check: %w", err), errorobs.SeverityWarn)
		} else {
			compEvalID = out.EvaluationID
		}
	}
	result.ComplianceEvalID = compEvalID

	// 6. Drift judge. This is the canonical "did the artifact match
	// the spec" verdict. AutoDriftCheck=false skips the LLM call and
	// records verdict="skipped"; AutoDriftCheck=true (default) tries
	// to call the LLM, and if unavailable records drift_detected with
	// reasoning explaining the skip.
	autoCheck := true
	if in.AutoDriftCheck != nil {
		autoCheck = *in.AutoDriftCheck
	}
	if !autoCheck {
		result.Verdict = "skipped"
		result.Confidence = 0
		result.NextAction = "publish" // operator manually reviews
		result.Reasoning = "auto_drift_check=false; operator reviews manually"
	} else if in.Artifact.Text == "" {
		result.Verdict = "skipped"
		result.Confidence = 0
		result.NextAction = "publish"
		result.Reasoning = "no artifact text; drift_judge requires text body"
	} else {
		// v2.4.0 memory-RAG: prepend agent_memory hits to the
		// drift_judge content so the judge sees relevant prior
		// decisions / findings when scoring the artifact. v2.4.1:
		// filtered by activeAgentID so the judge only sees prior
		// context authored by the same agent (no cross-agent
		// leakage when multiple LLMs share a project).
		// Best-effort: errors are swallowed — drift_judge runs even
		// if agent_memory is broken (the data-plane should never
		// break the judge path; that's the principle that keeps
		// the VLP integration non-blocking).
		enriched := o.enrichWithAgentMemory(ctx, in.Artifact.Text, []string{"decision", "finding"}, activeAgentID, 5)
		judgeOut, jerr := o.Judge(ctx, JudgeInput{
			EvalType:   "drift_judge",
			TargetType: "artifact",
			TargetID:   fmt.Sprintf("artifact_%d", artifactID),
			Content:    enriched,
		})
		if jerr != nil {
			// v2.11.0 (spec 757) T6 — drift/error conflation fix:
			// previously an LLM failure produced verdict="drift_detected"
			// (the SAME verdict as genuine semantic drift), so the
			// operator believed the artifact drifted when the judge
			// simply never ran. Now:
			//   - verdict = "needs_human" (NO verdict was produced —
			//     the infra failed, not the artifact)
			//   - error captured durably in the Error Observatory
			//     (domain=llm)
			//   - NextAction stays "human_gate" (operator retries with
			//     a key or runs manual drift check)
			o.RecordError(ctx, "publish_vibe", in.SessionID, fmt.Errorf("drift_judge: %w", jerr), errorobs.SeverityError)
			result.Verdict = "needs_human"
			result.Confidence = 0
			result.NextAction = "human_gate"
			result.Reasoning = fmt.Sprintf("drift_judge unavailable (LLM infra failure, not drift): %v", jerr)
		} else {
			result.Verdict = parseDriftVerdict(judgeOut.VerdictJSON, judgeOut.Confidence)
			result.Confidence = judgeOut.Confidence
			result.NextAction = nextActionForVerdict(result.Verdict)
			result.Reasoning = "drift_judge ok: " + judgeOut.VerdictJSON
		}
	}

	// 7. Persist drift_log (always; even on skipped/no-LLM).
	d := &vibeflow.DriftReport{
		ArtifactID:     artifactID,
		SpecID:         specID,
		Verdict:        result.Verdict,
		JudgeReasoning: result.Reasoning,
		CreatedAt:      now,
	}
	if result.Verdict == "aligned" {
		// Aligned = immediately reconciled (no drift to fix).
		reconciled := o.now().Format(time.RFC3339Nano)
		d.ReconciledAt = reconciled
	}
	driftID, derr := o.Store.SaveDriftReport(ctx, wc, d)
	if derr != nil {
		// Don't fail the whole publish — drift_log is best-effort.
		// The caller sees the IDs; the drift row can be retried.
		// v2.11.0 (spec 757): was appended to Reasoning only; now
		// also captured durably (store domain — the persist failed).
		o.RecordError(ctx, "publish_vibe", in.SessionID, fmt.Errorf("drift_log save: %w", derr), errorobs.SeverityWarn)
		result.Reasoning = result.Reasoning + "; drift_log save failed: " + derr.Error()
	} else {
		result.DriftID = driftID
	}

	// 8. Update artifact validation_status based on verdict.
	switch result.Verdict {
	case "aligned":
		if err := o.Store.SetArtifactValidation(ctx, wc, artifactID, "passed"); err != nil {
			result.Reasoning = result.Reasoning + "; set validation=passed failed: " + err.Error()
		}
	case "skipped":
		// Leave at pending; operator reviews.
	default:
		if err := o.Store.SetArtifactValidation(ctx, wc, artifactID, "failed"); err != nil {
			result.Reasoning = result.Reasoning + "; set validation=failed failed: " + err.Error()
		}
	}

	// 9. v2.8.0-alpha A1 + A4: auto-save decision + auto-archive
	// todos when verdict=aligned AND DARK_MEMORY_V280=1.
	if v280Enabled() && result.Verdict == "aligned" {
		if in.Artifact.AutoSaveDecision {
			o.autoSaveDecisionOnAligned(ctx, wc, specID, artifactID, in, result, activeAgentID)
		}
		o.autoArchiveSpecTodosOnAligned(ctx, wc, specID, result)
	}

	return result, nil
}

// autoSaveDecisionOnAligned is the A1 hook. When drift_judge says the
// artifact matches the spec, this persists a kind=decision
// agent_memory row capturing the spec intent + the artifact URL +
// the drift_judge reasoning. Pinned=true (decisions are reference
// material for future sessions). Tags include spec:<id>,verdict:aligned,
// artifact:<url> so future searches can find it.
//
// Best-effort: failures are logged + appended to result.Reasoning
// (the publish itself succeeded; the decision auto-save is gravy).
func (o *Orchestrator) autoSaveDecisionOnAligned(
	ctx context.Context,
	wc store.WriteContext,
	specID, artifactID int64,
	in PublishVibeInput,
	result *PublishResult,
	activeAgentID string,
) {
	// Build decision content from spec intent (truncated) +
	// artifact URL + drift_judge reasoning (truncated).
	specIntent := in.Spec.Spec
	if len(specIntent) > 500 {
		specIntent = specIntent[:500] + "..."
	}
	reasoning := result.Reasoning
	if len(reasoning) > 500 {
		reasoning = reasoning[:500] + "..."
	}
	content := fmt.Sprintf(
		"Spec %d verdict=aligned (confidence=%.2f)\n\nIntent:\n%s\n\nArtifact:\n%s\n\nConstraint:\n%s\n\nPinned by auto-save on vibe_publish %s",
		specID, result.Confidence, specIntent, in.Artifact.ArtifactURL,
		reasoning, o.now().Format(time.RFC3339Nano),
	)
	tags := fmt.Sprintf("spec:%d,verdict:aligned,artifact:%s",
		specID, in.Artifact.ArtifactURL)

	operator := wc.Actor
	if operator == "" {
		operator = o.activeOperator(ctx)
	}
	if operator == "" {
		operator = "orchestrator_publish_vibe"
	}

	saveInput := AgentMemorySaveInput{
		Operator: operator,
		AgentID:  activeAgentID,
		Kind:     agentmemory.KindDecision,
		Title:    fmt.Sprintf("Decision from spec %d", specID),
		Content:  content,
		Tags:     tags,
		Pinned:   true,
	}
	out, err := o.AgentMemorySave(ctx, saveInput)
	if err != nil {
		log.Printf("dark-mem-mcp: publish_vibe auto-save decision err=%v (spec_id=%d)", err, specID)
		result.Reasoning = result.Reasoning + "; auto-save decision failed: " + err.Error()
		return
	}
	result.AutoSavedDecisionID = out.Row.ID
}

// autoArchiveSpecTodosOnAligned is the A4 hook. When drift_judge says
// aligned, the spec's tasks are now complete — the matching kind=todo
// rows should be archived (closed). This implements the todo lifecycle
// (open from vibe_spec → closed on aligned publish).
//
// Best-effort: failures are logged + appended to result.Reasoning.
func (o *Orchestrator) autoArchiveSpecTodosOnAligned(
	ctx context.Context,
	wc store.WriteContext,
	specID int64,
	result *PublishResult,
) {
	// List open todos tagged with this spec_id. The Tag filter
	// matches rows whose comma-separated tags contain this token
	// (case-insensitive). Spec id is included in the row's tags
	// by vibe_spec's auto-save hook (A4).
	tagFilter := fmt.Sprintf("spec:%d", specID)
	todos, err := o.Store.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{
		Kind:            agentmemory.KindTodo,
		Tag:             tagFilter,
		IncludeArchived: false,
		Limit:           100,
	})
	if err != nil {
		log.Printf("dark-mem-mcp: publish_vibe list-spec-todos err=%v (spec_id=%d)", err, specID)
		result.Reasoning = result.Reasoning + "; list spec todos failed: " + err.Error()
		return
	}

	var archived []int64
	for _, todo := range todos {
		if err := o.Store.ArchiveAgentMemory(ctx, wc, todo.ID); err != nil {
			log.Printf("dark-mem-mcp: publish_vibe archive-todo err=%v (todo_id=%d)", err, todo.ID)
			continue
		}
		archived = append(archived, todo.ID)
	}
	if len(archived) > 0 {
		result.AutoArchivedTodoIDs = archived
	}
}

// parseDriftVerdict maps an LLM Judge verdict JSON to one of the
// canonical drift verdicts: aligned | drift_detected | needs_human.
//
// Two JSON shapes are accepted:
//
//	Modern (post-v1.4.0 dark_memory_judge):
//	  {"verdict":"aligned"|"drift_detected"|"needs_human",
//	   "confidence":0.92, "reasoning":"..."}
//
//	Legacy:
//	  {"aligned":true, "confidence":0.92, "issues":[]}
//	  {"aligned":false, "drift_items":["..."], "confidence":0.85}
//
// confidence < 0.5 always returns "needs_human" regardless of the LLM's
// verdict — that's the floor at which we trust the LLM-as-judge.
//
// INFRA-001 fix: pre-fix version only recognized the legacy {"aligned":bool}
// shape and silently returned "drift_detected" for modern output, causing
// every VibePublish to be misclassified. The fix checks the modern shape
// first (canonical v1.4.0+), then falls back to legacy, then to a
// whitespace/case-tolerant substring match.
func parseDriftVerdict(verdictJSON string, confidence float32) string {
	if confidence < 0.5 {
		return "needs_human"
	}
	// Try to parse the JSON; check both shapes.
	var v map[string]any
	if err := json.Unmarshal([]byte(verdictJSON), &v); err == nil {
		// Modern shape (preferred, post-v1.4.0).
		if verdict, ok := v["verdict"].(string); ok {
			switch verdict {
			case "aligned":
				return "aligned"
			case "drift_detected":
				return "drift_detected"
			case "needs_human":
				return "needs_human"
			}
		}
		// Legacy shape: boolean `aligned` field.
		if aligned, ok := v["aligned"].(bool); ok {
			if aligned {
				return "aligned"
			}
			return "drift_detected"
		}
	}
	// Lenient fallback: substring match on a whitespace-collapsed,
	// lowercased copy of the raw JSON. Handles whitespace, case
	// variation, and partial parses.
	normalized := strings.ToLower(verdictJSON)
	// Collapse runs of whitespace into a single space so that
	// pretty-printed JSON like `"Verdict" : "aligned"` matches
	// `"verdict":"aligned"`.
	var b strings.Builder
	b.Grow(len(normalized))
	prevSpace := false
	for _, r := range normalized {
		isSpace := r == ' ' || r == '\t' || r == '\n' || r == '\r'
		if isSpace {
			if !prevSpace {
				b.WriteRune(' ')
			}
			prevSpace = true
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	// Step 2: collapse whitespace around colons. The first ReplaceAll
	// handles `: ` (colon followed by space, e.g. `"key": "value"`).
	// The second handles ` :` (space followed by colon, e.g. `"key" : "value"`).
	// Apply both to normalise pretty-printed JSON to the canonical compact form.
	compact := strings.ReplaceAll(b.String(), `: `, `:`)
	compact = strings.ReplaceAll(compact, ` :`, `:`)
	if strings.Contains(compact, `"verdict":"aligned"`) ||
		strings.Contains(compact, `"aligned":true`) ||
		strings.Contains(compact, `"drift":false`) {
		return "aligned"
	}
	return "drift_detected"
}

// nextActionForVerdict maps a verdict to a NextAction string the
// calling agent can branch on.
func nextActionForVerdict(v string) string {
	switch v {
	case "aligned":
		return "publish"
	case "drift_detected":
		return "reconcile"
	default:
		return "human_gate"
	}
}

// enrichWithAgentMemory is the v2.4.0 memory-RAG prepend helper.
// Takes a base text + a list of kinds to filter on (defaults to
// "decision" + "finding" when len(kinds)==0) + an agent_id
// (v2.4.1; empty = project-wide — v2.4.0 behavior) + a limit
// (defaults to 5, max 50). Returns the base text with the formatted
// hit block prepended, OR the base text unchanged if (a) agent_memory
// is broken, (b) no project is active, or (c) no hits. Errors are
// swallowed — the caller should never see an error from this path.
//
// v2.4.1: agent_id is forwarded to recallForVibe so the judge only
// sees prior context from the same agent. When multiple LLMs share
// a project, this prevents drift_judge from being misled by another
// agent's unrelated decisions / findings.
func (o *Orchestrator) enrichWithAgentMemory(ctx context.Context, base string, kinds []string, agentID string, limit int) string {
	if strings.TrimSpace(base) == "" {
		return base
	}
	if len(kinds) == 0 {
		kinds = []string{"decision", "finding"}
	}
	// Filter hits across the supplied kinds; in practice we run the
	// search once with no kind filter and re-filter the hits
	// in-process (cheaper than N round trips). v2.4.1: agentID
	// forwarded to SearchAgentMemory for per-agent filtering.
	hits, err := o.recallForVibe(ctx, base, "", "", agentID, limit*len(kinds))
	if err != nil || len(hits) == 0 {
		return base
	}
	filtered := make([]agentmemory.SearchHit, 0, len(hits))
	want := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		want[k] = true
	}
	for _, h := range hits {
		if want[h.Kind] {
			filtered = append(filtered, h)
			if len(filtered) >= limit {
				break
			}
		}
	}
	if len(filtered) == 0 {
		return base
	}
	block := formatHitsForContext(filtered)
	if block == "" {
		return base
	}
	return block + "\n\n=== Artifact under review ===\n\n" + base
}
