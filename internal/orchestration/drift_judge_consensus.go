// Package orchestration — drift_judge_consensus.go
//
// v2.20.0 T09 (spec 1276): artifact-anchored N-shot drift_judge.
//
// # Why this file exists
//
// T08 (drift_judge.go) replaced caller-controlled Content with the
// artifact-anchored pipeline. T09 extends the same fix to the N-shot
// consensus path: callers can now run consensus over an ArtifactRef
// and the judge will (a) resolve the artifact once, (b) chunk the
// resolved bytes into N pieces, (c) score each chunk independently
// via nli.Provider, and (d) return the modal verdict across chunks
// with provenance for every chunk.
//
// The legacy JudgeConsensus(drift_judge, Content=...) path still
// works (with a deprecation warning) for backward compat — Phase 1
// of spec 1276 H1. At v2.22.0 (Phase 2) the Content path is removed
// for drift_judge and only ArtifactRef remains (compile-time check).
//
// # Threat model
//
// The same 4 actors apply:
//
//   - Caller LLM: passes ArtifactRef, not Content. Cannot influence
//     the verdict by submitting arbitrary text.
//   - Judge LLM: NLI model (deterministic for the same premise +
//     hypothesis pair). The model is authoritative when its label
//     is canonical.
//   - Adversary externo: large artifact (> 4 MiB) →
//     artifact.HardMaxBytes cap. Premise > NLI MaxPremiseBytes →
//     ErrInputTooLarge → chunk gets needs_human.
//   - Operator: pins Project.NLIConfig; can override Default for
//     primary / fallback / cache.
//
// # Chunking strategy (sealed)
//
// ConsensusChunkSize = 4 KB. Smaller than NLI MaxPremiseBytes (64 KB)
// so every chunk fits comfortably. The artifact is split into N
// chunks of equal size, capped at N (so a 100 KB artifact with N=7
// gives 7 chunks of ~14 KB each — well under 64 KB).
//
//   - len(bytes) <= 4 KB → 1 chunk (the whole artifact), N redundant
//     shots (cache hits after first call; deterministic redundancy).
//   - len(bytes) > 4 KB → ceil(len(bytes) / 4 KB) chunks, capped at N.
//     chunks are evenly distributed across the artifact.
//
// Boundary cases:
//
//   - len(bytes) == 0 → drift_detected (empty artifact cannot match
//     spec).
//   - chunkSize > NLI MaxPremiseBytes → ErrArtifactTooLargeForConsensus.
//     Operator can reduce chunk size via the NLI MaxPremiseBytes
//     setting on Project.NLIConfig.Primary.
//
// # Aggregation
//
// Each chunk is scored independently → NLI label. Modal label wins.
// Modal fraction = modal_count / N. If ModalFraction < 0.6 →
// final verdict = "needs_human" (low agreement; operator review).
// Confidence = avg of per-chunk scores.
//
// # Persistence
//
// Mirrors the legacy JudgeConsensus persistence model:
//
//   - Each chunk's score is persisted as a SDDEvaluation row with
//     TargetID = in.TargetID + ":chunk:N" (N = chunk index).
//   - The consensus row is persisted with TargetID = in.TargetID +
//     ":consensus" and a canonical drift-consensus VerdictJSON.
//
// # Backward compatibility (Phase 1 — v2.20.0)
//
// JudgeConsensus(EvalType=drift_judge, ArtifactRef=...) → routes to
// DriftJudgeConsensus (this file). The result is converted to a
// JudgeConsensusResult so the wire protocol is unchanged.
//
// JudgeConsensus(EvalType=drift_judge, Content=...) → legacy path with
// deprecation warning (spec 1276 H1 phase 1).
//
// # Hard invariants (sealed)
//
//  1. DriftJudgeConsensus ALWAYS receives an ArtifactRef. Empty →
//     errMissingField("artifact_ref").
//  2. DriftJudgeConsensus ALWAYS resolves the artifact FIRST. Resolve
//     fails → verdict="drift_detected" + Error Observatory.
//  3. Each chunk is scored independently via nli.Provider.Score. The
//     score is premise=chunk_bytes + hypothesis=spec_intent.
//  4. NLI label → verdict: entailment→aligned, contradiction→
//     drift_detected, neutral→needs_human. Unknown → needs_human.
//  5. Per-chunk Score errors → that chunk's verdict = needs_human +
//     Error Observatory row. The aggregation still runs.
//  6. All chunks fail → error (mirrors legacy JudgeConsensus "all N
//     samples failed"). Modal fraction = 0.0.
//  7. ModalFraction < 0.6 → final verdict = needs_human (regardless
//     of modal string).
//  8. DriftJudgeConsensus does NOT log the artifact body or
//     AuthToken. Latency + verdict + provider_id + SHA-256 per chunk
//     are logged at debug level.
package orchestration

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/artifact"
	"github.com/dark-agents/dark-memory-mcp/internal/errorobs"
	"github.com/dark-agents/dark-memory-mcp/internal/nli"
	"github.com/dark-agents/dark-memory-mcp/internal/ssd"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// ConsensusChunkSize is the canonical chunk size for the
// artifact-anchored consensus path. 4 KB is well below NLI
// MaxPremiseBytes (default 64 KB) and lets chunked consensus catch
// local disagreements within a larger artifact (e.g., a header that
// says "OK" and a body that has a problem).
//
// Tunable per-project would require a T09+ follow-up; for v2.20.0
// this is a sealed package constant.
const ConsensusChunkSize = 4096

