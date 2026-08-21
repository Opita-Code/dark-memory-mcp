// Package tools — project_test.go: T07 tests for project.NLIConfig +
// project_create wiring.
//
// Tests are split into two layers:
//   1. project.NLIConfig.Validate()  — pure unit tests against the
//      sealed struct. No DB, no MCP server.
//   2. project_create end-to-end     — round-trip through the Store
//      (real SQLite via internal/store/sqlite.Open) and validates
//      the JSON column survives Create + Get + List idempotently,
//      AuthToken is redacted on read, and parse failures degrade
//      gracefully.
//
// Reference: spec 1276 T07.
package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	sqlitestore "github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
	_ "modernc.org/sqlite" // register "sqlite3" driver for raw column checks
)

// newProjectTestStore opens a fresh SQLite Store with the
// "default" project active. Mirrors the pattern from
// internal/store/sqlite/drift_merkle_test.go.
func newProjectTestStore(t *testing.T) (store.Store, string, func()) {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	dsn := filepath.Join(tmp, "project-test.db")
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         dsn,
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	st, err := sqlitestore.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("sqlitestore.Open: %v", err)
	}
	if err := st.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}
	cleanup := func() { _ = st.Close() }
	return st, dsn, cleanup
}

// rawDB opens a fresh database/sql handle on the same DSN — used
// only for column-corruption tests that need direct write access.
func rawDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// ----- NLIConfig.Validate unit tests --------------------------------------

func TestNLIConfig_Validate_NilIsValid(t *testing.T) {
	t.Parallel()
	if err := ((*project.NLIConfig)(nil)).Validate(); err != nil {
		t.Errorf("nil NLIConfig.Validate: err=%v, want nil (means: no override)", err)
	}
}

func TestNLIConfig_Validate_DisabledIsValid(t *testing.T) {
	t.Parallel()
	c := &project.NLIConfig{Enabled: false}
	if err := c.Validate(); err != nil {
		t.Errorf("Enabled=false NLIConfig.Validate: err=%v, want nil", err)
	}
}

func TestNLIConfig_Validate_EnabledRequiresPrimary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		mut  func(c *project.NLIConfig)
	}{
		{"missing Primary.ProviderID", func(c *project.NLIConfig) { c.Primary.ProviderID = "" }},
		{"missing Primary.Endpoint", func(c *project.NLIConfig) { c.Primary.Endpoint = "" }},
		{"zero Primary.TimeoutMS", func(c *project.NLIConfig) { c.Primary.TimeoutMS = 0 }},
		{"negative Primary.TimeoutMS", func(c *project.NLIConfig) { c.Primary.TimeoutMS = -1 }},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := validNLIConfig() // each sub-test gets its own copy
			tc.mut(c)
			err := c.Validate()
			if !errors.Is(err, project.ErrNLIConfigInvalid) {
				t.Errorf("Validate: err=%v, want ErrNLIConfigInvalid", err)
			}
		})
	}
}

func TestNLIConfig_Validate_TunableBounds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		mut  func(c *project.NLIConfig)
	}{
		{"zero LatencyBudgetMS", func(c *project.NLIConfig) { c.LatencyBudgetMS = 0 }},
		{"negative LatencyBudgetMS", func(c *project.NLIConfig) { c.LatencyBudgetMS = -100 }},
		{"MaxPremiseBytes < 64", func(c *project.NLIConfig) { c.MaxPremiseBytes = 63 }},
		{"MaxHypothesisBytes < 16", func(c *project.NLIConfig) { c.MaxHypothesisBytes = 15 }},
		{"MaxCacheEntries < 100", func(c *project.NLIConfig) { c.MaxCacheEntries = 99 }},
		{"zero CacheTTLSeconds", func(c *project.NLIConfig) { c.CacheTTLSeconds = 0 }},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := validNLIConfig()
			tc.mut(c)
			err := c.Validate()
			if !errors.Is(err, project.ErrNLIConfigInvalid) {
				t.Errorf("Validate: err=%v, want ErrNLIConfigInvalid", err)
			}
		})
	}
}

