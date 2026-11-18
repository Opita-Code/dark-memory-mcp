package tools

// SPEC-194 (T7, T8): the PLAN tools.
//
// The tests exercise the real handlers against a real SQLite store,
// which is stronger than a mocked one: the plan's write, its budget
// counter and its write_audit row are all checked against the same
// database the production code uses.
//
// The load-bearing assertion is the write_audit one (T8): the human
// decision MUST leave a row, because that row is the whole point of
// the decision being a tool and not a gate call.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
)

// newPlanEnv opens a real store, a real orchestrator and a registry
// with the PLAN namespace registered.
func newPlanEnv(t *testing.T) (*Registry, store.Store, *orchestration.Orchestrator) {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(tmp, "plan_e2e.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	st, err := sqlite.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateProject(ctx, &project.Project{ProjectID: "default", DisplayName: "Default"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := st.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}

	orch := orchestration.New(st, nil)
	reg := NewRegistry()
	// The corpus of the ranker is the REGISTRY itself, so the tests
	// need a registry that looks like the real one: the agent-memory
	// namespace is registered here so that an intent about writing a
	// memory really finds agent_memory_save among the candidates.
	// With a registry of only the three plan tools, every such intent
	// would match nothing and the re-proposal test would be vacuous.
	RegisterAgentMemory(reg, orch, st)
	if err := RegisterPlan(reg, orch); err != nil {
		t.Fatalf("RegisterPlan: %v", err)
	}
	return reg, st, orch
}

// planSpec creates a spec to hang a plan on.
func planSpec(t *testing.T, st store.Store, orch *orchestration.Orchestrator, intent string) int64 {
	t.Helper()
	id, err := orch.VibeSpec(context.Background(), orchestration.VibeSpecInput{
		VibeCase:  "C2",
		SessionID: "sess-plan",
		Spec:      `{"intent":"` + intent + `"}`,
		Tasks:     json.RawMessage(`[{"id":"t1","description":"do the thing"}]`),
	})
	if err != nil {
		t.Fatalf("VibeSpec: %v", err)
	}
	return id.SpecID
}

func planCall(t *testing.T, reg *Registry, name string, in map[string]any) *ToolResponse {
	t.Helper()
	tool := reg.Get(name)
	if tool == nil {
		t.Fatalf("tool %q not registered", name)
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := tool.Handler(context.Background(), raw)
	if err != nil {
		t.Fatalf("%s handler: %v", name, err)
	}
	return resp
}

// planData unmarshals the data payload of a tool response.
func planData(t *testing.T, resp *ToolResponse, out any) {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("tool error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Data)
	if err != nil {
		t.Fatalf("marshal data: %v", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("unmarshal data: %v (raw: %s)", err, raw)
	}
}

func TestPlan_CreateProposesADraft(t *testing.T) {
	reg, st, orch := newPlanEnv(t)
	specID := planSpec(t, st, orch, "write the release notes")

	var created struct {
		SpecID     int64  `json:"spec_id"`
		Status     string `json:"status"`
		Tier       string `json:"tier"`
		Judged     bool   `json:"judged"`
		Candidates []struct {
			Tool        string  `json:"tool"`
			Weight      float64 `json:"weight"`
			ScoreSource string  `json:"score_source"`
		} `json:"candidates"`
		Budget struct {
			MaxToolCalls  int `json:"max_tool_calls"`
			MaxJudgeCalls int `json:"max_judge_calls"`
		} `json:"budget"`
	}
	planData(t, planCall(t, reg, "plan_create", map[string]any{
		"spec_id": specID,
		"intent":  "write the release notes and save them as a memory",
	}), &created)

	if created.SpecID != specID {
		t.Errorf("spec_id = %d, want %d", created.SpecID, specID)
	}
	// Creating a plan does NOT approve it.
	if created.Status != "draft" {
		t.Errorf("status = %q, want %q: creating a plan does not approve it", created.Status, "draft")
	}
	if created.Tier == "" {
		t.Error("tier is empty")
	}
	if created.Budget.MaxToolCalls <= 0 {
		t.Errorf("budget.max_tool_calls = %d, want > 0", created.Budget.MaxToolCalls)
	}
	if created.Budget.MaxJudgeCalls <= 0 {
		t.Errorf("budget.max_judge_calls = %d, want > 0", created.Budget.MaxJudgeCalls)
	}
	// The judge did not run in this test (no judge wired), and the
	// result says so rather than pretending it did.
	if created.Judged {
		t.Error("judged = true, but no judge is wired in this test")
	}
}

func TestPlan_GetReadsBackWhatWasCreated(t *testing.T) {
	reg, st, orch := newPlanEnv(t)
	specID := planSpec(t, st, orch, "read me back")

	planCall(t, reg, "plan_create", map[string]any{
		"spec_id": specID,
		"intent":  "save a memory about the plan",
	})

	var got struct {
		Found  bool   `json:"found"`
		Status string `json:"status"`
	}
	planData(t, planCall(t, reg, "plan_get", map[string]any{"spec_id": specID}), &got)
	if !got.Found {
		t.Fatal("found = false, but the plan was just created")
	}
	if got.Status != "draft" {
		t.Errorf("status = %q, want %q", got.Status, "draft")
	}
}

// A spec with NO plan is a normal state, not an error.
func TestPlan_GetOnASpecWithoutAPlan(t *testing.T) {
	reg, st, orch := newPlanEnv(t)
	specID := planSpec(t, st, orch, "no plan here")

	var got struct {
		Found bool `json:"found"`
	}
	planData(t, planCall(t, reg, "plan_get", map[string]any{"spec_id": specID}), &got)
	if got.Found {
		t.Error("found = true for a spec with no plan")
	}
}

func TestPlan_DecideExecuteApproves(t *testing.T) {
	reg, st, orch := newPlanEnv(t)
	specID := planSpec(t, st, orch, "approve me")

	planCall(t, reg, "plan_create", map[string]any{
		"spec_id": specID,
		"intent":  "save a memory",
	})

	var decided struct {
		Decision  string `json:"decision"`
		Status    string `json:"status"`
		DecidedBy string `json:"decided_by"`
	}
	planData(t, planCall(t, reg, "plan_decide", map[string]any{
		"spec_id":    specID,
		"decision":   "execute",
		"decided_by": "the-operator",
	}), &decided)

	if decided.Decision != "execute" {
		t.Errorf("decision = %q, want %q", decided.Decision, "execute")
	}
	if decided.Status != "approved" {
		t.Errorf("status = %q, want %q", decided.Status, "approved")
	}
	if decided.DecidedBy != "the-operator" {
		t.Errorf("decided_by = %q, want %q", decided.DecidedBy, "the-operator")
	}

	// And the status sticks.
	var got struct {
		Status string `json:"status"`
	}
	planData(t, planCall(t, reg, "plan_get", map[string]any{"spec_id": specID}), &got)
	if got.Status != "approved" {
		t.Errorf("status after the decision = %q, want %q", got.Status, "approved")
	}
}

// T8: the human decision leaves a row in write_audit. That row is the
// trail (WS-7) of the moment the kernel stopped proposing and started
// obeying a human.
func TestPlan_DecideLeavesAWriteAuditRow(t *testing.T) {
	reg, st, orch := newPlanEnv(t)
	specID := planSpec(t, st, orch, "audit me")

	planCall(t, reg, "plan_create", map[string]any{
		"spec_id": specID,
		"intent":  "save a memory",
	})
	planCall(t, reg, "plan_decide", map[string]any{
		"spec_id":    specID,
		"decision":   "execute",
		"decided_by": "the-operator",
	})

	rows, err := st.ListWrites(context.Background(), audit.ListFilters{Limit: 50})
	if err != nil {
		t.Fatalf("ListWrites: %v", err)
	}
	var sawCreate, sawDecide bool
	for _, row := range rows {
		if row.TableName != "vibe_specs" {
			continue
		}
		switch row.WritePath {
		case "plan_create":
			sawCreate = true
		case "plan_decide":
			sawDecide = true
		}
	}
	if !sawCreate {
		t.Error("write_audit has no row with write_path=plan_create")
	}
	if !sawDecide {
		t.Error("write_audit has no row with write_path=plan_decide: the human decision left no trail")
	}
}

// An unknown decision is REJECTED, never guessed: defaulting to
// "execute" would let a typo approve a plan.
func TestPlan_DecideRejectsAnUnknownDecision(t *testing.T) {
	reg, st, orch := newPlanEnv(t)
	specID := planSpec(t, st, orch, "bad decision")
	planCall(t, reg, "plan_create", map[string]any{
		"spec_id": specID,
		"intent":  "save a memory",
	})

	resp := planCall(t, reg, "plan_decide", map[string]any{
		"spec_id":  specID,
		"decision": "approve", // not a valid decision
	})
	if resp.Error == nil {
		t.Fatal("plan_decide with an unknown decision returned no error")
	}
	if !strings.Contains(strings.ToLower(resp.Error.Message), "decision") {
		t.Errorf("error message %q does not mention the offending field", resp.Error.Message)
	}
}

// plan_decide(edit) without a prompt is refused: an edit without a
// prompt is not an edit, it is a rejection with extra steps.
func TestPlan_DecideEditRequiresAPrompt(t *testing.T) {
	reg, st, orch := newPlanEnv(t)
	specID := planSpec(t, st, orch, "edit without prompt")
	planCall(t, reg, "plan_create", map[string]any{
		"spec_id": specID,
		"intent":  "save a memory",
	})

	resp := planCall(t, reg, "plan_decide", map[string]any{
		"spec_id":  specID,
		"decision": "edit",
	})
	if resp.Error == nil {
		t.Fatal("plan_decide(edit) without a prompt returned no error")
	}
}

// plan_decide(edit) reopens the plan: the state becomes "edited" and
// the revision counter moves. The edited plan has to be approved
// again before it authorises anything.
func TestPlan_DecideEditReopensThePlan(t *testing.T) {
	reg, st, orch := newPlanEnv(t)
	specID := planSpec(t, st, orch, "edit me")
	planCall(t, reg, "plan_create", map[string]any{
		"spec_id": specID,
		"intent":  "save a memory",
	})

	var decided struct {
		Status   string `json:"status"`
		Revision int    `json:"revision"`
	}
	planData(t, planCall(t, reg, "plan_decide", map[string]any{
		"spec_id":     specID,
		"decision":    "edit",
		"edit_prompt": "narrow it down to one memory write",
	}), &decided)

	if decided.Status != "edited" {
		t.Errorf("status = %q, want %q", decided.Status, "edited")
	}
	if decided.Revision != 1 {
		t.Errorf("revision = %d, want 1", decided.Revision)
	}

	// And the reopened plan is NOT approved: an edit is not an
	// approval.
	var got struct {
		Status string `json:"status"`
	}
	planData(t, planCall(t, reg, "plan_get", map[string]any{"spec_id": specID}), &got)
	if got.Status == "approved" {
		t.Error("status = approved after an edit: the edited plan has to be approved again")
	}
}

func TestPlan_DecideCancelStopsThePlan(t *testing.T) {
	reg, st, orch := newPlanEnv(t)
	specID := planSpec(t, st, orch, "cancel me")
	planCall(t, reg, "plan_create", map[string]any{
		"spec_id": specID,
		"intent":  "save a memory",
	})

	var decided struct {
		Status string `json:"status"`
	}
	planData(t, planCall(t, reg, "plan_decide", map[string]any{
		"spec_id":  specID,
		"decision": "cancel",
	}), &decided)
	if decided.Status != "cancelled" {
		t.Errorf("status = %q, want %q", decided.Status, "cancelled")
	}
}

// plan_decide on a spec with no plan is an error, not a silent
// no-op: the caller asked to decide something that does not exist.
func TestPlan_DecideWithoutAPlanIsAnError(t *testing.T) {
	reg, st, orch := newPlanEnv(t)
	specID := planSpec(t, st, orch, "nothing to decide")

	resp := planCall(t, reg, "plan_decide", map[string]any{
		"spec_id":  specID,
		"decision": "execute",
	})
	if resp.Error == nil {
		t.Fatal("plan_decide on a spec with no plan returned no error")
	}
}

// plan_create on a spec that does not exist is an error.
func TestPlan_CreateOnAMissingSpecIsAnError(t *testing.T) {
	reg, _, _ := newPlanEnv(t)
	resp := planCall(t, reg, "plan_create", map[string]any{
		"spec_id": 999999,
		"intent":  "nothing",
	})
	if resp.Error == nil {
		t.Fatal("plan_create on a missing spec returned no error")
	}
}

// The three tools are in the PLAN namespace and the registry knows
// them.
func TestPlan_ToolsAreRegistered(t *testing.T) {
	reg, _, _ := newPlanEnv(t)
	for _, name := range []string{"plan_create", "plan_get", "plan_decide"} {
		if reg.Get(name) == nil {
			t.Errorf("tool %q is not registered", name)
		}
	}
}

// The full-doc corpus: the ranker sees the description AND the
// schema, which is the measured best configuration (the tool's own
// parameter names are often the terms the intent uses).
func TestPlan_ToolFullDocIncludesTheSchema(t *testing.T) {
	reg, _, _ := newPlanEnv(t)
	tool := reg.Get("plan_create")
	if tool == nil {
		t.Fatal("plan_create is not registered")
	}
	doc := toolFullDoc(tool)
	if !strings.Contains(doc, "spec_id") {
		t.Errorf("the full doc does not mention spec_id:\n%s", doc)
	}
	if !strings.Contains(doc, "intent") {
		t.Errorf("the full doc does not mention intent:\n%s", doc)
	}
	if !strings.Contains(doc, "Propose a plan") {
		t.Errorf("the full doc does not include the description:\n%s", doc)
	}
}

// The path without spec_id: the plan of the OPEN spec of the session.
// The negative case was covered (no open spec); the positive one is
// the path the gate will actually use, so it was the one that mattered.
func TestPlan_GetAndDecideTheOpenSpecOfTheSession(t *testing.T) {
	reg, st, orch := newPlanEnv(t)
	specID := planSpec(t, st, orch, "open spec of the session")

	planCall(t, reg, "plan_create", map[string]any{
		"spec_id": specID,
		"intent":  "save a memory",
	})

	// The session opens that spec.
	if _, err := st.SaveVLPState(context.Background(), store.WriteContext{
		Actor:     "test",
		SessionID: "sess-plan",
		WritePath: "test_seed_vlp",
	}, &store.VLPStateRow{
		SessionID:  "sess-plan",
		State:      1,
		OpenSpecID: specID,
	}); err != nil {
		t.Fatalf("SaveVLPState: %v", err)
	}

	// plan_get without spec_id reads the open plan.
	var got struct {
		Found  bool   `json:"found"`
		SpecID int64  `json:"spec_id"`
		Status string `json:"status"`
	}
	planData(t, planCall(t, reg, "plan_get", map[string]any{
		"session_id": "sess-plan",
	}), &got)
	if !got.Found {
		t.Fatal("plan_get without spec_id: found = false, but the session has an open plan")
	}
	if got.SpecID != specID {
		t.Errorf("spec_id = %d, want %d", got.SpecID, specID)
	}

	// And plan_decide without spec_id decides on it.
	var decided struct {
		SpecID   int64  `json:"spec_id"`
		Decision string `json:"decision"`
		Status   string `json:"status"`
	}
	planData(t, planCall(t, reg, "plan_decide", map[string]any{
		"session_id": "sess-plan",
		"decision":   "execute",
	}), &decided)
	if decided.SpecID != specID {
		t.Errorf("spec_id = %d, want %d", decided.SpecID, specID)
	}
	if decided.Status != "approved" {
		t.Errorf("status = %q, want %q", decided.Status, "approved")
	}
}

// The human decision has to leave a trail that says WHAT was decided
// and what the plan looked like BEFORE (SPEC-194 §3.7 and AC5). The
// earlier test only checked that the row EXISTS, which passes with an
// empty row: the independent review of PR #43 caught that.
func TestPlan_DecideLeavesATrailWithTheDecisionAndThePreviousPlan(t *testing.T) {
	reg, st, orch := newPlanEnv(t)
	specID := planSpec(t, st, orch, "audit the decision")

	planCall(t, reg, "plan_create", map[string]any{
		"spec_id": specID,
		"intent":  "save a memory",
	})
	planCall(t, reg, "plan_decide", map[string]any{
		"spec_id":    specID,
		"decision":   "execute",
		"decided_by": "the-operator",
	})

	rows, err := st.ListWrites(context.Background(), audit.ListFilters{
		TableName: "vibe_specs",
		WritePath: "plan_decide",
		Limit:     50,
	})
	if err != nil {
		t.Fatalf("ListWrites: %v", err)
	}
	var row *audit.WriteEvent
	for i := range rows {
		if rows[i].WritePath == "plan_decide" {
			row = &rows[i]
			break
		}
	}
	if row == nil {
		t.Fatal("write_audit has no row with write_path=plan_decide")
	}

	// The actor is the PERSON who decided, not the tool name.
	if row.Actor != "the-operator" {
		t.Errorf("actor = %q, want %q: the trail must name who decided", row.Actor, "the-operator")
	}
	// The notes say what was decided...
	if !strings.Contains(row.Notes, "decision=execute") {
		t.Errorf("notes %q do not say what was decided", row.Notes)
	}
	if !strings.Contains(row.Notes, "decided_by=the-operator") {
		t.Errorf("notes %q do not say who decided", row.Notes)
	}
	// ...and what the plan looked like BEFORE (AC5).
	if !strings.Contains(row.Notes, "previous_intent=") {
		t.Errorf("notes %q do not carry the previous plan", row.Notes)
	}
	if !strings.Contains(row.Notes, "previous_candidates=") {
		t.Errorf("notes %q do not carry the previous candidates", row.Notes)
	}
}

// An edit has to RE-PROPOSE: the reviewer narrowed the intent, so the
// shortlist is ranked again against the new one. Keeping the
// candidates of the old intent would be a plan that does not answer
// the question the reviewer asked (SPEC-194 §3.7).
func TestPlan_EditReProposesTheShortlist(t *testing.T) {
	reg, st, orch := newPlanEnv(t)
	specID := planSpec(t, st, orch, "edit re-proposes")

	planCall(t, reg, "plan_create", map[string]any{
		"spec_id": specID,
		"intent":  "write the quarterly sales report with charts and a summary",
	})

	var before struct {
		Plan struct {
			Candidates []struct {
				Tool string `json:"tool"`
			} `json:"candidates"`
			Revision int `json:"revision"`
		} `json:"plan"`
	}
	planData(t, planCall(t, reg, "plan_get", map[string]any{"spec_id": specID}), &before)

	var decided struct {
		Plan struct {
			Candidates []struct {
				Tool string `json:"tool"`
			} `json:"candidates"`
			Revision int `json:"revision"`
		} `json:"plan"`
	}
	planData(t, planCall(t, reg, "plan_decide", map[string]any{
		"spec_id":     specID,
		"decision":    "edit",
		"edit_prompt": "save a single memory row",
	}), &decided)

	if decided.Plan.Revision != 1 {
		t.Errorf("revision = %d, want 1", decided.Plan.Revision)
	}
	if len(decided.Plan.Candidates) == 0 {
		t.Fatal("the edit left no candidates: the plan was not re-proposed")
	}
	// The shortlist of the NEW intent must be about the memory write,
	// not about the report.
	sawMemory := false
	for _, c := range decided.Plan.Candidates {
		if c.Tool == "agent_memory_save" {
			sawMemory = true
		}
	}
	if !sawMemory {
		t.Errorf("the re-proposed shortlist does not contain agent_memory_save: %+v", decided.Plan.Candidates)
	}
}
