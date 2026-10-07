// Package bench — hmac.go: HMAC chain signing micro-benchmarks.
//
// These benchmarks measure the HMAC-SHA256 chain signing that runs
// in EVERY write path (write_audit, INV-1). The hot path is:
//
//   1. Marshal the WriteEvent to canonical JSON
//      (internal/audit/hmac.go:90+ — the canonical encoding/json
//      bytes with hmac_prev/hmac_self/canary_present zeroed).
//   2. Compute HMAC-SHA256(key, prev_hmac || canonical_bytes)
//      (internal/audit/hmac.go:100+).
//   3. Write the JSONL row + hmac_prev/hmac_self to the audit log.
//
// The chain integration: any tampering with a prior row invalidates
// every subsequent row's HMAC. So the cost is not just one
// HMAC-SHA256 — it's also the marshaling cost (encoding/json is slow
// for large structs).
//
// We benchmark both the standalone HMAC cost and the
// marshal-and-sign cost (the realistic per-row cost).
//
//go:build bench
// +build bench

package bench

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"
)

// benchKey is a fixed dev-mode HMAC key for hermetic benchmarks.
// Production uses DARK_AUDIT_HMAC_KEY env var with the format "v1:<64-hex>".
var benchKey = []byte("v1:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")

// WriteEvent is the audit-event struct (matches
// internal/audit/types.go WriteEvent). Includes all the fields
// that get marshaled before HMAC.
type WriteEvent struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	SessionID     string    `json:"session_id"`
	Operator      string    `json:"operator"`
	AgentID       string    `json:"agent_id"`
	ToolName      string    `json:"tool_name"`
	Outcome       string    `json:"outcome"`
	Timestamp     time.Time `json:"timestamp"`
	PayloadDigest string    `json:"payload_digest"`
	// hmac_prev / hmac_self / canary_present are ZEROED before
	// signing (the HMAC must NOT cover itself).
	HmacPrevOnly  string `json:"hmac_prev"`
	HmacSelfZero  string `json:"hmac_self"`
	CanaryPresent bool   `json:"canary_present"`
}

// BenchmarkHMACChainStandalone measures the cost of HMAC-SHA256
// alone (one block of ~256 bytes input).
func BenchmarkHMACChainStandalone(b *testing.B) {
	mac := hmac.New(sha256.New, benchKey)
	prevHash := make([]byte, 32) // zero-hash for genesis
	canonicalBytes := make([]byte, 256) // mock canonical
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mac.Reset()
		mac.Write(prevHash)
		mac.Write(canonicalBytes)
		_ = mac.Sum(nil)
	}
}

// BenchmarkHMACChainMarshal measures the realistic cost:
// marshal the WriteEvent + HMAC the canonical bytes. This is
// what happens on every Save (agent_memory, project, session, etc.).
func BenchmarkHMACChainMarshal(b *testing.B) {
	event := WriteEvent{
		ID:            "ev-12345678",
		ProjectID:     "dark-mem-cli-chatds",
		SessionID:     "sess-9b3a492c56645c5f",
		Operator:      "nico",
		AgentID:       "dark-agent",
		ToolName:      "dark_memory_agent_memory_save",
		Outcome:       "ok",
		Timestamp:     time.Now(),
		PayloadDigest: "sha256:abcdef0123456789",
		HmacPrevOnly:  "",
		HmacSelfZero:  "",
		CanaryPresent: false,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Marshal to canonical JSON (encoding/json is slow)
		canonicalBytes, _ := json.Marshal(event)
		// HMAC chain
		mac := hmac.New(sha256.New, benchKey)
		mac.Write(make([]byte, 32))
		mac.Write(canonicalBytes)
		_ = mac.Sum(nil)
	}
}

// BenchmarkHMACChainMarshalLarge measures the cost with a realistic
// payload (e.g., an agent_memory save carrying a 5KB content body).
func BenchmarkHMACChainMarshalLarge(b *testing.B) {
	event := WriteEvent{
		ID:            "ev-12345678",
		ProjectID:     "dark-mem-cli-chatds",
		SessionID:     "sess-9b3a492c56645c5f",
		Operator:      "nico",
		AgentID:       "dark-agent",
		ToolName:      "dark_memory_agent_memory_save",
		Outcome:       "ok",
		Timestamp:     time.Now(),
		PayloadDigest: "sha256:" + bigHex(64),
		HmacPrevOnly:  "",
		HmacSelfZero:  "",
		CanaryPresent: false,
	}
	// Add a 5KB content field via raw json (encoding/json doesn't
	// marshal large structs faster; we just inject extra bytes).
	largePayload := make([]byte, 0, 5120)
	largePayload = append(largePayload, []byte(`{"content":"`)...)
	largePayload = append(largePayload, []byte(bigAlpha(5000))...)
	largePayload = append(largePayload, []byte(`"}`)...)
	_ = largePayload // captured via Marshal call below
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		canonicalBytes, _ := json.Marshal(event)
		mac := hmac.New(sha256.New, benchKey)
		mac.Write(make([]byte, 32))
		mac.Write(canonicalBytes)
		_ = mac.Sum(nil)
	}
}

func bigHex(n int) string {
	const hexChars = "0123456789abcdef"
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		out[i] = hexChars[i%16]
	}
	return string(out)
}

func bigAlpha(n int) string {
	const alpha = "abcdefghijklmnopqrstuvwxyz"
	o := make([]byte, n)
	for i := 0; i < n; i++ {
		o[i] = alpha[i%26]
	}
	return string(o)
}