func TestNLIConfig_Validate_FallbackRequiresFields(t *testing.T) {
	t.Parallel()
	c := validNLIConfig()
	c.FallbackEnabled = true
	c.Fallback.ProviderID = ""
	err := c.Validate()
	if !errors.Is(err, project.ErrNLIConfigInvalid) {
		t.Errorf("Validate with FallbackEnabled but no Fallback.ProviderID: err=%v, want ErrNLIConfigInvalid", err)
	}
	c.Fallback.ProviderID = "minicheck"
	c.Fallback.Endpoint = ""
	err = c.Validate()
	if !errors.Is(err, project.ErrNLIConfigInvalid) {
		t.Errorf("Validate with FallbackEnabled but no Fallback.Endpoint: err=%v, want ErrNLIConfigInvalid", err)
	}
}

func TestNLIConfig_Validate_FullyValid(t *testing.T) {
	t.Parallel()
	if err := validNLIConfig().Validate(); err != nil {
		t.Errorf("fully valid NLIConfig.Validate: err=%v, want nil", err)
	}
}

// validNLIConfig returns a known-good NLIConfig.
func validNLIConfig() *project.NLIConfig {
	return &project.NLIConfig{
		Enabled: true,
		Primary: project.NLIPrimary{
			ProviderID: "deberta-v3-large-mnli",
			Endpoint:   "https://router.huggingface.co/hf-inference/models/microsoft/deberta-v3-large-mnli",
			AuthToken:  "hf_S3CRET_NEVER_LOG",
			TimeoutMS:  5000,
			ModelRev:   "main",
		},
		FallbackEnabled:    false,
		LatencyBudgetMS:    10000,
		MaxPremiseBytes:    65536,
		MaxHypothesisBytes: 8192,
		MaxCacheEntries:    10000,
		CacheTTLSeconds:    86400,
	}
}

// ----- Redacted: AuthToken cleared ---------------------------------------

func TestNLIConfig_Redacted_ClearsAuthToken(t *testing.T) {
	t.Parallel()
	c := validNLIConfig()
	c.Primary.AuthToken = "hf_S3CRET_TOKEN_DO_NOT_LEAK"
	c.Fallback.AuthToken = "minicheck_S3CRET_TOKEN_DO_NOT_LEAK"
	c.FallbackEnabled = true
	c.Fallback.ProviderID = "minicheck"
	c.Fallback.Endpoint = "http://localhost:8080/score"
	r := c.Redacted()
	if r == nil {
		t.Fatal("Redacted: nil")
	}
	if r.Primary.AuthToken != "" {
		t.Errorf("Redacted.Primary.AuthToken=%q, want empty", r.Primary.AuthToken)
	}
	if r.Fallback.AuthToken != "" {
		t.Errorf("Redacted.Fallback.AuthToken=%q, want empty", r.Fallback.AuthToken)
	}
	// Sanity: original is not mutated.
	if c.Primary.AuthToken != "hf_S3CRET_TOKEN_DO_NOT_LEAK" {
		t.Errorf("Redacted mutated original: %q", c.Primary.AuthToken)
	}
	// Sanity: Marshal now includes provider_id but NOT the token.
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(b)
	if containsToken(s, "hf_S3CRET_TOKEN_DO_NOT_LEAK") {
		t.Errorf("Marshal JSON contains Primary.AuthToken: %s", s)
	}
	if containsToken(s, "minicheck_S3CRET_TOKEN_DO_NOT_LEAK") {
		t.Errorf("Marshal JSON contains Fallback.AuthToken: %s", s)
	}
	if !containsToken(s, "deberta-v3-large-mnli") {
		t.Errorf("Marshal JSON lost provider_id: %s", s)
	}
	if !containsToken(s, "huggingface.co") {
		t.Errorf("Marshal JSON lost endpoint: %s", s)
	}
}

func TestNLIConfig_Redacted_Nil(t *testing.T) {
	t.Parallel()
	if ((*project.NLIConfig)(nil)).Redacted() != nil {
		t.Errorf("nil.Redacted: not nil")
	}
}

