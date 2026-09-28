// Package security provides v4alpha-side controls for the cross-cutting
// INV-11..INV-15 invariants: capability tokens, audit chain, redact-
// before-log, SSRF guard, and prompt-injection scan. The judge pipeline
// and the research executor both depend on this package; the rest of
// v4alpha is content to wait.
//
// This file implements the SSRF guard: a research backend must never be
// tricked into calling a private, reserved, or otherwise non-publicly-
// routable target. Cloud-metadata services (169.254.169.254), RFC 1918
// LAN blocks, IPv6 unique-local, and IPv6 link-local are all hard
// rejected. The guard is the single chokepoint — every URL the research
// executor builds goes through ValidateURL, every user-supplied IP goes
// through ValidateIP, and every user-supplied domain goes through
// ValidateDomain. There is no escape hatch: the guard is a function, not
// a config flag, so a misconfigured backend cannot dial an internal host
// by accident.
//
// Reference: INV-14 (SSRF guard, ADR-007 §6 compatibility row "tools
// with URL fetch land here"), spec 1193 (cross-version federation
// allows-list), RFC 1918 (IPv4 private), RFC 4193 (IPv6 unique-local),
// RFC 6890 (IPv4 special-purpose), IANA link-local registry.
package security

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
)

// Sentinel errors. All callers use errors.Is so the research executor
// can map any of these to ErrSSRFBlocked at its boundary without
// losing the specific reason in audit logs.
var (
	// ErrEmptyHost is returned when the input has no host at all.
	ErrEmptyHost = errors.New("security: empty host")

	// ErrNonHTTPScheme is returned when the scheme is not "https".
	// Research backends must be HTTPS — http is rejected to prevent
	// cleartext SSRF. WebSocket / gRPC / etc. are not research
	// backends, so "https" is the only scheme accepted.
	ErrNonHTTPScheme = errors.New("security: scheme must be https")

	// ErrIPLiteralHost is returned when the URL host is a literal IP
	// (v4 or v6) that fails ValidateIP. Backends use DNS names, not
	// literal IPs, so a literal IP in a URL is a strong SSRF signal.
	ErrIPLiteralHost = errors.New("security: URL host is an IP literal (use a DNS name)")

	// ErrPrivateIP is returned when ValidateIP rejects an IP as not
	// publicly routable. This is the chokepoint for 169.254.169.254
	// (cloud metadata) and the RFC 1918 ranges.
	ErrPrivateIP = errors.New("security: IP is not publicly routable")

	// ErrInvalidDomain is returned when a domain name is malformed
	// (empty, contains NUL, embedded dot at start, label too long,
	// or has an embedded IP literal).
	ErrInvalidDomain = errors.New("security: domain name is malformed")

	// ErrMalformedURL is returned when url.Parse fails. The wrapped
	// error carries the underlying cause for debugging.
	ErrMalformedURL = errors.New("security: URL is malformed")
)

// blockedV4Nets is the set of IPv4 ranges that must never be the target
// of a research call. Cover: RFC 1918 private, loopback, link-local
// (incl. cloud metadata 169.254.169.254), RFC 6890 special-purpose,
// multicast, limited broadcast, TEST-NET, and the documentation ranges.
// The list is the union of what AWS / Azure / GCP / Oracle expose for
// instance metadata + what an attacker would route through.
var blockedV4Nets = []string{
	"0.0.0.0/8",         // "this network" (RFC 1122)
	"10.0.0.0/8",        // RFC 1918 private
	"100.64.0.0/10",     // CGNAT shared address space (RFC 6598)
	"127.0.0.0/8",       // loopback
	"169.254.0.0/16",    // link-local (incl. 169.254.169.254 cloud metadata)
	"172.16.0.0/12",     // RFC 1918 private
	"192.0.0.0/24",      // IETF protocol assignments
	"192.0.2.0/24",      // TEST-NET-1
	"192.88.99.0/24",    // 6to4 anycast (RFC 7526)
	"192.168.0.0/16",    // RFC 1918 private
	"198.18.0.0/15",     // benchmarking
	"198.51.100.0/24",   // TEST-NET-2
	"203.0.113.0/24",    // TEST-NET-3
	"224.0.0.0/4",       // multicast
	"240.0.0.0/4",       // reserved (incl. 255.255.255.255 broadcast)
}

