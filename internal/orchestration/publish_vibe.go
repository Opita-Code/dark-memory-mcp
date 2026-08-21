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
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/artifact"
	"github.com/dark-agents/dark-memory-mcp/internal/errorobs"
	"github.com/dark-agents/dark-memory-mcp/internal/judgeparse"
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
// is the body of the artifact (used for brand_match +
// compliance_check). URL is the canonical location.
//
// v2.20.0 T08 (spec 1276): ArtifactRef (optional) anchors the drift_judge
// pipeline to the resolved artifact body. When set, the drift_judge
// uses artifact.Resolver to read the artifact (file/git_sha/url/
// spec_id/artifact_id) and the NLI Provider scores the resolved bytes
// against the spec intent. When nil, the legacy Content path is used
// for drift_judge (Phase 1 deprecation — v2.22.0 removes the Content
// path entirely; see spec 1276 H1).
//
// v2.8.0-alpha A1: AutoSaveDecision (default true when
// DARK_MEMORY_V280=1) triggers a kind=decision agent_memory row when
// drift_judge verdict=aligned. Set false to suppress per-call.
type PublishArtifactInput struct {
	ArtifactType  string `json:"artifact_type"`            // code|text|image|video|audio|multi
	ArtifactURL   string `json:"artifact_url"`             // where it lives
	Text          string `json:"text,omitempty"`           // body, for brand_match + compliance_check + legacy drift_judge
	BrandID       string `json:"brand_id,omitempty"`       // triggers brand_match if set
	Jurisdiction  string `json:"jurisdiction,omitempty"`   // triggers compliance_check if set
	HasDisclosure bool   `json:"has_disclosure,omitempty"` // EU AI Act flag for synthetic media
	// ArtifactRef (v2.20.0 T08, spec 1276) is the artifact-anchored
	// reference for the drift_judge pipeline. When non-nil, drift_judge
	// resolves this ref via artifact.Resolver and scores the resolved
	// bytes against the spec intent. Caller CANNOT influence the
	// verdict by submitting arbitrary text — the artifact is what
	// the NLI model sees.
	//
	// Phase 1 (v2.20.0): ArtifactRef is optional. When nil, drift_judge
	// falls back to the legacy Content path (with a deprecation log).
	// Phase 2 (v2.22.0): ArtifactRef is required for drift_judge.
	ArtifactRef *artifact.ArtifactRef `json:"artifact_ref,omitempty"`
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
	AutoDriftCheck *bool `json:"auto_drift_check,omitempty"`
	// AsyncDriftCheck (v2.14.0, spec 998 p1): when true, drift_judge
	// (and brand_match + compliance_check) run in a BACKGROUND goroutine
	// and PublishVibe returns immediately with verdict="pending" +
	// next_action="poll". The operator polls pipeline_status for the
	// final verdict. This fixes the sync-path UX where the MCP call
	// blocked 10-30s+ on the LLM judge (the root reason we raised the
	// opencode MCP timeout to 120s). Default false = legacy synchronous
	// behavior (backward compatible).
	AsyncDriftCheck bool   `json:"async_drift_check,omitempty"`
	SessionID       string `json:"session_id,omitempty"` // recorded on the artifact for INV-2
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
//
// v2.20.0 T11 (spec 1276): the 3 callsites that consume caller-
// controlled Content (brand_match, compliance_check, drift_judge
// legacy) are routed through materializeForPublish BEFORE the LLM
// judge runs. The Materialize shim writes the text to a content-
// addressed file (SHA-256 anchored) so the audit trail records WHICH
// bytes the LLM-judge saw. The LLM-judge still receives Content
// (Phase 1 backward compat); at v2.22.0 Content is removed and only
// ArtifactRef is accepted.
func (o *Orchestrator) runJudgePipeline(
	ctx context.Context,
	wc store.WriteContext,
	in PublishVibeInput,
	specID, artifactID int64,
	activeAgentID string,
	autoCheck bool,
) (verdict string, confidence float32, reasoning string, brandEvalID, compEvalID int64) {
	reasoning = "drift check pending"

	// T11 (spec 1276): shared sourceTag for the 3 callsites. The
	// caller-supplied text is SHA-256-anchored via Materialize; the
	// sourceTag identifies the artifact in the audit trail.
	sourceTag := fmt.Sprintf("publish_vibe_artifact_%d", artifactID)

	// Optional brand_match.
	if in.Artifact.BrandID != "" && in.Artifact.Text != "" {
		// T11: route caller-controlled Text through Materialize. The
		// LLM-judge still receives Content (Phase 1) — the
		// Materialized ArtifactRef is the audit-trail pointer.
		brandRef, matErr := o.materializeForPublish(ctx, in.Artifact.Text, sourceTag)
		if matErr != nil {
			// Materialize failure → the judge cannot proceed because
			// the audit trail would be missing. Mark as needs_human.
			o.RecordError(ctx, "publish_vibe", in.SessionID,
				fmt.Errorf("brand_match: materialize: %w", matErr), errorobs.SeverityWarn)
			reasoning = fmt.Sprintf("brand_match materialize failed: %v", matErr)
		} else {
			_ = brandRef // anchored artifact; the LLM-judge still sees Content
			o.RecordError(ctx, "publish_vibe", in.SessionID,
				fmt.Errorf("brand_match via Text is deprecated (spec 1276 T11); text material anchored to %s. v2.22.0 requires ArtifactRef.", brandRef.Path),
				errorobs.SeverityWarn)
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
	}

	// Optional compliance_check.
	if in.Artifact.Jurisdiction != "" && in.Artifact.Text != "" {
		// T11: same Materialize routing as brand_match above.
		compRef, matErr := o.materializeForPublish(ctx, in.Artifact.Text, sourceTag)
		if matErr != nil {
			o.RecordError(ctx, "publish_vibe", in.SessionID,
				fmt.Errorf("compliance_check: materialize: %w", matErr), errorobs.SeverityWarn)
			reasoning = reasoning + "; compliance_check materialize failed: " + matErr.Error()
		} else {
			_ = compRef
			o.RecordError(ctx, "publish_vibe", in.SessionID,
				fmt.Errorf("compliance_check via Text is deprecated (spec 1276 T11); text material anchored to %s. v2.22.0 requires ArtifactRef.", compRef.Path),
				errorobs.SeverityWarn)
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
	}

	// Canonical drift_judge.
	if !autoCheck {
		return "skipped", 0, "auto_drift_check=false; operator reviews manually", brandEvalID, compEvalID
	}
	// v2.20.0 T08 (spec 1276): artifact-anchored drift_judge pipeline.
	// When ArtifactRef is supplied, DriftJudge resolves the artifact
	// via artifact.Resolver and scores the resolved bytes against the
	// spec intent through an NLI Provider. The judge CANNOT evaluate
	// caller-controlled text.
	if in.Artifact.ArtifactRef != nil {
		driftOut, derr := o.DriftJudge(ctx, DriftJudgeInput{
			ArtifactRef: *in.Artifact.ArtifactRef,
			SpecIntent:  in.Spec.Spec,
			EvalType:    "drift_judge",
			TargetType:  "artifact",
			TargetID:    fmt.Sprintf("artifact_%d", artifactID),
			VibeCase:    in.Spec.VibeCase,
			AgentID:     activeAgentID,
		})
		if derr != nil {
			// DriftJudge surfaces only hard errors (art_ref missing,
			// canary, infra). All other error classes (resolve, score)
			// are mapped to verdicts INSIDE DriftJudge.
			return "needs_human", 0, fmt.Sprintf("drift_judge unavailable: %v", derr), brandEvalID, compEvalID
		}
		// drift_judge was not the LLM-judge path; persistence is
		// handled by the publish_vibe caller (SaveSDDEvaluation +
		// SaveDriftReport). For now we return the drift verdict
		// directly without persisting an SDDEvaluation row (the
		// drift_judge row IS the audit; SDDEvaluation is for the
		// legacy LLM-judge path).
		_ = driftOut.VerdictJSON // surfaced via SaveDriftReport.JudgeReasoning below
		reasoning := "drift_judge (artifact-anchored) " + driftOut.Reasoning
		return driftOut.Verdict, driftOut.Confidence, reasoning, brandEvalID, compEvalID
	}
	// v2.20.0 T08 Phase 1: legacy Content path. Caller did not supply
	// ArtifactRef. We log a deprecation warning + still run (backward
	// compat). The legacy path uses LLM-as-judge with Content +
	// agent_memory enrichment. At v2.22.0 this branch is removed
	// (spec 1276 H1).
	if in.Artifact.Text == "" {
		return "skipped", 0, "no artifact text and no artifact_ref; drift_judge requires one of them", brandEvalID, compEvalID
	}
	// T11 (spec 1276): route the enriched text through Materialize.
	// The legacy Content path still runs (Phase 1 backward compat)
	// but the audit trail records the SHA-256 materialized file.
	driftRef, matErr := o.materializeForPublish(ctx, in.Artifact.Text, sourceTag)
	if matErr != nil {
		o.RecordError(ctx, "publish_vibe", in.SessionID,
			fmt.Errorf("drift_judge: materialize: %w", matErr), errorobs.SeverityError)
		return "needs_human", 0, fmt.Sprintf("drift_judge materialize failed: %v", matErr), brandEvalID, compEvalID
	}
	_ = driftRef
	o.RecordError(ctx, "publish_vibe", in.SessionID,
		fmt.Errorf("drift_judge via Content is deprecated (spec 1276 H1); text material anchored to %s. v2.22.0 removes the Content path; supply ArtifactRef.", driftRef.Path),
		errorobs.SeverityWarn)
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
	return v, judgeOut.Confidence, "drift_judge (legacy Content path, deprecated) " + judgeOut.VerdictJSON, brandEvalID, compEvalID
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
				Verdict:       v,
				Confidence:    conf,
				Reasoning:     reasoning,
				SpecID:        specID,
				ArtifactID:    artifactID,
				DriftID:       pendingDriftID,
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

// parseDriftVerdict maps a drift_judge verdict blob to the canonical
// three-state verdict. t3 (spec 1242): delegates to the shared
// judgeparse package — the full eval_type-aware logic, stripCodeFence,
// and TD-J4 last-occurrence scan live there (were inline here).
func parseDriftVerdict(verdictJSON string, confidence float32) string {
	return judgeparse.ParsePipeline("drift_judge", verdictJSON, confidence)
}

// parseVerdict maps an LLM Judge verdict JSON to one of the canonical
// drift verdicts — aligned | drift_detected | needs_human — given the
// eval_type that produced it. Each judge emits a different JSON shape
// (see defaultSystemForEval in llm_client.go); this function translates
// each shape's semantics into the canonical three-state verdict used by
// drift reports and consensus aggregation.
//
// t3 (spec 1242): delegates to judgeparse.ParsePipeline — the single
// canonical verdict parser shared by the vibe pipeline, the M6 drift
// gate, and judgment_history. Semantic history preserved: eval_type
// shapes, confidence floor 0.5 → needs_human, stripCodeFence before
// structured Unmarshal (drift 1089), TD-J4 last-occurrence scan, and
// the v2.21.0 fail-safe default (needs_human — a parse/infra failure
// must surface for operator review, never as a silent drift_detected).
func parseVerdict(evalType, verdictJSON string, confidence float32) string {
	return judgeparse.ParsePipeline(evalType, verdictJSON, confidence)
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
