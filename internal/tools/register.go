// Package tools — register.go: the single entry point that wires all
// 26 tools into the Registry. Called from internal/server/server.go's
// RegisterAll path, and from tests that want a pre-populated registry.
package tools

import (
	"fmt"

	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/vlp"
)

// RegisterAll wires all 26 dark_memory_* tools into the registry, in
// the canonical order (spec 164, bridge.4 + spec 193 Layer 6). Safe
// to call once per Registry; subsequent calls are no-ops if the tools
// are already registered.
//
// The split into per-namespace Register* functions lets tests pull
// in a subset (e.g. only the JUDGE tools for an eval-pipeline test).
// The canonical 26-tool surface is the union of all namespaces.
func RegisterAll(reg *Registry, orch *orchestration.Orchestrator, st store.Store) error {
	if reg == nil {
		return fmt.Errorf("tools: RegisterAll: nil registry")
	}
	if orch == nil {
		return fmt.Errorf("tools: RegisterAll: nil orchestrator")
	}
	if st == nil {
		return fmt.Errorf("tools: RegisterAll: nil store")
	}

	// SESSION (4)
	RegisterSession(reg, orch, st)
	// RESEARCH (3)
	RegisterResearch(reg, orch, st)
	// VIBE (4)
	RegisterVibe(reg, orch, st)
	// CONTEXT (3) — read-only, no orchestrator needed (orchestrator
	// only used for write paths).
	RegisterContext(reg, nil, st)
	// JUDGE (3)
	RegisterJudge(reg, orch, st)
	// POLICY (2)
	RegisterPolicy(reg, orch, st)
	// OBSERVABILITY (3)
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

	// Sanity check: registry must contain all 26 canonical tools
	// after Register*. If a tool was forgotten, fail loudly at boot
	// rather than at request time.
	canonical := CanonicalOrder()
	for _, name := range canonical {
		if reg.Get(name) == nil {
			return fmt.Errorf("tools: RegisterAll: missing tool %q (canonical order violation)", name)
		}
	}
	if got := len(reg.ListCanonical()); got != 26 {
		return fmt.Errorf("tools: RegisterAll: expected 26 tools, got %d", got)
	}
	return nil
}