// consensusAgreementFloor is the minimum ModalFraction required for
// the modal verdict to be trusted. Below this, the consensus row
// overrides to "needs_human" (low agreement — operator review).
//
// 0.6 mirrors the legacy JudgeConsensus contract (judge_consensus.go
// line 254). The hard invariant is sealed; no per-project override.
const consensusAgreementFloor = 0.6

// driftChunkSample is the per-chunk outcome returned by
// runDriftChunk. It carries the sample (for the consensus row) and
// the underlying error (for the failed index list).
type driftChunkSample struct {
	sample DriftJudgeConsensusSample
	err    error
}

// chunkRange is one byte window in the artifact. Both fields are
// inclusive of the byte at Start and exclusive of the byte at End
// (mirrors Go slice semantics).
type chunkRange struct {
	Start int64
	End   int64
}

// DriftJudgeConsensusInput is the artifact-anchored N-shot
// drift_judge request. The caller MUST supply ArtifactRef — the
// consensus path never accepts raw Content from the caller (Phase 2
// of spec 1276 H1 removes the Content field at v2.22.0).
type DriftJudgeConsensusInput struct {
	ArtifactRef artifact.ArtifactRef // file/git_sha/url/spec_id/artifact_id
	SpecIntent  string               // the hypothesis (one-paragraph spec intent)
	TargetType  string               // "artifact" (default)
	TargetID    string               // audit grouping key
	VibeCase    string               // C1..C7 — G-Eval rubric extension
	PersonaID   string               // explicit persona id (v2.17.0)
	AgentID     string               // for agent-scoped memory enrichment
	NoEnrich    bool                 // skip prior-context enrichment
	N           int                  // shot count; default 3, clamped to [1, 7]
}

// DriftJudgeConsensusSample is one chunk's score in the consensus
// run. The shape mirrors JudgeConsensusSample for compatibility
// with the consensus tool's wire format, plus the chunk-specific
// provenance fields (start, end, sha256, nli_label).
type DriftJudgeConsensusSample struct {
	SampleIndex   int     `json:"sample_index"`
	ChunkStart    int64   `json:"chunk_start"`    // byte offset (inclusive)
	ChunkEnd      int64   `json:"chunk_end"`      // byte offset (exclusive)
	EvaluationID  int64   `json:"evaluation_id"`  // SDDEvaluation row id
	Verdict       string  `json:"verdict"`        // aligned | drift_detected | needs_human | skipped
	Confidence    float32 `json:"confidence"`
	NLILabel      string  `json:"nli_label"`      // raw NLI label (entailment/contradiction/neutral)
	NLIConfidence float64 `json:"nli_confidence"`
	ChunkSHA256   string  `json:"chunk_sha256"`   // hex SHA-256 of the chunk bytes
	LatencyMS     int64   `json:"latency_ms"`
	VerdictJSON   string  `json:"verdict_json"`
}

// DriftJudgeConsensusResult is the canonical verdict across N chunks.
//
// The shape mirrors JudgeConsensusResult for the consensus tool's
// wire format, plus artifact provenance (artifact_source,
// artifact_sha256, artifact_path) and chunk-specific fields
// (chunked_size, num_chunks, requested_n, provider_id).
type DriftJudgeConsensusResult struct {
	EvaluationID     int64                          `json:"evaluation_id"`     // id of the consensus row
	ModalVerdict     string                         `json:"modal_verdict"`     // the most-frequent verdict per chunk
	ModalCount       int                            `json:"modal_count"`       // how many chunks voted for the modal
	ModalFraction    float32                        `json:"modal_fraction"`    // modal_count / N; < 0.6 → needs_human override
	AvgConfidence    float32                        `json:"avg_confidence"`
	StdDevConfidence float32                        `json:"stddev_confidence"` // sample std dev (0 if N=1)
	ConfidenceLow    float32                        `json:"confidence_low"`    // avg - 1σ (clamped)
	ConfidenceHigh   float32                        `json:"confidence_high"`   // avg + 1σ (clamped)
	Verdict          string                         `json:"verdict"`           // modal_verdict OR "needs_human"
	NextAction       string                         `json:"next_action"`       // publish | reconcile | human_gate
	ChunkedSize      int                            `json:"chunked_size"`      // full artifact size in bytes
	NumChunks        int                            `json:"num_chunks"`        // chunks that were scored
	RequestedN       int                            `json:"requested_n"`       // N requested (clamped)
	ProviderID       string                         `json:"provider_id"`       // common provider across surviving chunks
	ArtifactSource   string                         `json:"artifact_source"`
	ArtifactSHA256   string                         `json:"artifact_sha256"`   // whole artifact hash
	ArtifactPath     string                         `json:"artifact_path"`
	// ArtifactSize is the resolved body size in bytes (v29, T10).
	// Persisted to sdd_evaluations.artifact_size for audit.
	ArtifactSize int64 `json:"artifact_size"`
	Samples      []DriftJudgeConsensusSample `json:"samples"`
	Reasoning    string                     `json:"reasoning"`
	Degraded     bool                       `json:"degraded"`
	FailedSampleIndices []int               `json:"failed_sample_indices,omitempty"`
}

