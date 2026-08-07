package orchestration

// SessionSweeper — Wave 5E.iii (INV-9 auto-promotion).
//
// The sweeper is a background goroutine that periodically promotes
// stale sessions per the dual-timeout policy declared in INV-9:
//
//   - IDLE_TIMEOUT  (env DARK_SESSION_IDLE_TIMEOUT, default 15m):
//     `open` sessions whose last_heartbeat_at is older than this are
//     demoted to `idle`. The session is still resumable but its
//     hot context is considered stale.
//
//   - HEARTBEAT_TIMEOUT (env DARK_SESSION_HEARTBEAT_TIMEOUT, default 60m):
//     `idle` sessions whose last_heartbeat_at is older than this are
//     closed with reason='aborted' (resurrectable per INV-8). The
//     harness clearly died and the operator can call SessionRecover
//     to find the candidate + SessionResurrect to revive.
//
// The sweeper is owned by the server lifecycle: started by Run() with
// context.Background(), stopped on shutdown via cancel(). It is a
// BEST-EFFORT process — failures on individual sessions are logged
// and the sweeper continues with the next session, never aborting
// the loop. The sweeper does NOT own audit emission; each transition
// goes through Store.CloseSession which writes the write_audit row
// with session_event='aborted' (the canonical transition).

import (
	"context"
	"errors"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/errorobs"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// Default timeouts. Operators can override via env.
//
// v2.10.0 (2026-08-04): defaults raised from 60s/300s to 15m/60m —
// the old 5-minute heartbeat timeout killed ACTIVE sessions on
// interactive harnesses (opencode, Claude Code) that do not emit
// periodic heartbeats: any thinking pause > 5 min → closed_aborted
// → ErrFrameStaleTooFar on the next tool call. 60 minutes without a
// heartbeat means the harness genuinely died (INV-8 resurrectable);
// 15 minutes of silence is the "likely idle" signal, not "dead".
const (
	defaultIdleTimeout      = 15 * time.Minute
	defaultHeartbeatTimeout = 60 * time.Minute
	defaultTickInterval     = 30 * time.Second
	defaultBatchLimit       = 200
)

// SweeperInput configures the background sweeper. Zero values use the
// defaults above.
type SweeperInput struct {
	// IdleTimeout: `open` → `idle` if last_heartbeat_at < now - IdleTimeout.
	// 0 → use DARK_SESSION_IDLE_TIMEOUT env or default (15m).
	IdleTimeout time.Duration

	// HeartbeatTimeout: `idle` → `closed_aborted` if last_heartbeat_at
	// < now - HeartbeatTimeout. 0 → use DARK_SESSION_HEARTBEAT_TIMEOUT
	// env or default (60m).
	HeartbeatTimeout time.Duration

	// TickInterval: how often the sweeper runs. 0 → default (30s).
	TickInterval time.Duration

	// BatchLimit: max sessions to promote per tick (defensive cap).
	// 0 → default (200).
	BatchLimit int

	// Actor: write_audit Actor string for transitions.
	// Empty → "session_sweeper".
	Actor string
}

// SweeperOutput is the per-tick summary; useful for tests + observability.
type SweeperOutput struct {
	TickAt           time.Time `json:"tick_at"`
	OpenToIdle       int       `json:"open_to_idle"`
	IdleToAborted    int       `json:"idle_to_aborted"`
	Errors           int       `json:"errors"`
	Duration         string    `json:"duration"`
	IdleTimeout      string    `json:"idle_timeout"`
	HeartbeatTimeout string    `json:"heartbeat_timeout"`
}

// Sweeper is the long-running background process. Use NewSweeper to
// construct; call Run with a cancellable context to start the loop.
// Run returns when ctx is cancelled.
type Sweeper struct {
	in   SweeperInput
	st   store.Store
	last SweeperOutput
}

// NewSweeper constructs a Sweeper bound to a Store. The Store's
// active project determines which sessions are visible (INV-7).
func NewSweeper(st store.Store, in SweeperInput) *Sweeper {
	return &Sweeper{in: resolveSweeperInput(in), st: st}
}

// Last returns the most recent tick summary. Useful for tests and
// for exposing metrics via a future dark_memory_session_status tool.
func (sw *Sweeper) Last() SweeperOutput { return sw.last }

// recordErr captures one sweeper error in the Error Observatory
// (v2.11.0, spec 757). Best-effort: a broken telemetry write must
// not break the sweeper loop. The in-memory out.Errors counter stays
// the primary signal (it is what tests assert); this is the durable
// side.
func (sw *Sweeper) recordErr(ctx context.Context, sessionID string, err error) {
	if err == nil {
		return
	}
	e := errorobs.New(sw.st.ActiveProject(), sessionID, "session_sweeper", err).WithSeverity(errorobs.SeverityWarn)
	if err := sw.st.SaveErrorEvent(ctx, e); err != nil {
		// Recording degraded; the in-memory counter still caught it.
		log.Printf("dark-mem-mcp: sweeper recordErr: %v", err)
	}
}

// Run starts the sweeper loop. Blocks until ctx is cancelled. Each
// tick calls runTick once; the goroutine sleeps for TickInterval
// between ticks. Failures on individual sessions are logged and do
// NOT abort the loop.
func (sw *Sweeper) Run(ctx context.Context) {
	tick := time.NewTicker(sw.in.TickInterval)
	defer tick.Stop()
	log.Printf("dark-mem-mcp: sweeper started idle_timeout=%s heartbeat_timeout=%s tick=%s",
		sw.in.IdleTimeout, sw.in.HeartbeatTimeout, sw.in.TickInterval)
	for {
		select {
		case <-ctx.Done():
			log.Printf("dark-mem-mcp: sweeper stopped (%v)", ctx.Err())
			return
		case <-tick.C:
			out := sw.runTick(ctx)
			sw.last = out
			if out.OpenToIdle > 0 || out.IdleToAborted > 0 || out.Errors > 0 {
				log.Printf("dark-mem-mcp: sweeper tick open->idle=%d idle->aborted=%d errors=%d duration=%s",
					out.OpenToIdle, out.IdleToAborted, out.Errors, out.Duration)
			}
		}
	}
}

// runTick performs one promotion pass. Exposed via Sweeper.Last for
// tests; not part of the orchestrator public surface (don't call
// directly — the loop owns cadence).
func (sw *Sweeper) runTick(ctx context.Context) SweeperOutput {
	start := time.Now().UTC()
	now := start
	out := SweeperOutput{
		TickAt:           start,
		IdleTimeout:      sw.in.IdleTimeout.String(),
		HeartbeatTimeout: sw.in.HeartbeatTimeout.String(),
	}

	// Pass 1: open → idle. Sessions that haven't heartbeated for
	// IdleTimeout get demoted (still alive, but stale).
	idleCutoff := now.Add(-sw.in.IdleTimeout)
	staleOpen, err := sw.st.ListStaleSessions(ctx,
		[]string{string(session.StatusOpen)}, idleCutoff, sw.in.BatchLimit)
	if err != nil {
		log.Printf("dark-mem-mcp: sweeper list stale open: %v", err)
		sw.recordErr(ctx, "", err)
		out.Errors++
	} else {
		for i := range staleOpen {
			// PromoteSessionStatus is the focused status-only transition
			// (SaveSession is INSERT-only, can't UPDATE). It validates
			// (open → idle) and emits write_audit with session_event='promote'.
			s := staleOpen[i]
			wc := store.WriteContext{
				Actor:     sw.in.Actor + ":open_to_idle",
				WritePath: "Sweeper.runTick",
				SessionID: s.SessionID,
			}
			if err := sw.st.PromoteSessionStatus(ctx, wc, s.SessionID, string(session.StatusIdle)); err != nil {
				// ErrInvalidState is benign race: another sweeper tick
				// (or session_heartbeat) already moved this session.
				if errors.Is(err, store.ErrInvalidState) {
					continue
				}
				log.Printf("dark-mem-mcp: sweeper promote open->idle %s: %v", s.SessionID, err)
				sw.recordErr(ctx, s.SessionID, err)
				out.Errors++
				continue
			}
			out.OpenToIdle++
		}
	}

	// Pass 2: idle → closed_aborted. Sessions that have been idle
	// past HeartbeatTimeout have their harness clearly dead. We close
	// them with reason='aborted' (resurrectable per INV-8).
	abortedCutoff := now.Add(-sw.in.HeartbeatTimeout)
	staleIdle, err := sw.st.ListStaleSessions(ctx,
		[]string{string(session.StatusIdle)}, abortedCutoff, sw.in.BatchLimit)
	if err != nil {
		log.Printf("dark-mem-mcp: sweeper list stale idle: %v", err)
		sw.recordErr(ctx, "", err)
		out.Errors++
	} else {
		for i := range staleIdle {
			s := staleIdle[i]
			wc := store.WriteContext{
				Actor:     sw.in.Actor + ":idle_to_aborted",
				WritePath: "Sweeper.runTick",
				SessionID: s.SessionID,
			}
			if err := sw.st.CloseSession(ctx, wc, s.SessionID, "aborted"); err != nil {
				// ErrInvalidState happens when the session was already
				// closed/heartbeated between the ListStale call and
				// here — race condition, not a real failure.
				if errors.Is(err, store.ErrInvalidState) {
					continue
				}
				log.Printf("dark-mem-mcp: sweeper close idle->aborted %s: %v", s.SessionID, err)
				sw.recordErr(ctx, s.SessionID, err)
				out.Errors++
				continue
			}
			// v2.0.2: clear the project's active_session_id pointer
			// (compare-and-set on session_id). Best-effort: if the
			// sweeper-aborted session was no longer the active
			// pointer (operator started a new session meanwhile),
			// CAS does nothing. No way to log without scanning
			// the project row; the next session_start will heal.
			//
			// The sessions table has no project_id column (project
			// is per-session via SetActiveProject at start time
			// and doesn't persist on the row), so we use the
			// store-wide active project that the sweeper is bound
			// to. If a multi-project sweeper is added later, this
			// will need to be parametrised.
			if projID := sw.st.ActiveProject(); projID != "" {
				if err := sw.st.ClearActiveSession(ctx, projID, s.SessionID); err != nil {
					// v2.11.0 (spec 757): was a silent discard; now
					// captured durably (Warn — the close succeeded,
					// the pointer clear missed).
					sw.recordErr(ctx, s.SessionID, err)
				}
			}
			out.IdleToAborted++
		}
	}

	out.Duration = time.Since(start).String()
	return out
}

// resolveSweeperInput applies env-overrides and defaults to a zero
// SweeperInput. The lookup order for each field is: input → env → default.
// Exported tests use this to make assertions about resolved values.
func resolveSweeperInput(in SweeperInput) SweeperInput {
	if in.IdleTimeout <= 0 {
		in.IdleTimeout = envDuration("DARK_SESSION_IDLE_TIMEOUT", defaultIdleTimeout)
	}
	if in.HeartbeatTimeout <= 0 {
		in.HeartbeatTimeout = envDuration("DARK_SESSION_HEARTBEAT_TIMEOUT", defaultHeartbeatTimeout)
	}
	if in.TickInterval <= 0 {
		in.TickInterval = envDuration("DARK_SESSION_SWEEP_INTERVAL", defaultTickInterval)
	}
	if in.BatchLimit <= 0 {
		in.BatchLimit = defaultBatchLimit
	}
	if in.Actor == "" {
		in.Actor = "session_sweeper"
	}
	return in
}

// envDuration reads a duration from env (seconds as float) with a default.
func envDuration(key string, def time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	// Try as int first (most common: "60", "300").
	if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	// Fall back to Go duration syntax ("60s", "5m").
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	return def
}
