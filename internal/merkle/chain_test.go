package merkle

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// ----- GenesisRoot -----

func TestGenesisRoot_Format(t *testing.T) {
	if len(GenesisRoot) != HashLen {
		t.Errorf("GenesisRoot length = %d, want %d", len(GenesisRoot), HashLen)
	}
	if strings.Trim(GenesisRoot, "0") != "" {
		t.Errorf("GenesisRoot not all zeros: %s", GenesisRoot)
	}
	// Must be valid hex.
	if _, err := decodeHex(GenesisRoot); err != nil {
		t.Errorf("GenesisRoot not valid hex: %v", err)
	}
}

// ----- ComputeRoot basics -----

func TestComputeRoot_Deterministic_SameInput(t *testing.T) {
	in := CanonicalInput{
		ArtifactID:     1,
		SpecID:         2,
		Verdict:        "aligned",
		SpecDiff:       "{}",
		JudgeReasoning: "ok",
		CreatedAt:      "2026-08-19T00:00:00Z",
	}
	r1, err := ComputeRoot(GenesisRoot, in)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := ComputeRoot(GenesisRoot, in)
	if err != nil {
		t.Fatal(err)
	}
	if r1 != r2 {
		t.Errorf("non-deterministic: %s vs %s", r1, r2)
	}
	if len(r1) != HashLen {
		t.Errorf("length = %d, want %d", len(r1), HashLen)
	}
	if _, err := decodeHex(r1); err != nil {
		t.Errorf("root not valid hex: %v", err)
	}
}

func TestComputeRoot_PrevLengthCheck(t *testing.T) {
	in := CanonicalInput{ArtifactID: 1}
	// Too short.
	if _, err := ComputeRoot("deadbeef", in); err == nil {
		t.Error("expected error for short prev")
	}
	// Too long.
	if _, err := ComputeRoot(strings.Repeat("a", HashLen+1), in); err == nil {
		t.Error("expected error for long prev")
	}
	// Empty.
	if _, err := ComputeRoot("", in); err == nil {
		t.Error("expected error for empty prev")
	}
	// Non-hex char.
	bad := strings.Repeat("z", HashLen)
	if _, err := ComputeRoot(bad, in); err == nil {
		t.Error("expected error for non-hex prev")
	}
	// Valid hex (length ok).
	if _, err := ComputeRoot(strings.Repeat("a", HashLen), in); err != nil {
		t.Errorf("valid hex should not error: %v", err)
	}
}

func TestComputeRoot_DifferentVerdicts_DifferentRoots(t *testing.T) {
	r1, _ := ComputeRoot(GenesisRoot, CanonicalInput{Verdict: "aligned"})
	r2, _ := ComputeRoot(GenesisRoot, CanonicalInput{Verdict: "drift_detected"})
	r3, _ := ComputeRoot(GenesisRoot, CanonicalInput{Verdict: "needs_human"})
	if r1 == r2 || r1 == r3 || r2 == r3 {
		t.Errorf("distinct verdicts collided: %s %s %s", r1, r2, r3)
	}
}

func TestComputeRoot_DifferentArtifactIDs_DifferentRoots(t *testing.T) {
	r1, _ := ComputeRoot(GenesisRoot, CanonicalInput{ArtifactID: 1, Verdict: "aligned"})
	r2, _ := ComputeRoot(GenesisRoot, CanonicalInput{ArtifactID: 2, Verdict: "aligned"})
	if r1 == r2 {
		t.Error("different artifact_ids collided")
	}
}

func TestComputeRoot_DifferentPrevs_DifferentRoots(t *testing.T) {
	r1, _ := ComputeRoot(GenesisRoot, CanonicalInput{Verdict: "aligned"})
	r2, _ := ComputeRoot(strings.Repeat("a", HashLen), CanonicalInput{Verdict: "aligned"})
	r3, _ := ComputeRoot(strings.Repeat("b", HashLen), CanonicalInput{Verdict: "aligned"})
	if r1 == r2 || r1 == r3 || r2 == r3 {
		t.Errorf("distinct prevs collided: %s %s %s", r1, r2, r3)
	}
}

func TestComputeRoot_DifferentCreatedAt_DifferentRoots(t *testing.T) {
	r1, _ := ComputeRoot(GenesisRoot, CanonicalInput{CreatedAt: "2026-01-01T00:00:00Z"})
	r2, _ := ComputeRoot(GenesisRoot, CanonicalInput{CreatedAt: "2026-01-01T00:00:01Z"})
	if r1 == r2 {
		t.Error("different created_at collided")
	}
}