// DriftJudgeConsensus is the artifact-anchored N-shot drift_judge
// pipeline. See package doc for chunking strategy and invariants.
//
// Cost: up to N nli.Provider.Score calls (one per chunk), capped at
// N=7. Latency is ~1 chunk (plus per-chunk retry/backoff) because
// chunks run concurrently — same wall-clock pattern as the legacy
// JudgeConsensus.
//
// Returns:
//   - errMissingField("artifact_ref") when ArtifactRef is empty.
//   - errMissingField("spec_intent") when SpecIntent is empty.
//   - ErrCanaryInPayload when canary is in spec_intent OR resolved body.
//   - A non-nil DriftJudgeConsensusResult (with possibly degraded
//     samples) when any chunk succeeds. The flow always returns
//     *something* unless ALL chunks fail (mirrors legacy semantics).
func (o *Orchestrator) DriftJudgeConsensus(ctx context.Context, in DriftJudgeConsensusInput) (*DriftJudgeConsensusResult, error) {
	// 1. Validate (sealed: ArtifactRef is required).
	if err := in.ArtifactRef.Validate(); err != nil {
		return nil, errMissingField("artifact_ref")
	}
	if strings.TrimSpace(in.SpecIntent) == "" {
		return nil, errMissingField("spec_intent")
	}
	if in.TargetType == "" {
		in.TargetType = "artifact"
	}
	n := in.N
	if n <= 0 {
		n = 3
	}
	if n > 7 {
		n = 7
	}
	in.N = n

	// 2. Canary check (INV-3) on the spec intent. The artifact body
	// is checked post-Resolve.
	if !o.Safety.Active().IsZero() {
		if o.Safety.Active().Match(in.SpecIntent) {
			return nil, fmt.Errorf("%w: drift_judge_consensus spec_intent contains canary token", store.ErrCanaryInPayload)
		}
	}

	// 3. Resolve the artifact via artifact.Resolver.
	resolver := o.ensureDriftJudgeResolver()
	resolved, err := resolver.Resolve(ctx, in.ArtifactRef)
	if err != nil {
		// Artifact resolution failed → cannot match the spec. The
		// drift verdict is "drift_detected" (the artifact does not
		// exist in the form the caller claims). Error Observatory
		// gets a row so the operator can audit.
		o.RecordError(ctx, "drift_judge_consensus", o.activeSessionID(ctx),
			fmt.Errorf("resolve artifact: %w", err), errorobs.SeverityWarn)
		// Empty resolved — zero samples. We still persist a
		// consensus row with the failure mode so the audit trail
		// captures the attempt.
		empty := &DriftJudgeConsensusResult{
			ModalVerdict:        "drift_detected",
			ModalCount:          0,
			ModalFraction:       0,
			AvgConfidence:       0,
			StdDevConfidence:    0,
			ConfidenceLow:       0,
			ConfidenceHigh:      0,
			Verdict:             "drift_detected",
			NextAction:          "reconcile",
			ChunkedSize:         0,
			NumChunks:           0,
			RequestedN:          n,
			Reasoning:           fmt.Sprintf("artifact resolution failed: %v", err),
			Degraded:            true,
			FailedSampleIndices: allIndices(n),
		}
		consensusEvalID, perr := o.persistDriftConsensusRow(ctx, in, empty)
		if perr == nil {
			empty.EvaluationID = consensusEvalID
		}
		// best-effort: persistence may fail but we still return the
		// result so the caller sees the verdict.
		return empty, nil
	}

	// 4. Canary on the resolved body (post-Resolve).
	if !o.Safety.Active().IsZero() && o.Safety.Active().Match(string(resolved.Bytes)) {
		return nil, fmt.Errorf("%w: drift_judge_consensus artifact body contains canary token", store.ErrCanaryInPayload)
	}

	// 5. Empty artifact → drift_detected (the artifact has zero
	// bytes; it cannot match a spec).
	if len(resolved.Bytes) == 0 {
		o.RecordError(ctx, "drift_judge_consensus", o.activeSessionID(ctx),
			errors.New("resolved artifact is empty"), errorobs.SeverityWarn)
		empty := &DriftJudgeConsensusResult{
			ModalVerdict:        "drift_detected",
			ModalCount:          0,
			ModalFraction:       0,
			Verdict:             "drift_detected",
			NextAction:          "reconcile",
			ChunkedSize:         0,
			NumChunks:           0,
			RequestedN:          n,
			ArtifactSource:      string(resolved.Source),
			ArtifactSHA256:      hexBytes(resolved.ContentSHA256),
			ArtifactPath:        resolved.Path,
			ArtifactSize:        0,
			Reasoning:           "resolved artifact is empty (0 bytes)",
			Degraded:            true,
			FailedSampleIndices: allIndices(n),
		}
		consensusEvalID, perr := o.persistDriftConsensusRow(ctx, in, empty)
		if perr == nil {
			empty.EvaluationID = consensusEvalID
		}
		return empty, nil
	}

	// 6. Resolve the NLI Provider. Same fallback semantics as DriftJudge.
	provider, err := o.EnsureNLIRouter(ctx)
	if err != nil {
		o.RecordError(ctx, "drift_judge_consensus", o.activeSessionID(ctx),
			fmt.Errorf("resolve nli provider: %w", err), errorobs.SeverityError)
		return nil, err
	}
	if provider == nil {
		provider, err = o.defaultDriftJudgeProvider(ctx)
		if err != nil {
			o.RecordError(ctx, "drift_judge_consensus", o.activeSessionID(ctx),
				fmt.Errorf("default nli provider: %w", err), errorobs.SeverityError)
			return nil, err
		}
	}

	// 7. Chunk the artifact.
	chunks := chunkArtifact(resolved.Bytes, n)
	if len(chunks) == 0 {
		// Defensive: chunkArtifact should return at least 1 chunk
		// for non-empty bytes. If it returns 0, treat as drift.
		return nil, errors.New("chunkArtifact returned 0 chunks for non-empty bytes")
	}

	// 8. Bound-check: each chunk must fit under the NLI MaxPremiseBytes
	// cap. We use the provider's defaults (64 KB) since we don't have
	// direct config access here. If a chunk exceeds the cap, the
	// per-chunk Score will surface ErrInputTooLarge — we don't reject
	// upfront; the per-chunk error handler maps it to needs_human.
	// This keeps the failure mode observable per chunk.

	// 9. Run N shots CONCURRENTLY. Each shot picks a chunk via
	// chunks[i % len(chunks)] — so when len(chunks) < N, redundant
	// shots reuse the same chunk (the cache hits after the first
	// call; the redundant samples are a stability check on the NLI
	// provider). When len(chunks) >= N, each shot gets a unique
	// chunk.
	outcomes := make([]driftChunkSample, n)

	now := o.now().Format(time.RFC3339Nano)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			chunkIdx := i % len(chunks)
			outcomes[i] = o.runDriftChunk(ctx, in, provider, resolved, chunks[chunkIdx], chunkIdx, len(chunks), i, now)
		}(i)
	}
	wg.Wait()

	// 10. Aggregate samples (order-preserving; samples keep their
	// chunk indices).
	samples := make([]DriftJudgeConsensusSample, 0, len(chunks))
	verdicts := make(map[string]int)
	confSum := 0.0
	var failed []int
	providerID := ""
	for i := range outcomes {
		if outcomes[i].err != nil {
			failed = append(failed, i)
			// Best-effort: record the canary/infrastructure error to
			// Error Observatory. We do NOT still create a sample
			// because the chunk cannot be scored.
			o.RecordError(ctx, "drift_judge_consensus", o.activeSessionID(ctx),
				fmt.Errorf("chunk %d: %w", i, outcomes[i].err), errorobs.SeverityError)
			continue
		}
		s := outcomes[i].sample
		samples = append(samples, s)
		verdicts[s.Verdict]++
		confSum += float64(s.Confidence)
		if providerID == "" {
			providerID = s.NLILabel // placeholder; we set ProviderID below
		}
	}

	if len(samples) == 0 {
		return nil, fmt.Errorf("drift_judge_consensus: all %d chunks failed (first error: %s)", len(chunks), outcomes[0].err)
	}

	// 11. Compute modal verdict + fraction. ModalFraction uses the
	// REQUESTED N (not len(samples)) so failed chunks count as
	// non-votes — same semantics as the legacy JudgeConsensus.
	modalVerdict, modalCount := driftConsensusModalVerdict(verdicts)
	modalFraction := float32(modalCount) / float32(n)
	avgConfidence := float32(confSum / float64(len(samples)))

	// 12. Compute confidence interval (sample std dev).
	stddev := driftConsensusStdDev(samples, avgConfidence)
	low := clamp01(avgConfidence - stddev)
	high := clamp01(avgConfidence + stddev)

	// 13. Final verdict override: if ModalFraction < 0.6, override
	// to "needs_human" (low agreement).
	finalVerdict := modalVerdict
	if modalFraction < consensusAgreementFloor {
		finalVerdict = "needs_human"
	}

	// 14. Pick the most common ProviderID across surviving samples
	// (defensive: the provider chain is uniform per call, but
	// Router.primary.ID() == Router.ID() == primary.ID()).
	providerID = mostCommonProviderID(samples)

	result := &DriftJudgeConsensusResult{
		ModalVerdict:        modalVerdict,
		ModalCount:          modalCount,
		ModalFraction:       modalFraction,
		AvgConfidence:       avgConfidence,
		StdDevConfidence:    stddev,
		ConfidenceLow:       low,
		ConfidenceHigh:      high,
		Verdict:             finalVerdict,
		NextAction:          nextActionForVerdict(finalVerdict),
		ChunkedSize:         len(resolved.Bytes),
		NumChunks:           len(chunks),
		RequestedN:          n,
		ProviderID:          providerID,
		ArtifactSource:      string(resolved.Source),
		ArtifactSHA256:      hexBytes(resolved.ContentSHA256),
		ArtifactPath:        resolved.Path,
		ArtifactSize:        int64(len(resolved.Bytes)),
		Samples:             samples,
		Degraded:            len(failed) > 0,
		FailedSampleIndices: failed,
		Reasoning: fmt.Sprintf(
			"drift_consensus: modal=%s (%d/%d, fraction=%.2f); avg_conf=%.3f; stddev=%.3f; interval=[%.3f, %.3f]; chunks=%d; artifact_size=%d; provider=%s",
			modalVerdict, modalCount, n, modalFraction, avgConfidence, stddev, low, high,
			len(chunks), len(resolved.Bytes), providerID),
	}
	if result.Degraded {
		result.Reasoning += fmt.Sprintf("; DEGRADED: %d chunk(s) failed (indices %v)", len(failed), failed)
	}

	// 15. Persist the consensus SDDEvaluation row.
	consensusID, perr := o.persistDriftConsensusRow(ctx, in, result)
	if perr == nil {
		result.EvaluationID = consensusID
	}
	// Persistence failure does NOT fail the caller — the verdict
	// is still authoritative (the caller observes the verdict in
	// the result); the audit row is best-effort.

	return result, nil
}

