package atomic

import (
	"strings"
	"testing"
	"time"
)

func TestCapabilitiesFrame_HappyPath(t *testing.T) {
	tools := []ToolGrant{
		{ToolName: "dark_memory_recall", Scope: "*", GrantedAt: time.Now()},
		{ToolName: "dark_memory_session_close", Scope: "proj-1", GrantedAt: time.Now()},
	}
	scopes := []ScopeGrant{
		{ProjectID: "proj-1", ReadOnly: false},
		{ProjectID: "*", ReadOnly: true},
	}
	f, err := NewCapabilitiesFrame("proj-1", "sess-1", tools, scopes, time.Time{}, "constitution,mod:red-team")
	if err != nil {
		t.Fatalf("NewCapabilitiesFrame: %v", err)
	}
	if err := f.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !f.HasGrant("dark_memory_recall") {
		t.Errorf("HasGrant recall: expected true")
	}
	if !f.HasGrant("dark_memory_session_close") {
		t.Errorf("HasGrant session_close: expected true")
	}
	if f.HasGrant("nonexistent_tool") {
		t.Errorf("HasGrant nonexistent: expected false")
	}
	if !f.HasProjectAccess("proj-1") {
		t.Errorf("HasProjectAccess proj-1: expected true")
	}
	if !f.HasProjectAccess("any-project") {
		t.Errorf("HasProjectAccess any-project under *: expected true")
	}
	if f.IsExpired(time.Now()) {
		t.Errorf("IsExpired: zero expiresAt should never expire")
	}
}

func TestCapabilitiesFrame_EmptyProjectID(t *testing.T) {
	if _, err := NewCapabilitiesFrame("", "s", nil, nil, time.Time{}, ""); err != ErrCapabilitiesEmptyProjectID {
		t.Errorf("want ErrCapabilitiesEmptyProjectID, got %v", err)
	}
}

func TestCapabilitiesFrame_EmptySessionID(t *testing.T) {
	if _, err := NewCapabilitiesFrame("p", "", nil, nil, time.Time{}, ""); err != ErrCapabilitiesEmptySessionID {
		t.Errorf("want ErrCapabilitiesEmptySessionID, got %v", err)
	}
}

func TestCapabilitiesFrame_ToolMissingName(t *testing.T) {
	tools := []ToolGrant{{ToolName: ""}}
	if _, err := NewCapabilitiesFrame("p", "s", tools, nil, time.Time{}, ""); err == nil {
		t.Fatalf("expected error for empty tool name")
	} else if !strings.Contains(err.Error(), "tool_name") {
		t.Errorf("want tool_name in error, got %v", err)
	}
}

func TestCapabilitiesFrame_ScopeMissingProjectID(t *testing.T) {
	scopes := []ScopeGrant{{ProjectID: ""}}
	if _, err := NewCapabilitiesFrame("p", "s", nil, scopes, time.Time{}, ""); err == nil {
		t.Fatalf("expected error for empty scope project_id")
	}
}

func TestCapabilitiesFrame_IsExpired(t *testing.T) {
	f, _ := NewCapabilitiesFrame("p", "s", nil, nil, time.Now().Add(-time.Hour), "env:DARK_GRANTS")
	if !f.IsExpired(time.Now()) {
		t.Errorf("expected expired for past expiresAt")
	}
}

func TestCapabilitiesFrame_HashDeterminism(t *testing.T) {
	f1, _ := NewCapabilitiesFrame("p", "s", []ToolGrant{{ToolName: "x", Scope: "*"}}, nil, time.Time{}, "src")
	f2, _ := NewCapabilitiesFrame("p", "s", []ToolGrant{{ToolName: "x", Scope: "*"}}, nil, time.Time{}, "src")
	f1.ComposedAtValue = f2.ComposedAtValue
	h1, _ := f1.Hash()
	h2, _ := f2.Hash()
	if h1 != h2 {
		t.Errorf("hash determinism failed")
	}
}
