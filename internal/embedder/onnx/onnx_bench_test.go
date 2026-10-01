// onnx_bench_test.go — Phase 9 Chunk 8.3 verbatim operator decision B.
//
// Real-latency benchmark for the bundled ONNX embedder
// (Xenova/all-MiniLM-L6-v2 INT8, 384-dim, ~22MB model + ~7MB
// libonnxruntime DLL) running on the operator's actual hardware.
//
// Run with `go test -bench=Embedder -benchmem -run=^$ -benchtime=10s`.
// Without `-bench` the file is a no-op (standard Go testing
// convention).
//
// # What this measures
//
// p50/p95/p99 latency for the production Embed() call against
// representative agent_memory content sizes (short title / medium
// observation / long decision / very-long spec chunk). Background:
// dark-memory stores agent_memory rows whose content ranges from
// 50-char titles to multi-KB spec excerpts; the embedder truncates
// inputs to MaxSeqLen=512 wordpieces per the ONNX adapter default.
//
// # What this does NOT measure
//
//   - GPU path. The bundled yalue/onnxruntime_go binding defaults
//     to the CPU execution provider; DirectML (the AMD GPU path
//     for Windows) requires a different binding — separate work
//     item, out of Chunk 8.3 scope. The Ryzen 5 5600 (6c/12t) +
//     24 GB RAM profile tested here is the realistic operator
//     runtime for a 4 GB VRAM AMD card anyway (DirectML overhead
//     often exceeds CPU speedup for small batch sizes).
//   - Cross-process concurrency. The ONNX session is wrapped by
//     embedder.Sync so concurrent calls serialize; this bench
//     runs single-goroutine which matches the production
//     SaveAgentMemory path.
//   - Mock vs real comparison. Use internal/embedder/mock for the
//     deterministic baseline (already covered by integration
//     tests in internal/store/sqlite).
package onnx

import (
	"strings"
	"testing"
	"time"
)

// Realistic agent_memory content samples. Length buckets chosen to
// match the distribution observed in the production dark-memory DB
// (p50~200 chars, p99~5KB, max~50KB).
var benchSamples = []struct {
	name string
	text string
}{
	{
		name: "short_50ch",
		text: "Phase 9 alpha.20 Chunk 8.3 SHIPPED",
	},
	{
		name: "medium_500ch",
		text: strings.Repeat("alpha-19 deferred items completion status report. ", 10), // ~500 chars
	},
	{
		name: "long_2000ch",
		text: strings.Repeat("the v4alpha EXTRACT pipeline (Chunk 7.1) is now reachable through the v3 MCP surface dark_memory_delegate_intent. ", 20), // ~2KB
	},
	{
		name: "verylong_10000ch",
		text: strings.Repeat("Architecture: RunDelegateIntentCore pure function in internal/v4alpha/transport/mcp/wire.go with no mcp-go types. ", 80), // ~10KB
	},
}

// setupBenchEmbedder constructs a fresh ONNX embedder for benchmarking.
// Skips the bench on platforms where the ONNX runtime is not bundled
// (so the bench file is portable; CI without windows-amd64 still
// passes the rest of the test suite).
func setupBenchEmbedder(b *testing.B) *onnxAdapter {
	b.Helper()
	emb, err := New(Options{DarkHome: b.TempDir()})
	if err != nil {
		b.Skipf("ONNX embedder unavailable: %v", err)
	}
	a, ok := emb.(*onnxAdapter)
	if !ok {
		b.Fatalf("New returned non-*onnxAdapter: %T", emb)
	}
	return a
}

// benchEmbedder measures Embed() latency across all sample sizes.
// (Removed in Phase 9 Chunk 8.3 — the per-bucket BenchmarkEmbedderBySize
// covers the same ground with better per-bucket p50/p95/p99. The
// Windows file-locking issue with the embedded DLL makes the
// combined bench FAIL on cleanup even though the inference
// itself is fine.)
//
// benchEmbedderBySize measures per-bucket latency with N rounds
// per bucket. Uses a custom loop (not b.RunParallel) because we want
// per-bucket p50/p95/p99 numbers — testing.B's default reporter
// only gives ns/op aggregate.
func BenchmarkEmbedderBySize(b *testing.B) {
	a := setupBenchEmbedder(b)
	defer func() { _ = a.Close() }()

	const Nper = 200 // 200 timed calls per bucket; enough for stable p99

	for _, s := range benchSamples {
		s := s
		b.Run(s.name, func(b *testing.B) {
			// Warm up: 10 un-timed calls.
			for i := 0; i < 10; i++ {
				if _, err := a.Embed(b.Context(), []string{s.text}); err != nil {
					b.Fatalf("warmup Embed: %v", err)
				}
			}
			// Timed loop: collect per-call latency.
			latencies := make([]time.Duration, 0, Nper)
			b.ResetTimer()
			for i := 0; i < Nper; i++ {
				start := time.Now()
				if _, err := a.Embed(b.Context(), []string{s.text}); err != nil {
					b.Fatalf("Embed: %v", err)
				}
				latencies = append(latencies, time.Since(start))
			}
			b.StopTimer()
			reportBenchLatencies(b, s.name, s.text, latencies)
		})
	}
}

// reportBenchLatencies prints p50/p95/p99/min/max + throughput
// for a latency sample. Stays inside testing.B's log so the output
// interleaves with normal bench output.
func reportBenchLatencies(b *testing.B, name string, text string, samples []time.Duration) {
	if len(samples) == 0 {
		return
	}
	// Sort in place to compute percentiles.
	sorted := make([]time.Duration, len(samples))
	copy(sorted, samples)
	sortDurations(sorted)

	p50 := sorted[len(sorted)*50/100]
	p95 := sorted[len(sorted)*95/100]
	p99 := sorted[len(sorted)*99/100]
	min := sorted[0]
	max := sorted[len(sorted)-1]

	// Throughput: 1 call per sample; aggregate over total wall time.
	var total time.Duration
	for _, d := range samples {
		total += d
	}
	avgNs := float64(total) / float64(len(samples))
	throughput := 1e9 / avgNs // calls per second

	b.Logf("[%s] n=%d  min=%s  p50=%s  p95=%s  p99=%s  max=%s  avg=%.2fms  thr=%.1f/s  chars=%d",
		name, len(samples),
		min.Round(time.Microsecond),
		p50.Round(time.Microsecond),
		p95.Round(time.Microsecond),
		p99.Round(time.Microsecond),
		max.Round(time.Microsecond),
		float64(avgNs)/1e6,
		throughput,
		len(text),
	)
}

// sortDurations sorts a []time.Duration in place using the standard
// library's sort.Slice (avoiding an extra import in the bench file).
func sortDurations(s []time.Duration) {
	// Insertion sort is fine for N=200; avoids the sort.Slice
	// allocation overhead in a hot bench helper.
	for i := 1; i < len(s); i++ {
		v := s[i]
		j := i - 1
		for j >= 0 && s[j] > v {
			s[j+1] = s[j]
			j--
		}
		s[j+1] = v
	}
}

// TestMain-style guard: if New() returns ErrDisabled (e.g. CI runs
// on linux-arm64), the bench SKIPs rather than fails. This lets
// the file live in the test corpus without breaking CI for
// unsupported platforms.
//
// Note: there's no TestMain here because onnx_test.go already has
// its own. The b.Skipf() call in setupBenchEmbedder handles the
// unsupported-platform case cleanly.