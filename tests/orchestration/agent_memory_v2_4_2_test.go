// Package orchestration_test: v2.4.2 brand_match + compliance_check
// enrichment tests.
//
// v2.4.2 closes the "judges are blind to prior context" debt for
// brand_match and compliance_check. The enrichment lives in the
// Judge orchestrator (judge.go), NOT in PublishVibe — drift_judge
// enrichment still lives in PublishVibe (v2.4.0) and is untouched
// here. This file verifies:
//
//   - TestV242_BrandMatch_EnrichesWithBrandDecisions
//     Judge(eval_type=brand_match) prepends agent_memory hits with
//     kind=decision (filtered by resolved agent_id) to the LLM
//     prompt. Findings/links/notes are filtered out.
//
//   - TestV242_ComplianceCheck_EnrichesWithDecisionsAndFindings
//     Judge(eval_type=compliance_check) prepends both kind=decision
//     AND kind=finding hits (filtered by agent_id).
//
//   - TestV242_BrandMatch_NoEnrich_RespectsOptOut
//     Judge with NoEnrich=true passes RAW content to the LLM (no
//     enrichment block). Verifies the opt-out escape hatch works.
//
//   - TestV242_PIIDetect_NoEnrichment
//     Judge(eval_type=pii_detect) does NOT enrich — pattern-matching
//     eval_type is out of enrichment scope.
//
//   - TestV242_Consensus_PassesAgentIDToAllSamples
//     JudgeConsensus(eval_type=brand_match) forwards AgentID to all
//     N Judge samples so each sample sees the same agent-scoped
//     enrichment.
//
//   - TestV242_AgentID_PriorityChain_ResolvesInJudge
//     Judge resolves AgentID via the v2.4.1 priority chain
//     (caller input > projects.default_agent_id > "") — same
//     resolveActiveAgentID helper used by SessionStart and
//     PublishVibe.
//
//   - TestV242_DriftJudge_EnrichmentUnchangedInPublishVibe
//     Verifies drift_judge enrichment still lives in PublishVibe
//     (NOT moved to Judge in v2.4.2). Direct Judge callers of
//     drift_judge do NOT get enriched — deliberate scope boundary.
package orchestration_test

