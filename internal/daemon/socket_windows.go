//go:build windows

package daemon

import (
	"net"
	"time"

	"github.com/Microsoft/go-winio"
)

// dialSocketImpl connects to a Windows named pipe (or a Unix socket
// on newer Go versions that support AF_UNIX on Windows).
func dialSocketImpl(socketPath string, timeout time.Duration) (net.Conn, error) {
	if isPipePath(socketPath) {
		// winio.DialPipe takes the full \\.\pipe\name path and
		// accepts a *time.Duration for the connect timeout.
		to := timeout
		return winio.DialPipe(socketPath, &to)
	}
	return net.DialTimeout("unix", socketPath, timeout)
}

// listenSocketImpl creates a listener on a Windows named pipe or
// Unix socket.
func listenSocketImpl(socketPath string) (net.Listener, error) {
	ensureSocketDir(socketPath)
	if isPipePath(socketPath) {
		// winio.ListenPipe takes the full \\.\pipe\name path. The
		// default PipeConfig grants access to the current user only.
		return winio.ListenPipe(socketPath, nil)
	}
	cfg := net.ListenConfig{}
	ln, err := cfg.Listen(listenCtx, "unix", socketPath)
	if err != nil {
		return nil, wrapListenError("unix", socketPath, err)
	}
	return ln, nil
}
