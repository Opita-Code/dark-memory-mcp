// Package mcp is the JSON-RPC transport for dark-memory-v4.
//
// BUG-7 (2026-09-27): the v4-alpha.1 transport wires the
// canonical mcp-go library (mark3labs/mcp-go v0.40.0, pinned by
// the parent module) into a thin wrapper that:
//
//   - constructs an mcp-go MCPServer with the full v4 tool set
//     (BUG-7 MVP + BUG-8 + ADR-007 C2/C3 + BUG-10 10a + PRE-1 C4
//     + Phase 2 + Phase 3 + Phase 4 Chunks 4.1+4.2 = 46 tools).
//   - drives the JSON-RPC loop on a configurable io.Reader/io.Writer
//     so the transport is testable in-process (production uses
//     os.Stdin/os.Stdout)
//
// The Server type holds shared dependencies (audit writer, session
// store, agent_memory store, project store, etc.) so each tool
// handler is a small closure over the *sql.DB rather than re-wiring
// from scratch.
//
// Tool count progression (2026-09-30):
//   v3.0.0-docfix canonical: 57 tools
//   v4 alpha.16 (Phase 3):    42 tools
//   v4 alpha.17 (Phase 4 +1): 46 tools (project_create,
//                              project_lookup, mindset_apply,
//                              delegate_intent)
package mcp

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/delegation"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/docs_index"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/event"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/judge"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/project"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/research"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/session"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/vibe"
)

// Version is the server's identity string. Set at package init
// from the linked binary's main.Version via ldflags — the parent
// cmd/dark-memory-v4 binary stamps it via:
//
//	go build -ldflags "-X github.com/dark-agents/dark-memory-mcp/internal/v4alpha/transport/mcp.Version=v4-alpha.1"
//
// Until that's wired, it resolves to the sentinel below.
var Version = "v4-alpha.1-dev"

// ServerName is the canonical server name baked into the MCP
// initialize response. The v3 dark-memory-mcp uses
// "dark-memory-mcp"; for v4 the binary is "dark-memory-v4" but
// the server identity preserves the operator-facing "dark-memory"
// prefix so persona harnesses don't break (spec 164 bridge.4
// canonicalNamespaces).
const ServerName = "dark-memory-v4"

// Server holds the MCP server + its dependency graph. Built by
// NewServer, used by ServeStdio.
type Server struct {
	mcpSrv          *server.MCPServer
	db              *sql.DB
	audit           *audit.Writer
	session         *session.Store
	memories        *agent_memory.Store
	projects        *project.Store // Phase 4 Chunk 4.1: namespace primitive
	pipeline        *vibe.Pipeline
	judgePipeline   *judge.Pipeline // ADR-007 C2: LLM-backed judge surface
	llmClient       judge.LLMClient // Phase 7 alpha.19 Chunk 7.1: EXTRACT step in delegate_intent
	extractCache    *delegation.ExtractCache // Phase 7 alpha.19 Chunk 7.1: hoist for cross-call caching
	eventEmitter    *event.DelegationProgressEmitter // Phase 12 T-103c: progress events for delegate_intent pipeline
	judgePersonas   judge.PersonaRegistry
	judgeStore      *judge.Store // ADR-007 C3: sdd_evaluations persistence
	researchExecutor *research.Executor // BUG-10 10a: 17-backend research fan-out
	startedAt       string
}

