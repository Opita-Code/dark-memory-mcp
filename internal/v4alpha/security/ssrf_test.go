package security

import (
	"net"
	"strings"
	"testing"
)

// TestValidateIP_BlockedRanges walks the canonical set of blocked
// IPs. Every entry here MUST be rejected; this is the SSRF chokepoint
// for cloud metadata services, RFC 1918 LANs, and link-local targets.
// A single missing entry here is a security bug.
func TestValidateIP_BlockedRanges(t *testing.T) {
	cases := []struct {
		ip   string
		why  string
	}{
		// RFC 1918 private
		{"10.0.0.1", "RFC 1918 10/8"},
		{"10.255.255.255", "RFC 1918 10/8 boundary"},
		{"172.16.0.1", "RFC 1918 172.16/12"},
		{"172.31.255.255", "RFC 1918 172.16/12 boundary"},
		{"192.168.0.1", "RFC 1918 192.168/16"},
		{"192.168.255.255", "RFC 1918 192.168/16 boundary"},
		// Cloud metadata — the canonical SSRF target
		{"169.254.169.254", "AWS / Azure / GCP / Oracle metadata"},
		{"169.254.0.1", "link-local start"},
		// Loopback
		{"127.0.0.1", "loopback"},
		{"127.255.255.254", "loopback boundary"},
		// "this network"
		{"0.0.0.0", "unspecified (0/8)"},
		// CGNAT
		{"100.64.0.1", "CGNAT 100.64/10"},
		// Test / documentation
		{"192.0.2.1", "TEST-NET-1"},
		{"198.51.100.1", "TEST-NET-2"},
		{"203.0.113.1", "TEST-NET-3"},
		{"198.18.0.1", "benchmarking"},
		// Multicast
		{"224.0.0.1", "multicast 224/4"},
		{"239.255.255.255", "multicast boundary"},
		// Reserved / broadcast
		{"240.0.0.1", "reserved 240/4"},
		{"255.255.255.255", "limited broadcast"},
		// IPv6 loopback / link-local / unique-local
		{"::1", "IPv6 loopback"},
		{"::", "IPv6 unspecified"},
		{"fe80::1", "IPv6 link-local"},
		{"fc00::1", "IPv6 unique-local fc00::/8"},
		{"fd00::1", "IPv6 unique-local fd00::/8"},
		{"ff02::1", "IPv6 multicast"},
		// IPv4-mapped IPv6 — the SSRF dodge. ::ffff:10.0.0.1 must
		// be caught by the IPv4 10/8 range, not slip through as "IPv6".
		{"::ffff:10.0.0.1", "IPv4-mapped IPv6 of RFC 1918"},
		{"::ffff:127.0.0.1", "IPv4-mapped IPv6 of loopback"},
		{"::ffff:169.254.169.254", "IPv4-mapped IPv6 of cloud metadata"},
		// Documentation
		{"2001:db8::1", "IPv6 documentation prefix"},
	}
	for _, c := range cases {
		t.Run(c.ip, func(t *testing.T) {
			ip := net.ParseIP(c.ip)
			if ip == nil {
				t.Fatalf("parse: %q", c.ip)
			}
			if err := ValidateIP(ip); err == nil {
				t.Fatalf("expected %q (%s) to be blocked, got nil", c.ip, c.why)
			}
		})
	}
}

// TestValidateIP_PublicRoutable covers the positive side: IPs that
// MUST pass. If any of these is blocked, legitimate research is
// broken. Stable public IPs (8.8.8.8, 1.1.1.1) are chosen because
// they will not be renumbered.
func TestValidateIP_PublicRoutable(t *testing.T) {
	for _, raw := range []string{
		"8.8.8.8",        // Google DNS
		"1.1.1.1",        // Cloudflare DNS
		"9.9.9.9",        // Quad9 DNS
		"140.82.112.3",   // GitHub
		"2606:4700:4700::1111", // Cloudflare DNS v6
		"2001:4860:4860::8888", // Google DNS v6
	} {
		t.Run(raw, func(t *testing.T) {
			ip := net.ParseIP(raw)
			if ip == nil {
				t.Fatalf("parse: %q", raw)
			}
			if err := ValidateIP(ip); err != nil {
				t.Fatalf("public IP %q rejected: %v", raw, err)
			}
		})
	}
}

