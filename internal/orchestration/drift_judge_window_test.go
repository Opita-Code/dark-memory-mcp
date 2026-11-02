package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/artifact"
	"github.com/dark-agents/dark-memory-mcp/internal/nli"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/judge/v4judge"
)

// TestDriftJudge_WindowsArtifactToJudgeBudget: an artifact bigger than
// the judge's input budget must be WINDOWED, not rejected.
//
// Before this, the resolver used its own default cap (256 KiB) and the
// judge refused anything above its budget (32 KiB) with
// ErrInputTooLarge — so every artifact over 32 KiB (a real source file,
// a binary, a config) could never be judged: the verdict fell through
// to the NLI chain or degraded to needs_human.
func TestDriftJudge_WindowsArtifactToJudgeBudget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	big := strings.Repeat("A", v4judge.DefaultMaxArtifactBytes*2)
	if err := os.WriteFile(path, []byte(big), 0600); err != nil {
		t.Fatal(err)
	}

	o, _ := newDriftJudgeTestOrchestrator(t)
	prov := &controllableProvider{
		score: nli.Score{Label: nli.LabelEntailment, Confidence: 0.9, ProviderID: "stub"},
	}
	o.WithNLIRouter(prov)

	out, err := o.DriftJudge(context.Background(), DriftJudgeInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "the artifact body should match the spec",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out.Verdict != "aligned" {
		t.Fatalf("verdict: got %q, want aligned", out.Verdict)
	}
	if len(prov.capturedPremise) > v4judge.DefaultMaxArtifactBytes {
		t.Fatalf("premise size: got %d bytes, want <= %d (the judge budget)",
			len(prov.capturedPremise), v4judge.DefaultMaxArtifactBytes)
	}
	if len(prov.capturedPremise) == 0 {
		t.Fatal("expected the judge to receive the windowed body")
	}
	if !out.ArtifactTrunc {
		t.Error("expected the truncation flag to be set: the judge saw a window")
	}
}

// TestDriftJudge_SmallArtifactIsNotTruncated: the window must not
// touch an artifact that already fits.
func TestDriftJudge_SmallArtifactIsNotTruncated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "small.txt")
	if err := os.WriteFile(path, []byte("short body"), 0600); err != nil {
		t.Fatal(err)
	}
	o, _ := newDriftJudgeTestOrchestrator(t)
	o.WithNLIRouter(&controllableProvider{
		score: nli.Score{Label: nli.LabelEntailment, Confidence: 0.9, ProviderID: "stub"},
	})
	out, err := o.DriftJudge(context.Background(), DriftJudgeInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "the artifact body should match the spec",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out.ArtifactTrunc {
		t.Error("a small artifact must not be reported as truncated")
	}
}
