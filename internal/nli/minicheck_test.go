package nli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newMiniCheckDefault(t *testing.T, hc HFInferenceClient) *MiniCheckProvider {
	t.Helper()
	p, err := NewMiniCheckProvider(ProviderConfig{
		ProviderID: "minicheck-flan-t5-large",
		Endpoint:   "https://example.test/score",
		AuthToken:  "minicheck_test_token",
		ModelRev:   "rev-minicheck-001",
	}, hc, DefaultMaxPremiseBytes, DefaultMaxHypothesisBytes)
	if err != nil {
		t.Fatalf("NewMiniCheckProvider: %v", err)
	}
	return p
}

// ---- MiniCheck: happy path with score field ----

func TestMiniCheckProvider_HappyPath_ScoreField(t *testing.T) {
	t.Parallel()
	stub := &stubHTTPClient{}
	stub.resp = &http.Response{StatusCode: 200, Body: readCloserFromString(`{"score":0.95,"label":1}`)}
	p := newMiniCheckDefault(t, stub)
	got, err := p.Score(context.Background(), "doc says the sky is blue", "claim: sky is blue")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if got.Label != LabelEntailment {
		t.Errorf("Label=%v, want entailment (0.95 >= 0.7)", got.Label)
	}
	if got.Confidence != 0.95 {
		t.Errorf("Confidence=%v, want 0.95", got.Confidence)
	}
	if got.ProviderID != "minicheck-flan-t5-large" {
		t.Errorf("ProviderID=%q, want minicheck-flan-t5-large", got.ProviderID)
	}
	if !got.Valid() {
		t.Errorf("Score failed invariant: %+v", got)
	}
}

// ---- MiniCheck: raw_prob field ----

func TestMiniCheckProvider_RawProbField(t *testing.T) {
	t.Parallel()
	stub := &stubHTTPClient{}
	stub.resp = &http.Response{StatusCode: 200, Body: readCloserFromString(`{"raw_prob":0.05}`)}
	p := newMiniCheckDefault(t, stub)
	got, err := p.Score(context.Background(), "d", "c")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if got.Label != LabelContradiction {
		t.Errorf("Label=%v, want contradiction (0.05 <= 0.3)", got.Label)
	}
}

// ---- MiniCheck: probability field ----

func TestMiniCheckProvider_ProbabilityField(t *testing.T) {
	t.Parallel()
	stub := &stubHTTPClient{}
	stub.resp = &http.Response{StatusCode: 200, Body: readCloserFromString(`{"probability":0.5}`)}
	p := newMiniCheckDefault(t, stub)
	got, err := p.Score(context.Background(), "d", "c")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if got.Label != LabelNeutral {
		t.Errorf("Label=%v, want neutral (in band 0.3..0.7)", got.Label)
	}
}

// ---- MiniCheck: legacy [P, 1-P] array ----

func TestMiniCheckProvider_ArrayShape(t *testing.T) {
	t.Parallel()
	stub := &stubHTTPClient{}
	stub.resp = &http.Response{StatusCode: 200, Body: readCloserFromString(`[0.85, 0.15]`)}
	p := newMiniCheckDefault(t, stub)
	got, err := p.Score(context.Background(), "d", "c")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if got.Label != LabelEntailment {
		t.Errorf("Label=%v, want entailment (0.85 >= 0.7)", got.Label)
	}
	if got.Confidence != 0.85 {
		t.Errorf("Confidence=%v, want 0.85", got.Confidence)
	}
}

// ---- MiniCheck: threshold override ----

func TestMiniCheckProvider_Thresholds(t *testing.T) {
	t.Parallel()
	stub := &stubHTTPClient{}
	stub.resp = &http.Response{StatusCode: 200, Body: readCloserFromString(`{"score":0.55}`)}
	p := newMiniCheckDefault(t, stub)
	// Tighten entailment to 0.9.
	if err := p.SetThresholds(0.9, 0.1); err != nil {
		t.Fatalf("SetThresholds: %v", err)
	}
	got, err := p.Score(context.Background(), "d", "c")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if got.Label != LabelNeutral {
		t.Errorf("Label=%v, want neutral (0.55 in 0.1..0.9)", got.Label)
	}
}