import (
	"context"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// v242Orch returns an orchestrator + store backed by a fresh SQLite
// DB with migrations applied + the 'acme' project as active.
func v242Orch(t *testing.T) (*orchestration.Orchestrator, store.Store) {
	t.Helper()
	orch, st := v241Orch(t) // v2.4.1 helper creates 'acme' + sets active
	return orch, st
}

// v242Pin flips the Pinned bit on a memory row by id. Required
// because v2.4.2 brand_match + compliance_check enrichment uses
// PINNED memories (operator-curated, predictable) — not BM25 search.
// The operator must pin a memory to surface it as prior context.
func v242Pin(t *testing.T, st store.Store, id int64) {
	t.Helper()
	pin := true
	if _, err := st.UpdateAgentMemory(context.Background(), store.WriteContext{Actor: "test", WritePath: "v242_test"}, id, &agentmemory.AgentMemoryUpdate{Pinned: &pin}); err != nil {
		t.Fatalf("pin %d: %v", id, err)
	}
}

// TestV242_BrandMatch_EnrichesWithBrandDecisions seeds 3 kinds of
// agent_memory rows (decision / finding / link), all authored by
// the same agent_id and PINNED. Calls Judge(brand_match) with
// explicit agent_id and verifies the LLM was called with content
// that contains the decision but NOT the finding or link
// (kinds=[decision]).
//
// PINNED-only behavior: v2.4.2 uses pinned memories instead of
// BM25 search. Brand decisions are operator-curated — the
// operator pinned them because they're the brand canon. To verify
// the kind filter works (not just the pinned filter), we pin
// all 3 rows but expect only the decision to surface.
func TestV242_BrandMatch_EnrichesWithBrandDecisions(t *testing.T) {
	orch, st := v242Orch(t)
	ctx := context.Background()

	// Seed: 1 brand decision + 1 brand finding + 1 brand link, all
	// authored by "claude-sonnet-4" (the resolved agent_id).
	idDec := v240SaveMemory(t, orch, "alice", "decision",
		"Brand voice: bold and warm. Never use corporate jargon.", "claude-sonnet-4", "semantic")
	idFin := v240SaveMemory(t, orch, "alice", "finding",
		"Research shows brand X is associated with sustainability claims.", "claude-sonnet-4", "episodic")
	idLink := v240SaveMemory(t, orch, "alice", "link",
		"https://brand.opitacode.com/style-guide.pdf", "claude-sonnet-4", "")
	// Pin all three so the kind filter (not the pinned filter) is
	// what differentiates them.
	v242Pin(t, st, idDec)
	v242Pin(t, st, idFin)
	v242Pin(t, st, idLink)

	// MockLLMClient that just records the request. The Judge will
	// enrich the content BEFORE calling the LLM, so the LLM's
	// LastReq.Content should contain the enrichment block + the
	// raw content.
	mock := &orchestration.MockLLMClient{
		Name_:       "mock_v242",
		VerdictJSON: `{"verdict":"match","score":0.85,"issues":[]}`,
		Confidence:  0.85,
		Model:       "mock-v242",
	}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))

	rawContent := "Our new copy: We deliver bold, warm service. No jargon."
	out, err := orch.Judge(ctx, orchestration.JudgeInput{
		EvalType: "brand_match",
		Content:  rawContent,
		AgentID:  "claude-sonnet-4",
	})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if out.EvaluationID == 0 {
		t.Fatalf("EvaluationID should be > 0")
	}

	// Verify LLM was called once + saw the enriched content.
	if mock.Calls != 1 {
		t.Fatalf("LLM should have been called once, got %d", mock.Calls)
	}
	enriched := mock.LastReq.Content

	// 1. The decision must be present (enriched block prepended).
	if !strings.Contains(enriched, "Brand voice: bold and warm") {
		t.Errorf("LLM did not see the brand decision in enriched content; got:\n%s", truncateForTest(enriched, 400))
	}
	// 2. The finding must NOT be present (kinds=[decision] only).
	if strings.Contains(enriched, "Research shows brand X is associated") {
		t.Errorf("LLM saw the finding; brand_match should only inject kind=decision. Got:\n%s", truncateForTest(enriched, 400))
	}
	// 3. The link must NOT be present.
	if strings.Contains(enriched, "brand.opitacode.com") {
		t.Errorf("LLM saw the link; brand_match should only inject kind=decision. Got:\n%s", truncateForTest(enriched, 400))
	}
	// 4. The raw content must still be present.
	if !strings.Contains(enriched, rawContent) {
		t.Errorf("raw content missing from enriched prompt")
	}
	// 5. The enrichment header must be present.
	if !strings.Contains(enriched, "Relevant prior context") {
		t.Errorf("enrichment header missing; got:\n%s", truncateForTest(enriched, 400))
	}
}

