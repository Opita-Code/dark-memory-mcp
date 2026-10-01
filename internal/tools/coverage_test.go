// Package tools — coverage_test.go: closes the Phase 6 Chunk 6.4
// mutation gap for pure-function files in internal/tools.
//
// Targets:
//   - errors.ToToolError (7 sentinels + cross-project special + nil + unknown)
//   - errors.classifyUnknown
//   - registry helpers (CanonicalOrder + canonical order invariants)
package tools

import (
	"errors"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// === ToToolError ======================================================

func TestToToolError_Nil(t *testing.T) {
	if te := ToToolError(nil); te != nil {
		t.Errorf("ToToolError(nil) = %+v; want nil", te)
	}
}

func TestToToolError_SessionRequired(t *testing.T) {
	te := ToToolError(store.ErrSessionRequired)
	if te == nil || te.Code != "ErrSessionRequired" {
		t.Errorf("Code = %v; want ErrSessionRequired", te)
	}
	if te.Message == "" || te.Hint == "" {
		t.Errorf("Message/Hint empty: %+v", te)
	}
}

func TestToToolError_InvalidArgument_NoField(t *testing.T) {
	te := ToToolError(store.ErrInvalidArgument)
	if te == nil || te.Code != "ErrInvalidArgument" {
		t.Errorf("Code = %v; want ErrInvalidArgument", te)
	}
	if te.Field != "" {
		t.Errorf("Field = %q; want empty for bare sentinel", te.Field)
	}
}

func TestToToolError_InvalidArgument_WithField(t *testing.T) {
	wrapped := &store.FieldError{
		Field: "operator",
		Store: store.ErrInvalidArgument,
	}
	te := ToToolError(wrapped)
	if te == nil || te.Code != "ErrInvalidArgument" {
		t.Errorf("Code = %v; want ErrInvalidArgument", te)
	}
	if te.Field != "operator" {
		t.Errorf("Field = %q; want operator", te.Field)
	}
	if !strings.Contains(te.Message, "operator") {
		t.Errorf("Message missing field name: %q", te.Message)
	}
}

func TestToToolError_NotFound(t *testing.T) {
	te := ToToolError(store.ErrNotFound)
	if te == nil || te.Code != "ErrNotFound" {
		t.Errorf("Code = %v; want ErrNotFound", te)
	}
}

func TestToToolError_AlreadyExists(t *testing.T) {
	te := ToToolError(store.ErrAlreadyExists)
	if te == nil || te.Code != "ErrAlreadyExists" {
		t.Errorf("Code = %v; want ErrAlreadyExists", te)
	}
}

func TestToToolError_CanaryInPayload(t *testing.T) {
	te := ToToolError(store.ErrCanaryInPayload)
	if te == nil || te.Code != "ErrCanaryInPayload" {
		t.Errorf("Code = %v; want ErrCanaryInPayload", te)
	}
	if !strings.Contains(te.Hint, "canary") {
		t.Errorf("Hint missing canary mention: %q", te.Hint)
	}
}

func TestToToolError_InvalidState(t *testing.T) {
	te := ToToolError(store.ErrInvalidState)
	if te == nil || te.Code != "ErrInvalidState" {
		t.Errorf("Code = %v; want ErrInvalidState", te)
	}
}

func TestToToolError_ConstitutionDrift(t *testing.T) {
	te := ToToolError(store.ErrConstitutionDrift)
	if te == nil || te.Code != "ErrConstitutionDrift" {
		t.Errorf("Code = %v; want ErrConstitutionDrift", te)
	}
}

func TestToToolError_CrossProject_NoDetails(t *testing.T) {
	te := ToToolError(store.ErrCrossProjectAccess)
	if te == nil || te.Code != "ErrCrossProjectAccess" {
		t.Errorf("Code = %v; want ErrCrossProjectAccess", te)
	}
	if te.Field != "" {
		t.Errorf("Field = %q; want empty for bare sentinel", te.Field)
	}
}

func TestToToolError_CrossProject_WithDetails(t *testing.T) {
	cpe := &store.CrossProjectAccessError{
		RowID:            42,
		RowProject:       "project-A",
		RequestedProject: "project-B",
	}
	wrapped := errors.Join(store.ErrCrossProjectAccess, cpe)
	te := ToToolError(wrapped)
	if te == nil || te.Code != "ErrCrossProjectAccess" {
		t.Errorf("Code = %v; want ErrCrossProjectAccess", te)
	}
	if te.Field != "id" {
		t.Errorf("Field = %q; want id", te.Field)
	}
	if !strings.Contains(te.Message, "42") {
		t.Errorf("Message missing RowID: %q", te.Message)
	}
}

func TestToToolError_UnknownError(t *testing.T) {
	te := ToToolError(errors.New("something else entirely"))
	if te == nil || te.Code != ErrInternal {
		t.Errorf("Code = %v; want %s", te.Code, ErrInternal)
	}
}

func TestToToolError_WrappedSentinel(t *testing.T) {
	wrapped := errors.Join(store.ErrNotFound, errors.New("wrapped context"))
	te := ToToolError(wrapped)
	if te == nil || te.Code != "ErrNotFound" {
		t.Errorf("Code = %v; want ErrNotFound (errors.Is traversal)", te)
	}
}

// === classifyUnknown ===================================================

func TestClassifyUnknown_Stable(t *testing.T) {
	if classifyUnknown(errors.New("foo")) != ErrInternal {
		t.Errorf("classifyUnknown should always return ErrInternal")
	}
}

// === CanonicalOrder ====================================================

func TestCanonicalOrder_StableAndNonEmpty(t *testing.T) {
	first := CanonicalOrder()
	if len(first) == 0 {
		t.Fatal("CanonicalOrder empty")
	}
	second := CanonicalOrder()
	if len(first) != len(second) {
		t.Errorf("CanonicalOrder length differs: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("CanonicalOrder[%d] differs: %q vs %q", i, first[i], second[i])
		}
	}
}

func TestCanonicalOrder_NoBarePrefix_Duplicates(t *testing.T) {
	seen := make(map[string]int)
	for _, n := range CanonicalOrder() {
		seen[n]++
	}
	for name, count := range seen {
		if count > 1 {
			t.Errorf("CanonicalOrder has %q listed %d times", name, count)
		}
	}
}