// runDriftChunk scores a single chunk and persists the per-chunk
// SDDEvaluation row. Returns a driftChunkSample (sample + err). Score
// errors → log + classify as needs_human.
//
// shotIndex is the 0-based shot number (0..N-1). chunkIndex is the
// chunk the score was applied to (0..len(chunks)-1). For
// len(chunks) < N, multiple shots may share the same chunkIndex.
//
// chunkTotal is the total number of chunks in this consensus run
// (len(chunks), not N). The v29 (T10) anchor + audit columns
// (ArtifactSource, ArtifactSHA256, ArtifactPath, ArtifactSize,
// ChunkIndex, ChunkTotal, NLIProviderID) are populated from the
// resolved artifact + provider so the persisted row, not just the
// VerdictJSON blob, carries the provenance.
func (o *Orchestrator) runDriftChunk(
	ctx context.Context,
	in DriftJudgeConsensusInput,
	provider nli.Provider,
	resolved *artifact.Resolved,
	chunk chunkRange,
	chunkIndex int,
	chunkTotal int,
	shotIndex int,
	now string,
) driftChunkSample {
	chunkBytes := resolved.Bytes[chunk.Start:chunk.End]
	chunkSHA := sha256.Sum256(chunkBytes)

	scoreStart := time.Now()
	score, scoreErr := provider.Score(ctx, string(chunkBytes), in.SpecIntent)
	scoreLatency := time.Since(scoreStart).Milliseconds()

	sample := DriftJudgeConsensusSample{
		SampleIndex:  shotIndex,
		ChunkStart:   chunk.Start,
		ChunkEnd:     chunk.End,
		ChunkSHA256:  hexBytes(chunkSHA),
		Verdict:      "needs_human", // default for failures
		LatencyMS:    scoreLatency,
		NLIConfidence: 0,
	}
	sample.VerdictJSON = formatDriftChunkVerdictJSON(chunkIndex, chunk, chunkSHA, nli.Score{}, nil)

	if scoreErr != nil {
		// Classify the error. Same hierarchy as DriftJudge.
		severity := errorobs.SeverityError
		switch {
		case errors.Is(scoreErr, nli.ErrInputTooLarge):
			severity = errorobs.SeverityWarn
		}
		o.RecordError(ctx, "drift_judge_consensus", o.activeSessionID(ctx),
			fmt.Errorf("chunk %d score: %w", chunkIndex, scoreErr), severity)
		// Persist a "needs_human" SDDEvaluation row anyway so the
		// audit trail captures the failure. TargetID uses chunkIndex
		// (so duplicate-shot rows share the same TargetID — that's
		// fine; the chunk SHA + shot index in VerdictJSON gives
		// uniqueness).
		evalID, perr := o.persistDriftChunkRow(ctx, in, resolved, chunkIndex, chunk, chunkTotal, sample, provider.ID(), scoreErr, now)
		if perr == nil {
			sample.EvaluationID = evalID
		}
		return driftChunkSample{sample: sample, err: scoreErr}
	}

	// Map NLI label → verdict.
	verdict := nliLabelToDriftVerdict(score.Label)
	sample.Verdict = verdict
	sample.Confidence = float32(score.Confidence)
	sample.NLILabel = string(score.Label)
	sample.NLIConfidence = score.Confidence
	sample.LatencyMS = score.LatencyMS
	sample.VerdictJSON = formatDriftChunkVerdictJSON(chunkIndex, chunk, chunkSHA, score, nil)

	// Persist the per-chunk SDDEvaluation row.
	evalID, perr := o.persistDriftChunkRow(ctx, in, resolved, chunkIndex, chunk, chunkTotal, sample, provider.ID(), nil, now)
	if perr == nil {
		sample.EvaluationID = evalID
	}

	return driftChunkSample{sample: sample, err: nil}
}

