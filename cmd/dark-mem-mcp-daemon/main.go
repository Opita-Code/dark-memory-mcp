// Command dark-mem-mcp-daemon is the long-lived server process
// (spec 1176, v2.19.0). It listens on a Unix socket / Windows named
// pipe, owns the SQLite connection, and serves JSON-RPC frames from
// one or more bridges.
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
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/daemon"
	"github.com/dark-agents/dark-memory-mcp/internal/server"
	"github.com/dark-agents/dark-memory-mcp/internal/version"
)

func main() {
	socketPath := flag.String("socket", daemon.DefaultSocketPath(), "Unix socket or named-pipe path")
	readyPath := flag.String("ready", daemon.DefaultReadyPath(), "ready-file path")
	pidPath := flag.String("pid", daemon.DefaultPIDPath(), "PID file path (Unix only)")
	idle := flag.Duration("idle", 30*time.Minute, "idle timeout before self-shutdown")
	ver := flag.String("version", "v"+version.Resolve().Version, "version string")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("dark-mem-mcp-daemon: starting (socket=%s ready=%s pid=%s idle=%s)",
		*socketPath, *readyPath, *pidPath, *idle)

	// Build a real dark-memory Server to back the daemon's RPC
	// handler. This is the SAME server that the single-binary mode
	// uses; we just split it out behind the daemon.
	srv, err := server.New(context.Background())
	if err != nil {
		log.Fatalf("dark-mem-mcp-daemon: server.New: %v", err)
	}
	if err := srv.RegisterAll(); err != nil {
		log.Fatalf("dark-mem-mcp-daemon: RegisterAll: %v", err)
	}

	// requestHandler bridges the line-delimited JSON protocol to the
	// in-process MCP server. It uses a simple in-memory switch over
	// the methods that the harness calls; full MCP-streaming support
	// is a follow-up (per spec 1176 §4.10).
	requestHandler := func(ctx context.Context, id, method string, params []byte) (any, error) {
		return handleRPC(ctx, srv, method, params)
	}

	d, err := daemon.NewDaemon(daemon.Config{
		SocketPath:  *socketPath,
		ReadyPath:   *readyPath,
		PIDPath:     *pidPath,
		IdleTimeout: *idle,
		Version:     *ver,
		OnRequest:   requestHandler,
	})
	if err != nil {
		log.Fatalf("dark-mem-mcp-daemon: NewDaemon: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := d.Run(ctx); err != nil {
		log.Fatalf("dark-mem-mcp-daemon: Run: %v", err)
	}

	// Ensure cleanup on exit.
	os.Exit(0)
}