// NewServer builds the MCPServer, registers the BUG-7 + BUG-8 + C2
// tools, and returns a Server ready for ServeStdio.
//
// The DB must have had CreateSchema called on every v4alpha
// package (audit, session, manifest, vibe, agent_memory). The
// binary layer (cmd/dark-memory-v4/serve.go) calls applyAllSchemas
// before NewServer so this contract is upheld in production.
// Tests construct the DB by hand.
//
// ADR-007 commit 2 (C2): the v4alpha/judge.Pipeline is built with a
// real LLMClient when one or more provider keys are configured (per
// DARK_JUDGE_PROVIDER env), otherwise with NoOpJudge fallback (same
// as commit 1's backwards-compat contract per ADR-007 §10). The
// 4 new judge tools (dark_memory_judge / _consensus /
// _judgment_history / _judge_list_personas) are registered after
// the legacy 25-tool set.
func NewServer(db *sql.DB) (*Server, error) {
	if db == nil {
		return nil, fmt.Errorf("mcp NewServer: db is nil")
	}

	auditW := audit.NewWriter(db)
	sessStore := session.NewStore(db, auditW)
	memStore := agent_memory.NewStore(db, auditW) // ADR-007 C3: INV-1 audit emission

	// Phase 4 Chunk 4.1: namespace primitive (project.Store).
	// INV-1 audit emission on Create via audit.Writer.WriteWithProject.
	// The projects table is created by applyAllSchemas before
	// NewServer is called (cmd/dark-memory-v4/serve.go), so this
	// Store has its schema ready.
	projStore, err := project.NewStore(db, auditW)
	if err != nil {
		return nil, fmt.Errorf("mcp NewServer: project.NewStore: %w", err)
	}

	// Phase 4 Chunk 4.3: wire the project validator into the
	// session Store. Start rejects unknown project_id with
	// ErrUnknownProject (INV-7 hard isolation at the session
	// boundary). The validator is the project.Store itself,
	// adapted via a small adapter (projectStoreValidator) because
	// session.Store's ProjectValidator interface returns (any,
	// error) — keeps session.Store independent of the project
	// package (no import cycle).
	sessStore.SetProjectsForTest(projectStoreValidator{projStore})

	// PRE-1 C2: index operator-facing docs so recall() can
	// find them. Defensive: per-doc failures are logged to
	// stderr; the server still starts in degraded mode.
	// The 5 individual audit_log rows (one per Save/Update)
	// capture the partial state for INV-1 traceability.
	idxRes, idxErr := docs_index.Index(context.Background(), memStore)
	if idxErr != nil {
		fmt.Fprintf(os.Stderr, "docs_index.Index fatal: %v\n", idxErr)
	} else if idxRes != nil && len(idxRes.Errors) > 0 {
		fmt.Fprintf(os.Stderr, "docs_index.Index partial: %d error(s): %v\n",
			len(idxRes.Errors), idxRes.Errors)
	}

	noopJudge := judge.NewNoOpJudge()
	pipe := vibe.NewPipeline(db, auditW, noopJudge)

	// Build the v4 judge pipeline. LLMClient is the real client
	// when env keys are set; nil otherwise (Pipeline fires EC-002
	// with verdict=errored → matches the NoOpJudge contract per
	// ADR-007 §6 backwards compat).
	llmClient, err := buildV4JudgeClient()
	// Phase 7 alpha.19 Chunk 7.1: expose llmClient on Server so the
	// delegate_intent EXTRACT step can call it directly (delegation.Extractor
	// needs an LLMClient for the sub-task decomposition call). Assigned
	// to the Server struct literal below.
	judgePersonas := judge.NewPersonaRegistry()
	judgePipe, err := judge.New(judge.PipelineConfig{
		LLMClient: llmClient,
		Personas:  judgePersonas,
	})
	if err != nil {
		return nil, fmt.Errorf("mcp NewServer: build judge pipeline: %w", err)
	}
	// ADR-007 C3: judge.Store for sdd_evaluations persistence.
	judgeStore := judge.NewStore(db, auditW)

	// BUG-10 10a: research executor (17 backends, SSRF guard,
	// health-aware routing, per-type cache).
	researchHTTP := research.NewHTTPClient(research.DefaultHTTPConfig())
	researchExec := research.NewExecutor(researchHTTP)

	mcpSrv := server.NewMCPServer(
		ServerName,
		Version,
		server.WithToolCapabilities(true),
		server.WithRecovery(),
	)

	s := &Server{
		mcpSrv:           mcpSrv,
		db:               db,
		audit:            auditW,
		session:          sessStore,
		memories:         memStore,
		projects:         projStore,
		pipeline:         pipe,
		judgePipeline:    judgePipe,
		llmClient:        llmClient,            // Phase 7 alpha.19 Chunk 7.1: EXTRACT step
		extractCache:     delegation.NewExtractCache(delegation.CacheTTLFromEnv()), // hoisted for cross-call cache hits
		judgePersonas:    judgePersonas,
		judgeStore:       judgeStore,
		researchExecutor: researchExec,
	}

	// Tool count breakdown (alpha.17, 2026-09-30, 46 tools total):
	//   health                 = 1
	//   session_*              = 5 (start, close, status, resume, heartbeat)
	//   agent_memory_*         = 6 (save, recall, list, get, update, archive)
	//   observability_*        = 3 (memory_state, writes, anomalies)
	//   error_obs_*            = 4 (summary, list, get, resolve)
	//   policy_*               = 2 (active_policy, load_constitution)
	//   vibe_*                 = 4 (spec, publish, pipeline_status, resolve_drift)
	//   judge_*                = 4 (judge, consensus, judgment_history, list_personas)
	//   judge_util_*           = 7 (normalize, validate_overrides, pattern_descriptions, verify, verify_hash, trace, validate_trace)
	//   research_*             = 3 (topic, recall, resume_thread)
	//   summarize_*            = 2 (summarize_session, skill_loaded) — PRE-1 C4
	//   audit_verify           = 1 — Phase 2 alpha.15
	//   project_* (NEW)        = 2 (create, lookup) — Phase 4 Chunk 4.2
	//   mindset_apply (NEW)    = 1 — Phase 4 Chunk 4.2 STUB
	//   delegate_intent (NEW)  = 1 — Phase 4 Chunk 4.2 STUB
	//   TOTAL                  = 46 tools
	registerHealthTool(s)         // 1
	registerSessionTools(s)       // 5
	registerAgentMemoryTools(s)   // 6
	registerObservabilityTools(s) // 3
	registerErrorObsTools(s)      // 4
	registerPolicyTools(s)        // 2
	registerVibeTools(s)          // 4
	registerJudgeTools(s)         // 4
	registerJudgeUtilTools(s)     // 7
	registerResearchTools(s)      // 3
	registerSummarizeTools(s)     // 2 — PRE-1 C4
	registerAuditVerifyTool(s)    // 1 — Phase 2 alpha.15
	registerProjectTools(s)       // 2 — Phase 4 Chunk 4.2
	registerMindsetTools(s)       // 1 — Phase 4 Chunk 4.2 STUB
	registerDelegationTools(s)     // 1 — Phase 4 Chunk 4.2 STUB

	return s, nil
}

