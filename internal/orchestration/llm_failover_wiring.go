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
	"fmt"
	"log"
	"os"
	"sync"
	"time"

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
//
// EL KEYRING NUNCA CUELGA EL BOOT (MEDIDO 2026-11-17)
//
// Esta funcion corre en el BOOT del servidor MCP, ANTES de `ServeStdio`.
// MEDIDO en esta maquina: con la coleccion del Secret Service BLOQUEADA y
// sin agente de prompts en la sesion, `ring.Set(...)` NO VUELVE NUNCA (se
// midio un bloqueo de mas de 15 s, con la goroutine todavia viva). Un boot
// colgado aqui deja el MCP entero sin servir, y el harness ve "mcp connect
// failed ... Request timed out" a los ~34 s.
//
// El keyring es una dependencia NO CRITICA del serving: el MCP sirve sus 78
// tools sin ninguna credencial de LLM. Por eso:
//
//   - La apertura del keyring va ACOTADA por un deadline.
//   - El keyring se envuelve en un TimeoutStore: cada operacion tiene
//     plazo, y al expirar se degrada al backend de env en vez de colgar.
//   - La migracion env→keyring va ACOTADA: si el keyring no responde, la
//     migracion se abandona (las claves de env siguen siendo la fuente de
//     verdad, que es justo lo que el fallback composite garantiza).
//
// El resultado: un keyring colgado degrada a env-var keys, y el MCP
// ARRANCA. Nunca al reves.
func buildDefaultKeyStore() (llmkeystore.KeyStore, error) {
	// Build the env-var backend (always available — read-only).
	env := llmkeystore.EnvStore(llm.EnvKeyForProvider)

	if os.Getenv("DARK_LLM_KEYRING") == "0" {
		log.Printf("dark-mem-mcp: DARK_LLM_KEYRING=0 — env-var keys only (OS keyring disabled)")
		return env, nil
	}

	ring, err := openKeyringWithDeadline()
	if err != nil {
		// R1 (risk register): keyring unavailable on locked-down
		// machines → env-var only, same as pre-v2.20.0.
		log.Printf("dark-mem-mcp: OS keyring unavailable (%v) — using env-var keys only", err)
		return env, nil
	}

	// El store con deadline: una operacion colgada se abandona y se
	// degrada al backend de env, en vez de colgar el boot.
	safe := llmkeystore.NewTimeoutStore(ring)

	composite := llmkeystore.NewComposite(safe, env)

	// Idempotent one-time migration (D7): copy env keys into the
	// keyring for providers missing a stored key.
	//
	// MEDIDO 2026-11-17: la migracion ESCRIBE en el keyring, y con la
	// coleccion bloqueada esa escritura no vuelve nunca. Por eso la
	// migracion va ACOTADA y, si expira, se abandona: las claves de env
	// siguen siendo la fuente de verdad (el composite las lee igual).
	pairs := make([][2]string, 0, len(llm.Catalog))
	for _, spec := range llm.Catalog {
		pairs = append(pairs, [2]string{spec.ID, spec.EnvKey})
	}
	migrated, merr := migrateWithDeadline(safe, llmkeystore.EnvEntriesFromCatalog(pairs))
	if merr != nil {
		log.Printf("dark-mem-mcp: env→keyring migration partial (%v); env fallback stays active", merr)
	} else if len(migrated) > 0 {
		log.Printf("dark-mem-mcp: migrated %d provider key(s) into the OS keyring: %v", len(migrated), migrated)
	}
	return composite, nil
}

// openKeyringWithDeadline opens the OS keyring under a deadline.
//
// MEDIDO 2026-11-17: `keyring.Open` contra un Secret Service sano tarda
// entre 0,8 ms y 8,7 ms. El plazo por defecto (2 s) es tres ordenes de
// magnitud por encima, asi que no va a falsos-positivivar un keyring sano.
//
// Cuando el keyring esta bloqueado o el daemon de secrets no contesta,
// `keyring.Open` puede quedarse esperando. Este limite convierte ese
// "espera infinita" en un fallo que el boot puede degradar.
func openKeyringWithDeadline() (llmkeystore.Store, error) {
	type result struct {
		ring llmkeystore.Store
		err  error
	}
	ch := make(chan result, 1)

	go func() {
		ring, err := llmkeystore.DefaultKeyring()
		ch <- result{ring: ring, err: err}
	}()

	timer := time.NewTimer(llmkeystore.DefaultOperationTimeout)
	defer timer.Stop()

	select {
	case r := <-ch:
		return r.ring, r.err
	case <-timer.C:
		return nil, fmt.Errorf("%w: opening the OS keyring took longer than %s",
			llmkeystore.ErrKeyringTimeout, llmkeystore.DefaultOperationTimeout)
	}
}

// migrateWithDeadline runs the env→keyring migration under a deadline.
//
// MEDIDO 2026-11-17: la escritura en una coleccion bloqueada NO VUELVE
// NUNCA. Sin este limite, la migracion del boot se queda esperando
// indefinidamente y el MCP no arranca. Con el limite, la migracion se
// abandona y las claves de env siguen mandando (el composite las lee).
func migrateWithDeadline(ring llmkeystore.KeyStore, entries []llmkeystore.EnvEntry) ([]string, error) {
	type result struct {
		migrated []string
		err      error
	}
	ch := make(chan result, 1)

	go func() {
		migrated, err := llmkeystore.MigrateFromEnv(ring, entries)
		ch <- result{migrated: migrated, err: err}
	}()

	timer := time.NewTimer(llmkeystore.DefaultOperationTimeout)
	defer timer.Stop()

	select {
	case r := <-ch:
		return r.migrated, r.err
	case <-timer.C:
		return nil, fmt.Errorf("%w: migrating env keys into the OS keyring took longer than %s; "+
			"the env vars remain the source of truth",
			llmkeystore.ErrKeyringTimeout, llmkeystore.DefaultOperationTimeout)
	}
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
//
// The endpoint is resolved HERE, against the credential the factory
// receives: the failover chain is the component that holds a key and
// knows which provider it belongs to, so this is where the
// credential→endpoint resolution belongs (the llm.FailoverOptions
// Factory signature stays untouched — it already carries the key).
//
// A provider with a single endpoint candidate keeps its previous
// behaviour (no probe, no extra request). For a multi-candidate
// provider the discovery is bounded and never fatal: a failure is
// logged — the operator must see that no endpoint accepted the key —
// and the client falls back to the provider's default endpoint so the
// judge keeps a client.
func judgeClientFactory(spec *llm.ProviderSpec, key string) (llm.JudgeClient, error) {
	return newCatalogClientWithEndpoint(spec, key, resolveEndpointForCredential(spec, key)), nil
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
