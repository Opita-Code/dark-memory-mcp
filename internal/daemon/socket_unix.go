//go:build !windows

package daemon

import (
	"net"
	"time"
)

// dialSocketImpl connects to a Unix socket.
func dialSocketImpl(socketPath string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("unix", socketPath, timeout)
}

// listenSocketImpl creates a listener on a Unix socket.
func listenSocketImpl(socketPath string) (net.Listener, error) {
	ensureSocketDir(socketPath)
	cfg := net.ListenConfig{}
	ln, err := cfg.Listen(listenCtx, "unix", socketPath)
	if err != nil {
		return nil, wrapListenError("unix", socketPath, err)
	}
	if unixLn, ok := ln.(*net.UnixListener); ok {
		unixLn.SetUnlinkOnClose(true)
	}
	return ln, nil
}
