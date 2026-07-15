// O4: RecallContext — retrieves relevant memory within a token
// budget. Wraps Store.Recall with the Atlan economy pipeline.
//
// Spec 173 O4: this is the canonical retrieval entry point. It is
// what MCP tool callers should use to ask "what do I know about X".
// The orchestrator enforces a token budget: items are compressed
// through economy.Compress and capped so the total stays under
// MaxTokens. If the budget is tight, callers can re-call with a
// smaller MaxTokens (the orchestrator returns a hint).
package orchestration

import (
	"context"
	"fmt"
	"strings"

	"github.com/dark-agents/dark-memory-mcp/internal/economy"
	"github.com/dark-agents/dark-memory-mcp/internal/research"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// RecallContextInput is the request for a context retrieval.
type RecallContextInput struct {
	Query     string `json:"query"`
	Intent    string `json:"intent,omitempty"`
	Source    string `json:"source,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty"` // default 2000
}

// RecallContextOutput is the result of a context retrieval.
type RecallContextOutput struct {
	Items      []research.Item `json:"items"`
	TokensUsed int             `json:"tokens_used"`
	Truncated  bool            `json:"truncated"` // true if budget forced fewer items than Recall returned
}

// RecallContext fetches items matching Query from the active project
// and compresses them within MaxTokens. Returns ErrInvalidArgument if
// Query is empty. Returns ErrSessionRequired if no active project.
//
// The orchestrator does NOT require any backends (unlike ResearchTopic).
// It pulls from the persisted Store only.
func (o *Orchestrator) RecallContext(ctx context.Context, in RecallContextInput) (*RecallContextOutput, error) {
	if strings.TrimSpace(in.Query) == "" {
		return nil, errMissingField("query")
	}
	if in.MaxTokens <= 0 {
		in.MaxTokens = 2000
	}

	// Pull from Store. Recall enforces project filter internally.
	opts := research.RecallOptions{
		Query:  in.Query,
		Intent: in.Intent,
		Source: in.Source,
		Limit:  100, // generous; compression will cap
	}
	raw, err := o.Store.Recall(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("recall_context: store recall: %w", err)
	}

	// Tuning heuristic: start with sane defaults, shrink CapTotal if
	// the budget is tight. Rough heuristic: 1 item ~= 200 tokens
	// post-compression.
	capTotal := 10
	if in.MaxTokens < 1000 {
		capTotal = 5
	}
	if in.MaxTokens < 500 {
		capTotal = 3
	}

	econ := economy.Options{
		FilterConfidenceThreshold: economy.DefaultOptions().FilterConfidenceThreshold,
		TruncatePerItem:            500,
		CapTotal:                   capTotal,
		CompressBody:               true,
	}

	compressed := economy.Compress(raw, econ)
	tokens := economy.EstimateTokens(compressed)

	// If we still exceed the budget, hard-cap item count further.
	truncated := false
	for tokens > in.MaxTokens && len(compressed) > 1 {
		truncated = true
		compressed = compressed[:len(compressed)-1]
		tokens = economy.EstimateTokens(compressed)
	}

	// Final per-item shrink if still over (long items).
	if tokens > in.MaxTokens {
		truncated = true
		for i := range compressed {
			if tokens <= in.MaxTokens {
				break
			}
			if len(compressed[i].Snippet) > 100 {
				compressed[i].Snippet = compressed[i].Snippet[:100] + "..."
				tokens = economy.EstimateTokens(compressed)
			}
		}
	}

	// Final per-item shrink if still over (long items).
	if tokens > in.MaxTokens {
		truncated = true
		for i := range compressed {
			if tokens <= in.MaxTokens {
				break
			}
			if len(compressed[i].Snippet) > 100 {
				compressed[i].Snippet = compressed[i].Snippet[:100] + "..."
				tokens = economy.EstimateTokens(compressed)
			}
		}
	}

	return &RecallContextOutput{
		Items:      compressed,
		TokensUsed: tokens,
		Truncated:  truncated,
	}, nil
}

// Void-import guard for errors used elsewhere.
var _ = store.ErrSessionRequired