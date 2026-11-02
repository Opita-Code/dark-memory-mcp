// credential.go — the ONE place where a binding provider id is
// turned into a credential.
//
// The project's NLIConfig stores the PROVIDER ID, never the secret:
// the API key lives in the OS keyring (llmkeystore) and is resolved
// at CALL time, from the provider id the binding carries. This file
// owns that resolution so every consumer (the LLMJudge path, the NLI
// chat path, the bind tool) agrees on WHICH key a provider id means.
//
// # THE DISPATCH PREFIXES
//
// A binding provider id carries a DISPATCH prefix that says WHICH
// consumer path the binding feeds:
//
//	"judge-<id>"  → the LLMJudge path (internal/v4alpha/judge/v4judge)
//	"chat-<id>"   → the NLI chat path (internal/nli)
//
// What follows the prefix is the CANONICAL CATALOG id. So
// "judge-minimax" and "chat-minimax" and "minimax" all name the SAME
// catalog provider ("minimax") and therefore the SAME keyring entry —
// one key serves every dispatch path of that provider.
//
// # SECURITY
//
// The credential is NEVER logged and NEVER included in an error
// message. Callers that need to report "no credential" report the
// PROVIDER ID, never the key.
package llm

import (
	"os"
	"strings"

	"github.com/dark-agents/dark-memory-mcp/internal/llmkeystore"
)

// dispatchPrefixes are the DISPATCH prefixes a binding provider id may
// carry. They select the CONSUMER PATH, not the provider.
var dispatchPrefixes = []string{"judge-", "chat-"}

// CatalogProviderID maps a binding provider id to the catalog provider
// it talks to: "judge-minimax" -> "minimax", "chat-minimax" ->
// "minimax", "minimax" -> "minimax". The "judge-"/"chat-" prefixes are
// DISPATCH prefixes; what follows is the canonical catalog id. False
// when the id names no catalog provider.
func CatalogProviderID(providerID string) (string, bool) {
	id := strings.TrimSpace(providerID)
	if id == "" {
		return "", false
	}
	for _, prefix := range dispatchPrefixes {
		if rest, ok := strings.CutPrefix(id, prefix); ok {
			id = rest
			break
		}
	}
	if id == "" {
		return "", false
	}
	spec := SpecByID(id)
	if spec == nil {
		return "", false
	}
	return spec.ID, true
}

// CredentialForProvider returns the credential to use for a provider id
// as it appears in a binding.
//
// Resolution order:
//
//  1. keyring, exact provider id
//  2. keyring, catalog id (prefix stripped)
//  3. env var of the catalog spec (spec.EnvKey)
//
// The keyring is the source of truth; the env is the compatibility
// backend (llmkeystore's composite read order). Returns ("", false)
// when no credential exists.
//
// The returned key is the SECRET: callers must never log it nor embed
// it in an error message.
func CredentialForProvider(ks llmkeystore.KeyStore, providerID string) (string, bool) {
	id := strings.TrimSpace(providerID)
	if id == "" {
		return "", false
	}
	// 1. keyring, exact provider id.
	if key, ok := keyFromStore(ks, id); ok {
		return key, true
	}
	catalogID, mapped := CatalogProviderID(id)
	if !mapped {
		return "", false
	}
	// 2. keyring, catalog id (dispatch prefix stripped).
	if catalogID != id {
		if key, ok := keyFromStore(ks, catalogID); ok {
			return key, true
		}
	}
	// 3. env var of the catalog spec (compatibility backend).
	if spec := SpecByID(catalogID); spec != nil && spec.EnvKey != "" {
		if key := strings.TrimSpace(os.Getenv(spec.EnvKey)); key != "" {
			return key, true
		}
	}
	return "", false
}

// keyFromStore reads a non-empty key from the key store. An empty
// value counts as "no key" (the store must never hand out a blank
// credential).
func keyFromStore(ks llmkeystore.KeyStore, providerID string) (string, bool) {
	if ks == nil {
		return "", false
	}
	key, err := ks.Get(providerID)
	if err != nil {
		return "", false
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", false
	}
	return key, true
}
