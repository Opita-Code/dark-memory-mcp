// Package judge — Edge Case (EC) catalog (ADR-007 §8).
//
// 15 deterministic checks that run as step [3] (pre-flight, ECs 001-014)
// or step [6] (post-LLM, EC-015 + verifier) of the pipeline. When an EC
// fires with severity "fatal" or "error", the pipeline short-circuits
// without spending an LLM call (pre-flight) or without trusting the
// LLM's verdict (post-LLM).
//
// Stats (per row 2053):
//
//	Total ECs:    15
//	Fatal:        2 (EC-002, EC-003)
//	Error:        7 (EC-001, EC-004, EC-005, EC-006, EC-010, EC-011, EC-015)
//	Warn:         6 (EC-007, EC-008, EC-009, EC-012, EC-013, EC-014)
//	F-caught:     6 (EC-009, EC-010, EC-011, EC-013, EC-014, EC-015)
//
// Each EC is a PURE FUNCTION. Same PipelineContext in -> same hit (or
// nil) out. 100% reproducible. 0 LLM cost.
package judge

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ---------- EdgeCase interface ----------

// EdgeCase is one deterministic check. Implemented by ecDef (the
// common case). The interface is exported so custom ECs can be
// registered via EdgeCaseRunner.Register.
type EdgeCase interface {
	ID() string
	Severity() string  // "fatal" | "error" | "warn"
	Catches() string   // "F1"-"F7" | "general" | "infra" | "config" | "bias-mitigation"
	Description() string
	Check(ctx context.Context, pc *PipelineContext) *EdgeCaseHit
}

// ecDef is the canonical EdgeCase impl. Fields are fixed at
// declaration; check is a closure that captures the EC's logic.
type ecDef struct {
	id, severity, catches, description string
	check                              func(*PipelineContext) *EdgeCaseHit
}

func (e ecDef) ID() string         { return e.id }
func (e ecDef) Severity() string    { return e.severity }
func (e ecDef) Catches() string     { return e.catches }
func (e ecDef) Description() string { return e.description }
func (e ecDef) Check(_ context.Context, pc *PipelineContext) *EdgeCaseHit {
	if e.check == nil {
		return nil
	}
	return e.check(pc)
}

// ---------- PipelineContext ----------

// PipelineContext is the data passed to every EC. Built by the
// Pipeline incrementally as steps complete.
type PipelineContext struct {
	// Populated before step [2].
	ArtifactContent []byte
	ArtifactRef     *Ref
	EvalType        string
	SpecIntent      string
	VibeCase        string
	PersonaID       string
	SchemaVersion   string

	// Populated by step [2] (extractor).
	Evidence []Evidence

	// Populated after step [5] (LLM call).
	Verdict *Verdict

	// References to the pipeline's registries + LLM client.
	LLMClient LLMClient
	Personas  PersonaRegistry
	Rubrics   RubricRegistry
}

// ---------- EdgeCaseRunner ----------

// EdgeCaseRunner dispatches ECs in deterministic order. Two methods:
//
//   - RunPreFlight: ECs 001-014 (before step [5] LLM call)
//   - RunPostLLM:   EC-015 (after step [5], needs the LLM's verdict)
//
// Use RunAll to run both in sequence.
type EdgeCaseRunner struct {
	Cases []EdgeCase
}

// NewDefaultEdgeCaseRunner returns a runner with all 15 ECs in
// canonical ID order.
func NewDefaultEdgeCaseRunner() *EdgeCaseRunner {
	return &EdgeCaseRunner{Cases: []EdgeCase{
		EC001EmptyArtifact, EC002LLMUnavailable, EC003PromptInjection,
		EC004ArtifactTooLarge, EC005PersonaNotRegistered, EC006SpecIntentMissing,
		EC007SelfReference, EC008PositionBiasPotential, EC009ArithmeticMismatch,
		EC010DocVsCodeDrift, EC011ScopeOverClaim, EC012ImplicitAssumption,
		EC013PriorVersionContamination, EC014EvidenceMissing,
		EC015VerdictReasoningInconsistency,
	}}
}

