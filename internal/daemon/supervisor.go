package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"
)

// Supervisor is the bridge-side supervisor (mirror dark-copilot
// spec 861). It dials the daemon socket, and on N consecutive dial
// failures, spawns a fresh daemon. The supervisor gives up (surfaces
// a connection error) after MaxRetries unsuccessful dial attempts.
//
// The supervisor is also a "give-up" mode: it does NOT infinite-loop.
// After GiveUpAfter total elapsed time, the bridge surfaces the
// connection error to the LLM caller.
type Supervisor struct {
	cfg SupervisorConfig
	// pingAttempts counts total dial attempts (atomic).
	pingAttempts atomic.Int64
	// spawnAttempts counts daemon spawn invocations (atomic).
	spawnAttempts atomic.Int64
}

// SupervisorConfig configures a Supervisor.
type SupervisorConfig struct {
	SocketPath     string        // daemon socket
	DaemonBin      string        // path to dark-mem-mcp-daemon binary
	DialTimeout    time.Duration // per-attempt dial timeout (default 5s)
	SpawnTimeout   time.Duration // time to wait for ready-file after spawn (default 5s)
	GapBetweenRetries time.Duration // sleep between retries (default 1s)
	MaxRetries     int           // dial attempts before spawning daemon (default 3)
	GiveUpAfter    time.Duration // total elapsed time before giving up (default 30s)
	OnSpawn        func() // optional spawn hook (testable; defaults to exec.Command)
}

// Default returns a SupervisorConfig with sensible defaults.
func Default(socketPath, daemonBin string) SupervisorConfig {
	return SupervisorConfig{
		SocketPath:       socketPath,
		DaemonBin:        daemonBin,
		DialTimeout:      5 * time.Second,
		SpawnTimeout:     5 * time.Second,
		GapBetweenRetries: 1 * time.Second,
		MaxRetries:       3,
		GiveUpAfter:      30 * time.Second,
		OnSpawn:          nil,
	}
}

// Connect dials the daemon with retry + spawn semantics. Returns the
// connected net.Conn or an error if GiveUpAfter is exceeded.
//
// Lifecycle of one Connect call:
//
//  1. Dial socket (DialTimeout). If success -> return conn.
//  2. If failure && attempts < MaxRetries -> sleep GapBetweenRetries,
//     retry. (MaxRetries - 1) times.
//  3. If still failing after MaxRetries attempts -> spawn daemon
//     (SpawnTimeout), then dial once more.
//  4. If that dial succeeds -> return conn.
//  5. Otherwise -> return error.
func (s *Supervisor) Connect(ctx context.Context) (conn net.Conn, err error) {
	cfg := s.cfg
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 5 * time.Second
	}
	if cfg.GapBetweenRetries <= 0 {
		cfg.GapBetweenRetries = 1 * time.Second
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3
	}
	if cfg.GiveUpAfter <= 0 {
		cfg.GiveUpAfter = 30 * time.Second
	}
	if cfg.SpawnTimeout <= 0 {
		cfg.SpawnTimeout = 5 * time.Second
	}

	deadline := time.Now().Add(cfg.GiveUpAfter)
	for attempt := 0; attempt < cfg.MaxRetries; attempt++ {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("supervisor: gave up after %s", cfg.GiveUpAfter)
		}
		s.pingAttempts.Add(1)
		c, dialErr := dialSocket(cfg.SocketPath, cfg.DialTimeout)
		if dialErr == nil {
			return c, nil
		}
		// Wait before next attempt.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(cfg.GapBetweenRetries):
		}
	}
	// After MaxRetries failures, spawn a fresh daemon.
	if err := s.spawnDaemon(ctx, cfg); err != nil {
		return nil, fmt.Errorf("supervisor: spawn failed: %w", err)
	}
	// Final dial after spawn.
	c, dialErr := dialSocket(cfg.SocketPath, cfg.DialTimeout)
	if dialErr == nil {
		return c, nil
	}
	return nil, fmt.Errorf("supervisor: dial after spawn failed: %w", dialErr)
}

// spawnDaemon invokes OnSpawn or falls back to exec.Command. Waits for
// the ready-file before returning.
func (s *Supervisor) spawnDaemon(ctx context.Context, cfg SupervisorConfig) error {
	s.spawnAttempts.Add(1)
	readyPath := DefaultReadyPath()
	if cfg.OnSpawn != nil {
		cfg.OnSpawn()
	} else {
		cmd := exec.CommandContext(ctx, cfg.DaemonBin, "daemon",
			"--socket", cfg.SocketPath,
			"--ready", readyPath,
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start daemon: %w", err)
		}
		// Detach: we don't wait for completion; the daemon runs until
		// idle timeout or signal.
		go func() { _ = cmd.Wait() }()
	}
	// Wait for the ready file.
	return WaitForReady(readyPath, cfg.SpawnTimeout)
}

// PingStats returns counters (for tests / observability).
func (s *Supervisor) PingStats() (pings int64, spawns int64) {
	return s.pingAttempts.Load(), s.spawnAttempts.Load()
}

// tryPing is a one-shot probe with a short timeout; exported as
// IsDaemonRunning's helper. Returns true if the daemon responds.
func tryPing(socketPath string, timeout time.Duration) bool {
	c, err := dialSocket(socketPath, timeout)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// socketDir derives the dir of the socket file (parent dir).
func socketDirFromRegistry(socketPath string) string {
	return filepath.Dir(socketPath)
}

var _ = errors.New // suppress unused import in some build configs
