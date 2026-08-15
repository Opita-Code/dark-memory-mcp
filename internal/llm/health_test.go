package llm

import (
	"context"
	"testing"
	"time"
)

// TestHealthRegistry_AllowedFails_Auth verifies cooldown on the 1st
// auth failure (policy 0).
func TestHealthRegistry_AllowedFails_Auth(t *testing.T) {
	h := NewHealthRegistry(HealthOptions{})
	h.RecordFailure("deepseek", FailAuth)
	if !h.InCooldown("deepseek") {
		t.Fatal("InCooldown = false after 1st auth failure, want true (policy auth→0)")
	}
}

// TestHealthRegistry_AllowedFails_Rate verifies cooldown only on the
// 4th rate-limit failure (policy 3).
func TestHealthRegistry_AllowedFails_Rate(t *testing.T) {
	h := NewHealthRegistry(HealthOptions{})
	for i := 0; i < 3; i++ {
		h.RecordFailure("openai", FailRate)
		if h.InCooldown("openai") {
			t.Fatalf("InCooldown after %d rate failures, want false (policy rate→3)", i+1)
		}
	}
	h.RecordFailure("openai", FailRate)
	if !h.InCooldown("openai") {
		t.Fatal("InCooldown after 4th rate failure = false, want true")
	}
}

// TestHealthRegistry_CooldownExpires verifies a provider recovers
// after cooldown_time.
func TestHealthRegistry_CooldownExpires(t *testing.T) {
	h := NewHealthRegistry(HealthOptions{CooldownTime: 50 * time.Millisecond})
	h.RecordFailure("deepseek", FailAuth)
	if !h.InCooldown("deepseek") {
		t.Fatal("InCooldown = false right after failure")
	}
	time.Sleep(80 * time.Millisecond)
	if h.InCooldown("deepseek") {
		t.Fatal("InCooldown = true after cooldown window, want false")
	}
}

// TestHealthRegistry_RecordSuccessClearsCooldown verifies a success
// resets counters + cooldown.
func TestHealthRegistry_RecordSuccessClearsCooldown(t *testing.T) {
	h := NewHealthRegistry(HealthOptions{})
	h.RecordFailure("deepseek", FailAuth)
	if !h.InCooldown("deepseek") {
		t.Fatal("setup: expected cooldown")
	}
	h.RecordSuccess("deepseek")
	if h.InCooldown("deepseek") {
		t.Fatal("InCooldown = true after RecordSuccess, want false")
	}
}

// TestHealthRegistry_IgnoreTransient verifies 429/408 never count.
func TestHealthRegistry_IgnoreTransient(t *testing.T) {
	h := NewHealthRegistry(HealthOptions{IgnoreTransient: true})
	for i := 0; i < 10; i++ {
		h.RecordFailure("deepseek", FailRate)
	}
	if h.InCooldown("deepseek") {
		t.Fatal("InCooldown = true with IgnoreTransient + rate failures, want false")
	}
	// Hard auth still cools down.
	h.RecordFailure("deepseek", FailAuth)
	if !h.InCooldown("deepseek") {
		t.Fatal("InCooldown = false after auth with IgnoreTransient, want true")
	}
}

// TestHealthRegistry_FilterCandidates_SafetyNet verifies fail-open:
// every candidate in cooldown → returns all.
func TestHealthRegistry_FilterCandidates_SafetyNet(t *testing.T) {
	h := NewHealthRegistry(HealthOptions{})
	h.RecordFailure("a", FailAuth)
	h.RecordFailure("b", FailAuth)
	// a+b in cooldown, c untouched.
	got := h.FilterCandidates([]string{"a", "b", "c"})
	if len(got) != 1 || got[0] != "c" {
		t.Fatalf("FilterCandidates = %v, want [c]", got)
	}
	// Safety net: ALL in cooldown → fail open (return all).
	gotAll := h.FilterCandidates([]string{"a", "b"})
	if len(gotAll) != 2 {
		t.Fatalf("safety net FilterCandidates = %v, want [a b]", gotAll)
	}
}

// TestHealthRegistry_StartStop_NoLeak verifies the background loop
// starts and stops cleanly (R2 risk-register check).
func TestHealthRegistry_StartStop_NoLeak(t *testing.T) {
	h := NewHealthRegistry(HealthOptions{Interval: 10 * time.Millisecond})
	var calls int
	h.SetProbeFn(func(ctx context.Context, providerID string) ProbeResult {
		calls++
		return ProbeResult{ProviderID: providerID, State: ProbeValid}
	})
	// Register a provider so the probe loop has something to iterate.
	h.RecordSuccess("deepseek")
	ctx, cancel := context.WithCancel(context.Background())
	h.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	h.Stop()
	if calls < 1 {
		t.Errorf("background probe calls = %d, want >= 1", calls)
	}
	cancel()
	// Stop after cancel is still safe (idempotent).
	h.Stop()
}

// TestHealthRegistry_BackgroundProbe_Cooldowns verifies the probe loop
// cools down a provider that fails its probe and clears on success.
func TestHealthRegistry_BackgroundProbe_Cooldowns(t *testing.T) {
	h := NewHealthRegistry(HealthOptions{Interval: 10 * time.Millisecond, CooldownTime: 500 * time.Millisecond})
	h.SetProbeFn(func(ctx context.Context, providerID string) ProbeResult {
		return ProbeResult{ProviderID: providerID, State: ProbeAuthError, Class: FailAuth}
	})
	// Ensure the provider is registered (probe loop only iterates
	// registered providers).
	h.RecordSuccess("deepseek") // registers + marks healthy
	ctx, cancel := context.WithCancel(context.Background())
	h.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	h.Stop()
	cancel()

	if !h.InCooldown("deepseek") {
		t.Fatal("InCooldown = false after background auth-failure probes, want true")
	}
}

// TestHealthRegistry_Snapshot reports per-provider counters.
func TestHealthRegistry_Snapshot(t *testing.T) {
	h := NewHealthRegistry(HealthOptions{})
	h.RecordFailure("deepseek", FailAuth)
	h.RecordFailure("deepseek", FailAuth) // 2nd — cooldown already on
	snap := h.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("Snapshot len = %d, want 1", len(snap))
	}
	s := snap[0]
	if s.ProviderID != "deepseek" {
		t.Errorf("ProviderID = %q, want deepseek", s.ProviderID)
	}
	if s.FailedCalls["auth"] != 2 {
		t.Errorf("FailedCalls[auth] = %d, want 2", s.FailedCalls["auth"])
	}
	if s.CooldownUntil == nil {
		t.Error("CooldownUntil = nil, want set")
	}
}