// Register adds a custom EC. Returns ErrECAlreadyRegistered if the
// ID is already in the runner.
func (r *EdgeCaseRunner) Register(ec EdgeCase) error {
	for _, existing := range r.Cases {
		if existing.ID() == ec.ID() {
			return fmt.Errorf("judge: edge case %q already registered", ec.ID())
		}
	}
	r.Cases = append(r.Cases, ec)
	sort.Slice(r.Cases, func(i, j int) bool { return r.Cases[i].ID() < r.Cases[j].ID() })
	return nil
}

// ErrECAlreadyRegistered is returned by Register on duplicate IDs.
var ErrECAlreadyRegistered = fmt.Errorf("judge: EC already registered")

// RunPreFlight runs ECs 001-014 (everything except EC-015, which
// needs the LLM verdict). Returns hits in deterministic order.
func (r *EdgeCaseRunner) RunPreFlight(ctx context.Context, pc *PipelineContext) []EdgeCaseHit {
	return r.runFiltered(ctx, pc, func(id string) bool { return id != "EC-015" })
}

// RunPostLLM runs EC-015 (the only post-LLM EC).
func (r *EdgeCaseRunner) RunPostLLM(ctx context.Context, pc *PipelineContext) []EdgeCaseHit {
	return r.runFiltered(ctx, pc, func(id string) bool { return id == "EC-015" })
}

// RunAll runs all ECs in canonical ID order. Use this for ad-hoc
// audit queries; the Pipeline itself uses RunPreFlight + RunPostLLM.
func (r *EdgeCaseRunner) RunAll(ctx context.Context, pc *PipelineContext) []EdgeCaseHit {
	var hits []EdgeCaseHit
	for _, ec := range r.Cases {
		if hit := ec.Check(ctx, pc); hit != nil {
			hits = append(hits, *hit)
		}
	}
	return hits
}

func (r *EdgeCaseRunner) runFiltered(ctx context.Context, pc *PipelineContext, keep func(id string) bool) []EdgeCaseHit {
	var hits []EdgeCaseHit
	for _, ec := range r.Cases {
		if !keep(ec.ID()) {
			continue
		}
		if hit := ec.Check(ctx, pc); hit != nil {
			hits = append(hits, *hit)
		}
	}
	return hits
}

// ShortCircuitVerdict maps the hits to a verdict label when at
// least one hit is fatal/error. Returns ("", false) when no
// short-circuit.
//
// Precedence (highest first): errored > drift_detected > needs_human.
// Among multiple error-level hits, the highest-precedence verdict
// wins. This handles e.g. EC-005 (errored) firing alongside
// EC-006 (needs_human) — errored is reported because the pipeline
// cannot proceed.
func ShortCircuitVerdict(hits []EdgeCaseHit) (string, bool) {
	priority := map[string]int{
		VerdictErrored:       3,
		VerdictDriftDetected: 2,
		VerdictNeedsHuman:    1,
	}
	var bestVerdict string
	bestPriority := -1
	found := false
	for _, h := range hits {
		if h.Severity != "fatal" && h.Severity != "error" {
			continue
		}
		v, ok := shortCircuitByEC[h.ID]
		if !ok {
			continue
		}
		p := priority[v]
		if !found || p > bestPriority {
			bestVerdict = v
			bestPriority = p
			found = true
		}
	}
	if !found {
		return "", false
	}
	return bestVerdict, true
}

// shortCircuitByEC maps each fatal/error EC to its short-circuit
// verdict (per ADR-007 §8).
var shortCircuitByEC = map[string]string{
	"EC-001": VerdictNeedsHuman,
	"EC-002": VerdictErrored,
	"EC-003": VerdictDriftDetected,
	"EC-004": VerdictNeedsHuman,
	"EC-005": VerdictErrored,
	"EC-006": VerdictNeedsHuman,
	"EC-010": VerdictDriftDetected,
	"EC-011": VerdictNeedsHuman,
	"EC-015": VerdictNeedsHuman,
}

