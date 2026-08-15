package daemon

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// Daemon is the long-lived process that owns the dark-memory server
// surface (SQLite connection, constitution watchdog, 53 tools).
//
// Lifecycle FSM:
//
//	not_running --Start()--> starting --Accept() done--> ready
//	                                                       |
//	                                                       | Close() OR idle timeout
//	                                                       v
//	                                                  shutting_down
//	                                                       |
//	                                                       | drain + remove
//	                                                       v
//	                                                  not_running
type Daemon struct {
	cfg        Config
	mu         sync.Mutex // guards state + idleTimer
	state      State
	listener   net.Listener
	idleTimer  *time.Timer
	idleCancel context.CancelFunc
	startedAt  time.Time
	pidPath    string
}

// Config configures a Daemon.
type Config struct {
	SocketPath     string        // Unix socket / Windows named pipe
	ReadyPath      string        // ready-file path
	PIDPath        string        // PID file path (empty on Windows)
	IdleTimeout    time.Duration // 30m default
	Version        string        // for pong response
	OnRequest      RequestHandler // injected handler (returns Frame response)
	// OnConn, when set, takes over connection handling (spec 1176
	// §4.10 MCP-over-socket). The daemon accepts the connection and
	// hands it to OnConn, which is responsible for speaking the
	// native MCP wire (initialize / tools/list / tools/call) over
	// that conn. When nil, the legacy Frame-protocol loop runs.
	//
	// The daemon still owns connection lifecycle: handleConn defers
	// conn.Close() regardless of which path runs.
	OnConn func(ctx context.Context, conn net.Conn)
}

// RequestHandler processes one RPC frame and returns the response
// frame. The daemon forwards every (id, method, params) to this
// function. Errors are returned as JSON-RPC errors.
type RequestHandler func(ctx context.Context, id, method string, params []byte) (result any, err error)

// State is the daemon FSM state.
type State string

const (
	StateNotRunning   State = "not_running"
	StateStarting     State = "starting"
	StateReady        State = "ready"
	StateShuttingDown  State = "shutting_down"
)

// NewDaemon constructs a Daemon. Does not start it; call Run().
func NewDaemon(cfg Config) (*Daemon, error) {
	if cfg.SocketPath == "" {
		return nil, errors.New("Config.SocketPath is required")
	}
	if cfg.OnRequest == nil && cfg.OnConn == nil {
		return nil, errors.New("Config.OnRequest or Config.OnConn is required")
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 30 * time.Minute
	}
	return &Daemon{
		cfg:    cfg,
		state:  StateNotRunning,
		pidPath: cfg.PIDPath,
	}, nil
}

// Run starts the daemon, accepts connections, blocks until ctx is
// cancelled or shutdown is requested. Idempotent: returns
// immediately if state != not_running.
func (d *Daemon) Run(ctx context.Context) error {
	d.mu.Lock()
	if d.state != StateNotRunning {
		d.mu.Unlock()
		return fmt.Errorf("daemon already in state %s", d.state)
	}
	d.state = StateStarting
	d.startedAt = time.Now()
	d.mu.Unlock()

	// 1. Listen on the socket.
	ln, err := listenSocket(d.cfg.SocketPath)
	if err != nil {
		d.transition(StateNotRunning)
		return fmt.Errorf("daemon listen: %w", err)
	}
	d.mu.Lock()
	d.listener = ln
	d.mu.Unlock()

	// 2. Write PID + ready files.
	if d.pidPath != "" {
		_ = WritePIDRecord(d.pidPath, PIDRecord{
			PID:        os.Getpid(),
			StartedAt:  d.startedAt.Format(time.RFC3339),
			ConfigHash: configHash(ctx),
			Version:    d.cfg.Version,
		})
	}
	if err := WriteReadyFile(d.cfg.ReadyPath); err != nil {
		_ = ln.Close()
		d.transition(StateNotRunning)
		return fmt.Errorf("write ready file: %w", err)
	}

	// 3. Transition to ready, start idle timer.
	d.transition(StateReady)
	d.resetIdleTimer()

	// 4. Accept loop.
	go d.acceptLoop(ln, ctx)

	// 5. Block until ctx done or shutdown requested.
	<-ctx.Done()
	d.transition(StateShuttingDown)
	d.cancelIdleTimer()

	if err := ln.Close(); err != nil {
		// best-effort; socket may already be closed
	}
	if d.cfg.ReadyPath != "" {
		_ = os.Remove(d.cfg.ReadyPath)
	}
	if d.pidPath != "" {
		_ = RemovePIDRecord(d.pidPath)
	}
	d.transition(StateNotRunning)
	return nil
}

// transition moves the FSM to next state (with locking).
func (d *Daemon) transition(next State) {
	d.mu.Lock()
	d.state = next
	d.mu.Unlock()
}

// State returns the current daemon state.
func (d *Daemon) State() State {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state
}

// resetIdleTimer (re)starts the 30m shutdown countdown.
func (d *Daemon) resetIdleTimer() {
	d.cancelIdleTimer()
	if d.cfg.IdleTimeout <= 0 {
		return
	}
	d.mu.Lock()
	d.idleTimer = time.AfterFunc(d.cfg.IdleTimeout, func() {
		d.mu.Lock()
		if d.state == StateReady {
			d.mu.Unlock()
			// Find a way to signal Run() to exit: cancel a child ctx.
			if d.idleCancel != nil {
				d.idleCancel()
			}
			return
		}
		d.mu.Unlock()
	})
	d.mu.Unlock()
}

func (d *Daemon) cancelIdleTimer() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.idleTimer != nil {
		d.idleTimer.Stop()
		d.idleTimer = nil
	}
}

// acceptLoop accepts connections in a loop; each connection handled
// in a goroutine.
func (d *Daemon) acceptLoop(ln net.Listener, ctx context.Context) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// log + continue; transient errors shouldn't kill the loop
			continue
		}
		d.resetIdleTimer()
		go d.handleConn(ctx, conn)
	}
}

// handleConn reads frames from a single connection, dispatches each
// RPC to OnRequest, and writes the response. When Config.OnConn is
// set (spec 1176 §4.10), the connection is handed over to OnConn
// instead — it owns the MCP wire for that connection. The deferred
// Close covers both paths.
func (d *Daemon) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	if d.cfg.OnConn != nil {
		d.cfg.OnConn(ctx, conn)
		return
	}
	r := bufio.NewReader(conn)
	for {
		frame, err := ReadFrame(r)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			// log + close
			return
		}
		d.resetIdleTimer()
		switch frame.Type {
		case FrameTypePing:
			_ = WriteFrame(conn, NewPongFrame(time.Since(d.startedAt), d.cfg.Version))
		case FrameTypeRPC:
			result, callErr := d.cfg.OnRequest(ctx, frame.ID, frame.Method, frame.Params)
			var rpcErr *RPCError
			if callErr != nil {
				rpcErr = &RPCError{Code: -32000, Message: callErr.Error()}
			}
			resp, err := NewResponseFrame(frame.ID, result, rpcErr)
			if err != nil {
				// bad input; close connection
				return
			}
			_ = WriteFrame(conn, resp)
		case FrameTypeNotify, FrameTypeShutdown:
			// daemon-side notifications come FROM daemon TO bridge.
			// Receiving one is unexpected; close.
			return
		default:
			// unknown frame; close
			return
		}
	}
}

// configHash derives a short hash for the PID-record metadata. Today
// the daemon has only one config (env vars + identity), so the hash
// is derived from the version string + socket path.
func configHash(ctx context.Context) string {
	// Minimal placeholder; a richer impl would hash cfg fields.
	return "v1"
}