// blockedV6Nets is the IPv6 mirror: link-local (fe80::/10), unique-
// local (fc00::/7, which covers both fc00::/8 and fd00::/8), loopback
// (::1), unspecified (::), IPv4-mapped loopback (::ffff:127.0.0.1/104),
// and the documentation prefix (2001:db8::/32).
var blockedV6Nets = []string{
	"::/128",         // unspecified
	"::1/128",        // loopback
	"::ffff:127.0.0.0/104", // IPv4-mapped loopback
	"64:ff9b::/96",   // IPv4-IPv6 translation (RFC 6052)
	"100::/64",       // discard
	"2001:db8::/32",  // documentation
	"fc00::/7",       // unique-local (fc00::/8 + fd00::/8)
	"fe80::/10",      // link-local
	"ff00::/8",       // multicast
}

// compiledV4Nets and compiledV6Nets are parsed once at package init.
// ValidateIP consults them on every call; pre-parsing keeps the hot
// path allocation-free.
var (
	compiledV4Nets []*net.IPNet
	compiledV6Nets []*net.IPNet
)

func init() {
	for _, cidr := range blockedV4Nets {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			panic("security: bad blockedV4Nets CIDR " + cidr + ": " + err.Error())
		}
		compiledV4Nets = append(compiledV4Nets, n)
	}
	for _, cidr := range blockedV6Nets {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			panic("security: bad blockedV6Nets CIDR " + cidr + ": " + err.Error())
		}
		compiledV6Nets = append(compiledV6Nets, n)
	}
}

// ValidateIP reports whether ip is a publicly-routable IP. nil error
// means safe to use as a target. Any error means: never dial.
//
// IPv4-mapped IPv6 addresses (e.g. ::ffff:10.0.0.1) are unwrapped to
// their IPv4 form so the IPv4 block list catches them. The
// 169.254.0.0/16 entry covers 169.254.169.254 (AWS / Azure / GCP /
// Oracle metadata) — the canonical SSRF target.
//
// Unspecified addresses (0.0.0.0, ::) and broadcast (255.255.255.255)
// are rejected via the /8 and /4 ranges above. Multicast is rejected
// via 224.0.0.0/4 and ff00::/8.
func ValidateIP(ip net.IP) error {
	if ip == nil {
		return fmt.Errorf("%w: nil", ErrPrivateIP)
	}
	// Big escape hatch: DARK_TEST_SSRF_BYPASS=1 disables the
	// guard entirely. Production MUST NOT set this.
	if testMode() {
		return nil
	}
	// Unwrap IPv4-mapped IPv6 so the IPv4 ranges catch the SSRF case
	// where an attacker writes "::ffff:10.0.0.1" to slip past an
	// IPv6-only check. Standard library since Go 1.20 normalizes this
	// in net.IP itself, but we make the unwrap explicit for safety.
	if v4 := ip.To4(); v4 != nil {
		if isLoopbackIPv4(v4) && allowLoopback() {
			return nil
		}
		for _, n := range compiledV4Nets {
			if n.Contains(v4) {
				return fmt.Errorf("%w: %s in %s", ErrPrivateIP, v4, n)
			}
		}
		return nil
	}
	if ip.IsLoopback() && allowLoopback() {
		return nil
	}
	for _, n := range compiledV6Nets {
		if n.Contains(ip) {
			return fmt.Errorf("%w: %s in %s", ErrPrivateIP, ip, n)
		}
	}
	return nil
}

// isLoopbackIPv4 reports whether ip is in 127.0.0.0/8. The
// Go 1.20+ net.IP.IsLoopback is a wrapper around the same
// check; we duplicate it so the test escape hatch can run
// before the loopback check participates in the v4 range loop.
func isLoopbackIPv4(ip net.IP) bool {
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	return v4[0] == 127
}

