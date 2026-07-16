// Package tools — next.go: NextAction helpers.
//
// Maps a verdict (aligned | drift_detected | needs_human | skipped)
// to the canonical NextAction. Mirrors publish_vibe.go's
// nextActionForVerdict so the MCP layer and the orchestrator layer
// stay in sync.
package tools

// VerdictForNext is the verdict string used to look up a NextAction.
// Mirrors publish_vibe.go's parseDriftVerdict canonical set.
type VerdictForNext string

const (
	VerdictAligned      VerdictForNext = "aligned"
	VerdictDriftDetected VerdictForNext = "drift_detected"
	VerdictNeedsHuman   VerdictForNext = "needs_human"
	VerdictSkipped      VerdictForNext = "skipped"
)

// NextActionForVerdict maps a verdict to the canonical NextAction.
// Returns nil for empty/invalid verdict. The args map carries the
// minimal context the caller needs to follow the hint.
func NextActionForVerdict(verdict string, ctx map[string]any) *NextAction {
	switch VerdictForNext(verdict) {
	case VerdictAligned:
		return &NextAction{
			Tool:   "vibe_publish", // re-publish path; aligns suggest another artifact
			When:   NextActionAlways,
			Reason: "previous artifact aligned with spec; safe to publish more or close the pipeline",
		}
	case VerdictDriftDetected:
		args := map[string]any{}
		// Forward drift_id + artifact_id when caller has them so the
		// LLM doesn't have to re-look-up before calling resolve_drift.
		if v, ok := ctx["drift_id"]; ok {
			args["drift_id"] = v
		}
		if v, ok := ctx["artifact_id"]; ok {
			args["artifact_id"] = v
		}
		return &NextAction{
			Tool:   "resolve_drift",
			Args:   args,
			When:   NextActionOnDrift,
			Reason: "drift detected; operator must accept or reject the artifact",
		}
	case VerdictNeedsHuman:
		return &NextAction{
			Tool:   "active_policy",
			When:   NextActionOnHumanGate,
			Reason: "needs human review; surface the active policy so the operator understands the constitution in force",
		}
	case VerdictSkipped:
		return nil
	default:
		return nil
	}
}

// NextActionFromPublishResult builds a NextAction from a publish_vibe
// result shape (verdict + next_action + artifact_id + drift_id). This
// is the inverse of publish_vibe.go's verdict mapping: the MCP layer
// can directly forward the verdict-derived Next.
func NextActionFromPublishResult(verdict, nextAction string, artifactID, driftID int64) *NextAction {
	switch nextAction {
	case NextActionPublish:
		return &NextAction{
			Tool:   "vibe_publish",
			When:   NextActionAlways,
			Reason: "previous artifact accepted; pipeline may continue",
		}
	case NextActionReconcile:
		return &NextAction{
			Tool: "resolve_drift",
			Args: map[string]any{
				"drift_id":    driftID,
				"artifact_id": artifactID,
			},
			When:   NextActionOnDrift,
			Reason: "drift_detected verdict; operator must accept or reject",
		}
	case NextActionHumanGate:
		return &NextAction{
			Tool:   "active_policy",
			When:   NextActionOnHumanGate,
			Reason: "needs_human verdict; operator reviews via the active policy",
		}
	default:
		return nil
	}
}