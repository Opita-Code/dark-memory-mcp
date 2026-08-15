// Command dark-mem-mcp-daemon is the long-lived server process
// (spec 1176, v2.19.0). It listens on a Unix socket / Windows named
// pipe, owns the SQLite connection, and serves the full dark-memory
// MCP surface over that socket (spec 1176 §4.10 MCP-over-socket).
//
// Usage:
//
//	dark-mem-mcp-daemon [flags]
//	--socket  PATH  Unix socket or \\.\pipe\ path (default: ~/.dark-agents/daemon.sock)
//	--ready   PATH  ready-file path (default: same dir as socket)
//	--idle    DUR   idle timeout (default: 30m, format: 30m/1h/90s)
//	--version VER   version string returned in pong (default: v2.19.0)
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/daemon"
	"github.com/dark-agents/dark-memory-mcp/internal/errorobs"
	"github.com/dark-agents/dark-memory-mcp/internal/federation"
	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/server"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/tools"
	"github.com/dark-agents/dark-memory-mcp/internal/version"
)

func main() {
	// Panic recovery at the boot layer (mirrors legacy_main.go).
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "dark-mem-mcp-daemon: panic during boot: %v\n%s\n", r, debug.Stack())
			os.Exit(1)
		}
	}()

	socketPath := flag.String("socket", daemon.DefaultSocketPath(), "Unix socket or named-pipe path")
	readyPath := flag.String("ready", daemon.DefaultReadyPath(), "ready-file path")
	pidPath := flag.String("pid", daemon.DefaultPIDPath(), "PID file path (Unix only)")
	idle := flag.Duration("idle", 30*time.Minute, "idle timeout before self-shutdown")
	ver := flag.String("version", "v"+version.Resolve().Version, "version string")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("dark-mem-mcp-daemon: starting (socket=%s ready=%s pid=%s idle=%s)",
		*socketPath, *readyPath, *pidPath, *idle)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Build a real dark-memory Server to back the daemon. This is the
	// SAME boot sequence as the single-binary mode (legacy_main.go);
	// we run the server behind the socket instead of stdio.
	srv, err := server.New(ctx)
	if err != nil {
		log.Fatalf("dark-mem-mcp-daemon: server.New: %v", err)
	}
	defer srv.Close()
	bootState := srv.BootState()
	// v2.20.0 (spec 1188 T6): default failover chain + background
	// health registry; torn down on exit.
	defer orchestration.ShutdownDefaultLLM()

	// Research backend (same wiring as the single-binary path).
	if rb := orchestration.NewMCPResearchBackend(); rb != nil {
		bootState.Orchestrator.WithBackends(rb)
		fmt.Fprintf(os.Stderr, "dark-mem-mcp-daemon: research backend registered: %s (bin=%s)\n", rb.Name(), rb.BinPath)
	} else {
		fmt.Fprintf(os.Stderr, "dark-mem-mcp-daemon: research backend NOT registered (dark-research-mcp binary not found; set DARK_RESEARCH_MCP_BIN)\n")
	}
	defer bootState.StopSweeper()

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
		log.Fatalf("dark-mem-mcp-daemon: tools.RegisterAll: %v", err)
	}

	activeSessionResolver := server.NewStoreBackedActiveSessionResolver(
		server.StoreBackedLookup(bootState.Store),
	)
	bootState.Orchestrator.OnActiveSessionChanged = activeSessionResolver.Invalidate

	bootState.Gate = &server.GateMiddleware{
		FrameSource:   frameSrc,
		DriftChecker:  nil,
		ActiveSession: activeSessionResolver,
		ActiveProject: bootState.Store.ActiveProject,
		ActiveConstitution: func() (string, string) { return bootState.Config.ConstitutionID, bootState.Config.ConstitutionVer },
		RecordRefusal: func(ctx context.Context, toolName, sessionID, code, message string) {
			bootState.Orchestrator.RecordError(ctx, toolName, sessionID,
				fmt.Errorf("gate refusal %s: %s", code, message), errorobs.SeverityWarn)
		},
	}

	peer, err := federation.NewPeerFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "dark-mem-mcp-daemon: federation peer init failed: %v\n", err)
	} else {
		tools.SetFederationPeer(peer)
		defer func() {
			_ = peer.Close()
		}()
		if peer != nil {
			tools.RegisterFederation(srv.Registry())
		}
	}

	// Register the populated Registry into the mcp-go MCPServer. This
	// is the surface ServeStream serves over each socket connection.
	if err := srv.RegisterAll(); err != nil {
		log.Fatalf("dark-mem-mcp-daemon: server.RegisterAll: %v", err)
	}

	if err := bootState.StartSweeper(ctx); err != nil {
		log.Fatalf("dark-mem-mcp-daemon: sweeper start: %v", err)
	}

	// v2.20.0 (spec 1188 T6): warm up the failover chain (keyring
	// migration + health loop). Best-effort.
	if _, llmErr := orchestration.DefaultFailoverClient(); llmErr != nil {
		fmt.Fprintf(os.Stderr, "dark-mem-mcp-daemon: LLM failover init warning: %v\n", llmErr)
	}

	// requestHandler bridges the line-delimited JSON protocol to the
	// in-process MCP server. Since spec 1176 §4.10 the daemon serves
	// the FULL MCP surface over the socket via OnConn
	// (MCP-over-socket); this Frame-based handler remains as a
	// compatibility fallback for ping + tools/list and internal
	// tooling that speaks the Frame protocol directly.
	requestHandler := func(ctx context.Context, id, method string, params []byte) (any, error) {
		return handleRPC(ctx, srv, method, params)
	}

	// OnConn (spec 1176 §4.10): serve the native MCP wire over each
	// accepted connection. The bridge is a transparent byte proxy, so
	// opencode's stdio JSON-RPC arrives here verbatim; ServeStream
	// runs the same MCPServer as the single-binary mode (canonical
	// order, hooks, meta propagator, gate middleware).
	onConn := func(ctx context.Context, conn net.Conn) {
		if err := srv.ServeStream(ctx, conn); err != nil {
			log.Printf("dark-mem-mcp-daemon: serve stream: %v", err)
		}
	}

	d, err := daemon.NewDaemon(daemon.Config{
		SocketPath:  *socketPath,
		ReadyPath:   *readyPath,
		PIDPath:     *pidPath,
		IdleTimeout: *idle,
		Version:     *ver,
		OnRequest:   requestHandler,
		OnConn:      onConn,
	})
	if err != nil {
		log.Fatalf("dark-mem-mcp-daemon: NewDaemon: %v", err)
	}

	if err := d.Run(ctx); err != nil {
		log.Fatalf("dark-mem-mcp-daemon: Run: %v", err)
	}
	os.Exit(0)
}
