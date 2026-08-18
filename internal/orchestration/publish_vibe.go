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
	"github.com/dark-agents/dark-memory-mcp/internal/vlp"
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
	// AsyncDriftCheck (v2.14.0, spec 998 p1): when true, drift_judge
	// (and brand_match + compliance_check) run in a BACKGROUND goroutine
	// and PublishVibe returns immediately with verdict="pending" +
	// next_action="poll". The operator polls pipeline_status for the
	// final verdict. This fixes the sync-path UX where the MCP call
	// blocked 10-30s+ on the LLM judge (the root reason we raised the
	// opencode MCP timeout to 120s). Default false = legacy synchronous
	// behavior (backward compatible).
	AsyncDriftCheck bool `json:"async_drift_check,omitempty"`
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
	Verdict          string  `json:"verdict"`     // aligned | drift_detected | needs_human | skipped | pending
	Confidence       float32 `json:"confidence"`  // 0..1; 0 if skipped or no-LLM
	NextAction       string  `json:"next_action"` // publish | reconcile | human_gate | poll
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
	// Async (v2.14.0, spec 998 p1): true when the drift check is
	// running in the background and Verdict="pending". The operator
	// polls pipeline_status(artifact_id) for the final verdict.
	Async bool `json:"async,omitempty"`
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

	// v2.13.0 (spec 952 T2): auto-emit EventVibePublish so the VLP
	// advances drafting_spec → spec_active without a separate harness
	// call. Best-effort: if the harness already advanced manually,
	// ErrInvalidTransition is a silent no-op.
	o.emitVLP(ctx, in.SessionID, "orchestrator_publish_vibe", vlp.EventVibePublish)

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

	// v2.13.0 (spec 952 T2): auto-emit EventArtifactLog so the VLP
	// advances spec_active → drift_judging. Best-effort.
	o.emitVLP(ctx, in.SessionID, "orchestrator_publish_vibe", vlp.EventArtifactLog)

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

	autoCheck := true
	if in.AutoDriftCheck != nil {
		autoCheck = *in.AutoDriftCheck
	}

	// v2.14.0 (spec 998 p1): AsyncDriftCheck=true runs the judge
	// pipeline in a background goroutine and returns immediately with
	// verdict="pending" + next_action="poll". The drift report row is
	// persisted as "pending" now (so pipeline_status has something to
	// poll), and the background judge updates it in place when done.
	if in.AsyncDriftCheck {
		result.Async = true
		result.Verdict = "pending"
		result.NextAction = "poll"
		result.Reasoning = "async drift check running; poll pipeline_status(artifact_id=" + fmt.Sprintf("%d", artifactID) + ")"
		// Persist the pending drift report first so pipeline_status
		// returns a row immediately (not nil).
		dPending := &vibeflow.DriftReport{
			ArtifactID:     artifactID,
			SpecID:         specID,
			Verdict:        "pending",
			JudgeReasoning: "async drift check started; awaiting background judge",
			CreatedAt:      o.now().Format(time.RFC3339Nano),
		}
		pendingDriftID, derr := o.Store.SaveDriftReport(ctx, wc, dPending)
		if derr != nil {
			o.RecordError(ctx, "publish_vibe", in.SessionID, fmt.Errorf("async drift_log save: %w", derr), errorobs.SeverityWarn)
			result.Reasoning = result.Reasoning + "; pending drift_log save failed: " + derr.Error()
		} else {
			result.DriftID = pendingDriftID
		}
		// Fire the background judge. context.Background detaches from
		// the MCP call returning (the request ctx would be cancelled).
		o.runAsyncJudgePipeline(ctx, wc, in, specID, artifactID, activeAgentID, autoCheck, pendingDriftID, result)
		return result, nil
	}

	// 4-6. brand_match (optional) + compliance_check (optional) +
	// drift_judge (the canonical verdict). Run through the shared
	// runJudgePipeline so the sync path and the async background
	// goroutine execute IDENTICAL judge logic.
	verdict, confidence, reasoning, brandEvalID, compEvalID := o.runJudgePipeline(ctx, wc, in, specID, artifactID, activeAgentID, autoCheck)
	result.Verdict = verdict
	result.Confidence = confidence
	result.Reasoning = reasoning
	result.BrandEvalID = brandEvalID
	result.ComplianceEvalID = compEvalID
	// Map the canonical verdict to the harness-branching hint. The
	// result struct is initialized to NextAction="human_gate" as a
	// pessimistic default; without this override the harness can
	// never tell aligned (publish) from drift_detected (reconcile)
	// in the sync path. Bug surfaced in pre-existing CI run 31815582636.
	result.NextAction = nextActionForVerdict(verdict)

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

	// v2.13.0 (spec 952 T2): auto-emit EventDriftLog with the
	// verdict so the VLP advances drift_judging → complete |
	// needs_human | spec_active (loop). Best-effort: if the harness
	// advanced manually, silence the ErrInvalidTransition.
	o.emitVLPWithVerdict(ctx, in.SessionID, "orchestrator_publish_vibe",
		vlp.EventDriftLog, verdictToVLP(result.Verdict))

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

