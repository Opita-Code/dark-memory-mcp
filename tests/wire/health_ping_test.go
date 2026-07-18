package wire

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestWire_HealthPingShape verifies the dark_memory_health_ping tool
// returns the documented contract over the live JSON-RPC wire.
//
// v1.3.0: this is the operator-facing liveness probe; it must answer
// in <500ms with the documented {server, db, runtime, registry,
// latency_ms, checked_at} shape, regardless of armed mode.
//
// IMPORTANT: mcp-go's stdio transport wraps every tool response in a
// `content: [{type:"text", text:"<ToolResponse JSON>"}]` envelope
// (the JSON-RPC spec for tools/call results). The harness here
// unwraps content[0].text and then parses the inner ToolResponse.
func TestWire_HealthPingShape(t *testing.T) {
	if os.Getenv("DARK_MEM_MCP_BIN") == "" {
		t.Skip("DARK_MEM_MCP_BIN not set; wire tests need the live binary")
	}

	s := startWireSession(t)

	respBytes, err := s.toolsCall("dark_memory_health_ping", map[string]any{})
	if err != nil {
		t.Fatalf("health_ping call: %v", err)
	}
	inner, err := unwrapToolResponse(t, respBytes)
	if err != nil {
		t.Fatalf("unwrap content[0].text: %v", err)
	}
	// inner is a JSON ToolResponse: {"data": <healthWireShape>, "audit":...}.
	// We need to drill into `.data` to get the typed result. The simplest
	// way: unmarshal into a tiny struct that exposes just `.data` as
	// json.RawMessage, then unmarshal that into healthWireShape.
	var respEnvelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(inner, &respEnvelope); err != nil {
		t.Fatalf("ToolResponse unmarshal: %v\n  body=%s", err, inner)
	}
	var got healthWireShape
	if err := json.Unmarshal(respEnvelope.Data, &got); err != nil {
		t.Fatalf("health_ping ToolResponse.data not healthWireShape: %v\n  body=%s", err, respEnvelope.Data)
	}

	// Contract checks (RFC §C-2 Health Probe).
	if got.Server.Name == "" {
		t.Errorf("server.name empty; want non-empty (the resolved DARK_SERVER_NAME)")
	}
	if got.Server.Version == "" {
		t.Errorf("server.version empty; want non-empty (DARK_SERVER_VERSION or DefaultServerVersion)")
	}
	if got.Server.CoexistenceGroup == "" {
		t.Errorf("server.coexistence_group empty; want 'dark-agents/memory'")
	}
	if got.DB.Driver != "sqlite" {
		t.Errorf("db.driver=%q; want 'sqlite' (this host has no postgres)", got.DB.Driver)
	}
	if !got.DB.Live {
		t.Errorf("db.live=false; the daemon just booted against a fresh per-test dark-memory.db; DB must be live")
	}
	if got.Runtime.UptimeSeconds < 0 {
		t.Errorf("runtime.uptime_seconds=%v negative", got.Runtime.UptimeSeconds)
	}
	if got.Runtime.BootedAt == "" {
		t.Errorf("runtime.booted_at empty; want RFC3339Nano")
	}
	if _, err := time.Parse(time.RFC3339Nano, got.Runtime.BootedAt); err != nil {
		t.Errorf("runtime.booted_at %q not RFC3339Nano: %v", got.Runtime.BootedAt, err)
	}
	if got.Runtime.PID <= 0 {
		t.Errorf("runtime.pid=%d; want >0", got.Runtime.PID)
	}
	if got.Registry.CanonicalTools != 28 {
		t.Errorf("registry.canonical_tools=%d; want 28 (v1.3.0 contract)", got.Registry.CanonicalTools)
	}
	if got.LatencyMS <= 0 || got.LatencyMS > 5000 {
		t.Errorf("latency_ms=%v; want >0 and <=5000ms (this is a liveness probe, not a benchmark)", got.LatencyMS)
	}
	if got.CheckedAt == "" {
		t.Errorf("checked_at empty")
	}

	// v1.4.0 (DARK-MEM-003): git block + drift. The `source` field
	// is always one of {ldflags, buildinfo, dev} per the resolver
	// contract. The `drift` field is true iff the build resolved via
	// the dev fallback OR the working tree was dirty at build time.
	// A CI release build should report drift=false; a dev build
	// (this test running locally) reports drift=true. Both are valid.
	switch got.Git.Source {
	case "ldflags", "buildinfo", "dev":
		// ok
	default:
		t.Errorf("git.source=%q; want one of ldflags|buildinfo|dev", got.Git.Source)
	}
	if got.Drift != (got.Git.IsDev || got.Git.Dirty) {
		t.Errorf("drift=%v != IsDev||Dirty (%v)", got.Drift, got.Git.IsDev || got.Git.Dirty)
	}

	// v1.3.0 (bug-hunt polish): GDPR/CCPA scope. The wire response
	// may be persisted by harnesses; the operator's username must
	// not appear verbatim. The redacted form keeps the path layout
	// (e.g. `<USER>\AppData\Local\Temp\probe.db`) so monitoring
	// rules can still distinguish per-host files, but the
	// personally-identifiable `<USER>` token replaces the actual
	// directory name. Raw path remains on stderr for debugging.
	//
	// We assert against the operator's actual env var $USERNAME
	// (Windows) or $USER (POSIX); if neither is set, we assert
	// only that `<USER>` appears in the redacted form.
	if os.Getenv("DARK_MEM_MCP_BIN_PII_REDACTION") != "skip" {
		operator := os.Getenv("USERNAME")
		if operator == "" {
			operator = os.Getenv("USER")
		}
		if operator != "" && strings.Contains(got.DB.DSNPath, operator) {
			t.Errorf("PII leak: db.dsn_path contains operator username %q; want redacted. dsn_path=%q", operator, got.DB.DSNPath)
		}
		// v1.4.1 (fix #2 of 2 follow-ups to v1.4.1 release): the
		// previous assertion `strings.Contains(dsn_path, "<USER>")`
		// was over-strict. redactHomeInPath only fires for paths
		// under the operator's $HOME (Windows: C:\Users\<name>\,
		// POSIX: /home/<name>/ or ~). It does NOT redact paths under
		// /tmp/, C:\Temp\, or any other location that isn't the
		// user's home directory.
		//
		// In CI, the wire test uses t.TempDir() which resolves to
		// /tmp/TestWire_HealthPingShape<id>/001/dark-memory.db —
		// outside any user home, so redaction never fires, so the
		// <USER> MUST-contain assertion fails.
		//
		// The meaningful PII guarantee is the MUST-NOT-contain
		// check above: the operator's literal username must never
		// appear in the wire response. That check still runs and
		// still protects the operator. The MUST-contain check
		// asserted an *implementation detail* of redactHomeInPath,
		// not a security property, so we drop it.
		//
		// PR #9.
	}

	fmt.Fprintf(os.Stderr,
		"=== health_ping: server=%s v=%s db.live=%v schema=v%d up=%.3fs canonical=%d extras=%d latency=%.2fms ===\n",
		got.Server.Name, got.Server.Version, got.DB.Live, got.DB.SchemaVersion,
		got.Runtime.UptimeSeconds, got.Registry.CanonicalTools, got.Registry.ExtraTools, got.LatencyMS,
	)
}

