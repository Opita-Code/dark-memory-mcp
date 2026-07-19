// Package server — lifecycle.go: process boot + shutdown sequence.
//
// Per RFC §6:
//
//	boot:
//	  1. load config from env       (bootstrap.go)
//	  2. open Store via runtime.Open
//	  3. Store.Open runs migrations + constitution watchdog (INV-4)
//	  4. safety.NewCanary → install on Store (INV-3)
//	  5. mcp.NewServer → register all 26 tools
//	  6. stdio transport
//	  7. boot_reconcile (Wave 5E.iii) — one sweep to catch sessions
//	     left behind by a crashed prior harness
//	  8. sweeper goroutine (Wave 5E.iii) — INV-9 dual-timeout auto-promotion
//
//	shutdown:
//	  1. stop sweeper goroutine (cancel context)
//	  2. close all active sessions with reason='clean' (terminal —
//	     see Wave 5E.v; default is clean, opt-out via
//	     DARK_SHUTDOWN_CLOSE_REASON=aborted for resurrectable)
//	  3. flush write_audit marker (audit rows are atomic with
//	     data writes; this step is a timing marker)
//	  4. close Store
//	  (mcp server stop happens at the transport layer, before
//	  Shutdown runs — ServeStdio returns first, then defers fire.)
//	Wave 5E.iv bug-hunt: previous header listed 5 steps but code
//	had 4 with a duplicate Step 2. Now 4 steps + correct numbering.
//
// Wave 5E.v (L6 adapter integration) maps the 3 lifecycle hooks from
// BRIDGE_AND_COEXISTENCE.md §6 to this package:
//
//   - startup-recover → SessionRecover is wired into main.go between
//     StartSweeper and ServeStdio. Logs a discoverable line for the
//     operator; auto-resurrection gated behind DARK_AUTO_RESURRECT.
//
//   - periodic-heartbeat → the sweeper goroutine (5E.iii) IS the
//     periodic heartbeat. It promotes stale sessions per INV-9.
//     Per-session heartbeat is operator-driven via
//     dark_memory_session_heartbeat.
//
//   - exit-close_clean → this file's Shutdown step 2 now closes
//     sessions with reason='clean' (terminal).
package server

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/safety"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
	"github.com/dark-agents/dark-memory-mcp/internal/tools"
)

// BootState carries the resolved boot handles. The Server keeps a
// reference to it for the shutdown sequence.
type BootState struct {
	Config       *Config
	Store        store.Store
	Safety       *safety.Holder
	Orchestrator *orchestration.Orchestrator
	Registry     *tools.Registry

	// Sweeper is constructed by Boot() but NOT started. The main
	// binary calls StartSweeper(ctx) after RegisterAll; Shutdown
	// calls StopSweeper before closing sessions + Store. nil if
	// INV-9 sweeper is disabled via env (DARK_SESSION_SWEEPER=off).
	Sweeper *orchestration.Sweeper

	// sweeperCancel cancels the sweeper goroutine. nil if the
	// sweeper never started.
	sweeperCancel context.CancelFunc

	// sweeperWG waits on the sweeper goroutine exit so Shutdown
	// is deterministic (no orphan goroutine after close).
	sweeperWG sync.WaitGroup

	// sweeperMu guards sweeperCancel + sweeperWG from concurrent
	// Start/Stop calls (defensive — main.go is single-threaded
	// but tests may not be).
	sweeperMu sync.Mutex

	// shutdownOnce makes Shutdown idempotent. Wave 5E.iv bug-hunt:
	// before this guard, ServeStdio's defer + main.go's
	// `defer srv.Close()` both fired Shutdown, and the second run
	// logged errors when ListSessions / CloseSession ran against a
	// closed Store. sync.Once collapses both into a single execution.
	shutdownOnce sync.Once
}

