// Command dark-mem-mcp-supervisor is the Phase 10 Chunk 10.2
// wrapper that solves the harness's "1 spawn attempt, then give
// up" problem.
//
// Why this exists
// ----------------
// The opencode harness's behavior on MCP `local` death (verified
// empirically 2026-10-02 + 2026-10-03):
//
//   - First death: harness respawn 1x. New PID.
//   - Respawn fails: harness marks the MCP as "dead" and DOES NOT
//     retry. The dark-memory tools disappear from the LLM's tool
//     list for the rest of the opencode session.
//
// The harness has no `auto_relaunch_on_browser_death` flag (unlike
// dark-copilot). The deploy-mcp-binary.md procedure relies on the
// harness to respawn after the binary exits — but in practice, the
// respawn either doesn't happen or fails the first time.
//
// What this wrapper does
// ----------------------
// Spawns `bin/dark-mem-mcp.exe` as a child process with stdio
// inherited. When the child exits, the wrapper respawns it after
// a 1-second backoff. The wrapper itself never exits (unless the
// harness kills it, which is the only legitimate termination).
//
// The harness spawns THIS wrapper, not the binary. The harness's
// stdio pipes are connected to the wrapper. The wrapper proxies
// stdio to the child (Go's os/exec inheritance does this by
// default on Windows for cmd.Stdin/Stdout/Stderr).
//
// From the harness's view, the wrapper is always alive — it never
// sees the child's death. The MCP handshake (initialize →
// tools/list) happens with the child through the wrapper's stdio
// proxy. Subsequent tool calls are also proxied. When the child
// dies, the wrapper restarts it; the harness sees a brief pause
// (the wrapper's brief restart loop) but no death event.
//
// Trade-offs
// ----------
// - The wrapper doesn't add process supervision beyond restart-loop.
//   It does NOT install signal handlers (no SIGHUP-equivalent),
//   does NOT log to a file, does NOT expose health metrics.
//   Those are out of scope for Chunk 10.2.
// - The wrapper exits if Go's stdlib exec fails repeatedly
//   (catastrophic error, e.g., binary missing). Operators will
//   see this as a harness "death" and need to restart opencode.
// - The wrapper inherits all env vars from the harness via
//   os.Environ(). This is the correct behavior — the harness
//   sets DARK_DB, DARK_JUDGE_PROVIDER, etc., and the child
//   needs them.
//
// Why not os.Exec (Unix "suckless re-exec")?
// ------------------------------------------
// os.Exec is not supported on Windows. Go's os.StartProcess
// spawns a child, but stdio pipes need explicit handle
// inheritance (bInheritHandles=true via syscall.SysProcAttr).
// On Windows, the reliable pattern is the wrapper — let the
// wrapper be a stable parent and spawn children that inherit
// pipes. Same model as daemontools, runit, s6, etc.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func main() {
	// Resolve binary path: same directory as this supervisor,
	// canonical name `dark-mem-mcp.exe`. After deploy procedure
	// atomic rename, this path points to the freshly-deployed
	// binary. Hardcoded — we don't want the wrapper to follow
	// symlinks or env vars that could mislead it.
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"dark-mem-mcp-supervisor: os.Executable failed: %v\n", err)
		os.Exit(1)
	}
	binaryPath := filepath.Join(filepath.Dir(exe), "dark-mem-mcp.exe")
	if _, err := os.Stat(binaryPath); err != nil {
		fmt.Fprintf(os.Stderr,
			"dark-mem-mcp-supervisor: binary %s not found: %v\n",
			binaryPath, err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr,
		"dark-mem-mcp-supervisor: supervising %s (pid=%d)\n",
		binaryPath, os.Getpid())

	attempts := 0
	maxAttempts := 100000 // effectively infinite for any realistic session
	for {
		attempts++
		// Spawn the binary as a child. Stdio is inherited from
		// this process (the wrapper). On Windows, the new
		// process's stdin/stdout/stderr handles are connected
		// to whatever the wrapper's handles are connected to —
		// which is the harness's pipes (because the harness
		// spawned the wrapper with those pipes).
		cmd := exec.Command(binaryPath, os.Args[1:]...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Env = os.Environ()
		err := cmd.Start()
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"dark-mem-mcp-supervisor: spawn failed (attempt=%d): %v — exiting\n",
				attempts, err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr,
			"dark-mem-mcp-supervisor: spawned child pid=%d (attempt=%d)\n",
			cmd.Process.Pid, attempts)

		// Block until the child exits.
		waitErr := cmd.Wait()

		// The child exited. Log + backoff + respawn.
		fmt.Fprintf(os.Stderr,
			"dark-mem-mcp-supervisor: child pid=%d exited (err=%v, attempt=%d)\n",
			cmd.Process.Pid, waitErr, attempts)

		if attempts >= maxAttempts {
			fmt.Fprintf(os.Stderr,
				"dark-mem-mcp-supervisor: max attempts (%d) reached, exiting\n",
				maxAttempts)
			os.Exit(1)
		}

		// 1s backoff before respawn. The deploy atomic rename
		// typically completes in <1s; this gives the operator
		// enough time to land the new binary before the next
		// child boot attempt.
		time.Sleep(1 * time.Second)
	}
}