// TestWire_HealthPingLatency budgets the health tool at <500ms round-trip
// from a freshly-booted binary. Detects accidental perf regressions where
// someone adds a row scan / blocking call to the hot path.
func TestWire_HealthPingLatency(t *testing.T) {
	if os.Getenv("DARK_MEM_MCP_BIN") == "" {
		t.Skip("DARK_MEM_MCP_BIN not set")
	}
	s := startWireSession(t)

	// Warm up once (DB connection init, canary constructor, etc.).
	if _, err := s.toolsCall("dark_memory_health_ping", map[string]any{}); err != nil {
		t.Fatalf("warm: %v", err)
	}

	// Measure 5 sequential calls; the worst one is the noise floor.
	worst := time.Duration(0)
	for i := 0; i < 5; i++ {
		t0 := time.Now()
		respBytes, err := s.toolsCall("dark_memory_health_ping", map[string]any{})
		if err != nil {
			t.Fatalf("warm call %d: %v", i, err)
		}
		if !strings.Contains(string(respBytes), `"content"`) {
			t.Fatalf("warm call %d no content envelope: %s", i, respBytes)
		}
		d := time.Since(t0)
		if d > worst {
			worst = d
		}
	}

	// 500ms is a comfortable ceiling on this dev host; production
	// should target <50ms but we keep 500ms as the wire-test go/no-go
	// so a CI runner under load doesn't flake.
	const ceiling = 500 * time.Millisecond
	if worst > ceiling {
		t.Errorf("worst-of-5 health_ping roundtrip = %v; want < %v", worst, ceiling)
	}
	fmt.Fprintf(os.Stderr, "=== health_ping worst-of-5 roundtrip = %v (< %v budget) ===\n", worst, ceiling)
}

