// Package wire tests dark-mem-mcp end-to-end via the JSON-RPC wire
// against the actual binary subprocess. These tests catch bugs
// that the Go-level orchestrator tests miss: harness encoding,
// schema negotiation, and boot-path wire shape.
//
// Pattern: invoke the binary as a subprocess, write JSON-RPC frames
// on stdin (newline-delimited), read responses on stdout, assert
// against parsed JSON. Each test targets a SPECIFIC fixed bug (F33,
// F35, F36, F37-F40, INV-8) so a regression points to the fix it
// came from.
//
// Tags: `wire` so you can `go test -tags wire ./tests/wire/...` or
// `go test ./tests/wire/...` (the wire tests are the default tag
// because they ARE the production-readiness signal).
package wire

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// wireSession talks to a live dark-mem-mcp.exe subprocess over its
// stdio JSON-RPC channel. The harness's per-call framing rules come
// straight from the opencode source (see opencode/packages/opencode/src/mcp/catalog.ts):
//
//   * One JSON object per write, terminated by '\n' (not '\r\n').
//   * Server emits one JSON object per reply, also '\n'-terminated.
//   * arguments is a single JSON object (NOT a wrapper envelope).
//   * clientInfo.name must be a non-empty string on initialize.
type wireSession struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *lineReader
	stderr bytes.Buffer
	idSeq  int
}

// startWireSession boots the binary against a per-test isolated DB
// (under t.TempDir()) so tests don't pollute each other or the
// operator's canonical dark-memory.db. DSN is forced via env var
// (the v1.2.0+ env-driven DSN switch).
func startWireSession(t *testing.T) *wireSession {
	t.Helper()

	bin := resolveWireBin(t)
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "dark-memory.db")

	cmd := exec.Command(bin)
	cmd.Env = append(cmd.Environ(),
		"DARK_DB="+dbPath,
		"DARK_DB_DRIVER=sqlite",
		// Disable constitution file lookup so the watchdog does not
		// need a parent file we don't ship.
		"DARK_CONSTITUTION_FILE=",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("wire: stdin pipe: %v", err)
	}
	stdoutR, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("wire: stdout pipe: %v", err)
	}
	cmd.Stderr = &sessionStderr{session: t, buf: &bytes.Buffer{}}
	if err := cmd.Start(); err != nil {
		t.Fatalf("wire: start %s: %v", bin, err)
	}

	s := &wireSession{
		cmd:    cmd,
		stdin:  stdin,
		stdout: newLineReader(stdoutR),
	}
	// Drain the boot banner so subsequent reads start at the first
	// JSON-RPC response. dark-mem-mcp prints the boot log to stderr
	// (not stdout), so this read is mostly empty, but defensive.
	s.skipBootBanner()

	// Send initialize + notifications/initialized exactly once.
	if err := s.request("initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "wire-test", "version": "test"},
	}); err != nil {
		t.Fatalf("wire: initialize failed: %v", err)
	}
	if err := s.notify("notifications/initialized", map[string]any{}); err != nil {
		t.Fatalf("wire: notifications/initialized failed: %v", err)
	}
	t.Cleanup(func() { s.close() })
	return s
}

// resolveWireBin finds the dark-mem-mcp.exe binary, preferring a
// freshly-built ../dark-mem-mcp.exe relative to the test binary.
// The convention: tests run from the package dir; ../dark-mem-mcp.exe
// is the canonical local build.
//
// Operators who want to test a specific built binary can set
// DARK_MEM_MCP_BIN to override.
func resolveWireBin(t *testing.T) string {
	t.Helper()
	if v := strings.TrimSpace(envOr("DARK_MEM_MCP_BIN", "")); v != "" {
		return v
	}
	// Search common layouts: the standard repo location and the
	// cmd/dark-mem-mcp/ subdir built by `go build`.
	candidates := []string{
		"../cmd/dark-mem-mcp/dark-mem-mcp.exe",
		"dark-mem-mcp.exe",
	}
	for _, c := range candidates {
		if _, err := exec.LookPath(c); err == nil {
			return c
		}
	}
	t.Fatalf("wire: cannot locate dark-mem-mcp.exe (set DARK_MEM_MCP_BIN to override; tried %v)", candidates)
	return ""
}

func envOr(k, def string) string {
	if v, ok := exec.LookPath("env"); ok == nil {
		_ = v
	}
	// Look in os.Getenv via direct call (avoid extra dep on os here);
	// tests fall through to default if missing.
	return goosGetenv(k, def)
}

// goosGetenv is a tiny wrapper that uses the testing.TB context to
// import os on demand (keeps this file's top-of-file imports compact).
func goosGetenv(k, def string) string {
	for _, e := range envVars() {
		if strings.HasPrefix(e, k+"=") {
			return strings.TrimPrefix(e, k+"=")
		}
	}
	return def
}

