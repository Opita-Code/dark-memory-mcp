// Package orchestration — spec_lookup_adapter.go
//
// v2.20.0 T08 (spec 1276): adapter from store.Store to artifact.SpecLookup.
// The drift_judge pipeline uses artifact.Resolver to resolve spec_id and
// artifact_id kinds; the resolver needs a SpecLookup interface that
// returns the spec text + artifact URL. The Store interface exposes
// GetSpec/GetArtifact (returning *vibeflow.Spec / *vibeflow.Artifact),
// so we adapt here without touching the store interface.
//
// The adapter is goroutine-safe: every method delegates to the Store,
// which is itself goroutine-safe (sync.Mutex on the SQLite driver).
package orchestration

import (
	"context"

	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

// SpecLookupSource is the subset of store.Store required for the
// drift_judge resolver adapter. Defined as an interface so tests
// can inject a mock without spinning up SQLite.
type SpecLookupSource interface {
	GetSpec(ctx context.Context, id int64) (*vibeflow.Spec, error)
	GetArtifact(ctx context.Context, id int64) (*vibeflow.Artifact, error)
}

// StoreSpecLookup adapts SpecLookupSource to artifact.SpecLookup.
// The wrapper has no state; methods are pure delegations.
type StoreSpecLookup struct {
	Source SpecLookupSource
}

// GetSpecText returns the spec_intent string for spec_id ref.
// Empty spec (spec.Spec == "") returns "" with no error — the caller
// (resolver) will treat zero bytes as a resolved-but-empty artifact.
func (l *StoreSpecLookup) GetSpecText(ctx context.Context, id int64) (string, error) {
	s, err := l.Source.GetSpec(ctx, id)
	if err != nil {
		return "", err
	}
	if s == nil {
		return "", nil
	}
	return s.Spec, nil
}

// GetArtifactURL returns the artifact's URL for artifact_id ref.
func (l *StoreSpecLookup) GetArtifactURL(ctx context.Context, id int64) (string, error) {
	a, err := l.Source.GetArtifact(ctx, id)
	if err != nil {
		return "", err
	}
	if a == nil {
		return "", nil
	}
	return a.ArtifactURL, nil
}

// NewStoreSpecLookup wraps a SpecLookupSource as an artifact.SpecLookup.
// Returns nil if src is nil.
func NewStoreSpecLookup(src SpecLookupSource) *StoreSpecLookup {
	if src == nil {
		return nil
	}
	return &StoreSpecLookup{Source: src}
}
