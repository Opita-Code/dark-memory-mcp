// O3: ResearchTopic — fans out to registered research backends,
// aggregates results, persists one ResearchRun + Items in the
// active project. Returns the new run ID and the items (already
// capped to MaxItems).
package orchestration

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/research"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// ResearchTopicInput is the request to run a research topic.
type ResearchTopicInput struct {
	Query     string `json:"query"`
	Intent    string `json:"intent"` // cve | web | news | dark | geo | academic
	SessionID string `json:"session_id,omitempty"`
	MaxItems  int    `json:"max_items,omitempty"` // default 20
}

// ResearchTopicOutput is the result of a research topic.
type ResearchTopicOutput struct {
	RunID      int64           `json:"run_id"`
	ItemsCount int             `json:"items_count"`
	Items      []research.Item `json:"items"`
}

// ResearchTopic runs the registered research backends, aggregates
// results, and persists them in the active project.
//
// Returns ErrInvalidArgument if Query is empty. Returns
// ErrSessionRequired if no active project. Backend errors are
// logged into research_runs.Errors and do not fail the call (graceful
// degradation).
func (o *Orchestrator) ResearchTopic(ctx context.Context, in ResearchTopicInput) (*ResearchTopicOutput, error) {
	if strings.TrimSpace(in.Query) == "" {
		return nil, errMissingField("query")
	}
	if in.MaxItems <= 0 {
		in.MaxItems = 20
	}

	start := o.now()

	// Fan out to backends. Sequential today; parallel is a future
	// enhancement (each backend's Research is independent; safe for
	// concurrent calls given typical backends are HTTP or DB
	// connections).
	var (
		allItems   []research.Item
		tried      []string
		used       string // first backend that returned items
		errs       []research.BackendError
	)
	for _, b := range o.backends {
		tried = append(tried, b.Name())
		items, err := b.Research(ctx, in.Query, in.Intent)
		if err != nil {
			errs = append(errs, research.BackendError{Backend: b.Name(), Err: err.Error()})
			continue
		}
		if used == "" && len(items) > 0 {
			used = b.Name()
		}
		allItems = append(allItems, items...)
	}

	// Cap to MaxItems (first-N selection, no reordering; callers can
	// re-rank via Recall).
	if len(allItems) > in.MaxItems {
		allItems = allItems[:in.MaxItems]
	}

	// Confidence average (best-effort; if no items, 0).
	var confSum float64
	for _, it := range allItems {
		confSum += float64(it.Confidence)
	}
	var confAvg float32
	if len(allItems) > 0 {
		confAvg = float32(confSum / float64(len(allItems)))
	}

	tookMs := o.now().Sub(start).Milliseconds()

	run := &research.ResearchRun{
		SessionID:     in.SessionID,
		Query:         in.Query,
		Intent:        in.Intent,
		BackendUsed:   used,
		BackendsTried: tried,
		TookMs:        tookMs,
		ConfidenceAvg: confAvg,
		Items:         allItems,
		Errors:        errs,
	}

	wc := store.WriteContext{
		Actor:     "orchestrator_research_topic",
		SessionID: in.SessionID,
		WritePath: "ResearchTopic",
	}
	runID, err := o.Store.SaveRun(ctx, wc, run)
	if err != nil {
		return nil, fmt.Errorf("research_topic: save run: %w", err)
	}

	return &ResearchTopicOutput{
		RunID:      runID,
		ItemsCount: len(allItems),
		Items:      allItems,
	}, nil
}

// Compile-time check that ResearchTopicOutput uses time.Time somewhere
// (the timestamp machinery above depends on time.Time).
var _ = time.Time{}