// TestV242_ComplianceCheck_EnrichesWithDecisionsAndFindings seeds
// a decision + a finding + a note, all compliance-related, all by
// the same agent and PINNED. Calls Judge(compliance_check) and
// verifies the LLM saw both decision + finding but NOT the note.
func TestV242_ComplianceCheck_EnrichesWithDecisionsAndFindings(t *testing.T) {
	orch, st := v242Orch(t)
	ctx := context.Background()

	// Seed: 1 jurisdictional decision + 1 prior flag + 1 generic
	// note (must NOT be enriched). All authored by "claude-sonnet-4"
	// and PINNED so the kind filter (not the pinned filter) is what
	// differentiates them. Contents are kept under 80 chars so the
	// formatHitsForContext title fallback doesn't truncate the
	// assertion substrings.
	idDec := v240SaveMemory(t, orch, "alice", "decision",
		"EU jurisdiction requires GDPR Article 13 disclosure.", "claude-sonnet-4", "semantic")
	idFin := v240SaveMemory(t, orch, "alice", "finding",
		"2026-Q1 audit: missing EU AI Act synthetic-media disclosure.", "claude-sonnet-4", "episodic")
	idNote := v240SaveMemory(t, orch, "alice", "note",
		"This project uses pnpm; the auditor flagged yarn.lock inconsistencies.", "claude-sonnet-4", "")
	v242Pin(t, st, idDec)
	v242Pin(t, st, idFin)
	v242Pin(t, st, idNote)

	mock := &orchestration.MockLLMClient{
		Name_:       "mock_v242_comp",
		VerdictJSON: `{"verdict":"non_compliant","issues":[],"required_disclosures":["EU AI Act Art. 50"]}`,
		Confidence:  0.78,
		Model:       "mock-v242",
	}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))

	rawContent := "Generated marketing copy for the EU market: 'AI-powered product X.'"
	out, err := orch.Judge(ctx, orchestration.JudgeInput{
		EvalType:   "compliance_check",
		TargetType: "artifact",
		TargetID:   "test_eu_copy",
		Content:    rawContent,
		AgentID:    "claude-sonnet-4",
	})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if out.EvaluationID == 0 {
		t.Fatalf("EvaluationID should be > 0")
	}
	if mock.Calls != 1 {
		t.Fatalf("LLM should have been called once, got %d", mock.Calls)
	}
	enriched := mock.LastReq.Content

	// 1. The decision must be present.
	if !strings.Contains(enriched, "GDPR Article 13") {
		t.Errorf("LLM did not see the compliance decision; got:\n%s", truncateForTest(enriched, 400))
	}
	// 2. The finding must be present (kinds=[decision, finding]).
	if !strings.Contains(enriched, "EU AI Act synthetic-media disclosure") {
		t.Errorf("LLM did not see the compliance finding; got:\n%s", truncateForTest(enriched, 400))
	}
	// 3. The note must NOT be present (note is not in kinds).
	if strings.Contains(enriched, "yarn.lock") {
		t.Errorf("LLM saw the note; compliance_check should only inject decision+finding. Got:\n%s", truncateForTest(enriched, 400))
	}
	// 4. Raw content must still be present.
	if !strings.Contains(enriched, rawContent) {
		t.Errorf("raw content missing from enriched prompt")
	}
}

// TestV242_BrandMatch_NoEnrich_RespectsOptOut calls Judge(brand_match)
// with NoEnrich=true and verifies the LLM was called with the RAW
// content — no enrichment block, no agent_memory hits.
func TestV242_BrandMatch_NoEnrich_RespectsOptOut(t *testing.T) {
	orch, st := v242Orch(t)
	ctx := context.Background()

	// Seed + pin a decision that WOULD be injected if NoEnrich=false.
	id := v240SaveMemory(t, orch, "alice", "decision",
		"Brand voice: bold and warm. Never use corporate jargon.", "claude-sonnet-4", "semantic")
	v242Pin(t, st, id)

	mock := &orchestration.MockLLMClient{
		Name_:       "mock_v242_no_enrich",
		VerdictJSON: `{"verdict":"match","score":0.92,"issues":[]}`,
		Confidence:  0.92,
		Model:       "mock-v242",
	}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))

	rawContent := "Our copy uses bold, warm language and no corporate jargon."
	out, err := orch.Judge(ctx, orchestration.JudgeInput{
		EvalType: "brand_match",
		Content:  rawContent,
		AgentID:  "claude-sonnet-4",
		NoEnrich: true, // v2.4.2: opt-out escape hatch
	})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if out.EvaluationID == 0 {
		t.Fatalf("EvaluationID should be > 0")
	}
	enriched := mock.LastReq.Content

	// NoEnrich=true → LLM must see EXACTLY the raw content (no
	// enrichment block, no agent_memory hits).
	if enriched != rawContent {
		t.Errorf("NoEnrich=true: LLM saw enriched content; want raw.\nGot: %s\nWant: %s",
			truncateForTest(enriched, 200), rawContent)
	}
	if strings.Contains(enriched, "Brand voice") {
		t.Errorf("NoEnrich=true: LLM saw the brand decision; got:\n%s", truncateForTest(enriched, 400))
	}
	if strings.Contains(enriched, "Relevant prior context") {
		t.Errorf("NoEnrich=true: LLM saw the enrichment header; got:\n%s", truncateForTest(enriched, 400))
	}
}

