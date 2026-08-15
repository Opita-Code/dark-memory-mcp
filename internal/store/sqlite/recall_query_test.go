// recall_query_test.go — regression coverage for the spec 1200 recall
// query rewrite (2026-08-15). Natural-language context queries like
// "daemon bridge arquitectura por qué existe spec 785" used to be
// AND-joined by FTS5 → zero hits for every query longer than ~3 words
// ("nunca sirven para query" — operator report). rewriteFTSQuery now
// OR-joins the meaningful tokens; recallQueryTerms mirrors those rules
// for the research_items LIKE path. This file was promoted from a
// temporary verification file (recall_query_fix_test.go, deleted).
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
			name: "accented stopword dropped",
			in:   "sí daemon",
			want: "daemon",
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
		{
			name: "empty input unchanged",
			in:   "",
			want: "",
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

func TestRecallQueryTerms_NilForNonsense(t *testing.T) {
	if got := recallQueryTerms("de la el"); got != nil {
		t.Errorf("all-stopword query = %v, want nil", got)
	}
	if got := recallQueryTerms(""); got != nil {
		t.Errorf("empty query = %v, want nil", got)
	}
}

func TestSplitQueryTokens_UnbalancedQuoteKeepsContent(t *testing.T) {
	// Unbalanced quote must never drop content: everything after the
	// opening quote stays in one token ("opita daemon), not two.
	got := splitQueryTokens(`"opita daemon`)
	if len(got) != 1 {
		t.Fatalf("want 1 token (all content preserved), got %v", got)
	}
	if got[0] != `"opita daemon` {
		t.Errorf("got %q, want %q", got[0], `"opita daemon`)
	}
}
