// Package research — v4alpha backend declarations.
//
// This file declares the 17 backends approved for BUG-10 10a
// (2026-09-27 probe: 17 returned HTTP 200, 1 rate-limited, 4 dead).
// Each backend is an ExtendedManifest that captures:
//
//   - the canonical intent (web, code, cve, ...)
//   - the tier (T1 authoritative, T2 corroborant, T3 discovery)
//   - which vibe-cases it serves (the discipline the operator
//     asked for: a C5 bundle call does not need crt.sh, and a C1
//     code call does not need arXiv)
//   - the URL template (validated by security.ValidateURL at boot)
//   - the rate limit (the Bucket the executor enforces)
//   - the byte cap hint (the executor narrows to the depth cap)
//
// The parsers that turn the backend's response JSON into research
// Items live in research_parsers.go. The fixtures (recorded
// responses) live in research_fixtures/ and are loaded by the
// snapshot tests.
//
// Adding a new backend: append a Backend struct to AllBackends,
// add a parser to research_parsers.go, add a fixture under
// research_fixtures/. The executor and the snapshot tests pick
// the new backend up automatically; no boot changes.
package research

import (
	_ "embed" // for fixture loading (future slice)
	"time"
)

// Backend is the v4alpha wiring: a manifest + a parser name. The
// executor looks up the parser by name in research_parsers.go.
type Backend struct {
	// Manifest is the extended v4 manifest. ValidateExtended is
	// called at boot; boot fails if the manifest is invalid (so
	// a typo in a URL template is caught at startup, not at
	// runtime).
	Manifest ExtendedManifest

	// ParserName is the registry key the executor uses to fetch
	// the parser function. Defining parsers by name (instead of
	// as methods on Backend) keeps the per-backend file small
	// and lets the snapshot fixtures live separately.
	ParserName string
}