// TestV242_PIIDetect_NoEnrichment verifies that pii_detect does NOT
// enrich. PII is pattern-matched; adding memory-RAG would inject
// noisy context and slow down the LLM call.
func TestV242_PIIDetect_NoEnrichment(t *testing.T) {
	orch, _ := v242Orch(t)
	ctx := context.Background()

	// Seed a decision that would be injected if pii_detect enriched.
	v240SaveMemory(t, orch, "alice", "decision",
		"PII redaction policy: mask emails + phones + IPs in all artifacts.", "claude-sonnet-4", "semantic")

	mock := &orchestration.MockLLMClient{
		Name_:       "mock_v242_pii",
		VerdictJSON: `{"pii_found":false,"items":[]}`,
		Confidence:  0.95,
		Model:       "mock-v242",
	}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))

	rawContent := "Contact us at support@example.com."
	out, err := orch.Judge(ctx, orchestration.JudgeInput{
		EvalType: "pii_detect",
		Content:  rawContent,
		AgentID:  "claude-sonnet-4",
	})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if out.EvaluationID == 0 {
		t.Fatalf("EvaluationID should be > 0")
	}
	enriched := mock.LastReq.Content

	// pii_detect must NOT enrich.
	if enriched != rawContent {
		t.Errorf("pii_detect must not enrich; LLM saw enriched content. Got:\n%s", truncateForTest(enriched, 200))
	}
	if strings.Contains(enriched, "PII redaction policy") {
		t.Errorf("pii_detect must not enrich; LLM saw the decision. Got:\n%s", truncateForTest(enriched, 400))
	}
}

// TestV242_Consensus_PassesAgentIDToAllSamples seeds brand decisions
// and calls JudgeConsensus(brand_match) with N=3. Verifies that all
// 3 LLM calls see the same enrichment (AgentID forwarded to each
// sample).
func TestV242_Consensus_PassesAgentIDToAllSamples(t *testing.T) {
	orch, st := v242Orch(t)
	ctx := context.Background()

	// Seed + pin a brand decision by claude-sonnet-4.
	id := v240SaveMemory(t, orch, "alice", "decision",
		"Brand voice: bold and warm. Never use corporate jargon.", "claude-sonnet-4", "semantic")
	v242Pin(t, st, id)

	mock := &orchestration.MockLLMClient{
		Name_:       "mock_v242_consensus",
		VerdictJSON: `{"verdict":"match","score":0.88,"issues":[]}`,
		Confidence:  0.88,
		Model:       "mock-v242",
	}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))

	rawContent := "Our copy: bold and warm. No jargon."
	out, err := orch.JudgeConsensus(ctx, orchestration.JudgeConsensusInput{
		EvalType: "brand_match",
		Content:  rawContent,
		AgentID:  "claude-sonnet-4",
		N:        3,
	})
	if err != nil {
		t.Fatalf("JudgeConsensus: %v", err)
	}
	if out.EvaluationID == 0 {
		t.Fatalf("EvaluationID should be > 0")
	}
	// N=3 samples means 3 Judge calls + 1 consensus SaveSDDEvaluation.
	if mock.Calls != 3 {
		t.Errorf("LLM should have been called 3 times (N=3), got %d", mock.Calls)
	}
	// Each of the 3 samples has its own LastReq — verify that the
	// LAST one (which is what LastReq captures) saw the enrichment.
	// (MockLLMClient only retains the most recent request, so we
	// verify the last sample was enriched. All N samples should
	// share the same enrichment since AgentID is forwarded.)
	enriched := mock.LastReq.Content
	if !strings.Contains(enriched, "Brand voice: bold and warm") {
		t.Errorf("last sample LLM did not see the brand decision (AgentID not forwarded?); got:\n%s",
			truncateForTest(enriched, 400))
	}
	if !strings.Contains(enriched, rawContent) {
		t.Errorf("raw content missing from enriched prompt")
	}
}

