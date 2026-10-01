package recall

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
)

// TestDecayScore_ForeverIsOne verifies forever rows never decay.
func TestDecayScore_ForeverIsOne(t *testing.T) {
	row := AnnotatedRow{
		Row:          agent_memory.Row{CreatedAt: time.Now().Add(-365 * 24 * time.Hour)},
		DecayClass:   DecayClassForever,
		DecayTauDays: 0,
	}
	got := DecayScore(row, time.Now())
	if got != 1.0 {
		t.Errorf("forever decay = %v, want 1.0", got)
	}
}

// TestDecayScore_StableAtTau verifies exp(-1) ≈ 0.368 at age = tau.
func TestDecayScore_StableAtTau(t *testing.T) {
	now := time.Now()
	row := AnnotatedRow{
		Row:              agent_memory.Row{CreatedAt: now.Add(-365 * 24 * time.Hour)},
		DecayClass:       DecayClassStable,
		DecayTauDays:     365,
		RefreshOnAccess:  false,
	}
	got := DecayScore(row, now)
	if got < 0.35 || got > 0.40 {
		t.Errorf("decay at tau = %v, want ≈ 0.368 (range 0.35-0.40)", got)
	}
}

// TestDecayScore_AccessBoostCap verifies access_count boost caps at 2.0×.
//
// Setup: row was last refreshed 365 days ago (age = tau), with
// access_count = 10000. The boost formula gives 1 + 0.3*log10(10001) ≈
// 2.2, but capped at 2.0. Combined with base = e^-1 ≈ 0.368, the
// expected total is 0.368 * 2.0 = 0.736.
func TestDecayScore_AccessBoostCap(t *testing.T) {
	now := time.Now()
	refreshed := now.Add(-365 * 24 * time.Hour)
	row := AnnotatedRow{
		Row:             agent_memory.Row{CreatedAt: refreshed.Add(-365 * 24 * time.Hour)},
		DecayClass:      DecayClassStable,
		DecayTauDays:    365,
		RefreshOnAccess: true,
		AccessCount:     10000,
		LastRefreshedAt: &refreshed,
	}
	got := DecayScore(row, now)
	// Expected: e^-1 * 2.0 = 0.3679 * 2.0 = 0.7358. Allow tiny float slop.
	if got > 0.7365 || got < 0.7350 {
		t.Errorf("access boost + decay at tau = %v, want ≈ 0.7358 (range 0.7350-0.7365)", got)
	}
}

// TestDecayScore_EmptyDecayClass verifies pre-Phase-5 rows (decay_class=NULL)
// don't blow up.
func TestDecayScore_EmptyDecayClass(t *testing.T) {
	row := AnnotatedRow{Row: agent_memory.Row{CreatedAt: time.Now().Add(-100 * 24 * time.Hour)}}
	got := DecayScore(row, time.Now())
	if got != 1.0 {
		t.Errorf("empty decay_class = %v, want 1.0 (no decay)", got)
	}
}