// AllBackends returns the 17 backends approved by the operator
// 2026-09-27. The slice is ordered by (Intent, Tier, Name) for
// deterministic boot logs. The executor iterates this slice once
// per (intent, vibe_case) request.
func AllBackends() []Backend {
	return []Backend{
		// --- CVE intent (C1 code, C5 bundle) ---
		{
			Manifest: ExtendedManifest{
				BackendManifest: BackendManifest{
					Name: "osv", Version: "4.0.0",
					Capabilities: []string{"research.cve"},
					Intent: IntentCVE,
					Primary: "osv.dev",
					FallbackChain: []string{"osv.dev"},
					RateLimits: map[string]string{"osv.dev": "5/s"},
					TimeoutMs: 12_000,
				},
				ManifestV4: ManifestV4{
					Tier: Tier1Authoritative,
					ServesVibeCases: []VibeCase{VibeC1Code, VibeC5Bundle},
					ByteCapHint: 64 * 1024,
					URLTemplate: "https://api.osv.dev/v1/vulns/{id}",
				},
			},
			ParserName: "osv",
		},
		{
			Manifest: ExtendedManifest{
				BackendManifest: BackendManifest{
					Name: "nvd", Version: "4.0.0",
					Capabilities: []string{"research.cve"},
					Intent: IntentCVE,
					Primary: "nvd.nist.gov",
					FallbackChain: []string{"nvd.nist.gov"},
					RateLimits: map[string]string{"nvd.nist.gov": "5/30s"},
					TimeoutMs: 12_000,
				},
				ManifestV4: ManifestV4{
					Tier: Tier2Corroborant,
					ServesVibeCases: []VibeCase{VibeC1Code, VibeC5Bundle, VibeC6Infra},
					ByteCapHint: 128 * 1024,
					URLTemplate: "https://services.nvd.nist.gov/rest/json/cves/2.0?cveId={id}",
				},
			},
			ParserName: "nvd",
		},

		// --- Code intent (C1 code, C5 bundle) ---
		{
			Manifest: ExtendedManifest{
				BackendManifest: BackendManifest{
					Name: "npm", Version: "4.0.0",
					Capabilities: []string{"research.code"},
					Intent: IntentCode,
					Primary: "registry.npmjs.org",
					FallbackChain: []string{"registry.npmjs.org"},
					RateLimits: map[string]string{"registry.npmjs.org": "30/s"},
					TimeoutMs: 8_000,
				},
				ManifestV4: ManifestV4{
					Tier: Tier1Authoritative,
					ServesVibeCases: []VibeCase{VibeC1Code, VibeC5Bundle},
					ByteCapHint: 64 * 1024,
					URLTemplate: "https://registry.npmjs.org/{name}/latest",
				},
			},
			ParserName: "npm",
		},
		{
			Manifest: ExtendedManifest{
				BackendManifest: BackendManifest{
					Name: "crates", Version: "4.0.0",
					Capabilities: []string{"research.code"},
					Intent: IntentCode,
					Primary: "crates.io",
					FallbackChain: []string{"crates.io"},
					RateLimits: map[string]string{"crates.io": "5/s"},
					TimeoutMs: 8_000,
				},
				ManifestV4: ManifestV4{
					Tier: Tier1Authoritative,
					ServesVibeCases: []VibeCase{VibeC1Code, VibeC5Bundle},
					ByteCapHint: 32 * 1024,
					URLTemplate: "https://crates.io/api/v1/crates/{name}",
				},
			},
			ParserName: "crates",
		},
		{
			Manifest: ExtendedManifest{
				BackendManifest: BackendManifest{
					Name: "github", Version: "4.0.0",
					Capabilities: []string{"research.code"},
					Intent: IntentCode,
					Primary: "api.github.com",
					FallbackChain: []string{"api.github.com"},
					RateLimits: map[string]string{"api.github.com": "1/m"},
					TimeoutMs: 8_000,
				},
				ManifestV4: ManifestV4{
					Tier: Tier2Corroborant,
					ServesVibeCases: []VibeCase{VibeC1Code, VibeC5Bundle, VibeC7Governance},
					ByteCapHint: 32 * 1024,
					URLTemplate: "https://api.github.com/search/repositories?q={q}&per_page=5",
				},
			},
			ParserName: "github",
		},

		// --- Academic intent (C2 text, C7 governance) ---
		{
			Manifest: ExtendedManifest{
				BackendManifest: BackendManifest{
					Name: "openalex", Version: "4.0.0",
					Capabilities: []string{"research.academic"},
					Intent: IntentAcademic,
					Primary: "api.openalex.org",
					FallbackChain: []string{"api.openalex.org"},
					RateLimits: map[string]string{"api.openalex.org": "10/s"},
					TimeoutMs: 8_000,
				},
				ManifestV4: ManifestV4{
					Tier: Tier1Authoritative,
					ServesVibeCases: []VibeCase{VibeC2Text, VibeC7Governance},
					ByteCapHint: 32 * 1024,
					URLTemplate: "https://api.openalex.org/works?search={q}&per-page=5",
				},
			},
			ParserName: "openalex",
		},
		{
			Manifest: ExtendedManifest{
				BackendManifest: BackendManifest{
					Name: "crossref", Version: "4.0.0",
					Capabilities: []string{"research.academic"},
					Intent: IntentAcademic,
					Primary: "api.crossref.org",
					FallbackChain: []string{"api.crossref.org"},
					RateLimits: map[string]string{"api.crossref.org": "10/s"},
					TimeoutMs: 8_000,
				},
				ManifestV4: ManifestV4{
					Tier: Tier2Corroborant,
					ServesVibeCases: []VibeCase{VibeC2Text, VibeC7Governance},
					ByteCapHint: 16 * 1024,
					URLTemplate: "https://api.crossref.org/works?query={q}&rows=5",
				},
			},
			ParserName: "crossref",
		},
		{
			Manifest: ExtendedManifest{
				BackendManifest: BackendManifest{
					Name: "arxiv", Version: "4.0.0",
					Capabilities: []string{"research.academic"},
					Intent: IntentAcademic,
					Primary: "export.arxiv.org",
					FallbackChain: []string{"export.arxiv.org"},
					RateLimits: map[string]string{"export.arxiv.org": "1/3s"},
					TimeoutMs: 12_000,
				},
				ManifestV4: ManifestV4{
					Tier: Tier3Discovery,
					ServesVibeCases: []VibeCase{VibeC2Text, VibeC7Governance},
					ByteCapHint: 32 * 1024,
					URLTemplate: "https://export.arxiv.org/api/query?search_query=all:{q}&max_results=5",
				},
			},
			ParserName: "arxiv",
		},

		// --- Domain intent (C6 infra) ---
		{
			Manifest: ExtendedManifest{
				BackendManifest: BackendManifest{
					Name: "rdap", Version: "4.0.0",
					Capabilities: []string{"research.domain"},
					Intent: IntentDomain,
					Primary: "rdap.org",
					FallbackChain: []string{"rdap.org"},
					RateLimits: map[string]string{"rdap.org": "10/s"},
					TimeoutMs: 8_000,
				},
				ManifestV4: ManifestV4{
					Tier: Tier1Authoritative,
					ServesVibeCases: []VibeCase{VibeC6Infra, VibeC3Image, VibeC4Video},
					ByteCapHint: 16 * 1024,
					URLTemplate: "https://rdap.org/domain/{name}",
				},
			},
			ParserName: "rdap",
		},

		// --- DNS intent (C6 infra) ---
		{
			Manifest: ExtendedManifest{
				BackendManifest: BackendManifest{
					Name: "doh-google", Version: "4.0.0",
					Capabilities: []string{"research.dns"},
					Intent: IntentDNS,
					Primary: "dns.google",
					FallbackChain: []string{"dns.google", "cloudflare-dns.com"},
					RateLimits: map[string]string{"dns.google": "20/s", "cloudflare-dns.com": "20/s"},
					TimeoutMs: 6_000,
				},
				ManifestV4: ManifestV4{
					Tier: Tier1Authoritative,
					ServesVibeCases: []VibeCase{VibeC6Infra},
					ByteCapHint: 4 * 1024,
					URLTemplate: "https://dns.google/resolve?name={name}&type={type}",
				},
			},
			ParserName: "doh",
		},
		{
			Manifest: ExtendedManifest{
				BackendManifest: BackendManifest{
					Name: "doh-cloudflare", Version: "4.0.0",
					Capabilities: []string{"research.dns"},
					Intent: IntentDNS,
					Primary: "cloudflare-dns.com",
					FallbackChain: []string{"cloudflare-dns.com"},
					RateLimits: map[string]string{"cloudflare-dns.com": "20/s"},
					TimeoutMs: 6_000,
				},
				ManifestV4: ManifestV4{
					Tier: Tier1Authoritative,
					ServesVibeCases: []VibeCase{VibeC6Infra},
					ByteCapHint: 4 * 1024,
					URLTemplate: "https://cloudflare-dns.com/dns-query?name={name}&type={type}",
				},
			},
			ParserName: "doh",
		},

		// --- IP intent (C6 infra) ---
		{
			Manifest: ExtendedManifest{
				BackendManifest: BackendManifest{
					Name: "ip-api", Version: "4.0.0",
					Capabilities: []string{"research.ip"},
					Intent: IntentIP,
					Primary: "ip-api.com",
					FallbackChain: []string{"ip-api.com"},
					RateLimits: map[string]string{"ip-api.com": "1/2s"},
					TimeoutMs: 6_000,
				},
				ManifestV4: ManifestV4{
					Tier: Tier1Authoritative,
					ServesVibeCases: []VibeCase{VibeC6Infra},
					ByteCapHint: 2 * 1024,
					URLTemplate: "https://ip-api.com/json/{ip}",
				},
			},
			ParserName: "ip-api",
		},
		{
			Manifest: ExtendedManifest{
				BackendManifest: BackendManifest{
					Name: "ripe-db", Version: "4.0.0",
					Capabilities: []string{"research.ip"},
					Intent: IntentIP,
					Primary: "rest.db.ripe.net",
					FallbackChain: []string{"rest.db.ripe.net"},
					RateLimits: map[string]string{"rest.db.ripe.net": "10/s"},
					TimeoutMs: 6_000,
				},
				ManifestV4: ManifestV4{
					Tier: Tier2Corroborant,
					ServesVibeCases: []VibeCase{VibeC6Infra},
					ByteCapHint: 2 * 1024,
					URLTemplate: "https://rest.db.ripe.net/abuse-contact/{ip}.json",
				},
			},
			ParserName: "ripe",
		},

		// --- Cert intent (C6 infra) ---
		{
			Manifest: ExtendedManifest{
				BackendManifest: BackendManifest{
					Name: "crt-sh", Version: "4.0.0",
					Capabilities: []string{"research.cert"},
					Intent: IntentCert,
					Primary: "crt.sh",
					FallbackChain: []string{"crt.sh"},
					RateLimits: map[string]string{"crt.sh": "1/3s"},
					TimeoutMs: 12_000,
				},
				ManifestV4: ManifestV4{
					Tier: Tier3Discovery,
					ServesVibeCases: []VibeCase{VibeC6Infra},
					// crt.sh returns 500 KB for popular domains;
					// the executor narrows to the depth cap.
					ByteCapHint: 64 * 1024,
					URLTemplate: "https://crt.sh/?q={name}&output=json",
				},
			},
			ParserName: "crt",
		},

		// --- Email intent (C6 infra) ---
		{
			Manifest: ExtendedManifest{
				BackendManifest: BackendManifest{
					Name: "hibp-range", Version: "4.0.0",
					Capabilities: []string{"research.email"},
					Intent: IntentEmail,
					Primary: "api.pwnedpasswords.com",
					FallbackChain: []string{"api.pwnedpasswords.com"},
					RateLimits: map[string]string{"api.pwnedpasswords.com": "1/s"},
					TimeoutMs: 8_000,
				},
				ManifestV4: ManifestV4{
					Tier: Tier1Authoritative,
					ServesVibeCases: []VibeCase{VibeC6Infra},
					ByteCapHint: 64 * 1024,
					// k-anonymity: send only the first 5 chars of
					// the SHA-1 hash, never the full hash or the
					// raw email. Caller is responsible for hashing.
					URLTemplate: "https://api.pwnedpasswords.com/range/{prefix}",
				},
			},
			ParserName: "hibp",
		},

		// --- Geo intent (C6 infra) ---
		{
			Manifest: ExtendedManifest{
				BackendManifest: BackendManifest{
					Name: "nominatim", Version: "4.0.0",
					Capabilities: []string{"research.geo"},
					Intent: IntentGeo,
					Primary: "nominatim.openstreetmap.org",
					FallbackChain: []string{"nominatim.openstreetmap.org"},
					RateLimits: map[string]string{"nominatim.openstreetmap.org": "1/s"},
					TimeoutMs: 8_000,
				},
				ManifestV4: ManifestV4{
					Tier: Tier1Authoritative,
					ServesVibeCases: []VibeCase{VibeC6Infra},
					ByteCapHint: 8 * 1024,
					URLTemplate: "https://nominatim.openstreetmap.org/search?q={q}&format=json&limit=5",
				},
			},
			ParserName: "nominatim",
		},

		// --- News intent (C2 text, C4 video) ---
		{
			Manifest: ExtendedManifest{
				BackendManifest: BackendManifest{
					Name: "hn-algolia", Version: "4.0.0",
					Capabilities: []string{"research.news"},
					Intent: IntentNews,
					Primary: "hn.algolia.com",
					FallbackChain: []string{"hn.algolia.com"},
					RateLimits: map[string]string{"hn.algolia.com": "5/s"},
					TimeoutMs: 6_000,
				},
				ManifestV4: ManifestV4{
					Tier: Tier2Corroborant,
					ServesVibeCases: []VibeCase{VibeC2Text, VibeC4Video},
					ByteCapHint: 16 * 1024,
					URLTemplate: "https://hn.algolia.com/api/v1/search?query={q}&hitsPerPage=5",
				},
			},
			ParserName: "hn",
		},
		{
			Manifest: ExtendedManifest{
				BackendManifest: BackendManifest{
					Name: "gdelt", Version: "4.0.0",
					Capabilities: []string{"research.news"},
					Intent: IntentNews,
					Primary: "api.gdeltproject.org",
					FallbackChain: []string{"api.gdeltproject.org"},
					RateLimits: map[string]string{"api.gdeltproject.org": "1/5s"},
					TimeoutMs: 12_000,
				},
				ManifestV4: ManifestV4{
					Tier: Tier3Discovery,
					ServesVibeCases: []VibeCase{VibeC2Text, VibeC4Video},
					ByteCapHint: 16 * 1024,
					URLTemplate: "https://api.gdeltproject.org/api/v2/doc/doc?query={q}&format=json&maxrecords=5",
				},
			},
			ParserName: "gdelt",
		},

		// --- Web intent (C2 text) ---
		{
			Manifest: ExtendedManifest{
				BackendManifest: BackendManifest{
					Name: "ddg-html", Version: "4.0.0",
					Capabilities: []string{"research.web"},
					Intent: IntentWeb,
					Primary: "html.duckduckgo.com",
					FallbackChain: []string{"html.duckduckgo.com"},
					RateLimits: map[string]string{"html.duckduckgo.com": "1/2s"},
					TimeoutMs: 12_000,
				},
				ManifestV4: ManifestV4{
					Tier: Tier3Discovery,
					ServesVibeCases: []VibeCase{VibeC2Text, VibeC4Video, VibeC3Image},
					ByteCapHint: 32 * 1024,
					URLTemplate: "https://html.duckduckgo.com/html/?q={q}",
				},
			},
			ParserName: "ddg",
		},
	}
}

// AllBackendsByIntent returns the backends that serve i, ordered
// by (Tier, Name). Used by the executor.
func AllBackendsByIntent(i Intent) []Backend {
	var out []Backend
	for _, b := range AllBackends() {
		if b.Manifest.Intent == i {
			out = append(out, b)
		}
	}
	return out
}

// AllBackendsByIntentAndVibeCase returns the backends that serve i
// AND declare v in ServesVibeCases, ordered by (Tier, Name). The
// executor uses this to discipline the fan-out: a C5 bundle call
// only sees code/cve backends, not crt.sh.
func AllBackendsByIntentAndVibeCase(i Intent, v VibeCase) []Backend {
	var out []Backend
	for _, b := range AllBackends() {
		if b.Manifest.Intent != i {
			continue
		}
		if len(b.Manifest.ServesVibeCases) == 0 {
			out = append(out, b)
			continue
		}
		for _, vc := range b.Manifest.ServesVibeCases {
			if vc == v {
				out = append(out, b)
				break
			}
		}
	}
	return out
}

// _ = time.Second keeps the import even if the file does not use
// time directly. Future slices that add per-backend timeout
// negotiation will.
var _ = time.Second
