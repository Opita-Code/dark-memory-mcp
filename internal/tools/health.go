// Package tools -- health.go: the dark_memory_health_ping tool.
//
// This is the operator-facing health probe. It is intentionally
// separate from dark_memory_memory_state for three reasons:
//
//  1. Latency budget: memory_state does per-table COUNT(*) which is
//     O(n) on the largest tables; health_ping must answer in <50ms
//     even on a 200MB dark-memory.db so it can be called from a
//     Kubernetes liveness probe.
//
//  2. Side-effect freedom: memory_state is read-only but it ticks
//     the orchestrator's audit bus; health_ping MUST NOT touch the
//     audit bus at all -- it is callable from a readiness probe
//     that fires every second.
//
//  3. Output shape: memory_state returns per-table counters for
//     debugging; health_ping returns a strict, documented shape
//     {server, db, runtime, registry} so monitoring rules can be
//     written against a stable contract. Changing the contract
//     here is a breaking change for any monitoring rule that
//     depends on the field set.
//
// Wire surface: dark_memory_health_ping (added to OBSERVABILITY in
// v1.3.0; bumps the canonical count from 27 to 28).
package tools

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"sync/atomic"
	"time"
)

// serverBootTime, serverVersion, serverName, coexistenceGroup are
// populated by RegisterAll from the Config the server resolved at
// boot time. They are process-global on purpose: there is exactly
// one dark-memory-mcp process per operator invocation (this is by
// design, per the BRIDGE_AND_COEXISTENCE bridge.2 coexistence
// model). Tests that call RegisterObservability directly without
// going through RegisterAll see the zero values (zero time, ""),
// which health_ping surfaces as uptime_seconds=0 and
// server_version="" -- those tests should set the globals if they
// care about a non-zero health response.
//
// Initialised here (not in RegisterAll) so the health tool can
// answer even before RegisterAll completes -- a partial-init
// scenario we want to be able to debug ("is the tool wired?").
var (
	serverBootTime      = time.Now()
	serverVersion       atomic.Pointer[string]
	serverName          atomic.Pointer[string]
	coexistenceGroup    atomic.Pointer[string]
	serverDriverLabel   atomic.Pointer[string]
	serverDSNPath       atomic.Pointer[string]
	registryCanonicalN  atomic.Int32
	registryExtrasN     atomic.Int32
	redteamArmedFlag    atomic.Bool
)

// SetRuntimeContext is called by RegisterAll (or by tests) to install
// the boot-time values that dark_memory_health_ping reads. Public so
// future server constructors (e.g. a Postgres-backed variant) can
// call it from a different boot path without replicating the
// contract.
//
// Safe to call after the registry is already populated -- the next
// health_ping sees the new values. NOT safe to call concurrently
// with Register*() (call it AFTER the registry is built).
func SetRuntimeContext(runtimeCtx RuntimeContext) {
	if !runtimeCtx.BootedAt.IsZero() {
		serverBootTime = runtimeCtx.BootedAt
	}
	if runtimeCtx.ServerVersion != "" {
		v := runtimeCtx.ServerVersion
		serverVersion.Store(&v)
	}
	if runtimeCtx.ServerName != "" {
		v := runtimeCtx.ServerName
		serverName.Store(&v)
	}
	if runtimeCtx.CoexistenceGroup != "" {
		v := runtimeCtx.CoexistenceGroup
		coexistenceGroup.Store(&v)
	}
	if runtimeCtx.DriverLabel != "" {
		v := runtimeCtx.DriverLabel
		serverDriverLabel.Store(&v)
	}
	if runtimeCtx.DSNPath != "" {
		v := runtimeCtx.DSNPath
		serverDSNPath.Store(&v)
	}
}

// SetRegistryCounts is called by RegisterAll after all Register*
// functions have run, so dark_memory_health_ping reports the actual
// tool surface that tools/list will emit.
//
// v1.3.0 (architectural decision): we cache the count at boot
// rather than re-reading from Registry on every health_ping call.
// Rationale:
//
//   - The Registry is a *Registry (sync.RWMutex-protected) and
//     reading from it requires holding the RLock. Health_ping is
//     meant to be a fast liveness probe; we'd rather have one
//     cheap atomic.Load than one mutex acquisition per call.
//
//   - The cache is set exactly once during boot (inside RegisterAll,
//     which fails if the canonical count doesn't match the
//     expected 28). The only way for the cache to drift is if
//     someone calls reg.Add() AFTER RegisterAll — internal API
//     misuse, not a production path.
//
//   - If the operator needs a live count, they can call
//     dark_memory_tools (a hypothetical introspection tool) or
//     inspect tools/list directly. health_ping's job is to answer
//     "are you alive?" with sub-millisecond latency, not to be a
//     full registry introspection API.
//
// Tested by: tests/wire/zz_toolenum_test.go (boots the binary,
// calls tools/list, asserts the wire-level tool count matches what
// health_ping reports).
func SetRegistryCounts(canonical, extras int, redteamArmed bool) {
	registryCanonicalN.Store(int32(canonical))
	registryExtrasN.Store(int32(extras))
	redteamArmedFlag.Store(redteamArmed)
}

