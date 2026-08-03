// Package tools — register.go: the single entry point that wires all
// 28 tools into the Registry. Called from internal/server/server.go's
// RegisterAll path, and from tests that want a pre-populated registry.
package tools

import (
	"errors"
	"fmt"

	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/policy"
	"github.com/dark-agents/dark-memory-mcp/internal/recall"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/vlp"
)

// RegisterAll wires all 38 dark_memory_* tools into the registry, in
// the canonical order (spec 164, bridge.4 + spec 193 Layer 6). Safe
// to call once per Registry; subsequent calls are no-ops if the tools
// are already registered.
//
// The split into per-namespace Register* functions lets tests pull
// in a subset (e.g. only the JUDGE tools for an eval-pipeline test).
// The canonical 38-tool surface (v2.6.0; was 35 in v2.5.x, 29 in
// v2.0.0, 28 in v1.3.x, 27 in v1.2.x, 26 in v1.1.x) is the union of
// all namespaces + the armed-mode extras (L7-REDTEAM, +3 tools when
// DARK_REDTEAM=armed — registered as "extras" below and emitted after
// the canonical 38 in tools/list).
//
// 5A.ii.b.2.c: bumped from 28 → 29 (added dark_memory_recall).
// v2.6.0: bumped from 35 → 38 (added dark_memory_agent_bootstrap,
// dark_memory_agent_recommend_companions, dark_memory_agent_detect_environment
// in the new AGENT_BOOTSTRAP namespace, slotted between RESEARCH
// and VIBE per the canonical-order contract).
//
// 5A.ii.b.2.c.1 (v2.0.1): returns the FrameSource singleton so the
// caller can wire it into the gate (server.GateMiddleware). Both the
// recall tool and the gate now share the same CachedSource instance;
// per-call construction is gone.
func RegisterAll(reg *Registry, orch *orchestration.Orchestrator, st store.Store, safety *store.SafetyHolder) (policy.FrameSource, error) {
	if reg == nil {
		return nil, fmt.Errorf("tools: RegisterAll: nil registry")
	}
	if orch == nil {
		return nil, fmt.Errorf("tools: RegisterAll: nil orchestrator")
	}
	if st == nil {
		return nil, fmt.Errorf("tools: RegisterAll: nil store")
	}

	// PROJECT (1) — v1.2.0. Must come before SESSION so that
	// project_create is registered before session_start is reachable
	// in tools/list (matches the canonical order at index 0).
	RegisterProject(reg, orch, st)
	// SESSION (4)
	RegisterSession(reg, orch, st)
	// RESEARCH (3)
	RegisterResearch(reg, orch, st)
	// AGENT_BOOTSTRAP (3) — v2.6.0. The 3 self-bootstrap tools:
	// agent_bootstrap (returns bootstrap resource content by surface),
	// agent_recommend_companions (detects harness + recommends missing
	// companion MCPs), agent_detect_environment (returns what the MCP
	// can infer about the runtime). Pure functions over the embedded
	// bootstrap fs + the global clientInfo store; no Store or
	// Orchestrator needed. Positioned between RESEARCH and VIBE so
	// any spec work can call them first.
	RegisterAgentBootstrap(reg)
	// VIBE (4)
	RegisterVibe(reg, orch, st)
	// CONTEXT (4) — read-only, no orchestrator needed (orchestrator
	// only used for write paths). 5A.ii.b.2.c adds `recall` (29th tool).
	RegisterContext(reg, nil, st)
	// AGENT_MEMORY (6) — v2.1.0 (Mem0-aligned data plane). All six
	// tools go through the orchestrator (write paths: save/update/
	// archive; read paths: list/get/recall). Positioned between CONTEXT
	// and MINDSET per spec D-12 / BRIDGE_AND_COEXISTENCE.md §3.
	RegisterAgentMemory(reg, orch, st)
	// MINDSET (1) — v2.7.0-alpha. Procedural composition with
	// judge-validated subagent system prompts. Uses agent_memory
	// rows as TTL cache (kind=context, tags=mindset-cache, expires_at
	// honored in orchestrator). Composition + validation both go
	// through the Judge orchestrator (eval_type=mindset_compose +
	// eval_type=mindset_quality) for full audit trail.
	RegisterMindset(reg, orch, st)
	// EMBEDDER (1) — v2.9.0-alpha PR-2. Hybrid retrieval consent gate.
	// The handler casts st to an embedder-introspector interface so
	// stores without the embedder field still register the tool with
	// a no-embedder fallback (status="ask" — correct: no embedder →
	// ask the user). Per row 164 §3, the tool surfaces the verbatim
	// prompt text the harness's LLM should display to the operator
	// when dark-memory boots without a detected provider.
	RegisterEmbedderSetup(reg, st)
	// 5A.ii.b.2.c.1 (v2.0.1): construct the FrameSource ONCE at boot
	// and share it between the recall tool and the Gate (server.Gate).
	// Pre-2.0.1, recall built a fresh CachedSource per invocation; that
	// worked because the cache was stateless, but it paid the cost of
	// composing a fresh StoreSource each time (one Store.GetVLPState +
	// one Store.ListSDDEvaluations + one Store.GetConstitution per call).
	// The singleton construction moves those three reads to boot, so
	// per-call cost is one GetFrame (or cache hit).
	src, err := recall.NewSingleton(st, safety, nil)
	if err != nil {
		return nil, fmt.Errorf("tools: RegisterAll: recall.NewSingleton: %w", err)
	}
	RegisterRecall(reg, st, safety, src)
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
		return nil, fmt.Errorf("tools: RegisterAll: vlp.NewPersistence: %w", err)
	}
	auditor, err := vlp.NewAuditor(st)
	if err != nil {
		return nil, fmt.Errorf("tools: RegisterAll: vlp.NewAuditor: %w", err)
	}
	uc, err := vlp.NewUseCase(persistence, auditor)
	if err != nil {
		return nil, fmt.Errorf("tools: RegisterAll: vlp.NewUseCase: %w", err)
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
			return nil, fmt.Errorf("tools: RegisterAll: RegisterRedTeam: %w", err)
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

	// Sanity check: registry must contain all 35 canonical tools
	// after Register*. If a tool was forgotten, fail loudly at boot
	// rather than at request time.
	//
	// 5A.ii.b.2.c: bumped from 28 → 29 (added dark_memory_recall).
	// v2.1.0: bumped from 29 → 34 (added AGENT_MEMORY namespace:
	// save, list, get, update, archive).
	// v2.3.0: bumped from 34 → 35 (added agent_memory_recall).
	// v2.6.0: bumped from 35 → 38 (added AGENT_BOOTSTRAP namespace:
	// agent_bootstrap, agent_recommend_companions, agent_detect_environment).
	// v2.7.0-alpha: bumped from 38 → 39 (added MINDSET namespace:
	// mindset_apply — procedural + judge-validated subagent prompts).
	// v2.9.0-alpha PR-3: bumped from 39 → 40 (added agent_memory_entities
	// for the agent_memory_entities side-table read path).
	// v2.9.0-alpha PR-2: bumped from 40 → 41 (added embedder_setup_prompt
	// for the row 164 §3 consent gate).
	canonical := CanonicalOrder()
	for _, name := range canonical {
		if reg.Get(name) == nil {
			return nil, fmt.Errorf("tools: RegisterAll: missing tool %q (canonical order violation)", name)
		}
	}
	if got := len(reg.ListCanonical()); got != 43 {
		return nil, fmt.Errorf("tools: RegisterAll: expected 43 tools, got %d", got)
	}
	return src, nil
}
