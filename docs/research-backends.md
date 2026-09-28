# Research Backends Reference (BUG-10 10a)

The v4 research executor fans out to **17 backends** that work
without an API key. The list is the operator-facing catalog
for `dark_memory_research_topic` and the audit row's
`backends_called` field. The list is stable across v4-alpha;
new backends ship in subsequent commits with their own ADR.

## Selection algorithm

The executor picks backends by `(intent, vibe_case, tier)`:

1. **Intent** is the operator's input. Each backend declares
   exactly one intent.
2. **Vibe_case** is the operator's `vibe_case` field
   (C1=code, C2=text, C3=image, C4=video, C5=bundle,
   C6=infra, C7=governance). A backend with no
   `ServesVibeCases` is a utility and is callable from any
   vibe case.
3. **Tier** sets the fan-out order:
   - **T1 (Authoritative)**: registry or primary source. Called
     first. T1 alone is enough for the operator to know the
     answer.
   - **T2 (Corroborant)**: second-source that confirms T1.
     Called when T1 returned nothing, or when `depth=deep`.
   - **T3 (Discovery)**: exploration. Called only when T1+T2
     returned nothing AND `depth=deep`.

`depth=shallow` is T1 only (1 HTTP call).
`depth=standard` is T1 + T2 (2 calls).
`depth=deep` is T1 + T2 + T3 (4 calls max).

## The 17 backends

| Intent | Backend | Tier | Vibe case | URL template | ToS note |
|---|---|---|---|---|---|
| `cve` | `osv` | T1 | C1 | `https://api.osv.dev/v1/vulns/{id}` | Free public API; rate limit ~10 req/s |
| `cve` | `nvd` | T2 | C1 | `https://services.nvd.nist.gov/rest/json/cves/2.0?cveId={id}` | NIST public, no key needed for low rate |
| `code` | `npm` | T1 | C1 | `https://registry.npmjs.org/{name}` | Public registry |
| `code` | `crates` | T1 | C1 | `https://crates.io/api/v1/crates/{name}` | Public registry |
| `code` | `github` | T2 | C1 | `https://api.github.com/repos/{name}` | Public, 60 req/h unauthenticated |
| `academic` | `openalex` | T1 | C2, C7 | `https://api.openalex.org/works?search={q}` | CC0 data dump, open API |
| `academic` | `crossref` | T2 | C2, C7 | `https://api.crossref.org/works?query={q}` | Public REST API |
| `academic` | `arxiv` | T1 | C2, C7 | `http://export.arxiv.org/api/query?search_query={q}` | Public, no key |
| `domain` | `rdap` | T1 | C6, C7 | `https://rdap.org/domain/{name}` | Public RDAP bootstrap |
| `dns` | `doh-google` | T1 | C6 | `https://dns.google/resolve?name={name}&type=A` | Google's DoH, public |
| `dns` | `doh-cloudflare` | T2 | C6 | `https://cloudflare-dns.com/dns-query?name={name}&type=A` | Cloudflare's DoH, public |
| `ip` | `ip-api` | T1 | C6 | `http://ip-api.com/json/{ip}` | Free for < 45 req/min |
| `ip` | `ripe-db` | T2 | C6 | `https://stat.ripe.net/data/whois/data.json?resource={ip}` | RIPE public, no key |
| `cert` | `crt` | T1 | C6 | `https://crt.sh/?q={name}&output=json` | Public CT log search |
| `email` | `hibp-range` | T2 | C6 | `https://api.pwnedpasswords.com/range/{prefix}` | k-anonymity, 5-char prefix; we do not send full email |
| `geo` | `nominatim` | T1 | C6, C7 | `https://nominatim.openstreetmap.org/search?q={q}&format=json` | 1 req/s rate limit; User-Agent required |
| `news` | `hn-algolia` | T1 | C4, C7 | `https://hn.algolia.com/api/v1/search?query={q}` | Public, no key |
| `news` | `gdelt` | T2 | C4, C7 | `https://api.gdeltproject.org/api/v2/doc/doc?query={q}&format=json` | Rate-limited; stale-while-revalidate handles 429s |
| `web` | `ddg-html` | T3 | (any) | `https://html.duckduckgo.com/html/?q={q}` | HTML scrape; DDG's own ToS prefers this over the JSON API |

## TTL table (per intent)

