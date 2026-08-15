// RecommendedModels is our senior-architect-curated OSINT table
// for picking the best LLM per (provider, eval_type) pair.
//
// This is HARDCODED — we ran the OSINT manually and pinned the
// recommendations. Live OSINT (pricing feeds, benchmark freshness)
// is a Wave 4+ task that refreshes this table from public sources.
// Until then, this file is the authoritative reference.
//
// v2.13.0 update (2026-08-10): the provider set now mirrors the
// provider catalog (provider_catalog.go) — the 8 providers with
// verified endpoints. Model names were re-verified against primary
// docs on 2026-08-10 (row 587):
//
//	anthropic  -> claude-* (Messages API)
//	openai     -> gpt-5 family
//	google     -> gemini-3.6-* (OpenAI-compat)
//	deepseek   -> deepseek-v4-flash / deepseek-v4-pro
//	minimax    -> MiniMax-M3
//	zhipu      -> glm-5.2
//	moonshot   -> kimi-k3 / kimi-k2.7-code
//	qwen       -> qwen3.8-max / qwen3.7-plus / qwen3.7-flash
//
// Providers WITHOUT an implemented endpoint (mistral, cohere, meta,
// xai, perplexity) were removed from this table — they are not
// reachable by the SelfHarnessClient, so recommending a model for
// them was misleading. They can return when endpoints are added.
//
// Per-eval-type model recommendations are based on:
//
//   - Reasoning depth    (drift_judge, consensus → pro-class)
//   - Rule precision     (compliance_check, pii_detect → careful smaller)
//   - Latency/cost       (brand_match, pii_detect → flash/haiku)
//   - Adversarial robustness (prompt_injection_scan → larger + careful)
//   - Multilingual       (Qwen covers 29 languages natively)
//
// Eval types are the strings used in ssd.EvaluationType:
//
//	brand_match, compliance_check, drift_judge, grounding_check,
//	pii_detect, prompt_injection_scan, consensus
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

