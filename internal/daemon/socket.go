package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Transport-agnostic socket helpers. On Unix, "unix" network; on
// Windows, named pipes via go-winio (\\.\pipe\dark-mem). The daemon
// and bridge agree on the same socket path (DefaultSocketPath).
//
// NOTE: Go's stdlib net package does NOT support a "pipe" network.
// Named pipes on Windows MUST use github.com/Microsoft/go-winio
// (winio.ListenPipe / winio.DialPipe). The platform-specific impl
// lives in socket_windows.go / socket_unix.go behind build tags.
// (Bug fixed 2026-08-14: v2.19.0 shipped with net.Dial("pipe", ...)
// which fails with "unknown network pipe" on Windows — the CI passed
// on Linux but the daemon never became ready on Windows.)

// DialSocket connects to the daemon socket with a timeout. Cross-
// platform: dispatches to the platform implementation.
func DialSocket(socketPath string, timeout time.Duration) (net.Conn, error) {
	return dialSocketImpl(socketPath, timeout)
}

// ListenSocket creates a listener on socketPath (Unix socket or
// Windows named pipe).
func ListenSocket(socketPath string) (net.Listener, error) {
	return listenSocketImpl(socketPath)
}

// dialSocket is the internal alias used by daemon.go / supervisor.go.
func dialSocket(socketPath string, timeout time.Duration) (net.Conn, error) {
	return dialSocketImpl(socketPath, timeout)
}

// listenSocket is the internal alias used by daemon.go.
func listenSocket(socketPath string) (net.Listener, error) {
	return listenSocketImpl(socketPath)
}

// probeSocket checks if a daemon is listening without blocking.
func probeSocket(socketPath string) bool {
	c, err := dialSocket(socketPath, 50*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// isPipePath returns true for Windows named-pipe paths.
func isPipePath(p string) bool {
	return len(p) >= 9 && p[:9] == `\\.\pipe\`
}

// ensureSocketDir creates the parent dir for Unix sockets (no-op on
// Windows named pipes, which live in the \\.\pipe\ namespace).
func ensureSocketDir(socketPath string) {
	if isPipePath(socketPath) {
		return
	}
	dir := filepath.Dir(socketPath)
	if dir != "" && dir != "." && dir != "/" {
		// Best-effort restrictive permissions.
		_ = os.MkdirAll(dir, 0o700)
		_ = os.Chmod(dir, 0o700)
	}
}

// wrapListenError adds context to a listen failure.
func wrapListenError(network, addr string, err error) error {
	return fmt.Errorf("listen %s %s: %w", network, addr, err)
}

// listenCtx is a shared background context for listeners.
var listenCtx = context.Background()