// ---------- The 15 ECs ----------

// EC-001: empty artifact.
var EC001EmptyArtifact = ecDef{
	id: "EC-001", severity: "error", catches: "general",
	description: "Artifact content is empty (no bytes / no rows)",
	check: func(pc *PipelineContext) *EdgeCaseHit {
		if len(pc.ArtifactContent) == 0 {
			return &EdgeCaseHit{ID: "EC-001", Severity: "error", Trigger: "artifact len 0", Catches: "general"}
		}
		return nil
	},
}

// EC-002: LLM provider unavailable. Fires when pc.LLMClient is nil
// (no provider wired) OR returns ErrLLMUnavailable on a probe.
var EC002LLMUnavailable = ecDef{
	id: "EC-002", severity: "fatal", catches: "infra",
	description: "LLM provider unavailable (no client / 5xx / timeout)",
	check: func(pc *PipelineContext) *EdgeCaseHit {
		if pc.LLMClient == nil {
			return &EdgeCaseHit{ID: "EC-002", Severity: "fatal", Trigger: "LLMClient is nil", Catches: "infra"}
		}
		// For commit 1, EC-002 is a presence-check; the Pipeline
		// runs an explicit probe before invoking this EC and
		// records the result. When pc.Verdict has a Provider field
		// set but no Content, that's a probe-failure signal from
		// the Pipeline. For commit 1, we treat any Verdict whose
		// PersonaID == "EC-002-PROBE-FAILED" as the probe result.
		// (See Pipeline.runPreFlight for the actual probe wiring.)
		if pc.Verdict != nil && pc.Verdict.PersonaID == "EC-002-PROBE-FAILED" {
			return &EdgeCaseHit{ID: "EC-002", Severity: "fatal", Trigger: "LLM probe failed", Catches: "infra"}
		}
		return nil
	},
}

// EC-003: prompt injection in artifact content. Scans ArtifactContent
// against PromptInjectionPatterns (10 regex).
var EC003PromptInjection = ecDef{
	id: "EC-003", severity: "fatal", catches: "F-injection",
	description: "Artifact content contains a prompt-injection pattern",
	check: func(pc *PipelineContext) *EdgeCaseHit {
		if pattern := MatchPromptInjection(string(pc.ArtifactContent)); pattern != "" {
			return &EdgeCaseHit{
				ID: "EC-003", Severity: "fatal",
				Trigger: "regex match against prompt-injection patterns: " + pattern,
				Catches: "F-injection",
			}
		}
		return nil
	},
}

// EC-004: artifact too large. Commit 1 uses a static 256 KiB cap.
// Commit 2 makes this provider-aware (80% of provider's context window).
var EC004ArtifactTooLarge = ecDef{
	id: "EC-004", severity: "error", catches: "infra",
	description: "Artifact size exceeds configured MaxArtifactBytes",
	check: func(pc *PipelineContext) *EdgeCaseHit {
		const defaultMax = 256 * 1024
		max := defaultMax
		if pc.ArtifactRef != nil && pc.ArtifactRef.MaxBytes > 0 {
			max = pc.ArtifactRef.MaxBytes
		}
		if len(pc.ArtifactContent) > max {
			return &EdgeCaseHit{
				ID: "EC-004", Severity: "error",
				Trigger: fmt.Sprintf("artifact size %d > %d", len(pc.ArtifactContent), max),
				Catches: "infra",
			}
		}
		return nil
	},
}

// EC-005: persona not registered. Fires when PersonaID is non-empty
// and not in the registry.
var EC005PersonaNotRegistered = ecDef{
	id: "EC-005", severity: "error", catches: "config",
	description: "Explicit PersonaID is not in the PersonaRegistry",
	check: func(pc *PipelineContext) *EdgeCaseHit {
		if pc.PersonaID == "" {
			return nil
		}
		if _, err := pc.Personas.Get(pc.PersonaID); err != nil {
			return &EdgeCaseHit{
				ID: "EC-005", Severity: "error",
				Trigger: fmt.Sprintf("persona %q not registered", pc.PersonaID),
				Catches: "config",
			}
		}
		return nil
	},
}

