// Package tools — register.go: the single entry point that wires all
// 28 tools into the Registry. Called from internal/server/server.go's
// RegisterAll path, and from tests that want a pre-populated registry.
package tools

import (
	"errors"
	"fmt"

	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/vlp"
)

// RegisterAll wires all 29 dark_memory_* tools into the registry, in
// the canonical order (spec 164, bridge.4 + spec 193 Layer 6). Safe
// to call once per Registry; subsequent calls are no-ops if the tools
// are already registered.
//
// The split into per-namespace Register* functions lets tests pull
// in a subset (e.g. only the JUDGE tools for an eval-pipeline test).
// The canonical 29-tool surface (v2.0.0; was 28 in v1.3.x, 27 in
// v1.2.x, 26 in v1.1.x) is the union of all namespaces + the
// armed-mode extras (L7-REDTEAM, +3 tools when DARK_REDTEAM=armed
// — registered as "extras" below and emitted after the canonical
// 29 in tools/list).
//
// 5A.ii.b.2.c: bumped from 28 → 29 (added dark_memory_recall).
func RegisterAll(reg *Registry, orch *orchestration.Orchestrator, st store.Store, safety *store.SafetyHolder) error {
	if reg == nil {
		return fmt.Errorf("tools: RegisterAll: nil registry")
	}
	if orch == nil {
		return fmt.Errorf("tools: RegisterAll: nil orchestrator")
	}
	if st == nil {
		return fmt.Errorf("tools: RegisterAll: nil store")
	}

	// PROJECT (1) — v1.2.0. Must come before SESSION so that
	// project_create is registered before session_start is reachable
	// in tools/list (matches the canonical order at index 0).
	RegisterProject(reg, orch, st)
	// SESSION (4)
	RegisterSession(reg, orch, st)
	// RESEARCH (3)
	RegisterResearch(reg, orch, st)
	// VIBE (4)
	RegisterVibe(reg, orch, st)
	// CONTEXT (4) — read-only, no orchestrator needed (orchestrator
	// only used for write paths). 5A.ii.b.2.c adds `recall` (29th tool).
	RegisterContext(reg, nil, st)
	RegisterRecall(reg, st, safety)
	// JUDGE (3)
	RegisterJudge(reg, orch, st)
	// POLICY (2)
	RegisterPolicy(reg, orch, st)
	// OBSERVABILITY (4) — v1.3.0 grew from 3 to 4 with health_ping.
	RegisterObservability(reg, orch, st)
	// ADMIN (3) — read-only or schema-only, no orchestrator needed.
	RegisterAdmin(reg, nil, st)

	// L6-VLP (1) — DMAP v1.1 spec 193. Construct the VLP UseCase from
	// the Store at boot time: Persistence + Auditor + UseCase are all
	// pure composition over Store (no extra config required). If any
	// construction step fails, fail boot rather than silently disabling
	// the L6 wire tool.
	persistence, err := vlp.NewPersistence(st)
	if err != nil {
		return fmt.Errorf("tools: RegisterAll: vlp.NewPersistence: %w", err)
	}
	auditor, err := vlp.NewAuditor(st)
	if err != nil {
		return fmt.Errorf("tools: RegisterAll: vlp.NewAuditor: %w", err)
	}
	uc, err := vlp.NewUseCase(persistence, auditor)
	if err != nil {
		return fmt.Errorf("tools: RegisterAll: vlp.NewUseCase: %w", err)
	}
	RegisterVLP(reg, uc)

	// L7-REDTEAM (3) — armed-mode optional. RegisterRedTeam panics
	// / errors if DARK_REDTEAM != "armed", so the un-armed server
	// gets exactly the canonical 28 tools (v1.3.0; no surface change
	// relative to the count expectation below) and the armed server
	// gets 28 + 3 = 31. The redteam tools are NOT in the canonical
	// order — they are namespace extras that tools/list emits after
	// the canonical 28.
	redteamArmed := false
	if err := RegisterRedTeam(reg, st); err != nil {
		// ErrArmedRequired is the EXPECTED return when the operator
		// has not flipped DARK_REDTEAM=armed. Log it as info, not
		// as an error, so the un-armed boot is silent.
		if errors.Is(err, store.ErrArmedRequired) {
			// not armed — that's fine, surface stays at 28.
		} else {
			return fmt.Errorf("tools: RegisterAll: RegisterRedTeam: %w", err)
		}
	} else {
		redteamArmed = true
	}

	// v1.3.0: feed the runtime context that dark_memory_health_ping
	// reads into the package globals. The server config (name,
	// version, coexistence group, driver label, DSN path) is installed
	// by main.go via SetRuntimeContext() before RegisterAll runs.
	// Here we only compute the registry counts so health_ping can
	// report "how many tools am I advertising right now".
	SetRegistryCounts(
		len(reg.ListCanonical()),
		reg.CountExtras(),
		redteamArmed,
	)

	// Sanity check: registry must contain all 29 canonical tools
	// after Register*. If a tool was forgotten, fail loudly at boot
	// rather than at request time.
	//
	// 5A.ii.b.2.c: bumped from 28 → 29 (added dark_memory_recall).
	canonical := CanonicalOrder()
	for _, name := range canonical {
		if reg.Get(name) == nil {
			return fmt.Errorf("tools: RegisterAll: missing tool %q (canonical order violation)", name)
		}
	}
	if got := len(reg.ListCanonical()); got != 29 {
		return fmt.Errorf("tools: RegisterAll: expected 29 tools, got %d", got)
	}
	return nil
}
