// Package judge — Rubric registry + scoring logic (ADR-007 §4).
//
// This file defines:
//
//   - Rubric (vibe_case + criteria + thresholds + version)
//   - CriterionDef (name + weight, no score — score is filled by the LLM in step [5])
//   - RubricRegistry (default registry + interface)
//   - Persona (judge identity for step [4])
//   - PersonaRegistry (default registry + interface)
//   - ComputeWeightedScore (weighted sum with tolerance)
//   - ThresholdToVerdict (score -> label, ADR-007 §4 thresholds)
//   - ApplyPerCriterionOverride (weight>=0.20 AND score<0.30 -> force needs_human)
//
// The 7 default rubrics (C1-C7) and 13 default personas are the
// canonical registry (Phase 6 alpha.18.1: added judge-decision +
// judge-research for C3+C4 canonical mapping per
// internal/v4alpha/vibe/spec.go:14-16). Operators can Register
// additional rubrics via the Registry API (future: Markdown override
// per spec 1155 v14).
package judge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
)

// ---------- Rubric ----------

// Rubric is one vibe_case's scoring framework. ADR-007 §4: each
// Rubric has 5 criteria whose weights sum to 1.0; the Pipeline in
// step [5] asks the LLM to score each, and step [6] sums Score*Weight.
type Rubric struct {
	// VibeCase is the canonical case label: "C1" through "C7".
	VibeCase string

	// PersonaID is the default persona for this rubric. Resolved
	// when the EvaluateRequest.PersonaID is empty.
	PersonaID string

	// Criteria lists the 5 scoring axes (name + weight).
	Criteria []CriterionDef

	// AlignedThreshold is the inclusive lower bound for the
	// "aligned" verdict (ADR-007 §4: 0.85).
	AlignedThreshold float64

	// DriftThreshold is the inclusive lower bound for the
	// "needs_human" verdict (ADR-007 §4: 0.50). Below this is
	// "drift_detected".
	DriftThreshold float64

	// Version is the sha256[:16] of (VibeCase + Criteria + thresholds).
	// Auto-computed by Register; never set manually.
	Version string
}

// CriterionDef declares one axis of a Rubric. The LLM fills Score +
// Note at evaluation time; Weight is fixed by the Rubric definition.
type CriterionDef struct {
	Name   string
	Weight float64
}

// ---------- Persona ----------

// Persona is one judge's identity. The Pipeline uses PersonaID to
// resolve the persona for an evaluation, which in turn drives the
// prompt template + scoring lens.
//
// Commit 1 registers 11 default personas (8 legacy + 3 v4 new).
// Commit 2 wires Markdown override (spec 1155 v14).
type Persona struct {
	// ID is the registry identifier (e.g. "judge-logical",
	// "judge-cross-modal"). Stable across versions.
	ID string

	// DisplayName is human-readable (e.g. "Logical Reasoning Judge").
	DisplayName string

	// ProviderHint is the recommended provider for this persona's
	// LLM call (e.g. "anthropic" for textual logic, "minimax" for
	// cross-modal). Empty means "any provider".
	ProviderHint string

	// Description is a 1-2 sentence role description, used in the
	// LLM prompt's system message.
	Description string

	// MarkdownOverride is an optional Markdown file path that
	// overrides the compiled defaults (spec 1155 v14 field-level
	// merge). Empty when no override.
	MarkdownOverride string
}

// ---------- RubricRegistry ----------

// RubricRegistry is the contract for vibe_case -> Rubric lookup.
type RubricRegistry interface {
	// Get returns the Rubric for a vibe_case or ErrRubricNotFound.
	Get(vibeCase string) (*Rubric, error)

	// List returns the registered vibe_cases in sorted order.
	List() []string

	// Register adds a Rubric to the registry. Validates the sum-
	// to-1.0 invariant on weights; computes Version. Returns
	// ErrRubricExists when the vibe_case is already registered.
	Register(r *Rubric) error
}

