// Copyright 2026 Nico. All rights reserved.
// T-405 regression tests, as revised by Phase 21 (operator decision,
// option C).
//
// HISTORY, because this contract has now moved twice and the reason
// matters more than the rule:
//
//   1. Original design: Store.GetProject called cfg.Redacted() on read.
//      Every internal caller therefore got an EMPTY bearer. T-405
//      (alpha.27-pre-1) diagnosed this correctly: drift_judge returned
//      needs_human ("nli score failed: nli: provider unavailable")
//      because the HTTP request went out with no Authorization header.
//   2. T-405's fix was to invert the store: return the RAW config and
//      push redaction onto tool result paths. That fixed the outage but
//      left the three internal/tools project tests red -- they still
//      asserted the original contract and nothing updated them. The repo
//      carried a contract change with its own tests left failing.
//   3. Phase 21 (option C) resolves it instead of picking a side: the
//      store REDACTS on the generic read paths (GetProject,
//      ListProjects) and exposes GetProjectRaw as a sealed accessor for
//      the four call sites that must actually authenticate. The T-405
//      outage cannot recur, because the orchestrator no longer depends
//      on the generic path leaking a credential.
//
// These tests assert BOTH halves of that contract against a real sqlite
// DB, because either half alone is a trap: redaction without a raw seam
// breaks drift_judge (the T-405 outage), and a raw seam without
// redaction re-introduces the credential leak.

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
// and returns (store, cleanup). Used by TestGetProject_RedactsAuthToken,
// TestGetProjectRaw_RetainsAuthToken and TestListProjects_RedactsAuthToken.
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

// TestGetProject_RedactsAuthToken -- the generic read path must NOT
// hand a bearer to a caller that did not explicitly ask for it.
//
// This is the assertion the whole option-C change rests on, and it is
// the exact test internal/tools/project_test.go has been failing since
// T-405 inverted the contract and never updated it.
func TestGetProject_RedactsAuthToken(t *testing.T) {
	s, cleanup := testStoreWithProject(t)
	defer cleanup()

	p, err := s.GetProject(context.Background(), "default")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if p == nil || p.NLIConfig == nil {
		t.Fatal("project or NLIConfig nil")
	}
	if p.NLIConfig.Primary.AuthToken != "" {
		t.Errorf("GetProject leaked AuthToken: %q", p.NLIConfig.Primary.AuthToken)
	}
	if p.NLIConfig.Fallback.AuthToken != "" {
		t.Errorf("GetProject leaked Fallback AuthToken: %q", p.NLIConfig.Fallback.AuthToken)
	}
	// Non-secret fields must survive redaction, or a redacted config is
	// useless to a caller that legitimately needs provider metadata.
	if p.NLIConfig.Primary.ProviderID != "chat-minimax-cn" {
		t.Errorf("redaction destroyed ProviderID: %q", p.NLIConfig.Primary.ProviderID)
	}
	if p.NLIConfig.Primary.Endpoint == "" {
		t.Error("redaction destroyed Endpoint")
	}
	if !p.NLIConfig.Enabled {
		t.Error("redaction destroyed Enabled")
	}
}

// TestGetProjectRaw_RetainsAuthToken -- the sealed seam MUST return the
// real credential.
//
// THIS IS THE TEST THAT PROTECTS drift_judge. T-405 was a real outage:
// empty bearer -> HTTP 401 -> needs_human on every drift_judge call.
// That bug is easy to reintroduce, because "we redact at the store now"
// is exactly how it came back the first time. If someone repoints the
// orchestrator at GetProject, or drops GetProjectRaw, this fails and the
// judge outage returns with it.
func TestGetProjectRaw_RetainsAuthToken(t *testing.T) {
	s, cleanup := testStoreWithProject(t)
	defer cleanup()

	p, err := s.GetProjectRaw(context.Background(), "default")
	if err != nil {
		t.Fatalf("GetProjectRaw: %v", err)
	}
	if p == nil || p.NLIConfig == nil {
		t.Fatal("project or NLIConfig nil")
	}
	if p.NLIConfig.Primary.AuthToken != "sk-TEST-DO-NOT-LEAK-1234567890" {
		t.Errorf("GetProjectRaw did not return the bearer (drift_judge would 401): got %q",
			p.NLIConfig.Primary.AuthToken)
	}
}

// TestListProjects_RedactsAuthToken -- the bulk read path redacts too.
//
// The comment this replaces claimed ListProjects is "the bulk path used
// by project_list tools" which redacted at output. There has never been
// a project_list tool; the only consumer is orchestration.memory_state,
// which counts projects and never needs a credential. So nothing
// redacted this path -- a comment asserted a guarantee that did not
// exist. There is deliberately no ListProjectsRaw: a bulk secret read is
// the shape of a mass-leak.
func TestListProjects_RedactsAuthToken(t *testing.T) {
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
		if p.NLIConfig.Primary.AuthToken != "" {
			t.Errorf("ListProjects leaked AuthToken for project %q: %q",
				p.ProjectID, p.NLIConfig.Primary.AuthToken)
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

// TestNLIConfig_Redacted_StillWorks -- guard against accidentally
// breaking the redaction helper. Store.GetProject and ListProjects call
// it on every read now, and tools/project.go + ProjectCreateResult still
// rely on it for the create result.
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