package nli

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeProvider is a programmable Provider for router tests. The
// programmed behavior (delay, score, error) is consumed atomically.
type fakeProvider struct {
	id       string
	mu       sync.Mutex
	score    Score
	err      error
	delay    time.Duration
	calls    atomic.Int32
	lastPrem string
	lastHypo string
}

func (f *fakeProvider) ID() string { return f.id }

func (f *fakeProvider) Score(ctx context.Context, premise, hypothesis string) (Score, error) {
	f.calls.Add(1)
	f.mu.Lock()
	d := f.delay
	pre := premise
	hy := hypothesis
	score := f.score
	err := f.err
	f.mu.Unlock()

	if d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return Score{ProviderID: f.id, LatencyMS: d.Milliseconds()}, ErrProviderTimeout
		}
	}
	f.mu.Lock()
	f.lastPrem = pre
	f.lastHypo = hy
	f.mu.Unlock()
	if err != nil {
		return Score{ProviderID: f.id, LatencyMS: d.Milliseconds()}, err
	}
	score.ProviderID = f.id
	score.LatencyMS = d.Milliseconds()
	return score, nil
}

func (f *fakeProvider) Program(s Score, err error, delay time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.score = s
	f.err = err
	f.delay = delay
}

func newTestRouter(t *testing.T, primary, fallback Provider, cfg Config) *Router {
	t.Helper()
	r, err := NewRouter(primary, fallback, cfg)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return r
}

// ---- Constructor validation ----

func TestNewRouter_Validation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		prim Provider
		fb   Provider
		cfg  Config
		want error
	}{
		{"nil primary", nil, nil, Config{}, ErrInvalidConfig},
		{"FallbackEnabled but nil fallback", &fakeProvider{id: "p"}, nil, Config{FallbackEnabled: true}, ErrInvalidConfig},
		{"empty primary ID", &fakeProvider{id: ""}, nil, Config{}, ErrInvalidConfig},
		{"empty fallback ID", &fakeProvider{id: "p"}, &fakeProvider{id: ""}, Config{FallbackEnabled: true}, ErrInvalidConfig},
		{"same primary and fallback ID", &fakeProvider{id: "same"}, &fakeProvider{id: "same"}, Config{FallbackEnabled: true}, ErrInvalidConfig},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewRouter(tt.prim, tt.fb, tt.cfg)
			if !errors.Is(err, tt.want) {
				t.Errorf("err=%v, want %v", err, tt.want)
			}
		})
	}
}

// ---- Primary success, fallback not taken ----

func TestRouter_PrimarySucceeds_NoFallback(t *testing.T) {
	t.Parallel()
	primary := &fakeProvider{id: "primary"}
	primary.Program(Score{Label: LabelEntailment, Confidence: 0.9}, nil, 0)
	fb := &fakeProvider{id: "fb"}
	r := newTestRouter(t, primary, fb, Config{FallbackEnabled: true, LatencyBudgetMS: 1000})

	got, err := r.Score(context.Background(), "p", "h")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if got.ProviderID != "primary" {
		t.Errorf("ProviderID=%q, want primary", got.ProviderID)
	}
	if primary.calls.Load() != 1 {
		t.Errorf("primary.calls=%d, want 1", primary.calls.Load())
	}
	if fb.calls.Load() != 0 {
		t.Errorf("fallback should not have been called: %d", fb.calls.Load())
	}
	st := r.Stats()
	if st.PrimarySuccesses != 1 || st.FallbackSuccesses != 0 {
		t.Errorf("stats: %+v", st)
	}
}

// ---- Primary fails (Timeout), fallback succeeds ----

func TestRouter_PrimaryTimeout_FallbackSucceeds(t *testing.T) {
	t.Parallel()
	primary := &fakeProvider{id: "primary"}
	primary.Program(Score{}, ErrProviderTimeout, 200*time.Millisecond)
	fb := &fakeProvider{id: "fb"}
	fb.Program(Score{Label: LabelContradiction, Confidence: 0.85}, nil, 0)
	r := newTestRouter(t, primary, fb, Config{FallbackEnabled: true, LatencyBudgetMS: 50})

	got, err := r.Score(context.Background(), "p", "h")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if got.ProviderID != "fb" {
		t.Errorf("ProviderID=%q, want fb", got.ProviderID)
	}
	if got.Label != LabelContradiction {
		t.Errorf("Label=%v, want contradiction", got.Label)
	}
	if primary.calls.Load() != 1 {
		t.Errorf("primary.calls=%d, want 1", primary.calls.Load())
	}
	if fb.calls.Load() != 1 {
		t.Errorf("fallback.calls=%d, want 1", fb.calls.Load())
	}
	st := r.Stats()
	if st.PrimaryFailures != 1 || st.FallbackSuccesses != 1 {
		t.Errorf("stats: %+v", st)
	}
}

// ---- Primary Unavailable, fallback succeeds ----