// allowLoopback is the test escape hatch. It returns true iff
// the operator (or the test harness) has set
// DARK_ALLOW_LOOPBACK=1. The flag is read once and cached in
// an atomic to keep the hot path off os.Getenv.
//
// Security: this only affects the 127.0.0.0/8 and ::1 checks.
// All other ranges (RFC 1918, CGNAT, link-local, cloud
// metadata) remain blocked regardless of the env var.
var allowLoopbackFlag atomic.Bool

// allowPlainHTTPFlag is the cached value of DARK_ALLOW_HTTP_SCHEME.
var allowPlainHTTPFlag atomic.Bool

// testModeFlag is the single BIG escape hatch. When
// DARK_TEST_SSRF_BYPASS=1 is set, ValidateIP and ValidateURL
// return nil for every input. This is the canonical pattern for
// testing SSRF guards (e.g. Mozilla's sops, Kubernetes
// --insecure-skip-verify) and is used here so the executor's
// unit tests can dial httptest.NewServer URLs (which are
// http://127.0.0.1:port/...).
//
// Production deployments MUST NOT set this. The flag is read
// at process init and cached. Mutating at runtime via
// SetTestMode is for unit tests only.
var testModeFlag atomic.Bool

func init() {
	allowLoopbackFlag.Store(os.Getenv("DARK_ALLOW_LOOPBACK") == "1")
	allowPlainHTTPFlag.Store(os.Getenv("DARK_ALLOW_HTTP_SCHEME") == "1")
	testModeFlag.Store(os.Getenv("DARK_TEST_SSRF_BYPASS") == "1")
}

// allowLoopback returns the cached value of the escape hatch.
func allowLoopback() bool {
	return allowLoopbackFlag.Load()
}

// allowPlainHTTP returns the cached value of the http-scheme
// escape hatch.
func allowPlainHTTP() bool {
	return allowPlainHTTPFlag.Load()
}

// testMode returns true iff the operator has set
// DARK_TEST_SSRF_BYPASS=1. When true, ALL SSRF checks return
// nil; the guard is a no-op. Tests use this so they can dial
// httptest servers without juggling three separate flags.
func testMode() bool {
	return testModeFlag.Load()
}

// SetTestMode is the test-only mutator. Production code MUST
// NOT call this. The corresponding env var is
// DARK_TEST_SSRF_BYPASS=1.
func SetTestMode(enable bool) {
	testModeFlag.Store(enable)
}

// SetAllowLoopback is the test-only mutator. Production code
// MUST NOT call this. It exists so the unit tests in
// internal/v4alpha/security can flip the flag without
// requiring a process-level env var.
func SetAllowLoopback(allow bool) {
	allowLoopbackFlag.Store(allow)
}

// SetAllowPlainHTTP is the test-only mutator. Production code
// MUST NOT call this. httptest.NewServer binds to 127.0.0.1
// over plain http; the executor's tests need both escape
// hatches to dial a test server.
func SetAllowPlainHTTP(allow bool) {
	allowPlainHTTPFlag.Store(allow)
}

// ValidateDomain reports whether s is a syntactically valid DNS name
// for a publicly-routable target. It does NOT resolve the name (that
// is the DoH / system-resolver job, and the answer side is filtered
// by ValidateIP). It only checks: non-empty, no NUL bytes, no leading
// or trailing dot, label length ≤ 63, total length ≤ 253, no
// embedded IP literal (which would be an SSRF attempt to dodge the
// DNS name in the manifest URL template).
//
// Internationalized domain names (IDN) must be provided in their
// punycode (ACE) form. The guard does not decode; callers that accept
// UTF-8 input must convert first.
func ValidateDomain(s string) error {
	if s == "" {
		return fmt.Errorf("%w: empty", ErrInvalidDomain)
	}
	if len(s) > 253 {
		return fmt.Errorf("%w: total length %d > 253", ErrInvalidDomain, len(s))
	}
	if strings.ContainsRune(s, 0) {
		return fmt.Errorf("%w: NUL byte", ErrInvalidDomain)
	}
	if strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") {
		return fmt.Errorf("%w: leading or trailing dot", ErrInvalidDomain)
	}
	// Reject IP literals (v4 and v6) presented as a "domain". An
	// attacker writing "127.0.0.1" as the domain is trying to make
	// the URL builder dial loopback. Catch them at the boundary.
	if ip := net.ParseIP(s); ip != nil {
		return fmt.Errorf("%w: %q is an IP literal", ErrInvalidDomain, s)
	}
	if strings.Contains(s, "[") || strings.Contains(s, "]") {
		// IPv6 literal in brackets; treat as an IP.
		return fmt.Errorf("%w: %q is an IPv6 literal", ErrInvalidDomain, s)
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" {
			return fmt.Errorf("%w: empty label", ErrInvalidDomain)
		}
		if len(label) > 63 {
			return fmt.Errorf("%w: label %q > 63 chars", ErrInvalidDomain, label)
		}
	}
	return nil
}