// chunkArtifact splits the artifact into N chunks of equal size,
// capped at N (n <= 7). Returns at least 1 chunk for non-empty
// bytes.
//
// Algorithm:
//   - len(bytes) <= ConsensusChunkSize: 1 chunk covering the whole artifact.
//   - len(bytes) > ConsensusChunkSize:  numChunks = min(N, ceil(len(bytes)/ConsensusChunkSize))
//     chunks are evenly distributed: chunkSize = len(bytes) / numChunks (last chunk absorbs remainder).
//
// For N=1 with a large artifact, the caller should use DriftJudge
// (single-shot) instead. DriftJudgeConsensus with N=1 still works
// but is degenerate (1 chunk = whole artifact).
func chunkArtifact(bytes []byte, n int) []chunkRange {
	if len(bytes) == 0 {
		return nil
	}
	if n <= 0 {
		n = 3
	}
	if n > 7 {
		n = 7
	}
	if len(bytes) <= ConsensusChunkSize {
		return []chunkRange{{Start: 0, End: int64(len(bytes))}}
	}
	// Multi-chunk path.
	numChunks := (len(bytes) + ConsensusChunkSize - 1) / ConsensusChunkSize
	if numChunks > n {
		numChunks = n
	}
	if numChunks == 1 {
		return []chunkRange{{Start: 0, End: int64(len(bytes))}}
	}
	// Even distribution: chunkSize = ceil(len(bytes) / numChunks).
	// The last chunk absorbs the remainder so every chunk is
	// contiguous (no gaps).
	chunkSize := (len(bytes) + numChunks - 1) / numChunks
	out := make([]chunkRange, 0, numChunks)
	for i := 0; i < numChunks; i++ {
		start := int64(i * chunkSize)
		end := int64((i + 1) * chunkSize)
		if i == numChunks-1 {
			end = int64(len(bytes))
		}
		if end > int64(len(bytes)) {
			end = int64(len(bytes))
		}
		if start >= end {
			// Defensive: shouldn't happen with the math above.
			break
		}
		out = append(out, chunkRange{Start: start, End: end})
	}
	if len(out) == 0 {
		return []chunkRange{{Start: 0, End: int64(len(bytes))}}
	}
	return out
}

