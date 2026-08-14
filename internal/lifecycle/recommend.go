package lifecycle

// Recommendation is the result of intersecting harness native +
// available providers. The orchestrator reads this struct to decide
// which LLM to use for the judge pipeline (consensus, drift, etc.).
type Recommendation struct {
	// ProviderID is the recommended provider's ID. Empty when no
	// provider is available.
	ProviderID string
	// Model is the recommended model (the first model from the
	// provider's catalog). Empty when no provider is available.
	Model string
	// Rung is the rung of the recommended model. Empty when no
	// provider is available.
	Rung HarnessRung
	// MatchedNative is true when the recommendation matched the
	// harness's native family. False when the recommendation fell
	// back to a different family because no provider matches the
	// native family.
	MatchedNative bool
	// AvailableProviders lists the provider IDs whose env keys are set,
	// in catalog order. Empty when no provider is available.
	AvailableProviders []string
}

// Recommend intersects the harness's native capability with the
// available providers and returns the best-aligned model.
//
// Algorithm:
//  1. If any available provider matches the harness's native family,
//     pick the first one (catalog order).
//  2. Else if any provider is available, pick the first (catalog order).
//  3. Else return zero-value Recommendation (no provider configured).
//
// The first-match rule is intentional: it preserves the order
// operators set via env vars (ANTHROPIC wins over OPENAI if both
// are set, etc.).
func Recommend(hn HarnessNative, available []ProviderInfo) Recommendation {
	availableIDs := make([]string, 0, len(available))
	for _, p := range available {
		availableIDs = append(availableIDs, p.ID)
	}
	if len(available) == 0 {
		return Recommendation{AvailableProviders: availableIDs}
	}
	if p, ok := MatchNativeProvider(hn, available); ok {
		return Recommendation{
			ProviderID:        p.ID,
			Model:             p.Models[0],
			Rung:              p.DefaultRung,
			MatchedNative:     true,
			AvailableProviders: availableIDs,
		}
	}
	p := available[0]
	return Recommendation{
		ProviderID:        p.ID,
		Model:             p.Models[0],
		Rung:              p.DefaultRung,
		MatchedNative:     false,
		AvailableProviders: availableIDs,
	}
}