// runJudgePipeline executes the optional brand_match + compliance_check
// + drift_judge judges and returns the canonical verdict triad
// (verdict, confidence, reasoning) plus the eval IDs of the optional
// judges. Shared by the sync publish path and the async background
// goroutine so both execute IDENTICAL judge logic.
//
// Contract (unchanged from pre-v2.14.0 sync behavior):
//   - autoCheck=false → skipped (no LLM calls).
//   - no artifact text → skipped (drift_judge requires text).
//   - LLM failure → needs_human (infra, not drift) + Error Observatory.
//   - low confidence (<0.5) from any judge → needs_human fallback.
func (o *Orchestrator) runJudgePipeline(
	ctx context.Context,
	wc store.WriteContext,
	in PublishVibeInput,
	specID, artifactID int64,
	activeAgentID string,
	autoCheck bool,
) (verdict string, confidence float32, reasoning string, brandEvalID, compEvalID int64) {
	reasoning = "drift check pending"

	// Optional brand_match.
	if in.Artifact.BrandID != "" && in.Artifact.Text != "" {
		out, err := o.Judge(ctx, JudgeInput{
			EvalType:   "brand_match",
			TargetType: "artifact",
			TargetID:   fmt.Sprintf("artifact_%d", artifactID),
			Content:    in.Artifact.Text,
		})
		if err != nil {
			reasoning = fmt.Sprintf("brand_match failed: %v", err)
			o.RecordError(ctx, "publish_vibe", in.SessionID, fmt.Errorf("brand_match: %w", err), errorobs.SeverityWarn)
		} else {
			brandEvalID = out.EvaluationID
			if out.Confidence < 0.5 {
				reasoning = fmt.Sprintf("brand_match low confidence (%f); drift_verdict will fall back to needs_human", out.Confidence)
			}
		}
	}

	// Optional compliance_check.
	if in.Artifact.Jurisdiction != "" && in.Artifact.Text != "" {
		out, err := o.Judge(ctx, JudgeInput{
			EvalType:   "compliance_check",
			TargetType: "artifact",
			TargetID:   fmt.Sprintf("artifact_%d", artifactID),
			Content:    in.Artifact.Text,
		})
		if err != nil {
			reasoning = reasoning + "; compliance_check failed: " + err.Error()
			o.RecordError(ctx, "publish_vibe", in.SessionID, fmt.Errorf("compliance_check: %w", err), errorobs.SeverityWarn)
		} else {
			compEvalID = out.EvaluationID
		}
	}

	// Canonical drift_judge.
	if !autoCheck {
		return "skipped", 0, "auto_drift_check=false; operator reviews manually", brandEvalID, compEvalID
	}
	if in.Artifact.Text == "" {
		return "skipped", 0, "no artifact text; drift_judge requires text body", brandEvalID, compEvalID
	}
	enriched := o.enrichWithAgentMemory(ctx, in.Artifact.Text, []string{"decision", "finding"}, activeAgentID, 5)
	judgeOut, jerr := o.Judge(ctx, JudgeInput{
		EvalType:   "drift_judge",
		TargetType: "artifact",
		TargetID:   fmt.Sprintf("artifact_%d", artifactID),
		Content:    enriched,
		// TD-J5 P0 fix: the spec intent must reach the judge. The
		// JudgeInput.SpecIntent field exists since v2.17.0 and the
		// tool path honors it, but the vibe_publish pipeline never
		// populated it — the judge compared the artifact against an
		// absent spec and (correctly) said needs_human.
		SpecIntent: in.Spec.Spec,
	})
	if jerr != nil {
		o.RecordError(ctx, "publish_vibe", in.SessionID, fmt.Errorf("drift_judge: %w", jerr), errorobs.SeverityError)
		return "needs_human", 0, fmt.Sprintf("drift_judge unavailable (LLM infra failure, not drift): %v", jerr), brandEvalID, compEvalID
	}
	v := parseDriftVerdict(judgeOut.VerdictJSON, judgeOut.Confidence)
	return v, judgeOut.Confidence, "drift_judge ok: " + judgeOut.VerdictJSON, brandEvalID, compEvalID
}

