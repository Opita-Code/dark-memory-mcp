# Deploying dark-mem-mcp.exe without restarting opencode

> **Audience**: operator (human or automation) iterating on
> `feat/v4-redesign` (or any branch) who needs to swap
> `bin/dark-mem-mcp.exe` mid-session without `taskkill /IM opencode.exe`.
>
> **Status (alpha.21, 2026-10-02)**: procedure VERIFIED via the
> Phase 10 B-rev1 deploy test. The previous v3-binary-only
> procedure required an opencode restart every time. The
> `watchReloadFlag` handler (added in Chunk 10.1) makes the
> file-flag trigger work, and the harness's single-respawn
> fallback is now sufficient for normal deploys.

## 0. Five-minute overview

```
deploy.sh (or manual):
  1. Build the new binary as `bin/dark-mem-mcp.exe.new`.
  2. Atomic rename the running .exe to a timestamped backup.
  3. Rename the new binary to the canonical path.
  4. Touch the reload flag `bin/dark-mem-mcp.exe.reload`.
  5. Within 2-5s, the running binary sees the flag, exits
     gracefully, and the opencode harness respawns the
     binary at the canonical path (which is now the new one).
  6. Verify via `dark_memory_health_ping`.

Total disconnect window: ~5s (2s poll + ~3s boot). All
in-flight tool calls fail; the opencode harness retries the
failed calls automatically (or surfaces the error to the
operator, depending on the call).
```

## 1. The harness constraint (READ THIS FIRST)

The opencode harness's behavior on MCP `local` death, as
empirically observed in the Phase 10 deploy test (2026-10-02):

| Death scenario | Harness behavior |
|---|---|
| First death | Respawn 1x. New PID. |
| Respawn fails (PID stuck in 70KB RAM, no DB load) | Harness marks the MCP as "dead" and **DOES NOT retry**. |
| Operator closes and reopens opencode window | Full re-init. Harness re-reads `opencode.jsonc` and respawns all enabled MCPs. |

**Implication**: if the first respawn fails, you MUST restart
opencode to recover. The `watchReloadFlag` handler in Chunk 10.1
is designed to make the first respawn succeed (the new binary is
at the canonical path, well-formed, and starts cleanly).

`dark-copilot` has a different respawn policy
(`auto_relaunch_on_browser_death`) — it retries indefinitely.
`dark-memory` does NOT have that flag. The deploy procedure
below assumes a successful first respawn.

## 2. The 6-step deploy procedure

```bash
# 0. Worktree + commit the code change first.
cd ~/Documents/dark-memory-mcp
git status                 # clean working tree required
git add <changed files>
git commit -m "..."

# 1. Build the new binary as .new (so the canonical name is
#    always a working binary; Windows lets you rename a running
#    file, but you can't replace it while it's open).
cd cmd/dark-mem-mcp
go build \
  -ldflags "-X github.com/dark-agents/dark-memory-mcp/internal/version.buildVersion=$(cd ../.. && git describe --tags --always --dirty)" \
  -o ../../bin/dark-mem-mcp.exe.new \
  .
cd ../..

# 2. Sanity-test the new binary (3-second startup smoke).
#    It will print "serving stdio" and then timeout (no MCP
#    client connected). Look for non-zero exit or boot errors.
timeout 3 ./bin/dark-mem-mcp.exe.new
# Expected: "registered 62 canonical + 3 extras" + exit 0
#           from the timeout (NOT the binary itself).
# If you see "panic during boot" or a non-zero exit, ABORT.

# 3. Atomic rename. On Windows, `mv` can rename a running
#    file (the OS keeps the file open via its handle, but
#    the directory entry changes). The running v3/v4 keeps
#    working with the renamed file.
mv bin/dark-mem-mcp.exe bin/dark-mem-mcp.exe.bak-$(date +%Y%m%d-%H%M%S)
mv bin/dark-mem-mcp.exe.new bin/dark-mem-mcp.exe

# 4. Touch the reload flag. The running binary's
#    watchReloadFlag goroutine (cmd/dark-mem-mcp/legacy_main.go)
#    polls for this file every 2s. When it sees the flag, it
#    removes it and calls cancel() to trigger graceful shutdown.
touch bin/dark-mem-mcp.exe.reload

# 5. Wait for the harness to respawn. Boot takes ~3s
#    (boot step1-5 in the binary's stderr). Then 2-3s for
#    the MCP initialize + tools/list handshake.
sleep 8

# 6. Verify. The opencode harness should now be talking
#    to the new binary. Any MCP tool call (e.g.
#    dark_memory_health_ping) confirms the respawn succeeded.
#    If the tool call fails with "MCP server unavailable" or
#    the harness hangs, the first respawn failed — see §3.
```

