package chatwire

import "testing"

// TestExtractUsage_ReadsTheProviderReport: a provider that reports usage
// gives the five numbers, including the two that live in the "details"
// blocks (the cached and the reasoning ones).
func TestExtractUsage_ReadsTheProviderReport(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":1071,"completion_tokens":295,"total_tokens":1366,
		"prompt_tokens_details":{"cached_tokens":128},
		"completion_tokens_details":{"reasoning_tokens":207}}}`)

	u, ok := ExtractUsage(body)
	if !ok {
		t.Fatal("ExtractUsage reported no usage, want the provider report")
	}
	if u.PromptTokens != 1071 || u.CompletionTokens != 295 || u.TotalTokens != 1366 {
		t.Errorf("usage = %+v, want prompt 1071, completion 295, total 1366", u)
	}
	if u.CachedTokens != 128 {
		t.Errorf("CachedTokens = %d, want 128", u.CachedTokens)
	}
	if u.ReasoningTokens != 207 {
		t.Errorf("ReasoningTokens = %d, want 207", u.ReasoningTokens)
	}
}

// TestExtractUsage_ProviderWithoutUsageIsNotZero: a provider that does not
// report usage is a MISSING datum, not a usage of zero. The difference
// decides whether a cost can be published or has to stay declared as
// unmeasured.
func TestExtractUsage_ProviderWithoutUsageIsNotZero(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"{\"verdict\":\"aligned\"}"},"finish_reason":"stop"}]}`)

	u, ok := ExtractUsage(body)
	if ok {
		t.Errorf("ExtractUsage reported usage %+v, want no usage", u)
	}
	if u != (Usage{}) {
		t.Errorf("usage = %+v, want the zero value", u)
	}
}

// TestExtractUsage_MissingDetailsAreZero: not every provider fills the
// "details" blocks. The missing ones are zero, and the usage is still
// reported.
func TestExtractUsage_MissingDetailsAreZero(t *testing.T) {
	body := []byte(`{"usage":{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105}}`)

	u, ok := ExtractUsage(body)
	if !ok {
		t.Fatal("ExtractUsage reported no usage, want the provider report")
	}
	if u.CachedTokens != 0 || u.ReasoningTokens != 0 {
		t.Errorf("usage = %+v, want the details at zero", u)
	}
	if u.TotalTokens != 105 {
		t.Errorf("TotalTokens = %d, want 105", u.TotalTokens)
	}
}

// TestExtractUsage_TotalIsSummedWhenAbsent: some providers omit
// total_tokens. The total is the sum of the other two, and a total of zero
// with tokens on both sides is a missing field, not a free request.
func TestExtractUsage_TotalIsSummedWhenAbsent(t *testing.T) {
	body := []byte(`{"usage":{"prompt_tokens":1000,"completion_tokens":40}}`)

	u, ok := ExtractUsage(body)
	if !ok {
		t.Fatal("ExtractUsage reported no usage, want the provider report")
	}
	if u.TotalTokens != 1040 {
		t.Errorf("TotalTokens = %d, want 1040", u.TotalTokens)
	}
}

// TestExtractUsage_MalformedBodyIsNotUsage: a body that is not JSON at all
// is not a usage report.
func TestExtractUsage_MalformedBodyIsNotUsage(t *testing.T) {
	if _, ok := ExtractUsage([]byte("esto no es json")); ok {
		t.Error("ExtractUsage reported usage for a malformed body")
	}
}

// TestUsage_Add_SumsEveryField: the attempts of one judge call are added
// field by field. The cache and the reasoning have their own sums: a cost
// that mixes them up is wrong.
func TestUsage_Add_SumsEveryField(t *testing.T) {
	a := Usage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110, CachedTokens: 5, ReasoningTokens: 3}
	b := Usage{PromptTokens: 200, CompletionTokens: 20, TotalTokens: 220, CachedTokens: 50, ReasoningTokens: 7}

	s := a.Add(b)
	if s.PromptTokens != 300 || s.CompletionTokens != 30 || s.TotalTokens != 330 {
		t.Errorf("sum = %+v, want prompt 300, completion 30, total 330", s)
	}
	if s.CachedTokens != 55 {
		t.Errorf("CachedTokens = %d, want 55", s.CachedTokens)
	}
	if s.ReasoningTokens != 10 {
		t.Errorf("ReasoningTokens = %d, want 10", s.ReasoningTokens)
	}
}

