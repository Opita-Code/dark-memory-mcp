// Package migrate - split_statements.go: SQL-aware statement splitter.
//
// This is the v2.1.0 upgrade of the naive `strings.Split(body, ";")`
// splitter. It tracks:
//   1. Line comments (`--` to end of line)
//   2. Block comments (`/* ... */`)
//   3. Single-quoted string literals with `''` escape
//   4. BEGIN..END blocks (full nesting)
//   5. Dollar-quoted strings (`$tag$...$tag$`) — Postgres, but cheap
//      to support
//
// The `;` is a split point ONLY when:
//   - we're not inside a string literal
//   - we're not inside any comment
//   - we're not inside a BEGIN..END block (i.e. depth == 0)
//   - we're not inside a dollar-quoted string
//
// Out of scope:
//   - Full SQL lexer (operator tokenization, identifier rules)
//   - Backslash continuations
//   - Nested BEGIN..END inside dollar quotes (the dollar-quote state
//     takes precedence)
//
// The state machine is intentionally a single-pass rune loop. The
// state diagram is:
//
//                    --     /*     '       BEGIN    $tag$
//   normal       ---> lineCmt blkCmt string begin    dollar
//     |  ^         |       |       |       |       |
//     |  |         v       v       v       v       v
//     |  +----- (back to normal on terminator)
//
// Transitions back to normal:
//   lineCmt  : on '\n'
//   blkCmt   : on '*/'
//   string   : on unescaped "'"
//   begin    : on matching END (decrements depth)
//   dollar   : on matching $tag$ close
//
// See split_test.go for the table-driven coverage. The drift-check
// for this code is the test suite.
package migrate

import "strings"

// sqlState enumerates the splitter's discrete states. We avoid
// anonymous magic numbers for readability and so test failures point
// at the right concept.
type sqlState int

const (
	stateNormal sqlState = iota
	stateLineCmt
	stateBlockCmt
	stateString
	stateBeginBlock // we're inside a BEGIN..END block; depth is tracked separately
	stateDollar
)

// splitSQL is the public entry point. It is splitStatements's body
// in migrate.go; see migrate.go for the wrapper.
func splitSQL(body string) []string {
	out := make([]string, 0, 16)
	var stmt strings.Builder
	state := stateNormal
	beginDepth := 0
	dollarTag := "" // current dollar-quote tag, including the $ markers (e.g. "$tag$")

	runes := []rune(body)
	for i := 0; i < len(runes); i++ {
		c := runes[i]

		switch state {
		case stateLineCmt:
			stmt.WriteRune(c)
			if c == '\n' {
				state = stateNormal
			}
			continue

		case stateBlockCmt:
			stmt.WriteRune(c)
			if c == '*' && i+1 < len(runes) && runes[i+1] == '/' {
				stmt.WriteRune('/')
				i++
				state = stateNormal
			}
			continue

		case stateString:
			stmt.WriteRune(c)
			if c == '\'' {
				// `''` inside a string is an escape, not close.
				if i+1 < len(runes) && runes[i+1] == '\'' {
					stmt.WriteRune('\'')
					i++
					continue
				}
				state = stateNormal
			}
			continue

		case stateDollar:
			stmt.WriteRune(c)
			// Look for the closing $tag$ sequence.
			if c == '$' && dollarTag != "" && endsWithDollarTag(stmt.String(), dollarTag) {
				state = stateNormal
				dollarTag = ""
			}
			continue

		case stateBeginBlock:
			// Inside BEGIN..END — split points are suppressed. The
			// only state transition is on END (decrements depth) or
			// on encountering a nested BEGIN (increments depth).
			stmt.WriteRune(c)
			// Detect BEGIN/END keywords. We need to be careful with
			// identifiers that contain BEGIN/END as substrings.
			if isStandaloneKeyword(stmt.String(), "BEGIN") {
				beginDepth++
				continue
			}
			if isStandaloneKeyword(stmt.String(), "END") {
				beginDepth--
				if beginDepth == 0 {
					state = stateNormal
				}
				continue
			}
			continue

		case stateNormal:
			// -- line comment
			if c == '-' && i+1 < len(runes) && runes[i+1] == '-' {
				stmt.WriteRune(c)
				state = stateLineCmt
				continue
			}
			// /* block comment */
			if c == '/' && i+1 < len(runes) && runes[i+1] == '*' {
				stmt.WriteRune(c)
				stmt.WriteRune('*')
				i++
				state = stateBlockCmt
				continue
			}
			// ' string literal
			if c == '\'' {
				stmt.WriteRune(c)
				state = stateString
				continue
			}
			// $tag$ dollar-quoted string
			if c == '$' {
				tag, length := scanDollarTag(runes, i)
				if tag != "" {
					stmt.WriteString(tag)
					i += length - 1 // -1 because the for-loop will i++ next
					dollarTag = tag
					state = stateDollar
					continue
				}
			}
			// BEGIN keyword → enter begin block
			if isStandaloneKeywordChar(c) {
				stmt.WriteRune(c)
				if isStandaloneKeyword(stmt.String(), "BEGIN") {
					beginDepth++
					state = stateBeginBlock
				}
				continue
			}
			// ; split point (only in stateNormal)
			if c == ';' {
				if s := strings.TrimSpace(stmt.String()); s != "" {
					out = append(out, s)
				}
				stmt.Reset()
				continue
			}
			stmt.WriteRune(c)
			continue
		}
	}
	// Trailing statement without a terminating `;`. Defensive: the
	// migrations always end with `;` so this is a safety net for
	// malformed input. Captured but not validated.
	if rest := strings.TrimSpace(stmt.String()); rest != "" {
		out = append(out, rest)
	}
	return out
}

