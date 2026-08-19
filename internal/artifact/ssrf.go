package artifact

// SSRF guard (CWE-918 / OWASP A10:2021). T02 of spec 1276 v2.20.0.
//
// Design:
//
//   - SSRFGuard classifies a URL as safe to fetch. It does not perform the
//     fetch itself; that is delegated to URLFetcher (T01's interface).
//   - Hostname→IP resolution is done by the injected DNSResolver so tests
//     can pin answers. Production uses net.DefaultResolver.
//   - Every resolved IP is checked. If ANY is in a blocked range, the URL
//     is rejected. This is the DNS-rebinding defense: an attacker who
//     rotates DNS answers between two lookups (public, then private) can
//     not get the guard to approve a private IP, because we check ALL
//     resolved IPs at Allow() time and require all to be public.
//   - For IP literals (no DNS), the IP is checked directly.
//   - IPv4-mapped IPv6 (::ffff:10.0.0.1) is normalized to IPv4 before
//     classification.
//   - IPv6 zone identifiers (fe80::1%eth0) are stripped before lookup.
//   - AllowedCIDRs is an OPTIONAL allowlist mode. When non-empty, the
//     blocklist is ignored and only IPs in the allowlist are accepted.
//     This is useful for "only fetch from example.com" configs.
//
// What is NOT in scope (deferred to T08 or later):
//
//   - HTTP redirect handling. The guard classifies the initial URL; the
//     caller must re-classify each Location: header. T08 will wire this.
//   - Single-resolve-then-dial-with-IP. The current implementation
//     resolves in Allow() and then dials the host (which re-resolves).
//     A determined attacker can rebind between the two resolutions. T08
//     will close this gap with a custom dialer that pins the IP from
//     Allow()'s resolution. For T02 we check all resolved IPs.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// SSRF guard sentinel errors. Use errors.Is to distinguish.
var (
	// ErrBlockedScheme: URL uses a non-http(s) scheme, or http when
	// AllowInsecure is false. The caller cannot override this; it's a
	// property of the URL itself.
	ErrBlockedScheme = errors.New("artifact: blocked URL scheme")

	// ErrBlockedHost: hostname (resolved) or IP literal is in a blocked
	// range (loopback, private, link-local, multicast, etc.) or is not
	// in the configured allowlist.
	ErrBlockedHost = errors.New("artifact: blocked URL host")

	// ErrUnresolvable: DNS lookup failed or returned no usable IPs.
	// We fail-closed: an unresolvable hostname is NOT fetchable.
	ErrUnresolvable = errors.New("artifact: hostname unresolvable")

	// ErrInvalidURL: URL parsing failed (malformed, no host, etc.).
	ErrInvalidURL = errors.New("artifact: invalid URL")
)

// DNSResolver abstracts hostname→IP resolution. net.DefaultResolver
// satisfies this interface. nil SSRFGuard.Resolver means "use the
// system resolver".
type DNSResolver interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// SSRFGuard decides whether a URL is safe to fetch under the configured
// policy. Construct one with the desired Resolver, AllowInsecure, and
// optional AllowedCIDRs allowlist.
type SSRFGuard struct {
	// Resolver is the DNS resolver. nil = use net.DefaultResolver.
	Resolver DNSResolver

	// AllowInsecure permits http:// URLs. Default false (https only).
	AllowInsecure bool

	// AllowedCIDRs, if non-empty, switches the guard into allowlist
	// mode: an IP is allowed only if it is in at least one of these
	// ranges. The blocklist (loopback, private, link-local, multicast)
	// is then ignored. Useful for restricting fetches to a partner's
	// IP range.
	AllowedCIDRs []*net.IPNet
}

// Allow classifies rawURL under the SSRF guard's policy. Returns nil if
// the URL is safe to fetch, or one of the sentinel errors otherwise.
//
// Classification pipeline:
//  1. Parse URL. Reject malformed URLs.
//  2. Check scheme (https always; http if AllowInsecure).
//  3. Strip IPv6 zone identifier.
//  4. If host is an IP literal, classify it directly.
//  5. Else resolve via DNS. Every resolved IP must classify as allowed.
//     Failure to resolve or empty answer → ErrUnresolvable.
//
// errors.Is-compatible: callers can branch on the sentinel.
func (g *SSRFGuard) Allow(ctx context.Context, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}

	// Scheme check.
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "https":
		// ok
	case "http":
		if !g.AllowInsecure {
			return fmt.Errorf("%w: http (set AllowInsecure=true to allow)", ErrBlockedScheme)
		}
	case "":
		return fmt.Errorf("%w: empty scheme", ErrInvalidURL)
	default:
		return fmt.Errorf("%w: scheme %q", ErrBlockedScheme, scheme)
	}

	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: empty host", ErrInvalidURL)
	}

	// Strip IPv6 zone identifier (fe80::1%eth0 → fe80::1).
	if i := strings.Index(host, "%"); i >= 0 {
		host = host[:i]
	}

	// Try IP literal first.
	if ip := net.ParseIP(host); ip != nil {
		return g.checkIP(ip)
	}

	// Hostname — resolve via DNS.
	ips, err := g.lookupHost(ctx, host)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrUnresolvable, host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("%w: %s returned no IPs", ErrUnresolvable, host)
	}

	// Every resolved IP must be allowed. This is the DNS-rebinding
	// defense — if the attacker returns both public and private IPs,
	// we block because at least one is private.
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			return fmt.Errorf("%w: %s resolved to invalid IP %q", ErrUnresolvable, host, ipStr)
		}
		if err := g.checkIP(ip); err != nil {
			return err
		}
	}
	return nil
}

