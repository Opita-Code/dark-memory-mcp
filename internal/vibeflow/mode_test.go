package vibeflow

import (
	"fmt"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// fixedNow returns a function that always returns the same time. Useful
// for deterministic idle tests.
func fixedNow() func() time.Time {
	t := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

// advancingNow returns a function that returns the base time + offset
// seconds each call. offset increments by 1 per call.
func advancingNow() (func() time.Time, *int) {
	base := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	calls := 0
	now := func() time.Time {
		calls++
		return base.Add(time.Duration(calls) * time.Second)
	}
	return now, &calls
}

// ---------------------------------------------------------------------------
// Mode / Signal / Tier basics
// ---------------------------------------------------------------------------

func TestMode_String(t *testing.T) {
	cases := []struct {
		m    Mode
		want string
	}{
		{ModeOff, "off"},
		{ModeDegraded, "degraded"},
		{ModeFull, "full"},
		{Mode(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.m.String(); got != c.want {
			t.Errorf("Mode(%d).String() = %q, want %q", c.m, got, c.want)
		}
	}
}

func TestSignal_String(t *testing.T) {
	cases := []struct {
		s    Signal
		want string
	}{
		{SignalNone, "none"},
		{SignalTime, "time"},
		{SignalToolCalls, "tool_calls"},
		{SignalFollowup, "followup"},
		{SignalDoubt, "doubt"},
		{SignalTokens, "tokens"},
		{SignalReset, "reset"},
		{Signal(99), "none"},
	}
	for _, c := range cases {
		if got := c.s.String(); got != c.want {
			t.Errorf("Signal(%d).String() = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestDefaultModeFor(t *testing.T) {
	cases := []struct {
		name string
		ctx  Context
		want Mode
	}{
		{"novice", Context{Tier: TierNovice}, ModeOff},
		{"intermediate", Context{Tier: TierIntermediate}, ModeDegraded},
		{"expert", Context{Tier: TierExpert}, ModeFull},
		{"unknown-tier", Context{Tier: Tier("weird")}, ModeDegraded},
		{"rush-novice", Context{Tier: TierNovice, RushMode: true}, ModeOff},
		{"rush-expert", Context{Tier: TierExpert, RushMode: true}, ModeOff}, // RushMode wins
		{"rush-intermediate", Context{Tier: TierIntermediate, RushMode: true}, ModeOff},
	}
	for _, c := range cases {
		if got := DefaultModeFor(c.ctx); got != c.want {
			t.Errorf("DefaultModeFor(%s) = %q, want %q", c.name, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Default-by-tier
// ---------------------------------------------------------------------------

func TestMode_Default_ForNovice(t *testing.T) {
	m := NewModeManager(Context{Tier: TierNovice}, fixedNow())
	if got := m.Current(); got != ModeOff {
		t.Errorf("novice default = %q, want off", got)
	}
}

func TestMode_Default_ForIntermediate(t *testing.T) {
	m := NewModeManager(Context{Tier: TierIntermediate}, fixedNow())
	if got := m.Current(); got != ModeDegraded {
		t.Errorf("intermediate default = %q, want degraded", got)
	}
}

func TestMode_Default_ForExpert(t *testing.T) {
	m := NewModeManager(Context{Tier: TierExpert}, fixedNow())
	if got := m.Current(); got != ModeFull {
		t.Errorf("expert default = %q, want full", got)
	}
}

func TestMode_CostLens_HalfPersonasDefaultOff(t *testing.T) {
	// The 10 personas mapped to (tier, rush, domain) per the L8 design.
	// 6 should default to off (RushMode wins for the 3 rush personas;
	// TierNovice wins for the 3 non-rush novices), 1 to degraded
	// (multi-idioma, intermediate, non-rush), 3 to full (experts).
	personas := []struct {
		name string
		ctx  Context
		want Mode
	}{
		{"tactico", Context{Tier: TierNovice, RushMode: true}, ModeOff},
		{"panic-debugger", Context{Tier: TierIntermediate, RushMode: true}, ModeOff}, // RushMode wins
		{"novato", Context{Tier: TierNovice}, ModeOff},
		{"no-tecnico", Context{Tier: TierNovice}, ModeOff},
		{"movil", Context{Tier: TierNovice}, ModeOff},
		{"rush-cognitivo", Context{Tier: TierNovice, RushMode: true}, ModeOff},
		{"multi-idioma", Context{Tier: TierIntermediate}, ModeDegraded},
		{"arquitecto", Context{Tier: TierExpert, Domain: DomainCode}, ModeFull},
		{"investigador", Context{Tier: TierExpert, Domain: DomainEducation}, ModeFull},
		{"escritor", Context{Tier: TierExpert, Domain: DomainMarketing}, ModeFull},
	}
	offCount := 0
	for _, p := range personas {
		m := NewModeManager(p.ctx, fixedNow())
		if got := m.Current(); got != p.want {
			t.Errorf("persona %q default = %q, want %q", p.name, got, p.want)
		}
		if got := m.Current(); got == ModeOff {
			offCount++
		}
	}
	if offCount != 6 {
		t.Errorf("expected 6/10 personas in off, got %d", offCount)
	}
}

// ---------------------------------------------------------------------------
// Off → Degraded escalation
// ---------------------------------------------------------------------------

func TestMode_Escalate_OffToDegraded_OnDoubt(t *testing.T) {
	m := NewModeManager(Context{Tier: TierNovice}, fixedNow())
	// 1 doubt (threshold for novice is 1)
	m.Apply(SignalDoubt, 1)
	if got := m.Current(); got != ModeDegraded {
		t.Errorf("after 1 doubt, novice mode = %q, want degraded", got)
	}
}

func TestMode_Escalate_OffToDegraded_OnFollowup(t *testing.T) {
	m := NewModeManager(Context{Tier: TierNovice}, fixedNow())
	m.Apply(SignalFollowup, 1)
	if got := m.Current(); got != ModeDegraded {
		t.Errorf("after 1 followup, novice mode = %q, want degraded", got)
	}
}

func TestMode_Escalate_OffToDegraded_OnTimeAndToolCalls(t *testing.T) {
	m := NewModeManager(Context{Tier: TierNovice}, fixedNow())
	// Below thresholds: 60s, 2 tools → stay off
	m.Apply(SignalTime, 60)
	m.Apply(SignalToolCalls, 2)
	if got := m.Current(); got != ModeOff {
		t.Errorf("60s + 2 tools, novice = %q, want off (need both thresholds)", got)
	}
	// Cross both thresholds: 65s, 3 tools → escalate
	m.Apply(SignalTime, 65)
	m.Apply(SignalToolCalls, 3)
	if got := m.Current(); got != ModeDegraded {
		t.Errorf("65s + 3 tools, novice = %q, want degraded", got)
	}
}

func TestMode_Stays_Off_WhenNoSignals(t *testing.T) {
	m := NewModeManager(Context{Tier: TierNovice}, fixedNow())
	// Apply lots of low-impact signals
	m.Apply(SignalTime, 30)
	m.Apply(SignalToolCalls, 2)
	m.Apply(SignalTokens, 1000)
	if got := m.Current(); got != ModeOff {
		t.Errorf("novice with low signals = %q, want off", got)
	}
}

// ---------------------------------------------------------------------------
// Degraded → Full escalation
// ---------------------------------------------------------------------------

func TestMode_Escalate_DegradedToFull_OnSustainedDoubt(t *testing.T) {
	m := NewModeManager(Context{Tier: TierIntermediate}, fixedNow())
	// Default = degraded
	m.Apply(SignalDoubt, 2) // threshold is 2
	if got := m.Current(); got != ModeFull {
		t.Errorf("intermediate with 2 doubts = %q, want full", got)
	}
}

func TestMode_Escalate_DegradedToFull_OnSustainedFollowup(t *testing.T) {
	m := NewModeManager(Context{Tier: TierIntermediate}, fixedNow())
	m.Apply(SignalFollowup, 2)
	if got := m.Current(); got != ModeFull {
		t.Errorf("intermediate with 2 followups = %q, want full", got)
	}
}

func TestMode_Escalate_DegradedToFull_OnTimeAndTools(t *testing.T) {
	m := NewModeManager(Context{Tier: TierIntermediate}, fixedNow())
	m.Apply(SignalTime, 120)
	m.Apply(SignalToolCalls, 6)
	if got := m.Current(); got != ModeFull {
		t.Errorf("intermediate 120s + 6 tools = %q, want full", got)
	}
}

func TestMode_Stays_Degraded_OnSingleDoubt(t *testing.T) {
	m := NewModeManager(Context{Tier: TierIntermediate}, fixedNow())
	m.Apply(SignalDoubt, 1) // threshold is 2 for intermediate
	if got := m.Current(); got != ModeDegraded {
		t.Errorf("intermediate with 1 doubt = %q, want degraded (need 2)", got)
	}
}

// ---------------------------------------------------------------------------
// De-escalation
// ---------------------------------------------------------------------------

func TestMode_DeEscalate_FullToDegraded_OnIdle(t *testing.T) {
	base := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	current := base
	now := func() time.Time { return current }
	// Use TierIntermediate so threshold is 300s (5min) for clarity
	m := NewModeManager(Context{Tier: TierIntermediate}, now)
	if m.Current() != ModeDegraded {
		t.Fatalf("intermediate should start in degraded; need full to test de-escalation")
	}
	// Promote to full via doubts
	m.Apply(SignalDoubt, 2) // threshold for degraded→full is 2
	if m.Current() != ModeFull {
		t.Fatalf("intermediate after 2 doubts should be full, got %q", m.Current())
	}
	// Advance 6 minutes (over 5min default threshold)
	current = base.Add(6 * time.Minute)
	// Bookkeeping tick (SignalTime) doesn't update LastSignal
	m.Apply(SignalTime, 0)
	if got := m.Current(); got != ModeDegraded {
		t.Errorf("after 6min idle = %q, want degraded", got)
	}
}

func TestMode_Stays_Full_WhenActive(t *testing.T) {
	base := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	current := base
	now := func() time.Time { return current }
	m := NewModeManager(Context{Tier: TierExpert}, now)
	// Active: tick every 30s, no long idle
	for i := 0; i < 20; i++ {
		current = base.Add(time.Duration(i*30) * time.Second)
		m.Apply(SignalToolCalls, i+1)
	}
	if got := m.Current(); got != ModeFull {
		t.Errorf("expert with 20 active ticks = %q, want full (no idle)", got)
	}
}

func TestMode_Reset_ReturnsToDefault(t *testing.T) {
	m := NewModeManager(Context{Tier: TierNovice}, fixedNow())
	m.Apply(SignalDoubt, 1) // → degraded
	if m.Current() != ModeDegraded {
		t.Fatalf("setup: should be degraded after 1 doubt")
	}
	m.Apply(SignalReset, 0)
	if got := m.Current(); got != ModeOff {
		t.Errorf("after reset, novice = %q, want off (default)", got)
	}
}

func TestMode_Reset_ClearsCounters(t *testing.T) {
	m := NewModeManager(Context{Tier: TierNovice}, fixedNow())
	m.Apply(SignalToolCalls, 5)
	m.Apply(SignalDoubt, 2)
	m.Apply(SignalReset, 0)
	s := m.State()
	if s.ToolCalls != 0 || s.Doubts != 0 || s.Followups != 0 {
		t.Errorf("after reset, counters not zero: %+v", s)
	}
}

// ---------------------------------------------------------------------------
// Per-tier calibration
// ---------------------------------------------------------------------------

func TestMode_NoviceEscalatesFaster(t *testing.T) {
	// Novice: 1 doubt = off→degraded, 1 doubt = degraded→full
	m := NewModeManager(Context{Tier: TierNovice}, fixedNow())
	m.Apply(SignalDoubt, 1)
	if m.Current() != ModeDegraded {
		t.Fatalf("novice: 1 doubt should = degraded")
	}
	m.Apply(SignalDoubt, 2) // threshold for degraded→full is 1 for novice
	if got := m.Current(); got != ModeFull {
		t.Errorf("novice: 2 doubts = %q, want full (threshold=1 for novice)", got)
	}
}

func TestMode_ExpertStaysFullLonger(t *testing.T) {
	base := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	current := base
	now := func() time.Time { return current }
	m := NewModeManager(Context{Tier: TierExpert}, now)
	// First real signal at t=0
	m.Apply(SignalToolCalls, 1)
	// 4 minutes idle — bookkeeping tick (SignalTime) doesn't reset
	current = base.Add(4 * time.Minute)
	m.Apply(SignalTime, 0)
	if got := m.Current(); got != ModeFull {
		t.Errorf("expert after 4min idle = %q, want full (expert idle=10min)", got)
	}
	// 6 minutes idle
	current = base.Add(6 * time.Minute)
	m.Apply(SignalTime, 0)
	if got := m.Current(); got != ModeFull {
		t.Errorf("expert after 6min idle = %q, want full", got)
	}
	// 11 minutes idle — over expert's 600s threshold
	current = base.Add(11 * time.Minute)
	m.Apply(SignalTime, 0)
	if got := m.Current(); got != ModeDegraded {
		t.Errorf("expert after 11min idle = %q, want degraded", got)
	}
}

// ---------------------------------------------------------------------------
// OnMessage — keyword extraction
// ---------------------------------------------------------------------------

func TestMode_OnMessage_ExtractsFollowup(t *testing.T) {
	m := NewModeManager(Context{Tier: TierNovice}, fixedNow())
	m.OnMessage("wait, actually I need foo", Features{})
	if got := m.Current(); got != ModeDegraded {
		t.Errorf("after 'wait, actually', novice = %q, want degraded", got)
	}
	if m.State().Followups != 1 {
		t.Errorf("followups = %d, want 1", m.State().Followups)
	}
}

func TestMode_OnMessage_ExtractsDoubt(t *testing.T) {
	m := NewModeManager(Context{Tier: TierNovice}, fixedNow())
	m.OnMessage("are you sure this is right?", Features{})
	if got := m.Current(); got != ModeDegraded {
		t.Errorf("after 'are you sure', novice = %q, want degraded", got)
	}
	if m.State().Doubts != 1 {
		t.Errorf("doubts = %d, want 1", m.State().Doubts)
	}
}

func TestMode_OnMessage_SpanishFollowup(t *testing.T) {
	m := NewModeManager(Context{Tier: TierNovice}, fixedNow())
	m.OnMessage("mejor usa la versión 2", Features{})
	if m.State().Followups < 1 {
		t.Errorf("Spanish 'mejor' should count as followup, got %d", m.State().Followups)
	}
}

func TestMode_OnMessage_SpanishDoubt(t *testing.T) {
	m := NewModeManager(Context{Tier: TierNovice}, fixedNow())
	m.OnMessage("verifica que esto esté bien", Features{})
	if m.State().Doubts < 1 {
		t.Errorf("Spanish 'verifica' should count as doubt, got %d", m.State().Doubts)
	}
}

func TestMode_OnMessage_PlainText_NoSignals(t *testing.T) {
	m := NewModeManager(Context{Tier: TierNovice}, fixedNow())
	m.OnMessage("please fix the typo in foo.go", Features{})
	if m.State().Doubts != 0 || m.State().Followups != 0 {
		t.Errorf("plain text: doubts=%d followups=%d, want 0/0", m.State().Doubts, m.State().Followups)
	}
}

// ---------------------------------------------------------------------------
// PanicMod (RushMode + PanicKeywords)
// ---------------------------------------------------------------------------

func TestMode_PanicMod_SuppressesDoubtEscalation(t *testing.T) {
	// RushMode + panic keywords should NOT escalate
	m := NewModeManager(Context{Tier: TierNovice, RushMode: true}, fixedNow())
	// "fix NOW" without RushMode would escalate
	m.OnMessage("fix NOW this is broken URGENT", Features{PanicKeywords: true})
	if got := m.Current(); got != ModeOff {
		t.Errorf("rush+panic should stay off, got %q", got)
	}
	if m.State().Doubts != 0 {
		t.Errorf("panic-mod should suppress doubt, got doubts=%d", m.State().Doubts)
	}
}

func TestMode_Panic_NoRushMode_Escalates(t *testing.T) {
	// Without RushMode, panic + novice should escalate
	m := NewModeManager(Context{Tier: TierNovice}, fixedNow())
	m.OnMessage("fix NOW this is broken URGENT", Features{PanicKeywords: true})
	if got := m.Current(); got != ModeDegraded {
		t.Errorf("non-rush panic should escalate, got %q", got)
	}
}

func TestMode_PanicMod_DoubtStillEscalates(t *testing.T) {
	// RushMode should suppress PANIC-driven doubt, but real doubt
	// ("are you sure") should still escalate.
	m := NewModeManager(Context{Tier: TierNovice, RushMode: true}, fixedNow())
	m.OnMessage("are you sure this is right?", Features{})
	if got := m.Current(); got != ModeDegraded {
		t.Errorf("real doubt under rush should escalate, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// History
// ---------------------------------------------------------------------------

func TestMode_History_RecordsTransitions(t *testing.T) {
	m := NewModeManager(Context{Tier: TierNovice}, fixedNow())
	m.Apply(SignalDoubt, 1) // off → degraded
	m.Apply(SignalDoubt, 2) // degraded → full
	h := m.History()
	if len(h) != 2 {
		t.Fatalf("history len = %d, want 2", len(h))
	}
	if h[0].From != ModeOff || h[0].To != ModeDegraded {
		t.Errorf("h[0] = %+v, want off→degraded", h[0])
	}
	if h[1].From != ModeDegraded || h[1].To != ModeFull {
		t.Errorf("h[1] = %+v, want degraded→full", h[1])
	}
}

func TestMode_History_Reset(t *testing.T) {
	m := NewModeManager(Context{Tier: TierNovice}, fixedNow())
	m.Apply(SignalDoubt, 1)
	m.Apply(SignalReset, 0)
	h := m.History()
	if len(h) != 2 {
		t.Errorf("history len after reset = %d, want 2", len(h))
	}
	if h[1].Signal != SignalReset {
		t.Errorf("last transition signal = %q, want reset", h[1].Signal)
	}
}

func TestMode_History_NoTransitions_StaysEmpty(t *testing.T) {
	m := NewModeManager(Context{Tier: TierNovice}, fixedNow())
	m.Apply(SignalToolCalls, 1)
	if h := m.History(); len(h) != 0 {
		t.Errorf("no transitions should produce empty history, got %d", len(h))
	}
}

// ---------------------------------------------------------------------------
// Cost-lens killer test
// ---------------------------------------------------------------------------

func TestMode_CostLens_5MinTask_StaysOff(t *testing.T) {
	// A 5-min task with no doubt, no followup, ≤2 tool calls should
	// stay in off (the cost-lens validation).
	m := NewModeManager(Context{Tier: TierNovice}, fixedNow())
	// 5 minutes = 300 seconds
	m.Apply(SignalTime, 300)
	m.Apply(SignalToolCalls, 2)
	if got := m.Current(); got != ModeOff {
		t.Errorf("5min task, no doubt, 2 tools = %q, want off (cost-lens)", got)
	}
}

func TestMode_CostLens_5MinTask_WithDoubt_Escalates(t *testing.T) {
	// A 5-min task where the operator shows doubt should escalate.
	m := NewModeManager(Context{Tier: TierNovice}, fixedNow())
	m.Apply(SignalTime, 300)
	m.Apply(SignalDoubt, 1)
	if got := m.Current(); got != ModeDegraded {
		t.Errorf("5min task with doubt = %q, want degraded", got)
	}
}

// ---------------------------------------------------------------------------
// Snapshot
// ---------------------------------------------------------------------------

func TestMode_Snapshot(t *testing.T) {
	m := NewModeManager(Context{Tier: TierIntermediate}, fixedNow())
	m.Apply(SignalToolCalls, 3)
	m.Apply(SignalTime, 45)
	s := m.Snapshot()
	// Should contain key=value pairs
	for _, want := range []string{"mode=degraded", "tier=intermediate", "tools=3", "elapsed=45s"} {
		if !contains(s, want) {
			t.Errorf("snapshot missing %q in %q", want, s)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// Defensive: default thresholds are non-zero
// ---------------------------------------------------------------------------

func TestMode_DefaultThresholds_NotZero(t *testing.T) {
	for _, tier := range []Tier{TierNovice, TierIntermediate, TierExpert} {
		th := DefaultThresholdsFor(tier)
		if th.OffToDegraded_TimeSec == 0 {
			t.Errorf("tier %q: OffToDegraded_TimeSec = 0", tier)
		}
		if th.DegradedToFull_TimeSec == 0 {
			t.Errorf("tier %q: DegradedToFull_TimeSec = 0", tier)
		}
		if th.FullToDegraded_IdleSec == 0 {
			t.Errorf("tier %q: FullToDegraded_IdleSec = 0", tier)
		}
	}
}

// ---------------------------------------------------------------------------
// Apply value semantics
// ---------------------------------------------------------------------------

func TestMode_Apply_TimeUpdatesElapsed(t *testing.T) {
	m := NewModeManager(Context{Tier: TierNovice}, fixedNow())
	m.Apply(SignalTime, 30)
	if m.State().Elapsed != 30*time.Second {
		t.Errorf("elapsed = %v, want 30s", m.State().Elapsed)
	}
}

func TestMode_Apply_TokensRecordedButNotGated(t *testing.T) {
	// Tokens are recorded but don't trigger transitions in L8.2
	// (deferred to Loop 9).
	m := NewModeManager(Context{Tier: TierNovice}, fixedNow())
	m.Apply(SignalTokens, 1_000_000)
	if m.State().TokensUsed != 1_000_000 {
		t.Errorf("tokens = %d, want 1000000", m.State().TokensUsed)
	}
	if got := m.Current(); got != ModeOff {
		t.Errorf("after huge token count, mode = %q, want off (tokens deferred to L9)", got)
	}
}

// ---------------------------------------------------------------------------
// Printf helper to keep snapshot output clean
// ---------------------------------------------------------------------------

var _ = fmt.Sprintf
