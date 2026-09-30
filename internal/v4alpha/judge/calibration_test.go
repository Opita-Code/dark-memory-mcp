// L1 unit tests for calibration.go (ADR-011, Phase 3).
//
// Per dark-testing skill §3.4.1 (execution-based verification):
// each test asserts a specific invariant about BootstrapCI that
// would FAIL if the implementation were subtly wrong.
//
// Test discipline:
//   - Determinism: same input → same output (fixed seed).
//   - Monotonicity: more samples → tighter CI.
//   - Edge cases: n=0/1/2 all return defined (degenerate) values.
//   - Biased signal: an injected over-confident point is OUTSIDE
//     the CI of synthetic biased samples.
package judge

import (
	"math"
	"testing"
)

// TestBootstrapCI_Deterministic — universal claim: same input
// always produces the same output. A14 defense: tests with
// non-trivial inputs (n=20, confidence=0.95, varied samples).
func TestBootstrapCI_Deterministic(t *testing.T) {
	samples := []float64{0.7, 0.8, 0.9, 0.6, 0.75, 0.85, 0.65, 0.7, 0.8, 0.7,
		0.6, 0.9, 0.5, 0.7, 0.8, 0.75, 0.65, 0.85, 0.7, 0.6}
	c := BootstrapCI(samples, 0.95, 1000)

	c2 := BootstrapCI(samples, 0.95, 1000)
	if c.PointEstimate != c2.PointEstimate ||
		c.CILow != c2.CILow ||
		c.CIHigh != c2.CIHigh {
		t.Fatalf("BootstrapCI not deterministic:\n  c1=%+v\n  c2=%+v", c, c2)
	}
}

// TestBootstrapCI_PointEstimateIsMean — point estimate is the
// arithmetic mean. Catches: "forgot to average" / "divided by n-1
// instead of n" / "used median instead of mean".
func TestBootstrapCI_PointEstimateIsMean(t *testing.T) {
	samples := []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0}
	expected := 0.55

	c := BootstrapCI(samples, 0.95, 1000)
	if math.Abs(c.PointEstimate-expected) > 1e-9 {
		t.Fatalf("PointEstimate = %f; want %f", c.PointEstimate, expected)
	}
}

// TestBootstrapCI_WidthMonotonicInN — more samples produce tighter
// CIs (resampling variance shrinks with n). Catches: off-by-one in
// sample-size handling, missing resampling.
func TestBootstrapCI_WidthMonotonicInN(t *testing.T) {
	// Use a noisy stream — if we used constant values the CI would
	// be 0 regardless of n.
	gen := func(n int) []float64 {
		out := make([]float64, n)
		// Deterministic pseudo-random: step 7, mod 17.
		x := uint64(1234567)
		for i := 0; i < n; i++ {
			x = x*1103515245 + 12345
			out[i] = float64(x%100) / 100.0
		}
		return out
	}

	widthFor := func(n int) float64 {
		c := BootstrapCI(gen(n), 0.95, 1000)
		return c.CIHigh - c.CILow
	}

	w10 := widthFor(10)
	w100 := widthFor(100)
	w1000 := widthFor(1000)

	if !(w10 >= w100 && w100 >= w1000) {
		t.Fatalf("CI width not monotonic in N: w(10)=%f w(100)=%f w(1000)=%f",
			w10, w100, w1000)
	}
	// Stronger: width should shrink by at least 2x going from
	// n=10 to n=1000 (rough bootstrap-theory expectation).
	if w1000*2 > w10 {
		t.Errorf("CI width did not shrink as expected: w(10)=%f vs 2*w(1000)=%f",
			w10, 2*w1000)
	}
}