// unwrapToolResponse pulls the inner ToolResponse JSON out of the
// mcp-go content envelope. Moved to envelope.go (shared helper) in
// v1.3.0; this file keeps the unused-named alias for callers that
// imported it directly. (Removed in v1.3.0 — see envelope.go.)

// unwrapToolResponse: see envelope.go (v1.3.0: extracted from this
// file so F35, F36, and health_ping share a single implementation).

// healthWireShape is the wire-format shape of the dark_memory_health_ping
// response. Mirrors tools.healthPingResult but with json tags the wire
// decoder populates. Kept separate so the tools package can evolve its
// internal struct without breaking this frozen contract test.
//
// v1.4.0 (DARK-MEM-003): the contract grew by a `git` block and a
// top-level `drift` bool. Per CONSTITUTION.md Rule 4, the `git` block
// is the resolver's view of the build provenance (tag, commit, dirty,
// build_time, source, is_dev) and `drift` is a single-bit liveness
// signal that the operator can monitor.
type healthWireShape struct {
	Server struct {
		Name             string `json:"name"`
		Version          string `json:"version"`
		CoexistenceGroup string `json:"coexistence_group"`
		RedTeamArmed     bool   `json:"redteam_armed"`
	} `json:"server"`
	DB struct {
		Driver        string `json:"driver"`
		DSNPath       string `json:"dsn_path,omitempty"`
		Live          bool   `json:"live"`
		SchemaVersion int    `json:"schema_version"`
		CanaryPresent bool   `json:"canary_present"`
		ActiveProject string `json:"active_project,omitempty"`
	} `json:"db"`
	Runtime struct {
		UptimeSeconds float64 `json:"uptime_seconds"`
		BootedAt      string  `json:"booted_at"`
		PID           int     `json:"pid"`
		GoVersion     string  `json:"go_version"`
	} `json:"runtime"`
	Registry struct {
		CanonicalTools int `json:"canonical_tools"`
		ExtraTools     int `json:"extra_tools"`
	} `json:"registry"`
	Git struct {
		Tag       string `json:"tag,omitempty"`
		Commit    string `json:"commit,omitempty"`
		Dirty     bool   `json:"dirty,omitempty"`
		BuildTime string `json:"build_time,omitempty"`
		Source    string `json:"source"`
		IsDev     bool   `json:"is_dev,omitempty"`
	} `json:"git"`
	Drift     bool    `json:"drift,omitempty"`
	LatencyMS float64 `json:"latency_ms"`
	CheckedAt string  `json:"checked_at"`
}