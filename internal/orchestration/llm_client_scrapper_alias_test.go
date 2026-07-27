// Package orchestration — llm_client_scrapper_alias_test.go: validates
// the v2.0.1 follow-up 3 (DARK_SCRAPPER_URL alias shim).
//
// Three contracts pinned:
//  1. DARK_SCRAPPER_URL set + DARK_DRIFT_JUDGE_DAEMON_URL empty →
//     falls through to the legacy env (provider="drift_judge_daemon").
//  2. Both set → DARK_DRIFT_JUDGE_DAEMON_URL wins (the legacy is
//     deprecated; explicit modern env takes precedence).
//  3. Neither set → ErrNoLLMAvailable (legacy behavior preserved).
//
// Why this matters: PR #10 (v2.0.0) renamed DARK_SCRAPPER_URL →
// DARK_DRIFT_JUDGE_DAEMON_URL as a HARD breaking change (H-4 has no
// exception clause). The follow-up shim adds a deprecation path so
// operators with legacy `.env` files see a one-line boot notice
// instead of a hard ErrNoLLMAvailable. The shim is scheduled for
// removal in v2.1.0.
package orchestration

import (
	"context"
	"os"
	"strings"
	"testing"
)

// withEnv sets the env for the duration of fn, then restores. Used
// to scope env mutations so tests don't pollute each other.
func withEnv(t *testing.T, kv map[string]string, fn func()) {
	t.Helper()
	prev := make(map[string]string, len(kv))
	missing := make(map[string]bool, len(kv))
	for k := range kv {
		if v, ok := os.LookupEnv(k); ok {
			prev[k] = v
		} else {
			missing[k] = true
		}
	}
	for k, v := range kv {
		_ = os.Setenv(k, v)
	}
	defer func() {
		for k, v := range prev {
			_ = os.Setenv(k, v)
		}
		for k := range missing {
			_ = os.Unsetenv(k)
		}
	}()
	fn()
}

func TestNewSelfHarnessClient_LegacyScrapperURL_FallsThrough(t *testing.T) {
	withEnv(t, map[string]string{
		"DARK_SCRAPPER_URL":             "http://legacy-daemon.local:8901",
		"DARK_DRIFT_JUDGE_DAEMON_URL":   "",
		"ANTHROPIC_API_KEY":             "",
		"OPENAI_API_KEY":                "",
		"GEMINI_API_KEY":                "",
	}, func() {
		c, err := NewSelfHarnessClient()
		if err != nil {
			t.Fatalf("NewSelfHarnessClient: %v", err)
		}
		if c == nil {
			t.Fatalf("expected non-nil client")
		}
		if c.provider != "drift_judge_daemon" {
			t.Errorf("provider = %q, want drift_judge_daemon (legacy alias)", c.provider)
		}
		if c.key != "http://legacy-daemon.local:8901" {
			t.Errorf("key = %q, want the legacy URL", c.key)
		}
	})
}

func TestNewSelfHarnessClient_BothEnvsSet_ModernWins(t *testing.T) {
	withEnv(t, map[string]string{
		"DARK_SCRAPPER_URL":           "http://legacy-daemon.local:8901",
		"DARK_DRIFT_JUDGE_DAEMON_URL": "http://modern-daemon.local:8901",
		"ANTHROPIC_API_KEY":           "",
		"OPENAI_API_KEY":              "",
		"GEMINI_API_KEY":              "",
	}, func() {
		c, err := NewSelfHarnessClient()
		if err != nil {
			t.Fatalf("NewSelfHarnessClient: %v", err)
		}
		if c.provider != "drift_judge_daemon" {
			t.Errorf("provider = %q, want drift_judge_daemon", c.provider)
		}
		if c.key != "http://modern-daemon.local:8901" {
			t.Errorf("key = %q, want the modern URL (modern env must win over legacy)", c.key)
		}
	})
}

func TestNewSelfHarnessClient_NeitherEnvSet_ReturnsErrNoLLMAvailable(t *testing.T) {
	withEnv(t, map[string]string{
		"DARK_SCRAPPER_URL":           "",
		"DARK_DRIFT_JUDGE_DAEMON_URL": "",
		"ANTHROPIC_API_KEY":           "",
		"OPENAI_API_KEY":              "",
		"GEMINI_API_KEY":              "",
	}, func() {
		c, err := NewSelfHarnessClient()
		if err == nil {
			t.Fatalf("expected ErrNoLLMAvailable when no LLM env is set; got client %+v", c)
		}
		if !strings.Contains(err.Error(), "no LLM available") {
			t.Errorf("error message %q must mention \"no LLM available\"", err.Error())
		}
	})
}

// Sanity check: Judge path works through the legacy alias. We don't
// reach a real daemon (the env points to localhost on an unbound
// port), but we verify the client constructs + routes correctly.
//
// The Judge() method is exercised end-to-end in the unit tests for
// judgeViaHTTP / judgeViaDriftJudgeDaemon (see llm_client_test.go);
// this test only verifies the alias wires the legacy URL into the
// provider's `key` field.
func TestNewSelfHarnessClient_LegacyAlias_JudgeFailsFast(t *testing.T) {
	withEnv(t, map[string]string{
		"DARK_SCRAPPER_URL":           "http://localhost:1",
		"DARK_DRIFT_JUDGE_DAEMON_URL": "",
		"ANTHROPIC_API_KEY":           "",
		"OPENAI_API_KEY":              "",
		"GEMINI_API_KEY":              "",
	}, func() {
		c, err := NewSelfHarnessClient()
		if err != nil {
			t.Fatalf("NewSelfHarnessClient: %v", err)
		}
		// Judge on a localhost:1 daemon must fail with a connection
		// error (not ErrNoLLMAvailable — the alias set the URL).
		_, jErr := c.Judge(context.Background(), JudgeRequest{
			EvalType: "drift_judge",
			Content:  "test",
		})
		if jErr == nil {
			t.Fatalf("expected connection error against localhost:1")
		}
		if strings.Contains(jErr.Error(), "no LLM available") {
			t.Errorf("legacy alias should not surface ErrNoLLMAvailable; got %v", jErr)
		}
	})
}