// buildV4JudgeClient wraps the env-aware RealLLMClient builder. When
// no provider key is configured, returns nil so the Pipeline fires
// EC-002 (LLM unavailable) — the same contract as NoOpJudge (per
// ADR-007 §6 backwards compat: v4-alpha.1 callers see the same
// "errored verdict, no panic" behavior).
func buildV4JudgeClient() (judge.LLMClient, error) {
	c, err := judge.NewRealLLMClient()
	if err != nil {
		// No key configured → return nil (EC-002 short-circuit).
		// Log nothing (the operator may intentionally run without
		// an LLM in tests or air-gapped environments).
		return nil, nil
	}
	return c, nil
}

// ServeStdio runs the JSON-RPC loop on the provided reader/writer.
// Production: pass os.Stdin / os.Stdout. Tests: pass bytes.Buffer /
// strings.Reader for in-process control.
//
// Returns nil on clean shutdown (ctx cancelled or stdin EOF).
//
// Worker pool is configured to SIZE 1 (serial) so tool calls
// execute in the order they're received. The default mcp-go
// worker pool runs them in parallel, which races against the
// read-after-write contract that FTS5 recall depends on
// (save + recall in one batch would otherwise see inconsistent
// state). Single-worker keeps the protocol simple at the cost
// of throughput, which is acceptable for v4-alpha.1 (operator-
// facing CLI, not a high-throughput service).
//
// We apply StdioOptions manually because Listen doesn't take
// them; only the package-level ServeStdio does (and that one
// hardcodes os.Stdin/os.Stdout). The option functions are
// exported, so applying them by hand is the documented escape
// hatch.
func (s *Server) ServeStdio(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	stdioSrv := server.NewStdioServer(s.mcpSrv)
	server.WithWorkerPoolSize(1)(stdioSrv)
	server.WithQueueSize(64)(stdioSrv)
	return stdioSrv.Listen(ctx, stdin, stdout)
}

// runtimeInfo is the structured response of health_ping. Defined
// here (not in health.go) because other tools echo fields like
// version + go_version into their response for diagnostic
// friendliness.
type runtimeInfo struct {
	ServerName    string `json:"server_name"`
	ServerVersion string `json:"server_version"`
	GoVersion     string `json:"go_version"`
	SchemaVersion string `json:"schema_version"`
}

