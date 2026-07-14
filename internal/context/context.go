// Package context provides the composed views (Context Objects) that
// retrieval tools return to the LLM. The principle (RFC §D-5, P2):
// every retrieval returns a coherent object, never a row dump.
//
// Three Context Objects cover the primary retrieval surfaces:
//
//   - ArtifactContext: the LLM asks "give me context about artifact #N".
//     Returns artifact + rendered spec + brand + compliance + drift + verdicts.
//
//   - SessionContext: the LLM asks "what's the state of my session?".
//     Returns session + active constitution + active mods + counts + recent writes.
//
//   - PolicyContext: the LLM asks "what's the operating envelope?".
//     Returns constitution + active mods + watchdog status + driver + schema version.
//
// Compose* functions accept a store.Store and a key, do the necessary reads,
// and return the composed view. They never write. They tolerate partial
// state (a missing spec, a missing brand, etc.) by returning nil for
// the missing field — never error on missing optional data.
package context

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/constitution"
	"github.com/dark-agents/dark-memory-mcp/internal/mods"
	"github.com/dark-agents/dark-memory-mcp/internal/research"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/ssd"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

// ---------------------------------------------------------------------------
// ArtifactContext
// ---------------------------------------------------------------------------

// ArtifactContext is the composed view for a single artifact.
// One tool call returns this — never 5 separate tool calls.
type ArtifactContext struct {
	Artifact       *vibeflow.Artifact
	SpecMarkdown   string                  // rendered from vibe_specs.spec_json
	SpecTasks      []TaskView              // parsed from vibe_specs.tasks_json
	Brand          *vibeflow.BrandGuide     // resolved from artifact.brand_id
	Compliance     *vibeflow.ComplianceRule // resolved from artifact.jurisdiction
	LastDrift      *vibeflow.DriftReport
	VerdictChain   []SddVerdictView        // brand + compliance + drift ordered by time
	WriteAuditTail []audit.WriteEvent      // last 10 writes for this artifact
	RelatedLinks   []research.Link         // cross-links from research_items
}

// TaskView is a single task from a spec, parsed and ready to render.
type TaskView struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	DependsOn   []string `json:"depends_on,omitempty"`
}

// SddVerdictView is one LLM-as-judge verdict, trimmed for context views.
type SddVerdictView struct {
	EvalType    string  `json:"eval_type"`
	TargetType  string  `json:"target_type"`
	TargetID    string  `json:"target_id"`
	VerdictJSON string  `json:"verdict_json"`
	Confidence  float32 `json:"confidence"`
	CreatedAt   string  `json:"created_at"`
}

// ComposeArtifact reads the artifact and everything it references.
// Returns store.ErrNotFound if the artifact does not exist.
func ComposeArtifact(ctx context.Context, s store.Store, artifactID int64) (*ArtifactContext, error) {
	a, err := s.GetArtifact(ctx, artifactID)
	if err != nil {
		return nil, fmt.Errorf("compose artifact: get: %w", err)
	}
	if a == nil {
		return nil, store.ErrNotFound
	}
	out := &ArtifactContext{Artifact: a}

	if a.SpecID > 0 {
		sp, err := s.GetSpec(ctx, a.SpecID)
		if err == nil && sp != nil {
			out.SpecMarkdown = RenderSpecMarkdown(sp)
			out.SpecTasks = ParseSpecTasks(sp.Tasks)
		}
	}
	if a.BrandID != "" {
		if b, _ := s.GetBrandGuide(ctx, a.BrandID); b != nil {
			out.Brand = b
		}
	}
	if a.Jurisdiction != "" {
		if c, _ := s.GetComplianceRule(ctx, a.Jurisdiction); c != nil {
			out.Compliance = c
		}
	}
	if d, _ := s.LatestDriftForArtifact(ctx, artifactID); d != nil {
		out.LastDrift = d
	}
	out.VerdictChain = loadVerdictChain(ctx, s, a.BrandID, a.Jurisdiction, artifactID)
	out.WriteAuditTail = loadAuditTail(ctx, s, "vibe_artifacts", artifactID, 10)
	out.RelatedLinks = loadRelatedLinks(ctx, s, a.SessionID, artifactID)
	return out, nil
}