// TestValidateIP_Nil covers the boundary case.
func TestValidateIP_Nil(t *testing.T) {
	if err := ValidateIP(nil); err == nil {
		t.Fatal("expected nil IP to be blocked")
	}
}

func TestValidateDomain_Valid(t *testing.T) {
	for _, d := range []string{
		"google.com",
		"osv.dev",
		"api.github.com",
		"a-b.c-d.example",
		"single",
	} {
		t.Run(d, func(t *testing.T) {
			if err := ValidateDomain(d); err != nil {
				t.Fatalf("%q rejected: %v", d, err)
			}
		})
	}
}

func TestValidateDomain_Invalid(t *testing.T) {
	for _, d := range []struct {
		in  string
		why string
	}{
		{"", "empty"},
		{".leading", "leading dot"},
		{"trailing.", "trailing dot"},
		{"with\x00nul", "NUL byte"},
		{"127.0.0.1", "IPv4 literal"},
		{"::1", "IPv6 literal"},
		{"[::1]", "bracketed IPv6 literal"},
		{strings.Repeat("a", 64) + ".com", "label too long"},
		{strings.Repeat("a", 254), "total too long"},
		{"empty..label", "empty label"},
	} {
		t.Run(d.why, func(t *testing.T) {
			if err := ValidateDomain(d.in); err == nil {
				t.Fatalf("expected %q (%s) to be rejected", d.in, d.why)
			}
		})
	}
}

func TestValidateURL_Valid(t *testing.T) {
	for _, u := range []string{
		"https://api.osv.dev/v1/vulns/GHSA-jfh8",
		"https://crt.sh/?q=google.com&output=json",
		"https://nominatim.openstreetmap.org/search?q=Berlin",
		"https://api.crossref.org/works?query=sqlite",
	} {
		t.Run(u, func(t *testing.T) {
			if err := ValidateURL(u); err != nil {
				t.Fatalf("%q rejected: %v", u, err)
			}
		})
	}
}

func TestValidateURL_Invalid(t *testing.T) {
	for _, u := range []struct {
		in  string
		why string
	}{
		{"", "empty"},
		{"https://", "empty host"},
		{"http://api.osv.dev/v1", "http not https"},
		{"ftp://api.osv.dev/v1", "ftp not https"},
		{"https://127.0.0.1/x", "literal IPv4 in host"},
		{"https://[::1]/x", "literal IPv6 in host"},
		{"https://10.0.0.5/x", "literal private IPv4"},
		{"https://169.254.169.254/latest/meta-data", "literal cloud metadata"},
		{"https://192.168.1.1/admin", "literal RFC 1918"},
		{"not-a-url", "no scheme"},
	} {
		t.Run(u.why, func(t *testing.T) {
			if err := ValidateURL(u.in); err == nil {
				t.Fatalf("expected %q (%s) to be rejected", u.in, u.why)
			}
		})
	}
}

func TestFilterPrivateIPs(t *testing.T) {
	in := []net.IP{
		net.ParseIP("8.8.8.8"),
		net.ParseIP("10.0.0.1"),
		net.ParseIP("1.1.1.1"),
		net.ParseIP("169.254.169.254"),
		net.ParseIP("9.9.9.9"),
	}
	kept, ok := FilterPrivateIPs(in)
	if !ok {
		t.Fatal("expected ok=true with at least one public IP")
	}
	if len(kept) != 3 {
		t.Fatalf("expected 3 kept (8.8.8.8, 1.1.1.1, 9.9.9.9), got %d", len(kept))
	}
}

func TestFilterPrivateIPs_AllBlocked(t *testing.T) {
	in := []net.IP{
		net.ParseIP("10.0.0.1"),
		net.ParseIP("127.0.0.1"),
	}
	kept, ok := FilterPrivateIPs(in)
	if ok {
		t.Fatal("expected ok=false when all IPs are private")
	}
	if len(kept) != 0 {
		t.Fatalf("expected empty kept slice, got %d", len(kept))
	}
}

func TestFilterPrivateIPs_Empty(t *testing.T) {
	kept, ok := FilterPrivateIPs(nil)
	if ok {
		t.Fatal("expected ok=false on empty input")
	}
	if len(kept) != 0 {
		t.Fatalf("expected empty slice, got %d", len(kept))
	}
}