// goRuntimeVersion is captured at package init for fast access.
var goRuntimeVersion = runtime.Version()

// --- test seams (Phase 4 Chunk 4.2) ---
//
// These methods exist ONLY so external test files (project_test.go
// in package mcp_test) can wire a Server with the minimum state
// needed to exercise the project/mindset/delegation handlers. They
// are not part of the public API; production code MUST use
// NewServer instead.

// SetProjectsForTest injects a *project.Store into the Server.
// Used by tests that need the project handlers (project_create,
// project_lookup) without spinning up the full dependency graph
// (researchExecutor, judgePipeline, etc.).
func (s *Server) SetProjectsForTest(p *project.Store) {
	s.projects = p
}

// projectStoreValidator adapts *project.Store to the
// session.ProjectValidator interface. Lets session.Store validate
// project_id existence without importing project directly. Returns
// (any, error) — session.Store ignores it (just checks err != nil
// for ErrProjectNotFound, which the adapter passes through).
//
// Lives here (transport/mcp) because that's where both deps are
// already imported. Inline (not in a separate file) because it's a
// 5-line adapter.
type projectStoreValidator struct {
	s *project.Store
}

// Lookup delegates to project.Store.Lookup. The *project.Project value
// is returned as `any` (interface signature is loose-typed by
// design — session.Store only cares about the error).
func (p projectStoreValidator) Lookup(ctx context.Context, projectID string) (any, error) {
	proj, err := p.s.Lookup(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return proj, nil
}

// ProjectsForTest returns the wired *project.Store. Returns nil
// when NewServer has not been called (e.g., in tests that
// construct a Server with only SetProjectsForTest).
func (s *Server) ProjectsForTest() *project.Store {
	return s.projects
}

// HandleMindsetApplyForTest returns the unexported handler
// registered for dark_memory_mindset_apply. Tests invoke it
// directly via callHandler (skips the mcp-go transport overhead).
func (s *Server) HandleMindsetApplyForTest() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return s.handleMindsetApply
}

// HandleDelegateIntentForTest returns the unexported handler
// registered for dark_memory_delegate_intent.
func (s *Server) HandleDelegateIntentForTest() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return s.handleDelegateIntent
}

// SetLLMClientForTest injects a judge.LLMClient into the Server. Used
// by Chunk 7.1 (delegate_intent EXTRACT) tests that wire a FakeLLM
// without spinning up the full HTTP-based RealLLMClient.
func (s *Server) SetLLMClientForTest(c judge.LLMClient) {
	s.llmClient = c
}

// LLMClientForTest returns the wired LLMClient (nil when not set).
func (s *Server) LLMClientForTest() judge.LLMClient {
	return s.llmClient
}

// SetMemoriesForTest injects an agent_memory.Store. Used by Chunk 7.1
// tests that need the persistent delegation cache wired.
func (s *Server) SetMemoriesForTest(m *agent_memory.Store) {
	s.memories = m
}

// MemoriesForTest returns the wired *agent_memory.Store. Returns nil
// when SetMemoriesForTest has not been called.
func (s *Server) MemoriesForTest() *agent_memory.Store {
	return s.memories
}

// SetExtractCacheForTest injects a delegation.ExtractCache. Used by
// Chunk 7.1 tests that need the EXTRACT path's cache layer wired
// (without this, s.extractCache is nil and every call is a cache miss).
func (s *Server) SetExtractCacheForTest(c *delegation.ExtractCache) {
	s.extractCache = c
}

// WithEventEmitter injects a *event.DelegationProgressEmitter for
// Phase 12 T-103c. When non-nil, the delegate_intent pipeline emits
// 1 root + 4 child progress events through the DECIDE→EXTRACT→MIND
// →CURATE→COMPLETED lifecycle. When nil, the pipeline runs without
// emitting events (backward compat for harnesses that haven't wired
// the emitter yet). Returns the server for chaining.
func (s *Server) WithEventEmitter(e *event.DelegationProgressEmitter) *Server {
	s.eventEmitter = e
	return s
}

// NewExtractCacheForTest returns a fresh in-memory ExtractCache for use
// in tests. Mirrors delegation.NewExtractCache but is exposed here so
// the test seam doesn't force callers to import the delegation package.
func NewExtractCacheForTest() *delegation.ExtractCache {
	return delegation.NewExtractCache(delegation.DefaultCacheTTL)
}