func TestComputeRoot_DifferentJudgeReasoning_DifferentRoots(t *testing.T) {
	r1, _ := ComputeRoot(GenesisRoot, CanonicalInput{JudgeReasoning: "ok"})
	r2, _ := ComputeRoot(GenesisRoot, CanonicalInput{JudgeReasoning: "doubt"})
	if r1 == r2 {
		t.Error("different judge_reasoning collided")
	}
}

func TestComputeRoot_SpecIDZero_OmitsFromCanonical(t *testing.T) {
	// SpecID=0 with omitempty → omitted from JSON → same root as
	// SpecID not present at all.
	r1, _ := ComputeRoot(GenesisRoot, CanonicalInput{SpecID: 0, Verdict: "aligned"})
	r2, _ := ComputeRoot(GenesisRoot, CanonicalInput{Verdict: "aligned"})
	if r1 != r2 {
		t.Errorf("SpecID=0 should not affect root: %s vs %s", r1, r2)
	}
}

func TestComputeRoot_SpecDiffEmpty_OmitsFromCanonical(t *testing.T) {
	r1, _ := ComputeRoot(GenesisRoot, CanonicalInput{SpecDiff: "", Verdict: "aligned"})
	r2, _ := ComputeRoot(GenesisRoot, CanonicalInput{Verdict: "aligned"})
	if r1 != r2 {
		t.Errorf("empty SpecDiff should not affect root: %s vs %s", r1, r2)
	}
}

// ----- Canonical JSON shape -----

func TestCanonical_JSON_FieldOrderAndKeys(t *testing.T) {
	in := CanonicalInput{
		ArtifactID:     1,
		SpecID:         2,
		Verdict:        "aligned",
		SpecDiff:       "x",
		JudgeReasoning: "ok",
		CreatedAt:      "t",
	}
	b, _ := json.Marshal(in)
	s := string(b)
	for _, k := range []string{`"a":1`, `"s":2`, `"v":"aligned"`, `"d":"x"`, `"j":"ok"`, `"t":"t"`} {
		if !strings.Contains(s, k) {
			t.Errorf("missing key %s in %s", k, s)
		}
	}
}

func TestCanonical_JSON_OmitsZeroSpecID(t *testing.T) {
	in := CanonicalInput{Verdict: "aligned"}
	b, _ := json.Marshal(in)
	if strings.Contains(string(b), `"s":`) {
		t.Errorf("SpecID=0 should be omitted, got %s", b)
	}
}

// ----- VerifyChain -----

func TestVerifyChain_NilRows(t *testing.T) {
	res := VerifyChain(nil)
	if !res.OK {
		t.Errorf("empty chain should pass, got %+v", res)
	}
	if res.RowsChecked != 0 {
		t.Errorf("RowsChecked = %d, want 0", res.RowsChecked)
	}
	// Empty chain head is the GenesisRoot sentinel (the implicit
	// prev_root for any first row that gets appended later).
	if res.ChainHead != GenesisRoot {
		t.Errorf("ChainHead = %s, want %s", res.ChainHead, GenesisRoot)
	}
}

func TestVerifyChain_EmptyRows(t *testing.T) {
	res := VerifyChain([]Report{})
	if !res.OK {
		t.Errorf("empty chain should pass, got %+v", res)
	}
}

func TestVerifyChain_SingleRow(t *testing.T) {
	in := CanonicalInput{ArtifactID: 1, Verdict: "aligned", CreatedAt: "t"}
	root, _ := ComputeRoot(GenesisRoot, in)
	res := VerifyChain([]Report{{ID: 1, Canonical: in, MerkleRoot: root}})
	if !res.OK {
		t.Errorf("single valid row should pass, got %+v", res)
	}
	if res.RowsChecked != 1 {
		t.Errorf("RowsChecked = %d, want 1", res.RowsChecked)
	}
	if res.ChainHead != root {
		t.Errorf("ChainHead = %s, want %s", res.ChainHead, root)
	}
}

func TestVerifyChain_TwoRows_Linked(t *testing.T) {
	in1 := CanonicalInput{ArtifactID: 1, Verdict: "aligned", CreatedAt: "t1"}
	in2 := CanonicalInput{ArtifactID: 2, Verdict: "drift_detected", CreatedAt: "t2"}
	r1, _ := ComputeRoot(GenesisRoot, in1)
	r2, _ := ComputeRoot(r1, in2)
	res := VerifyChain([]Report{
		{ID: 1, Canonical: in1, MerkleRoot: r1},
		{ID: 2, Canonical: in2, MerkleRoot: r2},
	})
	if !res.OK {
		t.Errorf("valid 2-row chain should pass, got %+v", res)
	}
	if res.RowsChecked != 2 {
		t.Errorf("RowsChecked = %d, want 2", res.RowsChecked)
	}
	if res.ChainHead != r2 {
		t.Errorf("ChainHead = %s, want %s", res.ChainHead, r2)
	}
}

