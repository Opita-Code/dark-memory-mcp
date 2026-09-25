package vibe

import "errors"

// Shared errors used across vibe types (artifact, drift, pipeline).
// Spec validation has its own sentinels in spec.go.
var (
	ErrNotFound = errors.New("vibe: not found")
)
