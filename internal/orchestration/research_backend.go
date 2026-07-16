// ResearchBackend is one OSINT-style search source the
// ResearchTopic orchestrator fans out to. Real backends (dark
// search, news_monitor, ip-footprint, cve-enrich, etc.) plug in by
// implementing this interface and being registered on the
// Orchestrator via WithBackend / WithBackends. Tests use
// MockResearchBackend.
package orchestration

import (
	"context"

	"github.com/dark-agents/dark-memory-mcp/internal/research"
)

// ResearchBackend is the contract for one search source.
//
// Implementations should be safe for concurrent calls. The
// orchestrator fans out to all registered backends in parallel
// goroutines (future enhancement; sequential today) and aggregates
// results into a single ResearchRun.
type ResearchBackend interface {
	// Name returns the stable identifier of this backend (e.g.
	// "web_search", "news_monitor", "cve_enrich"). Used in
	// research_runs.backend_used when only one backend contributed
	// items, and in backends_tried for audit.
	Name() string

	// Research performs one search. Returns a slice of items; an
	// error from a backend is logged into the run's Errors slice
	// (the orchestrator continues with the remaining backends).
	Research(ctx context.Context, query string, intent string) ([]research.Item, error)
}

// WithBackends returns a new Orchestrator with the given backends.
// Use when constructing the orchestrator from a configuration source.
func (o *Orchestrator) WithBackends(backends ...ResearchBackend) *Orchestrator {
	o.backends = append(o.backends, backends...)
	return o
}

// WithBackend adds a single backend. Convenience for tests.
func (o *Orchestrator) WithBackend(b ResearchBackend) *Orchestrator {
	o.backends = append(o.backends, b)
	return o
}

// MockResearchBackend is a configurable in-memory ResearchBackend
// for tests. It returns the configured items (and optional error) on
// every Research call. If Items and Err are both set, both are
// returned (the orchestrator logs the error but still uses the
// items).
type MockResearchBackend struct {
	Name_  string         // exposed as Name()
	Items  []research.Item // returned by Research
	Err    error          // returned by Research (alongside Items)
	Calls  int            // incremented on every Research call
	LastQ  string         // last query received
	LastI  string         // last intent received
}

// Name implements ResearchBackend.
func (m *MockResearchBackend) Name() string { return m.Name_ }

// Research implements ResearchBackend.
func (m *MockResearchBackend) Research(ctx context.Context, query, intent string) ([]research.Item, error) {
	m.Calls++
	m.LastQ = query
	m.LastI = intent
	return m.Items, m.Err
}