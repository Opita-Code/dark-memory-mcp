// Package redact — central secret detection and masking.
//
// WHY THIS EXISTS. INV-13 (redact-before-log) is recorded in
// docs/v4-status.md §3 as NOT STARTED, and measurement confirmed it: as
// of 2026-10-09 the repository contained no secret detector at all, only
// six unrelated ad-hoc redactors — redactKey (HMAC key pairs),
// redactJSONError (backticked JSON error text), NLIConfig.Redacted,
// redactDSN, redactHomeInPath, and the research package's
// RedactString/RedactResult. Each knows about one type. None is a policy.
//
// The motivation is concrete rather than theoretical. During the work
// that produced this package, an npm access token was pasted into an
// operator conversation. The discipline that says "rotate it and never
// store it" existed in a skill file, which is advice, not a control.
//
// WHAT THIS IS NOT. Defence in depth that reduces blast radius. It is
// not a security boundary, and the package says so where it is wired in.
// A redactor at a log sink protects the log; it cannot protect a memory
// that legitimately contains a rotation command, and it cannot protect a
// third-party provider that receives that memory.
//
// THE DESIGN CONSTRAINT THAT SHAPED EVERY PATTERN HERE. A redactor with
// false positives is worse than no redactor. Operators route around tools
// that mangle their text, and once that habit forms the layer protects
// nothing while appearing to. So every pattern is anchored on a
// distinctive prefix rather than on shape, and every match requires a
// length floor. A bare 40-character hex string is deliberately NOT a
// pattern: it is indistinguishable from a git SHA, and this repository is
// full of them. The false-positive rate was measured against the live
// corpus — 28,775 real write_audit rows and real agent_memory content —
// before this package was wired into anything. See
// TestRedact_NoFalsePositivesOnLiveCorpus in the audit package.
package redact

import (
	"regexp"
	"strings"
)

// Mask is what a detected secret is replaced with. The fixed width keeps
// the surrounding text (timestamps, ids, positions) readable and
// obviously-not-a-value, which is the point: a reader must be able to see
// that redaction happened without being tempted to look for the real
// value nearby.
const Mask = "[REDACTED]"

// rule is one detection pattern. Every rule is prefix-anchored; see the
// package comment for why shape-based rules were rejected.
type rule struct {
	name    string
	re      *regexp.Regexp
	// replace is the capture group substituted for the match. 0 means
	// the whole match.
	replace int
}

var rules = []rule{
	// npm access tokens (the family that motivated this package).
	{name: "npm", re: regexp.MustCompile(`npm_[A-Za-z0-9]{36}`)},
	// GitHub personal access / OAuth / user / server / refresh tokens.
	{name: "github_pat", re: regexp.MustCompile(`github_pat_[A-Za-z0-9_]{50,}`)},
	{name: "github", re: regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36}`)},
	// Anthropic.
	{name: "anthropic", re: regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{20,}`)},
	// OpenAI and the many providers that reuse the sk- convention
	// (DeepSeek, MiniMax, OpenRouter, Together, and others all issue
	// sk- prefixed keys).
	{name: "sk", re: regexp.MustCompile(`sk-[A-Za-z0-9_\-]{20,}`)},
	// AWS access key id. The companion secret is caught by the generic
	// high-entropy rule below; the id alone is worth naming because it
	// is unambiguous.
	{name: "aws", re: regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	// Slack tokens.
	{name: "slack", re: regexp.MustCompile(`xox[abprs]-[A-Za-z0-9-]{10,}`)},
	// Google API keys.
	{name: "google", re: regexp.MustCompile(`AIza[0-9A-Za-z_\-]{35}`)},
	// Stripe.
	{name: "stripe", re: regexp.MustCompile(`[sr]k_(live|test)_[A-Za-z0-9]{16,}`)},
	// JSON Web Tokens. Three base64url segments, each with a length
	// floor — enough to reject ordinary dotted identifiers.
	{name: "jwt", re: regexp.MustCompile(`eyJ[A-Za-z0-9_\-]{10,}\.eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}`)},
	// PEM private key blocks. Masked whole: the block is the secret.
	{name: "pem", re: regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)},
	// Authorization headers.
	{name: "bearer", re: regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=\-]{20,}`), replace: -1},
	// Credentials embedded in a URL: scheme://user:pass@host
	{name: "dsn", re: regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://[^\s:/@]+:)[^\s:/@]+(@)`), replace: -1},
	// Assignment form: KEY=value / "api_key": "value" where the value
	// looks like a credential. Requires a name that names a secret, so
	// ordinary config (port=5432, timeout=30s) is untouched.
	{name: "assignment", re: regexp.MustCompile(`(?i)((?:api[_\-]?key|secret|token|password|passwd|pwd|auth[_\-]?token|private[_\-]?key|access[_\-]?key)\s*[:=]\s*["']?)([A-Za-z0-9_\-./+=]{16,})`), replace: -1},
}

// FindAll returns the byte ranges of every detected secret in s, together
// with the rule that matched. Overlapping matches are resolved in favour
// of the earliest start, then the longest match, so a token that trips
// two rules is masked once rather than partially.
type Match struct {
	Rule  string
	Start int
	End   int
}

// FindAll locates secrets without modifying the input. Callers that only
// need a yes/no answer (a "does this input contain a credential?"
// guard) should use Contains, which is cheaper.
func FindAll(s string) []Match {
	var out []Match
	taken := make([]bool, len(s))

	for _, r := range rules {
		locs := r.re.FindAllStringSubmatchIndex(s, -1)
		for _, loc := range locs {
			start, end := r.pick(loc)
			if start < 0 || end <= start || end > len(s) {
				continue
			}
			// Respect an already-claimed span.
			clash := false
			for i := start; i < end; i++ {
				if taken[i] {
					clash = true
					break
				}
			}
			if clash {
				continue
			}
			for i := start; i < end; i++ {
				taken[i] = true
			}
			out = append(out, Match{Rule: r.name, Start: start, End: end})
		}
	}

	// Earliest first, so Redact's output is deterministic.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Start < out[j-1].Start; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// pick resolves the replacement span for a rule, honouring the
// capture-group form (replace == -1 means "keep group 1, mask group 2").
func (r rule) pick(loc []int) (int, int) {
	if r.replace == 0 {
		return loc[0], loc[1]
	}
	// Pair index 2 is group 1, 4 is group 2.
	if r.replace == -1 {
		if len(loc) >= 4 {
			return loc[2], loc[3]
		}
		return loc[0], loc[1]
	}
	return loc[0], loc[1]
}

// Redact returns s with every detected secret replaced by Mask. Text
// with no detected secret is returned byte-identical — asserted by test,
// because a redactor that rewrites clean text is indistinguishable from
// a bug to whoever is reading the log.
func Redact(s string) string {
	matches := FindAll(s)
	if len(matches) == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	prev := 0
	for _, m := range matches {
		if m.Start < prev {
			continue
		}
		b.WriteString(s[prev:m.Start])
		b.WriteString(Mask)
		prev = m.End
	}
	b.WriteString(s[prev:])
	return b.String()
}

// Contains reports whether s holds anything matching a rule. Use it for
// admission decisions (reject a memory that carries a credential) and
// Redact for sinks (a stderr write, an audit note).
func Contains(s string) bool {
	for _, r := range rules {
		if r.re.MatchString(s) {
			return true
		}
	}
	return false
}

// Rules returns the names of the active rules, for documentation and for
// the coverage test that keeps this list and the documentation in step.
func Rules() []string {
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		out = append(out, r.name)
	}
	return out
}