// runAsyncJudgePipeline is the v2.14.0 (spec 998 p1) background drift
// judge. It runs the SAME runJudgePipeline as the sync path but:
//   - detached from the request ctx (context.Background + timeout) so
//     it survives the MCP call returning;
//   - updates the pending drift report row in place via
//     UpdateDriftReportVerdict;
//   - sets artifact validation status;
//   - emits the VLP drift_log event with the final verdict (the VLP
//     sits at drift_judging while the judge runs — correct semantics);
//   - runs the A1 auto-save decision + A4 auto-archive todos hooks.
//
// Failure isolation: any panic or judge error is recorded in the Error
// Observatory and the drift report is updated to needs_human so the
// operator never sees a stuck "pending". The goroutine NEVER fails the
// original publish call (the artifact + pending drift row are already
// persisted when this runs).
func (o *Orchestrator) runAsyncJudgePipeline(
	ctx context.Context,
	wc store.WriteContext,
	in PublishVibeInput,
	specID, artifactID int64,
	activeAgentID string,
	autoCheck bool,
	pendingDriftID int64,
	result *PublishResult,
) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				o.RecordError(context.Background(), "publish_vibe_async", in.SessionID, fmt.Errorf("async drift_judge panic: %v", r), errorobs.SeverityError)
				if pendingDriftID > 0 {
					_ = o.Store.UpdateDriftReportVerdict(context.Background(), wc, pendingDriftID, "needs_human", "async drift_judge panic: "+fmt.Sprint(r))
				}
			}
		}()
		// Detach from the caller's request context: the MCP call is
		// returning right now and its ctx will be cancelled. Bound the
		// background work with its own timeout so a wedged LLM cannot
		// leak a goroutine forever. 120s matches defaultJudgeTimeoutMS
		// (base per-attempt timeout) — one judge round trip with retries.
		bgCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()

		v, conf, reasoning, _, _ := o.runJudgePipeline(bgCtx, wc, in, specID, artifactID, activeAgentID, autoCheck)

		// Update the pending drift report in place.
		if pendingDriftID > 0 {
			if err := o.Store.UpdateDriftReportVerdict(bgCtx, wc, pendingDriftID, v, reasoning); err != nil {
				o.RecordError(bgCtx, "publish_vibe_async", in.SessionID, fmt.Errorf("update drift report verdict: %w", err), errorobs.SeverityWarn)
			}
		}

		// Set artifact validation status (same mapping as sync).
		validationWC := wc
		switch v {
		case "aligned":
			if err := o.Store.SetArtifactValidation(bgCtx, validationWC, artifactID, "passed"); err != nil {
				o.RecordError(bgCtx, "publish_vibe_async", in.SessionID, fmt.Errorf("set validation=passed: %w", err), errorobs.SeverityWarn)
			}
		case "skipped":
			// leave pending; operator reviews.
		default:
			if err := o.Store.SetArtifactValidation(bgCtx, validationWC, artifactID, "failed"); err != nil {
				o.RecordError(bgCtx, "publish_vibe_async", in.SessionID, fmt.Errorf("set validation=failed: %w", err), errorobs.SeverityWarn)
			}
		}

		// Emit the VLP drift_log with the final verdict — the loop
		// advances drift_judging → complete | needs_human | spec_active.
		o.emitVLPWithVerdict(bgCtx, in.SessionID, "orchestrator_publish_vibe_async", vlp.EventDriftLog, verdictToVLP(v))

		// A1 + A4 hooks (same as sync aligned path).
		if v280Enabled() && v == "aligned" {
			// Rebuild a result with the final verdict for the hooks.
			hooked := &PublishResult{
				Verdict:     v,
				Confidence:  conf,
				Reasoning:   reasoning,
				SpecID:      specID,
				ArtifactID:  artifactID,
				DriftID:     pendingDriftID,
				ActiveAgentID: activeAgentID,
			}
			if in.Artifact.AutoSaveDecision {
				o.autoSaveDecisionOnAligned(bgCtx, wc, specID, artifactID, in, hooked, activeAgentID)
			}
			o.autoArchiveSpecTodosOnAligned(bgCtx, wc, specID, hooked)
		}
	}()
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
//
// v2.13.0 fix (OPITA-006): parseDriftVerdict was EVAL_TYPE-BLIND. The
// drift_judge/brand_match/compliance_check/mindset_quality shapes use a
// `verdict` key with TYPE-SPECIFIC values ("match", "compliant", ...),
// while grounding_check/pii_detect/prompt_injection_scan emit boolean
// keys ("grounded", "pii_found", "injection_found"). The old parser only
// understood the drift_judge value set, so every non-drift_judge verdict
// fell through to the "drift_detected" default — consensus(grounding_check)
// returned drift_detected even when the LLM said grounded:true. The fix
// delegates to parseVerdict with an explicit eval_type so each judge
// shape maps to the canonical three-state verdict.
func parseDriftVerdict(verdictJSON string, confidence float32) string {
	return parseVerdict("drift_judge", verdictJSON, confidence)
}