func TestVerifyChain_TamperedVerdict_Detected(t *testing.T) {
	in1 := CanonicalInput{ArtifactID: 1, Verdict: "aligned", CreatedAt: "t1"}
	in2 := CanonicalInput{ArtifactID: 2, Verdict: "drift_detected", CreatedAt: "t2"}
	r1, _ := ComputeRoot(GenesisRoot, in1)
	r2_real, _ := ComputeRoot(r1, in2)
	// Tamper: change verdict in canonical but keep stored merkle_root.
	in2_tampered := in2
	in2_tampered.Verdict = "aligned"
	res := VerifyChain([]Report{
		{ID: 1, Canonical: in1, MerkleRoot: r1},
		{ID: 2, Canonical: in2_tampered, MerkleRoot: r2_real},
	})
	if res.OK {
		t.Error("tampered verdict should fail verification")
	}
	if res.FirstBadID != 2 {
		t.Errorf("FirstBadID = %d, want 2", res.FirstBadID)
	}
	if !strings.Contains(res.FirstBadWhy, "expected") {
		t.Errorf("FirstBadWhy = %q, want 'expected ...'", res.FirstBadWhy)
	}
}

func TestVerifyChain_TamperedStoredRoot_Detected(t *testing.T) {
	in := CanonicalInput{ArtifactID: 1, Verdict: "aligned", CreatedAt: "t"}
	root, _ := ComputeRoot(GenesisRoot, in)
	// Stored root has trailing junk.
	res := VerifyChain([]Report{{ID: 1, Canonical: in, MerkleRoot: root + "ff"}})
	if res.OK {
		t.Error("tampered stored root should fail")
	}
	if res.FirstBadID != 1 {
		t.Errorf("FirstBadID = %d, want 1", res.FirstBadID)
	}
}

func TestVerifyChain_InsertMiddle_Detected(t *testing.T) {
	in1 := CanonicalInput{ArtifactID: 1, Verdict: "aligned", CreatedAt: "t1"}
	in2 := CanonicalInput{ArtifactID: 2, Verdict: "drift_detected", CreatedAt: "t2"}
	r1, _ := ComputeRoot(GenesisRoot, in1)
	r2, _ := ComputeRoot(r1, in2)
	// Pretend a fake row with id=2 was inserted before id=3 (which is
	// the real row 2). Verifier walks row1, fake, real_row2. Row3's
	// stored merkle_root (r2) was computed with prev=r1, but verifier
	// recomputes with prev=fake_root → mismatch.
	in_fake := CanonicalInput{ArtifactID: 99, Verdict: "aligned", CreatedAt: "t-fake"}
	r_fake, _ := ComputeRoot(r1, in_fake)
	res := VerifyChain([]Report{
		{ID: 1, Canonical: in1, MerkleRoot: r1},
		{ID: 2, Canonical: in_fake, MerkleRoot: r_fake},
		{ID: 3, Canonical: in2, MerkleRoot: r2},
	})
	if res.OK {
		t.Error("insertion in middle should fail")
	}
	if res.FirstBadID != 3 {
		t.Errorf("FirstBadID = %d, want 3", res.FirstBadID)
	}
}

func TestVerifyChain_DeleteMiddle_Detected(t *testing.T) {
	in1 := CanonicalInput{ArtifactID: 1, Verdict: "aligned", CreatedAt: "t1"}
	in2 := CanonicalInput{ArtifactID: 2, Verdict: "drift_detected", CreatedAt: "t2"}
	in3 := CanonicalInput{ArtifactID: 3, Verdict: "aligned", CreatedAt: "t3"}
	r1, _ := ComputeRoot(GenesisRoot, in1)
	r2, _ := ComputeRoot(r1, in2)
	r3, _ := ComputeRoot(r2, in3)
	// Delete row 2: verifier walks row1, row3. Row3's stored root was
	// computed with prev = r2, but verifier recomputes with prev = r1.
	// Hash collision (different inputs → different outputs) → mismatch.
	res := VerifyChain([]Report{
		{ID: 1, Canonical: in1, MerkleRoot: r1},
		{ID: 3, Canonical: in3, MerkleRoot: r3},
	})
	if res.OK {
		t.Error("deletion in middle should fail")
	}
	if res.FirstBadID != 3 {
		t.Errorf("FirstBadID = %d, want 3", res.FirstBadID)
	}
	if !strings.Contains(res.FirstBadWhy, "expected") {
		t.Errorf("FirstBadWhy = %q, want 'expected ...'", res.FirstBadWhy)
	}
}

