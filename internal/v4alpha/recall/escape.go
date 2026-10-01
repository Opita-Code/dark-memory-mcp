package recall

import "strings"

// escapeLike escapes SQLite LIKE wildcards (% and _) and the escape
// character (\) in s so the value is treated as literal. Caller MUST
// use `ESCAPE '\'` in the LIKE clause.
//
// This duplicates the agent_memory.escapeLike helper because that
// function is unexported. Keeping the duplication small avoids
// cross-package coupling on a tiny utility.
//
// Example:
//   escapeLike("ADR-1")   → "ADR-1"
//   escapeLike("a%b_c")   → "a\\%b\\_c"
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
