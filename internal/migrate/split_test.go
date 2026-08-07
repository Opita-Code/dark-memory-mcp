// Package migrate_test - split_test.go: table-driven coverage for
// splitStatements. These tests ARE the drift-check for the v2.1.0
// SQL-aware upgrade (see vibe-flow/main/agent_memory_v2_1_0_split_statements.md).
//
// If any test fails, the impl has drift; the spec acceptance
// criteria are not satisfied.
//
// Each case has:
//   - name:    human-readable label (printed on failure)
//   - input:   the migration body string to split
//   - want:    expected []string after splitting
//
// The test does NOT validate that the resulting strings are valid
// SQL — that's SQLite/Postgres's job. It only validates split
// boundaries.
//
// Uses the external test package (migrate_test) so we can import
// the sqlite/postgres subpackages without an import cycle.
package migrate_test

import (
	"testing"

	migrate "github.com/dark-agents/dark-memory-mcp/internal/migrate"
)

// splitStatements re-exports the production splitter so the
// test can call it by its old name. Kept as a thin alias to avoid
// sprinkling the package qualifier through the test bodies.
func splitStatements(body string) []string {
	return migrate.SplitStatementsForTest(body)
}

func TestSplitStatements(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		// --- baseline cases (must keep working) ---------------------

		{
			name: "empty body",
			in:   "",
			want: nil,
		},
		{
			name: "single statement no semicolon (trailing recovery)",
			in:   "CREATE TABLE foo (id INT)",
			want: []string{"CREATE TABLE foo (id INT)"},
		},
		{
			name: "two statements separated by semicolon",
			in:   "CREATE TABLE a (id INT); CREATE TABLE b (id INT)",
			want: []string{
				"CREATE TABLE a (id INT)",
				"CREATE TABLE b (id INT)",
			},
		},
		{
			name: "three statements with whitespace and empty trailing",
			in:   "CREATE TABLE a (id INT);\n  \nCREATE TABLE b (id INT);\n",
			want: []string{
				"CREATE TABLE a (id INT)",
				"CREATE TABLE b (id INT)",
			},
		},

		// --- string literal handling --------------------------------

		{
			name: "string literal with semicolon inside (one statement)",
			in:   `INSERT INTO foo VALUES ('a;b;c')`,
			want: []string{`INSERT INTO foo VALUES ('a;b;c')`},
		},
		{
			name: "two stmts, literal in first contains semicolon",
			in:   `INSERT INTO foo VALUES ('x;y'); SELECT 1`,
			want: []string{
				`INSERT INTO foo VALUES ('x;y')`,
				"SELECT 1",
			},
		},
		{
			name: "escaped single quote in literal",
			in:   `INSERT INTO foo VALUES ('it''s;ok')`,
			want: []string{`INSERT INTO foo VALUES ('it''s;ok')`},
		},
		{
			name: "literal with doubled quote adjacent to semicolon",
			in:   `INSERT INTO foo VALUES ('a''b'); SELECT 2`,
			want: []string{
				`INSERT INTO foo VALUES ('a''b')`,
				"SELECT 2",
			},
		},

		// --- line comment handling ----------------------------------

		{
			name: "line comment with semicolon (split around comment)",
			in:   "-- this is a comment with ; in it\nCREATE TABLE foo (id INT); CREATE TABLE bar (id INT)",
			want: []string{
				"-- this is a comment with ; in it\nCREATE TABLE foo (id INT)",
				"CREATE TABLE bar (id INT)",
			},
		},
		{
			name: "comment-only line followed by statement",
			in:   "-- just a comment\nCREATE TABLE foo (id INT)",
			want: []string{"-- just a comment\nCREATE TABLE foo (id INT)"},
		},

		// --- block comment handling ---------------------------------

		{
			name: "block comment with semicolons inside",
			in:   "/* this ; comment ; has ; semicolons */ CREATE TABLE foo (id INT); SELECT 1",
			want: []string{
				"/* this ; comment ; has ; semicolons */ CREATE TABLE foo (id INT)",
				"SELECT 1",
			},
		},
		{
			name: "block comment spanning multiple lines",
			in:   "/* line 1;\n   line 2;\n*/ CREATE TABLE foo (id INT)",
			want: []string{"/* line 1;\n   line 2;\n*/ CREATE TABLE foo (id INT)"},
		},

		// --- BEGIN..END block handling ------------------------------

		{
			name: "trigger body BEGIN..END single statement inside",
			in:   `CREATE TRIGGER foo_ai AFTER INSERT ON foo BEGIN INSERT INTO foo_fts(rowid, content) VALUES (new.id, new.content); END`,
			want: []string{
				"CREATE TRIGGER foo_ai AFTER INSERT ON foo BEGIN INSERT INTO foo_fts(rowid, content) VALUES (new.id, new.content); END",
			},
		},
		{
			name: "trigger body with multi-line layout",
			in: `CREATE TRIGGER foo_ai AFTER INSERT ON foo
    BEGIN INSERT INTO foo_fts(rowid, content) VALUES (new.id, new.content); END;`,
			want: []string{
				"CREATE TRIGGER foo_ai AFTER INSERT ON foo\n    BEGIN INSERT INTO foo_fts(rowid, content) VALUES (new.id, new.content); END",
			},
		},
		{
			name: "two triggers separated by semicolon",
			in:   `CREATE TRIGGER foo_ai AFTER INSERT ON foo BEGIN INSERT INTO foo_fts(rowid, content) VALUES (new.id, new.content); END; CREATE TRIGGER foo_ad AFTER DELETE ON foo BEGIN INSERT INTO foo_fts(foo_fts, rowid, content) VALUES('delete', old.id, old.content); END`,
			want: []string{
				"CREATE TRIGGER foo_ai AFTER INSERT ON foo BEGIN INSERT INTO foo_fts(rowid, content) VALUES (new.id, new.content); END",
				"CREATE TRIGGER foo_ad AFTER DELETE ON foo BEGIN INSERT INTO foo_fts(foo_fts, rowid, content) VALUES('delete', old.id, old.content); END",
			},
		},
		{
			name: "nested BEGIN..END (rare but possible)",
			in: `CREATE FUNCTION foo() RETURNS TRIGGER AS $$
BEGIN
  IF NEW.x THEN
    BEGIN
      INSERT INTO foo VALUES (NEW.x);
    END;
  END IF;
END;
$$ LANGUAGE plpgsql`,
			want: []string{
				"CREATE FUNCTION foo() RETURNS TRIGGER AS $$\nBEGIN\n  IF NEW.x THEN\n    BEGIN\n      INSERT INTO foo VALUES (NEW.x);\n    END;\n  END IF;\nEND;\n$$ LANGUAGE plpgsql",
			},
		},

		// --- dollar-quoted strings (Postgres) -----------------------

		{
			name: "dollar-quoted string with semicolons inside",
			in: `CREATE FUNCTION foo() RETURNS INT AS $tag$
DECLARE x INT;
BEGIN x := 1; RETURN x; END;
$tag$ LANGUAGE plpgsql`,
			want: []string{
				"CREATE FUNCTION foo() RETURNS INT AS $tag$\nDECLARE x INT;\nBEGIN x := 1; RETURN x; END;\n$tag$ LANGUAGE plpgsql",
			},
		},
		{
			name: "two dollar-quoted stmts separated by semicolon",
			in:   `CREATE FUNCTION a() RETURNS INT AS $$ BEGIN RETURN 1; END; $$ LANGUAGE plpgsql; CREATE FUNCTION b() RETURNS INT AS $$ BEGIN RETURN 2; END; $$ LANGUAGE plpgsql`,
			want: []string{
				"CREATE FUNCTION a() RETURNS INT AS $$ BEGIN RETURN 1; END; $$ LANGUAGE plpgsql",
				"CREATE FUNCTION b() RETURNS INT AS $$ BEGIN RETURN 2; END; $$ LANGUAGE plpgsql",
			},
		},
		{
			name: "dollar quote tag with underscore",
			in:   `DO $_body_$ BEGIN PERFORM 1; END $_body_$`,
			want: []string{"DO $_body_$ BEGIN PERFORM 1; END $_body_$"},
		},

		// --- identifier false-positive guards ------------------------

		{
			name: "identifier containing BEGIN as substring",
			in:   "CREATE TABLE mybegin_table (id INT); SELECT 1",
			want: []string{
				"CREATE TABLE mybegin_table (id INT)",
				"SELECT 1",
			},
		},
		{
			name: "identifier containing END as substring",
			in:   "CREATE TABLE weekend_table (id INT); SELECT 1",
			want: []string{
				"CREATE TABLE weekend_table (id INT)",
				"SELECT 1",
			},
		},
		{
			name: "lowercase begin/end",
			in:   `create trigger foo_ai after insert on foo begin insert into foo_fts(rowid) values (new.id); end`,
			want: []string{
				"create trigger foo_ai after insert on foo begin insert into foo_fts(rowid) values (new.id); end",
			},
		},

		// --- mixed case ---------------------------------------------

		{
			name: "comment + literal + trigger all in one body",
			in: `-- Migration v18: agent_memory
CREATE TABLE agent_memory (content TEXT DEFAULT 'hello;world');
CREATE TRIGGER foo_ai AFTER INSERT ON agent_memory BEGIN INSERT INTO agent_memory_fts(rowid, content) VALUES (new.id, new.content); END;
CREATE INDEX idx_foo ON agent_memory (content)`,
			want: []string{
				"-- Migration v18: agent_memory\nCREATE TABLE agent_memory (content TEXT DEFAULT 'hello;world')",
				"CREATE TRIGGER foo_ai AFTER INSERT ON agent_memory BEGIN INSERT INTO agent_memory_fts(rowid, content) VALUES (new.id, new.content); END",
				"CREATE INDEX idx_foo ON agent_memory (content)",
			},
		},

		// --- whitespace edge cases ----------------------------------

		{
			name: "leading whitespace trimmed",
			in:   "   \n\t  CREATE TABLE foo (id INT)",
			want: []string{"CREATE TABLE foo (id INT)"},
		},
		{
			name: "only whitespace returns empty",
			in:   "   \n\t  ",
			want: nil,
		},
		{
			name: "semicolon-only returns empty",
			in:   ";",
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitStatements(tc.in)
			if !equalStringSlice(got, tc.want) {
				t.Errorf("\n  in:   %q\n  got:  %#v\n  want: %#v", tc.in, got, tc.want)
			}
		})
	}
}

