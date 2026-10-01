// Package recall - export_test.go: exposes unexported helpers to the
// external test package (recall_test) so pure helpers can be tested
// without dragging the Store-bound frame compositors into scope.
package recall

import (
	"context"

	"github.com/dark-agents/dark-memory-mcp/internal/atomic"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

var (
	ParseTimestamp               = parseTimestamp
	ParseSDDVerdict              = parseSDDVerdict
	ParsePersonaFromConstitution = parsePersonaFromConstitution
	Truncate                     = truncate
)

// Phase 7 Chunk 7.6 — exposes cache.go internals so cache_test.go
// can exercise the frameTTL unknown-kind fallback path and the
// direct persistIdentity / persistCapabilities entry points without
// round-tripping through CachedSource.IdentityFrame. The CachedSource
// methods are receiver-bound, so we wrap them in free functions
// callable from the external test package.
var FrameTTL = frameTTL

func PersistIdentity(c *CachedSource, ctx context.Context, sessionID string, frame *atomic.IdentityFrame) error {
	return c.persistIdentity(ctx, sessionID, frame)
}

func PersistCapabilities(c *CachedSource, ctx context.Context, sessionID string, frame *atomic.CapabilitiesFrame) error {
	return c.persistCapabilities(ctx, sessionID, frame)
}

func AuditWriteContext(c *CachedSource, sessionID string) store.WriteContext {
	return c.auditWriteContext(sessionID)
}

func ApplyCanary(c *CachedSource, dst *bool) {
	c.applyCanary(dst)
}

func RecordCacheErr(c *CachedSource, ctx context.Context, sessionID, toolName string, err error) {
	c.recordCacheErr(ctx, sessionID, toolName, err)
}
