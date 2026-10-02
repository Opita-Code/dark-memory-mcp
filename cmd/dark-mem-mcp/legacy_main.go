// legacy_main.go — the dark-mem-mcp single-binary MCP server boot.
// Invoked from main.go. Pre-v2.19.0 this lived in main.go; the
// v2.19.0 dispatcher→bridge→daemon split is removed from active
// use (spec 1176 §4.10 not implemented; the daemon never wired
// initialize/tools/call), so the legacy path is now the single
// boot path.
//
// Boot sequence: server.New → register MCP research backend →
// set runtime context → wire v4alpha delegate_intent deps (alpha.20
// Chunk 8.1) → wire tools + drift gate (M6) + federation
// peer + sweeper → startup-recover → ServeStdio. See
// ARCHITECTURE.md §boot path for the multi-bin timeline.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/drift"
	"github.com/dark-agents/dark-memory-mcp/internal/embedder"
	"github.com/dark-agents/dark-memory-mcp/internal/errorobs"
	"github.com/dark-agents/dark-memory-mcp/internal/federation"
	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/server"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/tools"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	v4delegation "github.com/dark-agents/dark-memory-mcp/internal/v4alpha/delegation"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/judge"
)

// legacyMain is invoked from main(). It is the v2.18.0-and-earlier
// single-binary dark-memory MCP boot sequence. Kept verbatim here
// so the dispatcher in main.go can route to it without duplicating
// 200+ lines of boot logic.
func legacyMain() {
	// Review-w4-002: panic recovery at the boot layer.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "dark-mem-mcp: panic during boot: %v\n%s\n", r, debug.Stack())
			os.Exit(1)
		}
	}()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Phase 10 Chunk 10.1: file-flag reload watcher (Windows-compatible
	// equivalent of SIGHUP). SIGHUP doesn't exist on Windows; instead
	// the operator (or a deploy script) touches <exe>.reload in the
	// binary's directory. The watcher polls every 2s, sees the flag,
	// removes it, and calls cancel() to trigger graceful shutdown.
	// The harness (opencode) then respawns the binary at the canonical
	// path — which is the freshly atomic-renamed version. Per the
	// Phase 10 deploy doc (docs/deploy-mcp-binary.md), the full
	// deploy cycle is: build → rename → touch flag → wait → verify.
	// The watcher's poll cost is one os.Stat per 2s (~10us); the
	// pre-existing 30s sweeper tick dominates the boot loop's
	// overhead so this is in the noise.
	go watchReloadFlag(ctx, cancel)

	srv, err := server.New(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: server.New failed: %v\n", err)
		os.Exit(1)
	}
	bootState := srv.BootState()

	if rb := orchestration.NewMCPResearchBackend(); rb != nil {
		bootState.Orchestrator.WithBackends(rb)
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: research backend registered: %s (bin=%s)\n", rb.Name(), rb.BinPath)
	} else {
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: research backend NOT registered (dark-research-mcp binary not found; set DARK_RESEARCH_MCP_BIN)\n")
	}
	defer bootState.StopSweeper()
	defer srv.Close()
	// v2.20.0 (spec 1188): the default failover chain + background
	// health registry start lazily on first judge call; ensure the
	// OS-keyring migration + health loop are initialized at boot and
	// torn down on exit.
	defer orchestration.ShutdownDefaultLLM()

	tools.SetRuntimeContext(tools.RuntimeContext{
		BootedAt:         bootState.Config.BootedAt,
		ServerVersion:    bootState.Config.ServerVersion,
		ServerName:       bootState.Config.ServerName,
		CoexistenceGroup: bootState.Config.CoexistenceGroup,
		DriverLabel:      string(bootState.Config.DBDriver),
		DSNPath:          bootState.Config.DBDSN,
	})

	safetyFP := &store.SafetyHolder{
		SetCanary:       func(string) {},
		Active:          func() string { return string(bootState.Safety.Active()) },
		ValidatePayload: func(payload string) error { return bootState.Safety.ValidatePayload(payload) },
	}
	// Phase 9 alpha.20 Chunk 8.1: wire v4alpha delegate_intent deps
	// so dark_memory_delegate_intent runs the v4alpha DECIDE→EXTRACT→
	// MIND→CURATE pipeline (alpha.19 Chunk 7.1). The deps are:
	//   - v4judge.LLMClient: from env keys (same as the v4alpha Server
	//     boot); nil when no provider key is configured.
	//   - ExtractCache: hoisted for cross-call cache hits
	//     (DARK_DELEGATION_CACHE_TTL env or default 1h).
	//   - agent_memory.Store: for CURATE subagent binding rows
	//     (kind=link, tag=subagent:v1, Chunk 7.2). NOTE: the v3
	//     Store interface deliberately hides *sql.DB, so the v3
	//     binary cannot construct v4alpha's agent_memory.Store
	//     without an interface change. We leave Memories=nil for
	//     the v3 binary in Chunk 8.1; CURATE becomes a no-op
	//     (subagent_id empty in subtasks). Chunk 8.1.1 (or a
	//     later alpha.20 chunk) will add Store.RawDB() so CURATE
	//     is fully wired.
	// Operators without an LLM key can roll back to v2 with
	// DARK_DELEGATION_BACKEND=v2.
	v4LLM, v4LLMErr := judge.NewRealLLMClient()
	if v4LLMErr != nil {
		// No provider key — fall through. The v4alpha adapter
		// will surface needs_human with llm_network_error
		// alternatives. Operators without keys should set
		// DARK_DELEGATION_BACKEND=v2 to use the deterministic
		// router. We log once and continue.
		fmt.Fprintf(os.Stderr,
			"dark-mem-mcp: v4alpha delegate_intent LLM not configured (%v); "+
				"EXTRACT will surface needs_human. Set DARK_DELEGATION_BACKEND=v2 "+
				"for the deterministic router.\n", v4LLMErr)
	}
	_ = audit.Writer{}     // referenced for compile-time dep (CURATE follow-up)
	_ = agent_memory.Store{} // ditto.
	v4DelegateBackend := &tools.DelegateIntentBackend{
		LLMClient:    v4LLM,
		ExtractCache: v4delegation.NewExtractCache(v4delegation.CacheTTLFromEnv()),
		Memories:     nil, // CURATE no-op in v3 binary until Store.RawDB() lands
	}
	// Phase 9 alpha.20 Chunk 8.2: enable v4alpha personas in the lazy
	// PersonaRegistry. After this call, dark_memory_judge_list_personas
	// returns 14 personas (8 v2 compiled + 6 v4alpha) instead of 8.
	// The registry is read-only after the first construction; calling
	// this BEFORE the first persona resolution (i.e., before any
	// judge call) ensures the 6 v4-new entries are included.
	bootState.Orchestrator.WithV4AlphaPersonas(true)
	// Phase 9 alpha.20 Chunk 8.3: wire the embedder at boot.
	// FactoryAuto walks the ladder (manual → harness → ONNX → OPENAI
	// → stub); Store.WithEmbedder records the chosen backend so
	// SearchAgentMemory Mode=vector|rrf has a real embedder to call.
	// Without this, the Store always boots with embedder.None()
	// and Mode=vector returns embedder.ErrDisabled → falls back to
	// BM25-only. Now: any operator with DARK_MEMORY_EMBEDDER set,
	// or Ollama running on localhost:11434, or bundled ONNX loadable,
	// gets hybrid retrieval for free.
	embedderKind := embedder.FactoryAuto().Kind()
	bootState.Store.WithEmbedder(embedder.FactoryAuto())
	fmt.Fprintf(os.Stderr,
		"dark-mem-mcp: embedder kind=%q dim=%d (DARK_MEMORY_EMBEDDER=%q)\n",
		embedderKind,
		bootState.Store.Embedder().Dim(),
		os.Getenv("DARK_MEMORY_EMBEDDER"))
	frameSrc, err := tools.RegisterAllWithDeps(srv.Registry(), bootState.Orchestrator, bootState.Store, safetyFP, v4DelegateBackend)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: tools.RegisterAll failed: %v\n", err)
		os.Exit(1)
	}

	activeSessionResolver := server.NewStoreBackedActiveSessionResolver(
		server.StoreBackedLookup(bootState.Store),
	)
	bootState.Orchestrator.OnActiveSessionChanged = activeSessionResolver.Invalidate

	// t4 (spec 1242, M6): wire the drift-at-write interceptor.
	// Resolution order: Project.DriftStrictness override, else
	// DARK_DRIFT_STRICTNESS env, else StrictnessOff (skip —
	// pre-wiring behavior preserved).
	strictness := drift.StrictnessFromEnv()
	if proj, err := bootState.Store.GetProject(ctx, bootState.Store.ActiveProject()); err == nil && proj != nil {
		strictness = drift.ResolveStrictness(proj.DriftStrictness, strictness, nil)
	}
	driftChecker := drift.NewChecker(bootState.Store, server.DriftJudgeFromOrchestrator(bootState.Orchestrator), strictness)

	bootState.Gate = &server.GateMiddleware{
		FrameSource:        frameSrc,
		DriftChecker:       driftChecker,
		ActiveSession:      activeSessionResolver,
		ActiveProject:      bootState.Store.ActiveProject,
		ActiveConstitution: func() (string, string) { return bootState.Config.ConstitutionID, bootState.Config.ConstitutionVer },
		RecordRefusal: func(ctx context.Context, toolName, sessionID, code, message string) {
			bootState.Orchestrator.RecordError(ctx, toolName, sessionID,
				fmt.Errorf("gate refusal %s: %s", code, message), errorobs.SeverityWarn)
		},
	}

	peer, err := federation.NewPeerFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: federation peer init failed: %v\n", err)
		os.Exit(1)
	}
	tools.SetFederationPeer(peer)
	defer func() {
		if peer != nil {
			_ = peer.Close()
		}
	}()
	if peer != nil {
		tools.RegisterFederation(srv.Registry())
	}
	if err := srv.RegisterAll(); err != nil {
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: server.RegisterAll failed: %v\n", err)
		os.Exit(1)
	}

	if err := bootState.StartSweeper(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: sweeper start failed: %v\n", err)
		os.Exit(1)
	}

	runStartupRecoverLegacy(ctx, bootState.Orchestrator)

	// v2.20.0 (spec 1188 T6): warm up the default failover chain at
	// boot (migrates env keys into the OS keyring + starts the
	// background health loop). Best-effort — a missing key is not a
	// boot failure.
	if _, llmErr := orchestration.DefaultFailoverClient(); llmErr != nil {
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: LLM failover init warning: %v\n", llmErr)
	}

	if err := srv.ServeStdio(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: ServeStdio failed: %v\n", err)
		os.Exit(1)
	}
}

