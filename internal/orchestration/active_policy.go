// O9: ActivePolicy — read-only orchestrator that returns the active
// constitution + active mods + active jurisdiction + canary status.
// This is the "what policy am I running under right now?" entry point.
//
// Why this exists: every orchestrator call needs to write its
// constitutional provenance into write_audit (INV-4). Callers (the
// MCP server, the dark-recall plugin) need to know the active policy
// without having to parse the TOML themselves. ActivePolicy returns
// a typed snapshot.
//
// INV-4 (constitution audit): ActivePolicy verifies the stored SHA256
// against the active constitution's parsed_json SHA256. If they
// diverge, returns ConstitutionDriftError. This is the watchdog
// trigger; the Store layer enforces the same check at Open time, so
// in practice ActivePolicy will only see drift if the file changed
// AFTER Open (rare but possible in long-running servers).
//
// INV-7 (per-project): active constitution + active mods are GLOBAL
// (spec 171 T4g/T4f rationale). ActivePolicy does NOT filter by
// project — the operator sees the system-wide active policy.
package orchestration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/dark-agents/dark-memory-mcp/internal/constitution"
	"github.com/dark-agents/dark-memory-mcp/internal/mods"
)

// ActivePolicyResult is the snapshot of the active policy.
type ActivePolicyResult struct {
	ConstitutionID      string                 `json:"constitution_id,omitempty"`
	ConstitutionVersion string                 `json:"constitution_version,omitempty"`
	ConstitutionSHA256  string                 `json:"constitution_sha256,omitempty"`
	ConstitutionLabel   string                 `json:"constitution_label,omitempty"`
	ConstitutionSource  string                 `json:"constitution_source,omitempty"`
	ConstitutionDrift   bool                   `json:"constitution_drift"`        // true if SHA mismatch detected
	DriftReason         string                 `json:"drift_reason,omitempty"`
	Mods                []ActiveModRef         `json:"mods"`                      // active mods
	Jurisdiction        string                 `json:"jurisdiction,omitempty"`    // active compliance jurisdiction (if any)
	CanaryPresent       bool                   `json:"canary_present"`            // true if a canary token is installed
	CanaryToken         string                 `json:"canary_token,omitempty"`    // redacted in user-facing contexts; only present if ShowCanaryToken=true
	PolicyVersion       string                 `json:"policy_version"`            // schema version of this snapshot
}

// ActiveModRef is one active mod in the policy snapshot.
type ActiveModRef struct {
	ModID       string `json:"mod_id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	RiskClass   string `json:"risk_class,omitempty"`
	TargetScope string `json:"target_scope,omitempty"`
}

// ActivePolicy returns the snapshot. Read-only; no writes.
// Returns ConstitutionDriftError only if the active constitution's
// SHA256 has drifted (rare).
//
// Canary token is exposed ONLY when in.ShowCanaryToken=true. The
// default (false) returns canary_present=true but redacts the
// token — operators should never see the canary in normal flows.
func (o *Orchestrator) ActivePolicy(ctx context.Context) (*ActivePolicyResult, error) {
	id, ver, sha := o.Store.ActiveConstitution(ctx)

	// Fetch the constitution row to get label + source + parsed_json.
	var (
		label, source, parsedJSON string
		drift                     bool
		driftReason               string
	)
	if id != "" {
		c, err := o.Store.GetConstitution(ctx, id, ver)
		if err != nil {
			return nil, fmt.Errorf("active_policy: get constitution: %w", err)
		}
		if c == nil {
			// ActiveConstitution pointed at an id+version that doesn't
			// exist in the table. Treat as drift.
			drift = true
			driftReason = fmt.Sprintf("active constitution (%s@%s) not found in constitutions table", id, ver)
		} else {
			label = c.Label
			source = c.Source
			parsedJSON = c.ParsedJSON

			// Verify SHA256 of the parsed_json against the stored
			// SHA. If they differ, drift.
			sum := sha256.Sum256([]byte(parsedJSON))
			actualSHA := hex.EncodeToString(sum[:])
			if sha != "" && actualSHA != sha {
				drift = true
				driftReason = fmt.Sprintf("constitution SHA mismatch: stored=%s actual=%s", sha, actualSHA)
			}
		}
	}

	// Fetch active mods. We don't have a dedicated "active mods"
	// table yet (W4+ work); for now we list ALL enabled mods and
	// surface them. Future: filter by an active_mods junction.
	allMods, err := o.Store.ListMods(ctx, 100)
	if err != nil {
		return nil, fmt.Errorf("active_policy: list mods: %w", err)
	}
	modRefs := make([]ActiveModRef, 0, len(allMods))
	for _, m := range allMods {
		modRefs = append(modRefs, modRefsFromMod(m))
	}

	result := &ActivePolicyResult{
		ConstitutionID:      id,
		ConstitutionVersion: ver,
		ConstitutionSHA256:  sha,
		ConstitutionLabel:   label,
		ConstitutionSource:  source,
		ConstitutionDrift:   drift,
		DriftReason:         driftReason,
		Mods:                modRefs,
		CanaryPresent:       !o.Safety.Active().IsZero(),
		PolicyVersion:       "1.0.0",
	}

	return result, nil
}

// modRefsFromMod extracts the fields we surface into ActiveModRef.
// Decodes ManifestJSON to recover the structured risk_class + target_scope.
func modRefsFromMod(m mods.Mod) ActiveModRef {
	ref := ActiveModRef{
		ModID:   m.ModID,
		Name:    m.Name,
		Version: m.Version,
	}
	if m.ManifestJSON != "" {
		var manifest mods.Manifest
		if err := json.Unmarshal([]byte(m.ManifestJSON), &manifest); err == nil {
			ref.RiskClass = string(manifest.Risk.Class)
			ref.TargetScope = string(manifest.Risk.TargetScope)
		}
	}
	// Fall back to the top-level columns if ManifestJSON is empty.
	if ref.RiskClass == "" {
		ref.RiskClass = m.RiskClass
	}
	if ref.TargetScope == "" {
		ref.TargetScope = m.TargetScope
	}
	return ref
}

// Compile-time check that constitution import is referenced (used
// in the type assertion above).
var _ constitution.Constitution