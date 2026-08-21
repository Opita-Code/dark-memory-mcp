package artifact

// Tests for SSRFGuard + safeURLFetcher. T02 of spec 1276 v2.20.0.
//
// Coverage matrix:
//   - Scheme: https / http+AllowInsecure / http-!AllowInsecure / file /
//     gopher / javascript / empty / malformed.
//   - IP literal: public, private (10/8, 172.16/12, 192.168/16), loopback,
//     link-local unicast (169.254/16, fe80::/10), unique-local (fc00::/7),
//     multicast, unspecified, IPv4-mapped IPv6.
//   - Hostname via DNS: public, private, mixed (rebinding), unresolvable.
//   - Allowlist mode (AllowedCIDRs).
//   - safeURLFetcher integration with httptest.Server.
//
// Mutation score target: M1 ≥75% (dark-testing v4.0.0). The tests below
// kill every reachable mutant as of T02.

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeResolver is a stub DNSResolver. LookupHost reads from answers;
// err is returned if non-nil.
type fakeResolver struct {
	answers map[string][]string
	err     error
}

func (f *fakeResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.answers[host], nil
}

// newGuardWithCIDR builds a guard from a list of CIDR strings for the
// allowlist. Empty cidrList means no allowlist.
func newGuardWithCIDR(t *testing.T, cidrList ...string) *SSRFGuard {
	t.Helper()
	g := &SSRFGuard{}
	for _, s := range cidrList {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			t.Fatalf("ParseCIDR(%q): %v", s, err)
		}
		g.AllowedCIDRs = append(g.AllowedCIDRs, n)
	}
	return g
}

