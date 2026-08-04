// Package orchestration — error_observatory.go
//
// Wave 5D (spec 757): the Error Observatory instrumentation helper.
// The orchestrator is the trust boundary between the store and the
// harness — it sees every failure mode (session lifecycle, vibe
// publishing, mindset composition, delegation) and is where the
// 15+ silent-discard sites live.
//
// RecordError is the single entry point: it builds an errorobs
// event (classification + sanitization + severity) and persists it
// best-effort. Callers must NEVER treat RecordError's failure as a
// reason to fail the original request — error telemetry is
// diagnostic, not contractual.
package orchestration

import (
	"context"
	"log"

	"github.com/dark-agents/dark-memory-mcp/internal/errorobs"
)

// RecordError captures one error occurrence durably. Best-effort:
// if the store cannot persist (DB wedged, no project), the failure
// is logged and swallowed — the caller's original request must not
// fail because telemetry failed.
//
// toolName is the originating tool/orchestrator name (e.g.
// "session_start", "publish_vibe", "mindset_apply"). sessionID is
// the operational session when known (may be empty). err is the raw
// error; the classifier maps it to (domain, code, severity) and
// Sanitize scrubs the message.
//
// severity overrides the classifier's default when non-empty
// (errorobs.SeverityFatal / SeverityError / SeverityWarn). Use
// Fatal when the failure implies systemic damage, Warn when the
// request completed with reduced fidelity.
func (o *Orchestrator) RecordError(ctx context.Context, toolName, sessionID string, err error, severity errorobs.Severity) {
	if err == nil {
		return
	}
	if o == nil || o.Store == nil {
		log.Printf("dark-mem-mcp: RecordError(%s): no store; dropping err=%v", toolName, err)
		return
	}
	e := errorobs.New(o.Store.ActiveProject(), sessionID, toolName, err)
	if severity != "" {
		e = e.WithSeverity(severity)
	}
	if err := o.Store.SaveErrorEvent(ctx, e); err != nil {
		log.Printf("dark-mem-mcp: RecordError(%s): telemetry write failed: %v", toolName, err)
	}
}