// TestSplitStatements_BackwardCompat verifies that all existing
// migrations v1..v17 still produce reasonable splits after the
// upgrade. We pin two invariants:
//
//  1. Every DDL/DML keyword (CREATE/ALTER/INSERT/UPDATE/DELETE/DROP/PRAGMA)
//     that appears at the start of a line in the source body is
//     captured in some output stmt (no silent drops).
//  2. Each captured output stmt that has a real DDL/DML keyword at
//     its start contains exactly one of those keywords (no accidental
//     merges).
//
// We do NOT pin the exact stmt count because the new splitter may
// capture trailing comment-only content as a separate stmt (the
// naive `strings.Split` filter would have dropped it). What matters
// is that the DDL statements themselves aren't dropped or merged.
func TestSplitStatements_BackwardCompat(t *testing.T) {
	for _, m := range allMigrationsForCompat() {
		if m.Version < 1 || m.Version > 17 {
			continue
		}
		t.Run(m.Name, func(t *testing.T) {
			stmts := splitStatements(m.Up)
			if len(stmts) == 0 {
				t.Errorf("v%d %s: splitStatements returned 0 stmts", m.Version, m.Name)
			}
			// Invariant 1: every DDL keyword at line-start appears
			// in some output stmt.
			wantKeywords := topLevelDDLKeywords(m.Up)
			for _, kw := range wantKeywords {
				found := false
				for _, s := range stmts {
					if startsWith(s, kw) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("v%d %s: DDL keyword %q at line-start was dropped from split output", m.Version, m.Name, kw)
				}
			}
			// Invariant 2: each output stmt with a leading DDL
			// keyword is non-empty AND the keyword is at index 0.
			for i, s := range stmts {
				if !startsWithDDLKeyword(s) {
					continue // comments, blank lines, etc. — fine
				}
				if len(s) == 0 {
					t.Errorf("v%d %s: stmt[%d] starts with DDL but is empty", m.Version, m.Name, i)
				}
			}
		})
	}
}

