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

// TestDriftConsensus_RowCarriesTheQuestionItAnswered: a stored verdict
// must say WHAT WAS ASKED, not only what was judged.
//
// The `prompt_version` column existed since v29 and was never filled. A row
// said WHAT was judged (artifact_sha256) and WHO judged it (model,
// nli_provider_id), and not WHAT WAS ASKED. That gap is not cosmetic: the
// drift judge answers a question about an artifact, and the same artifact
// with a different question can get a different verdict. A reader that only
// has the artifact digest cannot tell whether a stored verdict still answers
// the question it was asked.
//
// This test fixes the contract: every row the consensus writes (the chunk
// rows and the consensus row) carries the hex SHA-256 of the spec intent.
func TestDriftConsensus_RowCarriesTheQuestionItAnswered(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	artifactPath := filepath.Join(tmp, "pregunta-test.bin")
	artifactBytes := []byte("the artifact body content for the prompt version test")
	if err := os.WriteFile(artifactPath, artifactBytes, 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	orch, st := newDriftJudgeTestOrchestrator(t)
	prov := &chunkLabelProvider{
		labels: []nli.Label{nli.LabelEntailment, nli.LabelEntailment, nli.LabelEntailment},
		provID: "deberta-v3-large-mnli",
	}
	orch.WithNLIRouter(prov)

	const intent = "the artifact should contain the question it was asked"
	in := DriftJudgeConsensusInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: artifactPath},
		SpecIntent:  intent,
		TargetType:  "artifact",
		TargetID:    "drift_judge_consensus:prompt_version_test",
		N:           3,
	}
	if _, err := orch.DriftJudgeConsensus(ctx, in); err != nil {
		t.Fatalf("DriftJudgeConsensus: %v", err)
	}

	rows, err := st.ListSDDEvaluations(ctx, ssd.ListFilters{EvalType: "drift_judge", Limit: 100})
	if err != nil {
		t.Fatalf("ListSDDEvaluations: %v", err)
	}

	expected := sha256.Sum256([]byte(intent))
	var checked int
	for _, r := range rows {
		if r.TargetID != in.TargetID+":consensus" && !isChunkSuffix(r.TargetID, in.TargetID) {
			continue
		}
		checked++
		if r.PromptVersion == "" {
			t.Errorf("row %s: prompt_version is empty: the verdict does not say what was asked", r.TargetID)
			continue
		}
		if r.PromptVersion != hexBytes(expected) {
			t.Errorf("row %s: prompt_version = %q, want %q", r.TargetID, r.PromptVersion, hexBytes(expected))
		}
	}
	if checked == 0 {
		t.Fatalf("no rows found for target_id=%s", in.TargetID)
	}
}

// TestPromptVersionOf_DifferentQuestionsDifferentDigests: two questions
// about the same artifact are two different judgements, and the digest has
// to say so. Without this, the "vigente" rule (which the artifact sha
// already implements) could not be extended to the question.
func TestPromptVersionOf_DifferentQuestionsDifferentDigests(t *testing.T) {
	a := promptVersionOf("la parte uno hace esto")
	b := promptVersionOf("la parte uno hace otra cosa")
	if a == b {
		t.Fatalf("dos preguntas distintas dan el mismo digest: %q", a)
	}
	if len(a) != 64 {
		t.Errorf("digest length = %d, want 64 (hex sha256)", len(a))
	}
	if a != promptVersionOf("la parte uno hace esto") {
		t.Errorf("el digest no es estable: %q", a)
	}
}