// TestBootstrapCI_EdgeCases — n=0, n=1, n=2 all return defined
// (degenerate) values. Catches: panic on empty, NaN on tiny n.
func TestBootstrapCI_EdgeCases(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		c := BootstrapCI(nil, 0.95, 1000)
		if c.PointEstimate != 0 || c.CILow != 0 || c.CIHigh != 0 {
			t.Errorf("empty samples: %+v; want all-zero", c)
		}
		if c.N != 0 {
			t.Errorf("N = %d; want 0", c.N)
		}
	})

	t.Run("one_sample", func(t *testing.T) {
		c := BootstrapCI([]float64{0.42}, 0.95, 1000)
		if c.PointEstimate != 0.42 {
			t.Errorf("PointEstimate = %f; want 0.42", c.PointEstimate)
		}
		if c.CILow != 0.42 || c.CIHigh != 0.42 {
			t.Errorf("CI bounds (%f, %f); want (0.42, 0.42)", c.CILow, c.CIHigh)
		}
	})

	t.Run("two_samples", func(t *testing.T) {
		// Bootstrap with replacement of 2 indices from [0.3, 0.7]:
		// 4 equally likely outcomes (0,0)→0.3, (0,1)→0.5,
		// (1,0)→0.5, (1,1)→0.7. Sorted resample means have
		// CI spanning [0.3, 0.7] (not degenerate). The point
		// estimate is still the arithmetic mean of the inputs.
		c := BootstrapCI([]float64{0.3, 0.7}, 0.95, 1000)
		if math.Abs(c.PointEstimate-0.5) > 1e-9 {
			t.Errorf("PointEstimate = %f; want 0.5", c.PointEstimate)
		}
		// CI spans the input range (lower than the upper bound of
		// the two-element domain), point estimate inside CI.
		if c.CILow > c.PointEstimate {
			t.Errorf("CILow (%f) > PointEstimate (%f)", c.CILow, c.PointEstimate)
		}
		if c.PointEstimate > c.CIHigh {
			t.Errorf("PointEstimate (%f) > CIHigh (%f)", c.PointEstimate, c.CIHigh)
		}
		if c.CIHigh < 0.65 {
			t.Errorf("CIHigh = %f; want >= 0.65 (resample distribution mass at 0.7)", c.CIHigh)
		}
	})

	t.Run("invalid_confidence", func(t *testing.T) {
		c := BootstrapCI([]float64{0.5}, 0, 100)
		if c.PointEstimate != 0 {
			t.Errorf("confidence=0 should return zero-valued CI; got %+v", c)
		}
		c = BootstrapCI([]float64{0.5}, 1.5, 100)
		if c.PointEstimate != 0 {
			t.Errorf("confidence=1.5 should return zero-valued CI; got %+v", c)
		}
	})
}

// TestBootstrapCI_BiasedSignalDetectable — over-confident point
// estimate must be DETECTABLY outside the CI of synthetic biased
// samples. This is the property EC-007b relies on: if the LLM
// reports confidence=0.99 but the CI high is 0.7, EC-007b fires.
//
// Catches: "resample means all equal" (would make CI degenerate),
// "alpha direction reversed" (CI low > CI high).
func TestBootstrapCI_BiasedSignalDetectable(t *testing.T) {
	// 100 samples clustered around 0.6.
	samples := make([]float64, 100)
	for i := range samples {
		// 0.5 + small noise in [0, 0.2].
		samples[i] = 0.5 + 0.05*float64(i%5)
	}
	c := BootstrapCI(samples, 0.95, 1000)

	// LLM reports confidence 0.95 (clearly over-confident for
	// samples centered ~0.6).
	llmConfidence := 0.95
	if llmConfidence <= c.CIHigh {
		t.Fatalf("biased signal not detectable: llm_confidence=%f, ci_high=%f (expected llm_confidence > ci_high)",
			llmConfidence, c.CIHigh)
	}

	// Sanity: ciLow < ciHigh.
	if c.CILow >= c.CIHigh {
		t.Fatalf("ci_low=%f >= ci_high=%f", c.CILow, c.CIHigh)
	}
}

// TestBootstrapCI_ConfidenceControlsWidth — higher confidence (e.g.
// 0.99 vs 0.80) yields a WIDER CI. Catches: "swapped alpha".
func TestBootstrapCI_ConfidenceControlsWidth(t *testing.T) {
	samples := []float64{0.5, 0.6, 0.7, 0.8, 0.55, 0.65, 0.75, 0.85, 0.6, 0.7,
		0.5, 0.65, 0.75, 0.8, 0.55, 0.7, 0.8, 0.6, 0.65, 0.75}
	c80 := BootstrapCI(samples, 0.80, 1000)
	c95 := BootstrapCI(samples, 0.95, 1000)
	c99 := BootstrapCI(samples, 0.99, 1000)

	w80 := c80.CIHigh - c80.CILow
	w95 := c95.CIHigh - c95.CILow
	w99 := c99.CIHigh - c99.CILow

	if !(w80 <= w95 && w95 <= w99) {
		t.Fatalf("CI width not monotonic in confidence: w(80)=%f w(95)=%f w(99)=%f",
			w80, w95, w99)
	}
}

// TestShouldRecalibrate — boundary check per Play Favorites §4.3.
// Catches: "forgot the >= ", off-by-one.
func TestShouldRecalibrate(t *testing.T) {
	if ShouldRecalibrate(0) {
		t.Error("ShouldRecalibrate(0) = true; want false")
	}
	if ShouldRecalibrate(49) {
		t.Error("ShouldRecalibrate(49) = true; want false (boundary < 50)")
	}
	if !ShouldRecalibrate(50) {
		t.Error("ShouldRecalibrate(50) = false; want true (boundary)")
	}
	if !ShouldRecalibrate(100) {
		t.Error("ShouldRecalibrate(100) = false; want true")
	}
}

// TestMean — small helper used by other tests + by callers that
// only want the point estimate.
func TestMean(t *testing.T) {
	if Mean(nil) != 0 {
		t.Errorf("Mean(nil) = %f; want 0", Mean(nil))
	}
	if math.Abs(Mean([]float64{0.2, 0.4, 0.6})-0.4) > 1e-9 {
		t.Errorf("Mean([0.2,0.4,0.6]) = %f; want 0.4", Mean([]float64{0.2, 0.4, 0.6}))
	}
}