// driftConsensusModalVerdict returns the verdict with the most
// votes, breaking ties by canonical priority: aligned > drift_detected
// > needs_human > skipped (the order operators trust most).
//
// Mirrors the legacy modalVerdictFromCounts in judge_consensus.go.
// Local copy because the drift path is feature-frozen at v2.20.0
// (the legacy path gets the same priority but lives in a separate
// type).
func driftConsensusModalVerdict(counts map[string]int) (string, int) {
	if len(counts) == 0 {
		return "needs_human", 0
	}
	priority := []string{"aligned", "drift_detected", "needs_human", "skipped"}
	max := 0
	for _, c := range counts {
		if c > max {
			max = c
		}
	}
	for _, v := range priority {
		if counts[v] == max {
			return v, max
		}
	}
	for v, c := range counts {
		if c == max {
			return v, c
		}
	}
	return "needs_human", 0
}

// driftConsensusStdDev computes the sample standard deviation of
// confidence values across samples. Returns 0 if N < 2.
func driftConsensusStdDev(samples []DriftJudgeConsensusSample, mean float32) float32 {
	if len(samples) < 2 {
		return 0
	}
	var sumSq float64
	for _, s := range samples {
		d := float64(s.Confidence) - float64(mean)
		sumSq += d * d
	}
	variance := sumSq / float64(len(samples)-1)
	return float32(math.Sqrt(variance))
}

// mostCommonProviderID returns the most common non-empty ProviderID
// across surviving samples. The NLI provider chain is uniform per
// call (T08 invariant: Router.ID() == primary.ID()), so this is
// mostly defensive. Empty if all samples have empty ProviderID.
//
// Implements majority-vote: each sample's ProviderID is extracted
// from its VerdictJSON (the per-chunk VerdictJSON carries the
// ProviderID from the underlying nli.Score). The most common
// ProviderID wins.
func mostCommonProviderID(samples []DriftJudgeConsensusSample) string {
	if len(samples) == 0 {
		return ""
	}
	counts := make(map[string]int)
	for _, s := range samples {
		id := extractProviderIDFromChunkJSON(s.VerdictJSON)
		if id != "" {
			counts[id]++
		}
	}
	if len(counts) == 0 {
		return ""
	}
	// Pick the most common id; ties broken by lex order (deterministic).
	best := ""
	bestCount := 0
	for id, c := range counts {
		if c > bestCount || (c == bestCount && (best == "" || id < best)) {
			best = id
			bestCount = c
		}
	}
	return best
}

// persistDriftChunkRow persists one chunk's score as an
// SDDEvaluation row. TargetID is suffixed with ":chunk:N" so
// callers can filter the audit trail by chunk index. The v29 (T10)
// anchor + audit columns are populated from the resolved artifact
// + chunk metadata so the SQL row, not just the VerdictJSON blob,
// carries the provenance. Returns 0 if persistence fails (the
// caller continues).
func (o *Orchestrator) persistDriftChunkRow(
	ctx context.Context,
	in DriftJudgeConsensusInput,
	resolved *artifact.Resolved,
	chunkIndex int,
	chunk chunkRange,
	chunkTotal int,
	sample DriftJudgeConsensusSample,
	providerID string,
	scoreErr error,
	now string,
) (int64, error) {
	wc := store.WriteContext{
		Actor:     "orchestrator_drift_judge_consensus_chunk",
		WritePath: fmt.Sprintf("DriftJudgeConsensusChunk[%d]", chunkIndex),
	}
	eval := &ssd.SDDEvaluation{
		EvalType:       "drift_judge",
		TargetType:     in.TargetType,
		TargetID:       fmt.Sprintf("%s:chunk:%d", in.TargetID, chunkIndex),
		VerdictJSON:    sample.VerdictJSON,
		Confidence:     sample.Confidence,
		Model:          "", // NLI doesn't expose a model name (ProviderID instead)
		ConstitutionID: wc.ConstitutionID,
		CreatedAt:      now,
		PersonaID:      in.PersonaID,
		// v29 anchor + audit columns (spec 1276 T10).
		ArtifactSource: string(resolved.Source),
		ArtifactSHA256: hexBytes(resolved.ContentSHA256),
		ArtifactPath:   resolved.Path,
		ArtifactSize:   int64(len(resolved.Bytes)),
		ChunkIndex:     chunkIndex,
		ChunkTotal:     chunkTotal,
		NLIProviderID:  providerID,
	}
	if scoreErr != nil {
		// failure path: lower confidence, provenance in the JSON
		eval.Confidence = 0
	}
	return o.Store.SaveSDDEvaluation(ctx, wc, eval)
}