// TestAccount_ResultIsNilWithoutRequests: no request, no account. The
// caller distinguishes "nothing happened" from "something happened and we
// do not know what it cost".
func TestAccount_ResultIsNilWithoutRequests(t *testing.T) {
	var a Account
	if a.Result() != nil {
		t.Error("an empty account produced an account")
	}
}

// TestAccount_TokensAreTheSumWhenEveryRequestReported: two requests that
// both report usage give the sum of the two.
func TestAccount_TokensAreTheSumWhenEveryRequestReported(t *testing.T) {
	var a Account
	a.Add(&Usage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110})
	a.Add(&Usage{PromptTokens: 200, CompletionTokens: 20, TotalTokens: 220})

	res := a.Result()
	if res == nil {
		t.Fatal("the account produced no result")
	}
	if res.Requests != 2 {
		t.Errorf("Requests = %d, want 2", res.Requests)
	}
	if res.Tokens == nil {
		t.Fatal("the tokens are missing, want the sum of the two requests")
	}
	if res.Tokens.TotalTokens != 330 {
		t.Errorf("TotalTokens = %d, want 330", res.Tokens.TotalTokens)
	}
}

// TestAccount_TokensAreNilWhenARequestDidNotReport: the requests are a
// fact — they happened and they are billed — but the tokens are unknown
// when one of them did not report. A partial sum written as a total is a
// wrong number, and this data exists to say what the judge cost.
func TestAccount_TokensAreNilWhenARequestDidNotReport(t *testing.T) {
	var a Account
	a.Add(&Usage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110})
	a.Add(nil)

	res := a.Result()
	if res == nil {
		t.Fatal("the account produced no result")
	}
	if res.Requests != 2 {
		t.Errorf("Requests = %d, want 2", res.Requests)
	}
	if res.Tokens != nil {
		t.Errorf("Tokens = %+v, want nil: one request did not report usage", res.Tokens)
	}
}

// TestCombine_MissingSideDoesNotBreakTheTotal: the NLI chain did not run,
// so there is nothing to add and nothing to lose. A side that did NOT
// happen was not billed.
func TestCombine_MissingSideDoesNotBreakTheTotal(t *testing.T) {
	juez := &ProviderUsage{Requests: 1, Tokens: &Usage{TotalTokens: 1366}}

	res := Combine(juez, nil)
	if res == nil {
		t.Fatal("Combine produced no result")
	}
	if res.Requests != 1 {
		t.Errorf("Requests = %d, want 1", res.Requests)
	}
	if res.Tokens == nil || res.Tokens.TotalTokens != 1366 {
		t.Errorf("Tokens = %+v, want the judge's tokens", res.Tokens)
	}
}

// TestCombine_TokensAreNilWhenOneSideDidNotReport: the NLI chain ran and
// its provider did not report usage. The requests are known; the total is
// not.
func TestCombine_TokensAreNilWhenOneSideDidNotReport(t *testing.T) {
	juez := &ProviderUsage{Requests: 2, Tokens: &Usage{TotalTokens: 2600}}
	nli := &ProviderUsage{Requests: 1, Tokens: nil}

	res := Combine(juez, nli)
	if res == nil {
		t.Fatal("Combine produced no result")
	}
	if res.Requests != 3 {
		t.Errorf("Requests = %d, want 3", res.Requests)
	}
	if res.Tokens != nil {
		t.Errorf("Tokens = %+v, want nil: the NLI request did not report usage", res.Tokens)
	}
}

// TestCombine_NothingOnBothSidesIsNothing: no judge call and no NLI call
// means no account.
func TestCombine_NothingOnBothSidesIsNothing(t *testing.T) {
	if Combine(nil, nil) != nil {
		t.Error("Combine produced an account out of nothing")
	}
}
