// legacy_main.go — the pre-v2.19.0 single-binary dark-memory MCP
// server. Invoked from main.go when DARK_MEM_BRIDGE=0.
//
// Identical to the v2.18.0 main.go body (preserved here verbatim
// so the dispatcher can call it). Splits main.go (which is small
// and only routes) from the heavy legacy server boot (which
// imports the orchestrator + 49 tools).

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/dark-agents/dark-memory-mcp/internal/drift"
	"github.com/dark-agents/dark-memory-mcp/internal/errorobs"
	"github.com/dark-agents/dark-memory-mcp/internal/federation"
	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/server"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/tools"
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
	frameSrc, err := tools.RegisterAll(srv.Registry(), bootState.Orchestrator, bootState.Store, safetyFP)
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