// Boot runs the 6-step boot sequence. Steps 1-4 happen here; step 5
// (register 26 tools) is the caller's responsibility because the
// registry is populated by the per-namespace tool files at package
// init time (or via explicit Register* calls in tests). Step 6 is
// ServeStdio (separate, called after Boot returns).
//
// On any failure, Boot cleans up partial state and returns an error
// — caller should log + exit 1, never panic.
func Boot(ctx context.Context) (*BootState, error) {
	// Step 1: load config.
	cfg, err := LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("server.Boot step1 (load config): %w", err)
	}
	log.Printf("dark-mem-mcp: boot step1 ok driver=%s dsn=%s", cfg.DBDriver, redactDSN(cfg.DBDSN))

	// Step 2: open Store via runtime.Open. The runtime package
	// selects the right driver (sqlite or postgres) and returns the
	// Store interface.
	st, err := runtime.Open(ctx, cfg.StoreConfig())
	if err != nil {
		return nil, fmt.Errorf("server.Boot step2 (runtime.Open): %w", err)
	}
	log.Printf("dark-mem-mcp: boot step2 ok driver=%s", st.DriverName())

	// Step 3: log the schema state after migrations ran. The
	// actual migration + constitution watchdog ran inside
	// runtime.Open (step 2); on constitution drift the open
	// returns ErrConstitutionDrift and we refused to boot. Here
	// we just query the resulting schema version and emit a
	// line the operator can pattern-match on to confirm the
	// schema is at the expected version. The query is cheap
	// (one SELECT MAX(version) on schema_migrations).
	schemaVer, err := st.SchemaVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("server.Boot step3 (SchemaVersion): %w", err)
	}
	log.Printf("dark-mem-mcp: boot step3 ok migrations applied + constitution watchdog (driver=%s schema_version=%d)", st.DriverName(), schemaVer)

	// Step 4: install canary on the Store. The Holder is shared with
	// the orchestrator so orchestrator calls see the same canary
	// state.
	safe := installCanary(st)
	log.Printf("dark-mem-mcp: boot step4 ok canary installed (present=%v)", !safe.Active().IsZero())

	// Construct the orchestrator. WithBackends / WithLLMSelector can
	// be applied by the caller after Boot (e.g. for tests).
	orch := orchestration.New(st, safe)

	// Construct the registry. Tools are added by the per-namespace
	// tool files via Register* functions called by the binary's
	// init-time setup (or explicitly in tests).
	reg := tools.NewRegistry()

	// Construct the sweeper (Wave 5E.iii / INV-9). The sweeper is
	// NEVER nil here — even when DARK_SESSION_SWEEPER=off the
	// Sweeper is constructed but StartSweeper becomes a no-op.
	// This keeps BootState.Sweeper type-stable for callers that
	// always read Last() after a tick.
	sw := orchestration.NewSweeper(st, orchestration.SweeperInput{})

	return &BootState{
		Config:       cfg,
		Store:        st,
		Safety:       safe,
		Orchestrator: orch,
		Registry:     reg,
		Sweeper:      sw,
	}, nil
}

// StartSweeper starts the background sweeper goroutine AND runs the
// one-shot boot_reconcile pass first. Idempotent — calling twice
// returns nil with no side effect (the second call sees sweeperCancel
// already non-nil and skips).
//
// If DARK_SESSION_SWEEPER=off, both the goroutine start AND the boot
// reconcile pass are skipped — the operator has turned INV-9 off
// entirely. Sessions in that mode are only ever transitioned by
// explicit tool calls (session_close / session_resurrect).
//
// Returns an error only if boot_reconcile itself errored; the
// goroutine start never errors (its failures are counted per-tick
// in Sweeper.Last().Errors).
func (b *BootState) StartSweeper(ctx context.Context) error {
	if b == nil || b.Sweeper == nil {
		return nil
	}
	if os.Getenv("DARK_SESSION_SWEEPER") == "off" {
		log.Printf("dark-mem-mcp: sweeper disabled via DARK_SESSION_SWEEPER=off")
		return nil
	}
	b.sweeperMu.Lock()
	defer b.sweeperMu.Unlock()
	if b.sweeperCancel != nil {
		// Already started.
		return nil
	}

	// One-shot boot_reconcile FIRST. Catches sessions left behind by
	// a crashed prior harness. Runs synchronously so the operator
	// sees the result in the boot log before MCP traffic starts.
	orchestration.BootReconcile(ctx, b.Sweeper)

	// Then start the periodic goroutine.
	sweepCtx, cancel := context.WithCancel(context.Background())
	b.sweeperCancel = cancel
	b.sweeperWG.Add(1)
	go func() {
		defer b.sweeperWG.Done()
		b.Sweeper.Run(sweepCtx)
	}()
	return nil
}