// persistDriftConsensusRow persists the consensus row with the
// ":consensus" suffix on TargetID. The VerdictJSON is the canonical
// drift-consensus shape (includes chunk stats + artifact provenance);
// the v29 audit columns (ArtifactSource, ArtifactSHA256, ArtifactPath,
// ArtifactSize, ChunkTotal, NLIProviderID) are populated as first-
// class columns so a reader can filter consensus rows without
// parsing the JSON blob. ChunkIndex for the consensus row is 0
// (the consensus row itself is not a chunk — the disambiguator is
// ChunkTotal > 0).
func (o *Orchestrator) persistDriftConsensusRow(
	ctx context.Context,
	in DriftJudgeConsensusInput,
	result *DriftJudgeConsensusResult,
) (int64, error) {
	wc := store.WriteContext{
		Actor:     "orchestrator_drift_judge_consensus",
		WritePath: "DriftJudgeConsensus",
	}
	now := o.now().Format(time.RFC3339Nano)
	eval := &ssd.SDDEvaluation{
		EvalType:       "drift_judge",
		TargetType:     in.TargetType,
		TargetID:       in.TargetID + ":consensus",
		VerdictJSON:    formatDriftConsensusVerdictJSON(result),
		Confidence:     result.AvgConfidence,
		Model:          "",
		ConstitutionID: wc.ConstitutionID,
		CreatedAt:      now,
		PersonaID:      in.PersonaID,
		// v29 anchor + audit columns (spec 1276 T10).
		ArtifactSource: result.ArtifactSource,
		ArtifactSHA256: result.ArtifactSHA256,
		ArtifactPath:   result.ArtifactPath,
		ArtifactSize:   result.ArtifactSize,
		ChunkIndex:     0,                         // consensus row is not a chunk
		ChunkTotal:     result.NumChunks,          // 0 if no chunks were scored
		NLIProviderID:  result.ProviderID,
	}
	return o.Store.SaveSDDEvaluation(ctx, wc, eval)
}

// formatDriftConsensusVerdictJSON produces the canonical verdict JSON
// for the drift-consensus row. Shape (stable, lex-ordered keys):
//
//	{
//	  "modal": "aligned",
//	  "fraction": 0.85,
//	  "avg_confidence": 0.82,
//	  "stddev_confidence": 0.04,
//	  "degraded": false,
//	  "failed": [],
//	  "requested_n": 3,
//	  "num_chunks": 7,
//	  "chunked_size": 12345,
//	  "provider_id": "deberta-v3-large-mnli",
//	  "artifact_source": "file",
//	  "artifact_sha256": "abc...",
//	  "artifact_path": "/path/to/file",
//	  "samples": [
//	    {"sample_index": 0, "chunk_start": 0, "chunk_end": 4096, "chunk_sha256": "...", "verdict": "aligned", "confidence": 0.92, "nli_label": "entailment", "nli_confidence": 0.92, "latency_ms": 100}
//	  ]
//	}
func formatDriftConsensusVerdictJSON(r *DriftJudgeConsensusResult) string {
	var b strings.Builder
	b.WriteString("{")
	b.WriteString(`"modal":"`)
	b.WriteString(r.ModalVerdict)
	b.WriteString(`",`)
	writeFloatField(&b, "fraction", r.ModalFraction)
	writeFloatField(&b, "avg_confidence", r.AvgConfidence)
	writeFloatField(&b, "stddev_confidence", r.StdDevConfidence)
	if r.Degraded {
		b.WriteString(`"degraded":true,"failed":[`)
		for i, f := range r.FailedSampleIndices {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(fmt.Sprintf("%d", f))
		}
		b.WriteString(`],`)
	} else {
		b.WriteString(`"degraded":false,`)
	}
	b.WriteString(fmt.Sprintf(`"requested_n":%d,`, r.RequestedN))
	b.WriteString(fmt.Sprintf(`"num_chunks":%d,`, r.NumChunks))
	b.WriteString(fmt.Sprintf(`"chunked_size":%d,`, r.ChunkedSize))
	if r.ProviderID != "" {
		b.WriteString(`"provider_id":`)
		writeJSONStringField(&b, r.ProviderID)
		b.WriteString(",")
	}
	if r.ArtifactSource != "" {
		b.WriteString(`"artifact_source":`)
		writeJSONStringField(&b, r.ArtifactSource)
		b.WriteString(",")
	}
	if r.ArtifactSHA256 != "" {
		b.WriteString(`"artifact_sha256":`)
		writeJSONStringField(&b, r.ArtifactSHA256)
		b.WriteString(",")
	}
	if r.ArtifactPath != "" {
		b.WriteString(`"artifact_path":`)
		writeJSONStringField(&b, r.ArtifactPath)
		b.WriteString(",")
	}
	b.WriteString(`"samples":[`)
	for i, s := range r.Samples {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("{")
		b.WriteString(fmt.Sprintf(`"sample_index":%d,`, s.SampleIndex))
		b.WriteString(fmt.Sprintf(`"chunk_start":%d,`, s.ChunkStart))
		b.WriteString(fmt.Sprintf(`"chunk_end":%d,`, s.ChunkEnd))
		if s.ChunkSHA256 != "" {
			b.WriteString(`"chunk_sha256":`)
			writeJSONStringField(&b, s.ChunkSHA256)
			b.WriteString(",")
		}
		b.WriteString(`"verdict":`)
		writeJSONStringField(&b, s.Verdict)
		b.WriteString(",")
		writeFloatField(&b, "confidence", s.Confidence)
		if s.NLILabel != "" {
			b.WriteString(`"nli_label":`)
			writeJSONStringField(&b, s.NLILabel)
			b.WriteString(",")
		}
		if s.NLIConfidence > 0 {
			b.WriteString(fmt.Sprintf(`"nli_confidence":%.4f,`, s.NLIConfidence))
		}
		b.WriteString(fmt.Sprintf(`"latency_ms":%d`, s.LatencyMS))
		b.WriteString("}")
	}
	b.WriteString("]}")
	return b.String()
}