// EC-006: spec intent missing. Fires when SpecIntent is empty or
// shorter than 10 chars (the minimum for a meaningful hypothesis).
var EC006SpecIntentMissing = ecDef{
	id: "EC-006", severity: "error", catches: "config",
	description: "SpecIntent is empty or too short (< 10 chars)",
	check: func(pc *PipelineContext) *EdgeCaseHit {
		if len(strings.TrimSpace(pc.SpecIntent)) < 10 {
			return &EdgeCaseHit{
				ID: "EC-006", Severity: "error",
				Trigger: fmt.Sprintf("spec_intent len %d < 10", len(pc.SpecIntent)),
				Catches: "config",
			}
		}
		return nil
	},
}

// EC-007: self-reference (judge == author). Fires when the persona's
// ProviderHint matches a marker in the artifact content. Commit 1
// uses a simple heuristic: artifact contains "author: <provider>" and
// the persona's ProviderHint matches.
var EC007SelfReference = ecDef{
	id: "EC-007", severity: "warn", catches: "bias-mitigation",
	description: "Persona provider matches artifact author provider (self-judge)",
	check: func(pc *PipelineContext) *EdgeCaseHit {
		if pc.PersonaID == "" || pc.Personas == nil {
			return nil
		}
		p, err := pc.Personas.Get(pc.PersonaID)
		if err != nil || p.ProviderHint == "" {
			return nil
		}
		marker := fmt.Sprintf("author: %s", p.ProviderHint)
		if strings.Contains(strings.ToLower(string(pc.ArtifactContent)), strings.ToLower(marker)) {
			return &EdgeCaseHit{
				ID: "EC-007", Severity: "warn",
				Trigger: fmt.Sprintf("persona provider %q matches artifact author marker", p.ProviderHint),
				Catches: "bias-mitigation",
			}
		}
		return nil
	},
}

// EC-008: position bias potential. Fires when EvalType is "pairwise"
// (we don't support pairwise in commit 1; this is a placeholder).
var EC008PositionBiasPotential = ecDef{
	id: "EC-008", severity: "warn", catches: "bias-mitigation",
	description: "Pairwise eval without position swap (Zheng et al. 2023)",
	check: func(pc *PipelineContext) *EdgeCaseHit {
		if pc.EvalType == "pairwise" {
			return &EdgeCaseHit{
				ID: "EC-008", Severity: "warn",
				Trigger: "pairwise eval type without position swap recorded",
				Catches: "bias-mitigation",
			}
		}
		return nil
	},
}

// EC-009: arithmetic mismatch. Caza F2 ("32 tools" when sum was 34).
var arithmeticRe = regexp.MustCompile(`(\d+)\s*([+\-*/])\s*(\d+)\s*=\s*(\d+)`)

var EC009ArithmeticMismatch = ecDef{
	id: "EC-009", severity: "warn", catches: "F2",
	description: "Artifact contains an arithmetic claim that doesn't compute",
	check: func(pc *PipelineContext) *EdgeCaseHit {
		text := string(pc.ArtifactContent)
		matches := arithmeticRe.FindAllStringSubmatch(text, -1)
		for _, m := range matches {
			a, errA := strconv.Atoi(m[1])
			b, errB := strconv.Atoi(m[3])
			c, errC := strconv.Atoi(m[4])
			if errA != nil || errB != nil || errC != nil {
				continue
			}
			var expected int
			switch m[2] {
			case "+":
				expected = a + b
			case "-":
				expected = a - b
			case "*":
				expected = a * b
			case "/":
				if b == 0 {
					continue
				}
				expected = a / b
			default:
				continue
			}
			if expected != c {
				return &EdgeCaseHit{
					ID: "EC-009", Severity: "warn",
					Trigger: fmt.Sprintf("arithmetic mismatch: %d%s%d=%d (computed %d)", a, m[2], b, c, expected),
					Catches: "F2",
				}
			}
		}
		return nil
	},
}