func TestMiniCheckProvider_SetThresholds_Validation(t *testing.T) {
	t.Parallel()
	p := newMiniCheckDefault(t, &stubHTTPClient{})
	if err := p.SetThresholds(0.5, 0.5); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("entailAt == contraAt: err=%v", err)
	}
	if err := p.SetThresholds(0.3, 0.5); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("entailAt < contraAt: err=%v", err)
	}
	if err := p.SetThresholds(1.5, 0.0); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("entailAt > 1: err=%v", err)
	}
	if err := p.SetThresholds(0.5, -0.1); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("contraAt < 0: err=%v", err)
	}
}

// ---- MiniCheck: HTTP errors ----

func TestMiniCheckProvider_HTTPStatusMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status int
		want   error
	}{
		{200, nil},
		{401, ErrProviderUnavailable},
		{403, ErrProviderUnavailable},
		{429, ErrProviderRateLimited},
		{500, ErrProviderUnavailable},
		{400, ErrProviderBadResponse},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			t.Parallel()
			stub := &stubHTTPClient{}
			body := `{"score":0.5}`
			if tt.status != 200 {
				body = `{}`
			}
			stub.resp = &http.Response{StatusCode: tt.status, Body: readCloserFromString(body)}
			p := newMiniCheckDefault(t, stub)
			_, err := p.Score(context.Background(), "a", "b")
			if tt.want == nil {
				if err != nil {
					t.Errorf("status %d: err=%v", tt.status, err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Errorf("status %d: err=%v, want %v", tt.status, err, tt.want)
			}
		})
	}
}

// ---- MiniCheck: bad response ----

func TestMiniCheckProvider_BadResponse_Empty(t *testing.T) {
	t.Parallel()
	stub := &stubHTTPClient{}
	stub.resp = &http.Response{StatusCode: 200, Body: readCloserFromString(``)}
	p := newMiniCheckDefault(t, stub)
	_, err := p.Score(context.Background(), "a", "b")
	if !errors.Is(err, ErrProviderBadResponse) {
		t.Errorf("err=%v, want ErrProviderBadResponse", err)
	}
}

func TestMiniCheckProvider_BadResponse_NoScoreField(t *testing.T) {
	t.Parallel()
	stub := &stubHTTPClient{}
	stub.resp = &http.Response{StatusCode: 200, Body: readCloserFromString(`{"foo":"bar"}`)}
	p := newMiniCheckDefault(t, stub)
	_, err := p.Score(context.Background(), "a", "b")
	if !errors.Is(err, ErrProviderBadResponse) {
		t.Errorf("err=%v, want ErrProviderBadResponse", err)
	}
}

func TestMiniCheckProvider_BadResponse_EmptyArray(t *testing.T) {
	t.Parallel()
	stub := &stubHTTPClient{}
	stub.resp = &http.Response{StatusCode: 200, Body: readCloserFromString(`[]`)}
	p := newMiniCheckDefault(t, stub)
	_, err := p.Score(context.Background(), "a", "b")
	if !errors.Is(err, ErrProviderBadResponse) {
		t.Errorf("err=%v, want ErrProviderBadResponse", err)
	}
}

func TestMiniCheckProvider_BadResponse_OutOfRange(t *testing.T) {
	t.Parallel()
	stub := &stubHTTPClient{}
	stub.resp = &http.Response{StatusCode: 200, Body: readCloserFromString(`{"score":-0.5}`)}
	p := newMiniCheckDefault(t, stub)
	_, err := p.Score(context.Background(), "a", "b")
	if !errors.Is(err, ErrProviderBadResponse) {
		t.Errorf("err=%v, want ErrProviderBadResponse", err)
	}
}

