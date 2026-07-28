// Package agentbootstrap - clientinfo.go: dual-spec clientInfo reader.
//
// The MCP spec evolved how clientInfo is communicated:
//
//   - Spec 2025-06-18 (legacy): clientInfo is sent in the initialize
//     handshake, at params.clientInfo. Read once, before any other
//     request is processed.
//   - Spec 2026-07-28 (new): clientInfo moves out of the handshake and
//     into the _meta bag on every per-request message. The handshake
//     no longer carries it.
//
// This file implements a single source of truth (CurrentClientInfo)
// that reads from EITHER path and exposes whichever the harness
// provided last. Both paths converge on the same struct; tools that
// want to know "what harness is running me" query CurrentClientInfo
// without caring about spec version.
//
// Concretely:
//
//   - The MCP server's OnAfterInitialize hook is wired up in Register.
//     It reads the InitializeRequest.ClientInfo and stores it.
//   - The MCP server's WithMetaPropagator option is wired up with a
//     propagator that watches for clientInfo keys in _meta and updates
//     the stored clientInfo on every request.
//
// The legacy path is dominant in 2026 — most harnesses today still
// send initialize.clientInfo. The new path will become dominant once
// the 2026-07-28 spec lands in SDKs. We support both today because the
// cost is one struct field and a few lines of glue code; the benefit
// is that the recommend_companions and detect_environment tools work
// uniformly across both spec versions.
package agentbootstrap

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// MetaClientInfoKey is the conventional key under which a 2026-07-28-
// era harness places its clientInfo in the per-request _meta bag. The
// spec is still being finalized, but the de-facto convention (as of
// SEP-2575) is to nest a full Implementation object under this key.
const MetaClientInfoKey = "clientInfo"

// ClientInfoRecord is a snapshot of the harness's identifying metadata
// at the moment it was last observed. The snapshot includes:
//
//   - info: the harness's Implementation struct (Name, Title, Version)
//   - source: which spec path the data came from (legacy handshake
//     vs new _meta per-request)
//   - specVersion: the protocolVersion the harness advertised in its
//     most recent initialize request, if any
//   - lastSeenUnixMillis: wall-clock millis when the snapshot was
//     last updated (for diagnostics / drift detection)
type ClientInfoRecord struct {
	Info             mcp.Implementation
	Source           string // "initialize.clientInfo" or "_meta.clientInfo"
	SpecVersion      string
	LastSeenUnixMill int64
}

// store is the goroutine-safe singleton that holds the most recent
// clientInfo. Single-process servers get a single instance; tests
// construct their own via NewClientInfoStore for isolation.
//
// We use sync.RWMutex because reads (the hot path: every tool call to
// recommend_companions / detect_environment) vastly outnumber writes
// (one per connect + one per request with a new clientInfo).
type store struct {
	mu  sync.RWMutex
	now func() int64 // injectable clock for tests

	// current is the most recent ClientInfoRecord, or zero value if
	// no client has connected yet.
	current ClientInfoRecord
}

var (
	globalStore     *store
	globalStoreOnce sync.Once
)

// globalClientInfoStore returns the process-wide singleton. Tests that
// need isolation should call NewClientInfoStore() instead and pass the
// returned store around explicitly.
func globalClientInfoStore() *store {
	globalStoreOnce.Do(func() {
		globalStore = &store{now: unixMillisNow}
	})
	return globalStore
}

// NewClientInfoStore returns a fresh, isolated clientInfo store. Used
// by tests; production code uses the globalClientInfoStore() singleton.
func NewClientInfoStore() *store {
	return &store{now: unixMillisNow}
}

// Current returns the most recent ClientInfoRecord. Returns the zero
// value if no harness has connected yet (caller checks for empty Name).
func (s *store) Current() ClientInfoRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// SetFromInitialize stores the clientInfo captured from the legacy
// initialize handshake. Called from Register's OnAfterInitialize hook.
func (s *store) SetFromInitialize(req *mcp.InitializeRequest) {
	if req == nil {
		return
	}
	s.set(req.Params.ClientInfo, req.Params.ProtocolVersion, "initialize.clientInfo")
}

// SetFromMeta inspects the given per-request _meta bag for a clientInfo
// key and stores whatever it finds. Returns true if the _meta bag
// contained a usable clientInfo (caller can use the bool to log).
func (s *store) SetFromMeta(meta *mcp.Meta) bool {
	if meta == nil {
		return false
	}
	// The MCP Meta type is map[string]any. The new spec nests a
	// full Implementation under MetaClientInfoKey. We accept the
	// Implementation directly OR a stringified form (defensive: some
	// early 2026-07-28 implementations may serialize as a string).
	raw, ok := meta.AdditionalFields[MetaClientInfoKey]
	if !ok {
		return false
	}
	switch v := raw.(type) {
	case mcp.Implementation:
		s.set(v, "", "_meta.clientInfo")
		return true
	case *mcp.Implementation:
		if v != nil {
			s.set(*v, "", "_meta.clientInfo")
			return true
		}
	case map[string]any:
		// Defensive: the SDK may have decoded the nested struct into
		// a map if the wire format uses string-keyed JSON. Reconstruct.
		info := mcp.Implementation{
			Name:    stringField(v, "name"),
			Title:   stringField(v, "title"),
			Version: stringField(v, "version"),
		}
		if info.Name != "" {
			s.set(info, "", "_meta.clientInfo")
			return true
		}
	}
	return false
}

