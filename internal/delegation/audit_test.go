// Package delegation — audit_test.go: AUDIT-stage tests (Wave 5C).
//
// RecallSubagentFindings must return exactly the rows the sub-agent
// persisted under its tag, and must be isolation-correct (rows from
// other sub-agents / other kinds are invisible). The fake recalls
// whatever the store would return for the filters — the store's own
// INV-7 + tag matching is exercised in the dual-driver suite.
package delegation

import (
	"context"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
)

// fakeRecaller is a minimal AuditRecaller that returns canned rows
// keyed by (kind, tag).
type fakeRecaller struct {
	rows []agentmemory.AgentMemory
	err  error
}

func (f *fakeRecaller) ListAgentMemory(_ context.Context, flt agentmemory.AgentMemoryListFilters) ([]agentmemory.AgentMemory, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []agentmemory.AgentMemory
	for _, r := range f.rows {
		if flt.Kind != "" && r.Kind != flt.Kind {
			continue
		}
		if flt.Tag != "" && !containsTag(r.Tags, flt.Tag) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func containsTag(tags, want string) bool {
	for _, t := range splitTags(tags) {
		if t == want {
			return true
		}
	}
	return false
}

func splitTags(tags string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(tags); i++ {
		if i == len(tags) || tags[i] == ',' {
			if i > start {
				out = append(out, tags[start:i])
			}
			start = i + 1
		}
	}
	return out
}

func TestRecallSubagentFindings_GroupsByKind(t *testing.T) {
	ctx := context.Background()
	rec := &fakeRecaller{rows: []agentmemory.AgentMemory{
		{ID: 1, Kind: "finding", Tags: "subagent-abc, code-review"},
		{ID: 2, Kind: "decision", Tags: "subagent-abc"},
		{ID: 3, Kind: "observation", Tags: "subagent-abc"},
		{ID: 4, Kind: "note", Tags: "subagent-abc"}, // other kind — ignored
		{ID: 5, Kind: "finding", Tags: "subagent-xyz"}, // other subagent — ignored
	}}

	got, err := RecallSubagentFindings(ctx, rec, "abc")
	if err != nil {
		t.Fatalf("RecallSubagentFindings: %v", err)
	}
	if got.SubagentID != "abc" {
		t.Errorf("SubagentID = %q, want abc", got.SubagentID)
	}
	if len(got.Findings) != 1 || got.Findings[0].ID != 1 {
		t.Errorf("Findings = %+v, want [id=1]", got.Findings)
	}
	if len(got.Decisions) != 1 || got.Decisions[0].ID != 2 {
		t.Errorf("Decisions = %+v, want [id=2]", got.Decisions)
	}
	if len(got.Observations) != 1 || got.Observations[0].ID != 3 {
		t.Errorf("Observations = %+v, want [id=3]", got.Observations)
	}
	if got.Total != 3 {
		t.Errorf("Total = %d, want 3", got.Total)
	}
}

func TestRecallSubagentFindings_EmptyIsNotError(t *testing.T) {
	ctx := context.Background()
	rec := &fakeRecaller{rows: nil}

	got, err := RecallSubagentFindings(ctx, rec, "ghost")
	if err != nil {
		t.Fatalf("RecallSubagentFindings: %v", err)
	}
	if got.Total != 0 {
		t.Errorf("Total = %d, want 0 (empty is not an error)", got.Total)
	}
}

func TestRecallSubagentFindings_EmptyIDRejected(t *testing.T) {
	ctx := context.Background()
	rec := &fakeRecaller{}
	if _, err := RecallSubagentFindings(ctx, rec, ""); err == nil {
		t.Fatal("expected error for empty subagent_id")
	}
}

func TestRecallSubagentFindings_StoreErrorPropagates(t *testing.T) {
	ctx := context.Background()
	rec := &fakeRecaller{err: errTestStore}
	if _, err := RecallSubagentFindings(ctx, rec, "abc"); err == nil {
		t.Fatal("expected store error to propagate")
	}
}

var errTestStore = errTest{}

type errTest struct{}

func (errTest) Error() string { return "test store error" }

func TestTagForSubagent(t *testing.T) {
	if got := TagForSubagent("abc"); got != "subagent-abc" {
		t.Errorf("TagForSubagent(abc) = %q, want subagent-abc", got)
	}
}