// EC-010: doc-vs-code drift. Caza F1 (row 2040 "switched to regular
// FTS5" vs contentless schema in agent_memory.go:116-117).
//
// Commit 1 heuristic: when Evidence references "file:///path:line-line"
// but the artifact content doesn't contain that line range marker, fire.
var fileLineRe = regexp.MustCompile(`file://[^:]+:(\d+)-(\d+)`)

var EC010DocVsCodeDrift = ecDef{
	id: "EC-010", severity: "error", catches: "F1",
	description: "Evidence cites file:line range that doesn't appear in artifact content",
	check: func(pc *PipelineContext) *EdgeCaseHit {
		// Check Evidence sources for file:line references.
		for _, e := range pc.Evidence {
			m := fileLineRe.FindStringSubmatch(e.Source)
			if m == nil {
				continue
			}
			// The Evidence's Source claims a file:line range. Verify
			// that the artifact content contains a marker near that
			// range. If not, the evidence is stale.
			rangeStr := m[0][strings.LastIndex(m[0], ":")+1:]
			if !strings.Contains(string(pc.ArtifactContent), rangeStr) {
				return &EdgeCaseHit{
					ID: "EC-010", Severity: "error",
					Trigger: fmt.Sprintf("file:line %s cited in evidence not present in artifact content", rangeStr),
					Catches: "F1",
				}
			}
		}
		return nil
	},
}

// EC-011: scope over-claim. Caza F4 ("BUG-9 = 32 tools + audit +
// Judge" copy-paste from row 2040). Fires when artifact claims
// "all N" but the count of BUG-N markers differs.
var allNRe = regexp.MustCompile(`(?i)all\s+(\d+)\s+\w+`)
var bugNRe = regexp.MustCompile(`BUG-\d+`)

var EC011ScopeOverClaim = ecDef{
	id: "EC-011", severity: "error", catches: "F4",
	description: "Artifact claims 'all N' but the actual count differs",
	check: func(pc *PipelineContext) *EdgeCaseHit {
		text := string(pc.ArtifactContent)
		matches := allNRe.FindAllStringSubmatch(text, -1)
		if len(matches) == 0 {
			return nil
		}
		bugCount := len(bugNRe.FindAllString(text, -1))
		if bugCount == 0 {
			return nil
		}
		for _, m := range matches {
			claimN, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			if claimN > 0 && claimN != bugCount {
				return &EdgeCaseHit{
					ID: "EC-011", Severity: "error",
					Trigger: fmt.Sprintf("scope over-claim: 'all %d' but %d BUG-N markers in artifact", claimN, bugCount),
					Catches: "F4",
				}
			}
		}
		return nil
	},
}

// EC-012: implicit assumption. Fires when artifact contains
// "assuming X" / "if Y" that doesn't appear in SpecIntent.
var implicitAssumptionRe = regexp.MustCompile(`(?i)(assuming|if)\s+([^.,;]+)`)

var EC012ImplicitAssumption = ecDef{
	id: "EC-012", severity: "warn", catches: "general",
	description: "Artifact contains 'assuming X' / 'if Y' not in SpecIntent",
	check: func(pc *PipelineContext) *EdgeCaseHit {
		text := string(pc.ArtifactContent)
		matches := implicitAssumptionRe.FindAllStringSubmatch(text, -1)
		specLower := strings.ToLower(pc.SpecIntent)
		for _, m := range matches {
			assumption := strings.TrimSpace(m[2])
			if len(assumption) < 5 {
				continue
			}
			if !strings.Contains(specLower, strings.ToLower(assumption)) {
				return &EdgeCaseHit{
					ID: "EC-012", Severity: "warn",
					Trigger: fmt.Sprintf("implicit assumption not in spec_intent: %q", assumption),
					Catches: "general",
				}
			}
		}
		return nil
	},
}

