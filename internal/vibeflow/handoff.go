package vibeflow

import (
	"fmt"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Loop 13 L13.1 — sub-agent handoff contract
//
// WHY THIS FILE EXISTS
// ====================
//
// Row 251 (pinned, operator-validated) fixed the thesis for delegation in
// dark-memory long before this loop: delegating is NOT passing context
// in a prompt. It is:
//
//	1. CURATE   which subset of agent_memory the child inherits
//	2. MIND     build the right mindset (mindset_apply)
//	3. ISOLATE  the child (C2, subagent_register)
//	4. RECOVER  findings via recall, not via a return message
//	5. SYNTHESIZE from persistent memory, not from chat history
//
// All five stages are implemented: agent_memory_delegate, mindset_apply,
// subagent_register, agent_memory_recall and internal/delegation/router.go
// all exist and are wired (row 200/201 shipped them in v2.9.3; row 206
// smoke-tested C2 end to end). The MECHANISM IS BUILT.
//
// So this file is not a handoff mechanism. It is the CONTRACT that the
// mechanism was missing.
//
// THE PROBLEM: EVERY EPPISTEMIC RULE DIES AT THE BOUNDARY
// ========================================================
//
// Loops 11 and 12 built real machinery for telling the truth about
// evidence: EvidenceGrade, the independence ladder, arbitration, the
// unexamined default. Every word of it is true about ONE agent judging
// its OWN artifact.
//
// Delegation breaks all of it, in four specific ways:
//
//	A. CALIBRATION DOES NOT TRANSFER. A child's finding arrives in the
//	   parent as GradeUnverified by default. If the parent treats it as
//	   GradeMeasured because "it's in agent_memory", L11.1 collapses at
//	   the seam. PRESENCE IN MEMORY IS NOT EVIDENCE.
//
//	B. SELF-PREFERENCE PROPAGATES. Per arXiv:2404.13076 the danger is
//	   the evaluator being the evaluatee. A child-produced artifact is
//	   more independent of the parent judge than a parent-produced one,
//	   and until L13.1 nothing in the system could SEE that.
//
//	C. CORRELATED CONVERGENCE IS THE G19 TRAP WEARING A NEW HAT. Spawn
//	   five sub-agents on one model with one mindset and let them agree.
//	   A parent that counts that as five votes is committing the exact
//	   sin L12.1 exists to forbid, just with sub-agents instead of calls.
//
//	D. SYNTHESIS LAUNDERS. Row 251 stage 5 synthesizes from persistent
//	   memory. If synthesis reads findings without their grades and emits
//	   a synthesis without grades, aggregation has laundered uncaveated
//	   claims into an apparently consolidated conclusion.
//
// The rule that answers all four:
//
//	AN EPISTEMIC RULE THAT DOES NOT SURVIVE THE DELEGATION BOUNDARY
//	 WAS ONLY EVER TRUE FOR ONE AGENT.
//
// DESIGN PRINCIPLES (for human audit — same as L9.3 / L10.1 / L11.1 / L12.1)
// ========================================================================
//
//	A. NO BARE CROSSING. Every Finding that crosses the boundary
//	   carries Grade, Source and Caveat. A handoff that degrades a graded
//	   claim into a bare summary is refused by the type system, not by
//	   reviewer discipline.
//	B. ORIGIN IS MACHINE-VISIBLE. Whoever produced a claim is a field,
//	   not an assumption. This is the L12.1 third axis, wired through.
//	C. HEADCOUNT IS NOT EVIDENCE. Convergence grade is a function of
//	   DIVERSITY, never of how many agents agreed. Ten identical agents
//	   are worth exactly as much as one, and that is pinned by test.
//	D. UNRESOLVED DISPUTES TRAVEL AS UNRESOLVED. An open arbitration is
//	   handed over with its verdict intact, so the child cannot inherit
//	   an unaudited claim and re-emit it as settled.
//	E. DETERMINISTIC. Pure functions. No clock, no randomness, no I/O.
//	   Same input → same output, byte for byte.
//	F. NO NEW VOCABULARY WHERE AN EXISTING ONE WORKS. This file reuses
//	   L11.1's EvidenceGrade and L12.1's Basis / Resolution /
//	   Independence / JudgeSource rather than inventing parallel types.
//	   Two definitions of "independent" in one system is exactly the
//	   drift this project exists to prevent.
//
// ---------------------------------------------------------------------------

// Finding is one unit of work output crossing the delegation boundary.
//
// The Grade / Source / Caveat triple is NOT optional decoration. It is
// the payload. A Finding with an empty Caveat below GradeMeasured is a
// contract violation, and Validate rejects it — see
// HandoffSpec.Validate.
type Finding struct {
	// ID is a stable unique identity for this finding, used for dedup
	// and round-tripping. Two findings about the SAME claim must still
	// carry distinct IDs.
	ID string

	// Claim is the identifier of the claim this finding speaks to.
	//
	// This is a separate field from ID on purpose. Converge operates on
	// claims, not on findings: two findings corroborate each other only
	// if they address the same claim, and conflating identity with
	// subject would have made the dedup rule and the convergence rule
	// contradict each other.
	Claim string

	// Summary is the one-line description of what was found.
	Summary string

	// Grade is the evidence grade the PRODUCING agent assigned. It is
	// carried verbatim and is never silently promoted by the parent.
	Grade EvidenceGrade

	// Source names where the finding came from, e.g.
	// "agent_memory_recall(agent_memory)" or "subagent_probe".
	Source string

	// Caveat must be non-empty whenever Grade is below GradeMeasured.
	// Mirrors the L11.1 Claim invariant so a finding cannot launder a
	// weaker claim by changing container.
	Caveat string

	// Origin is who produced this (L13.1 third axis).
	Origin Origin

	// Delegate is the producing agent's full identity, so diversity can
	// be computed over the SET rather than guessed from a string.
	Delegate JudgeSource
}

// String renders a finding for audit output.
func (f Finding) String() string {
	return fmt.Sprintf("%s [%s] %s", f.ID, f.Grade, f.Summary)
}

// honest reports whether the finding satisfies the L11.1 invariant:
// anything weaker than measured must disclose its weakness.
func (f Finding) honest() bool {
	return f.Grade == GradeMeasured || strings.TrimSpace(f.Caveat) != ""
}

// ---------------------------------------------------------------------------
// HandoffSpec — what crosses from parent to child
// ---------------------------------------------------------------------------

// HandoffSpec is the curated set of state a parent hands to a sub-agent.
// It is the machine-checkable counterpart of row 251 stage 1 (CURATE).
//
// There is deliberately no free-text Summary field on this struct. A
// parent that wants to pass prose can still pass prose to the LLM, but
// the governed channel carries typed, graded, attributable state — which
// is how the laundering path in principle D gets closed.
type HandoffSpec struct {
	// SubagentID is the opaque id the parent generated and already
	// registered via agent_memory_delegate / subagent_register.
	SubagentID string

	// Task is what the child is being asked to do.
	Task string

	// Findings are the curated rows the child inherits. Ordered slice,
	// never a map, so rendering is deterministic.
	Findings []Finding

	// Disputes are UNRESOLVED arbitrations the parent is carrying.
	//
	// This field is the mechanical form of principle D. A parent that
	// resolved a dispute hands the Resolution and Basis along with it,
	// so a child reading "correlated-repeat" or "abstain" knows the
	// claim is NOT settled. Without this, the child inherits an
	// unaudited claim and re-emits it downstream as though it had been
	// verified — the laundering path, one delegation hop later.
	Disputes []Arbitration
}

// Validate reports every contract violation in the spec, so a caller can
// see all of them at once instead of fixing them one rejected call at a
// time.
//
// The rules mirror the guarantees the type system cannot make on its own:
// a Finding's caveat obligation (inherited from L11.1's Claim invariant)
// and the rule that only genuinely unresolved disputes travel.
func (h HandoffSpec) Validate() []string {
	var errs []string

	if strings.TrimSpace(h.SubagentID) == "" {
		errs = append(errs, "subagent_id is required")
	}
	if strings.TrimSpace(h.Task) == "" {
		errs = append(errs, "task is required")
	}

	seen := make(map[string]bool, len(h.Findings))
	for i, f := range h.Findings {
		if strings.TrimSpace(f.ID) == "" {
			errs = append(errs, fmt.Sprintf("findings[%d]: id is required", i))
		} else if seen[f.ID] {
			// A duplicated id would let the same claim be counted twice
			// in a convergence set, inflating headcount. Since headcount
			// is not evidence anyway this is cosmetic, but duplicate ids
			// are usually a real curation bug worth surfacing.
			errs = append(errs, fmt.Sprintf("findings[%d]: duplicate id %q", i, f.ID))
		}
		seen[f.ID] = true

		if strings.TrimSpace(f.Summary) == "" {
			errs = append(errs, fmt.Sprintf("findings[%d] (%s): summary is required", i, f.ID))
		}
		if strings.TrimSpace(f.Claim) == "" {
			errs = append(errs, fmt.Sprintf("findings[%d] (%s): claim is required — a finding that names no claim cannot corroborate anything", i, f.ID))
		}
		if !f.honest() {
			errs = append(errs, fmt.Sprintf(
				"findings[%d] (%s): grade %s is below measured and carries no caveat — a weaker claim may not cross the boundary uncaveated",
				i, f.ID, f.Grade))
		}
		if strings.TrimSpace(f.Source) == "" {
			errs = append(errs, fmt.Sprintf("findings[%d] (%s): source is required", i, f.ID))
		}
	}

	for i, d := range h.Disputes {
		// Only unresolved disputes belong in a handoff. Shipping a
		// settled dispute as if it were still open would misinform the
		// child; silently dropping one would launder it (principle D).
		if d.Resolution != ResolutionAbstain {
			errs = append(errs, fmt.Sprintf(
				"disputes[%d]: resolution is %s, not %s — only unresolved disputes travel in a handoff",
				i, d.Resolution, ResolutionAbstain))
		}
	}

	return errs
}

// Errs is a convenience wrapper for callers that only want the joined
// message.
func (h HandoffSpec) Errs() error {
	if e := h.Validate(); len(e) > 0 {
		return fmt.Errorf("handoff contract violated: %s", strings.Join(e, "; "))
	}
	return nil
}

// GradedFindings returns a Claim per finding, so a handoff flows through
// the same L11.1 evidence surface as everything else the operator sees.
// A curated view that bypasses the grade system would defeat the file.
func (h HandoffSpec) GradedFindings() []Claim {
	out := make([]Claim, 0, len(h.Findings))
	for _, f := range h.Findings {
		src := f.Source
		if f.Origin.known() {
			src += " from " + f.Origin.String()
		}
		out = append(out, Claim{
			Label:  f.ID,
			Value:  f.Summary,
			Grade:  f.Grade,
			Source: src,
			Caveat: f.Caveat,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Convergence — is N agreeing delegates corroboration?
// ---------------------------------------------------------------------------

// ConvergenceVerdict answers the only question row 251 stage 5 raises:
// several sub-agents reported the same thing. Does that make it more
// true?
//
// The answer this type gives is the whole point of L13.1:
//
//	THE GRADE IS A FUNCTION OF DIVERSITY, NEVER OF HEADCOUNT.
//
// Ten sub-agents running one model with one mindset are ONE observation
// sampled ten times. Counting them as ten agreeing witnesses is the
// G19 sin wearing a different hat, and it is the single most likely way
// a delegating agent launders an unverified claim into a "consensus".
type ConvergenceVerdict struct {
	// Count is how many findings agreed. Reported for audit and NEVER
	// an input to Grade. Exposed precisely so that a reader can see it
	// is decorative.
	Count int

	// DistinctModels, DistinctProviders and DistinctOrigins are the
	// diversity axes actually present in the agreeing set.
	DistinctModels    int
	DistinctProviders int
	DistinctOrigins   int

	// Independence is the BEST pairwise independence achieved within the
	// set. Using the best pair rather than the worst is deliberate and is
	// the one place L13.1 is generous: if even one member of the panel
	// is genuinely independent of another, a real second opinion exists.
	// The ladder then decides what that is worth.
	Independence Independence

	// SameClaim reports whether every finding in the set addresses the
	// SAME claim id.
	//
	// Read this carefully, because the name is the whole design: this
	// function does NOT and CANNOT verify that findings agree with each
	// other semantically. Deciding whether "no hole in the parser" and
	// "there IS a hole in the parser" mean the same thing is an LLM
	// judgment, not a string comparison, and a string comparison that
	// pretended otherwise was the original bug in this function.
	//
	// What this function verifies is strictly narrower, and narrower is
	// the point: the findings address ONE claim, and the agents producing
	// them are decorrelated enough for agreement to count as anything.
	// Semantic agreement is the caller's judgment and must stay visible
	// as such in the parent's reasoning.
	SameClaim bool

	// Basis, Resolution and Grade mirror L12.1's vocabulary. The
	// underlying grade the claim carried before the round is
	// IncomingGrade.
	Basis         Basis
	Resolution    Resolution
	Grade         EvidenceGrade
	IncomingGrade EvidenceGrade

	// Reason is a human-readable audit sentence. Never empty.
	Reason string
}

// String renders the verdict for audit.
//
// The wording is deliberate: it says "findings on one claim", never
// "agreeing findings". This function does not verify semantic agreement,
// so its own rendering must not imply that it did.
func (c ConvergenceVerdict) String() string {
	return fmt.Sprintf(
		"convergence: %s (basis=%s grade=%s) from %d findings on %s | diversity: %d models, %d providers, %d origins | best pairwise=%s\n  reason: %s\n",
		c.Resolution, c.Basis, c.Grade, c.Count, c.claimWord(),
		c.DistinctModels, c.DistinctProviders, c.DistinctOrigins,
		c.Independence, c.Reason)
}

// claimWord describes the panel shape without ever asserting agreement.
func (c ConvergenceVerdict) claimWord() string {
	switch {
	case c.Count == 0:
		return "no claim"
	case c.SameClaim:
		return "one claim"
	default:
		return "several claims"
	}
}

// distinct returns the number of distinct non-empty values in a set of
// sources. Empty strings are excluded, matching the conservatism of
// ClassifyIndependence: an unrecorded axis does not count as diversity.
func distinct(sources []JudgeSource, pick func(JudgeSource) string) int {
	seen := make(map[string]bool)
	for _, s := range sources {
		if v := pick(s); v != "" {
			seen[v] = true
		}
	}
	return len(seen)
}

// ComputeConvergence decides whether an agreeing set of findings from
// distinct delegates is corroboration.
//
// incoming is the evidence grade the claim carried BEFORE this delegate
// round. That parameter exists so the idempotence rule has a real prior
// to hold flat, exactly as Arbitrate does — a constant would have been
// a claim about nothing.
//
// Rules, in order:
//
//  1. An empty set is insufficient data, not unanimity. A panel of zero
//     that unanimously agrees proves nothing, and treating it as
//     corroboration would make "no evidence" the strongest possible
//     evidence.
//
//  2. A panel addressing MORE THAN ONE CLAIM cannot corroborate
//     anything. Its findings are about different subjects, so no amount
//     of diversity among them says anything about any single claim. The
//     verdict abstains and says so.
//
//     Note what is deliberately NOT checked here: whether the findings
//     agree with each other semantically. That is an LLM judgment. An
//     earlier version of this function compared finding IDs, treated
//     distinct IDs as agreement, and consequently PROMOTED a panel
//     whose members had reported opposite things — the exact laundering
//     this loop exists to prevent. SameClaim is the honest, checkable
//     precondition; semantic agreement stays the caller's to assert.
//
//  3. Correlated panel -> the grade is held EXACTLY flat. The claim
//     stands (they did address one claim) but nothing was learned, so
//     the basis is BasisCorrelatedRepeat regardless of Count.
//
//  4. A genuinely independent pair in the set -> the grade rises by
//     exactly one step, via the same promote() L12.1 uses.
//
// Note what rule 3 does NOT depend on: Count. One delegate and one
// hundred delegates on one model produce identical grades. That is the
// anti-voting property, and it is asserted directly by test rather than
// left to the reader to infer.
func ComputeConvergence(findings []Finding, incoming EvidenceGrade) ConvergenceVerdict {
	c := ConvergenceVerdict{
		Count:         len(findings),
		IncomingGrade: incoming,
		SameClaim:     false,
		Independence:  IndependenceNone,
		Basis:         BasisNoEvidence,
		Resolution:    ResolutionAbstain,
		Grade:         GradeUnverified,
	}

	// Rule 1 — an empty panel is not a unanimous one.
	if len(findings) == 0 {
		c.Reason = "no findings were returned, so there is no claim to corroborate; an empty panel is not unanimity"
		return c
	}

	sources := make([]JudgeSource, 0, len(findings))
	claims := make(map[string]bool, len(findings))
	for _, f := range findings {
		sources = append(sources, f.Delegate)
		claims[f.Claim] = true
	}
	// Rule 2 — a panel spanning several claims corroborates none of them.
	if len(claims) != 1 {
		c.Basis = BasisCorrelatedRepeat
		c.Reason = fmt.Sprintf(
			"the delegate panel spans %d distinct claims across %d findings; a panel addressing several claims cannot corroborate any one of them — escalate to Arbitrate with an independent authority",
			len(claims), len(findings))
		return c
	}
	c.SameClaim = true

	c.DistinctModels = distinct(sources, func(s JudgeSource) string { return s.ModelRev })
	c.DistinctProviders = distinct(sources, func(s JudgeSource) string { return s.ProviderID })
	for _, o := range originsOf(findings) {
		if o.known() {
			c.DistinctOrigins++
		}
	}

	// Best pairwise independence. Deterministic: the inputs are ordered,
	// and ties resolve to the first pair encountered.
	best := IndependenceNone
	for i := 0; i < len(sources); i++ {
		for j := i + 1; j < len(sources); j++ {
			if g := ClassifyIndependence(sources[i], sources[j]); g > best {
				best = g
			}
		}
	}
	c.Independence = best

	// A single-member panel has no pairs, so it is structurally
	// correlated with itself. Saying so plainly beats letting the
	// headline look like a one-person consensus.
	if len(sources) == 1 {
		c.Basis = BasisCorrelatedRepeat
		c.Resolution = ResolutionUpholdVerdict
		c.Grade = incoming
		c.Reason = "a single delegate has no independent counterpart; its finding stands uncorroborated"
		return c
	}

	switch best {
	case IndependenceCorrelated:
		c.Basis = BasisCorrelatedRepeat
		c.Resolution = ResolutionUpholdVerdict
		c.Grade = incoming // IDEMPOTENT — held exactly flat.
		c.Reason = fmt.Sprintf(
			"all %d delegates share a model, provider or origin (%d models, %d providers, %d origins); correlated panel corroboration cannot raise the evidence grade, no matter how many agents agreed",
			len(findings), c.DistinctModels, c.DistinctProviders, c.DistinctOrigins)
	case IndependenceIndependent:
		c.Basis = BasisIndependentEvidence
		c.Resolution = ResolutionUpholdVerdict
		c.Grade = promote(incoming)
		c.Reason = fmt.Sprintf(
			"the panel contains a genuinely independent pair (%d models, %d providers, %d origins across %d delegates); independent corroboration promotes the grade from %s to %s",
			c.DistinctModels, c.DistinctProviders, c.DistinctOrigins, len(findings), incoming, promote(incoming))
	default:
		c.Basis = BasisCorrelatedRepeat
		c.Resolution = ResolutionUpholdVerdict
		c.Grade = incoming
		c.Reason = fmt.Sprintf(
			"every delegate is the same authority (%d models, %d providers, %d origins); a panel of identical agents is one observation sampled %d times",
			c.DistinctModels, c.DistinctProviders, c.DistinctOrigins, len(findings))
	}
	return c
}

// originsOf returns the distinct origins present, sorted for determinism
// of the caller-visible count.
func originsOf(findings []Finding) []Origin {
	seen := make(map[Origin]bool)
	for _, f := range findings {
		seen[f.Origin] = true
	}
	out := make([]Origin, 0, len(seen))
	for o := range seen {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