// parseVerdict maps an LLM Judge verdict JSON to one of the canonical
// drift verdicts — aligned | drift_detected | needs_human — given the
// eval_type that produced it. Each judge emits a different JSON shape
// (see defaultSystemForEval in llm_client.go); this function translates
// each shape's semantics into the canonical three-state verdict used by
// drift reports and consensus aggregation.
//
// Semantics per eval_type (what "aligned" means for each judge):
//
//	drift_judge            {"verdict":"aligned"|"drift_detected"|"needs_human"}  → verbatim
//	brand_match            {"verdict":"match"|"drift_detected"}                  → match=aligned
//	compliance_check       {"verdict":"compliant"|"non_compliant"}               → compliant=aligned
//	mindset_quality        {"verdict":"aligned"|"drift_detected"|"needs_human"}  → verbatim
//	grounding_check        {"grounded":true|false}                               → true=aligned
//	pii_detect             {"pii_found":true|false}                              → false=aligned
//	prompt_injection_scan  {"injection_found":true|false}                        → false=aligned
//	spec_test_alignment    {"alignment":0.0-1.0}                                 → >=0.7=aligned (M6)
//	mutation_score_check   {"pass":true|false}                                   → true=aligned (M1)
//	security_coverage      {"coverage":0.0-1.0}                                  → >=0.8=aligned (M7)
//	resilience_check       {"passed":true|false}                                 → true=aligned (M8)
//	test_quality_review    {"verdict":"aligned"|"drift_detected"|"needs_human"}  → verbatim
//	oracle_quality         {"verdict":"aligned"|"drift_detected"|"needs_human"}  → verbatim
//
// confidence < 0.5 always returns "needs_human" regardless of the LLM's
// verdict — that's the floor at which we trust the LLM-as-judge.
//
// stripCodeFence removes a ```json ``` (or ```yaml / ```markdown)
// wrapper from the judge's raw output so the structured verdict inside
// can be parsed as JSON. Returns the input unchanged when no fence is
// present. TD-J4 evolution (drift 1089): without this, the fallback
// scans the entire output including the judge's evidence quotes.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop the opening ```lang line.
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	// Drop the closing ``` (and any trailing newline before it).
	if i := strings.LastIndex(s, "```"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
func parseVerdict(evalType, verdictJSON string, confidence float32) string {
	if confidence < 0.5 {
		return "needs_human"
	}
	// Try to parse the JSON; check both shapes. TD-J4 evolution (v2,
	// drift 1089): MiniMax-M3 wraps its structured verdict in a
	// ```json fence. Unmarshalling the raw text fails because of the
	// fence, and the lenient fallback then scans the WHOLE output —
	// including the judge's OWN evidence quotes, which can contain
	// the canonicalTokens list verbatim (with "verdict":"drift_detected"
	// tokens LATER than the judge's real top-level "verdict":"aligned").
	// Stripping the fence BEFORE Unmarshal lets the structured branch
	// trust the top-level verdict field; the text-scan fallback is
	// only reached when no structured verdict can be parsed at all.
	var v map[string]any
	if err := json.Unmarshal([]byte(stripCodeFence(verdictJSON)), &v); err == nil {
		// eval_type-specific boolean shapes (non-verdict judges).
		switch evalType {
		case "grounding_check":
			if grounded, ok := v["grounded"].(bool); ok {
				if grounded {
					return "aligned"
				}
				return "drift_detected"
			}
		case "pii_detect":
			if found, ok := v["pii_found"].(bool); ok {
				if found {
					// PII present = the content fails the check.
					return "drift_detected"
				}
				return "aligned"
			}
		case "prompt_injection_scan":
			if found, ok := v["injection_found"].(bool); ok {
				if found {
					return "drift_detected"
				}
				return "aligned"
			}
		case "brand_match":
			// brand_match emits {"verdict":"match"|"drift_detected"}.
			if verdict, ok := v["verdict"].(string); ok {
				switch verdict {
				case "match":
					return "aligned"
				case "drift_detected":
					return "drift_detected"
				}
			}
		case "compliance_check":
			// compliance_check emits {"verdict":"compliant"|"non_compliant"}.
			if verdict, ok := v["verdict"].(string); ok {
				switch verdict {
				case "compliant":
					return "aligned"
				case "non_compliant":
					return "drift_detected"
				}
			}
		case "spec_test_alignment":
			// M6 — alignment = tests_verifying_spec_claims / spec_claims.
			// >= 0.7 passes (target 1.0 for published artifacts; the
			// judge reports missing claims so a 0.85 with one missing
			// claim surfaces as drift, not aligned).
			if a, ok := numericBool(v, "alignment"); ok {
				if a >= 0.7 {
					return "aligned"
				}
				return "drift_detected"
			}
		case "mutation_score_check":
			// M1 — mutation_score = mutants_killed / total_mutants.
			// The judge emits "pass" (score >= threshold). pass=true → aligned.
			if pass, ok := v["pass"].(bool); ok {
				if pass {
					return "aligned"
				}
				return "drift_detected"
			}
		case "security_coverage":
			// M7 — security_coverage = owasp_vectors_with_tests / total.
			// >= 0.8 passes (target 1.0 for all applicable vectors).
			if c, ok := numericBool(v, "coverage"); ok {
				if c >= 0.8 {
					return "aligned"
				}
				return "drift_detected"
			}
		case "resilience_check":
			// M8 — resilience_score = chaos_experiments_passed / run.
			// passed=true → aligned (target >= 0.90).
			if passed, ok := v["passed"].(bool); ok {
				if passed {
					return "aligned"
				}
				return "drift_detected"
			}
		}
		// Generic `verdict` string shape (drift_judge, mindset_quality,
		// and anything else that speaks the canonical values).
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
	// Markdown backtick form (`verdict:`needs_human``) — strip
	// backticks and `**` bold markers BEFORE the colon-collapse so the
	// `: ` (colon+space) replacement can bind `verdict: aligned`.
	// Otherwise `verdict:` `aligned`` has a backtick between the colon
	// and the value and the `: ` pattern never matches.
	b2 := strings.ReplaceAll(b.String(), "`", "")
	b2 = strings.ReplaceAll(b2, "**", "")
	compact := strings.ReplaceAll(b2, `: `, `:`)
	compact = strings.ReplaceAll(compact, ` :`, `:`)
	// TD-J4 P0 fix: LAST-OCCURRENCE semantics instead of the old
	// Contains-ordered switch. The judge's final verdict appears at
	// the END of its output; the artifact text it QUOTES in the
	// reasoning can contain the same tokens (e.g. an artifact that
	// says "Expected verdict: aligned" — the old switch matched the
	// quote BEFORE the judge's own conclusion and produced a FALSE
	// ALIGNED, bypassing the drift gate with injected text).
	// Matching the token with the greatest LastIndex means a quoted
	// artifact phrase can never win over the judge's conclusion.
	// No token found → fail-safe needs_human (parse/infra failure
	// must surface for operator review, never as silent approval).
	canonicalTokens := []struct {
		token   string
		verdict string
	}{
		{`"verdict":"aligned"`, "aligned"},
		{`verdict:"aligned"`, "aligned"},
		{`verdict:aligned`, "aligned"},
		{`"aligned":true`, "aligned"},
		{`"drift":false`, "aligned"},
		{`"verdict":"needs_human"`, "needs_human"},
		{`verdict:"needs_human"`, "needs_human"},
		{`verdict:needs_human`, "needs_human"},
		{`"verdict":"drift_detected"`, "drift_detected"},
		{`verdict:"drift_detected"`, "drift_detected"},
		{`verdict:drift_detected`, "drift_detected"},
	}
	bestVerdict := ""
	bestIdx := -1
	for _, c := range canonicalTokens {
		if idx := strings.LastIndex(compact, c.token); idx > bestIdx {
			bestIdx = idx
			bestVerdict = c.verdict
		}
	}
	if bestVerdict != "" {
		return bestVerdict
	}
	// v2.21.0 (spec 1200, P0 fix): fail-safe default is needs_human,
	// NOT drift_detected. An unparseable verdict (wrong format, empty
	// response, model change) means the judge could not form a
	// verdict — that is infrastructure/parse failure, which per
	// spec 757 T6 must surface as needs_human (operator reviews),
	// never as a spurious drift_detected.
	return "needs_human"
}

// numericBool reads a float field from a map[string]any (the shape
// json.Unmarshal produces). Accepts float64 (the JSON default),
// json.Number, and int (for tests constructing the map by hand).
// Returns ok=false when the field is absent or not numeric.
func numericBool(v map[string]any, key string) (float64, bool) {
	raw, ok := v[key]
	if !ok {
		return 0, false
	}
	switch n := raw.(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

// nextActionForVerdict maps a verdict to a NextAction string the
// calling agent can branch on.
func nextActionForVerdict(v string) string {
	switch v {
	case "aligned", "skipped":
		// aligned = drift_judge says spec satisfied → publish
		// skipped = AutoDriftCheck=false (no drift check ran) → publish
		// Both are "no drift detected" outcomes; the harness can proceed.
		return "publish"
	case "drift_detected":
		return "reconcile"
	default:
		// needs_human (no LLM, low confidence, or anything else) →
		// operator must decide.
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