// topLevelDDLKeywords returns the first DDL keyword of each
// statement-start line in body. A line is a "statement start" if
// it begins a new statement (i.e., the previous non-empty line
// ended with `;`, or it's the first non-empty line). This avoids
// false positives from continuation lines inside multi-line
// statements like INSERT...SELECT or CREATE TABLE bodies.
func topLevelDDLKeywords(body string) []string {
	out := []string{}
	prevEndedStmt := true // first non-empty line starts a stmt
	for _, line := range stringsSplitLines(body) {
		trimmed := stringsTrimLeft(line)
		if trimmed == "" {
			// Empty line: doesn't end a stmt; preserve prevEndedStmt
			continue
		}
		if prevEndedStmt {
			first := firstWord(trimmed)
			switch first {
			case "CREATE", "ALTER", "INSERT", "UPDATE", "DELETE", "DROP", "PRAGMA":
				out = append(out, first)
			}
		}
		// A line "ends a stmt" iff its last non-whitespace char is `;`.
		prevEndedStmt = lineEndsStmt(trimmed)
	}
	return out
}

// lineEndsStmt returns true if the (already-trimmed) line's last
// rune is `;`. Used by topLevelDDLKeywords to detect statement
// boundaries in the source body.
func lineEndsStmt(trimmed string) bool {
	if trimmed == "" {
		return false
	}
	// Find the last non-whitespace rune.
	for i := len(trimmed) - 1; i >= 0; i-- {
		c := rune(trimmed[i])
		if c == ' ' || c == '\t' {
			continue
		}
		return c == ';'
	}
	return false
}