// TestV242_Consensus_PassesNoEnrichToAllSamples verifies that
// JudgeConsensus forwards NoEnrich to all N Judge samples. The
// drift_judge evaluation 420 flagged this as a potential
// inconsistency (consensus path missed NoEnrich). Test seeds a
// pinned brand decision, calls JudgeConsensus(brand_match) with
// NoEnrich=true + N=3, and verifies the LLM saw the raw content
// (no enrichment block). Mirrors TestV242_BrandMatch_NoEnrich_
// RespectsOptOut at the consensus level.
func TestV242_Consensus_PassesNoEnrichToAllSamples(t *testing.T) {
	orch, st := v242Orch(t)
	ctx := context.Background()

	// Seed + pin a brand decision that WOULD be enriched if NoEnrich=false.
	id := v240SaveMemory(t, orch, "alice", "decision",
		"Brand voice: bold and warm. Never use corporate jargon.", "claude-sonnet-4", "semantic")
	v242Pin(t, st, id)

	mock := &orchestration.MockLLMClient{
		Name_:       "mock_v242_consensus_no_enrich",
		VerdictJSON: `{"verdict":"match","score":0.85,"issues":[]}`,
		Confidence:  0.85,
		Model:       "mock-v242",
	}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))

	rawContent := "Our copy: bold and warm. No jargon."
	out, err := orch.JudgeConsensus(ctx, orchestration.JudgeConsensusInput{
		EvalType: "brand_match",
		Content:  rawContent,
		AgentID:  "claude-sonnet-4",
		N:        3,
		NoEnrich: true, // v2.4.2: opt-out forwarded to all 3 samples
	})
	if err != nil {
		t.Fatalf("JudgeConsensus: %v", err)
	}
	if out.EvaluationID == 0 {
		t.Fatalf("EvaluationID should be > 0")
	}
	if mock.Calls != 3 {
		t.Errorf("LLM should have been called 3 times (N=3), got %d", mock.Calls)
	}
	// The LAST sample's content must be EXACTLY the raw content
	// (NoEnrich=true forwarded to Judge.NoEnrich, which skips
	// enrichment entirely).
	if mock.LastReq.Content != rawContent {
		t.Errorf("NoEnrich=true: consensus sample saw enriched content; want raw.\nGot: %s\nWant: %s",
			truncateForTest(mock.LastReq.Content, 200), rawContent)
	}
	if strings.Contains(mock.LastReq.Content, "Brand voice") {
		t.Errorf("NoEnrich=true: consensus sample saw the brand decision; got:\n%s",
			truncateForTest(mock.LastReq.Content, 400))
	}
}

