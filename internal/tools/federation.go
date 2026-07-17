// Package tools — federation.go: wires the dark_memory_federation_lookup
// tool as an EXTRA (not part of the canonical 28; same model as the
// armed-mode redteam extras).
//
// F7 motivation: the dark-memory and dark-research MCPs use two physically
// separate SQLite files. Cross-namespace operations look like "not found"
// from either side because neither side queries the other. This tool makes
// the federation EXPLICIT: an operator (or agent) who suspects an artifact
// lives in the other MCP can call federation_lookup to discover it.
//
// The tool is READ-ONLY by design. It never writes to the peer DB; all
// cross-namespace data movement (if ever needed) must go through explicit
// promote/demote tools that do not exist yet.
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dark-agents/dark-memory-mcp/internal/federation"
)

// federationPeer is the process-wide peer handle, initialized once at
// boot via SetFederationPeer from main.go / RegisterAll. Nil means the
// peer is not configured (env var DARK_FEDERATION_PEER_DSN unset) and
// federation_lookup returns a "peer disabled" hint.
var federationPeer *federation.Peer

// SetFederationPeer installs the process-wide peer handle. Called once
// at boot. Subsequent calls are ignored (idempotent — first writer wins).
func SetFederationPeer(p *federation.Peer) {
	if federationPeer == nil {
		federationPeer = p
	}
}

// GetFederationPeer exposes the peer for in-process callers (e.g.
// pipeline_status uses it to add cross-namespace hints on miss). Returns
// nil when no peer is configured.
func GetFederationPeer() *federation.Peer {
	return federationPeer
}

// federationLookupInput is the input to dark_memory_federation_lookup.
// Either artifact_id OR session_id must be provided (not both required;
// if both are provided, both are queried).
type federationLookupInput struct {
	ArtifactID int64  `json:"artifact_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
}

// FederationLookupResult is the output of dark_memory_federation_lookup.
type FederationLookupResult struct {
	PeerEnabled  bool                       `json:"peer_enabled"`
	PeerDSN      string                     `json:"peer_dsn,omitempty"`
	Hint         string                     `json:"hint,omitempty"`
	PeerArtifact *federation.PeerArtifact    `json:"peer_artifact,omitempty"`
	PeerDrift    *federation.PeerDrift       `json:"peer_drift,omitempty"`
	PeerSessionArtifacts []int64            `json:"peer_session_artifacts,omitempty"`
}

// RegisterFederation wires federation_lookup into the registry as an
// extra (not in canonical order; surfaces after the canonical 28 in
// tools/list). Safe to call when no peer is configured: the tool still
// registers but every call returns a "peer disabled" response.
func RegisterFederation(reg *Registry) {
	reg.Add(BindSimple("federation_lookup",
		"Cross-namespace lookup: check if an artifact_id or session_id exists in the dark-research federation peer (DARK_FEDERATION_PEER_DSN). READ-ONLY. Returns 'peer disabled' when no peer is configured.",
		MustJSONSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"artifact_id": map[string]any{
					"type":        "integer",
					"description": "Optional. Look up this artifact_id in the peer's vibe_artifacts + latest vibe_drift_reports.",
				},
				"session_id": map[string]any{
					"type":        "string",
					"description": "Optional. Return peer artifact IDs whose session_id matches.",
				},
			},
			"additionalProperties": false,
		}),
		func(ctx context.Context, raw json.RawMessage) (*ToolResponse, error) {
			var in federationLookupInput
			if err := json.Unmarshal(raw, &in); err != nil {
				return &ToolResponse{Error: &ToolError{
					Code:    "ErrInvalidArgument",
					Message: fmt.Sprintf("federation_lookup: invalid input: %v", err),
					Hint:    "Pass at least one of artifact_id (integer) or session_id (string).",
				}}, nil
			}
			if in.ArtifactID == 0 && in.SessionID == "" {
				return &ToolResponse{Error: &ToolError{
					Code:    "ErrInvalidArgument",
					Message: "federation_lookup: no lookup keys provided",
					Hint:    "Pass artifact_id or session_id (or both).",
				}}, nil
			}

			out := FederationLookupResult{
				PeerEnabled: federationPeer != nil && federationPeer.IsEnabled(),
			}

			if !out.PeerEnabled {
				out.Hint = "federation is disabled (DARK_FEDERATION_PEER_DSN not set). To enable, set the env var to the path of the dark-research SQLite DB on this host."
				return &ToolResponse{Data: out}, nil
			}

			out.PeerDSN = federationPeer.DSN()

			if in.ArtifactID > 0 {
				art, err := federationPeer.LookupArtifact(ctx, in.ArtifactID)
				if err != nil {
					return &ToolResponse{Error: &ToolError{
						Code:    "ErrInternal",
						Message: fmt.Sprintf("federation_lookup: peer artifact query failed: %v", err),
						Hint:    "Peer DB may be locked or schema-drifted. Check DARK_FEDERATION_PEER_DSN.",
					}}, nil
				}
				out.PeerArtifact = art

				drift, err := federationPeer.LookupDrift(ctx, in.ArtifactID)
				if err != nil {
					return &ToolResponse{Error: &ToolError{
						Code:    "ErrInternal",
						Message: fmt.Sprintf("federation_lookup: peer drift query failed: %v", err),
						Hint:    "Peer DB may be locked or schema-drifted. Check DARK_FEDERATION_PEER_DSN.",
					}}, nil
				}
				out.PeerDrift = drift
			}

			if in.SessionID != "" {
				ids, err := federationPeer.LookupSessionArtifacts(ctx, in.SessionID, 100)
				if err != nil {
					return &ToolResponse{Error: &ToolError{
						Code:    "ErrInternal",
						Message: fmt.Sprintf("federation_lookup: peer session query failed: %v", err),
						Hint:    "Peer DB may be locked or schema-drifted. Check DARK_FEDERATION_PEER_DSN.",
					}}, nil
				}
				out.PeerSessionArtifacts = ids
			}

			return &ToolResponse{Data: out}, nil
		}))
}