func TestRouter_PrimaryUnavailable_FallbackSucceeds(t *testing.T) {
	t.Parallel()
	primary := &fakeProvider{id: "primary"}
	primary.Program(Score{}, ErrProviderUnavailable, 0)
	fb := &fakeProvider{id: "fb"}
	fb.Program(Score{Label: LabelNeutral, Confidence: 0.5}, nil, 0)
	r := newTestRouter(t, primary, fb, Config{FallbackEnabled: true})
	got, err := r.Score(context.Background(), "p", "h")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if got.ProviderID != "fb" {
		t.Errorf("ProviderID=%q, want fb", got.ProviderID)
	}
}

// ---- Primary RateLimited, fallback succeeds ----

func TestRouter_PrimaryRateLimited_FallbackSucceeds(t *testing.T) {
	t.Parallel()
	primary := &fakeProvider{id: "primary"}
	primary.Program(Score{}, ErrProviderRateLimited, 0)
	fb := &fakeProvider{id: "fb"}
	fb.Program(Score{Label: LabelEntailment, Confidence: 0.7}, nil, 0)
	r := newTestRouter(t, primary, fb, Config{FallbackEnabled: true})
	got, err := r.Score(context.Background(), "p", "h")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if got.ProviderID != "fb" {
		t.Errorf("ProviderID=%q, want fb", got.ProviderID)
	}
}

// ---- Primary BadResponse → no fallback (contract bug) ----

func TestRouter_PrimaryBadResponse_NoFallback(t *testing.T) {
	t.Parallel()
	primary := &fakeProvider{id: "primary"}
	primary.Program(Score{}, ErrProviderBadResponse, 0)
	fb := &fakeProvider{id: "fb"}
	fb.Program(Score{Label: LabelEntailment, Confidence: 0.7}, nil, 0)
	r := newTestRouter(t, primary, fb, Config{FallbackEnabled: true})
	_, err := r.Score(context.Background(), "p", "h")
	if !errors.Is(err, ErrNoProvider) {
		t.Errorf("err=%v, want ErrNoProvider", err)
	}
	if fb.calls.Load() != 0 {
		t.Errorf("fallback should NOT be called on BadResponse: %d", fb.calls.Load())
	}
	st := r.Stats()
	if st.FallbackSkipped != 1 {
		t.Errorf("FallbackSkipped=%d, want 1", st.FallbackSkipped)
	}
}

// ---- Fallback disabled, primary fails ----

func TestRouter_FallbackDisabled_PrimaryFails(t *testing.T) {
	t.Parallel()
	primary := &fakeProvider{id: "primary"}
	primary.Program(Score{}, ErrProviderTimeout, 0)
	r := newTestRouter(t, primary, nil, Config{FallbackEnabled: false})
	_, err := r.Score(context.Background(), "p", "h")
	if !errors.Is(err, ErrNoProvider) {
		t.Errorf("err=%v, want ErrNoProvider", err)
	}
}

// ---- Both fail ----

func TestRouter_BothFail_ErrNoProvider(t *testing.T) {
	t.Parallel()
	primary := &fakeProvider{id: "primary"}
	primary.Program(Score{}, ErrProviderUnavailable, 0)
	fb := &fakeProvider{id: "fb"}
	fb.Program(Score{}, ErrProviderRateLimited, 0)
	r := newTestRouter(t, primary, fb, Config{FallbackEnabled: true})
	_, err := r.Score(context.Background(), "p", "h")
	if !errors.Is(err, ErrNoProvider) {
		t.Errorf("err=%v, want ErrNoProvider", err)
	}
}

// ---- Input validation runs ONCE on the entry, no provider called ----

func TestRouter_InputValidation_NoProviderCalled(t *testing.T) {
	t.Parallel()
	primary := &fakeProvider{id: "primary"}
	primary.Program(Score{Label: LabelEntailment, Confidence: 0.5}, nil, 0)
	r := newTestRouter(t, primary, nil, Config{FallbackEnabled: false})
	if _, err := r.Score(context.Background(), "", "h"); !errors.Is(err, ErrInputEmpty) {
		t.Errorf("empty premise: err=%v", err)
	}
	if _, err := r.Score(context.Background(), "p", ""); !errors.Is(err, ErrInputEmpty) {
		t.Errorf("empty hypothesis: err=%v", err)
	}
	if primary.calls.Load() != 0 {
		t.Errorf("primary should not be called for empty inputs: %d", primary.calls.Load())
	}

	longP := make([]byte, 70_000)
	r.maxPBytes = 100
	if _, err := r.Score(context.Background(), string(longP), "h"); !errors.Is(err, ErrInputTooLarge) {
		t.Errorf("oversized premise: err=%v", err)
	}
	if primary.calls.Load() != 0 {
		t.Errorf("primary should not be called for oversized input: %d", primary.calls.Load())
	}
}

// ---- Latency budget enforced ----

