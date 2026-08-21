package nli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// readCloserFromString builds a minimal io.ReadCloser carrying s.
func readCloserFromString(s string) io.ReadCloser {
	return io.NopCloser(bytes.NewBufferString(s))
}

// stubHTTPClient is a minimal in-test HFInferenceClient that records the
// request body and supplies a canned response. Avoids httptest.Server
// overhead when the test only cares about the wire format.
type stubHTTPClient struct {
	reqBody   []byte
	resp      *http.Response
	respErr   error
	gotMethod string
	gotURL    string
	gotAuth   string
	calls     atomic.Int32
}

func (s *stubHTTPClient) Do(req *http.Request) (*http.Response, error) {
	s.calls.Add(1)
	s.gotMethod = req.Method
	s.gotURL = req.URL.String()
	s.gotAuth = req.Header.Get("Authorization")
	if req.Body != nil {
		buf := make([]byte, req.ContentLength)
		_, _ = req.Body.Read(buf)
		s.reqBody = buf
	}
	return s.resp, s.respErr
}

func newDeBERTaDefault(t *testing.T, hc HFInferenceClient) *DeBERTaProvider {
	t.Helper()
	p, err := NewDeBERTaProvider(ProviderConfig{
		ProviderID: "deberta-v3-large-mnli",
		Endpoint:   "https://example.test/score",
		AuthToken:  "hf_test_token",
		ModelRev:   "rev-abc",
	}, hc, DefaultMaxPremiseBytes, DefaultMaxHypothesisBytes)
	if err != nil {
		t.Fatalf("NewDeBERTaProvider: %v", err)
	}
	return p
}

// ---- DeBERTa: happy path ----

func TestDeBERTaProvider_HappyPath(t *testing.T) {
	t.Parallel()
	stub := &stubHTTPClient{}
	stub.resp = &http.Response{
		StatusCode: 200,
		Body:       readCloserFromString(`[{"label":"LABEL_2","score":0.97},{"label":"LABEL_0","score":0.02},{"label":"LABEL_1","score":0.01}]`),
	}
	p := newDeBERTaDefault(t, stub)

	got, err := p.Score(context.Background(), "the sky is blue", "the sky has color")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if got.Label != LabelEntailment {
		t.Errorf("Label=%v, want %v", got.Label, LabelEntailment)
	}
	if got.Confidence != 0.97 {
		t.Errorf("Confidence=%v, want 0.97", got.Confidence)
	}
	if got.ProviderID != "deberta-v3-large-mnli" {
		t.Errorf("ProviderID=%q, want %q", got.ProviderID, "deberta-v3-large-mnli")
	}
	if got.ModelRev != "rev-abc" {
		t.Errorf("ModelRev=%q, want rev-abc", got.ModelRev)
	}
	if !got.Valid() {
		t.Errorf("Score failed invariant: %+v", got)
	}
	if !bytes.Contains(stub.reqBody, []byte("[SEP]")) {
		t.Errorf("payload missing [SEP]: %s", string(stub.reqBody))
	}
	if !bytes.Contains(stub.reqBody, []byte(`"top_k":3`)) {
		t.Errorf("payload missing top_k=3: %s", string(stub.reqBody))
	}
	if stub.gotAuth != "Bearer hf_test_token" {
		t.Errorf("auth=%q, want %q", stub.gotAuth, "Bearer hf_test_token")
	}
}

// ---- DeBERTa: label mapping ----

func TestDeBERTaProvider_LabelMapOverride(t *testing.T) {
	t.Parallel()
	stub := &stubHTTPClient{}
	stub.resp = &http.Response{StatusCode: 200, Body: readCloserFromString(`[{"label":"ENTAILMENT","score":0.9}]`)}
	p, err := NewDeBERTaProvider(ProviderConfig{
		ProviderID: "deberta-v3-large-mnli",
		Endpoint:   "https://example.test/score",
		AuthToken:  "tok",
		LabelMap:   map[string]Label{"ENTAILMENT": LabelNeutral}, // intentionally wrong to test override
	}, stub, 1024, 1024)
	if err != nil {
		t.Fatalf("NewDeBERTaProvider: %v", err)
	}
	got, err := p.Score(context.Background(), "a", "b")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if got.Label != LabelNeutral {
		t.Errorf("Label=%v, want %v (override)", got.Label, LabelNeutral)
	}
}

// ---- DeBERTa: HTTP error classification ----