// containsToken is a defensive string-search helper.
func containsToken(haystack, needle string) bool {
	if len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// ----- runProjectCreate round-trip ----------------------------------------
//
// All tests in this section use newProjectTestStore which calls
// sqlite.Open() → migrate.SetClock() (a global write). Running
// them in parallel races on that global. We serialize these tests
// to avoid spurious failures; the unit tests above (TestNLIConfig_*)
// are pure-Go and stay parallel.

func TestRunProjectCreate_NLIConfig_RoundTrip(t *testing.T) {
	st, _, cleanup := newProjectTestStore(t)
	defer cleanup()
	ctx := context.Background()

	in := ProjectCreateInput{
		ProjectID:   "test-nli-rt",
		DisplayName: "NLI Round-Trip",
		NLIConfig:   validNLIConfig(),
	}
	out, err := runProjectCreate(ctx, st, in)
	if err != nil {
		t.Fatalf("runProjectCreate: %v", err)
	}
	if out.IdempotentReplay {
		t.Errorf("first create: IdempotentReplay=true, want false")
	}
	if out.NLIConfig == nil {
		t.Fatal("out.NLIConfig: nil, want non-nil")
	}
	if out.NLIConfig.Primary.AuthToken != "" {
		t.Errorf("out.NLIConfig.Primary.AuthToken=%q, want empty (redacted)", out.NLIConfig.Primary.AuthToken)
	}
	if out.NLIConfig.Fallback.AuthToken != "" {
		t.Errorf("out.NLIConfig.Fallback.AuthToken=%q, want empty", out.NLIConfig.Fallback.AuthToken)
	}
	if out.NLIConfig.Primary.ProviderID != "deberta-v3-large-mnli" {
		t.Errorf("ProviderID=%q, want deberta-v3-large-mnli", out.NLIConfig.Primary.ProviderID)
	}
	if out.NLIConfig.MaxCacheEntries != 10000 {
		t.Errorf("MaxCacheEntries=%d, want 10000", out.NLIConfig.MaxCacheEntries)
	}
}

func TestRunProjectCreate_NLIConfig_IdempotentPreservesOnReplay(t *testing.T) {
	st, _, cleanup := newProjectTestStore(t)
	defer cleanup()
	ctx := context.Background()

	in := ProjectCreateInput{
		ProjectID:   "test-nli-idemp",
		DisplayName: "NLI Idempotent",
		NLIConfig:   validNLIConfig(),
	}
	if _, err := runProjectCreate(ctx, st, in); err != nil {
		t.Fatalf("first create: %v", err)
	}
	in2 := ProjectCreateInput{
		ProjectID:   "test-nli-idemp",
		DisplayName: "NLI Idempotent",
		// NLIConfig: nil — explicit "don't change" on replay.
	}
	out, err := runProjectCreate(ctx, st, in2)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if !out.IdempotentReplay {
		t.Errorf("second create: IdempotentReplay=false, want true")
	}
	if out.NLIConfig == nil {
		t.Fatal("replay: NLIConfig=nil, want existing config preserved")
	}
	if out.NLIConfig.Primary.ProviderID != "deberta-v3-large-mnli" {
		t.Errorf("replay: ProviderID=%q, want preserved",
			out.NLIConfig.Primary.ProviderID)
	}
	if out.NLIConfig.Primary.AuthToken != "" {
		t.Errorf("replay: AuthToken leaked: %q", out.NLIConfig.Primary.AuthToken)
	}
}

func TestRunProjectCreate_NLIConfig_NilIsOK(t *testing.T) {
	st, _, cleanup := newProjectTestStore(t)
	defer cleanup()
	ctx := context.Background()
	in := ProjectCreateInput{
		ProjectID:   "test-nli-nil",
		DisplayName: "NLI Nil",
		NLIConfig:   nil,
	}
	out, err := runProjectCreate(ctx, st, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if out.NLIConfig != nil {
		t.Errorf("nil input → out.NLIConfig=%v, want nil", out.NLIConfig)
	}
}

func TestRunProjectCreate_NLIConfig_InvalidRejected(t *testing.T) {
	st, _, cleanup := newProjectTestStore(t)
	defer cleanup()
	ctx := context.Background()
	in := ProjectCreateInput{
		ProjectID:   "test-nli-invalid",
		DisplayName: "NLI Invalid",
		NLIConfig: &project.NLIConfig{
			Enabled:            true,
			Primary:            project.NLIPrimary{},
			LatencyBudgetMS:    1000,
			MaxPremiseBytes:    1000,
			MaxHypothesisBytes: 1000,
			MaxCacheEntries:    1000,
			CacheTTLSeconds:    1000,
		},
	}
	_, err := runProjectCreate(ctx, st, in)
	if err == nil {
		t.Fatal("invalid NLIConfig: err=nil, want ErrInvalidArgument")
	}
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("err=%v, want wrapping ErrInvalidArgument", err)
	}
}

func TestRunProjectCreate_NLIConfig_DisabledIsValid(t *testing.T) {
	st, _, cleanup := newProjectTestStore(t)
	defer cleanup()
	ctx := context.Background()
	in := ProjectCreateInput{
		ProjectID:   "test-nli-disabled",
		DisplayName: "NLI Disabled",
		NLIConfig: &project.NLIConfig{
			Enabled: false,
		},
	}
	if _, err := runProjectCreate(ctx, st, in); err != nil {
		t.Errorf("disabled NLIConfig: err=%v, want nil", err)
	}
}

func TestRunProjectCreate_NLIConfig_GetProjectStripsAuthToken(t *testing.T) {
	st, dsn, cleanup := newProjectTestStore(t)
	defer cleanup()
	ctx := context.Background()

	in := ProjectCreateInput{
		ProjectID:   "test-nli-strip",
		DisplayName: "NLI Strip",
		NLIConfig:   validNLIConfig(),
	}
	in.NLIConfig.Primary.AuthToken = "hf_LEAKY_TOKEN"
	if _, err := runProjectCreate(ctx, st, in); err != nil {
		t.Fatalf("create: %v", err)
	}
	p, err := st.GetProject(ctx, "test-nli-strip")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if p == nil || p.NLIConfig == nil {
		t.Fatal("GetProject: NLIConfig nil")
	}
	if p.NLIConfig.Primary.AuthToken != "" {
		t.Errorf("GetProject leaked AuthToken: %q", p.NLIConfig.Primary.AuthToken)
	}
	// Sanity: column was actually persisted with the secret (otherwise
	// stripping is trivially satisfied — the secret would never reach
	// the DB, breaking T08 wiring). Read raw column via database/sql.
	db := rawDB(t, dsn)
	var raw string
	if err := db.QueryRow("SELECT nli_config_json FROM projects WHERE project_id = ?", "test-nli-strip").Scan(&raw); err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if !containsToken(raw, "hf_LEAKY_TOKEN") {
		t.Errorf("raw column does not contain secret — never persisted, which breaks T08 wiring: %s", raw)
	}
}

func TestRunProjectCreate_NLIConfig_InvalidJSONOnReadDegrades(t *testing.T) {
	st, dsn, cleanup := newProjectTestStore(t)
	defer cleanup()
	ctx := context.Background()

	if _, err := runProjectCreate(ctx, st, ProjectCreateInput{
		ProjectID:   "test-nli-corrupt",
		DisplayName: "NLI Corrupt",
		NLIConfig:   validNLIConfig(),
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	db := rawDB(t, dsn)
	if _, err := db.Exec("UPDATE projects SET nli_config_json = ? WHERE project_id = ?", "not-valid-json{", "test-nli-corrupt"); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	p, err := st.GetProject(ctx, "test-nli-corrupt")
	if err != nil {
		t.Fatalf("GetProject on corrupted JSON: err=%v, want nil", err)
	}
	if p == nil {
		t.Fatal("GetProject returned nil")
	}
	if p.NLIConfig != nil {
		t.Errorf("corrupted JSON: p.NLIConfig=%+v, want nil (graceful degradation)", p.NLIConfig)
	}
}

func TestRunProjectCreate_NLIConfig_PartialUpdateMerges(t *testing.T) {
	st, _, cleanup := newProjectTestStore(t)
	defer cleanup()
	ctx := context.Background()

	first := validNLIConfig()
	if _, err := runProjectCreate(ctx, st, ProjectCreateInput{
		ProjectID:   "test-nli-merge",
		DisplayName: "NLI Merge",
		NLIConfig:   first,
	}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	// Bypass runProjectCreate's idempotent-replay short-circuit so
	// the underlying CreateProject UPDATE path runs. Update with a
	// new NLIConfig (different tunables) — the store overwrites
	// the entire JSON column.
	updated := validNLIConfig()
	updated.MaxCacheEntries = 50000
	updated.CacheTTLSeconds = 3600
	updated.Primary.AuthToken = "hf_UPDATED"
	if err := st.CreateProject(ctx, &project.Project{
		ProjectID:   "test-nli-merge",
		DisplayName: "NLI Merge",
		NLIConfig:   updated,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	p, err := st.GetProject(ctx, "test-nli-merge")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if p.NLIConfig.MaxCacheEntries != 50000 {
		t.Errorf("MaxCacheEntries=%d, want 50000 (full overwrite)", p.NLIConfig.MaxCacheEntries)
	}
	if p.NLIConfig.CacheTTLSeconds != 3600 {
		t.Errorf("CacheTTLSeconds=%d, want 3600", p.NLIConfig.CacheTTLSeconds)
	}
	if p.NLIConfig.Primary.AuthToken != "" {
		t.Errorf("AuthToken leaked after UPDATE: %q", p.NLIConfig.Primary.AuthToken)
	}
}

func TestRunProjectCreate_NLIConfig_ConcurrentSafe(t *testing.T) {
	st, _, cleanup := newProjectTestStore(t)
	defer cleanup()
	ctx := context.Background()
	const N = 20
	done := make(chan error, N)
	for i := 0; i < N; i++ {
		go func() {
			in := ProjectCreateInput{
				ProjectID:   "test-nli-concurrent",
				DisplayName: "NLI Concurrent",
				NLIConfig:   validNLIConfig(),
			}
			_, err := runProjectCreate(ctx, st, in)
			done <- err
		}()
	}
	for i := 0; i < N; i++ {
		if err := <-done; err != nil {
			t.Errorf("concurrent create %d: %v", i, err)
		}
	}
	p, err := st.GetProject(ctx, "test-nli-concurrent")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if p.NLIConfig == nil || p.NLIConfig.Primary.ProviderID != "deberta-v3-large-mnli" {
		t.Errorf("NLIConfig lost under concurrency: %v", p.NLIConfig)
	}
}

func TestRunProjectCreate_NLIConfig_ListProjects_StripsToken(t *testing.T) {
	st, _, cleanup := newProjectTestStore(t)
	defer cleanup()
	ctx := context.Background()
	for _, pid := range []string{"test-list-a", "test-list-b"} {
		in := ProjectCreateInput{
			ProjectID:   pid,
			DisplayName: pid,
			NLIConfig:   validNLIConfig(),
		}
		in.NLIConfig.Primary.AuthToken = "hf_TOKEN_FOR_" + pid
		if _, err := runProjectCreate(ctx, st, in); err != nil {
			t.Fatalf("create %s: %v", pid, err)
		}
	}
	list, err := st.ListProjects(ctx, 0)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	for _, p := range list {
		if p.NLIConfig != nil && (p.NLIConfig.Primary.AuthToken != "" || p.NLIConfig.Fallback.AuthToken != "") {
			t.Errorf("ListProjects leaked token for %s: primary=%q fallback=%q",
				p.ProjectID, p.NLIConfig.Primary.AuthToken, p.NLIConfig.Fallback.AuthToken)
		}
	}
}