// StopSweeper cancels the sweeper goroutine and waits for it to exit.
// Idempotent — calling without StartSweeper first is a no-op.
// Always returns nil; partial-failure paths log and continue.
func (b *BootState) StopSweeper() {
	if b == nil || b.Sweeper == nil {
		return
	}
	b.sweeperMu.Lock()
	cancel := b.sweeperCancel
	b.sweeperCancel = nil
	b.sweeperMu.Unlock()
	if cancel != nil {
		cancel()
		b.sweeperWG.Wait()
		log.Printf("dark-mem-mcp: sweeper stopped cleanly")
	}
}

// Shutdown runs the 4-step shutdown sequence. Idempotent — safe to
// call multiple times via sync.Once (Wave 5E.iv bug-hunt: previously
// the function was documented as "idempotent" but actually wasn't —
// a second call would re-list sessions against a closed Store and
// emit error logs). Always returns nil; partial-failure paths
// log and continue.
func (b *BootState) Shutdown(ctx context.Context) error {
	if b == nil {
		return nil
	}
	b.shutdownOnce.Do(func() {
		// Step 1: stop the sweeper goroutine (Wave 5E.iii). Must happen
		// BEFORE we close sessions in step 2 — otherwise the sweeper
		// might race a shutdown-time CloseSession with an aborted close.
		b.StopSweeper()

		// Step 2: close all active sessions. Skipped if no active
		// project is set (ListSessions requires one under INV-7); a
		// fresh server that booted but received no calls will have no
		// active project, and there's nothing to close.
		//
		// Wave 5E.v (L6 adapter integration, exit-close_clean hook):
		// default close reason is 'clean' (terminal — operator opted
		// into clean shutdown). Operators who want shutdown to leave
		// sessions resurrectable set
		// DARK_SHUTDOWN_CLOSE_REASON=aborted. The 'aborted' default
		// from before 5E.v silently converted every graceful exit
		// into a resurrectable state — wrong default. Clean is the
		// operator-visible contract: "I asked the server to shut down,
		// so my sessions are done."
		closeReason := shutdownCloseReason()
		if b.Store != nil && b.Store.ActiveProject() != "" {
			sessions, err := b.Store.ListSessions(ctx, 10000)
			if err == nil {
				for _, s := range sessions {
					if s.Status != "open" {
						continue
					}
					wc := store.WriteContext{
						Actor:     "server_shutdown",
						WritePath: "Shutdown",
					}
					// Wave 5E.v: reason comes from env (default 'clean').
					// See BRIDGE_AND_COEXISTENCE.md §6 for the hook.
					if err := b.Store.CloseSession(ctx, wc, s.SessionID, closeReason); err != nil {
						log.Printf("dark-mem-mcp: shutdown step2 close session %s: %v", s.SessionID, err)
					}
				}
			} else {
				log.Printf("dark-mem-mcp: shutdown step2 list sessions: %v", err)
			}
			log.Printf("dark-mem-mcp: shutdown step2 ok sessions closed (reason=%s)", closeReason)
		}

		// Step 3: write_audit is flushed atomically with each data
		// write (INV-1) so there is no pending buffer to flush at
		// this layer. We log a marker so the operator can correlate
		// shutdown timing with the last write.
		log.Printf("dark-mem-mcp: shutdown step3 ok (audit flushed atomically with writes)")

		// Step 4: close Store.
		if b.Store != nil {
			if err := b.Store.Close(); err != nil {
				log.Printf("dark-mem-mcp: shutdown step4 close store: %v", err)
			} else {
				log.Printf("dark-mem-mcp: shutdown step4 ok store closed")
			}
		}
	})
	return nil
}