// EC-013: prior-version contamination. Caza F6 (mixing error_obs/
// package vs transport/mcp/error_obs.go file). Fires when artifact
// references paths from a different schema version.
//
// Commit 1 heuristic: artifact contains "internal/v2alpha/" or
// "dark-memory-mcp/v2.20.0" while SchemaVersion starts with
// "v4alpha/".
var priorVersionPathRe = regexp.MustCompile(`internal/v2alpha/|v2\.20\.0|docs/v3\.0`)

var EC013PriorVersionContamination = ecDef{
	id: "EC-013", severity: "warn", catches: "F6",
	description: "Artifact references paths from a prior schema version",
	check: func(pc *PipelineContext) *EdgeCaseHit {
		if !strings.HasPrefix(pc.SchemaVersion, "v4alpha/") {
			return nil
		}
		text := string(pc.ArtifactContent)
		if match := priorVersionPathRe.FindString(text); match != "" {
			return &EdgeCaseHit{
				ID: "EC-013", Severity: "warn",
				Trigger: fmt.Sprintf("artifact references prior-version path %q under v4alpha schema", match),
				Catches: "F6",
			}
		}
		return nil
	},
}

// EC-014: evidence missing. Caza F3 (claims without grep output) and
// F5 (claims without enumerating what they cover).
//
// Commit 1 heuristic: when the artifact contains a verifiable claim
// like "All N schemas apply" without listing them (no list markers
// nearby), flag it.
var verifiableClaimRe = regexp.MustCompile(`(?i)(all\s+\d+|every\s+\w+|each\s+of\s+the)`)

var EC014EvidenceMissing = ecDef{
	id: "EC-014", severity: "warn", catches: "F3,F5",
	description: "Artifact makes verifiable claim without enumerated evidence",
	check: func(pc *PipelineContext) *EdgeCaseHit {
		text := string(pc.ArtifactContent)
		matches := verifiableClaimRe.FindAllString(text, -1)
		if len(matches) == 0 {
			return nil
		}
		// Check if there are list-like markers (numbered or bulleted)
		// in the vicinity. If not, the claim lacks evidence.
		hasListMarkers := strings.Contains(text, "\n- ") ||
			regexp.MustCompile(`\n\d+\.\s`).MatchString(text) ||
			regexp.MustCompile(`\n\*\s`).MatchString(text)
		if !hasListMarkers {
			return &EdgeCaseHit{
				ID: "EC-014", Severity: "warn",
				Trigger: "artifact has verifiable claim(s) without enumerated evidence (no list markers)",
				Catches: "F3,F5",
			}
		}
		return nil
	},
}

// EC-015: verdict/reasoning inconsistency. Caza F7 (verdict=aligned
// when reasoning lists problems). Runs AFTER step [5] (LLM call).
//
// Substring match (not word-boundary): inflections like "failed",
// "fails", "failing" all indicate a problem. False positives
// ("driftwood", "fairly") are recoverable by the operator review
// that the override triggers (needs_human).
var inconsistencyKeywords = []string{"drift", "missing", "fail", "fail-closed", "broken", "error"}

var EC015VerdictReasoningInconsistency = ecDef{
	id: "EC-015", severity: "error", catches: "F7",
	description: "LLM verdict=aligned but reasoning contains negative keywords",
	check: func(pc *PipelineContext) *EdgeCaseHit {
		if pc.Verdict == nil {
			return nil
		}
		if pc.Verdict.Verdict != VerdictAligned {
			return nil
		}
		reasoningLower := strings.ToLower(pc.Verdict.Reasoning)
		for _, kw := range inconsistencyKeywords {
			if strings.Contains(reasoningLower, kw) {
				return &EdgeCaseHit{
					ID: "EC-015", Severity: "error",
					Trigger: fmt.Sprintf("verdict=aligned but reasoning contains %q", kw),
					Catches: "F7",
				}
			}
		}
		return nil
	},
}