// defaultRubricRegistry is the in-memory registry. Concurrent-safe
// via RWMutex.
type defaultRubricRegistry struct {
	mu      sync.RWMutex
	byCase  map[string]*Rubric
}

// NewRubricRegistry returns a registry pre-populated with the 7
// canonical C1-C7 rubrics (per ADR-007 §4).
func NewRubricRegistry() RubricRegistry {
	r := &defaultRubricRegistry{byCase: make(map[string]*Rubric)}
	for _, def := range defaultRubricDefs {
		_ = r.Register(def)
	}
	return r
}

func (r *defaultRubricRegistry) Get(vibeCase string) (*Rubric, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rub, ok := r.byCase[vibeCase]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrRubricNotFound, vibeCase)
	}
	// Return a copy so callers can't mutate the registry's rubric.
	return cloneRubric(rub), nil
}

func (r *defaultRubricRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.byCase))
	for k := range r.byCase {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (r *defaultRubricRegistry) Register(rub *Rubric) error {
	if rub == nil {
		return fmt.Errorf("judge: nil rubric")
	}
	if rub.VibeCase == "" {
		return fmt.Errorf("judge: rubric missing VibeCase")
	}
	if len(rub.Criteria) == 0 {
		return fmt.Errorf("judge: rubric %q has no criteria", rub.VibeCase)
	}
	var sum float64
	seen := make(map[string]bool)
	for i, c := range rub.Criteria {
		if c.Name == "" {
			return fmt.Errorf("judge: rubric %q criteria[%d] empty name", rub.VibeCase, i)
		}
		if seen[c.Name] {
			return fmt.Errorf("judge: rubric %q has duplicate criterion %q", rub.VibeCase, c.Name)
		}
		seen[c.Name] = true
		if c.Weight < 0 || c.Weight > 1 {
			return fmt.Errorf("judge: rubric %q criterion %q weight %f out of [0,1]", rub.VibeCase, c.Name, c.Weight)
		}
		sum += c.Weight
	}
	// Tolerance: 1e-6 to absorb floating-point representation noise.
	if diff := sum - 1.0; diff > 1e-6 || diff < -1e-6 {
		return fmt.Errorf("judge: rubric %q weights sum to %f, must sum to 1.0", rub.VibeCase, sum)
	}
	if rub.AlignedThreshold < 0 || rub.AlignedThreshold > 1 {
		return fmt.Errorf("judge: rubric %q AlignedThreshold %f out of [0,1]", rub.VibeCase, rub.AlignedThreshold)
	}
	if rub.DriftThreshold < 0 || rub.DriftThreshold > 1 {
		return fmt.Errorf("judge: rubric %q DriftThreshold %f out of [0,1]", rub.VibeCase, rub.DriftThreshold)
	}
	if rub.AlignedThreshold <= rub.DriftThreshold {
		return fmt.Errorf("judge: rubric %q AlignedThreshold (%f) must be > DriftThreshold (%f)", rub.VibeCase, rub.AlignedThreshold, rub.DriftThreshold)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byCase[rub.VibeCase]; exists {
		return fmt.Errorf("judge: rubric %q already registered", rub.VibeCase)
	}
	// Compute version (sha256[:16] of canonical form).
	rub.Version = rubricVersionHash(rub)
	stored := cloneRubric(rub)
	r.byCase[rub.VibeCase] = stored
	return nil
}

func cloneRubric(r *Rubric) *Rubric {
	out := &Rubric{
		VibeCase:         r.VibeCase,
		PersonaID:        r.PersonaID,
		AlignedThreshold: r.AlignedThreshold,
		DriftThreshold:   r.DriftThreshold,
		Version:          r.Version,
		Criteria:         make([]CriterionDef, len(r.Criteria)),
	}
	copy(out.Criteria, r.Criteria)
	return out
}

// rubricVersionHash computes the Version field of a Rubric as
// sha256[:16] of a canonical form (sorted criteria + thresholds +
// vibe_case). Sorting makes the hash stable regardless of Criteria
// insertion order in source.
func rubricVersionHash(r *Rubric) string {
	sorted := make([]CriterionDef, len(r.Criteria))
	copy(sorted, r.Criteria)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	canonical := fmt.Sprintf("vibe=%s|persona=%s|aligned=%.4f|drift=%.4f|",
		r.VibeCase, r.PersonaID, r.AlignedThreshold, r.DriftThreshold)
	for _, c := range sorted {
		canonical += fmt.Sprintf("%s=%.4f;", c.Name, c.Weight)
	}
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])[:16]
}

