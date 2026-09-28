package research

import (
	"strings"
	"testing"
	"time"
)

// TestAllParsersHaveFixtures enforces the contract: every parser
// registered in the parsers map has a matching fixture. A new
// parser without a fixture fails this test, not the snapshot
// test (which would skip with a confusing message).
func TestAllParsersHaveFixtures(t *testing.T) {
	for name := range parsers {
		if _, ok := allFixtures[name]; !ok {
			t.Errorf("parser %q has no fixture; add one to fixtures.go", name)
		}
	}
	for name := range allFixtures {
		if _, ok := parsers[name]; !ok {
			t.Errorf("fixture %q has no parser; add one to parsers.go", name)
		}
	}
}

// TestSnapshot_Parsers runs every parser against its fixture and
// checks the minimum contract: at least one Item, the Intent is
// set, the Sources has at least one entry, and no error. Per-parser
// shape checks live in the individual TestParse* functions below.
func TestSnapshot_Parsers(t *testing.T) {
	for name, fn := range parsers {
		body, ok := allFixtures[name]
		if !ok {
			continue
		}
		t.Run(name, func(t *testing.T) {
			r, err := fn([]byte(body), "CVE-2021-44228")
			if err != nil {
				t.Fatalf("parse %q: %v", name, err)
			}
			if len(r.Items) == 0 {
				t.Fatalf("%q: no items parsed", name)
			}
			if r.Intent == "" {
				t.Fatalf("%q: empty intent", name)
			}
			if len(r.Sources) == 0 {
				t.Fatalf("%q: no sources", name)
			}
		})
	}
}

// Per-parser shape checks. These are the specific assertions per
// backend (which fields map to which Item fields). They run after
// the snapshot test above; if a parser breaks, the per-parser
// test points to the exact field, not just "broken".

func TestParseOSV_Shape(t *testing.T) {
	r, err := parseOSV([]byte(fixtureOSV), "CVE-2021-44228")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(r.Items))
	}
	if !strings.HasPrefix(r.Items[0].Title, "GHSA-jfh8-c2jp-5v3q") {
		t.Errorf("title = %q, want prefix GHSA-jfh8-c2jp-5v3q", r.Items[0].Title)
	}
	if r.Items[0].Confidence < 0.9 {
		t.Errorf("confidence = %f, want >= 0.9", r.Items[0].Confidence)
	}
}

func TestParseNVD_Shape(t *testing.T) {
	r, err := parseNVD([]byte(fixtureNVD), "CVE-2021-44228")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(r.Items))
	}
	if !strings.Contains(r.Items[0].Title, "CRITICAL") {
		t.Errorf("title = %q, want CRITICAL marker", r.Items[0].Title)
	}
}

func TestParseNPM_Shape(t *testing.T) {
	r, err := parseNPM([]byte(fixtureNPM), "express")
	if err != nil {
		t.Fatal(err)
	}
	if r.Items[0].Title != "express@4.21.1" {
		t.Errorf("title = %q, want express@4.21.1", r.Items[0].Title)
	}
}

func TestParseCrates_Shape(t *testing.T) {
	r, err := parseCrates([]byte(fixtureCrates), "serde")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(r.Items[0].Title, "serde@") {
		t.Errorf("title = %q, want serde@", r.Items[0].Title)
	}
	if len(r.Items) < 2 {
		t.Errorf("expected at least 2 items (current + recent), got %d", len(r.Items))
	}
}

func TestParseGitHub_Shape(t *testing.T) {
	r, err := parseGitHub([]byte(fixtureGitHub), "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Items[0].Title, "sqlite/sqlite") {
		t.Errorf("title = %q, want sqlite/sqlite", r.Items[0].Title)
	}
	if !strings.Contains(r.Items[0].Title, "★") {
		t.Errorf("title = %q, want ★ marker", r.Items[0].Title)
	}
}

func TestParseOpenAlex_Shape(t *testing.T) {
	r, err := parseOpenAlex([]byte(fixtureOpenAlex), "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if r.Items[0].Title == "" {
		t.Errorf("empty title")
	}
}

