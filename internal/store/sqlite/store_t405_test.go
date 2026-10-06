// Copyright 2026 Nico. All rights reserved.
// T-405 regression tests (v4.0.0-alpha.27-pre-1): Store.GetProject +
// Store.ListProjects must RETAIN the AuthToken on read (so internal
// callers like orchestration.EnsureNLIRouter and tools.llm_provider_probe
// can use it). Tool result paths MUST call NLIConfig.Redacted() themselves.
//
// Root cause of the regression: Store.GetProject previously called
// cfg.Redacted() unconditionally. The redaction was meant for tool
// output boundaries but accidentally leaked into internal callers that
// need the bearer to reach the LLM. Result: every drift_judge call
// returned needs_human ("nli score failed: nli: provider unavailable")
// because the HTTP request went out with no Authorization header.
//
// These tests assert the new contract end-to-end with a real sqlite DB.

package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// testStoreWithProject opens a fresh on-disk sqlite store, seeds the
// "default" project with a NLIConfig that has a real (visible) bearer,
// and returns (store, cleanup). Used by TestGetProject_RetainsAuthToken
// + TestListProjects_RetainsAuthToken.
func testStoreWithProject(t *testing.T) (*Store, func()) {
	t.Helper()
	ctx := context.Background()
	cfg := store.Config{
		Driver:      "sqlite",
		DSN:         filepath.Join(t.TempDir(), "test.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	st, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s, ok := st.(*Store)
	if !ok {
		t.Fatalf("Open returned %T, want *Store", st)
	}
	if err := s.CreateProject(ctx, &project.Project{
		ProjectID:   "default",
		DisplayName: "Test",
		NLIConfig: &project.NLIConfig{
			Enabled: true,
			Primary: project.NLIPrimary{
				ProviderID: "chat-minimax-cn",
				Endpoint:   "https://api.minimaxi.com/v1/chat/completions",
				AuthToken:  "sk-TEST-DO-NOT-LEAK-1234567890",
				TimeoutMS:  30000,
				ModelRev:   "MiniMax-M3",
			},
		},
	}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return s, func() { _ = s.Close() }
}

// TestGetProject_RetainsAuthToken — T-405 regression guard.
// Pre-fix, GetProject returned cfg.Redacted() and the orchestrator
// built an NLIProvider with an empty bearer → HTTP 401 → needs_human.
// Post-fix, GetProject MUST return the full token so internal callers
// can build working NLIProviders.
func TestGetProject_RetainsAuthToken(t *testing.T) {
	s, cleanup := testStoreWithProject(t)
	defer cleanup()

	p, err := s.GetProject(context.Background(), "default")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if p == nil || p.NLIConfig == nil {
		t.Fatal("project or NLIConfig nil")
	}
	if p.NLIConfig.Primary.AuthToken == "" {
		t.Fatal("GetProject stripped AuthToken (regression of T-405): internal callers " +
			"need the bearer to reach the LLM")
	}
	if p.NLIConfig.Primary.AuthToken != "sk-TEST-DO-NOT-LEAK-1234567890" {
		t.Errorf("AuthToken mutated: got prefix=%q, want sk-TEST-",
			p.NLIConfig.Primary.AuthToken[:min(8, len(p.NLIConfig.Primary.AuthToken))])
	}
}

// TestListProjects_RetainsAuthToken — T-405 regression guard.
// ListProjects is the bulk path used by project_list tools. Same
// contract: retain the token; tool layer redacts at output.
func TestListProjects_RetainsAuthToken(t *testing.T) {
	s, cleanup := testStoreWithProject(t)
	defer cleanup()

	out, err := s.ListProjects(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("ListProjects returned 0 rows")
	}
	for _, p := range out {
		if p.NLIConfig == nil {
			continue // archived or no config — acceptable
		}
		if p.NLIConfig.Primary.AuthToken == "" {
			t.Errorf("ListProjects stripped AuthToken for project %q (regression of T-405)",
				p.ProjectID)
		}
	}
}

// TestGetProject_NLIConfigParseFailure_DoesNotStripConfig — sanity check.
// If the JSON parse fails (e.g. corrupted row), NLIConfig stays nil.
// Pre-fix, this path also went through Redacted() which would NPE on nil.
func TestGetProject_NLIConfigParseFailure_DoesNotStripConfig(t *testing.T) {
	ctx := context.Background()
	cfg := store.Config{
		Driver:      "sqlite",
		DSN:         filepath.Join(t.TempDir(), "corrupt.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	st, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s, ok := st.(*Store)
	if !ok {
		t.Fatalf("Open returned %T, want *Store", st)
	}
	defer s.Close()

	if err := s.CreateProject(ctx, &project.Project{
		ProjectID:   "default",
		DisplayName: "Test",
	}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Corrupt the nli_config_json directly via raw SQL.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE projects SET nli_config_json = ? WHERE project_id = ?`,
		"{this is not valid json", "default"); err != nil {
		t.Fatalf("corrupt write: %v", err)
	}

	p, err := s.GetProject(ctx, "default")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	// Parse failure leaves NLIConfig nil (graceful degradation). This is
	// the existing posture — Redacted() was a no-op when called on nil.
	if p.NLIConfig != nil {
		t.Errorf("expected NLIConfig nil on parse failure, got %+v", p.NLIConfig)
	}
}

// TestNLIConfig_Redacted_StillWorks — guard against accidentally
// breaking the redaction helper. Even though Store no longer calls it,
// tools/project.go + ProjectCreateResult still rely on it.
func TestNLIConfig_Redacted_StillWorks(t *testing.T) {
	c := &project.NLIConfig{
		Enabled: true,
		Primary: project.NLIPrimary{
			ProviderID: "chat-x",
			Endpoint:   "https://x",
			AuthToken:  "DO-NOT-LEAK",
		},
		Fallback: project.NLIPrimary{
			AuthToken: "FALLBACK-TOKEN",
		},
	}
	r := c.Redacted()
	if r == nil {
		t.Fatal("Redacted returned nil")
	}
	if r.Primary.AuthToken != "" || r.Fallback.AuthToken != "" {
		t.Errorf("Redacted left tokens: primary=%q fallback=%q",
			r.Primary.AuthToken, r.Fallback.AuthToken)
	}
	// Original must be untouched (defensive copy).
	if c.Primary.AuthToken != "DO-NOT-LEAK" || c.Fallback.AuthToken != "FALLBACK-TOKEN" {
		t.Errorf("Redacted mutated original: primary=%q fallback=%q",
			c.Primary.AuthToken, c.Fallback.AuthToken)
	}
	// Sanity: a stripped config is still valid JSON.
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "DO-NOT-LEAK") || strings.Contains(string(b), "FALLBACK-TOKEN") {
		t.Errorf("Redacted JSON still contains tokens: %s", string(b))
	}
}