// installCanary constructs a fresh random canary and pushes it into
// the Store's canary slot (via Store.SetCanary). Returns the Holder
// the orchestrator uses so they share the same canary state.
//
// Standard pattern: Boot installs a random canary. Tests that want
// a fixed canary should install it BEFORE calling Boot (via the
// Store interface directly).
func installCanary(st store.Store) *safety.Holder {
	holder := &safety.Holder{}
	holder.Set(safety.NewCanary())
	st.SetCanary(holder.Active().String())
	return holder
}

// shutdownCloseReason resolves the close reason Shutdown will use
// for every open session. Wave 5E.v (L6 adapter integration,
// exit-close_clean hook).
//
//   - DARK_SHUTDOWN_CLOSE_REASON=clean (default): terminal, NOT
//     resurrectable. Matches operator intent ("I asked the server
//     to stop").
//   - DARK_SHUTDOWN_CLOSE_REASON=aborted: resurrectable per INV-8.
//     For crash-recovery workflows where the operator expects the
//     next harness to call SessionRecover + SessionResurrect.
//   - Anything else: defaults to 'clean' (defensive).
//
// Validates the value against session.CloseReason.Validate() so an
// invalid env setting doesn't crash Shutdown; logs a warning and
// proceeds with 'clean'.
func shutdownCloseReason() string {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("DARK_SHUTDOWN_CLOSE_REASON")))
	if raw == "" {
		return "clean"
	}
	switch raw {
	case "clean", "aborted":
		return raw
	default:
		log.Printf("dark-mem-mcp: DARK_SHUTDOWN_CLOSE_REASON=%q invalid (want clean|aborted); defaulting to clean", raw)
		return "clean"
	}
}

// redactDSN strips credentials from a DSN for log safety. Postgres
// DSNs come in two flavours:
//   - Keyword form: "host=localhost port=5432 user=app password=secret dbname=mcp"
//   - URI form:     "postgresql://app:secret@localhost:5432/mcp"
// We redact the password component in both. SQLite paths are file
// paths and have no embedded secrets.
//
// Wave 5E.iv bug-hunt: the previous impl only handled keyword form
// and hand-rolled case-insensitive substring search. Two bugs:
//   - URI form leaked the password verbatim (security).
//   - O(n*m) hand-rolled scan replaced with strings.EqualFold.
func redactDSN(dsn string) string {
	if dsn == "" {
		return "<empty>"
	}
	// URI form: postgresql://user:password@host:port/db?sslmode=...
	// Also covers postgres:// scheme and any <scheme>://<userinfo>@... shape.
	if schemeEnd := strings.Index(dsn, "://"); schemeEnd >= 0 {
		rest := dsn[schemeEnd+3:]
		if atIdx := strings.Index(rest, "@"); atIdx >= 0 {
			userinfo := rest[:atIdx]
			hostPart := rest[atIdx+1:]
			// userinfo is "user" or "user:password".
			if colonIdx := strings.Index(userinfo, ":"); colonIdx >= 0 {
				user := userinfo[:colonIdx]
				redacted := dsn[:schemeEnd+3] + user + ":<redacted>@" + hostPart
				return redacted
			}
			// No password in userinfo — nothing to redact.
			return dsn
		}
		// URI form without userinfo — nothing to redact.
		return dsn
	}
	// Keyword form: scan for password= / Password= (case-insensitive)
	// and redact the value up to the next space.
	lower := strings.ToLower(dsn)
	for _, key := range []string{"password=", "passwd="} {
		if idx := strings.Index(lower, key); idx >= 0 {
			end := idx + len(key)
			for end < len(dsn) && dsn[end] != ' ' && dsn[end] != '\n' && dsn[end] != '\t' {
				end++
			}
			return dsn[:idx+len(key)] + "<redacted>" + dsn[end:]
		}
	}
	return dsn
}