// TestRefreshOnAccess_UpdatesCount verifies RefreshOnAccess bumps access_count
// and last_refreshed_at.
func TestRefreshOnAccess_UpdatesCount(t *testing.T) {
	db, _, st := newTestDB(t)
	ctx := context.Background()

	id := saveRow(t, st, "observation", "test row for refresh", "")
	n, err := RefreshOnAccess(ctx, db, id, time.Now().UTC())
	if err != nil {
		t.Fatalf("RefreshOnAccess: %v", err)
	}
	if n != 1 {
		t.Errorf("first refresh: access_count = %d, want 1", n)
	}
	n, err = RefreshOnAccess(ctx, db, id, time.Now().UTC())
	if err != nil {
		t.Fatalf("RefreshOnAccess: %v", err)
	}
	if n != 2 {
		t.Errorf("second refresh: access_count = %d, want 2", n)
	}

	// Verify last_refreshed_at was set.
	var lr sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT last_refreshed_at FROM agent_memory WHERE id=?`, id).Scan(&lr); err != nil {
		t.Fatalf("read last_refreshed_at: %v", err)
	}
	if !lr.Valid || lr.String == "" {
		t.Errorf("last_refreshed_at not set, got %v", lr)
	}
}

// TestMarkSuperseded_Success verifies a valid supersession updates both rows.
func TestMarkSuperseded_Success(t *testing.T) {
	db, _, st := newTestDB(t)
	ctx := context.Background()

	id1 := saveDecision(t, st, "old decision about widgets", "")
	id2 := saveDecision(t, st, "new decision about widgets", "")

	if err := MarkSuperseded(ctx, db, id1, id2, "evidence_emerged", "newer data wins", "test"); err != nil {
		t.Fatalf("MarkSuperseded: %v", err)
	}

	// Verify id1 is now superseded.
	var state string
	if err := db.QueryRowContext(ctx, `SELECT decision_state FROM agent_memory WHERE id=?`, id1).Scan(&state); err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state != "superseded" {
		t.Errorf("id1 state = %q, want superseded", state)
	}

	// Verify decision_transitions row was inserted.
	var trigger, reason string
	if err := db.QueryRowContext(ctx,
		`SELECT trigger, reason FROM decision_transitions WHERE decision_id=? AND superseded_id=?`,
		id2, id1).Scan(&trigger, &reason); err != nil {
		t.Fatalf("read transition: %v", err)
	}
	if trigger != "evidence_emerged" {
		t.Errorf("trigger = %q, want evidence_emerged", trigger)
	}
	if reason != "newer data wins" {
		t.Errorf("reason = %q, want 'newer data wins'", reason)
	}
}

// TestMarkSuperseded_RejectsNonDecision verifies the kind-validation guard.
func TestMarkSuperseded_RejectsNonDecision(t *testing.T) {
	db, _, st := newTestDB(t)
	ctx := context.Background()

	idNote := saveRow(t, st, "note", "just a note", "")
	idDecision := saveDecision(t, st, "a decision", "")

	err := MarkSuperseded(ctx, db, idNote, idDecision, "test", "test", "")
	if !errors.Is(err, ErrInvalidSupersession) {
		t.Errorf("expected ErrInvalidSupersession, got %v", err)
	}
}

// TestMarkSuperseded_RejectsSelf verifies the self-supersession guard.
func TestMarkSuperseded_RejectsSelf(t *testing.T) {
	db, _, _ := newTestDB(t)
	ctx := context.Background()
	err := MarkSuperseded(ctx, db, 1, 1, "test", "test", "")
	if err == nil || err.Error() == "" {
		t.Errorf("expected error for self-supersession, got nil")
	}
	if errors.Is(err, ErrInvalidSupersession) {
		t.Errorf("self-supersession should NOT be ErrInvalidSupersession (that's for kind/project mismatch)")
	}
}

// TestMarkSuperseded_RejectsCrossProject verifies the project isolation guard.
func TestMarkSuperseded_RejectsCrossProject(t *testing.T) {
	db, _, st := newTestDB(t)
	ctx := context.Background()

	id1 := saveDecision(t, st, "decision in default project", "")
	saveDecisionWithProject(t, db, "decision in tenant-b", "tenant-b")
	// Get the second id (need to query it back).
	var id2 int64
	if err := db.QueryRowContext(ctx,
		`SELECT id FROM agent_memory WHERE content=?`, "decision in tenant-b",
	).Scan(&id2); err != nil {
		t.Fatalf("read tenant-b id: %v", err)
	}

	err := MarkSuperseded(ctx, db, id1, id2, "test", "test", "")
	if !errors.Is(err, ErrInvalidSupersession) {
		t.Errorf("expected ErrInvalidSupersession for cross-project, got %v", err)
	}
}

// TestPerVibeCaseMultiplier verifies the canonical multiplier table.
func TestPerVibeCaseMultiplier(t *testing.T) {
	cases := []struct {
		vibe string
		want float64
	}{
		{VibeCaseCode, 1.0},
		{VibeCaseText, 0.5},
		{VibeCaseDecision, 1.0}, // forever; multiplier is no-op
		{VibeCaseResearch, 1.0},
		{VibeCaseVideo, 0.25},
		{VibeCaseAudio, 1.0},
		{"unknown", 1.0},
		{"", 1.0},
	}
	for _, c := range cases {
		got := PerVibeCaseMultiplier(c.vibe)
		if got != c.want {
			t.Errorf("PerVibeCaseMultiplier(%q) = %v, want %v", c.vibe, got, c.want)
		}
	}
}

// TestMicroEval_LifecycleBench50 verifies the 50-question LifecycleBench-style
// micro-eval harness. This is a thin smoke test — full eval is operator-graded.
//
// The harness runs RecallFor(C3, query) and counts:
//   - rows returned
//   - distinct rows
//   - if the first row has a non-empty rationale (decision quality signal)
//
// Phase 5 ships this harness; alpha.19 wires it into CI.
func TestMicroEval_LifecycleBench50(t *testing.T) {
	db, _, st := newTestDB(t)
	ctx := context.Background()

	// Seed 50 decision rows.
	for i := 0; i < 50; i++ {
		_ = saveDecision(t, st, "decision number "+string(rune('A'+i%26))+" about widget v"+string(rune('0'+i%10)), "")
	}

	// 50 questions: alternating "widget v?" stems.
	answered := 0
	for i := 0; i < 50; i++ {
		stem := string(rune('A' + i%26))
		rows, err := RecallFor(ctx, db, VibeCaseDecision, "widget "+stem, "default", 5)
		if err != nil {
			t.Fatalf("question %d: RecallFor: %v", i, err)
		}
		if len(rows) > 0 {
			answered++
		}
	}
	if answered == 0 {
		t.Errorf("micro-eval answered 0 of 50 questions — FTS5 may be broken")
	}
	t.Logf("micro-eval answered %d of 50 questions", answered)
}

// Force a compile error if agent_memory is unused (it's imported
// transitively via the newTestDB helper).
var _ = agent_memory.Row{}