func TestSSRFGuard_Scheme(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		allow   bool
		secure   bool // AllowInsecure = true
		wantErr error
	}{
		{"https allowed", "https://example.com/", true, false, nil},
		{"http allowed insecure", "http://example.com/", true, true, nil},
		{"http blocked secure", "http://example.com/", false, false, ErrBlockedScheme},
		{"file blocked", "file:///etc/passwd", false, false, ErrBlockedScheme},
		{"gopher blocked", "gopher://example.com/", false, false, ErrBlockedScheme},
		{"javascript blocked", "javascript:alert(1)", false, true, ErrBlockedScheme},
		{"ftp blocked", "ftp://example.com/", false, false, ErrBlockedScheme},
		{"empty scheme", "//example.com/", false, false, ErrInvalidURL},
		{"unknown scheme", "data:text/plain,foo", false, true, ErrBlockedScheme},
		{"uppercase HTTPS", "HTTPS://example.com/", true, false, nil},
		{"mixed-case Http", "Http://example.com/", true, true, nil},
	}
	resolver := &fakeResolver{answers: map[string][]string{
		"example.com": {"93.184.216.34"},
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &SSRFGuard{Resolver: resolver, AllowInsecure: tt.secure}
			err := g.Allow(context.Background(), tt.url)
			if tt.allow {
				if err != nil {
					t.Fatalf("Allow: %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Allow: %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestSSRFGuard_IPLiterals(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		allow   bool
		wantErr error
	}{
		// Public — should pass.
		{"public IPv4 8.8.8.8", "https://8.8.8.8/", true, nil},
		{"public IPv4 1.1.1.1", "https://1.1.1.1/", true, nil},
		{"public IPv4 boundary 172.32.0.0", "https://172.32.0.0/", true, nil},
		{"public IPv4 boundary 172.15.255.255", "https://172.15.255.255/", true, nil},
		{"public IPv6 2606:4700:4700::1111", "https://[2606:4700:4700::1111]/", true, nil},
		{"public IPv6 docs prefix 2001:db8::1", "https://[2001:db8::1]/", true, nil},

		// Loopback — should block.
		{"loopback IPv4 127.0.0.1", "https://127.0.0.1/", false, ErrBlockedHost},
		{"loopback IPv4 127.255.255.255", "https://127.255.255.255/", false, ErrBlockedHost},
		{"loopback IPv6 ::1", "https://[::1]/", false, ErrBlockedHost},

		// Private IPv4 — should block.
		{"private 10.0.0.1", "https://10.0.0.1/", false, ErrBlockedHost},
		{"private 10.255.255.255", "https://10.255.255.255/", false, ErrBlockedHost},
		{"private 172.16.0.1", "https://172.16.0.1/", false, ErrBlockedHost},
		{"private 172.31.255.255", "https://172.31.255.255/", false, ErrBlockedHost},
		{"private 192.168.1.1", "https://192.168.1.1/", false, ErrBlockedHost},

		// Link-local — cloud metadata here.
		{"link-local 169.254.169.254 AWS metadata", "https://169.254.169.254/latest/meta-data/", false, ErrBlockedHost},
		{"link-local 169.254.0.1", "https://169.254.0.1/", false, ErrBlockedHost},

		// IPv6 link-local / unique-local.
		{"IPv6 link-local fe80::1", "https://[fe80::1]/", false, ErrBlockedHost},
		{"IPv6 unique-local fc00::1", "https://[fc00::1]/", false, ErrBlockedHost},
		{"IPv6 unique-local fd00::1", "https://[fd00::1]/", false, ErrBlockedHost},

		// Multicast.
		{"IPv4 multicast 224.0.0.1", "https://224.0.0.1/", false, ErrBlockedHost},
		{"IPv6 multicast ff02::1", "https://[ff02::1]/", false, ErrBlockedHost},

		// Unspecified.
		{"unspecified IPv4 0.0.0.0", "https://0.0.0.0/", false, ErrBlockedHost},
		{"unspecified IPv6 ::", "https://[::]/", false, ErrBlockedHost},

		// IPv4-mapped IPv6.
		{"IPv4-mapped 10.0.0.1", "https://[::ffff:10.0.0.1]/", false, ErrBlockedHost},
		{"IPv4-mapped 127.0.0.1", "https://[::ffff:127.0.0.1]/", false, ErrBlockedHost},
		{"IPv4-mapped 8.8.8.8 (allowed)", "https://[::ffff:8.8.8.8]/", true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &SSRFGuard{}
			err := g.Allow(context.Background(), tt.url)
			if tt.allow {
				if err != nil {
					t.Fatalf("Allow(%s): %v, want nil", tt.url, err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Allow(%s): %v, want %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

func TestSSRFGuard_Hostname(t *testing.T) {
	tests := []struct {
		name        string
		hostAnswers map[string][]string
		url         string
		allow       bool
		wantErr     error
	}{
		{
			name:        "public hostname",
			hostAnswers: map[string][]string{"example.com": {"93.184.216.34"}},
			url:         "https://example.com/",
			allow:       true,
		},
		{
			name:        "private hostname",
			hostAnswers: map[string][]string{"internal.local": {"10.0.0.5"}},
			url:         "https://internal.local/",
			allow:       false,
			wantErr:     ErrBlockedHost,
		},
		{
			name: "mixed public + private (DNS rebinding)",
			hostAnswers: map[string][]string{
				"rebinding.local": {"1.1.1.1", "10.0.0.1"},
			},
			url:     "https://rebinding.local/",
			allow:   false,
			wantErr: ErrBlockedHost,
		},
		{
			name: "mixed private + public (different order)",
			hostAnswers: map[string][]string{
				"rebinding.local": {"10.0.0.1", "1.1.1.1"},
			},
			url:     "https://rebinding.local/",
			allow:   false,
			wantErr: ErrBlockedHost,
		},
		{
			name: "all public multiple IPs",
			hostAnswers: map[string][]string{
				"dual.example.com": {"1.1.1.1", "8.8.8.8"},
			},
			url:   "https://dual.example.com/",
			allow: true,
		},
		{
			name:        "hostname unresolvable",
			hostAnswers: map[string][]string{},
			url:         "https://does-not-exist.local/",
			allow:       false,
			wantErr:     ErrUnresolvable,
		},
		{
			name: "hostname resolves to invalid IP string",
			hostAnswers: map[string][]string{
				"broken.local": {"not-an-ip"},
			},
			url:     "https://broken.local/",
			allow:   false,
			wantErr: ErrUnresolvable,
		},
		{
			name: "hostname with userinfo still classifies by host",
			hostAnswers: map[string][]string{
				"example.com": {"93.184.216.34"},
			},
			url:   "https://user:pass@example.com/",
			allow: true,
		},
		{
			name: "hostname with port",
			hostAnswers: map[string][]string{
				"example.com": {"93.184.216.34"},
			},
			url:   "https://example.com:8443/",
			allow: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &SSRFGuard{Resolver: &fakeResolver{answers: tt.hostAnswers}}
			err := g.Allow(context.Background(), tt.url)
			if tt.allow {
				if err != nil {
					t.Fatalf("Allow(%s): %v, want nil", tt.url, err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Allow(%s): %v, want %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

func TestSSRFGuard_AllowedCIDRs(t *testing.T) {
	tests := []struct {
		name string
		cidr []string
		url  string
		want error
	}{
		{
			name: "single CIDR contains 127.0.0.1",
			cidr: []string{"127.0.0.0/8"},
			url:  "https://127.0.0.1:8080/",
			want: nil,
		},
		{
			name: "single CIDR excludes 10.0.0.1",
			cidr: []string{"127.0.0.0/8"},
			url:  "https://10.0.0.1/",
			want: ErrBlockedHost,
		},
		{
			name: "CIDR excludes public IP",
			cidr: []string{"10.0.0.0/8"},
			url:  "https://8.8.8.8/",
			want: ErrBlockedHost,
		},
		{
			name: "multiple CIDRs — public in second",
			cidr: []string{"10.0.0.0/8", "1.1.1.1/32"},
			url:  "https://1.1.1.1/",
			want: nil,
		},
		{
			name: "allowlist with hostname resolution",
			cidr: []string{"1.1.1.1/32"},
			url:  "https://public.example.com/",
			want: nil, // resolver returns 1.1.1.1
		},
		{
			name: "allowlist with hostname returning non-listed IP",
			cidr: []string{"10.0.0.0/8"},
			url:  "https://public.example.com/",
			want: ErrBlockedHost,
		},
	}
	resolver := &fakeResolver{answers: map[string][]string{
		"public.example.com": {"1.1.1.1"},
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newGuardWithCIDR(t, tt.cidr...)
			g.Resolver = resolver
			err := g.Allow(context.Background(), tt.url)
			if tt.want == nil {
				if err != nil {
					t.Fatalf("Allow: %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("Allow: %v, want %v", err, tt.want)
			}
		})
	}
}

func TestSSRFGuard_InvalidURL(t *testing.T) {
	g := &SSRFGuard{}
	tests := []string{
		"",
		"://noscheme",
		"https://",
		"\x00invalid",
	}
	for _, raw := range tests {
		t.Run("url="+raw, func(t *testing.T) {
			err := g.Allow(context.Background(), raw)
			if !errors.Is(err, ErrInvalidURL) && !errors.Is(err, ErrUnresolvable) {
				t.Fatalf("Allow(%q): %v, want ErrInvalidURL or ErrUnresolvable", raw, err)
			}
		})
	}
}

func TestSSRFGuard_DefaultResolver_FailClosed(t *testing.T) {
	// Use the system resolver with a definitely-nonexistent host.
	g := &SSRFGuard{} // nil Resolver → net.DefaultResolver
	err := g.Allow(context.Background(), "https://this-host-does-not-exist.invalid/")
	if !errors.Is(err, ErrUnresolvable) {
		t.Fatalf("Allow: %v, want ErrUnresolvable", err)
	}
}

// --- safeURLFetcher wiring ------------------------------------------------

type stubFetcher struct {
	body []byte
	err  error
}

func (s *stubFetcher) Fetch(_ context.Context, _ string, _ int) ([]byte, error) {
	return s.body, s.err
}

func TestSafeURLFetcher_BlocksBeforeInnerFetch(t *testing.T) {
	inner := &stubFetcher{body: []byte("should not be called")}
	guard := &SSRFGuard{Resolver: &fakeResolver{answers: map[string][]string{
		"example.com": {"10.0.0.1"}, // private
	}}}
	f := NewSafeURLFetcher(guard, inner)
	body, err := f.Fetch(context.Background(), "https://example.com/", 1024)
	if err == nil {
		t.Fatalf("Fetch: nil err, want ErrBlockedHost")
	}
	if !errors.Is(err, ErrBlockedHost) {
		t.Fatalf("Fetch: %v, want ErrBlockedHost", err)
	}
	if body != nil {
		t.Errorf("Fetch returned body %q, want nil", body)
	}
}

func TestSafeURLFetcher_AllowsAndDelegates(t *testing.T) {
	inner := &stubFetcher{body: []byte("ok")}
	guard := &SSRFGuard{Resolver: &fakeResolver{answers: map[string][]string{
		"example.com": {"93.184.216.34"},
	}}}
	f := NewSafeURLFetcher(guard, inner)
	body, err := f.Fetch(context.Background(), "https://example.com/", 1024)
	if err != nil {
		t.Fatalf("Fetch: %v, want nil", err)
	}
	if string(body) != "ok" {
		t.Errorf("body: %q, want %q", body, "ok")
	}
}

func TestSafeURLFetcher_Integration_HTTPTestServer(t *testing.T) {
	// Real HTTP server bound to 127.0.0.1. Configure guard with
	// allowlist mode to permit loopback (necessary because tests bind
	// locally; production never allows 127.0.0.1).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(w, strings.NewReader("hello from test server"))
	}))
	defer srv.Close()

	t.Run("loopback allowed via allowlist", func(t *testing.T) {
		// httptest.NewServer uses http://, not https://. Enable
		// AllowInsecure for this test only.
		guard := newGuardWithCIDR(t, "127.0.0.0/8")
		guard.AllowInsecure = true
		// Real HTTP fetcher using net/http.
		inner := &httpClientFetcher{client: srv.Client()}
		f := NewSafeURLFetcher(guard, inner)
		body, err := f.Fetch(context.Background(), srv.URL, 1024)
		if err != nil {
			t.Fatalf("Fetch: %v, want nil", err)
		}
		if !strings.Contains(string(body), "hello") {
			t.Errorf("body: %q, want hello...", body)
		}
	})

	t.Run("loopback blocked in blocklist mode", func(t *testing.T) {
		guard := &SSRFGuard{AllowInsecure: true} // http allowed, but 127.0.0.1 blocked
		inner := &httpClientFetcher{client: srv.Client()}
		f := NewSafeURLFetcher(guard, inner)
		_, err := f.Fetch(context.Background(), srv.URL, 1024)
		if !errors.Is(err, ErrBlockedHost) {
			t.Fatalf("Fetch: %v, want ErrBlockedHost", err)
		}
	})

	t.Run("public IP passed-through", func(t *testing.T) {
		// The test server URL points to 127.0.0.1 but we configure the
		// resolver to claim it is a public IP. The guard's blocklist
		// check is bypassed because we trust the resolver; the dial
		// will fail because the IP doesn't actually serve anything.
		// The point: the guard's Allow() should NOT block a public IP.
		guard := &SSRFGuard{Resolver: &fakeResolver{answers: map[string][]string{
			"public.example.com": {"8.8.8.8"},
		}}}
		inner := &httpClientFetcher{client: srv.Client()}
		f := NewSafeURLFetcher(guard, inner)
		_, err := f.Fetch(context.Background(), "https://public.example.com/", 1024)
		// Allow() should succeed; the dial may fail later (DNS+connect).
		// We accept either nil or a connection error, but NOT ErrBlockedHost.
		if errors.Is(err, ErrBlockedHost) {
			t.Fatalf("Fetch: %v, want non-block error", err)
		}
	})
}

// httpClientFetcher is a URLFetcher backed by net/http. Used in the
// integration test above; T08 will use a production-grade version with
// timeout, redirect policy, and single-resolve dial.
type httpClientFetcher struct {
	client *http.Client
}

func (h *httpClientFetcher) Fetch(ctx context.Context, rawURL string, cap int) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, int64(cap)))
}

func TestSSRFGuard_BlockedHost_ErrorMessageMentionsIP(t *testing.T) {
	// For audit trails, the error message should contain the IP that
	// was blocked. This is useful for forensics.
	g := &SSRFGuard{}
	err := g.Allow(context.Background(), "https://10.0.0.5/")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "10.0.0.5") {
		t.Errorf("error message %q should contain the blocked IP", err.Error())
	}
}