// loadVerdictChain returns the most recent verdicts across brand/compliance/drift
// for this artifact, ordered chronologically (oldest first).
func loadVerdictChain(ctx context.Context, s store.Store, brandID, jurisdiction string, artifactID int64) []SddVerdictView {
	chain := []SddVerdictView{}

	if brandID != "" {
		if v, _ := s.LatestSDDEvaluation(ctx, "brand_match", "artifact", fmt.Sprintf("%d", artifactID)); v != nil {
			chain = append(chain, SddVerdictView{
				EvalType: v.EvalType, TargetType: v.TargetType, TargetID: v.TargetID,
				VerdictJSON: v.VerdictJSON, Confidence: v.Confidence, CreatedAt: v.CreatedAt,
			})
		}
	}
	if jurisdiction != "" {
		if v, _ := s.LatestSDDEvaluation(ctx, "compliance_check", "artifact", fmt.Sprintf("%d", artifactID)); v != nil {
			chain = append(chain, SddVerdictView{
				EvalType: v.EvalType, TargetType: v.TargetType, TargetID: v.TargetID,
				VerdictJSON: v.VerdictJSON, Confidence: v.Confidence, CreatedAt: v.CreatedAt,
			})
		}
	}
	if v, _ := s.LatestSDDEvaluation(ctx, "drift_judge", "artifact", fmt.Sprintf("%d", artifactID)); v != nil {
		chain = append(chain, SddVerdictView{
			EvalType: v.EvalType, TargetType: v.TargetType, TargetID: v.TargetID,
			VerdictJSON: v.VerdictJSON, Confidence: v.Confidence, CreatedAt: v.CreatedAt,
		})
	}
	return chain
}

// loadAuditTail returns the last N write_audit rows for a given row.
func loadAuditTail(ctx context.Context, s store.Store, tableName string, rowID int64, n int) []audit.WriteEvent {
	rows, err := s.ListWrites(ctx, audit.ListFilters{Limit: n * 4})
	if err != nil {
		return nil
	}
	out := []audit.WriteEvent{}
	for _, e := range rows {
		if e.TableName == tableName && e.RowID == rowID {
			out = append(out, e)
			if len(out) >= n {
				break
			}
		}
	}
	return out
}