The cache TTLs match the data volatility:

| Intent | SoftTTL | StaleTTL | Rationale |
|---|---|---|---|
| `dns` | 5m | 1m | Records change in minutes |
| `ip` | 5m | 1m | Geolocation stable for the session |
| `domain` | 1h | 1h | RDAP status changes daily |
| `cert` | 1h | 1h | CT logs append in minutes; reads stable |
| `cve` | 24h | 1h | Re-scores daily |
| `code` | 1h | 1h | Registry metadata stable |
| `academic` | 7d | 1d | Papers don't change post-publish |
| `email` | 30m | 1h | HIBP appends; k-anonymity safe to cache |
| `geo` | 7d | 1d | Nominatim is slow to mutate |
| `news` | 1h | 1h | HN/GDELT are stream-shaped |
| `web` | 1h | 1h | DDG results decay |

The `executor` reads `DefaultCacheConfigs()` at boot. The
table is per intent; no backend has a custom override in 10a
(customization lands in 10b via the `cache.override` env var
per the spec).

## Health + circuit breaker

Each backend has a `Health` (one per name) tracking
consecutive errors:

- `DegradeAfter=3`: after 3 consecutive errors the backend is
  deprioritized (degraded but still callable).
- `KillAfter=10`: after 10 consecutive errors the backend is
  marked dead and skipped until the next `RecordSuccess`
  (a single success on a dead backend revives it; this is
  the standard circuit-breaker pattern).

A dead backend's `IsCallable()` returns `false` and the
executor skips it without dialing.

## SSRF guard

Every URL the executor builds goes through
`security.ValidateURL`. The guard rejects:

- IPv4 RFC 1918 (10/8, 172.16/12, 192.168/16)
- IPv4 CGNAT (100.64/10)
- IPv4 link-local (169.254/16) — cloud-metadata range
- IPv4 loopback (127/8)
- IPv4 multicast + broadcast
- IPv4 documentation
- IPv6 loopback, unique-local, link-local, multicast
- IPv4-mapped IPv6 (::ffff:10.0.0.1)

A test escape hatch `DARK_TEST_SSRF_BYPASS=1` disables
the guard entirely so unit tests can hit
`httptest.NewServer`. Production MUST NOT set this.

## Prompt-injection gate

Every Result is passed through `research.Check` which:

1. Redacts PII (emails, IPs, bearer tokens) into `[REDACTED]`
2. Scans for the 10 override patterns (OP-1..OP-10) on the
   T5-normalized text
3. Returns the gated Result; block-severity hits drop the
   envelope without counting as a backend error

The 10 patterns and the T5 pipeline are exposed via the
`dark_memory_judge_util_*` tools (see docs/judge-pipeline-v4.md
§6 for the spec).

## Rate-limit handling

The executor's `HTTPClient.Do` honors 429 + Retry-After:

- The cache is consulted first (avoids the call when fresh).
- On 429, the executor records an error and treats the
  backend as `Degraded` for 1 minute.
- On 5xx, the executor records an error and counts toward
  the `DegradeAfter`/`KillAfter` thresholds.

Rate-limit budgets (token bucket) are in
`internal/v4alpha/research/research_registry.go`; the
10a wiring is the same `RateSpec` per backend.

## What's NOT in 10a (10b/10c backends)

The 17 backends are the no-API-key set. The following
backends land in 10b/10c once a key is configured:

- `vendor-advisory` (vendor security advisories, requires key)
- `ieee-xplore` (academic, requires key)
- `pubmed` (academic, requires key)
- `semantic-scholar` (academic, requires key)
- `exa` (web, requires key)
- `perplexity` (web, requires key)

These land when the operator has set the env var
`DARK_RESEARCH_PROVIDER_KEY_<NAME>=...` and the executor
gates them on `os.LookupEnv` (no silent degradation if the
key is missing — the backend reports "not configured" and
the executor skips it cleanly).

## Cross-references

- `internal/v4alpha/research/backends_v4.go` — the 17 manifests
- `internal/v4alpha/research/cache.go` — TTL table + stale-while-revalidate
- `internal/v4alpha/research/health.go` — circuit breaker
- `internal/v4alpha/research/executor.go` — the fan-out loop
- `internal/v4alpha/security/ssrf.go` — SSRF guard
- `docs/judge-pipeline-v4.md` §6 — prompt-injection gate
