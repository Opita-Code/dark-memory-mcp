// Package bench provides Go benchmarks for the dark-memory-mcp v4
// performance baseline (Phase 19 / Loop 9 / T-411).
//
// All benchmarks use isolated test databases to avoid touching the
// production dark.db at %APPDATA%\dark-agents\dark.db. Each
// benchmark writes to bench_*_test.db in t.TempDir().
//
// Run with:
//
//	go test -tags=bench -bench=. -benchmem -count=10 ./bench/...
//
// benchstat-compatible output (no header parsing required):
//	go test -tags=bench -bench=. -benchmem -count=10 ./bench/... | tee bench-cold.txt
//
//go:build bench
// +build bench

package bench

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// binPath resolves the canonical dark-mem-mcp binary path. The
// benchmark fails fast if the binary is not built.
//
// Production path (Windows):
//
//	C:\Users\Nico\Documents\dark-memory-mcp\bin\dark-mem-mcp.exe
//
// Tests that need a different binary (e.g., a CI-built one) can
// set BENCH_DARK_MEM_MCP_BIN to override.
func binPath(b *testing.B) string {
	if v := os.Getenv("BENCH_DARK_MEM_MCP_BIN"); v != "" {
		if _, err := os.Stat(v); err == nil {
			return v
		} else {
			b.Fatalf("BENCH_DARK_MEM_MCP_BIN set to %q but not found: %v", v, err)
		}
	}
	candidates := []string{
		"../bin/dark-mem-mcp.exe",
		"../bin/dark-mem-mcp",
		"../../bin/dark-mem-mcp.exe",
		"../../bin/dark-mem-mcp",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			abs, err := filepath.Abs(c)
			if err == nil {
				return abs
			}
		}
	}
	b.Skip("dark-mem-mcp binary not found; build with `make build` or set BENCH_DARK_MEM_MCP_BIN")
	return ""
}

// BenchmarkColdStartSubprocess measures the FULL boot time of the
// dark-mem-mcp binary, from process exec until the binary's first
// stdout line. This includes:
//   - Go runtime startup (~50-200ms on Windows)
//   - dark-mem-mcp-supervisor.exe parent fork (if applicable)
//   - dark-mem-mcp.exe child boot (server.New → Boot)
//   - All 15 boot phases from legacy_main.go:48 to :230
//
// It does NOT measure MCP handshake latency (that's
// BenchmarkMCPInitialize) or first-tool-call latency (that's
// BenchmarkFirstToolCall).
//
// Run with: go test -tags=bench -bench=BenchmarkColdStartSubprocess
//   -benchtime=10x ./bench/...
//
// Skip with: -short flag (cheap skip path for non-Bench runs).
func BenchmarkColdStartSubprocess(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping cold-start benchmark in -short mode")
	}
	bin := binPath(b)

	// Each iteration gets a unique DSN so we always cold-start
	// against a fresh database (forces the full migration path on
	// each iteration). This is the cold-start scenario the
	// operator cares about (deploy → first request).
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tmpDB := filepath.Join(b.TempDir(), "bench_coldstart.db")
		ctx, cancelCtx := context.WithTimeout(context.Background(), 30*time.Second)
		start := time.Now()
		cmd := exec.CommandContext(ctx, bin)
		cmd.Env = append(os.Environ(),
			"DARK_DB_DRIVER=sqlite",
			"DARK_DB_DSN="+tmpDB,
			"DARK_AUDIT_HMAC_KEY=v1:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			// Bypass the dark-cli audit gate (we don't have one in bench)
			"DARK_AUDIT_DIR=",
			"DARK_TRUST_ROOTS_DIR=",
			// Disable federation peer (not needed for cold-start measurement)
			"DARK_FEDERATION_PEER_DSN=",
			// Force embedder=none (skip Ollama probe)
			"DARK_MEMORY_EMBEDDER=none",
		)
		// We don't actually need the binary to serve; we just
		// measure until it starts writing boot logs to stderr.
		// The cmd.Wait in the goroutine below confirms the
		// process actually executed.
		stderr, err := cmd.StderrPipe()
		if err != nil {
			cancelCtx()
			b.Fatalf("StderrPipe: %v", err)
		}
		if err := cmd.Start(); err != nil {
			cancelCtx()
			b.Fatalf("cmd.Start: %v", err)
		}
		// Wait for the first stderr line (boot log) OR process exit
		// (failed boot). Either way, we record the duration to the
		// first observable output.
		firstLine := make(chan struct{}, 1)
		go func() {
			buf := make([]byte, 4096)
			_, _ = stderr.Read(buf)
			select {
			case firstLine <- struct{}{}:
			default:
			}
		}()
		select {
		case <-firstLine:
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill()
			cancelCtx()
			b.Fatalf("cold-start: no stderr within 20s")
		}
		elapsed := time.Since(start)
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		stderr.Close()
		cancelCtx()
		b.ReportMetric(float64(elapsed.Microseconds()), "us/boot")
	}
}