// RecommendedModels is the provider-catalog-aligned set + per-eval
// model picks. Order matters for "best of all" recommendation: the
// first entry whose provider is detected in env wins.
var RecommendedModels = []ModelRecommendation{
	{
		Provider: "anthropic",
		Default:  "claude-sonnet-4-5",
		PerType: map[ssd.EvaluationType]string{
			ssd.EvalBrandMatch:          "claude-haiku-4-5",  // fast NLU
			ssd.EvalComplianceCheck:     "claude-sonnet-4-5", // careful rule-following
			ssd.EvalDriftJudge:          "claude-opus-4-7",   // deep reasoning
			ssd.EvalGroundingCheck:      "claude-sonnet-4-5",
			ssd.EvalPIIDetect:           "claude-haiku-4-5", // pattern recognition
			ssd.EvalPromptInjectionScan: "claude-sonnet-4-5",
			ssd.EvalConsensus:           "claude-opus-4-7",
			ssd.EvalMindsetCompose:      "claude-sonnet-4-5",
			ssd.EvalMindsetQuality:      "claude-sonnet-4-5",
		},
	},
	{
		Provider: "openai",
		Default:  "gpt-5",
		PerType: map[ssd.EvaluationType]string{
			ssd.EvalBrandMatch:          "gpt-5-mini",
			ssd.EvalComplianceCheck:     "gpt-5",
			ssd.EvalDriftJudge:          "gpt-5",
			ssd.EvalGroundingCheck:      "gpt-5",
			ssd.EvalPIIDetect:           "gpt-5-mini",
			ssd.EvalPromptInjectionScan: "gpt-5",
			ssd.EvalConsensus:           "gpt-5",
			ssd.EvalMindsetCompose:      "gpt-5-mini",
			ssd.EvalMindsetQuality:      "gpt-5",
		},
	},
	{
		Provider: "google",
		Default:  "gemini-3.6-flash",
		PerType: map[ssd.EvaluationType]string{
			ssd.EvalBrandMatch:          "gemini-3.6-flash",
			ssd.EvalComplianceCheck:     "gemini-3.6-pro",
			ssd.EvalDriftJudge:          "gemini-3.6-pro",
			ssd.EvalGroundingCheck:      "gemini-3.6-pro",
			ssd.EvalPIIDetect:           "gemini-3.6-flash",
			ssd.EvalPromptInjectionScan: "gemini-3.6-pro",
			ssd.EvalConsensus:           "gemini-3.6-pro",
			ssd.EvalMindsetCompose:      "gemini-3.6-flash",
			ssd.EvalMindsetQuality:      "gemini-3.6-pro",
		},
	},
	{
		Provider: "deepseek",
		Default:  "deepseek-v4-flash",
		PerType: map[ssd.EvaluationType]string{
			ssd.EvalBrandMatch:          "deepseek-v4-flash",
			ssd.EvalComplianceCheck:     "deepseek-v4-flash",
			ssd.EvalDriftJudge:          "deepseek-v4-pro", // deeper reasoning
			ssd.EvalGroundingCheck:      "deepseek-v4-pro",
			ssd.EvalPIIDetect:           "deepseek-v4-flash",
			ssd.EvalPromptInjectionScan: "deepseek-v4-pro",
			ssd.EvalConsensus:           "deepseek-v4-pro",
			ssd.EvalMindsetCompose:      "deepseek-v4-pro",
			ssd.EvalMindsetQuality:      "deepseek-v4-flash",
		},
	},
	{
		Provider: "minimax",
		Default:  "MiniMax-M3",
		PerType: map[ssd.EvaluationType]string{
			ssd.EvalBrandMatch:          "MiniMax-M3",
			ssd.EvalComplianceCheck:     "MiniMax-M3",
			ssd.EvalDriftJudge:          "MiniMax-M3",
			ssd.EvalGroundingCheck:      "MiniMax-M3",
			ssd.EvalPIIDetect:           "MiniMax-M3",
			ssd.EvalPromptInjectionScan: "MiniMax-M3",
			ssd.EvalConsensus:           "MiniMax-M3",
			ssd.EvalMindsetCompose:      "MiniMax-M3",
			ssd.EvalMindsetQuality:      "MiniMax-M3",
		},
	},
	{
		Provider: "minimax-cn",
		Default:  "MiniMax-M3",
		PerType: map[ssd.EvaluationType]string{
			ssd.EvalBrandMatch:          "MiniMax-M3",
			ssd.EvalComplianceCheck:     "MiniMax-M3",
			ssd.EvalDriftJudge:          "MiniMax-M3",
			ssd.EvalGroundingCheck:      "MiniMax-M3",
			ssd.EvalPIIDetect:           "MiniMax-M3",
			ssd.EvalPromptInjectionScan: "MiniMax-M3",
			ssd.EvalConsensus:           "MiniMax-M3",
			ssd.EvalMindsetCompose:      "MiniMax-M3",
			ssd.EvalMindsetQuality:      "MiniMax-M3",
		},
	},
	{
		Provider: "zhipu",
		Default:  "glm-5.2",
		PerType: map[ssd.EvaluationType]string{
			ssd.EvalBrandMatch:          "glm-5.2",
			ssd.EvalComplianceCheck:     "glm-5.2",
			ssd.EvalDriftJudge:          "glm-5.2",
			ssd.EvalGroundingCheck:      "glm-5.2",
			ssd.EvalPIIDetect:           "glm-5.2",
			ssd.EvalPromptInjectionScan: "glm-5.2",
			ssd.EvalConsensus:           "glm-5.2",
			ssd.EvalMindsetCompose:      "glm-5.2",
			ssd.EvalMindsetQuality:      "glm-5.2",
		},
	},
	{
		Provider: "moonshot",
		Default:  "kimi-k3",
		PerType: map[ssd.EvaluationType]string{
			ssd.EvalBrandMatch:          "kimi-k2.7-code",
			ssd.EvalComplianceCheck:     "kimi-k3",
			ssd.EvalDriftJudge:          "kimi-k3",
			ssd.EvalGroundingCheck:      "kimi-k3",
			ssd.EvalPIIDetect:           "kimi-k2.7-code",
			ssd.EvalPromptInjectionScan: "kimi-k3",
			ssd.EvalConsensus:           "kimi-k3",
			ssd.EvalMindsetCompose:      "kimi-k2.7-code",
			ssd.EvalMindsetQuality:      "kimi-k3",
		},
	},
	{
		Provider: "qwen",
		Default:  "qwen3.8-max",
		PerType: map[ssd.EvaluationType]string{
			ssd.EvalBrandMatch:          "qwen3.7-flash",
			ssd.EvalComplianceCheck:     "qwen3.8-max",
			ssd.EvalDriftJudge:          "qwen3.8-max",
			ssd.EvalGroundingCheck:      "qwen3.8-max",
			ssd.EvalPIIDetect:           "qwen3.7-flash",
			ssd.EvalPromptInjectionScan: "qwen3.8-max",
			ssd.EvalConsensus:           "qwen3.8-max",
			ssd.EvalMindsetCompose:      "qwen3.7-plus",
			ssd.EvalMindsetQuality:      "qwen3.8-max",
		},
	},
}

// RecommendedModel returns the recommended model for (provider,
// eval_type) according to our OSINT catalog. Returns "" if the
// provider is not in the catalog — in that case, the caller is
// expected to fall through to the LLM client's own auto-config
// (SelfHarnessClient).
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

// ListProviders returns the list of known providers.
func ListProviders() []string {
	out := make([]string, len(RecommendedModels))
	for i, rec := range RecommendedModels {
		out[i] = rec.Provider
	}
	return out
}
