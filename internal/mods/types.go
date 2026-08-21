// Package mods defines the mod-registry types. A Mod is a drop-in
// package of knowledge (text/datasets) and/or capabilities (tools,
// parsers, backends). The mod loader discovers mod.toml manifests
// and registers them here.
package mods

// Manifest is the parsed mod.toml. Field tags follow go-toml/v2 conventions.
// Schema is aligned with the dark-research-mcp mod format (v1.0.0) so
// dark-memory can load the same peer mods (e.g. user/red-team-jailbreak-arsenal,
// user/osint-cve-deepdive) without duplication. Optional fields use
// omitempty so older mods that lack them still parse.
type Manifest struct {
	Meta         Meta          `toml:"meta"          json:"meta"`
	Requirements Requirements  `toml:"requirements"  json:"requirements"`
	Capabilities Capabilities  `toml:"capabilities"  json:"capabilities"`
	Knowledge    KnowledgeRefs `toml:"knowledge"     json:"knowledge"`
	Directives   DirectiveRefs `toml:"directives"    json:"directives"`
	Activation   Activation    `toml:"activation"    json:"activation"`
	Risk         Risk          `toml:"risk"          json:"risk"`
}

type Meta struct {
	ID          string   `toml:"id"          json:"mod_id"`
	Name        string   `toml:"name"        json:"name"`
	Version     string   `toml:"version"     json:"version"`
	Author      string   `toml:"author"      json:"author,omitempty"`
	License     string   `toml:"license"     json:"license,omitempty"`
	Description string   `toml:"description" json:"description,omitempty"`
	Homepage    string   `toml:"homepage"    json:"homepage,omitempty"`
	Tags        []string `toml:"tags"        json:"tags,omitempty"`
}

type Requirements struct {
	DarkResearchVersion       string   `toml:"dark_research_version"       json:"dark_research_version,omitempty"`
	ConstitutionCompatibility []string `toml:"constitution_compatibility"  json:"constitution_compatibility,omitempty"`
	Mods                      []string `toml:"mods"                         json:"mods,omitempty"`
}

type Capabilities struct {
	Tools    []string `toml:"tools"    json:"tools,omitempty"`
	Parsers  []string `toml:"parsers"  json:"parsers,omitempty"`
	Backends []string `toml:"backends" json:"backends,omitempty"`
}

type KnowledgeRefs struct {
	PromptInjections []string `toml:"prompt_injections" json:"prompt_injections,omitempty"`
	DataSources      []string `toml:"data_sources"      json:"data_sources,omitempty"`
}

type DirectiveRefs struct {
	PromptFragments []string `toml:"prompt_fragments" json:"prompt_fragments,omitempty"`
}

type Activation struct {
	AutoLoad bool `toml:"auto_load" json:"auto_load"`
}

type Risk struct {
	Class        string   `toml:"risk_class"    json:"risk_class"`
	TargetScope  string   `toml:"target_scope"  json:"target_scope"`
	RequiresTor  bool     `toml:"requires_tor"  json:"requires_tor"`
	RequiresAuth []string `toml:"requires_auth" json:"requires_auth,omitempty"`
}

// RiskClass is the declared risk envelope for a mod.
type RiskClass string

const (
	RiskClassResearchOnly       RiskClass = "research-only"
	RiskClassActiveProbing      RiskClass = "active-probing"
	RiskClassExploitDevelopment RiskClass = "exploit-development"
)

// TargetScope declares where the mod's tools are allowed to operate.
type TargetScope string

const (
	TargetScopePublicInternet        TargetScope = "public_internet"
	TargetScopePrivateInfrastructure TargetScope = "private_infrastructure"
	TargetScopeDarkweb               TargetScope = "darkweb"
	TargetScopeLocalOnly             TargetScope = "local_only"
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