func TestVerifyChain_LegacyRowsAtStart_Skipped(t *testing.T) {
	in := CanonicalInput{ArtifactID: 1, Verdict: "aligned", CreatedAt: "t"}
	root, _ := ComputeRoot(GenesisRoot, in)
	res := VerifyChain([]Report{
		{ID: 1, Canonical: CanonicalInput{}, MerkleRoot: ""},
		{ID: 2, Canonical: CanonicalInput{}, MerkleRoot: ""},
		{ID: 3, Canonical: in, MerkleRoot: root},
	})
	if !res.OK {
		t.Errorf("leading legacy rows should be skipped, got %+v", res)
	}
	if res.RowsSkipped != 2 {
		t.Errorf("RowsSkipped = %d, want 2", res.RowsSkipped)
	}
	if res.RowsChecked != 1 {
		t.Errorf("RowsChecked = %d, want 1", res.RowsChecked)
	}
	if res.ChainHead != root {
		t.Errorf("ChainHead = %s, want %s", res.ChainHead, root)
	}
}

func TestVerifyChain_GapAfterChain_Detected(t *testing.T) {
	in := CanonicalInput{ArtifactID: 1, Verdict: "aligned", CreatedAt: "t"}
	root, _ := ComputeRoot(GenesisRoot, in)
	res := VerifyChain([]Report{
		{ID: 1, Canonical: in, MerkleRoot: root},
		{ID: 2, Canonical: CanonicalInput{}, MerkleRoot: ""}, // gap
		{ID: 3, Canonical: CanonicalInput{ArtifactID: 3, Verdict: "aligned", CreatedAt: "t3"}, MerkleRoot: root + "aa"},
	})
	if res.OK {
		t.Error("gap should fail")
	}
	if res.FirstBadID != 2 {
		t.Errorf("FirstBadID = %d, want 2", res.FirstBadID)
	}
	if !strings.Contains(res.FirstBadWhy, "chain gap") {
		t.Errorf("FirstBadWhy = %q, want 'chain gap'", res.FirstBadWhy)
	}
}

// ----- Tampering on every chain position -----

func TestVerifyChain_TamperingAtEveryPosition(t *testing.T) {
	// Build a chain of 5 rows, then mutate each in turn and verify
	// detection.
	rows := make([]Report, 5)
	prev := GenesisRoot
	for i := 0; i < 5; i++ {
		in := CanonicalInput{
			ArtifactID: int64(i + 1),
			Verdict:    "aligned",
			CreatedAt:  "t",
		}
		r, _ := ComputeRoot(prev, in)
		rows[i] = Report{ID: int64(i + 1), Canonical: in, MerkleRoot: r}
		prev = r
	}
	// Sanity: chain verifies as-is.
	if !VerifyChain(rows).OK {
		t.Fatal("baseline chain failed")
	}
	// Tamper each position; verify each is detected at THAT id.
	for pos := 0; pos < 5; pos++ {
		tampered := make([]Report, len(rows))
		copy(tampered, rows)
		tampered[pos].Canonical.Verdict = "drift_detected"
		res := VerifyChain(tampered)
		if res.OK {
			t.Errorf("tamper at position %d should fail", pos)
			continue
		}
		if res.FirstBadID != int64(pos+1) {
			t.Errorf("tamper at position %d: FirstBadID = %d, want %d", pos, res.FirstBadID, pos+1)
		}
	}
}

// ----- Sequential appends -----

func TestSequentialAppends_AllVerify(t *testing.T) {
	rows := []Report{}
	prev := GenesisRoot
	for i := 0; i < 50; i++ {
		in := CanonicalInput{
			ArtifactID: int64(i + 1),
			Verdict:    "aligned",
			CreatedAt:  "2026-08-19T00:00:00Z",
		}
		root, _ := ComputeRoot(prev, in)
		rows = append(rows, Report{ID: int64(i + 1), Canonical: in, MerkleRoot: root})
		prev = root
	}
	res := VerifyChain(rows)
	if !res.OK {
		t.Errorf("50 sequential appends should verify, got %+v", res)
	}
	if res.RowsChecked != 50 {
		t.Errorf("RowsChecked = %d, want 50", res.RowsChecked)
	}
}

// ----- Concurrent ComputeRoot -----

func TestComputeRoot_Concurrent_Deterministic(t *testing.T) {
	in := CanonicalInput{ArtifactID: 1, Verdict: "aligned", CreatedAt: "t"}
	const n = 100
	var wg sync.WaitGroup
	roots := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := ComputeRoot(GenesisRoot, in)
			if err != nil {
				t.Errorf("goroutine %d: %v", i, err)
				return
			}
			roots[i] = r
		}(i)
	}
	wg.Wait()
	for i := 1; i < n; i++ {
		if roots[i] != roots[0] {
			t.Errorf("goroutine %d root differs: %s vs %s", i, roots[i], roots[0])
		}
	}
}

// ----- helpers -----

func decodeHex(s string) ([]byte, error) {
	return hex.DecodeString(s)
}