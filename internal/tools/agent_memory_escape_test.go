package tools

import (
	"testing"
)

// TestEscapeFTS5_QuoteWrap verifies v2.8.0-alpha.2 quote-wrap behavior
// (commit <pending>). The old allowlist-based escapeFTS5 rejected
// chars like . and - because FTS5 treats them as syntax operators; the
// new implementation wraps each token in FTS5 phrase quotes so any
// character is accepted.
func TestEscapeFTS5_QuoteWrap(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		// The cases that broke under the old allowlist escape.
		{in: "v2.8.0 alpha harness gaps", want: `"v2.8.0" "alpha" "harness" "gaps"`, wantErr: false},
		{in: "dark-agent", want: `"dark-agent"`, wantErr: false},
		{in: "a.b.c", want: `"a.b.c"`, wantErr: false},
		// Length-prefix support.
		{in: "jira*", want: `"jira"*`, wantErr: false},
		{in: "rate limit ddb", want: `"rate" "limit" "ddb"`, wantErr: false},
		// FTS5 reserved words are safe inside quotes (treated as literal
		// tokens within phrases), so quote-wrap must NOT reject them.
		{in: "AND OR", want: `"AND" "OR"`, wantErr: false},
		// Embedded double quotes are escaped by doubling. Token
		// `"hello"` becomes the FTS5 phrase `"""hello"""` which
		// parses as: open quote, escaped quote, hello, escaped
		// quote, close quote — i.e. the literal string `"hello"`.
		{in: `say "hello"`, want: `"say" """hello"""`, wantErr: false},
		// Whitespace-only.
		{in: "   ", wantErr: true},
		// Empty.
		{in: "", wantErr: true},
		// Single token.
		{in: "single", want: `"single"`, wantErr: false},
	}
	for _, c := range cases {
		got, err := escapeFTS5(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("escapeFTS5(%q) err=%v, wantErr=%v", c.in, err, c.wantErr)
			continue
		}
		if !c.wantErr && got != c.want {
			t.Errorf("escapeFTS5(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
