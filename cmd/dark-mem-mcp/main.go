// Package main is the dark-mem-mcp binary — the MCP server that exposes
// 26 dark_memory_* tools backed by the dark-memory-mcp library's
// orchestrators. See ../internal/server/server.go for boot + lifecycle
// logic; ../internal/tools/*.go for the per-namespace tool handlers.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/dark-agents/dark-memory-mcp/internal/federation"
	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/server"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/tools"
)

func main() {
	// Review-w4-002: panic recovery at the boot layer. mcp-go's
	// WithRecovery() only catches panics INSIDE tool handlers (per its
	// own docs). A panic during server.New or tools.RegisterAll would
	// still crash the binary with a stack trace, killing the opencode
	// subprocess and leaving the harness without a coherent error.
	// The CLI binary has the same protection; mirror it here.
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
	// Stop the sweeper BEFORE srv.Close() — order matters per
	// lifecycle.Shutdown step ordering.
	defer bootState.StopSweeper()
	defer srv.Close()

	// v1.3.0: install the boot-time metadata that
	// dark_memory_health_ping reads (server version, name,
	// coexistence group, driver label, DSN path) BEFORE the
	// tool registry is built so the first health_ping call
	// already reports the correct values. Installing AFTER
	// RegisterAll is also OK (SetRuntimeContext uses atomic
	// stores; subsequent health_pings see the new values) but
	// doing it before is cleaner.
	tools.SetRuntimeContext(tools.RuntimeContext{
		BootedAt:         bootState.Config.BootedAt,
		ServerVersion:    bootState.Config.ServerVersion,
		ServerName:       bootState.Config.ServerName,
		CoexistenceGroup: bootState.Config.CoexistenceGroup,
		DriverLabel:      string(bootState.Config.DBDriver),
		DSNPath:          bootState.Config.DBDSN,
	})

	// Register all 29 tools in canonical order (BRIDGE_AND_COEXISTENCE.md
	// §3 / spec 164 bridge.4 + DMAP v1.1 spec 193 Layer 6). RegisterAll
	// returns an error if any of the 29 expected tools is missing from
	// the registry — fail fast. v1.3.0: health_ping added to OBSERVABILITY
	// (3 → 4 tools); canonical count is now 28. 5A.ii.b.2.c: dark_memory_recall
	// added to CONTEXT (4 → 5 tools); canonical count is now 29.
	//
	// Safety is converted from *safety.Holder to *store.SafetyHolder
	// (function-pointer adapter) so dark_memory_recall can thread
	// canary state to composed IdentityFrame.CanaryActive.
	safetyFP := &store.SafetyHolder{
		SetCanary:       func(string) {},
		Active:          func() string { return string(bootState.Safety.Active()) },
		ValidatePayload: func(payload string) error { return bootState.Safety.ValidatePayload(payload) },
	}
	if err := tools.RegisterAll(srv.Registry(), bootState.Orchestrator, bootState.Store, safetyFP); err != nil {
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: tools.RegisterAll failed: %v\n", err)
		os.Exit(1)
	}

	// F7 federation peer: opt-in cross-namespace lookup against the
	// dark-research DB. Read-only. Boot fails if DARK_FEDERATION_PEER_DSN
	// points to a DB without vibe_artifacts + vibe_drift_reports tables
	// (we validate at startup so a misconfiguration doesn't silently
	// disable the federation lookup at request time).
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
	// Register the federation_lookup tool ONLY when the peer is enabled.
	// Same opt-in pattern as DARK_REDTEAM=armed (redteam extras). When
	// DARK_FEDERATION_PEER_DSN is unset, the surface stays at 28 canonical
	// tools and the conformance test `TestBridge7_ListToolsCanonical`
	// continues to pass.
	if peer != nil {
		tools.RegisterFederation(srv.Registry())
	}
	if err := srv.RegisterAll(); err != nil {
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: server.RegisterAll failed: %v\n", err)
		os.Exit(1)
	}

	// Wave 5E.iii: start the INV-9 sweeper AFTER RegisterAll (so the
	// tool registry is fully populated when the first MCP request can
	// race the first tick) but BEFORE ServeStdio (so the boot_reconcile
	// log line appears in the boot sequence, not after traffic starts).
	if err := bootState.StartSweeper(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: sweeper start failed: %v\n", err)
		os.Exit(1)
	}

	// Wave 5E.v (L6 adapter integration, startup-recover hook per
	// BRIDGE_AND_COEXISTENCE.md §6): detect a closed_aborted session
	// from a prior harness and surface it to the operator BEFORE MCP
	// traffic starts. Read-only by default — auto-resurrection gated
	// behind DARK_AUTO_RESURRECT=on_boot (operator opt-in).
	runStartupRecover(ctx, bootState.Orchestrator)

	if err := srv.ServeStdio(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: ServeStdio failed: %v\n", err)
		os.Exit(1)
	}
}

// runStartupRecover implements the startup-recover L6 hook. Detects
// the most-recent closed_aborted session for the operator, logs a
// discoverable line, and (when DARK_AUTO_RESURRECT=on_boot) auto-calls
// SessionResurrect to revive it.
//
// Operator env vars:
//
//   - DARK_OPERATOR         (default "dark-agent"): the operator id
//     to recover for. Set to the harness's notion of "current user".
//   - DARK_AUTO_RESURRECT   (default off): when set to "on_boot",
//     auto-call SessionResurrect on the detected candidate. Without
//     this flag, the candidate is just LOGGED for the operator.
//
// Failure to recover is non-fatal — we log and continue. The harness
// still boots; the operator can call dark_memory_session_recover +
// dark_memory_session_resurrect manually if needed.
func runStartupRecover(ctx context.Context, orch *orchestration.Orchestrator) {
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
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: startup-recover ok (no candidate for operator=%s lookback=24h)\n", operator)
		return
	}
	candidate := recoverOut.Candidate
	fmt.Fprintf(os.Stderr,
		"dark-mem-mcp: startup-recover found candidate_session_id=%s operator=%s closed_at=%s requires_consent=%v\n",
		candidate.SessionID, candidate.Operator, candidate.ClosedAt, recoverOut.RequiresConsent)

	if os.Getenv("DARK_AUTO_RESURRECT") != "on_boot" {
		fmt.Fprintf(os.Stderr,
			"dark-mem-mcp: startup-recover hint: invoke dark_memory_session_resurrect(original_session_id=%q) to revive, or set DARK_AUTO_RESURRECT=on_boot for automatic recovery\n",
			candidate.SessionID)
		return
	}
	resOut, err := orch.SessionResurrect(ctx, orchestration.SessionResurrectInput{
		OriginalSessionID: candidate.SessionID,
		Reason:            "auto_resurrect_on_boot",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: startup-recover auto-resurrect failed: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr,
		"dark-mem-mcp: startup-recover auto-resurrected new_session_id=%s chain_len=%d constitution_bumped=%v\n",
		resOut.NewSessionID, resOut.ResurrectChainLen, resOut.ConstitutionBumped)
}
