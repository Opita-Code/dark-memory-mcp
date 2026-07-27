// Package agentmemory_test: unit tests for the agentmemory
// package's types + helpers. The Store-level integration tests
// live in tests/dual_driver/agent_memory_test.go (real SQLite
// driver). These are the cheap, fast, no-driver tests.
package agentmemory_test

import (
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
)

func TestValidKind(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"note", true},
		{"observation", true},
		{"decision", true},
		{"finding", true},
		{"todo", true},
		{"link", true},
		{"context", true},
		{"", false},
		{"NOTE", false},  // case-sensitive
		{"note ", false}, // whitespace not trimmed
		{"unknown", false},
	}
	for _, c := range cases {
		if got := agentmemory.ValidKind(c.in); got != c.want {
			t.Errorf("ValidKind(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestValidScope(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", true},         // empty == ScopeCurrent default
		{"current", true},
		{"session", true},
		{"project", true},
		{"operator", true},
		{"all", true},
		{"global", false},
		{"SESSION", false},
		{"nonsense", false},
	}
	for _, c := range cases {
		if got := agentmemory.ValidScope(c.in); got != c.want {
			t.Errorf("ValidScope(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestAgentMemory_IsArchived(t *testing.T) {
	m := agentmemory.AgentMemory{}
	if m.IsArchived() {
		t.Error("zero-value AgentMemory.IsArchived() = true, want false")
	}
	m.ArchivedAt = "2026-07-27T00:00:00Z"
	if !m.IsArchived() {
		t.Error("AgentMemory with ArchivedAt set: IsArchived() = false, want true")
	}
}

func TestAgentMemory_CreatedAtTime(t *testing.T) {
	m := agentmemory.AgentMemory{}
	if _, ok := m.CreatedAtTime(); ok {
		t.Error("zero CreatedAt should fail to parse")
	}
	m.CreatedAt = "2026-07-27T10:30:00Z"
	tm, ok := m.CreatedAtTime()
	if !ok {
		t.Fatal("CreatedAt set but failed to parse")
	}
	if tm.Year() != 2026 || tm.Month() != 7 || tm.Day() != 27 {
		t.Errorf("CreatedAtTime year/month/day wrong: %v", tm)
	}
	m.CreatedAt = "not a date"
	if _, ok := m.CreatedAtTime(); ok {
		t.Error("malformed CreatedAt should fail to parse")
	}
}

// Sanity check that the seven kind constants have stable names.
// Adding a new kind requires updating tests, code, and CHANGELOG;
// renaming an existing one is a wire-contract break.
func TestKindConstantsStable(t *testing.T) {
	want := []string{
		agentmemory.KindNote,
		agentmemory.KindObservation,
		agentmemory.KindDecision,
		agentmemory.KindFinding,
		agentmemory.KindTodo,
		agentmemory.KindLink,
		agentmemory.KindContext,
	}
	if len(want) != 7 {
		t.Fatalf("expected 7 kinds, got %d", len(want))
	}
	for i, k := range want {
		if !agentmemory.ValidKind(k) {
			t.Errorf("kind[%d] %q is not ValidKind=true (constant drift?)", i, k)
		}
	}
	// And no duplicates
	seen := map[string]bool{}
	for _, k := range want {
		if seen[k] {
			t.Errorf("duplicate kind %q", k)
		}
		seen[k] = true
	}
	// And no kind starts with a non-letter (would be a SQL identifier
	// hazard in the migration's CHECK constraints if we add one).
	for _, k := range want {
		if !strings.HasPrefix(k, "n") && !strings.HasPrefix(k, "o") &&
			!strings.HasPrefix(k, "d") && !strings.HasPrefix(k, "f") &&
			!strings.HasPrefix(k, "t") && !strings.HasPrefix(k, "l") &&
			!strings.HasPrefix(k, "c") {
			// Loose: just ensure no leading underscore or digit
			if strings.HasPrefix(k, "_") || (k[0] >= '0' && k[0] <= '9') {
				t.Errorf("kind %q has unsafe leading char", k)
			}
		}
	}
}