// lookupHost dispatches to the injected resolver or net.DefaultResolver.
func (g *SSRFGuard) lookupHost(ctx context.Context, host string) ([]string, error) {
	if g.Resolver != nil {
		return g.Resolver.LookupHost(ctx, host)
	}
	var r net.Resolver
	return r.LookupHost(ctx, host)
}

// checkIP classifies a single IP under the guard's policy. It is the
// single source of truth for "is this IP safe to connect to".
//
// In default (blocklist) mode, an IP is blocked if it is:
//   - unspecified (0.0.0.0, ::)
//   - loopback (127.0.0.0/8, ::1)
//   - private (10/8, 172.16/12, 192.168/16, fc00::/7)
//   - link-local unicast (169.254/16, fe80::/10) — covers cloud metadata
//   - link-local multicast (224.0.0.0/24, ff02::/16)
//   - interface-local multicast (ff01::/16)
//   - multicast (224.0.0.0/4, ff00::/8)
//
// In allowlist mode, an IP must be in at least one AllowedCIDR.
func (g *SSRFGuard) checkIP(ip net.IP) error {
	// Normalize IPv4-mapped IPv6 to IPv4. ::ffff:10.0.0.1 → 10.0.0.1.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}

	// Allowlist mode: skip blocklist, just check membership.
	if len(g.AllowedCIDRs) > 0 {
		for _, cidr := range g.AllowedCIDRs {
			if cidr.Contains(ip) {
				return nil
			}
		}
		return fmt.Errorf("%w: %s not in allowed CIDRs", ErrBlockedHost, ip)
	}

	// Blocklist mode. Order: most-specific check first.
	switch {
	case ip.IsUnspecified():
		return fmt.Errorf("%w: %s is unspecified", ErrBlockedHost, ip)
	case ip.IsLoopback():
		return fmt.Errorf("%w: %s is loopback", ErrBlockedHost, ip)
	case ip.IsPrivate():
		return fmt.Errorf("%w: %s is private", ErrBlockedHost, ip)
	case ip.IsLinkLocalUnicast():
		// 169.254.0.0/16 covers AWS/GCP/Azure metadata at 169.254.169.254.
		// fe80::/10 covers IPv6 link-local.
		return fmt.Errorf("%w: %s is link-local unicast", ErrBlockedHost, ip)
	case ip.IsLinkLocalMulticast():
		return fmt.Errorf("%w: %s is link-local multicast", ErrBlockedHost, ip)
	case ip.IsInterfaceLocalMulticast():
		return fmt.Errorf("%w: %s is interface-local multicast", ErrBlockedHost, ip)
	case ip.IsMulticast():
		return fmt.Errorf("%w: %s is multicast", ErrBlockedHost, ip)
	}
	return nil
}

// safeURLFetcher wraps an SSRFGuard around an existing URLFetcher. It
// is the canonical way to wire a guard into the resolver's URL path.
//
// On Fetch: classify the URL via Allow(); if blocked, return an error
// without calling the inner fetcher. Otherwise delegate to inner with
// the same cap.
type safeURLFetcher struct {
	guard *SSRFGuard
	inner URLFetcher
}

// Compile-time check: safeURLFetcher satisfies URLFetcher.
var _ URLFetcher = (*safeURLFetcher)(nil)

// Fetch classifies rawURL via guard.Allow and delegates to inner on
// success. The cap argument is forwarded verbatim.
func (s *safeURLFetcher) Fetch(ctx context.Context, rawURL string, cap int) ([]byte, error) {
	if err := s.guard.Allow(ctx, rawURL); err != nil {
		return nil, fmt.Errorf("ssrf: %w", err)
	}
	return s.inner.Fetch(ctx, rawURL, cap)
}

// NewSafeURLFetcher wires an SSRFGuard around an inner URLFetcher. The
// returned fetcher enforces guard policy on every call.
//
// Use:
//
//	fetcher := artifact.NewSafeURLFetcher(guard, httpClientFetcher)
//	resolver := &artifact.Resolver{URLs: fetcher}
//
// Production wiring is T08; T02 only provides the building blocks.
func NewSafeURLFetcher(guard *SSRFGuard, inner URLFetcher) URLFetcher {
	return &safeURLFetcher{guard: guard, inner: inner}
}