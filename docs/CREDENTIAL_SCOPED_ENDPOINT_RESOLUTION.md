# Credential-scoped endpoint resolution

Status: implemented (2026-11-02). Touches `internal/llm/catalog.go`,
`internal/llm/endpoint.go`, `internal/llmkeystore/keystore.go`,
`internal/tools/llm_config.go`, `internal/tools/llm_bind.go` and the
judge/failover/NLI consumers.

## The problem

The provider catalog used to pin exactly ONE base URL per provider id.
That is wrong for providers whose keys are region-scoped.

MiniMax is the concrete case: the global region (`api.minimax.io`) and
the China region (`api.minimaxi.com`) mint DIFFERENT keys, and each key
authenticates ONLY against its own endpoint. A key minted for one
region returns HTTP 401 `invalid api key` on the other.

With a single hardcoded base URL the failure was indistinguishable from
"the key is wrong": the operator's key was fine, the URL was not. The
2026-08-13 catalog note ("the key currently injected by the harness is
scoped to it only") documented the symptom instead of fixing the class
of bug — the next key rotation would break it again.

The same shape appears in the connectivity probe: it POSTed
`{"model":"probe", ...}`. No provider serves a model named "probe", so
the probe failed with HTTP 400 for every provider — an unrelated fault
masquerading as an auth fault.

## The model

**The endpoint is a property of the CREDENTIAL, not of the provider.**

Therefore:

- the catalog declares **candidates** (`ProviderSpec.Endpoints`), not
  a single endpoint;
- the effective endpoint is **resolved against the key in hand**, in one
  place, so every consumer agrees on it;
- the resolution is **cached** and **persisted with the credential**,
  so discovery is paid once, not per call.

### Resolution order (`llm.ResolveEndpoint`)

1. **Operator override** — `DARK_LLM_BASE_URL_<PROVIDER>` (and
   `DARK_LLM_ANTHROPIC_BASE_URL_<PROVIDER>`). Explicit intent wins
   outright, no probe. This is the escape hatch that does not require a
   recompile.
2. **Cached resolution for this credential** — the cache is keyed by
   provider + a SHA-256 fingerprint of the key, so a rotated key forces
   a fresh discovery while an unchanged key costs nothing.
3. **Probe discovery** — try each candidate in preference order with
   the provider's cheap probe (a model listing, never a completion).
   The first candidate that authenticates wins. A 401/403 is the
   definitive "this endpoint does not serve this key"; network errors
   and 5xx are reported but do not stop the search.
4. **Fail loudly** — when no candidate accepts the key, the error names
   every candidate and what each one answered. A wrong key and a wrong
   endpoint must not look the same.

### Credential record

The keyring entry for a provider now carries the endpoint that was
resolved for it:

```json
{"key":"...","endpoint_id":"global","base_url":"https://api.minimax.io/v1"}
```

`llmkeystore.CredentialStore` is an OPTIONAL extension of the existing
`KeyStore` interface (which is untouched, so no consumer breaks).
Legacy entries — a bare key with no JSON envelope — still read back
correctly, and re-setting the same key preserves its endpoint while
replacing the key discards it.

`llm_key_add` therefore performs the discovery at write time: the key is
validated against every candidate, and the winning endpoint is stored
with it. `llm_key_list` reports that endpoint (never the key).

## Consumers

Every component that holds a credential resolves the endpoint with it:

- `internal/v4alpha/judge` — the drift judge client;
- `internal/v4alpha/judge/v4judge` — the LLM-judge binding
  (`judge-*` provider ids) used by the drift pipeline;
- `internal/llm/failover` + `internal/orchestration` — the failover
  chain and its factory;
- `internal/llm/validate.go` — the connectivity probe (a probe of the
  wrong region would report a healthy provider as dead and cool it
  down);
- `internal/nli` — the NLI provider used by the drift pipeline;
- `internal/tools/llm_config.go` — `llm_key_add` / `llm_key_list`;
- `internal/tools/llm_bind.go` — the connectivity probe.

Providers with a single candidate keep their previous behaviour: the
resolver short-circuits to that candidate without a probe, so nothing
changes for anthropic, openai, google, deepseek and the rest.

## The probe

The probe sends a model the provider actually serves: the binding's
`model_rev` when present, otherwise the catalog `DefaultModel` of the
provider that owns the endpoint (`llm.DefaultModelForURL`). A
placeholder model name is never sent, because a 400 for "unknown
model" is indistinguishable from a 400 for anything else.

## What this does NOT do

- It does not add a region flag to provider ids. `minimax` and
  `minimax-cn` remain distinct ids (they differ in key env var and in
  operator intent); the resolution decides the endpoint for whichever
  key is actually in hand.
- It does not probe on every call. Discovery runs when the credential
  changes or the cache expires (24h).
- It does not store the key anywhere it was not stored before: the OS
  keyring remains the only persistent home for provider keys.