func TestParseCrossref_Shape(t *testing.T) {
	r, err := parseCrossref([]byte(fixtureCrossref), "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Items[0].Title, "SQLite") {
		t.Errorf("title = %q, want SQLite prefix", r.Items[0].Title)
	}
}

func TestParseArXiv_Shape(t *testing.T) {
	r, err := parseArXiv([]byte(fixtureArXiv), "prometheus 2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Items[0].Title, "Prometheus 2") {
		t.Errorf("title = %q, want Prometheus 2", r.Items[0].Title)
	}
}

func TestParseRDAP_Shape(t *testing.T) {
	r, err := parseRDAP([]byte(fixtureRDAP), "google.com")
	if err != nil {
		t.Fatal(err)
	}
	if r.Items[0].Title != "google.com" {
		t.Errorf("title = %q, want google.com", r.Items[0].Title)
	}
}

func TestParseDoH_Shape(t *testing.T) {
	r, err := parseDoH([]byte(fixtureDoH), "google.com")
	if err != nil {
		t.Fatal(err)
	}
	if r.Items[0].Title != "google.com." {
		t.Errorf("title = %q, want google.com.", r.Items[0].Title)
	}
	if !strings.Contains(r.Items[0].Snippet, "142.250.80.46") {
		t.Errorf("snippet = %q, want 142.250.80.46", r.Items[0].Snippet)
	}
}

func TestParseIPAPI_Shape(t *testing.T) {
	r, err := parseIPAPI([]byte(fixtureIPAPI), "8.8.8.8")
	if err != nil {
		t.Fatal(err)
	}
	if r.Items[0].Title != "8.8.8.8" {
		t.Errorf("title = %q, want 8.8.8.8", r.Items[0].Title)
	}
}

func TestParseRIPE_Shape(t *testing.T) {
	r, err := parseRIPE([]byte(fixtureRIPE), "8.8.8.8")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(r.Items[0].URL, "https://") {
		t.Errorf("url = %q, want https://", r.Items[0].URL)
	}
}

func TestParseCRT_Shape(t *testing.T) {
	r, err := parseCRT([]byte(fixtureCRT), "google.com")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Items[0].URL, "crt.sh/?id=") {
		t.Errorf("url = %q, want crt.sh/?id=", r.Items[0].URL)
	}
}

func TestParseHIBP_Shape(t *testing.T) {
	r, err := parseHIBP([]byte(fixtureHIBP), "ABCDE")
	if err != nil {
		t.Fatal(err)
	}
	// 2 non-zero count lines + 1 zero-count line we skip.
	if len(r.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(r.Items))
	}
	for _, it := range r.Items {
		if !strings.HasPrefix(it.Title, "ABCDE:") {
			t.Errorf("title = %q, want ABCDE: prefix", it.Title)
		}
	}
}

func TestParseNominatim_Shape(t *testing.T) {
	r, err := parseNominatim([]byte(fixtureNominatim), "Berlin")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Items[0].Title, "Berlin") {
		t.Errorf("title = %q, want Berlin", r.Items[0].Title)
	}
}

func TestParseHN_Shape(t *testing.T) {
	r, err := parseHN([]byte(fixtureHN), "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if r.Items[0].Title == "" {
		t.Errorf("empty title")
	}
}

func TestParseGDELT_Shape(t *testing.T) {
	r, err := parseGDELT([]byte(fixtureGDELT), "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if r.Items[0].Title == "" {
		t.Errorf("empty title")
	}
}