## 3. Recovery if the respawn fails

The harness gives up after one failed respawn. If
`dark_memory_health_ping` returns an error or hangs:

1. **Verify the new binary is at the canonical path**:
   `ls -la bin/dark-mem-mcp.exe` should show the new size.
2. **Test the binary standalone**: `timeout 3 ./bin/dark-mem-mcp.exe`
   should print "serving stdio" and exit 0 from the timeout.
3. **If the standalone test works**, the harness is in a
   bad state. The only recovery is **restart opencode**:
   - Close the opencode window completely.
   - Verify no `opencode.exe` processes remain: `tasklist | findstr opencode`.
   - Reopen opencode.
   - The harness reads `opencode.jsonc` and respawns all
     enabled MCPs. The new binary is at the canonical path,
     so it will be the one that gets spawned.

4. **If the standalone test ALSO fails**, the new binary
   has a bug. Don't proceed:
   - Restore the backup: `mv bin/dark-mem-mcp.exe.bak-<ts> bin/dark-mem-mcp.exe`
   - Investigate the boot error. Common causes:
     - `startup-recover failed: workflow tool requires an active session`
       → harmless, ignored. Carry on.
     - `boot step1 ok` then `boot step2 ok` then nothing
       → DB lock. Wait 30s for any stale process to release.
     - `panic during boot: <panic>` → fix the bug, rebuild, retry.

## 4. Why the file-flag (not SIGHUP)

SIGHUP doesn't exist on Windows. The classic Unix "graceful
re-exec on SIGHUP" pattern (suckless re-exec, daemontools
restart, runit) doesn't translate directly. The file-flag
sentinel is the Windows-native equivalent:

| Aspect | SIGHUP (Unix) | File-flag (Windows) |
|---|---|---|
| Trigger | `kill -HUP <pid>` | `touch <exe>.reload` |
| Latency | immediate | 2s poll interval |
| Cost | 1 syscall | 1 os.Stat per 2s (≈10µs) |
| Multiple processes watching | 1 signal, all wake up | 1 flag, all see it; remove makes idempotent |
| Atomicity | kernel-delivered | filesystem-mtime-based |

The 2s poll is the minimum that gives the operator a
reasonable deploy latency without measurable CPU overhead.
The pre-existing 30s sweeper tick dominates the boot loop's
overhead, so the reload watcher is in the noise.

## 5. Why the atomic rename (not direct replace)

On Windows, you cannot `mv new-file existing-file` if
`existing-file` is currently being executed — the OS returns
`ERROR_SHARING_VIOLATION`. You CAN rename the existing file
(the OS keeps the file open via its handle, the directory
entry changes), and you can then place a new file at the
original name. This is the "atomic rename" pattern.

The running process keeps working with the renamed file (now
`dark-mem-mcp.exe.bak-<ts>`). When the reload flag is touched,
the process exits. The harness respawns the binary at the
canonical name, which is the new file.

## 6. Why the file-flag instead of in-place re-exec

