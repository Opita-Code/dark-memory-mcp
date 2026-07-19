// Package context_test covers round-trip behavior of ArtifactContext,
// SessionContext, and PolicyContext. Each test uses an in-memory
// SQLite store via the dual-driver test helper.
package context_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	contextview "github.com/dark-agents/dark-memory-mcp/internal/context"
	"github.com/dark-agents/dark-memory-mcp/internal/research"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

func openTestStore(t *testing.T) store.Store {
	t.Helper()
	cfg := store.Config{
		Driver:       store.DriverSQLite,
		DSN:          filepath.Join(t.TempDir(), "test.db"),
		WALMode:      true,
		ForeignKeys:  true,
		BusyTimeout:  5 * time.Second,
	}
	s, err := runtime.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seedSpec writes a spec with the given tasks JSON and returns its id.
func seedSpec(t *testing.T, ctx context.Context, s store.Store, sessionID, tasksJSON string) int64 {
	t.Helper()
	wc := store.WriteContext{Actor: "test", SessionID: sessionID, WritePath: "seed"}
	sp := &vibeflow.Spec{
		VibeCase:     "C1",
		SessionID:    sessionID,
		Constitution: `{"rule":"test"}`,
		Spec:         `{"what":"seed"}`,
		Tasks:        tasksJSON,
	}
	id, err := s.SaveSpec(ctx, wc, sp)
	if err != nil {
		t.Fatalf("SaveSpec: %v", err)
	}
	return id
}

// seedBrand writes a brand guide and returns nothing (idempotent).
func seedBrand(t *testing.T, ctx context.Context, s store.Store, brandID string) {
	t.Helper()
	wc := store.WriteContext{Actor: "test", SessionID: "seed", WritePath: "seed"}
	if err := s.SaveBrandGuide(ctx, wc, &vibeflow.BrandGuide{
		BrandID: brandID,
		Voice:   `{"tone":"technical"}`,
	}); err != nil {
		t.Fatalf("SaveBrandGuide: %v", err)
	}
}

// seedCompliance writes a compliance rule.
func seedCompliance(t *testing.T, ctx context.Context, s store.Store, jurisdiction string) {
	t.Helper()
	wc := store.WriteContext{Actor: "test", SessionID: "seed", WritePath: "seed"}
	if err := s.SaveComplianceRule(ctx, wc, &vibeflow.ComplianceRule{
		Jurisdiction: jurisdiction,
		Rules:        `{"disclosure_required":true}`,
	}); err != nil {
		t.Fatalf("SaveComplianceRule: %v", err)
	}
}

// seedArtifact writes a spec + artifact + drift report and returns the artifact id.
func seedArtifact(t *testing.T, ctx context.Context, s store.Store, sessionID, brandID, jurisdiction string) int64 {
	t.Helper()
	wc := store.WriteContext{Actor: "test", SessionID: sessionID, WritePath: "seed"}

	tasksJSON := `[{"id":"1","description":"task","depends_on":[]}]`
	specID := seedSpec(t, ctx, s, sessionID, tasksJSON)
	seedBrand(t, ctx, s, brandID)
	seedCompliance(t, ctx, s, jurisdiction)

	a := &vibeflow.Artifact{
		SessionID:     sessionID,
		VibeCase:      "C1",
		SpecID:        specID,
		ArtifactURL:   "file:///tmp/artifact.txt",
		ArtifactType:  "text",
		BrandID:       brandID,
		Jurisdiction:  jurisdiction,
		HasDisclosure: false,
	}
	id, err := s.SaveArtifact(ctx, wc, a)
	if err != nil {
		t.Fatalf("SaveArtifact: %v", err)
	}

	// Add a drift report so LastDrift has something to find.
	if _, err := s.SaveDriftReport(ctx, wc, &vibeflow.DriftReport{
		ArtifactID: id,
		SpecID:     specID,
		Verdict:    "aligned",
	}); err != nil {
		t.Fatalf("SaveDriftReport: %v", err)
	}
	return id
}

// seedSession writes a session and returns it.
func seedSession(t *testing.T, ctx context.Context, s store.Store, sessionID string) {
	t.Helper()
	// Set up the active project (required since INV-7 / migration v7).
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}
	wc := store.WriteContext{Actor: "test", SessionID: sessionID, WritePath: "seed"}
	sess := &session.Session{
		SessionID: sessionID,
		Status:    string(session.StatusOpen),
		Operator:  "test",
	}
	if _, err := s.SaveSession(ctx, wc, sess); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
}