func TestRouter_LatencyBudget_CancelsPrimary(t *testing.T) {
	t.Parallel()
	primary := &fakeProvider{id: "primary"}
	// Slow but eventually returns OK — should be canceled by budget.
	primary.Program(Score{Label: LabelEntailment, Confidence: 0.9}, nil, 1*time.Second)
	fb := &fakeProvider{id: "fb"}
	fb.Program(Score{Label: LabelContradiction, Confidence: 0.8}, nil, 0)
	r := newTestRouter(t, primary, fb, Config{FallbackEnabled: true, LatencyBudgetMS: 50})
	start := time.Now()
	got, err := r.Score(context.Background(), "p", "h")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if got.ProviderID != "fb" {
		t.Errorf("ProviderID=%q, want fb (budget should cancel primary)", got.ProviderID)
	}
	// Should have completed within budget + small slack.
	if elapsed > 500*time.Millisecond {
		t.Errorf("elapsed=%v, want < 500ms (budget + fallback)", elapsed)
	}
}

// ---- Concurrent calls ----

func TestRouter_Concurrent_RaceFree(t *testing.T) {
	t.Parallel()
	primary := &fakeProvider{id: "primary"}
	primary.Program(Score{Label: LabelEntailment, Confidence: 0.5}, nil, 0)
	r := newTestRouter(t, primary, nil, Config{FallbackEnabled: false})
	const N = 100
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, _ = r.Score(context.Background(), "p", "h")
		}()
	}
	wg.Wait()
	if got := r.Stats().PrimarySuccesses; got != N {
		t.Errorf("PrimarySuccesses=%d, want %d", got, N)
	}
	if got := primary.calls.Load(); got != N {
		t.Errorf("primary.calls=%d, want %d", got, N)
	}
}

// ---- Stats and Reset ----

func TestRouter_StatsAndReset(t *testing.T) {
	t.Parallel()
	primary := &fakeProvider{id: "primary"}
	primary.Program(Score{Label: LabelEntailment, Confidence: 0.5}, nil, 0)
	fb := &fakeProvider{id: "fb"}
	fb.Program(Score{Label: LabelContradiction, Confidence: 0.5}, nil, 0)
	r := newTestRouter(t, primary, fb, Config{FallbackEnabled: true})
	// 5 success + 3 bad-response
	for i := 0; i < 5; i++ {
		_, _ = r.Score(context.Background(), "p", "h")
	}
	primary.Program(Score{}, ErrProviderBadResponse, 0)
	for i := 0; i < 3; i++ {
		_, _ = r.Score(context.Background(), "p", "h")
	}
	st := r.Stats()
	if st.PrimarySuccesses != 5 {
		t.Errorf("PrimarySuccesses=%d, want 5", st.PrimarySuccesses)
	}
	if st.PrimaryFailures != 3 {
		t.Errorf("PrimaryFailures=%d, want 3", st.PrimaryFailures)
	}
	if st.FallbackSkipped != 3 {
		t.Errorf("FallbackSkipped=%d, want 3", st.FallbackSkipped)
	}
	if st.FallbackSuccesses != 0 {
		t.Errorf("FallbackSuccesses=%d, want 0 (fallback not triggered)", st.FallbackSuccesses)
	}
	r.Reset()
	st = r.Stats()
	if st.PrimarySuccesses != 0 || st.PrimaryFailures != 0 || st.FallbackSkipped != 0 {
		t.Errorf("after Reset: %+v", st)
	}
}

// ---- Score returned by router is Valid() ----

func TestRouter_ScoreIsValid(t *testing.T) {
	t.Parallel()
	primary := &fakeProvider{id: "primary"}
	primary.Program(Score{Label: LabelNeutral, Confidence: 0.42, ModelRev: "v1"}, nil, 0)
	r := newTestRouter(t, primary, nil, Config{FallbackEnabled: false})
	got, err := r.Score(context.Background(), "p", "h")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if !got.Valid() {
		t.Errorf("Router returned invalid Score: %+v", got)
	}
}

// ---- Provenance: fallback ProviderID is propagated ----

func TestRouter_Provenance_FromFallback(t *testing.T) {
	t.Parallel()
	primary := &fakeProvider{id: "deberta-v3-large-mnli"}
	primary.Program(Score{}, ErrProviderTimeout, 0)
	fb := &fakeProvider{id: "minicheck-flan-t5-large"}
	fb.Program(Score{Label: LabelContradiction, Confidence: 0.7, ModelRev: "rev-x"}, nil, 0)
	r := newTestRouter(t, primary, fb, Config{FallbackEnabled: true})
	got, err := r.Score(context.Background(), "p", "h")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if got.ProviderID != "minicheck-flan-t5-large" {
		t.Errorf("ProviderID=%q, want %q", got.ProviderID, "minicheck-flan-t5-large")
	}
	if got.ModelRev != "rev-x" {
		t.Errorf("ModelRev=%q, want rev-x (from fallback)", got.ModelRev)
	}
}