func TestDeBERTaProvider_HTTPStatusMapping(t *testing.T) {
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
		{502, ErrProviderUnavailable},
		{503, ErrProviderUnavailable},
		{400, ErrProviderBadResponse}, // 4xx other than 401/403/429 → contract bug
		{418, ErrProviderBadResponse},
		{301, ErrProviderUnavailable}, // unexpected redirect
	}
	for _, tt := range tests {
		tt := tt
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			t.Parallel()
			stub := &stubHTTPClient{}
			body := `[{"label":"LABEL_2","score":0.5}]`
			if tt.status != 200 {
				body = `{"error":"upstream"}`
			}
			stub.resp = &http.Response{StatusCode: tt.status, Body: readCloserFromString(body)}
			p := newDeBERTaDefault(t, stub)
			_, err := p.Score(context.Background(), "a", "b")
			if tt.want == nil {
				if err != nil {
					t.Errorf("status %d: unexpected err=%v", tt.status, err)
				}
				return
			}
			if err == nil {
				t.Errorf("status %d: expected err=%v, got nil", tt.status, tt.want)
				return
			}
			if !errors.Is(err, tt.want) {
				t.Errorf("status %d: err=%v, want %v", tt.status, err, tt.want)
			}
		})
	}
}

// ---- DeBERTa: malformed response ----

func TestDeBERTaProvider_BadResponse_EmptyArray(t *testing.T) {
	t.Parallel()
	stub := &stubHTTPClient{}
	stub.resp = &http.Response{StatusCode: 200, Body: readCloserFromString(`[]`)}
	p := newDeBERTaDefault(t, stub)
	_, err := p.Score(context.Background(), "a", "b")
	if !errors.Is(err, ErrProviderBadResponse) {
		t.Errorf("err=%v, want ErrProviderBadResponse", err)
	}
}

func TestDeBERTaProvider_BadResponse_NotJSON(t *testing.T) {
	t.Parallel()
	stub := &stubHTTPClient{}
	stub.resp = &http.Response{StatusCode: 200, Body: readCloserFromString(`not json`)}
	p := newDeBERTaDefault(t, stub)
	_, err := p.Score(context.Background(), "a", "b")
	if !errors.Is(err, ErrProviderBadResponse) {
		t.Errorf("err=%v, want ErrProviderBadResponse", err)
	}
}

func TestDeBERTaProvider_BadResponse_OutOfRangeScore(t *testing.T) {
	t.Parallel()
	stub := &stubHTTPClient{}
	stub.resp = &http.Response{StatusCode: 200, Body: readCloserFromString(`[{"label":"LABEL_2","score":1.5}]`)}
	p := newDeBERTaDefault(t, stub)
	_, err := p.Score(context.Background(), "a", "b")
	if !errors.Is(err, ErrProviderBadResponse) {
		t.Errorf("err=%v, want ErrProviderBadResponse", err)
	}
}

func TestDeBERTaProvider_BadResponse_UnknownLabel(t *testing.T) {
	t.Parallel()
	stub := &stubHTTPClient{}
	stub.resp = &http.Response{StatusCode: 200, Body: readCloserFromString(`[{"label":"LABEL_99","score":0.5}]`)}
	p := newDeBERTaDefault(t, stub)
	_, err := p.Score(context.Background(), "a", "b")
	if !errors.Is(err, ErrProviderBadResponse) {
		t.Errorf("err=%v, want ErrProviderBadResponse", err)
	}
}

func TestDeBERTaProvider_BadResponse_EmptyLabelField(t *testing.T) {
	t.Parallel()
	stub := &stubHTTPClient{}
	stub.resp = &http.Response{StatusCode: 200, Body: readCloserFromString(`[{"label":"","score":0.5}]`)}
	p := newDeBERTaDefault(t, stub)
	_, err := p.Score(context.Background(), "a", "b")
	if !errors.Is(err, ErrProviderBadResponse) {
		t.Errorf("err=%v, want ErrProviderBadResponse", err)
	}
}

// ---- DeBERTa: input validation ----

