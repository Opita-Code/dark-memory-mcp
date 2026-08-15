// recall_query_fix_test.go — TEMPORARY verification (spec 1200):
// proves rewriteFTSQuery turns the failing multi-word query into an
// OR query that returns hits. DELETE after verification.
package sqlite

import (
	"testing"
)

func TestRewriteFTSQuery_OrSemantics(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "single term unchanged",
			in:   "daemon",
			want: "daemon",
		},
		{
			name: "multi-word with stopwords → OR of meaningful",
			in:   "daemon bridge arquitectura por qué existe spec 785",
			// por/que/existe dropped (stopwords), 785 dropped (numeric),
			// remaining tokens OR-joined in order.
			want: "daemon OR bridge OR arquitectura OR spec",
		},
		{
			name: "all stopwords → raw fallback",
			in:   "de la el",
			want: "de la el",
		},
		{
			name: "quoted phrase preserved",
			in:   `"opita market" daemon`,
			want: `"opita market" OR daemon`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rewriteFTSQuery(tc.in)
			if got != tc.want {
				t.Errorf("rewriteFTSQuery(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRecallQueryTerms_DropsStopwords(t *testing.T) {
	terms := recallQueryTerms("daemon bridge arquitectura por qué existe spec 785")
	if len(terms) != 4 {
		t.Fatalf("want 4 terms, got %v", terms)
	}
	want := []string{"daemon", "bridge", "arquitectura", "spec"}
	for i, w := range want {
		if terms[i] != w {
			t.Errorf("terms[%d] = %q, want %q", i, terms[i], w)
		}
	}
}