// formatDriftChunkVerdictJSON produces the per-chunk verdict JSON
// for the SDDEvaluation row. Shape (stable, lex-ordered):
//
//	{
//	  "verdict": "aligned",
//	  "confidence": 0.92,
//	  "chunk_start": 0,
//	  "chunk_end": 4096,
//	  "chunk_sha256": "abc...",
//	  "nli_label": "entailment",
//	  "nli_confidence": 0.92,
//	  "provider_id": "deberta-v3-large-mnli",
//	  "model_rev": "rev-1",
//	  "latency_ms": 100
//	}
//
// scoreErr is the underlying error from nli.Provider.Score (nil on
// success). When non-nil, the verdict is "needs_human" and the
// "error" field carries the error string for audit.
func formatDriftChunkVerdictJSON(chunkIndex int, chunk chunkRange, chunkSHA [32]byte, score nli.Score, scoreErr error) string {
	_ = chunkIndex // reserved for future (currently not in the JSON; the chunk index is in TargetID)
	var b strings.Builder
	b.WriteString("{")
	verdict := nliLabelToDriftVerdict(score.Label)
	if scoreErr != nil {
		verdict = "needs_human"
	}
	b.WriteString(`"verdict":`)
	writeJSONStringField(&b, verdict)
	b.WriteString(",")
	conf := float32(0)
	if scoreErr == nil {
		conf = float32(score.Confidence)
	}
	writeFloatField(&b, "confidence", conf)
	b.WriteString(fmt.Sprintf(`"chunk_start":%d,`, chunk.Start))
	b.WriteString(fmt.Sprintf(`"chunk_end":%d,`, chunk.End))
	b.WriteString(`"chunk_sha256":`)
	writeJSONStringField(&b, hexBytes(chunkSHA))
	b.WriteString(",")
	if scoreErr == nil {
		if score.Label != "" {
			b.WriteString(`"nli_label":`)
			writeJSONStringField(&b, string(score.Label))
			b.WriteString(",")
		}
		if score.Confidence > 0 {
			b.WriteString(fmt.Sprintf(`"nli_confidence":%.4f,`, score.Confidence))
		}
		if score.ProviderID != "" {
			b.WriteString(`"provider_id":`)
			writeJSONStringField(&b, score.ProviderID)
			b.WriteString(",")
		}
		if score.ModelRev != "" {
			b.WriteString(`"model_rev":`)
			writeJSONStringField(&b, score.ModelRev)
			b.WriteString(",")
		}
		if score.LatencyMS > 0 {
			b.WriteString(fmt.Sprintf(`"latency_ms":%d,`, score.LatencyMS))
		}
	} else {
		b.WriteString(`"error":`)
		writeJSONStringField(&b, scoreErr.Error())
		b.WriteString(",")
	}
	// Strip trailing comma.
	s := b.String()
	if strings.HasSuffix(s, ",") {
		s = s[:len(s)-1]
	}
	return s + "}"
}

// writeFloatField writes "key":value as a 4-decimal float.
func writeFloatField(b *strings.Builder, key string, v float32) {
	b.WriteString(`"`)
	b.WriteString(key)
	b.WriteString(`":`)
	b.WriteString(fmt.Sprintf("%.4f", v))
	b.WriteString(",")
}

// writeJSONStringField writes a JSON-escaped string value (without
// trailing comma).
func writeJSONStringField(b *strings.Builder, s string) {
	b.WriteString(`"`)
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteString(`"`)
}

// extractProviderIDFromChunkJSON pulls the "provider_id" field out
// of a per-chunk verdict JSON. Used to compute the consensus row's
// provider_id without re-running the score.
func extractProviderIDFromChunkJSON(blob string) string {
	const key = `"provider_id":"`
	i := strings.Index(blob, key)
	if i < 0 {
		return ""
	}
	rest := blob[i+len(key):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// allIndices returns [0, 1, ..., n-1]. Used to mark all samples as
// failed when the artifact can't be resolved (degraded=true).
func allIndices(n int) []int {
	if n <= 0 {
		return nil
	}
	out := make([]int, n)
	for i := 0; i < n; i++ {
		out[i] = i
	}
	return out
}