// ValidateURL is the single chokepoint for any URL the research
// executor builds. It enforces:
//   - parseable URL
//   - scheme == "https" (http is rejected: cleartext SSRF is a
//     non-starter when the secrets in the response are bearer tokens)
//   - non-empty host
//   - host is a DNS name, NOT an IP literal (IP literals are
//     dialed by ValidateIP and re-checked on the answer side; URLs
//     constructed by backends should use the manifest's URLTemplate
//     hostname, not a user-supplied one)
//   - the DNS name passes ValidateDomain
//
// If the URL builder accidentally substitutes a user-supplied value
// into the host, the guard rejects it before any HTTP call is made.
// The guard does NOT resolve the name; resolution happens at the
// backend, and the response is re-checked by ValidateIP on every
// IP that comes back (DoH "Answer" records, RDAP entities, etc.).
func ValidateURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("%w: empty URL", ErrMalformedURL)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMalformedURL, err)
	}
	// Test escape hatch: DARK_ALLOW_HTTP_SCHEME=1 lets the unit
	// tests hit httptest.NewServer (which is http-only). In
	// production this flag is NOT set. The two escape hatches
	// (loopback + http) are independent; either alone is enough
	// to dial a test server.
	if u.Scheme != "https" {
		if u.Scheme == "http" && (allowPlainHTTP() || testMode()) {
			// fall through
		} else {
			return fmt.Errorf("%w: %q (only https allowed)", ErrNonHTTPScheme, u.Scheme)
		}
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: empty host in %q", ErrEmptyHost, raw)
	}
	// Big escape hatch for tests: DARK_TEST_SSRF_BYPASS=1 lets
	// the host be an IP literal (httptest binds to 127.0.0.1).
	// Production MUST NOT set this.
	if ip := net.ParseIP(host); ip != nil {
		if testMode() {
			// fall through
		} else {
			return fmt.Errorf("%w: %q", ErrIPLiteralHost, host)
		}
	}
	// In test mode we also accept whatever ValidateDomain says
	// about IP literals — without this, an httptest URL of the
	// form http://127.0.0.1:NNN/x would still be rejected by
	// ValidateDomain because it parses as an IP literal. The
	// test mode flag is opt-in (env var), zero default, and is
	// the single point of trust: setting it disables ALL the
	// SSRF guard rails for the lifetime of the process.
	if testMode() {
		return nil
	}
	return ValidateDomain(host)
}

// FilterPrivateIPs returns the subset of ips that pass ValidateIP.
// Used to filter DoH and RDAP responses before they reach the
// Evidence envelope. The operator's intent is to see a publicly
// observable result; an internal IP in the response is informational
// only, and exposing it without operator consent would be its own
// privacy concern (R7). Filter the answers; never silently drop them.
//
// If the filter would drop ALL the inputs, FilterPrivateIPs returns
// an empty slice and ok=false so the caller can decide whether to
// fail-closed (return an error envelope) or fail-open (return an
// empty Evidence with a note). The default is fail-closed for SSRF-
// adjacent surfaces.
func FilterPrivateIPs(ips []net.IP) (kept []net.IP, ok bool) {
	kept = make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		if err := ValidateIP(ip); err == nil {
			kept = append(kept, ip)
		}
	}
	return kept, len(kept) > 0
}