func TestComposeArtifact_RoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedSession(t, ctx, s, "session-A")

	artifactID := seedArtifact(t, ctx, s, "session-A", "acme", "EU")

	c, err := contextview.ComposeArtifact(ctx, s, artifactID)
	if err != nil {
		t.Fatalf("ComposeArtifact: %v", err)
	}
	if c.Artifact == nil || c.Artifact.ID != artifactID {
		t.Fatalf("Artifact not loaded: %+v", c.Artifact)
	}
	if c.Brand == nil || c.Brand.BrandID != "acme" {
		t.Fatalf("Brand not resolved: %+v", c.Brand)
	}
	if c.Compliance == nil || c.Compliance.Jurisdiction != "EU" {
		t.Fatalf("Compliance not resolved: %+v", c.Compliance)
	}
	if c.LastDrift == nil || c.LastDrift.Verdict != "aligned" {
		t.Fatalf("LastDrift not resolved: %+v", c.LastDrift)
	}
	if c.SpecMarkdown == "" {
		t.Fatalf("SpecMarkdown not rendered")
	}
	if !strings.Contains(c.SpecMarkdown, "# Spec") {
		t.Fatalf("SpecMarkdown malformed: %q", c.SpecMarkdown)
	}
	if len(c.SpecTasks) != 1 || c.SpecTasks[0].ID != "1" {
		t.Fatalf("SpecTasks malformed: %+v", c.SpecTasks)
	}
	if len(c.WriteAuditTail) == 0 {
		t.Fatalf("WriteAuditTail empty — INV-1 may be broken")
	}
}

func TestComposeArtifact_NotFound(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	_, err := contextview.ComposeArtifact(ctx, s, 99999)
	if err == nil {
		t.Fatalf("expected error for missing artifact")
	}
}

func TestComposeSession_RoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedSession(t, ctx, s, "session-B")

	// Create an artifact in this session so counts are non-zero.
	seedArtifact(t, ctx, s, "session-B", "acme", "US")

	c, err := contextview.ComposeSession(ctx, s, "session-B")
	if err != nil {
		t.Fatalf("ComposeSession: %v", err)
	}
	if c.Session == nil || c.Session.SessionID != "session-B" {
		t.Fatalf("Session not loaded: %+v", c.Session)
	}
	if c.ActiveSpec == nil {
		t.Fatalf("ActiveSpec not set")
	}
	if c.Counts.SpecsTotal == 0 {
		t.Fatalf("SpecsTotal should be > 0")
	}
	if c.Counts.ArtifactsTotal == 0 {
		t.Fatalf("ArtifactsTotal should be > 0")
	}
	if c.Counts.DriftsTotal == 0 {
		t.Fatalf("DriftsTotal should be > 0 (we seeded one)")
	}
	if c.Counts.WriteAuditTotal == 0 {
		t.Fatalf("WriteAuditTotal should be > 0")
	}
}

func TestComposeSession_NotFound(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	_, err := contextview.ComposeSession(ctx, s, "missing-session")
	if err == nil {
		t.Fatalf("expected error for missing session")
	}
}

func TestComposePolicy_Defaults(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	p, err := contextview.ComposePolicy(ctx, s, "sqlite", 6)
	if err != nil {
		t.Fatalf("ComposePolicy: %v", err)
	}
	if p.Driver != "sqlite" {
		t.Fatalf("Driver mismatch: %s", p.Driver)
	}
	if p.SchemaVersion != 6 {
		t.Fatalf("SchemaVersion mismatch: %d", p.SchemaVersion)
	}
	if p.CoexistenceGroup != "dark-agents/memory" {
		t.Fatalf("CoexistenceGroup not set")
	}
	if p.CoexistenceVersion != "cx.v2" {
		t.Fatalf("CoexistenceVersion not cx.v2")
	}
	if p.WatchdogStatus != "ok" {
		t.Fatalf("WatchdogStatus should default to ok: %s", p.WatchdogStatus)
	}
}

func TestRenderSpecMarkdown_Deterministic(t *testing.T) {
	now := "2026-07-14T20:00:00Z"
	sp := &vibeflow.Spec{
		ID:        42,
		VibeCase:  "C1",
		CreatedAt: now,
		UpdatedAt: now,
		Constitution: `{"k":"v"}`,
		Spec:         `{"what":"x"}`,
		Tasks:        `[{"id":"1","description":"d"}]`,
	}
	md1 := contextview.RenderSpecMarkdown(sp)
	md2 := contextview.RenderSpecMarkdown(sp)
	if md1 != md2 {
		t.Fatalf("RenderSpecMarkdown is non-deterministic:\n---\n%s\n---\n%s", md1, md2)
	}
	if !strings.Contains(md1, "# Spec #42") {
		t.Fatalf("markdown missing spec id: %s", md1)
	}
	if !strings.Contains(md1, "Constitution (hard rules)") {
		t.Fatalf("markdown missing constitution section: %s", md1)
	}
}

func TestParseSpecTasks_Variants(t *testing.T) {
	cases := []struct {
		name    string
		json    string
		wantLen int
	}{
		{"empty", "", 0},
		{"single", `[{"id":"1","description":"d"}]`, 1},
		{"with deps", `[{"id":"1","description":"d","depends_on":["2","3"]}]`, 1},
		{"multi", `[{"id":"1","description":"a"},{"id":"2","description":"b"}]`, 2},
		{"invalid", `{not json`, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := contextview.ParseSpecTasks(c.json)
			if len(got) != c.wantLen {
				t.Fatalf("%s: want len %d got %d", c.name, c.wantLen, len(got))
			}
		})
	}
}

// ensure unused import slots don't break the build when we add helpers
var _ = json.Marshal
var _ = audit.ListFilters{}
var _ = research.Item{}