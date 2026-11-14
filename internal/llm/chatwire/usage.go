package chatwire

import "encoding/json"

// Usage is the token usage that an OpenAI-compatible provider reports for
// one request:
//
//	{"usage":{"prompt_tokens":1071,"completion_tokens":295,
//	          "total_tokens":1366,
//	          "prompt_tokens_details":{"cached_tokens":128},
//	          "completion_tokens_details":{"reasoning_tokens":207}}}
//
// The two "details" blocks are optional. Not every provider fills them, and
// a provider that does not fill them reports ZERO, not "unknown". The
// difference matters for the cache: cached tokens are billed differently
// from fresh ones, so a cost that pretends they are all fresh is wrong.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
}

// ExtractUsage reads the `usage` object of an OpenAI-compatible response.
//
// ok is FALSE when the provider does not report usage at all. That is NOT
// the same as a usage of zero: a provider that says nothing about tokens is
// a MISSING datum, and the difference decides whether a cost can be
// published or has to stay declared as unmeasured.
//
// The body is read as it came: the caller extracts the usage from the RAW
// response, BEFORE the OpenAI shape is unwrapped, because the unwrapped
// content carries no usage.
func ExtractUsage(body []byte) (Usage, bool) {
	var shape struct {
		Usage *struct {
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			TotalTokens         int `json:"total_tokens"`
			PromptTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CompletionTokensDetails *struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &shape); err != nil || shape.Usage == nil {
		return Usage{}, false
	}
	u := Usage{
		PromptTokens:     shape.Usage.PromptTokens,
		CompletionTokens: shape.Usage.CompletionTokens,
		TotalTokens:      shape.Usage.TotalTokens,
	}
	if shape.Usage.PromptTokensDetails != nil {
		u.CachedTokens = shape.Usage.PromptTokensDetails.CachedTokens
	}
	if shape.Usage.CompletionTokensDetails != nil {
		u.ReasoningTokens = shape.Usage.CompletionTokensDetails.ReasoningTokens
	}
	// Some providers omit total_tokens. The sum of the other two is the
	// total, and a total of zero with tokens on both sides is a missing
	// field, not a free request.
	if u.TotalTokens == 0 && (u.PromptTokens > 0 || u.CompletionTokens > 0) {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
	return u, true
}

// Add sums two usages.
//
// It is used to add the attempts of one judge call: a verdict that took two
// requests cost two requests, and reporting only the second would understate
// the cost by half.
func (u Usage) Add(other Usage) Usage {
	return Usage{
		PromptTokens:     u.PromptTokens + other.PromptTokens,
		CompletionTokens: u.CompletionTokens + other.CompletionTokens,
		TotalTokens:      u.TotalTokens + other.TotalTokens,
		CachedTokens:     u.CachedTokens + other.CachedTokens,
		ReasoningTokens:  u.ReasoningTokens + other.ReasoningTokens,
	}
}

// ProviderUsage is what one verdict cost the provider: how many requests it
// made, and the tokens those requests reported.
//
// Requests is a FACT: the request happened, and it is billed whether or not
// the provider said anything about its tokens.
//
// Tokens is nil when NOT every request reported usage. A partial sum
// written as a total is a wrong number, and this data exists for one thing:
// to say what the judge cost.
type ProviderUsage struct {
	Requests int
	Tokens   *Usage
}

// Account accumulates the provider requests behind one verdict.
//
// The zero value is an empty account: no requests, no tokens. The callers
// add one request at a time, because a verdict can cost one request (the
// judge answered) or several (the judge retried, and then the NLI anchor
// answered).
type Account struct {
	requests int
	tokens   *Usage
	sinUso   int
}

// Add folds one provider request into the account.
//
// A nil usage means the provider did NOT report usage for that request. The
// account keeps the count of requests — that part is a fact — and refuses
// to publish a partial sum as a total.
func (a *Account) Add(u *Usage) {
	a.requests++
	if u == nil {
		a.sinUso++
		return
	}
	if a.tokens == nil {
		copia := *u
		a.tokens = &copia
	} else {
		suma := a.tokens.Add(*u)
		a.tokens = &suma
	}
}

// Result returns the account, or nil when no request was made.
//
// The returned account carries the tokens ONLY when every request reported
// usage. Otherwise Tokens is nil: the requests are known, the cost is not.
func (a *Account) Result() *ProviderUsage {
	if a.requests == 0 {
		return nil
	}
	res := &ProviderUsage{Requests: a.requests}
	if a.sinUso == 0 && a.tokens != nil {
		copia := *a.tokens
		res.Tokens = &copia
	}
	return res
}

// Combine merges two accounts into one.
//
// The result is nil when both are nil. The tokens survive only when BOTH
// sides have them: the same rule as Account, applied to the merge.
//
// A missing side (the NLI chain did not run, say) does not make the result
// incomplete: what did not happen was not billed. Only a side that HAPPENED
// and did not report its usage makes the total unknowable.
func Combine(a, b *ProviderUsage) *ProviderUsage {
	if a == nil && b == nil {
		return nil
	}
	res := &ProviderUsage{}
	completo := true
	tokens := &Usage{}
	hayTokens := false
	for _, parte := range []*ProviderUsage{a, b} {
		if parte == nil {
			continue
		}
		res.Requests += parte.Requests
		if parte.Tokens == nil {
			completo = false
			continue
		}
		if !hayTokens {
			copia := *parte.Tokens
			tokens = &copia
			hayTokens = true
			continue
		}
		suma := tokens.Add(*parte.Tokens)
		tokens = &suma
	}
	if completo && hayTokens {
		res.Tokens = tokens
	}
	return res
}
