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
// Windows, "pipe" network (\\.\pipe\dark-mem). The daemon and bridge
// agree on the same socket path (DefaultSocketPath).
//
// Note: This implementation uses net.Dialer's "unix" or "pipe"
// network. On cross-platform support, Go's net package auto-routes
// based on the path prefix.

// dialSocket connects to the daemon socket with a timeout. Cross-
// platform: detects Unix socket vs Windows named pipe by path
// prefix.
func dialSocket(socketPath string, timeout time.Duration) (net.Conn, error) {
	d := net.Dialer{Timeout: timeout}
	network, addr := normalizeSocket(socketPath)
	return d.Dial(network, addr)
}

// normalizeSocket converts a raw socket path to (network, address)
// for net.Dial / net.Listen.
func normalizeSocket(socketPath string) (network, address string) {
	if isPipePath(socketPath) {
		// Strip leading \\.\pipe\ for net.Dial "pipe" network.
		addr := socketPath
		if len(addr) >= 9 && addr[:9] == `\\.\pipe\` {
			addr = addr[9:]
		}
		return "pipe", addr
	}
	return "unix", socketPath
}

// listenSocket creates a listener on socketPath (Unix socket or
// Windows named pipe).
func listenSocket(socketPath string) (net.Listener, error) {
	dir := filepath.Dir(socketPath)
	if dir != "" && dir != "." && dir != "/" {
		// Best-effort restrictive permissions; Windows ignores these.
		_ = os.MkdirAll(dir, 0o700)
		_ = os.Chmod(dir, 0o700)
	}
	network, addr := normalizeSocket(socketPath)
	cfg := net.ListenConfig{}
	ln, err := cfg.Listen(context.Background(), network, addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s %s: %w", network, addr, err)
	}
	if unixLn, ok := ln.(*net.UnixListener); ok {
		unixLn.SetUnlinkOnClose(true)
	}
	return ln, nil
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