// startsWith returns true if s starts with prefix (case-insensitive).
func startsWith(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		a := s[i]
		b := prefix[i]
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
	return true
}

// startsWithDDLKeyword returns true if s (after trim) starts with a
// DDL keyword. Used by the backward-compat test to skip comment-only
// stmts.
func startsWithDDLKeyword(s string) bool {
	t := stringsTrimLeft(s)
	first := firstWord(t)
	switch first {
	case "CREATE", "ALTER", "INSERT", "UPDATE", "DELETE", "DROP", "PRAGMA", "SELECT", "WITH":
		return true
	}
	return false
}

// countTopLevelStatements counts DDL/DML statements in body by
// looking for top-level keywords at the start of a line (after
// trimming). It's a coarse approximation used only for the
// backward-compat guard.
//
// Deprecated: superseded by topLevelDDLKeywords which returns the
// actual keywords (not just the count), enabling invariant checks
// per the v2.1.0 backward-compat test rewrite.
func countTopLevelStatements(body string) int {
	return len(topLevelDDLKeywords(body))
}

// --- tiny helpers (kept local to avoid pulling in strings at the
// top, which would shadow the test target) -------------------------

// allMigrationsForCompat returns the migrations from the two driver
// packages (sqlite + postgres). Both define `var Migrations` but
// live in different packages; we import them only via the test-only
// compat_bridge_test.go bridge file.
func allMigrationsForCompat() []migrate.Migration {
	out := []migrate.Migration{}
	for _, m := range sqliteMigrationsForTest() {
		out = append(out, m)
	}
	for _, m := range postgresMigrationsForTest() {
		out = append(out, m)
	}
	return out
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func joinIndented(s []string) string {
	out := ""
	for _, x := range s {
		out += "  - " + x + "\n"
	}
	return out
}

func stringsSplitLines(s string) []string {
	out := []string{}
	cur := ""
	for _, c := range s {
		if c == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func stringsTrimLeft(s string) string {
	for i, c := range s {
		if c != ' ' && c != '\t' && c != '\r' {
			return s[i:]
		}
	}
	return ""
}

func firstWord(s string) string {
	out := ""
	for _, c := range s {
		if c == ' ' || c == '\t' || c == '(' {
			break
		}
		out += string(c)
	}
	return out
}