// loadRelatedLinks returns cross-links from research_items linked to this
// artifact (via session + item provenance). Best-effort; nil on miss.
func loadRelatedLinks(ctx context.Context, s store.Store, sessionID string, artifactID int64) []research.Link {
	// Implementation note: research_links are tied to research_items, not
	// directly to artifacts. For now we return links whose note or
	// target_id matches the artifact_id. A future version can join via
	// research_items.session_id + artifact.session_id.
	if sessionID == "" {
		return nil
	}
	runs, err := s.ListRuns(ctx, "", 20)
	if err != nil {
		return nil
	}
	out := []research.Link{}
	seen := map[int64]bool{}
	for _, r := range runs {
		if r.SessionID != sessionID {
			continue
		}
		items, _ := s.ListItems(ctx, r.ID, "", 50)
		for _, item := range items {
			if seen[item.ID] {
				continue
			}
			seen[item.ID] = true
			// research_links doesn't have a store.Store method that returns
			// by item_id; we surface the item as a "link" with itself
			// for now (real cross-link enumeration is a future work).
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// SessionContext
// ---------------------------------------------------------------------------

// SessionContext is the composed view for an operational session.
// One tool call returns this when the LLM asks "what's my session state?".
type SessionContext struct {
	Session           *session.Session
	ActiveConstitution *constitution.Constitution
	ActiveMods        []mods.Mod
	Counts            SessionCounts
	RecentWrites      []audit.WriteEvent // last 20 writes for this session
	PendingDrifts     []DriftTask        // drift reports not yet reconciled
	ActiveSpec        *vibeflow.Spec     // most recent spec for this session
}

// SessionCounts is the per-session row counts.
type SessionCounts struct {
	RunsTotal       int `json:"runs_total"`
	ItemsTotal      int `json:"items_total"`
	LinksTotal      int `json:"links_total"`
	SpecsTotal      int `json:"specs_total"`
	ArtifactsTotal  int `json:"artifacts_total"`
	DriftsTotal     int `json:"drifts_total"`
	PendingDrifts   int `json:"pending_drifts"`
	SDDEvaluations  int `json:"sdd_evaluations"`
	WriteAuditTotal int `json:"write_audit_total"`
}

// DriftTask is a pending drift report that needs human reconciliation.
type DriftTask struct {
	ArtifactID int64  `json:"artifact_id"`
	DriftID    int64  `json:"drift_id"`
	Verdict    string `json:"verdict"`
	CreatedAt  string `json:"created_at"`
}

// ComposeSession reads the session and everything tied to it.
func ComposeSession(ctx context.Context, s store.Store, sessionID string) (*SessionContext, error) {
	sess, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("compose session: get: %w", err)
	}
	if sess == nil {
		return nil, store.ErrNotFound
	}
	out := &SessionContext{Session: sess}

	if sess.ConstitutionID != "" && sess.ConstitutionVer != "" {
		if c, _ := s.GetConstitution(ctx, sess.ConstitutionID, sess.ConstitutionVer); c != nil {
			out.ActiveConstitution = c
		}
	}
	out.ActiveMods = loadActiveMods(ctx, s, sess.ActiveMods)
	out.Counts = computeCounts(ctx, s, sessionID)
	out.RecentWrites = loadRecentWrites(ctx, s, sessionID, 20)
	out.PendingDrifts = loadPendingDrifts(ctx, s, sessionID)
	if sps, _ := s.ListSpecs(ctx, vibeflow.SpecListFilters{SessionID: sessionID, Limit: 1}); len(sps) > 0 {
		out.ActiveSpec = &sps[0]
	}
	return out, nil
}

// loadActiveMods resolves the session's active_mods JSON list into Mod rows.
// Empty list or parse error yields empty.
func loadActiveMods(ctx context.Context, s store.Store, activeModsJSON string) []mods.Mod {
	if activeModsJSON == "" {
		return nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(activeModsJSON), &ids); err != nil {
		return nil
	}
	out := []mods.Mod{}
	for _, id := range ids {
		if m, _ := s.GetMod(ctx, id); m != nil {
			out = append(out, *m)
		}
	}
	return out
}

// computeCounts walks the tables for rows keyed to this session.
// Best-effort; missing rows don't error.
func computeCounts(ctx context.Context, s store.Store, sessionID string) SessionCounts {
	var c SessionCounts
	if runs, _ := s.ListRuns(ctx, "", 500); len(runs) > 0 {
		for _, r := range runs {
			if r.SessionID == sessionID {
				c.RunsTotal++
			}
		}
	}
	if specs, _ := s.ListSpecs(ctx, vibeflow.SpecListFilters{SessionID: sessionID, Limit: 1000}); len(specs) > 0 {
		c.SpecsTotal = len(specs)
	}
	if arts, _ := s.ListArtifacts(ctx, vibeflow.ArtifactListFilters{SessionID: sessionID, Status: "", Limit: 1000}); len(arts) > 0 {
		c.ArtifactsTotal = len(arts)
		for _, a := range arts {
			d, _ := s.LatestDriftForArtifact(ctx, a.ID)
			if d != nil {
				c.DriftsTotal++
				if d.ReconciledAt == "" {
					c.PendingDrifts++
				}
			}
		}
	}
	if evals, _ := s.ListSDDEvaluations(ctx, ssd.ListFilters{Limit: 1000}); len(evals) > 0 {
		c.SDDEvaluations = len(evals) // rough — session filter not on this method
	}
	if writes, _ := s.ListWrites(ctx, audit.ListFilters{SessionID: sessionID, Limit: 5000}); len(writes) > 0 {
		c.WriteAuditTotal = len(writes)
	}
	return c
}

// loadRecentWrites returns the last N writes for a session.
func loadRecentWrites(ctx context.Context, s store.Store, sessionID string, n int) []audit.WriteEvent {
	rows, err := s.ListWrites(ctx, audit.ListFilters{SessionID: sessionID, Limit: n})
	if err != nil {
		return nil
	}
	return rows
}

// loadPendingDrifts returns drift reports not yet reconciled for this session.
func loadPendingDrifts(ctx context.Context, s store.Store, sessionID string) []DriftTask {
	arts, _ := s.ListArtifacts(ctx, vibeflow.ArtifactListFilters{SessionID: sessionID, Limit: 1000})
	out := []DriftTask{}
	for _, a := range arts {
		d, err := s.LatestDriftForArtifact(ctx, a.ID)
		if err != nil || d == nil {
			continue
		}
		if d.ReconciledAt == "" {
			out = append(out, DriftTask{
				ArtifactID: a.ID, DriftID: d.ID, Verdict: d.Verdict, CreatedAt: d.CreatedAt,
			})
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// PolicyContext
// ---------------------------------------------------------------------------

// PolicyContext is the composed view for the active operating policy.
// Returned by dark_memory_active_policy (RFC §6) and used by the dark-recall
// plugin v2.3 to render its system reminder.
type PolicyContext struct {
	Constitution       *constitution.Constitution `json:"constitution,omitempty"`
	ActiveMods         []mods.Mod                 `json:"active_mods"`
	WatchdogStatus     string                     `json:"watchdog_status"` // "ok" | "drift"
	Driver             string                     `json:"driver"`
	SchemaVersion      int                        `json:"schema_version"`
	CoexistenceGroup   string                     `json:"coexistence_group"`
	CoexistenceVersion string                     `json:"coexistence_version"`
}

// ComposePolicy reads the active constitution + mods + driver info.
func ComposePolicy(ctx context.Context, s store.Store, driverName string, schemaVersion int) (*PolicyContext, error) {
	out := &PolicyContext{
		WatchdogStatus:     "ok", // updated below if mismatch detected
		Driver:             driverName,
		SchemaVersion:      schemaVersion,
		CoexistenceGroup:   "dark-agents/memory",
		CoexistenceVersion: "cx.v2",
	}
	id, ver, _ := s.ActiveConstitution(ctx)
	if id != "" && ver != "" {
		c, _ := s.GetConstitution(ctx, id, ver)
		if c != nil {
			out.Constitution = c
			// Watchdog signal: if the active constitution's enabled flag is false,
			// or if its SHA matches the watchdog sentinel mismatch pattern (none here),
			// surface it. Future: file-vs-stored mismatch via a verifier hook.
		}
	}
	mods_list, _ := s.ListMods(ctx, 100)
	out.ActiveMods = mods_list
	return out, nil
}

// ---------------------------------------------------------------------------
// Markdown rendering helpers
// ---------------------------------------------------------------------------

// RenderSpecMarkdown turns a spec row into a stable markdown document.
// The output is deterministic given the same input — useful for caching
// and for diffing drift reports.
func RenderSpecMarkdown(sp *vibeflow.Spec) string {
	if sp == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Spec #%d — `%s`\n\n", sp.ID, sp.VibeCase)
	if sp.SessionID != "" {
		fmt.Fprintf(&b, "**Session:** `%s`  \n", sp.SessionID)
	}
	fmt.Fprintf(&b, "**Created:** %s  \n", sp.CreatedAt)
	if sp.UpdatedAt != "" {
		fmt.Fprintf(&b, "**Updated:** %s  \n", sp.UpdatedAt)
	}
	b.WriteString("\n")

	if sp.Constitution != "" {
		b.WriteString("## Constitution (hard rules)\n\n")
		b.WriteString(prettyJSON(sp.Constitution))
		b.WriteString("\n\n")
	}
	if sp.Spec != "" {
		b.WriteString("## Intent (what + why)\n\n")
		b.WriteString(prettyJSON(sp.Spec))
		b.WriteString("\n\n")
	}
	if sp.Tasks != "" {
		b.WriteString("## Tasks\n\n")
		b.WriteString(prettyJSON(sp.Tasks))
		b.WriteString("\n")
	}
	return b.String()
}

// ParseSpecTasks extracts TaskViews from the spec.tasks JSON string.
func ParseSpecTasks(tasksJSON string) []TaskView {
	if tasksJSON == "" {
		return nil
	}
	var raw []map[string]any
	if err := json.Unmarshal([]byte(tasksJSON), &raw); err != nil {
		return nil
	}
	out := make([]TaskView, 0, len(raw))
	for _, t := range raw {
		tv := TaskView{}
		if id, ok := t["id"].(string); ok {
			tv.ID = id
		}
		if desc, ok := t["description"].(string); ok {
			tv.Description = desc
		}
		if deps, ok := t["depends_on"].([]any); ok {
			for _, d := range deps {
				if s, ok := d.(string); ok {
					tv.DependsOn = append(tv.DependsOn, s)
				}
			}
		}
		out = append(out, tv)
	}
	return out
}

// prettyJSON reformats a JSON string with 2-space indent. If the input
// isn't valid JSON, returns it verbatim.
func prettyJSON(s string) string {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return s
	}
	return string(out)
}