// set is the internal mutator. Caller has already extracted the
// Implementation + source; set takes the write lock and stores.
func (s *store) set(info mcp.Implementation, specVersion, source string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = ClientInfoRecord{
		Info:             info,
		Source:           source,
		SpecVersion:      specVersion,
		LastSeenUnixMill: s.now(),
	}
}

// stringField safely extracts a string from a map[string]any, returning
// empty string if the key is missing or the value is not a string.
func stringField(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// CurrentClientInfo is the public accessor the tools use. Returns the
// global singleton's most recent ClientInfoRecord.
func CurrentClientInfo() ClientInfoRecord {
	return globalClientInfoStore().Current()
}

// GlobalStoreForTest returns the global singleton store. Test-only
// helper: production code should use CurrentClientInfo to read the
// current snapshot. Tests use this to inject synthetic clientInfo
// records without having to drive a real initialize handshake.
//
// Example:
//
//	agentbootstrap.GlobalStoreForTest().SetFromInitialize(...)
func GlobalStoreForTest() *store {
	return globalClientInfoStore()
}

// ClearForTest resets the global store to its zero state. Test-only
// helper: production code never needs to clear the store (the most
// recent observation always wins). Tests use this between subtests
// to ensure no leakage from prior tests.
func (s *store) ClearForTest() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = ClientInfoRecord{}
}

// RegisterHooks wires up the OnAfterInitialize hook and the
// MetaPropagator so the store captures clientInfo from both spec
// versions. The returned *Hooks and MetaPropagator are designed to be
// passed to server.NewMCPServer's WithHooks and WithMetaPropagator
// options respectively.
//
// Returns:
//
//   - hooks: a *server.Hooks whose OnAfterInitialize callback stores
//     the legacy-spec clientInfo.
//   - propagator: a server.MetaPropagator impl that watches every
//     per-request _meta bag and stores new-spec clientInfo.
//
// Operators who want to add MORE hooks of their own should compose
// these into their own *server.Hooks rather than overwriting.
//
// This function is safe to call multiple times; each call returns a
// fresh pair that captures into the global store.
func RegisterHooks() (*server.Hooks, *ClientInfoMetaPropagator) {
	hooks := &server.Hooks{}
	hooks.AddAfterInitialize(func(ctx context.Context, id any, msg *mcp.InitializeRequest, result *mcp.InitializeResult) {
		globalClientInfoStore().SetFromInitialize(msg)
	})
	return hooks, &ClientInfoMetaPropagator{}
}

// ClientInfoMetaPropagator is a server.MetaPropagator that stores
// clientInfo found in the _meta bag. Implements the
// server.tracing.MetaPropagator interface.
type ClientInfoMetaPropagator struct{}

// ExtractMeta is called by the server on every inbound request that
// has a non-nil _meta. We pass-through the meta unchanged (no
// trace-context extraction) and side-effect: write any clientInfo
// key into the global store.
//
// The pass-through contract matters: trace-context propagators may be
// installed too, and ours must not consume or rewrite _meta fields it
// doesn't understand. Returning the meta as-is is the safe default.
func (p *ClientInfoMetaPropagator) ExtractMeta(ctx context.Context, meta *mcp.Meta) context.Context {
	globalClientInfoStore().SetFromMeta(meta)
	return ctx
}

// InjectMeta is the outbound counterpart. We don't inject anything
// (the server doesn't need to add clientInfo to its outbound _meta;
// the server's own serverInfo is set in InitializeResult).
func (p *ClientInfoMetaPropagator) InjectMeta(ctx context.Context, meta *mcp.Meta) *mcp.Meta {
	_ = ctx
	return meta
}

// NormalizeClientName maps known harness naming variants to a canonical
// short name used by the companion-recommendation logic. Returns
// "unknown" if the name doesn't match a known harness.
//
// Recognized canonical names:
//
//   - claude-desktop
//   - claude-code
//   - opencode
//   - cline
//   - cursor
//   - continue
//
// String match is case-insensitive on the Name field. We also check
// Title and Version substrings as secondary signals for clients whose
// Name is a generic SDK default.
func NormalizeClientName(info mcp.Implementation) string {
	if info.Name == "" && info.Title == "" {
		return "unknown"
	}
	candidates := []string{
		strings.ToLower(info.Name),
		strings.ToLower(info.Title),
		strings.ToLower(info.Version),
	}
	known := []string{
		"claude-desktop", "claude-code", "claude",
		"opencode", "cline", "roo-cline",
		"cursor", "continue",
	}
	for _, c := range candidates {
		for _, k := range known {
			if strings.Contains(c, k) {
				if k == "claude" {
					// Disambiguate the bare "claude" prefix.
					if strings.Contains(c, "desktop") {
						return "claude-desktop"
					}
					if strings.Contains(c, "code") {
						return "claude-code"
					}
					return "claude-family"
				}
				if k == "roo-cline" {
					return "cline"
				}
				return k
			}
		}
	}
	return "unknown"
}

// unixMillisNow returns the current wall-clock time in milliseconds
// since the Unix epoch. Injected into store.now so tests can supply a
// deterministic clock.
func unixMillisNow() int64 {
	return time.Now().UnixMilli()
}