// skipBootBanner is now a no-op. The feeder reads stdout asynchronously
// and pushes complete lines to a buffered channel; readOne blocks
// until the next line arrives. The drain-on-first-call optimization
// was wrong: it discarded the initialize response.
func (s *wireSession) skipBootBanner() {}

// request writes a JSON-RPC `id`-bearing request and reads the
// matching response. The `id` is monotonically allocated by
// wireSession so concurrent-style nested probes don't collide
// (we run tests serially, but defensive).
func (s *wireSession) request(method string, params any) error {
	id := s.nextID()
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	})
	return s.roundTrip(raw)
}

// notify writes a JSON-RPC `notification` (no id, no reply expected).
func (s *wireSession) notify(method string, params any) error {
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": method, "params": params,
	})
	_, err := s.stdin.Write(append(raw, '\n'))
	return err
}

// roundTrip marshals one frame into stdin and reads the matching
// JSON-RPC response by id from stdout. The mcp-go server uses
// newline-delimited JSON, so we Write+Read with a trailing '\n' on
// each side.
func (s *wireSession) roundTrip(raw []byte) error {
	if _, err := s.stdin.Write(append(raw, '\n')); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	resp, err := s.stdout.readOne()
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	var env struct {
		ID    any   `json:"id"`
		Error any   `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		return fmt.Errorf("unmarshal: %w: %s", err, string(resp))
	}
	if env.Error != nil {
		return fmt.Errorf("rpc error: %v", env.Error)
	}
	if env.ID == nil {
		// Notification reply (shouldn't happen for ids we own) — treat
		// as success but record for debugging.
		return fmt.Errorf("got notification-like reply for our id: %s", string(resp))
	}
	return nil
}

// toolsCall wraps tools/call with a strict per-test request ID.
func (s *wireSession) toolsCall(name string, args map[string]any) ([]byte, error) {
	id := s.nextID()
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	if _, err := s.stdin.Write(append(raw, '\n')); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}
	return s.stdout.readOne()
}

// close sends an EOF by closing stdin, then waits for the daemon
// to exit (graceful shutdown). If it doesn't exit within 5s, kill.
func (s *wireSession) close() {
	_ = s.stdin.Close()
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = s.cmd.Process.Kill()
		<-done
	}
}

// nextID allocates an id monotonically per session. MCP JSON-RPC ids
// must be unique within a session; we use a single goroutine (the
// wire tests run serially) so a plain int is sufficient.
func (s *wireSession) nextID() int { s.idSeq++; return s.idSeq }

// envVars returns os.Environ()'s slice. Implemented in helpers.go
// to keep this file lean.
func envVars() []string { return osEnviron() }

// sessionStderr captures the subprocess stderr and writes it to the
// test's logging stream if the session is closed before normal exit.
type sessionStderr struct {
	session *testing.T
	buf     *bytes.Buffer
}

func (s *sessionStderr) Write(p []byte) (int, error) {
	s.buf.Write(p)
	return len(p), nil
}

// lineReader reads newline-delimited JSON from an io.ReadCloser.
// readOne blocks until a complete line arrives or the underlying
// reader returns EOF.
type lineReader struct {
	r io.ReadCloser
	c chan readResult
	once bool
}

type readResult struct {
	line []byte
	err  error
}

func newLineReader(r io.ReadCloser) *lineReader {
	lr := &lineReader{r: r, c: make(chan readResult, 4)}
	go lr.feeder()
	return lr
}

func (lr *lineReader) feeder() {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1024)
	for {
		n, err := lr.r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			for {
				idx := indexOfNewline(buf)
				if idx < 0 {
					break
				}
				line := make([]byte, idx)
				copy(line, buf[:idx])
				lr.c <- readResult{line: line}
				buf = buf[idx+1:]
			}
		}
		if err != nil {
			if err != io.EOF {
				lr.c <- readResult{err: err}
			}
			close(lr.c)
			return
		}
	}
}

func indexOfNewline(buf []byte) int {
	for i, b := range buf {
		if b == '\n' {
			return i
		}
	}
	return -1
}

func (lr *lineReader) readOne() ([]byte, error) {
	// Block until the feeder pushes the next complete JSON line onto
	// the channel. We intentionally do NOT drain on the first call —
	// the initialize response must be consumed by roundTrip, not
	// discarded by a "drain pending boot output" optimisation.
	res := <-lr.c
	return res.line, res.err
}

// --- context helpers ---

// bootCtx returns a context with a 30-second timeout for boot
// operations (start process, wait for first response).
func bootCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}
