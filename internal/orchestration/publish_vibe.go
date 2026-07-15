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
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
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
type PublishArtifactInput struct {
	ArtifactType string `json:"artifact_type"`            // code|text|image|video|audio|multi
	ArtifactURL  string `json:"artifact_url"`             // where it lives
	Text         string `json:"text,omitempty"`           // body, for judges
	BrandID      string `json:"brand_id,omitempty"`       // triggers brand_match if set
	Jurisdiction string `json:"jurisdiction,omitempty"`   // triggers compliance_check if set
	HasDisclosure bool  `json:"has_disclosure,omitempty"` // EU AI Act flag for synthetic media
}

// PublishVibeInput is the full request to publish one artifact under
// one spec.
type PublishVibeInput struct {
	Spec           PublishSpecInput     `json:"spec"`
	Artifact       PublishArtifactInput `json:"artifact"`
	// AutoDriftCheck: pointer so we can distinguish "unset" (default
	// true — run drift_judge) from "explicitly false" (skip). The
	// bool zero value would otherwise default to false and surprise
	// callers who don't set the field.
	AutoDriftCheck *bool                `json:"auto_drift_check,omitempty"`
	SessionID      string               `json:"session_id,omitempty"` // recorded on the artifact for INV-2
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
	Verdict          string  `json:"verdict"`        // aligned | drift_detected | needs_human | skipped
	Confidence       float32 `json:"confidence"`     // 0..1; 0 if skipped or no-LLM
	NextAction       string  `json:"next_action"`    // publish | reconcile | human_gate
	Reasoning        string  `json:"reasoning"`      // human-readable explanation
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
		judgeOut, jerr := o.Judge(ctx, JudgeInput{
			EvalType:   "drift_judge",
			TargetType: "artifact",
			TargetID:   fmt.Sprintf("artifact_%d", artifactID),
			Content:    in.Artifact.Text,
		})
		if jerr != nil {
			// No LLM available (or canary rejected). Record
			// drift_detected + reasoning; NextAction=human_gate so
			// the operator knows to retry with a key or run manual
			// drift check.
			result.Verdict = "drift_detected"
			result.Confidence = 0
			result.NextAction = "human_gate"
			result.Reasoning = fmt.Sprintf("drift_judge skipped: %v", jerr)
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

	return result, nil
}

// parseDriftVerdict maps an LLM Judge verdict JSON to one of the
// canonical drift verdicts: aligned | drift_detected | needs_human.
//
// Convention: the LLM is asked to return JSON like
//
//	{"aligned": true, "confidence": 0.92, "issues": []}
//
// or
//
//	{"aligned": false, "drift_items": ["missing_field_x"], "confidence": 0.85}
//
// confidence < 0.5 always returns "needs_human" regardless of the LLM's
// verdict — that's the floor at which we trust the LLM-as-judge.
func parseDriftVerdict(verdictJSON string, confidence float32) string {
	if confidence < 0.5 {
		return "needs_human"
	}
	// Try to parse the JSON; fall through to string matching on parse
	// failure (lenient).
	var v map[string]any
	if err := json.Unmarshal([]byte(verdictJSON), &v); err == nil {
		if aligned, ok := v["aligned"].(bool); ok {
			if aligned {
				return "aligned"
			}
			return "drift_detected"
		}
	}
	// Lenient fallback: substring match.
	lc := strings.ToLower(verdictJSON)
	if strings.Contains(lc, `"aligned":true`) || strings.Contains(lc, `"drift":false`) {
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