func TestParseDDG_Shape(t *testing.T) {
	r, err := parseDDG([]byte(fixtureDDG), "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	// DDG wraps target URLs in /l/?uddg=...; we expect to extract
	// at least 1 result from the 2 anchors in the fixture.
	if len(r.Items) < 1 {
		t.Fatalf("expected at least 1 item, got %d", len(r.Items))
	}
	for _, it := range r.Items {
		if it.Title == "" {
			t.Errorf("empty title in DDG result")
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("truncate short = %q", got)
	}
	if got := truncate("hello world", 5); got != "hello…" {
		t.Errorf("truncate long = %q, want hello…", got)
	}
}

// ---------- Merge tests ----------

func TestMerge_EntityResolution(t *testing.T) {
	now := time.Now()
	results := []Result{
		{
			Intent: IntentCVE, Query: "CVE-2021-44228", Sources: []string{"osv.dev"},
			Items: []Item{{Title: "CVE-2021-44228", URL: "https://osv.dev/vulnerability/CVE-2021-44228", Snippet: "OSV summary", Confidence: 0.95}},
			FetchedAt: now,
		},
		{
			Intent: IntentCVE, Query: "CVE-2021-44228", Sources: []string{"nvd.nist.gov"},
			Items: []Item{{Title: "CVE-2021-44228", URL: "https://nvd.nist.gov/vuln/detail/CVE-2021-44228", Snippet: "NVD summary", Confidence: 0.95}},
			FetchedAt: now,
		},
	}
	out := Merge(results, VibeC1Code, now, func(Intent) time.Time { return now.Add(48 * time.Hour) })
	if len(out) != 1 {
		t.Fatalf("expected 1 evidence (corroborated), got %d", len(out))
	}
	if len(out[0].CorroboratedBy) != 2 {
		t.Errorf("expected 2 corroborating backends, got %d", len(out[0].CorroboratedBy))
	}
}

func TestMerge_RelevanceOrdering(t *testing.T) {
	now := time.Now()
	exp := now.Add(time.Hour)
	results := []Result{
		{
			Intent: IntentCode, Sources: []string{"registry.npmjs.org"},
			Items: []Item{{Title: "low", Snippet: "low", Confidence: 0.3}},
			FetchedAt: now,
		},
		{
			Intent: IntentCode, Sources: []string{"crates.io"},
			Items: []Item{{Title: "high", Snippet: "high", Confidence: 0.9}},
			FetchedAt: now,
		},
	}
	out := Merge(results, VibeC1Code, now, func(Intent) time.Time { return exp })
	if out[0].Title != "high" {
		t.Errorf("expected high relevance first, got %q", out[0].Title)
	}
}

func TestRelevanceScore_Freshness(t *testing.T) {
	now := time.Now()
	fetched := now.Add(-30 * time.Minute)
	expires := now.Add(30 * time.Minute) // 1 hour total
	score := RelevanceScore(1.0, 1.0, 0, fetched, expires, now)
	// At 30m of 1h elapsed, freshness = 0.5. score = 1.0 * 1.0 * 1.0 * 0.5 = 0.5
	if score < 0.4 || score > 0.6 {
		t.Errorf("expected ~0.5 at 30m/1h, got %f", score)
	}
}

func TestRelevanceScore_Corroboration(t *testing.T) {
	now := time.Now()
	s1 := RelevanceScore(1.0, 1.0, 0, now, now.Add(time.Hour), now)
	s3 := RelevanceScore(1.0, 1.0, 3, now, now.Add(time.Hour), now)
	if s3 <= s1 {
		t.Errorf("3-source corroboration (%f) should exceed 1-source (%f)", s3, s1)
	}
	if s3 < 1.2 || s3 > 1.4 {
		t.Errorf("expected ~1.3 with 3 sources, got %f", s3)
	}
}

func TestRelevanceScore_NoUpperClamp(t *testing.T) {
	now := time.Now()
	// All factors maxed: 1.0 × 1.0 × 1.3 × 1.0 = 1.3. The function
	// does NOT clamp at 1.0; the downstream consumer handles that.
	score := RelevanceScore(1.0, 1.0, 10, now, now.Add(time.Hour), now)
	if score < 1.29 || score > 1.31 {
		t.Errorf("expected ~1.3 (no upper clamp), got %f", score)
	}
}

// ---------- Cache tests ----------

func TestCache_FreshnessTransitions(t *testing.T) {
	clock := time.Now
	c := NewCache(clock)
	cfg := CacheConfig{SoftTTL: 10 * time.Minute, StaleTTL: 5 * time.Minute}
	r := &Result{Intent: IntentCode, Query: "x"}
	t0 := time.Now()
	c.Store("backend", "key", r, t0, cfg)

	// Fresh at t0
	_, _, _, state, ok := c.Lookup("backend", "key")
	if !ok || state != CacheFresh {
		t.Fatalf("expected fresh, got state=%d ok=%v", state, ok)
	}

	// Stale at t0 + 11 min (past soft, before hard)
	clock = func() time.Time { return t0.Add(11 * time.Minute) }
	c.now = clock
	_, _, _, state, ok = c.Lookup("backend", "key")
	if !ok || state != CacheStale {
		t.Fatalf("expected stale at 11m, got state=%d ok=%v", state, ok)
	}

	// Expired at t0 + 16 min (past hard)
	clock = func() time.Time { return t0.Add(16 * time.Minute) }
	c.now = clock
	_, _, _, state, ok = c.Lookup("backend", "key")
	if !ok || state != CacheExpired {
		t.Fatalf("expected expired at 16m, got state=%d ok=%v", state, ok)
	}
}

func TestCache_Missing(t *testing.T) {
	c := NewCache(nil)
	_, _, _, state, ok := c.Lookup("backend", "nope")
	if ok {
		t.Errorf("expected !ok for missing key")
	}
	if state != CacheMissing {
		t.Errorf("expected CacheMissing, got %d", state)
	}
}

func TestCache_InvalidateBackend(t *testing.T) {
	c := NewCache(nil)
	cfg := CacheConfig{SoftTTL: time.Hour, StaleTTL: time.Hour}
	c.Store("a", "k1", &Result{}, time.Now(), cfg)
	c.Store("a", "k2", &Result{}, time.Now(), cfg)
	c.Store("b", "k1", &Result{}, time.Now(), cfg)
	if c.Size() != 3 {
		t.Fatalf("expected 3, got %d", c.Size())
	}
	c.InvalidateBackend("a")
	if c.Size() != 1 {
		t.Errorf("expected 1 after InvalidateBackend(a), got %d", c.Size())
	}
}

// ---------- Health tests ----------

func TestHealth_RecordSuccess_ResetsConsecutive(t *testing.T) {
	h := NewHealth()
	now := time.Now()
	h.RecordError(errFake, now)
	h.RecordError(errFake, now)
	h.RecordSuccess(now)
	if h.Snapshot().ConsecutiveErrors != 0 {
		t.Errorf("expected 0 consecutive after success, got %d", h.Snapshot().ConsecutiveErrors)
	}
	if h.Snapshot().Status != StatusHealthy {
		t.Errorf("expected healthy after success, got %s", h.Snapshot().Status)
	}
}

func TestHealth_DowngradeAfter3(t *testing.T) {
	h := NewHealth()
	now := time.Now()
	for i := 0; i < DegradeAfter; i++ {
		h.RecordError(errFake, now)
	}
	if h.Snapshot().Status != StatusDegraded {
		t.Errorf("expected Degraded after %d errors, got %s", DegradeAfter, h.Snapshot().Status)
	}
}

func TestHealth_DeadAfter10(t *testing.T) {
	h := NewHealth()
	now := time.Now()
	for i := 0; i < KillAfter; i++ {
		h.RecordError(errFake, now)
	}
	if h.Snapshot().Status != StatusDead {
		t.Errorf("expected Dead after %d errors, got %s", KillAfter, h.Snapshot().Status)
	}
}

func TestHealth_DeadRecovers(t *testing.T) {
	h := NewHealth()
	now := time.Now()
	for i := 0; i < KillAfter; i++ {
		h.RecordError(errFake, now)
	}
	if h.Snapshot().Status != StatusDead {
		t.Fatal("setup")
	}
	// Operator runs ?refresh=true. A success on the next call
	// brings the backend back to Healthy.
	h.RecordSuccess(now)
	if h.Snapshot().Status != StatusHealthy {
		t.Errorf("expected healthy after recovery, got %s", h.Snapshot().Status)
	}
}

func TestHealth_NotCallable_Dead(t *testing.T) {
	h := NewHealth()
	now := time.Now()
	for i := 0; i < KillAfter; i++ {
		h.RecordError(errFake, now)
	}
	if h.IsCallable() {
		t.Error("Dead backend should not be callable")
	}
}

// errFake is a tiny stand-in for error in tests.
var errFake = errFakeT{}

type errFakeT struct{}

func (errFakeT) Error() string { return "fake" }
