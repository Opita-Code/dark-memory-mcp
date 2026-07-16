// RecommendedModels is our senior-architect-curated OSINT table
// for picking the best LLM per (provider, eval_type) pair.
//
// This is HARDCODED — we ran the OSINT manually and pinned the
// recommendations. Live OSINT (pricing feeds, benchmark freshness)
// is a Wave 4+ task that refreshes this table from public sources.
// Until then, this file is the authoritative reference.
//
// Top 10 cloud LLM providers as of 2026-Q3. Per-eval-type model
// recommendations are based on:
//
//   - Reasoning depth    (drift_judge, consensus → opus-class)
//   - Rule precision     (compliance_check, pii_detect → careful smaller)
//   - Latency/cost       (brand_match, pii_detect → flash/haiku)
//   - Adversarial robustness (prompt_injection_scan → larger + careful)
//   - Multilingual       (Qwen covers 29 languages natively)
//
// Eval types are the strings used in ssd.EvaluationType:
//
//   brand_match, compliance_check, drift_judge, grounding_check,
//   pii_detect, prompt_injection_scan, consensus
package orchestration

import (
	"github.com/dark-agents/dark-memory-mcp/internal/ssd"
)

// ModelRecommendation picks one model per (provider, eval_type) pair.
// Default is the model used when no eval_type-specific override exists.
type ModelRecommendation struct {
	Provider string
	Default  string
	PerType  map[ssd.EvaluationType]string
}

