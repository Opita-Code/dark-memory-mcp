// Package lifecycle detects the runtime characteristics of the host
// harness and the available LLM providers, and computes a model
// recommendation that aligns the two.
//
// Spec 1171 (plan label "Spec 1150") ships this package in v2.18.0.
// It is the foundation for spec 1152 (async + delegation) and spec
// 1153 (auto-config wizard), which depend on the harness fingerprint
// and provider catalog exposed here.
package lifecycle

// HarnessRung is the harness's native capability tier. The tier
// maps to the model families the harness natively understands:
//
//   - RungHeavy  : Opus-class, Gemini-Pro, GPT-5
//   - RungMedium : Sonnet-class, DeepSeek-V4, MiniMax-M3
//   - RungLight  : Haiku-class, flash, mini
//   - RungUnknown: harness not in the catalog (canonical name "unknown")
type HarnessRung string

const (
	RungHeavy   HarnessRung = "heavy"
	RungMedium  HarnessRung = "medium"
	RungLight   HarnessRung = "light"
	RungUnknown HarnessRung = "unknown"
)

// HarnessNative describes one harness's native capability. Sourced
// from the hardcoded harnessCatalog (LookUpHarnessNative).
type HarnessNative struct {
	// Family is the model family the harness natively understands.
	// Values: "anthropic", "openai", "google", "deepseek", "minimax",
	// "minimax-cn", "moonshot", "z-ai", "dashscope", "multi", "unknown".
	Family string
	// Rung is the harness's native capability tier.
	Rung HarnessRung
	// NativeModels is the harness's native model lineup, ordered
	// from highest to lowest rung. Empty for "multi" harnesses
	// (Continue.dev, Cline) where the operator configures models.
	NativeModels []string
	// CanWriteConfig is true when the harness has a writable config
	// file the MCP can target (e.g. opencode.jsonc, .claude/settings.json).
	// Used by spec 1153 (auto-config wizard) to decide whether to offer
	// auto-write.
	CanWriteConfig bool
	// ConfigPath is the path to the config file, informational only.
	// Empty when CanWriteConfig is false.
	ConfigPath string
}

// harnessCatalog is the hardcoded map of canonical harness names to
// HarnessNative. Sources (all 2026 public docs):
//   - opencode: model_version = opencode/MiniMax-M3
//   - Claude Code: claude-sonnet-4.5 / claude-opus-4.5 / claude-haiku-4.5
//   - Claude Desktop: same as Claude Code (consumer wrapper)
//   - Codex: gpt-5 / gpt-5.5 / gpt-5-mini
//   - Cursor: claude-sonnet family (Cursor's default provider)
//   - Continue.dev: multi-provider (configurable in ~/.continue/config.json)
//   - Cline: multi-provider (configurable in VS Code settings)
//
// "unknown" is the catch-all for harnesses not in the catalog.
var harnessCatalog = map[string]HarnessNative{
	"opencode": {
		Family:         "minimax",
		Rung:           RungMedium,
		NativeModels:   []string{"MiniMax-M3", "minimax/M3"},
		CanWriteConfig: true,
		ConfigPath:     "opencode.jsonc",
	},
	"claude-code": {
		Family:         "anthropic",
		Rung:           RungMedium,
		NativeModels:   []string{"claude-sonnet-4.5", "claude-opus-4.5", "claude-haiku-4.5"},
		CanWriteConfig: true,
		ConfigPath:     ".claude/settings.json",
	},
	"claude-desktop": {
		Family:         "anthropic",
		Rung:           RungMedium,
		NativeModels:   []string{"claude-sonnet-4.5", "claude-opus-4.5", "claude-haiku-4.5"},
		CanWriteConfig: false,
		ConfigPath:     "",
	},
	"claude-family": {
		Family:         "anthropic",
		Rung:           RungMedium,
		NativeModels:   []string{"claude-sonnet-4.5", "claude-opus-4.5", "claude-haiku-4.5"},
		CanWriteConfig: false,
		ConfigPath:     "",
	},
	"codex": {
		Family:         "openai",
		Rung:           RungMedium,
		NativeModels:   []string{"gpt-5", "gpt-5.5", "gpt-5-mini"},
		CanWriteConfig: true,
		ConfigPath:     ".codex/config.toml",
	},
	"cursor": {
		Family:         "anthropic",
		Rung:           RungMedium,
		NativeModels:   []string{"claude-sonnet-4.5", "claude-opus-4.5"},
		CanWriteConfig: true,
		ConfigPath:     ".cursor/config.json",
	},
	"continue": {
		Family:         "multi",
		Rung:           RungMedium,
		NativeModels:   []string{},
		CanWriteConfig: true,
		ConfigPath:     "~/.continue/config.json",
	},
	"cline": {
		Family:         "multi",
		Rung:           RungMedium,
		NativeModels:   []string{},
		CanWriteConfig: true,
		ConfigPath:     "VS Code settings.json",
	},
	"unknown": {
		Family:         "unknown",
		Rung:           RungUnknown,
		NativeModels:   []string{},
		CanWriteConfig: false,
		ConfigPath:     "",
	},
}

// LookupHarnessNative returns the harness's native capability by
// canonical name. Returns the "unknown" entry when the canonical name
// is not in the catalog.
//
// The canonical name is one of: opencode, claude-code, claude-desktop,
// claude-family, codex, cursor, continue, cline, unknown. The
// canonicalization is done by agentbootstrap.NormalizeClientName.
func LookupHarnessNative(canonical string) HarnessNative {
	if n, ok := harnessCatalog[canonical]; ok {
		return n
	}
	return harnessCatalog["unknown"]
}
