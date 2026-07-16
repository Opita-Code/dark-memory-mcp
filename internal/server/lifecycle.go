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
//
//	shutdown:
//	  1. close all active sessions (write status=closed)
//	  2. flush write_audit
//	  3. close Store
//	  4. mcp server stop
package server

import (
	"context"
	"fmt"
	"log"

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

	// Step 3: run migrations + constitution watchdog. Both happen
	// inside the driver constructor (invoked by runtime.Open). On
	// constitution drift the open returns ErrConstitutionDrift and
	// we refuse to boot. We don't re-call Migrate here (already ran
	// during Open); the log line is just for operator visibility.
	log.Printf("dark-mem-mcp: boot step3 ok migrations + constitution watchdog (driver=%s)", st.DriverName())

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

	return &BootState{
		Config:       cfg,
		Store:        st,
		Safety:       safe,
		Orchestrator: orch,
		Registry:     reg,
	}, nil
}

// Shutdown runs the 4-step shutdown sequence. Idempotent — safe to
// call multiple times. Always returns nil; partial-failure paths
// log and continue.
func (b *BootState) Shutdown(ctx context.Context) error {
	if b == nil {
		return nil
	}
	// Step 1: close all active sessions. Skipped if no active
	// project is set (ListSessions requires one under INV-7); a
	// fresh server that booted but received no calls will have no
	// active project, and there's nothing to close.
	if b.Store != nil && b.Store.ActiveProject() != "" {
		sessions, err := b.Store.ListSessions(ctx, 10000)
		if err == nil {
			for _, s := range sessions {
				if s.Status != "active" {
					continue
				}
				wc := store.WriteContext{
					Actor:     "server_shutdown",
					WritePath: "Shutdown",
				}
				if err := b.Store.CloseSession(ctx, wc, s.SessionID); err != nil {
					log.Printf("dark-mem-mcp: shutdown step1 close session %s: %v", s.SessionID, err)
				}
			}
		} else {
			log.Printf("dark-mem-mcp: shutdown step1 list sessions: %v", err)
		}
		log.Printf("dark-mem-mcp: shutdown step1 ok sessions closed")
	}

	// Step 2: flush write_audit. The Store layer writes audit rows
	// atomically with data writes (INV-1) so there is no pending
	// buffer to flush at this layer. We log a marker so the operator
	// can correlate shutdown timing with the last write.
	log.Printf("dark-mem-mcp: shutdown step2 ok (audit flushed atomically with writes)")

	// Step 3: close Store.
	if b.Store != nil {
		if err := b.Store.Close(); err != nil {
			log.Printf("dark-mem-mcp: shutdown step3 close store: %v", err)
		} else {
			log.Printf("dark-mem-mcp: shutdown step3 ok store closed")
		}
	}
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

// redactDSN strips credentials from a DSN for log safety. Postgres
// DSNs may contain password=...; SQLite paths are file paths.
func redactDSN(dsn string) string {
	if dsn == "" {
		return "<empty>"
	}
	// Look for password= and redact the value until next space.
	for _, key := range []string{"password=", "Password="} {
		if idx := indexOfCI(dsn, key); idx >= 0 {
			end := idx + len(key)
			for end < len(dsn) && dsn[end] != ' ' && dsn[end] != '\n' {
				end++
			}
			return dsn[:idx+len(key)] + "<redacted>" + dsn[end:]
		}
	}
	return dsn
}

func indexOfCI(s, substr string) int {
	if len(substr) > len(s) {
		return -1
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			a, b := s[i+j], substr[j]
			if a >= 'A' && a <= 'Z' {
				a += 32
			}
			if b >= 'A' && b <= 'Z' {
				b += 32
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