// ---------- PersonaRegistry ----------

// PersonaRegistry is the contract for persona_id -> Persona lookup.
// EC-005 fires when an explicit persona_id has no registration.
type PersonaRegistry interface {
	Get(personaID string) (*Persona, error)
	List() []string
	Register(p *Persona) error
}

type defaultPersonaRegistry struct {
	mu    sync.RWMutex
	byID  map[string]*Persona
}

// NewPersonaRegistry returns a registry pre-populated with the 11
// canonical personas (8 legacy + 3 v4 new per ADR-007 §3).
func NewPersonaRegistry() PersonaRegistry {
	r := &defaultPersonaRegistry{byID: make(map[string]*Persona)}
	for _, p := range defaultPersonas {
		_ = r.Register(p)
	}
	return r
}

func (r *defaultPersonaRegistry) Get(id string) (*Persona, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrPersonaNotRegistered, id)
	}
	out := *p // shallow copy
	return &out, nil
}

func (r *defaultPersonaRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.byID))
	for k := range r.byID {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (r *defaultPersonaRegistry) Register(p *Persona) error {
	if p == nil {
		return fmt.Errorf("judge: nil persona")
	}
	if p.ID == "" {
		return fmt.Errorf("judge: persona missing ID")
	}
	if p.DisplayName == "" {
		return fmt.Errorf("judge: persona %q missing DisplayName", p.ID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byID[p.ID]; exists {
		return fmt.Errorf("judge: persona %q already registered", p.ID)
	}
	stored := *p
	r.byID[p.ID] = &stored
	return nil
}

// ---------- Threshold logic (ADR-007 §4) ----------

// ComputeWeightedScore returns sum(criteria[i].Score * criteria[i].Weight).
// Empty Criteria returns 0 (this happens when the verdict short-
// circuited via an edge case and step [5] was skipped).
func ComputeWeightedScore(criteria []Criterion) float64 {
	var sum float64
	for _, c := range criteria {
		sum += c.Score * c.Weight
	}
	return sum
}

// ThresholdToVerdict maps a weighted score to a label using the
// Rubric's thresholds.
//
// ADR-007 §4:
//
//	[aligned_threshold, 1.0] -> aligned
//	[drift_threshold, aligned_threshold) -> needs_human
//	[0.0, drift_threshold) -> drift_detected
//
// Empty criteria (no LLM call made) returns "needs_human" with
// confidence 0.0 — the operator must review manually.
func ThresholdToVerdict(score float64, rub *Rubric) string {
	if rub == nil {
		return VerdictNeedsHuman
	}
	if score >= rub.AlignedThreshold {
		return VerdictAligned
	}
	if score >= rub.DriftThreshold {
		return VerdictNeedsHuman
	}
	return VerdictDriftDetected
}

// ApplyPerCriterionOverride returns the final verdict label after
// considering the per-criterion override (ADR-007 §4):
//
//	any criterion with weight >= 0.20 AND score < 0.30
//	=> force verdict = needs_human
//
// This catches the case where a critical criterion fails badly but
// other criteria compensate enough to land in the aligned range.
//
// Returns the override verdict AND a description of which criterion
// triggered (empty string if no override).
func ApplyPerCriterionOverride(criteria []Criterion, baseVerdict string) (verdict string, overrideBy string) {
	verdict = baseVerdict
	for _, c := range criteria {
		if c.Weight >= 0.20 && c.Score < 0.30 {
			overrideBy = fmt.Sprintf("%s (weight=%.2f, score=%.2f)", c.Name, c.Weight, c.Score)
			if verdict == VerdictAligned {
				verdict = VerdictNeedsHuman
			}
			// For other verdicts, the override doesn't change the
			// label but we still report it for audit (a critical
			// criterion failing badly in a "drift_detected" verdict
			// is useful provenance).
		}
	}
	return verdict, overrideBy
}

// ---------- Canonical defaults ----------

// defaultRubricDefs are the 7 canonical C1-C7 rubrics per ADR-007 §4.
// Source: row 2049 atomic mirror. Verified weights sum to 1.0.
var defaultRubricDefs = []*Rubric{
	{
		VibeCase:         "C1",
		PersonaID:        "judge-logical",
		AlignedThreshold: 0.85,
		DriftThreshold:   0.50,
		Criteria: []CriterionDef{
			{Name: "correctness", Weight: 0.35},
			{Name: "tests", Weight: 0.25},
			{Name: "security", Weight: 0.20},
			{Name: "idiomatic", Weight: 0.10},
			{Name: "docs", Weight: 0.10},
		},
	},
	{
		VibeCase:         "C2",
		PersonaID:        "judge-logical",
		AlignedThreshold: 0.85,
		DriftThreshold:   0.50,
		Criteria: []CriterionDef{
			{Name: "faithfulness", Weight: 0.30},
			{Name: "intent_match", Weight: 0.25},
			{Name: "style", Weight: 0.15},
			{Name: "completeness", Weight: 0.15},
			{Name: "grammar", Weight: 0.15},
		},
	},
	{
		VibeCase:         "C3",
		PersonaID:        "judge-decision",
		AlignedThreshold: 0.85,
		DriftThreshold:   0.50,
		Criteria: []CriterionDef{
			{Name: "rationale_clarity", Weight: 0.30},
			{Name: "evidence_quality", Weight: 0.25},
			{Name: "alternatives_considered", Weight: 0.20},
			{Name: "reversibility", Weight: 0.15},
			{Name: "stakeholder_impact", Weight: 0.10},
		},
	},
	{
		VibeCase:         "C4",
		PersonaID:        "judge-research",
		AlignedThreshold: 0.85,
		DriftThreshold:   0.50,
		Criteria: []CriterionDef{
			{Name: "source_diversity", Weight: 0.25},
			{Name: "citation_quality", Weight: 0.25},
			{Name: "methodology", Weight: 0.20},
			{Name: "novelty", Weight: 0.15},
			{Name: "reproducibility", Weight: 0.15},
		},
	},
	{
		VibeCase:         "C5",
		PersonaID:        "judge-cross-modal",
		AlignedThreshold: 0.85,
		DriftThreshold:   0.50,
		Criteria: []CriterionDef{
			{Name: "subject_consistency", Weight: 0.30},
			{Name: "scene_transitions", Weight: 0.20},
			{Name: "audio_sync", Weight: 0.15},
			{Name: "narrative", Weight: 0.20},
			{Name: "duration_match", Weight: 0.15},
		},
	},
	{
		VibeCase:         "C6",
		PersonaID:        "judge-pipeline",
		AlignedThreshold: 0.85,
		DriftThreshold:   0.50,
		Criteria: []CriterionDef{
			{Name: "speech_clarity", Weight: 0.25},
			{Name: "noise_floor", Weight: 0.20},
			{Name: "voice_consistency", Weight: 0.20},
			{Name: "pacing", Weight: 0.20},
			{Name: "timbre_preservation", Weight: 0.15},
		},
	},
	{
		VibeCase:         "C7",
		PersonaID:        "judge-evidential",
		AlignedThreshold: 0.85,
		DriftThreshold:   0.50,
		Criteria: []CriterionDef{
			{Name: "invariant_compliance", Weight: 0.30},
			{Name: "audit_chain", Weight: 0.25},
			{Name: "rbac", Weight: 0.20},
			{Name: "provenance", Weight: 0.15},
			{Name: "documentation", Weight: 0.10},
		},
	},
}

// defaultPersonas are the 11 canonical personas (8 legacy + 3 v4 new).
// Source: row 2047 atomic mirror + ADR-007 §3.
var defaultPersonas = []*Persona{
	// 8 legacy personas (preserve v4-alpha.1 registry semantics).
	{ID: "judge-logical", DisplayName: "Logical Reasoning Judge", ProviderHint: "anthropic", Description: "Scores textual/code logic, faithfulness, tests, correctness."},
	{ID: "judge-visual", DisplayName: "Visual Reasoning Judge", ProviderHint: "anthropic", Description: "Scores image composition, color, accessibility (legacy visual lens)."},
	{ID: "judge-security", DisplayName: "Security Judge", ProviderHint: "anthropic", Description: "Scores security posture, RBAC, attack-surface reduction."},
	{ID: "judge-compositional", DisplayName: "Compositional Judge", ProviderHint: "anthropic", Description: "Scores style, grammar, narrative composition (text)." },
	{ID: "judge-mutation", DisplayName: "Mutation Testing Judge", ProviderHint: "anthropic", Description: "Scores test quality via mutation score, oracle truthfulness."},
	{ID: "judge-resilience", DisplayName: "Resilience Judge", ProviderHint: "anthropic", Description: "Scores rollback, blast radius, idempotence, observability."},
	{ID: "judge-evidential", DisplayName: "Evidential Judge", ProviderHint: "anthropic", Description: "Scores invariant compliance, audit chain, provenance, RBAC (governance lens)."},
	{ID: "judge-coverage", DisplayName: "Coverage Judge", ProviderHint: "anthropic", Description: "Scores coverage of a bundle (C5): provenance, lockfile parity, transitive safety."},

	// 3 v4 new personas (ADR-007 §3).
	{ID: "judge-cross-modal", DisplayName: "Cross-Modal Judge", ProviderHint: "minimax", Description: "Scores video artifacts (C5): subject consistency, scene transitions, audio sync, narrative, duration match. Multi-modal provider preferred."},
	{ID: "judge-pipeline", DisplayName: "Pipeline Judge", ProviderHint: "anthropic", Description: "Scores audio artifacts (C6): speech clarity, noise floor, voice consistency, pacing, timbre preservation."},
	{ID: "judge-opinion", DisplayName: "Opinion Judge", ProviderHint: "anthropic", Description: "Scores subjective artifacts where multiple valid framings exist (ADR-007 §3 reserved for future use)."},

	// 2 Phase 6 personas (alpha.18.1, Chunk 6.1) — C3 + C4 canonical.
	{ID: "judge-decision", DisplayName: "Decision Judge", ProviderHint: "anthropic", Description: "Scores decision artifacts (C3): rationale clarity, evidence quality, alternatives considered, reversibility, stakeholder impact."},
	{ID: "judge-research", DisplayName: "Research Judge", ProviderHint: "anthropic", Description: "Scores research artifacts (C4): source diversity, citation quality, methodology, novelty, reproducibility."},
}

// ---------- Compile-time defaults check ----------

// _ verifies that every default rubric's PersonaID resolves to a
// default persona. Catches "rubric points at unknown persona" bugs
// at init time rather than at first evaluation.
var _ = func() error {
	rr := NewRubricRegistry()
	pr := NewPersonaRegistry()
	for _, vc := range rr.List() {
		rub, _ := rr.Get(vc)
		if _, err := pr.Get(rub.PersonaID); err != nil {
			panic(fmt.Sprintf("judge: default rubric %q points at unregistered persona %q", vc, rub.PersonaID))
		}
	}
	return nil
}()
