package orchestration

// BootReconcile — Wave 5E.iii (INV-9 catch-up after crash).
//
// Runs ONCE at server boot to promote sessions left behind by a
// crashed harness. The semantic is identical to one sweeper tick
// (open→idle, idle→closed_aborted) but executed synchronously so the
// operator sees the result in the boot log before any MCP traffic
// is accepted.
//
// Why a separate entry point from Sweeper.Run:
//   - The sweeper is async + best-effort. BootReconcile is a blocking
//     one-shot the boot sequence waits on.
//   - On boot, ErrInvalidState is NOT a race (no other process is
//     touching the sessions table) — it means the migration backfill
//     already cleaned the row, or the prior server closed it cleanly
//     during shutdown. Both are benign and the sweeper's race-tolerant
//     handling of them applies equally here.
//   - We log a summary line so operators can confirm the catch-up
//     happened. Format is identical to sweeper log lines so operators
//     grep the same pattern.
//
// Wave 5E.iv bug-hunt: when DARK_SESSION_SWEEPER=off, this pass is
// intentionally SKIPPED — operators who turn off the sweeper want
// manual control over both the periodic and the boot-time
// reconciliation. The skip happens in StartSweeper, not here.

import (
	"context"
	"log"
)

// BootReconcile runs one tick of the sweeper synchronously. The
// returned SweeperOutput mirrors what Sweeper.Last() would expose
// after the next tick. Errors from individual session promotions
// are already counted in Output.Errors; the returned error is only
// non-nil for catastrophic failures (e.g. ListStaleSessions returning
// a transport error, which we already count + log inside runTick).
//
// Caller (server.Boot) should run this AFTER Store.Open + safety
// install but BEFORE the MCP server starts accepting traffic.
func BootReconcile(ctx context.Context, sw *Sweeper) SweeperOutput {
	out := sw.runTick(ctx)
	if out.OpenToIdle > 0 || out.IdleToAborted > 0 || out.Errors > 0 {
		log.Printf("dark-mem-mcp: boot_reconcile open->idle=%d idle->aborted=%d errors=%d duration=%s",
			out.OpenToIdle, out.IdleToAborted, out.Errors, out.Duration)
	} else {
		log.Printf("dark-mem-mcp: boot_reconcile clean (no stale sessions)")
	}
	sw.last = out
	return out
}