func TestMiniCheckProvider_BadResponse_ScoreFieldNotNumber(t *testing.T) {
	t.Parallel()
	stub := &stubHTTPClient{}
	stub.resp = &http.Response{StatusCode: 200, Body: readCloserFromString(`{"score":"0.5"}`)}
	p := newMiniCheckDefault(t, stub)
	_, err := p.Score(context.Background(), "a", "b")
	if !errors.Is(err, ErrProviderBadResponse) {
		t.Errorf("err=%v, want ErrProviderBadResponse", err)
	}
}

// ---- MiniCheck: input validation ----

func TestMiniCheckProvider_InputValidation(t *testing.T) {
	t.Parallel()
	stub := &stubHTTPClient{}
	stub.resp = &http.Response{StatusCode: 200, Body: readCloserFromString(`{"score":0.5}`)}
	p := newMiniCheckDefault(t, stub)
	ctx := context.Background()
	if _, err := p.Score(ctx, "", "h"); !errors.Is(err, ErrInputEmpty) {
		t.Errorf("empty doc: err=%v", err)
	}
	if _, err := p.Score(ctx, "p", ""); !errors.Is(err, ErrInputEmpty) {
		t.Errorf("empty claim: err=%v", err)
	}
	long := bytes.Repeat([]byte("a"), DefaultMaxPremiseBytes+1)
	if _, err := p.Score(ctx, string(long), "h"); !errors.Is(err, ErrInputTooLarge) {
		t.Errorf("oversized doc: err=%v", err)
	}
}

// ---- MiniCheck: real httptest.Server ----

func TestMiniCheckProvider_EndToEnd_HTTP(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify headers + body.
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type=%q", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("Authorization") != "Bearer minicheck_test_token" {
			t.Errorf("auth=%q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"score":0.7,"label":1}`))
	}))
	defer srv.Close()
	cfg := ProviderConfig{
		ProviderID: "minicheck-flan-t5-large",
		Endpoint:   srv.URL,
		AuthToken:  "minicheck_test_token",
	}
	p, err := NewMiniCheckProvider(cfg, http.DefaultClient, 1024, 1024)
	if err != nil {
		t.Fatalf("NewMiniCheckProvider: %v", err)
	}
	got, err := p.Score(context.Background(), "d", "c")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	// 0.7 is the boundary: >= 0.7 → entailment.
	if got.Label != LabelEntailment {
		t.Errorf("Label=%v, want entailment", got.Label)
	}
}

// ---- MiniCheck: construction validation ----

func TestNewMiniCheckProvider_Validation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     ProviderConfig
		hc      HFInferenceClient
		wantErr error
	}{
		{"empty ProviderID", ProviderConfig{Endpoint: "x"}, &stubHTTPClient{}, ErrInvalidConfig},
		{"empty Endpoint", ProviderConfig{ProviderID: "x"}, &stubHTTPClient{}, ErrInvalidConfig},
		{"nil client", ProviderConfig{ProviderID: "x", Endpoint: "y"}, nil, ErrInvalidConfig},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewMiniCheckProvider(tt.cfg, tt.hc, 1024, 1024)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("err=%v, want %v", err, tt.wantErr)
			}
		})
	}
}

// ---- Boundary mapping properties ----

func TestMapMiniCheckScore_Boundaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		score    float64
		entailAt float64
		contraAt float64
		want     Label
	}{
		{0.95, 0.7, 0.3, LabelEntailment},
		{0.7, 0.7, 0.3, LabelEntailment},   // boundary inclusive
		{0.5, 0.7, 0.3, LabelNeutral},
		{0.3, 0.7, 0.3, LabelContradiction}, // boundary inclusive
		{0.05, 0.7, 0.3, LabelContradiction},
		{0.0, 0.7, 0.3, LabelContradiction},
		{1.0, 0.7, 0.3, LabelEntailment},
	}
	for _, tt := range tests {
		got := mapMiniCheckScore(tt.score, tt.entailAt, tt.contraAt)
		if got != tt.want {
			t.Errorf("score=%v entail=%v contra=%v: got=%v want=%v",
				tt.score, tt.entailAt, tt.contraAt, got, tt.want)
		}
	}
}