// runStartupRecoverLegacy detects a closed_aborted session from a
// prior harness. Split from dispatcher.go to keep the legacy main
// self-contained.
func runStartupRecoverLegacy(ctx context.Context, orch *orchestration.Orchestrator) {
	operator := os.Getenv("DARK_OPERATOR")
	if operator == "" {
		operator = "dark-agent"
	}
	recoverOut, err := orch.SessionRecover(ctx, orchestration.SessionRecoverInput{
		Operator: operator,
		Lookback: "24h",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: startup-recover failed: %v\n", err)
		return
	}
	if recoverOut == nil || !recoverOut.Found {
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: startup-recover ok (no candidate)\n")
		return
	}
	candidate := recoverOut.Candidate
	fmt.Fprintf(os.Stderr,
		"dark-mem-mcp: startup-recover found candidate_session_id=%s operator=%s\n",
		candidate.SessionID, candidate.Operator)

	if os.Getenv("DARK_AUTO_RESURRECT") != "on_boot" {
		fmt.Fprintf(os.Stderr,
			"dark-mem-mcp: set DARK_AUTO_RESURRECT=on_boot to auto-resurrect\n")
		return
	}
	resOut, err := orch.SessionResurrect(ctx, orchestration.SessionResurrectInput{
		OriginalSessionID: candidate.SessionID,
		Reason:            "auto_resurrect_on_boot",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: auto-resurrect failed: %v\n", err)
		return
	}
	_ = resOut // suppress unused warning
}

// watchReloadFlag polls for a flag file at <exe>.reload every 2s
// and triggers graceful shutdown when it appears. This is the
// Phase 10 Chunk 10.1 Windows-compatible equivalent of a SIGHUP
// graceful re-exec — SIGHUP doesn't exist on Windows, so we use a
// filesystem sentinel instead.
//
// The deploy procedure (docs/deploy-mcp-binary.md §3) is:
//
//  1. Build new binary as `bin/dark-mem-mcp.exe.new`.
//  2. Atomic rename: `bin/dark-mem-mcp.exe` → `bin/dark-mem-mcp.exe.bak-<ts>`,
//     then `bin/dark-mem-mcp.exe.new` → `bin/dark-mem-mcp.exe`.
//  3. Touch `bin/dark-mem-mcp.exe.reload`.
//  4. Within 2s, the watcher sees the flag, removes it, and calls
//     cancel() — the existing SIGINT/SIGTERM handler runs and
//     dark-mem-mcp exits gracefully.
//  5. The opencode harness respawns the binary at the canonical
//     path. The new binary is the freshly-renamed version. Total
//     disconnect window: 2s (poll interval) + ~3s (boot) = ~5s.
//
// The watcher is idempotent (the os.Remove on the flag is best-
// effort; a duplicate touch is harmless). The 2s poll is the
// minimum that gives the operator a reasonable deploy latency
// without measurable CPU overhead (one os.Stat per 2s ≈ 10µs,
// dwarfed by the 30s sweeper tick).
func watchReloadFlag(ctx context.Context, cancel context.CancelFunc) {
	exe, err := os.Executable()
	if err != nil {
		// os.Executable failed — fall back to ARGV[0].
		exe = os.Args[0]
	}
	flagPath := exe + ".reload"
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, statErr := os.Stat(flagPath); statErr == nil {
				fmt.Fprintf(os.Stderr,
					"dark-mem-mcp: reload flag %s found, triggering graceful shutdown for harness respawn\n",
					flagPath)
				// Best-effort flag cleanup. If Remove fails, a
				// second deploy will see the same flag and
				// self-trigger — annoying but harmless.
				_ = os.Remove(flagPath)
				cancel()
				return
			}
		}
	}
}