func TestDeBERTaProvider_InputValidation(t *testing.T) {
	t.Parallel()
	stub := &stubHTTPClient{}
	stub.resp = &http.Response{StatusCode: 200, Body: readCloserFromString(`[{"label":"LABEL_2","score":0.5}]`)}
	p := newDeBERTaDefault(t, stub)
	ctx := context.Background()

	if _, err := p.Score(ctx, "", "h"); !errors.Is(err, ErrInputEmpty) {
		t.Errorf("empty premise: err=%v, want ErrInputEmpty", err)
	}
	if _, err := p.Score(ctx, "p", ""); !errors.Is(err, ErrInputEmpty) {
		t.Errorf("empty hypothesis: err=%v, want ErrInputEmpty", err)
	}

	long := bytes.Repeat([]byte("a"), DefaultMaxPremiseBytes+1)
	if _, err := p.Score(ctx, string(long), "h"); !errors.Is(err, ErrInputTooLarge) {
		t.Errorf("oversized premise: err=%v, want ErrInputTooLarge", err)
	}
	longH := bytes.Repeat([]byte("a"), DefaultMaxHypothesisBytes+1)
	if _, err := p.Score(ctx, "p", string(longH)); !errors.Is(err, ErrInputTooLarge) {
		t.Errorf("oversized hypothesis: err=%v, want ErrInputTooLarge", err)
	}
}

// ---- DeBERTa: tie-break in canonical priority ----

func TestDeBERTaProvider_TieBreak(t *testing.T) {
	t.Parallel()
	stub := &stubHTTPClient{}
	stub.resp = &http.Response{StatusCode: 200, Body: readCloserFromString(`[{"label":"LABEL_0","score":0.34},{"label":"LABEL_1","score":0.34},{"label":"LABEL_2","score":0.34}]`)}
	p := newDeBERTaDefault(t, stub)
	got, err := p.Score(context.Background(), "p", "h")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if got.Label != LabelEntailment {
		t.Errorf("tie-break: Label=%v, want entailment", got.Label)
	}
}

// ---- DeBERTa: timeout → ErrProviderTimeout ----

func TestDeBERTaProvider_Timeout(t *testing.T) {
	t.Parallel()
	srv := hangServer(t)
	defer srv.Close()
	cfg := ProviderConfig{
		ProviderID: "deberta-v3-large-mnli",
		Endpoint:   srv.URL,
		AuthToken:  "tok",
		TimeoutMS:  100,
	}
	p, err := NewDeBERTaProvider(cfg, http.DefaultClient, 1024, 1024)
	if err != nil {
		t.Fatalf("NewDeBERTaProvider: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err = p.Score(ctx, "p", "h")
	if !errors.Is(err, ErrProviderTimeout) {
		t.Errorf("err=%v, want ErrProviderTimeout", err)
	}
}

// ---- DeBERTa: connection refused → ErrProviderUnavailable ----

func TestDeBERTaProvider_ConnectionRefused(t *testing.T) {
	t.Parallel()
	cfg := ProviderConfig{
		ProviderID: "deberta-v3-large-mnli",
		Endpoint:   "http://127.0.0.1:1", // port 1: connection refused
		AuthToken:  "tok",
		TimeoutMS:  1000,
	}
	p, err := NewDeBERTaProvider(cfg, http.DefaultClient, 1024, 1024)
	if err != nil {
		t.Fatalf("NewDeBERTaProvider: %v", err)
	}
	_, err = p.Score(context.Background(), "p", "h")
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Errorf("err=%v, want ErrProviderUnavailable", err)
	}
}

// ---- DeBERTa: construction validation ----

func TestNewDeBERTaProvider_Validation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     ProviderConfig
		hc      HFInferenceClient
		wantErr error
	}{
		{"empty ProviderID", ProviderConfig{Endpoint: "x", AuthToken: "y"}, &stubHTTPClient{}, ErrInvalidConfig},
		{"empty Endpoint", ProviderConfig{ProviderID: "x", AuthToken: "y"}, &stubHTTPClient{}, ErrInvalidConfig},
		{"nil client", ProviderConfig{ProviderID: "x", Endpoint: "y", AuthToken: "z"}, nil, ErrInvalidConfig},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewDeBERTaProvider(tt.cfg, tt.hc, 1024, 1024)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("err=%v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestNewDeBERTaProvider_NegativeCaps(t *testing.T) {
	t.Parallel()
	if _, err := NewDeBERTaProvider(ProviderConfig{ProviderID: "x", Endpoint: "y", AuthToken: "z"}, &stubHTTPClient{}, -1, 1024); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("negative premise cap: err=%v", err)
	}
	if _, err := NewDeBERTaProvider(ProviderConfig{ProviderID: "x", Endpoint: "y", AuthToken: "z"}, &stubHTTPClient{}, 1024, -1); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("negative hypothesis cap: err=%v", err)
	}
}

// ---- helpers ----

// hangServer returns an httptest.Server that sleeps longer than the
// expected client timeout. Used to force ErrProviderTimeout
// deterministically — the client gives up while the server is still
// sleeping, then srv.Close in the test ends the sleep.
func hangServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
}