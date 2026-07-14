// Package mods defines the mod-registry types. A Mod is a drop-in
// package of knowledge (text/datasets) and/or capabilities (tools,
// parsers, backends). The mod loader discovers mod.toml manifests
// and registers them here.
package mods

// Manifest is the parsed mod.toml. Field tags follow go-toml/v2 conventions.
type Manifest struct {
	Meta         Meta          `toml:"meta"`
	Risk         Risk          `toml:"risk"`
	Knowledge    KnowledgeRefs `toml:"knowledge"`
	Directives   DirectiveRefs `toml:"directives"`
	Capabilities Capabilities  `toml:"capabilities"`
}

type Meta struct {
	ID      string `toml:"id"`
	Version string `toml:"version"`
	Name    string `toml:"name"`
}

type Risk struct {
	Class       string `toml:"risk_class"`
	TargetScope string `toml:"target_scope"`
}

type KnowledgeRefs struct {
	PromptInjections []string `toml:"prompt_injections"`
	DataSources      []string `toml:"data_sources"`
}

type DirectiveRefs struct {
	PromptFragments []string `toml:"prompt_fragments"`
}

type Capabilities struct {
	Tools    []string `toml:"tools"`
	Parsers  []string `toml:"parsers"`
	Backends []string `toml:"backends"`
}

// RiskClass is the declared risk envelope for a mod.
type RiskClass string

const (
	RiskClassResearchOnly      RiskClass = "research-only"
	RiskClassActiveProbing     RiskClass = "active-probing"
	RiskClassExploitDevelopment RiskClass = "exploit-development"
)

// TargetScope declares where the mod's tools are allowed to operate.
type TargetScope string

const (
	TargetScopePublicInternet      TargetScope = "public_internet"
	TargetScopePrivateInfrastructure TargetScope = "private_infrastructure"
	TargetScopeDarkweb            TargetScope = "darkweb"
	TargetScopeLocalOnly          TargetScope = "local_only"
)

// Source describes where a mod came from.
type Source string

const (
	SourceUser     Source = "user"
	SourceRegistry Source = "registry"
)

// Mod is one installed mod manifest.
type Mod struct {
	ID           int64  `json:"id"`
	ModID        string `json:"mod_id"`
	Name         string `json:"name"`
	Version      string `json:"version"`
	Source       string `json:"source"`
	ManifestJSON string `json:"manifest_json"`
	SHA256       string `json:"sha256"`
	RiskClass    string `json:"risk_class,omitempty"`
	TargetScope  string `json:"target_scope,omitempty"`
	RequiresTor  bool   `json:"requires_tor"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

// ModLoad is one load event: "mod X was loaded at time T under
// constitution Y, took D milliseconds, contributed C capabilities".
type ModLoad struct {
	ID                int64  `json:"id"`
	ModID             string `json:"mod_id"`
	SessionID         string `json:"session_id,omitempty"`
	LoadedAt          string `json:"loaded_at"`
	DurationMs        int64  `json:"duration_ms"`
	CapabilitiesCount int    `json:"capabilities_count"`
	Error             string `json:"error,omitempty"`
	ConstitutionID    string `json:"constitution_id,omitempty"`
}