// isStandaloneKeywordChar returns true for runes that can appear in
// SQL keywords / identifiers (letters, digits, underscore). Used
// to detect the END of a keyword as we stream runes into stmt.
func isStandaloneKeywordChar(c rune) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '_'
}

// isStandaloneKeyword returns true if buf ends with `kw` and the
// rune before is whitespace/start-of-buffer AND the rune after (if
// any) is not a keyword char. This is the BEGIN/END detector —
// crucial to avoid matching identifiers like mybegin_table.
//
// Buffers are small (migration bodies are at most a few KB; the
// per-statement buffer is bounded by the next `;`). Linear scan
// from the end is fine.
func isStandaloneKeyword(buf, kw string) bool {
	if len(buf) < len(kw) {
		return false
	}
	end := len(buf)
	start := end - len(kw)
	// Check the rune before the candidate (must be whitespace or
	// start-of-buffer).
	if start > 0 {
		prev := rune(buf[start-1])
		if !isWhitespaceRune(prev) && prev != '(' {
			return false
		}
	}
	// Case-insensitive match — compare both sides lowercased so
	// uppercase BEGIN and lowercase begin both match.
	for i := 0; i < len(kw); i++ {
		a := rune(buf[start+i])
		b := rune(kw[i])
		if a >= 'A' && a <= 'Z' {
			a += 'a' - 'A'
		}
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		if a != b {
			return false
		}
	}
	// Check the rune after the candidate (must NOT be a keyword
	// char, or end-of-buffer).
	if end < len(buf) {
		next := rune(buf[end])
		if isStandaloneKeywordChar(next) {
			return false
		}
	}
	return true
}

func isWhitespaceRune(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f'
}

// scanDollarTag scans a dollar-quote tag at position i. Returns the
// full tag (e.g. "$tag$" or "$$") and the rune-length consumed, or
// ("", 0) if no tag starts at i.
//
// Rules (Postgres-compatible):
//   - $$ is the empty tag (2 chars)
//   - $name$ is a named tag where name matches [A-Za-z_][A-Za-z0-9_]*
//   - The closing tag must match the opening one exactly.
func scanDollarTag(runes []rune, i int) (string, int) {
	if runes[i] != '$' {
		return "", 0
	}
	// Try $$ (empty tag, 2 chars).
	if i+1 < len(runes) && runes[i+1] == '$' {
		return "$$", 2
	}
	// Try $name$ where name starts with letter or underscore.
	if i+1 >= len(runes) {
		return "", 0
	}
	first := runes[i+1]
	if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') || first == '_') {
		return "", 0
	}
	end := i + 2
	for end < len(runes) {
		c := runes[end]
		if c == '$' {
			// Closing $ at end of tag.
			tag := string(runes[i : end+1])
			return tag, end + 1 - i
		}
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_') {
			// Not a valid name char → not a dollar-quote tag.
			return "", 0
		}
		end++
	}
	return "", 0
}

// endsWithDollarTag returns true if buf ends with the closing tag
// (e.g. "$tag$" or "$$"). Used by the stateDollar case to detect
// the close of a dollar-quoted string.
func endsWithDollarTag(buf, tag string) bool {
	if len(buf) < len(tag) {
		return false
	}
	return buf[len(buf)-len(tag):] == tag
}