// RecommendedModels is the top-10 cloud LLM providers + per-eval
// model picks. Order matters for "best of all" recommendation: the
// first entry whose provider is detected in env wins.
var RecommendedModels = []ModelRecommendation{
	{
		Provider: "anthropic",
		Default:  "claude-sonnet-4-5",
		PerType: map[ssd.EvaluationType]string{
			ssd.EvalBrandMatch:           "claude-haiku-4-5", // fast NLU
			ssd.EvalComplianceCheck:      "claude-sonnet-4-5", // careful rule-following
			ssd.EvalDriftJudge:           "claude-opus-4-7",   // deep reasoning
			ssd.EvalGroundingCheck:       "claude-sonnet-4-5",
			ssd.EvalPIIDetect:            "claude-haiku-4-5", // pattern recognition
			ssd.EvalPromptInjectionScan: "claude-sonnet-4-5",
			ssd.EvalConsensus:            "claude-opus-4-7",
		},
	},
	{
		Provider: "openai",
		Default:  "gpt-5",
		PerType: map[ssd.EvaluationType]string{
			ssd.EvalBrandMatch:           "gpt-5-mini",
			ssd.EvalComplianceCheck:      "gpt-5",
			ssd.EvalDriftJudge:           "gpt-5",
			ssd.EvalGroundingCheck:       "gpt-5",
			ssd.EvalPIIDetect:            "gpt-5-mini",
			ssd.EvalPromptInjectionScan: "gpt-5",
			ssd.EvalConsensus:            "gpt-5",
		},
	},
	{
		Provider: "google",
		Default:  "gemini-2.5-pro",
		PerType: map[ssd.EvaluationType]string{
			ssd.EvalBrandMatch:           "gemini-2.5-flash",
			ssd.EvalComplianceCheck:      "gemini-2.5-pro",
			ssd.EvalDriftJudge:           "gemini-2.5-pro",
			ssd.EvalGroundingCheck:       "gemini-2.5-pro",
			ssd.EvalPIIDetect:            "gemini-2.5-flash",
			ssd.EvalPromptInjectionScan: "gemini-2.5-pro",
			ssd.EvalConsensus:            "gemini-2.5-pro",
		},
	},
	{
		Provider: "mistral",
		Default:  "mistral-large-2",
		PerType: map[ssd.EvaluationType]string{
			ssd.EvalBrandMatch:           "mistral-small",
			ssd.EvalComplianceCheck:      "mistral-large-2",
			ssd.EvalDriftJudge:           "mistral-large-2",
			ssd.EvalGroundingCheck:       "mistral-large-2",
			ssd.EvalPIIDetect:            "mistral-small",
			ssd.EvalPromptInjectionScan: "mistral-large-2",
			ssd.EvalConsensus:            "mistral-large-2",
		},
	},
	{
		Provider: "cohere",
		Default:  "command-r-plus",
		PerType: map[ssd.EvaluationType]string{
			ssd.EvalBrandMatch:           "command-r",
			ssd.EvalComplianceCheck:      "command-r-plus",
			ssd.EvalDriftJudge:           "command-r-plus",
			ssd.EvalGroundingCheck:       "command-r-plus",
			ssd.EvalPIIDetect:            "command-r",
			ssd.EvalPromptInjectionScan: "command-r-plus",
			ssd.EvalConsensus:            "command-r-plus",
		},
	},
	{
		Provider: "meta",
		Default:  "llama-3.1-405b",
		PerType: map[ssd.EvaluationType]string{
			ssd.EvalBrandMatch:           "llama-3.1-70b",
			ssd.EvalComplianceCheck:      "llama-3.1-405b",
			ssd.EvalDriftJudge:           "llama-3.1-405b",
			ssd.EvalGroundingCheck:       "llama-3.1-405b",
			ssd.EvalPIIDetect:            "llama-3.1-8b",
			ssd.EvalPromptInjectionScan: "llama-3.1-405b",
			ssd.EvalConsensus:            "llama-3.1-405b",
		},
	},
	{
		Provider: "xai",
		Default:  "grok-2",
		PerType: map[ssd.EvaluationType]string{
			ssd.EvalBrandMatch:           "grok-2-mini",
			ssd.EvalComplianceCheck:      "grok-2",
			ssd.EvalDriftJudge:           "grok-2",
			ssd.EvalGroundingCheck:       "grok-2",
			ssd.EvalPIIDetect:            "grok-2-mini",
			ssd.EvalPromptInjectionScan: "grok-2",
			ssd.EvalConsensus:            "grok-2",
		},
	},
	{
		Provider: "deepseek",
		Default:  "deepseek-v3",
		PerType: map[ssd.EvaluationType]string{
			ssd.EvalBrandMatch:           "deepseek-v3",
			ssd.EvalComplianceCheck:      "deepseek-v3",
			ssd.EvalDriftJudge:           "deepseek-r1", // reasoning
			ssd.EvalGroundingCheck:       "deepseek-v3",
			ssd.EvalPIIDetect:            "deepseek-v3",
			ssd.EvalPromptInjectionScan: "deepseek-v3",
			ssd.EvalConsensus:            "deepseek-r1",
		},
	},
	{
		Provider: "qwen",
		Default:  "qwen-2.5-72b",
		PerType: map[ssd.EvaluationType]string{
			ssd.EvalBrandMatch:           "qwen-2.5-7b",
			ssd.EvalComplianceCheck:      "qwen-2.5-72b",
			ssd.EvalDriftJudge:           "qwen-2.5-72b",
			ssd.EvalGroundingCheck:       "qwen-2.5-72b",
			ssd.EvalPIIDetect:            "qwen-2.5-7b",
			ssd.EvalPromptInjectionScan: "qwen-2.5-72b",
			ssd.EvalConsensus:            "qwen-2.5-72b",
		},
	},
	{
		Provider: "perplexity",
		Default:  "sonar-pro",
		PerType: map[ssd.EvaluationType]string{
			ssd.EvalBrandMatch:           "sonar",
			ssd.EvalComplianceCheck:      "sonar-pro",
			ssd.EvalDriftJudge:           "sonar-pro",
			ssd.EvalGroundingCheck:       "sonar-pro", // search-augmented — best for grounding
			ssd.EvalPIIDetect:            "sonar",
			ssd.EvalPromptInjectionScan: "sonar-pro",
			ssd.EvalConsensus:            "sonar-pro",
		},
	},
}

// RecommendedModel returns the recommended model for (provider,
// eval_type) according to our OSINT catalog. Returns "" if the
// provider is not in the catalog — in that case, the caller is
// expected to fall through to the LLM client's own auto-config
// (SelfHarnessClient or DarkscrapperClient).
func RecommendedModel(provider, evalType string) string {
	for _, rec := range RecommendedModels {
		if rec.Provider == provider {
			if m, ok := rec.PerType[ssd.EvaluationType(evalType)]; ok && m != "" {
				return m
			}
			return rec.Default
		}
	}
	return ""
}

// IsKnownProvider returns true if the provider has a recommendation
// in our OSINT catalog.
func IsKnownProvider(provider string) bool {
	for _, rec := range RecommendedModels {
		if rec.Provider == provider {
			return true
		}
	}
	return false
}

// ListProviders returns the list of known providers (top-10).
func ListProviders() []string {
	out := make([]string, len(RecommendedModels))
	for i, rec := range RecommendedModels {
		out[i] = rec.Provider
	}
	return out
}