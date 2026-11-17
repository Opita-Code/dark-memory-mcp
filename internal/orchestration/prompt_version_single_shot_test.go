package orchestration

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/artifact"
	"github.com/dark-agents/dark-memory-mcp/internal/nli"
	"github.com/dark-agents/dark-memory-mcp/internal/ssd"
)

// TestDriftSingleShot_RowCarriesTheQuestionItAnswered: the path the repo's
// own judge runner uses (ONE drift_judge per artifact, not the N-shot
// consensus) must record the question too.
//
// WHY THIS TEST EXISTS NEXT TO THE OTHER ONE. The consensus rows and the
// single-shot rows are written by different functions
// (persistDriftChunkRow / persistDriftConsensusRow on one side,
// driftToJudge on the other). Filling one and not the other leaves the
// store saying WHAT was judged for the verdicts that a photo reads, and
// saying WHAT WAS ASKED only for the ones a consensus wrote. The photo of
// this repo reads the single-shot rows, so that is the one that has to
// carry the question.
func TestDriftSingleShot_RowCarriesTheQuestionItAnswered(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	artifactPath := filepath.Join(tmp, "pregunta-single.bin")
	if err := os.WriteFile(artifactPath, []byte("the artifact body for the single shot prompt version test"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	orch, st := newDriftJudgeTestOrchestrator(t)
	prov := &chunkLabelProvider{
		labels: []nli.Label{nli.LabelEntailment},
		provID: "deberta-v3-large-mnli",
	}
	orch.WithNLIRouter(prov)

	const intent = "the artifact carries the body the question asks about"
	in := JudgeInput{
		EvalType:    "drift_judge",
		TargetType:  "artifact",
		TargetID:    "drift_judge_single:prompt_version_test",
		ArtifactRef: &artifact.ArtifactRef{Kind: artifact.KindFile, Path: artifactPath},
		SpecIntent:  intent,
	}
	if _, err := orch.driftToJudge(ctx, in); err != nil {
		t.Fatalf("driftToJudge: %v", err)
	}

	rows, err := st.ListSDDEvaluations(ctx, ssd.ListFilters{EvalType: "drift_judge", Limit: 100})
	if err != nil {
		t.Fatalf("ListSDDEvaluations: %v", err)
	}
	expected := hexBytes(sha256.Sum256([]byte(intent)))
	found := false
	for _, r := range rows {
		if r.TargetID != in.TargetID {
			continue
		}
		found = true
		if r.PromptVersion != expected {
			t.Errorf("row %s: prompt_version = %q, want %q", r.TargetID, r.PromptVersion, expected)
		}
	}
	if !found {
		t.Fatalf("no row found for target_id=%s", in.TargetID)
	}
}

// TestPromptVersionOf_NoQuestionIsNotAnEmptyQuestion: a row written without
// a question keeps prompt_version NULL. NULL means "the question was not
// recorded" — it never means "the question was the empty question".
//
// This matters because the column is what a reader uses to decide whether a
// stored verdict still answers the question it was asked. A digest of the
// empty string would look like a recorded question and would make every
// question-less verdict look like it answered something.
func TestPromptVersionOf_NoQuestionIsNotAnEmptyQuestion(t *testing.T) {
	for _, vacio := range []string{"", "   ", "\t\n"} {
		if got := promptVersionOf(vacio); got != "" {
			t.Errorf("promptVersionOf(%q) = %q, want \"\" (NULL: no question was asked)", vacio, got)
		}
	}
	if promptVersionOf("una pregunta") == "" {
		t.Errorf("promptVersionOf de una pregunta real esta vacia")
	}
}
