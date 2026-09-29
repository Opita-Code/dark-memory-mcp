// Package mcp is the JSON-RPC transport for dark-memory-v4.
//
// BUG-7 (2026-09-27): the v4-alpha.1 transport wires the
// canonical mcp-go library (mark3labs/mcp-go v0.40.0, pinned by
// the parent module) into a thin wrapper that:
//
//   - constructs an mcp-go MCPServer with the BUG-7 MVP tool set
//     (health_ping, session_start/close/status, agent_memory_save/recall)
//   - drives the JSON-RPC loop on a configurable io.Reader/io.Writer
//     so the transport is testable in-process (production uses
//     os.Stdin/os.Stdout)
//
// The Server type holds shared dependencies (audit writer, session
// store, agent_memory store) so each tool handler is a small
// closure over the *sql.DB rather than re-wiring from scratch.
//
// The MVP tool set is deliberately small (6 tools) — the
// remaining 69 tools (vibe-loop, governance, observability,
// research, security, admin, update) land in subsequent BUG-8
// commits. The transport package structure stays the same; only
// the registration calls in NewServer grow.
package mcp

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/mark3labs/mcp-go/server"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/docs_index"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/judge"
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
	pipeline        *vibe.Pipeline
	judgePipeline   *judge.Pipeline // ADR-007 C2: LLM-backed judge surface
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
		pipeline:         pipe,
		judgePipeline:    judgePipe,
		judgePersonas:    judgePersonas,
		judgeStore:       judgeStore,
		researchExecutor: researchExec,
	}

	// BUG-7 + BUG-8 + C2 + C3 + 10a + PRE-1 C4 + Phase 2 tool set.
	// 29 + 7 judge_util + 3 research + 2 summarize + 1 audit_verify = 42 tools.
	registerHealthTool(s)         // 1
	registerSessionTools(s)       // 5 (start, close, status, resume, heartbeat)
	registerAgentMemoryTools(s)   // 6 (save, recall, list, get, update, archive)
	registerObservabilityTools(s) // 3 (memory_state, writes, anomalies)
	registerErrorObsTools(s)      // 4 (summary, list, get, resolve)
	registerPolicyTools(s)        // 2 (active_policy, load_constitution)
	registerVibeTools(s)          // 4 (spec, publish, pipeline_status, resolve_drift)
	registerJudgeTools(s)         // 4 (judge, consensus, judgment_history, list_personas)
	registerJudgeUtilTools(s)     // 7 (normalize, validate_overrides, pattern_descriptions, verify, verify_hash, trace, validate_trace)
	registerResearchTools(s)      // 3 (topic, recall, resume_thread)
	registerSummarizeTools(s)     // 2 (summarize_session, skill_loaded) — PRE-1 C4
	registerAuditVerifyTool(s)    // 1 (audit_verify) — Phase 2 alpha.15

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