// TestV242_AgentID_PriorityChain_ResolvesInJudge verifies that Judge
// resolves AgentID via the v2.4.1 priority chain: caller input >
// projects.default_agent_id > "". Same resolveActiveAgentID helper
// used by SessionStart and PublishVibe.
func TestV242_AgentID_PriorityChain_ResolvesInJudge(t *testing.T) {
	orch, st := v242Orch(t)
	ctx := context.Background()

	// Re-create project with DefaultAgentID set.
	if err := st.CreateProject(ctx, &project.Project{
		ProjectID:      "acme",
		DisplayName:    "ACME",
		DefaultAgentID: "gpt-4o",
	}); err != nil {
		t.Fatalf("create project with default_agent_id: %v", err)
	}
	if err := st.SetActiveProject(ctx, "acme"); err != nil {
		t.Fatalf("set active project: %v", err)
	}

	// Seed + pin a decision by gpt-4o (project default) + a decision by
	// claude-sonnet-4 (will be caller override).
	idGPT := v240SaveMemory(t, orch, "alice", "decision",
		"gpt-4o decision under project default", "gpt-4o", "semantic")
	idClaude := v240SaveMemory(t, orch, "alice", "decision",
		"claude-sonnet-4 decision under caller override", "claude-sonnet-4", "semantic")
	v242Pin(t, st, idGPT)
	v242Pin(t, st, idClaude)

	mock := &orchestration.MockLLMClient{
		Name_:       "mock_v242_priority",
		VerdictJSON: `{"verdict":"match","score":0.9,"issues":[]}`,
		Confidence:  0.9,
		Model:       "mock-v242",
	}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))

	// Caller-supplied AgentID = claude-sonnet-4 should WIN over
	// projects.default_agent_id = gpt-4o. The LLM should see
	// claude's decision but NOT gpt's.
	_, err := orch.Judge(ctx, orchestration.JudgeInput{
		EvalType: "brand_match",
		Content:  "test content",
		AgentID:  "claude-sonnet-4",
	})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	enriched := mock.LastReq.Content
	if !strings.Contains(enriched, "claude-sonnet-4 decision under caller override") {
		t.Errorf("LLM did not see caller's agent_id decision (priority chain broken?); got:\n%s",
			truncateForTest(enriched, 400))
	}
	if strings.Contains(enriched, "gpt-4o decision under project default") {
		t.Errorf("LLM saw the project-default agent_id; caller override should WIN. Got:\n%s",
			truncateForTest(enriched, 400))
	}

	// Reset LastReq + verify fallback chain: NO caller AgentID →
	// projects.default_agent_id wins.
	mock.LastReq = orchestration.JudgeRequest{}
	_, err = orch.Judge(ctx, orchestration.JudgeInput{
		EvalType: "brand_match",
		Content:  "test content",
		// AgentID intentionally omitted → fallback to projects.default_agent_id
	})
	if err != nil {
		t.Fatalf("Judge (fallback): %v", err)
	}
	enriched = mock.LastReq.Content
	if !strings.Contains(enriched, "gpt-4o decision under project default") {
		t.Errorf("LLM did not see the project-default agent_id decision (fallback chain broken?); got:\n%s",
			truncateForTest(enriched, 400))
	}
	if strings.Contains(enriched, "claude-sonnet-4 decision under caller override") {
		t.Errorf("LLM saw the caller's agent_id (should be absent when no caller input); got:\n%s",
			truncateForTest(enriched, 400))
	}
}

// TestV242_DriftJudge_EnrichmentUnchangedInPublishVibe verifies that
// drift_judge enrichment still lives in PublishVibe (v2.4.0 audit-
// stable) and is NOT moved to Judge in v2.4.2. Direct Judge callers
// of drift_judge must NOT see enrichment.
func TestV242_DriftJudge_EnrichmentUnchangedInPublishVibe(t *testing.T) {
	orch, _ := v242Orch(t)
	ctx := context.Background()

	// Seed a drift_judge-relevant decision (in PublishVibe, this
	// would enrich the drift_judge content).
	v240SaveMemory(t, orch, "alice", "decision",
		"v2.4.0 ships agent_memory RAG into the vibe-loop", "claude-sonnet-4", "semantic")

	mock := &orchestration.MockLLMClient{
		Name_:       "mock_v242_drift",
		VerdictJSON: `{"verdict":"aligned","confidence":0.92,"reasoning":"matches spec"}`,
		Confidence:  0.92,
		Model:       "mock-v242",
	}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))

	// Direct Judge(eval_type=drift_judge) call — MUST NOT enrich
	// (drift_judge enrichment lives in PublishVibe, untouched by v2.4.2).
	rawContent := "an artifact body that drift_judge would normally judge"
	out, err := orch.Judge(ctx, orchestration.JudgeInput{
		EvalType: "drift_judge",
		Content:  rawContent,
		AgentID:  "claude-sonnet-4",
	})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if out.EvaluationID == 0 {
		t.Fatalf("EvaluationID should be > 0")
	}
	enriched := mock.LastReq.Content

	// drift_judge Judge path must NOT enrich (the decision would
	// be injected if it were — proving the move didn't happen).
	if enriched != rawContent {
		t.Errorf("drift_judge Judge must not enrich; LLM saw enriched content. Got:\n%s",
			truncateForTest(enriched, 200))
	}
	if strings.Contains(enriched, "v2.4.0 ships agent_memory RAG") {
		t.Errorf("drift_judge Judge must not enrich; LLM saw the decision. Got:\n%s",
			truncateForTest(enriched, 400))
	}
}

// truncateForTest caps a string for inclusion in test failure messages.
func truncateForTest(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}
