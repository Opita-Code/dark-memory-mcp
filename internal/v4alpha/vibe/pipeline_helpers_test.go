package vibe

import (
	"context"
	"database/sql"
	"sync"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/judge"
)

// FakeJudge is a deterministic Judge implementation for tests. It
// returns NextVerdict (or a default aligned verdict) on every call
// and records all calls in Calls so tests can assert on what the
// Pipeline passed.
type FakeJudge struct {
	mu          sync.Mutex
	NextVerdict *judge.Verdict
	NextErr     error
	Calls       []FakeJudgeCall
}

// FakeJudgeCall captures one Evaluate invocation.
type FakeJudgeCall struct {
	EvalType   string
	SpecIntent string
	Ref        *judge.Ref
}

// Evaluate returns the configured NextVerdict / NextErr or a default
// aligned verdict when nothing is configured.
func (f *FakeJudge) Evaluate(_ context.Context, evalType, specIntent string, ref *judge.Ref) (*judge.Verdict, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, FakeJudgeCall{
		EvalType:   evalType,
		SpecIntent: specIntent,
		Ref:        ref,
	})
	if f.NextErr != nil {
		return nil, f.NextErr
	}
	if f.NextVerdict != nil {
		return f.NextVerdict, nil
	}
	return &judge.Verdict{
		Verdict:    judge.VerdictAligned,
		Confidence: 0.95,
		Reasoning:  "default fake aligned",
	}, nil
}

// newTestPipeline wires a Pipeline against the test DB with a
// FakeJudge and a fresh audit.Writer. The audit.Writer reuses the
// same SQLite connection. Accepts testHelper so it works inside
// rapid.Check too.
func newTestPipeline(t testHelper, db *sql.DB) (*Pipeline, *audit.Writer, *FakeJudge) {
	t.Helper()
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("audit.CreateSchema: %v", err)
	}
	w := audit.NewWriter(db)
	j := &FakeJudge{}
	p := NewPipeline(db, w, j)
	return p, w, j
}