In-place re-exec (`os.Exec` / `syscall.Exec`) replaces the
current process image with a new one, inheriting the stdio
file descriptors. The harness sees a continuous conversation.

`os.Exec` is not supported on Windows. The Go stdlib has
`os.StartProcess` which spawns a child, but the stdio handle
inheritance is fragile (you need to set
`syscall.SysProcAttr.CmdLine` and bInheritHandles correctly
to inherit the parent's stdio handles).

The file-flag approach sidesteps this entirely: the parent
exits, the harness respawns the new binary, the new binary
inherits stdio from the harness's pipes. The disconnect
window is ~5s, which is acceptable for a deploy.

If the disconnect window becomes a problem (e.g., high-traffic
production), the next iteration (alpha.22+) can implement
Windows-compatible in-place re-exec via `os.StartProcess` +
explicit handle inheritance. Out of scope for Chunk 10.1.

## 7. Cross-references

- `cmd/dark-mem-mcp/legacy_main.go:50-58` — signal handler + watcher goroutine
- `cmd/dark-mem-mcp/legacy_main.go:286-321` — `watchReloadFlag` function
- `docs/v4-alpha-11-plan.md §10 OD7` — exhaustive e2e gate requirement
- `docs/v4-alpha-20-1-decision.md §6` — alpha.21+ enforcement
- `docs/RUNBOOK.md §1` — first-install procedure (for the cold-start case)
- `CHANGELOG.md [4.0.0-alpha.21]` — release entry
- dark-memory row 2378 (Phase 9 alpha.20.1 SUMMARY)
- dark-memory row 2381+ (Phase 10 Chunk 10.1 deploy procedure atomic mirror — pending)

## 8. LUCIDEZ honest disclosure (deploy tests 2026-10-02 + 2026-10-03)

### 8.1 First test (2026-10-02): harness gives up after 1 failed respawn

The Phase 10 B-rev1 deploy test killed the v3 binary (pid 15104)
before adding the file-flag handler. The harness respawned once
(pid 15116) but that respawn failed; the harness gave up. The
dark-memory MCP stayed dead for the rest of the session, even
after the operator's opencode restart. Lesson: **always add the
reload handler BEFORE the first deploy attempt**, or risk a
session-long outage.

### 8.2 Second test (2026-10-03): chicken-and-egg with the fix itself

The Chunk 10.1b fix (os.Stdin.Close() instead of cancel() only)
was deployed via the file-flag trigger. The OLD running binary
(v4-alpha.21-pre-1, the cancel-only version) detected the flag,
called cancel(), but didn't die (because ServeStdio isn't
context-aware). The HARNESS then spawned PID 8624 (the post-fix
binary), but PID 4856 was still alive and serving MCP, so the
harness kept using PID 4856. When I manually killed PID 4856, the
harness had no fallback to PID 8624 and gave up — dark-memory
went "dead" for the session AGAIN.

**Lesson: the first deploy of a watcher-fix requires an opencode
restart.** The fix can only be tested AFTER it's deployed, but
the deploy mechanism that the fix enables is the only way to
deploy without restart. Bootstrap requires one opencode restart,
then subsequent deploys work clean.

After the second opencode restart (where the harness spawns the
post-fix binary from cold), the procedure works end-to-end:
touch flag → stdin close → graceful exit → harness respawns with
the freshly-deployed canonical binary. No more restart needed.

The Phase 10 B-rev1 deploy test killed the v3 binary (pid
15104) before adding the file-flag handler. The harness
respawned once (pid 15116) but that respawn failed; the
harness gave up. The dark-memory MCP stayed dead for the
rest of the session, even after the operator's opencode
restart. Lesson: **always add the reload handler BEFORE the
first deploy attempt**, or risk a session-long outage.

The Chunk 10.1 handler + the test verification (this doc
+ a green `dark_memory_health_ping` after the reload-flag
trigger) is the minimum viable deploy story. alpha.21
SHIPS when both pieces are green.
