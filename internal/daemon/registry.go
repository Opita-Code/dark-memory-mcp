package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DefaultSocketPath returns the platform-specific default Unix socket
// or Windows named pipe path. The daemon and bridge use the same
// default so they agree without explicit env-var configuration.
//
//   - macOS/Linux:  ~/.dark-agents/daemon.sock
//   - Windows:      \\.\pipe\dark-mem
//
// Override via DARK_MEM_DAEMON_SOCKET env var.
func DefaultSocketPath() string {
	if env := os.Getenv("DARK_MEM_DAEMON_SOCKET"); env != "" {
		return env
	}
	if isWindows() {
		return `\\.\pipe\dark-mem`
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// Fallback to /tmp; rare on POSIX systems without HOME.
		return "/tmp/dark-mem-daemon.sock"
	}
	return filepath.Join(home, ".dark-agents", "daemon.sock")
}

// DefaultReadyPath returns the path to the ready-file (atomic-rename
// per dark-copilot G14 convention).
//
//   - macOS/Linux:  ~/.dark-agents/daemon.ready
//   - Windows:      %USERPROFILE%\.dark-agents\daemon.ready
//
// NOTE: On Windows the ready file MUST NOT derive from the socket
// path — the socket is a named pipe (\\.\pipe\...) which lives in a
// different namespace and cannot hold a regular file. (Bug fixed
// 2026-08-14: v2.19.0 used filepath.Dir(socket) which produced
// \\.\pipe\daemon.ready and failed with "cannot find the file".)
func DefaultReadyPath() string {
	if env := os.Getenv("DARK_MEM_DAEMON_READY"); env != "" {
		return env
	}
	if isWindows() {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.Getenv("USERPROFILE")
		}
		if home != "" {
			return filepath.Join(home, ".dark-agents", "daemon.ready")
		}
	}
	dir := filepath.Dir(DefaultSocketPath())
	return filepath.Join(dir, "daemon.ready")
}

// DefaultPIDPath returns the path to the PID file (Unix) or empty string.
// Windows has no analogous file (PID is exposed via the socket endpoint).
func DefaultPIDPath() string {
	if isWindows() {
		return "" // PID file is Unix-only
	}
	if env := os.Getenv("DARK_MEM_DAEMON_PID"); env != "" {
		return env
	}
	dir := filepath.Dir(DefaultSocketPath())
	return filepath.Join(dir, "daemon.pid")
}

// PIDRecord is the on-disk format of the PID file. Mirrors philschmid
// pattern: PID + config hash for staleness detection.
type PIDRecord struct {
	PID        int    `json:"pid"`
	StartedAt  string `json:"started_at"`
	ConfigHash string `json:"config_hash"`
	Version    string `json:"version"`
}

// WritePIDRecord atomically writes pid+metadata to path. On Unix,
// fsync ensures durability before the function returns.
//
// Per row 859 antipattern audit + dark-copilot G14 (atomic rename
// without pre-Remove), the write uses O_EXCL + os.Rename semantics:
// 1. Write to a temp file in the same directory
// 2. fsync the temp file
// 3. os.Rename temp -> target
//
// On Windows, os.Rename is atomic on NTFS.
func WritePIDRecord(path string, record PIDRecord) error {
	if path == "" {
		return nil // no-op (Windows)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir PID dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "daemon.pid.*")
	if err != nil {
		return fmt.Errorf("create temp PID file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName) // best-effort cleanup if rename fails
	}()
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(record); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("encode PID record: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync PID file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp PID file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename PID file: %w", err)
	}
	return nil
}

// ReadPIDRecord reads and decodes the PID file. Returns an error if
// the file does not exist or is corrupt.
func ReadPIDRecord(path string) (PIDRecord, error) {
	if path == "" {
		return PIDRecord{}, fmt.Errorf("PID file not used on this platform")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return PIDRecord{}, err
	}
	var r PIDRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return PIDRecord{}, fmt.Errorf("unmarshal PID record: %w", err)
	}
	return r, nil
}

// RemovePIDRecord removes the PID file. Idempotent.
func RemovePIDRecord(path string) error {
	if path == "" {
		return nil
	}
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// WriteReadyFile atomically writes the ready signal (one byte:
// newline) per dark-copilot G14 convention.
func WriteReadyFile(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir ready dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "daemon.ready.*")
	if err != nil {
		return fmt.Errorf("create temp ready file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.Write([]byte("ready\n")); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write ready file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync ready file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close ready temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename ready file: %w", err)
	}
	return nil
}

// WaitForReady blocks until the ready file exists or timeout. Returns
// nil on ready, error on timeout.
func WaitForReady(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("ready file %s not created within %s", path, timeout)
}

// IsDaemonRunning checks if a daemon is responsive by attempting a
// ping with a short timeout.
func IsDaemonRunning(socketPath string, timeout time.Duration) bool {
	// Implementation lives in supervisor.go (which has the dial logic).
	return tryPing(socketPath, timeout)
}

func isWindows() bool {
	return os.PathSeparator == '\\' && os.PathListSeparator == ';'
}
