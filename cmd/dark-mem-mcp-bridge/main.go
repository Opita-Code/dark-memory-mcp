// Command dark-mem-mcp-bridge is the per-opencode-session shim
// (spec 1176, v2.19.0). It connects to a long-lived dark-memory
// daemon over a Unix socket / Windows named pipe, then forwards
// newline-delimited JSON frames between opencode's stdin/stdout
// and the daemon connection.
//
// Usage:
//
//	dark-mem-mcp-bridge [flags]
//	--socket PATH  daemon socket (default: ~/.dark-agents/daemon.sock)
//	--daemon PATH  daemon binary path (default: alongside the bridge)
//	--no-spawn      do NOT spawn the daemon on connect failure
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/daemon"
)

func main() {
	socketPath := flag.String("socket", daemon.DefaultSocketPath(), "daemon socket path")
	daemonBin := flag.String("daemon", defaultDaemonBin(), "path to dark-mem-mcp-daemon binary")
	noSpawn := flag.Bool("no-spawn", false, "do NOT spawn the daemon on connect failure")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("dark-mem-mcp-bridge: starting (socket=%s daemon=%s)", *socketPath, *daemonBin)

	conn, err := connectWithOptionalSpawn(*socketPath, *daemonBin, !*noSpawn)
	if err != nil {
		log.Fatalf("dark-mem-mcp-bridge: connect: %v", err)
	}
	defer conn.Close()
	log.Printf("dark-mem-mcp-bridge: connected to daemon (socket=%s)", *socketPath)

	// Forward stdin -> daemon, daemon -> stdout until either closes.
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		_ = pipe(os.Stdin, conn)
		// stdin closed -> opencode is shutting down. Close the
		// connection so the daemon can detect dead bridges.
		_ = conn.Close()
	}()
	go func() {
		defer wg.Done()
		_ = pipe(conn, os.Stdout)
	}()
	wg.Wait()
}

// connectWithOptionalSpawn dials the daemon. If the dial fails and
// allowSpawn is true, spawns the daemon binary and retries. Returns
// the connected net.Conn.
func connectWithOptionalSpawn(socketPath, daemonBin string, allowSpawn bool) (net.Conn, error) {
	c, err := daemon.DialSocket(socketPath, 5*time.Second)
	if err == nil {
		return c, nil
	}
	if !allowSpawn {
		return nil, fmt.Errorf("dial %s: %w", socketPath, err)
	}
	log.Printf("dark-mem-mcp-bridge: dial failed (%v); spawning daemon", err)
	cmd := exec.Command(daemonBin,
		"--socket", socketPath,
		"--ready", daemon.DefaultReadyPath(),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn daemon: %w (dial was: %v)", err, err)
	}
	go func() { _ = cmd.Wait() }()
	if err := daemon.WaitForReady(daemon.DefaultReadyPath(), 5*time.Second); err != nil {
		return nil, fmt.Errorf("daemon never became ready: %w (dial was: %v)", err, err)
	}
	c, err = daemon.DialSocket(socketPath, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial after spawn: %w", err)
	}
	return c, nil
}

// pipe copies bytes from r to w line-by-line (newline-delimited JSON
// frames). EOF from either side terminates the function. Errors
// other than EOF are logged but not returned (the caller uses wg.Wait).
func pipe(r io.Reader, w io.Writer) error {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if _, werr := w.Write(line); werr != nil {
				return werr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func defaultDaemonBin() string {
	exe, err := os.Executable()
	if err != nil {
		return "dark-mem-mcp-daemon"
	}
	dir := filepath.Dir(exe)
	// Try the .exe variant first (Windows).
	candidate := filepath.Join(dir, "dark-mem-mcp-daemon.exe")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	candidateNoExt := filepath.Join(dir, "dark-mem-mcp-daemon")
	if _, err := os.Stat(candidateNoExt); err == nil {
		return candidateNoExt
	}
	return "dark-mem-mcp-daemon"
}