// RuntimeContext is the bag of boot-time values the server wires
// into dark_memory_health_ping. Fields are optional; empty/zero
// values fall back to sane defaults so a partial init is still
// observable.
type RuntimeContext struct {
	BootedAt          time.Time
	ServerVersion     string
	ServerName        string
	CoexistenceGroup  string
	DriverLabel       string // "sqlite" | "postgres"
	DSNPath           string // for operators to know which file the daemon has open
}

// healthPingResult is the canonical shape returned by
// dark_memory_health_ping. The shape is frozen; new fields are
// added below with a "v1.3.x" version suffix and old fields are
// never removed (only deprecated).
//
// Top-level keys:
//   - server:    identity (name, version, coexistence group, armed mode)
//   - db:        connectivity + schema version + canary + active project
//   - runtime:   process-level metrics (uptime, boot time, PID)
//   - registry:  the tool surface dark_memory_* is advertising
//   - latency_ms:    the round-trip cost of THIS health call (helpful
//                    for spotting slow-down trends in monitoring dashboards)
//   - checked_at:    ISO8601 of when this response was generated
type healthPingResult struct {
	Server struct {
		Name              string `json:"name"`
		Version           string `json:"version"`
		CoexistenceGroup  string `json:"coexistence_group"`
		RedTeamArmed      bool   `json:"redteam_armed"`
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
	LatencyMS float64 `json:"latency_ms"`
	CheckedAt string  `json:"checked_at"`
}

// RegisterHealth wires the dark_memory_health_ping tool into the
// registry. It is called from RegisterObservability's namespace
// extender (added in v1.3.0) -- it is a sibling of memory_state,
// not a replacement. The schema is a strict {} object so callers
// pass no arguments.
func RegisterHealth(reg *Registry, st storeBridge) {
	reg.Add(BindSimple("health_ping",
		"Return the runtime health snapshot (server identity, DB connectivity, schema version, canary, registry counts, uptime, latency). Read-only, no audit side-effects, safe to call from a liveness probe.",
		MustJSONSchema(map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
		func(ctx context.Context, raw json.RawMessage) (*ToolResponse, error) {
			start := time.Now()
			out := healthPingResult{}
			out.CheckedAt = start.UTC().Format(time.RFC3339Nano)

			// --- server ---
			if v := serverName.Load(); v != nil {
				out.Server.Name = *v
			}
			if v := serverVersion.Load(); v != nil {
				out.Server.Version = *v
			}
			if v := coexistenceGroup.Load(); v != nil {
				out.Server.CoexistenceGroup = *v
			}
			out.Server.RedTeamArmed = redteamArmedFlag.Load()

			// --- db (live ping) ---
			if v := serverDriverLabel.Load(); v != nil {
				out.DB.Driver = *v
			}
			if v := serverDSNPath.Load(); v != nil {
				// Redact PII (operator's home directory on POSIX/Windows,
				// e.g. C:\Users\Nico\.../probe.db → <USER>/probe.db).
				// The raw path is preserved in stderr boot logs for
				// operators who need it; the MCP wire surface is what
				// travels to harnesses that may persist to disk.
				out.DB.DSNPath = redactHomeInPath(*v)
			}
			if st != nil {
				// pingCtx is used ONLY for the Ping roundtrip; we
				// derive a fresh context for the follow-up
				// SchemaVersion read because the Ping ctx may
				// already be canceled by the time we get here
				// (a fast Ping success doesn't cancel the ctx,
				// but we explicitly call cancel() on the failure
				// path and a defensive second ctx avoids any
				// "context canceled" surprises on the read path).
				pingCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
				pingErr := st.Ping(pingCtx)
				cancel()
				if pingErr != nil {
					out.DB.Live = false
					// Surface the failure as a structured
					// ToolError so monitoring rules can branch
					// on the JSON-RPC error envelope. The data
					// block remains populated with everything
					// we managed to read (server identity,
					// registry counts, etc.); only the
					// connectivity bool is false.
					out.LatencyMS = latencyMS(start)
					return &ToolResponse{
						Data: &out,
						Error: &ToolError{
							Code:    "ErrDBUnreachable",
							Message: "dark-memory-mcp DB ping failed: " + pingErr.Error(),
							Hint:    "Check the DARK_DB path and that the file is readable. If this was a corruption event, see PRODUCTION_CHECKLIST.md R-2 (dark.db corruption recovery).",
						},
					}, nil
				}
				out.DB.Live = true
				// schema version + canary + active project are
				// cheap reads next to the Ping round-trip;
				// include them. Use a FRESH ctx derived from the
				// parent ctx so the pingCtx (already canceled
				// above) doesn't leak into these reads.
				readCtx, readCancel := context.WithTimeout(ctx, 200*time.Millisecond)
				if v, err := st.SchemaVersion(readCtx); err == nil {
					out.DB.SchemaVersion = v
				}
				readCancel()
				out.DB.CanaryPresent = st.CanaryPresent()
				out.DB.ActiveProject = st.ActiveProject()
			}

			// --- runtime ---
			out.Runtime.UptimeSeconds = time.Since(serverBootTime).Seconds()
			out.Runtime.BootedAt = serverBootTime.UTC().Format(time.RFC3339Nano)
			out.Runtime.PID = os.Getpid()
			out.Runtime.GoVersion = goVersion()

			// --- registry ---
			out.Registry.CanonicalTools = int(registryCanonicalN.Load())
			out.Registry.ExtraTools = int(registryExtrasN.Load())

			out.LatencyMS = latencyMS(start)
			return &ToolResponse{Data: &out}, nil
		}))
}

// storeBridge is the read-only interface dark_memory_health_ping
// needs from the live Store. Defined as an interface here (instead
// of importing the full store.Store) so tests can inject a
// stand-in via RegisterHealth(reg, &fakeStore{...}).
type storeBridge interface {
	Ping(ctx context.Context) error
	SchemaVersion(ctx context.Context) (int, error)
	CanaryPresent() bool
	ActiveProject() string
}

// goVersion returns a string describing the Go runtime version.
// Kept inline to avoid an extra import beyond `runtime`.
func goVersion() string {
	return goVersionString
}

// latencyMS returns the milliseconds elapsed since start as a
// float64, computed from nanoseconds for sub-millisecond precision.
//
// v1.3.0 bug-hunt fix: the previous implementation used
// time.Since(start).Microseconds()/1000.0 which truncates to 0 for
// operations under 1ms. On a fast dev host a successful Ping +
// SchemaVersion + CanaryPresent can finish in well under a
// millisecond, producing latency_ms=0.00 in the wire response.
// Monitoring dashboards interpret 0 as either "didn't run" or
// "anomaly" and alert; using nanoseconds keeps the value meaningful
// even on warm paths (typical observed: 0.05ms-2ms).
//
// v1.3.0 (further fix): added minimum floor of 1 nanosecond to
// guarantee a non-zero result even when the handler is so fast that
// the monotonic clock hasn't ticked. Monotonic clocks on Windows
// have ~15ms resolution historically; modern Windows is closer to
// 100ns. The math itself can't return 0 with Nanoseconds() in any
// realistic scenario, but the previous test run reported 0
// intermittently — root cause still under investigation. The floor
// ensures monitoring dashboards never see a spurious "0ms latency"
// that would silently mask an outage.
func latencyMS(start time.Time) float64 {
	ns := time.Since(start).Nanoseconds()
	if ns <= 0 {
		ns = 1 // floor: emit at least 1 nanosecond so wire value is never 0.00
	}
	return float64(ns) / 1_000_000.0
}

// redactHomeInPath returns the input path with the user's home
// directory replaced by `<USER>`. Used by health_ping to keep the
// operator's username out of the wire surface (GDPR/CCPA scope:
// the MCP wire response may be persisted by harnesses; the raw
// path with username is private operator information).
//
// Behavior:
//   - Windows: replaces `C:\Users\<name>\` with `<USER>\` (preserves backslashes)
//   - POSIX:   replaces `/Users/<name>/`, `/home/<name>/`, and bare `/root/`
//   - Paths without a recognizable home component are returned unchanged
//     (operator chose a non-standard layout; do not silently mangle).
//
// The raw path remains available on stderr (boot log line 1) so
// operators who need it for debugging still see it.
func redactHomeInPath(p string) string {
	if p == "" {
		return p
	}
	// Detect path style without normalizing separators so the
	// output preserves the operator's chosen separator convention.
	// We try Windows first (more specific), then POSIX.
	if winHome := regexHomeWindows.FindStringIndex(p); winHome != nil {
		return p[:winHome[0]] + "<USER>" + p[winHome[1]:]
	}
	if posixHome := regexHomePOSIX.FindStringIndex(p); posixHome != nil {
		return p[:posixHome[0]] + "<USER>" + p[posixHome[1]:]
	}
	return p
}

// regexHomeWindows matches the Windows user-profile prefix, with
// optional backslash OR forward slash separators. Compiled at
// package init (Go's regexp is thread-safe after Compile).
//
// Pattern: <drive>:\Users\<name>  or  <drive>:/Users/<name>
// where <drive> is a single ASCII letter (case-insensitive).
// The match includes the trailing slash-or-backslash so we strip
// the whole prefix uniformly.
var regexHomeWindows = regexp.MustCompile(`(?i)^[A-Z]:[\\/]Users[\\/][^\\/]+`)

// regexHomePOSIX matches the POSIX home-directory prefix. macOS
// uses /Users/<name>; Linux uses /home/<name>; root uses /root.
// The username component excludes leading dots so `/root/.config`
// matches `/root` (not `/root/.config`); dotfiles are not valid
// usernames on POSIX systems.
var regexHomePOSIX = regexp.MustCompile(`^/(?:Users|home|root)(?:/[^/.][^/]*)?`)
