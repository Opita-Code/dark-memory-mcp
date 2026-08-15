// llm_failover_wiring.go — orchestration-side wiring for the spec 1188
// failover chain.
//
// ensureLLMSelector (orchestrator.go) is the primary injection point.
// When the harness does NOT inject its own LLMSelector, we build the
// DEFAULT one here: a FailoverClient over the canonical llm.Catalog,
// backed by a composite key store (OS keyring first, env vars second)
// and a LiteLLM-style HealthRegistry with background probing.
//
// The singleton is built lazily on first use (ensureLLMSelector) and
// torn down via ShutdownDefaultLLM (called from cmd/*/main.go).
package orchestration

import (
	"context"
	"errors"
	"log"
	"os"
	"sync"

	"github.com/dark-agents/dark-memory-mcp/internal/llm"
	"github.com/dark-agents/dark-memory-mcp/internal/llmkeystore"
)

var (
	defaultLLMMu       sync.Mutex
	defaultFailover    *llm.FailoverClient
	defaultHealth      *llm.HealthRegistry
	defaultKS          llmkeystore.KeyStore
	defaultFailoverErr error
	defaultKeyringMode string // "" (unset) | "0" — last DARK_LLM_KEYRING value built for
)

// DefaultFailoverClient builds (once) the default judge client:
//
//   - key store: OS keyring (Windows Credential Manager) composite with
//     env vars; env keys are migrated into the keyring idempotently;
//   - health registry with background probes (default 30s interval);
//   - FailoverClient over llm.Catalog, pin = DARK_JUDGE_PROVIDER.
//
// Returns nil + error when the chain cannot be built (e.g. no factory
// — practically impossible here). The Judge layer converts a nil client
// into ErrNoLLMAvailable like before.
func DefaultFailoverClient() (*llm.FailoverClient, error) {
	keyringMode := os.Getenv("DARK_LLM_KEYRING")
	defaultLLMMu.Lock()
	defer defaultLLMMu.Unlock()
	// Rebuild when the keyring mode changed (tests flip
	// DARK_LLM_KEYRING=0 to force the no-LLM path; production never
	// changes it at runtime). Keyring mode "" vs "0" is the only
	// state that forces a rebuild — the env keys themselves are read
	// live by the env backend on every Get, so they need no rebuild.
	if defaultFailover == nil || defaultKeyringMode != keyringMode {
		ks, err := buildDefaultKeyStore()
		if err != nil {
			defaultFailoverErr = err
			return nil, err
		}
		defaultKS = ks

		defaultHealth = llm.NewHealthRegistry(llm.HealthOptions{})
		// Background probe: validate each keyed provider without
		// spending tokens. Providers without keys are skipped by
		// ValidateProvider (empty key → unreachable, which would
		// cooldown them — so only probe keyed providers).
		defaultHealth.SetProbeFn(func(ctx context.Context, providerID string) llm.ProbeResult {
			spec := llm.SpecByID(providerID)
			if spec == nil {
				return llm.ProbeResult{ProviderID: providerID, State: llm.ProbeUnreachable, Class: llm.FailServer}
			}
			key, err := ks.Get(providerID)
			if err != nil {
				return llm.ProbeResult{ProviderID: providerID, State: llm.ProbeUnreachable, Class: llm.FailTimeout, Error: "no key"}
			}
			return llm.ValidateProvider(ctx, spec, key)
		})
		defaultHealth.Start(context.Background())

		defaultFailover, defaultFailoverErr = llm.NewFailoverClient(llm.FailoverOptions{
			Specs:    catalogSpecs(),
			KS:       ks,
			Health:   defaultHealth,
			Pin:      os.Getenv("DARK_JUDGE_PROVIDER"),
			Factory:  judgeClientFactory,
			Classify: classifyJudgeError,
			Logf:     log.Printf,
		})
		defaultKeyringMode = keyringMode
	}
	return defaultFailover, defaultFailoverErr
}

// ShutdownDefaultLLM stops the background health loop. Safe to call
// multiple times / when never started.
func ShutdownDefaultLLM() {
	if defaultHealth != nil {
		defaultHealth.Stop()
	}
}

// DefaultKeyStore returns the composite key store (OS keyring +
// env fallback) used by the default failover chain. Builds the
// singleton on first call. Returns nil + err when the chain cannot
// be built.
func DefaultKeyStore() (llmkeystore.KeyStore, error) {
	if _, err := DefaultFailoverClient(); err != nil {
		return nil, err
	}
	return defaultKS, nil
}

// buildDefaultKeyStore composes the OS keyring with the env-var
// fallback, and migrates env keys into the keyring exactly once.
//
// DARK_LLM_KEYRING=0 forces env-var-only mode (risk R1 escape hatch):
// the OS keyring is never opened and no migration runs. Useful on
// locked-down machines without a Credential Manager service, and in
// tests that assert the no-LLM path deterministically.
func buildDefaultKeyStore() (llmkeystore.KeyStore, error) {
	// Build the env-var backend (always available — read-only).
	env := llmkeystore.EnvStore(llm.EnvKeyForProvider)

	if os.Getenv("DARK_LLM_KEYRING") == "0" {
		log.Printf("dark-mem-mcp: DARK_LLM_KEYRING=0 — env-var keys only (OS keyring disabled)")
		return env, nil
	}

	ring, err := llmkeystore.DefaultKeyring()
	if err != nil {
		// R1 (risk register): keyring unavailable on locked-down
		// machines → env-var only, same as pre-v2.20.0.
		log.Printf("dark-mem-mcp: OS keyring unavailable (%v) — using env-var keys only", err)
		return env, nil
	}

	composite := llmkeystore.NewComposite(ring, env)

	// Idempotent one-time migration (D7): copy env keys into the
	// keyring for providers missing a stored key.
	pairs := make([][2]string, 0, len(llm.Catalog))
	for _, spec := range llm.Catalog {
		pairs = append(pairs, [2]string{spec.ID, spec.EnvKey})
	}
	migrated, merr := llmkeystore.MigrateFromEnv(ring, llmkeystore.EnvEntriesFromCatalog(pairs))
	if merr != nil {
		log.Printf("dark-mem-mcp: env→keyring migration partial (%v); env fallback stays active", merr)
	} else if len(migrated) > 0 {
		log.Printf("dark-mem-mcp: migrated %d provider key(s) into the OS keyring: %v", len(migrated), migrated)
	}
	return composite, nil
}

// catalogSpecs returns pointers to the canonical catalog entries
// (priority order preserved).
func catalogSpecs() []*llm.ProviderSpec {
	out := make([]*llm.ProviderSpec, 0, len(llm.Catalog))
	for i := range llm.Catalog {
		out = append(out, &llm.Catalog[i])
	}
	return out
}

// judgeClientFactory builds the concrete SelfHarnessClient for one
// provider+key (the same client the legacy path used).
func judgeClientFactory(spec *llm.ProviderSpec, key string) (llm.JudgeClient, error) {
	return newCatalogClient(spec, key), nil
}

// classifyJudgeError maps SelfHarnessClient errors to failover
// failure classes. Returns "" when the error shape is unknown (the
// FailoverClient falls back to its default classifier).
func classifyJudgeError(err error) llm.FailClass {
	if err == nil {
		return ""
	}
	var st *judgeHTTPStatusError
	if errors.As(err, &st) {
		switch {
		case st.status == 401 || st.status == 403:
			return llm.FailAuth
		case st.status == 429:
			return llm.FailRate
		case st.status == 408:
			return llm.FailTimeout
		case st.status >= 500:
			return llm.FailServer
		}
	}
	return "" // let FailoverClient default handle it
}
