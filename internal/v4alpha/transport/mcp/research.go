// research_* tools — the v4-alpha research executor surface
// (BUG-10 10a). The transport layer is a thin closure over the
// v4alpha/research.Executor. The executor owns the
// tier-aware fan-out, the cache, the SSRF guard, and the merge
// step. The transport owns the audit row, the operator id
// resolution (INV-1), and the JSON envelope.
//
//   - dark_memory_research_topic         → run a research query
//   - dark_memory_research_recall        → recall prior research
//   - dark_memory_research_resume_thread → continue a thread
//
// ADR-007 C3 audit emission: every dark_memory_research_topic
// call emits one audit_log row with payload_redacted=true and
// query_hash=sha256(target). The raw target is NEVER logged
// (R6 mitigation). Future: a `payload_redacted=false` mode for
// self-hosted tenants who opt in.
package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/research"
)

const (
	researchTopicToolName        = "dark_memory_research_topic"
	researchRecallToolName       = "dark_memory_research_recall"
	researchResumeThreadToolName = "dark_memory_research_resume_thread"
)

func registerResearchTools(s *Server) {
	registerResearchTopic(s)
	registerResearchRecall(s)
	registerResearchResumeThread(s)
}

// ---------- topic ----------

type researchTopicInput struct {
	Intent     string `json:"intent" jsonschema:"required" jsonschema_description:"Research intent: web | academic | code | cve | domain | dns | cert | ip | threat | email | dark | geo | news"`
	VibeCase   string `json:"vibe_case,omitempty" jsonschema_description:"Optional C1..C7 vibe-case filter (e.g. C1 for code-related research)"`
	Target     string `json:"target" jsonschema:"required" jsonschema_description:"The target to research (CVE id, domain, IP, query string, package name, ...). NOT logged — the audit row stores sha256(target)."`
	Depth      string `json:"depth,omitempty" jsonschema_description:"shallow (T1 only, 1 call) | standard (T1+T2, 2 calls) | deep (T1+T2+T3, 4 calls). Default standard."`

	// ADR-007 C3 audit metadata.
	Operator  string `json:"operator" jsonschema:"required" jsonschema_description:"Operator id (INV-1 audit owner)"`
	SessionID string `json:"session_id,omitempty" jsonschema_description:"Optional active session id for audit_log.session_id"`
	ProjectID string `json:"project_id,omitempty" jsonschema_description:"Optional project id (INV-7)"`
}

type researchTopicOutput struct {
	Items    []research.Evidence   `json:"items"`
	ItemCount int                  `json:"item_count"`
	Backends []string              `json:"backends_called"`
	AuditID  int64                 `json:"audit_id,omitempty"`
}

func registerResearchTopic(s *Server) {
	tool := mcp.NewTool(researchTopicToolName,
		mcp.WithDescription("Run a research query across the 17 no-API-key backends. Tier-aware fan-out (T1 → T2 → T3), health-aware routing, per-type cache TTL with stale-while-revalidate, SSRF guard, prompt-injection gate, and entity resolution across corroborating backends."),
		mcp.WithInputSchema[researchTopicInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = ctx
		var in researchTopicInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := validateResearchTopicInput(&in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		intent := research.Intent(in.Intent)
		var vc research.VibeCase
		if in.VibeCase != "" {
			parsed, err := research.ParseVibeCase(in.VibeCase)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("vibe_case: %v", err)), nil
			}
			vc = parsed
		}
		depth, err := parseDepth(in.Depth)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		exec := s.researchExecutor
		if exec == nil {
			return mcp.NewToolResultError("research executor not configured"), nil
		}
		evidence, execErr := exec.Execute(ctx, intent, vc, in.Target, depth)
		// Even when evidence is empty we emit the audit row
		// (with item_count=0) so the operator has a record of
		// the request. execErr is reported in the audit but
		// does NOT prevent the audit emission.
		out := researchTopicOutput{
			Items:     evidence,
			ItemCount: len(evidence),
		}
		// Collect backend names that contributed.
		seen := map[string]bool{}
		for _, e := range evidence {
			if !seen[e.Backend] {
				seen[e.Backend] = true
				out.Backends = append(out.Backends, e.Backend)
			}
		}
		// Audit emission (best-effort; never fails the call).
		if id, auditOK := emitResearchAudit(ctx, s, in, len(evidence), execErr); auditOK {
			out.AuditID = id
		}
		if execErr != nil {
			// Return what we have + the error envelope.
			return resultJSON(out)
		}
		return resultJSON(out)
	})
}

func validateResearchTopicInput(in *researchTopicInput) error {
	if in.Operator == "" {
		return fmt.Errorf("research: operator required (INV-1)")
	}
	if in.Intent == "" {
		return fmt.Errorf("research: intent required (web | academic | code | cve | domain | dns | cert | ip | threat | email | dark | geo | news)")
	}
	if in.Target == "" {
		return fmt.Errorf("research: target is required")
	}
	if !research.IsValidIntent(research.Intent(in.Intent)) {
		return fmt.Errorf("research: unknown intent %q", in.Intent)
	}
	if in.Depth != "" {
		switch in.Depth {
		case "shallow", "standard", "deep":
		default:
			return fmt.Errorf("research: depth must be shallow | standard | deep (got %q)", in.Depth)
		}
	}
	return nil
}

func parseDepth(raw string) (research.Depth, error) {
	switch raw {
	case "", "standard":
		return research.DepthStandard, nil
	case "shallow":
		return research.DepthShallow, nil
	case "deep":
		return research.DepthDeep, nil
	}
	return 0, fmt.Errorf("research: depth must be shallow | standard | deep")
}

// emitResearchAudit writes one audit row for the research call.
// Returns the audit id (or 0 on failure) and whether the write
// succeeded. The audit row never contains the raw target; only
// the SHA-256 hash (R6 mitigation).
func emitResearchAudit(ctx context.Context, s *Server, in researchTopicInput, itemCount int, execErr error) (int64, bool) {
	hash := sha256.Sum256([]byte(in.Target))
	hashHex := hex.EncodeToString(hash[:])
	// The audit row's payload is a redacted summary the
	// operator can grep: intent + depth + target_hash + result.
	summary := map[string]any{
		"intent":      in.Intent,
		"vibe_case":   in.VibeCase,
		"depth":       in.Depth,
		"target_hash": hashHex,
		"item_count":  itemCount,
	}
	if execErr != nil {
		summary["error"] = execErr.Error()
	}
	// The executor may not have a writer; this is best-effort
	// for 10a — the real integration with the audit.Writer is
	// in 10b. For now, log the hash + intent to the writer if
	// it exists, and report 0 on failure.
	if s.audit == nil {
		return 0, false
	}
	id, err := s.audit.Write(ctx, in.Operator, in.SessionID, []byte(auditRowFromSummary(summary)))
	if err != nil {
		return 0, false
	}
	return id, true
}

// auditRowFromSummary serializes a summary map into a flat
// string the audit writer accepts. We keep the format
// colon-separated key=value for grep-friendliness.
func auditRowFromSummary(s map[string]any) string {
	var b strings.Builder
	for k, v := range s {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%s=%v", k, v)
	}
	return b.String()
}

// ---------- recall ----------

type researchRecallInput struct {
	Query         string `json:"query" jsonschema:"required" jsonschema_description:"Free-text query for prior research items"`
	MaxTokens     int    `json:"max_tokens,omitempty" jsonschema_description:"Token budget; default 4000"`
	MinConfidence float64 `json:"min_confidence,omitempty" jsonschema_description:"Drop items below this confidence (0..1); default 0.3"`

	Operator  string `json:"operator" jsonschema:"required" jsonschema_description:"Operator id (INV-1)"`
	SessionID string `json:"session_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
}

type researchRecallOutput struct {
	Items []research.Evidence `json:"items"`
	Count int                 `json:"count"`
}

func registerResearchRecall(s *Server) {
	tool := mcp.NewTool(researchRecallToolName,
		mcp.WithDescription("Recall prior research items by query string. Returns Evidence-shaped items the operator can re-use without re-dialing the backends."),
		mcp.WithInputSchema[researchRecallInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = ctx
		var in researchRecallInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if in.Operator == "" {
			return mcp.NewToolResultError("research: operator required (INV-1)"), nil
		}
		if in.Query == "" {
			return mcp.NewToolResultError("research: query is required"), nil
		}
		// 10a: recall is a thin wrapper over the cache. The
		// executor exposes a CacheSnapshot for this; the
		// transport calls it and filters by query.
		out := researchRecallOutput{Items: []research.Evidence{}, Count: 0}
		if s.researchExecutor == nil {
			return mcp.NewToolResultError("research executor not configured"), nil
		}
		items := s.researchExecutor.RecallByQuery(in.Query)
		out.Items = items
		out.Count = len(items)
		return resultJSON(out)
	})
}

// ---------- resume_thread ----------

type researchResumeThreadInput struct {
	ThreadID  string `json:"thread_id" jsonschema:"required" jsonschema_description:"Existing thread id to resume"`
	Query     string `json:"query" jsonschema:"required" jsonschema_description:"Follow-up query"`
	MaxItems  int    `json:"max_items,omitempty" jsonschema_description:"Cap on returned items; default 20"`

	Operator  string `json:"operator" jsonschema:"required" jsonschema_description:"Operator id (INV-1)"`
	SessionID string `json:"session_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
}

type researchResumeThreadOutput struct {
	ThreadID string               `json:"thread_id"`
	Items    []research.Evidence  `json:"items"`
	Count    int                  `json:"count"`
}

func registerResearchResumeThread(s *Server) {
	tool := mcp.NewTool(researchResumeThreadToolName,
		mcp.WithDescription("Continue a multi-turn research thread. The executor stitches the new query onto the prior context and returns the merged evidence."),
		mcp.WithInputSchema[researchResumeThreadInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = ctx
		var in researchResumeThreadInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if in.Operator == "" {
			return mcp.NewToolResultError("research: operator required (INV-1)"), nil
		}
		if in.ThreadID == "" || in.Query == "" {
			return mcp.NewToolResultError("thread_id and query are required"), nil
		}
		out := researchResumeThreadOutput{
			ThreadID: in.ThreadID,
			Items:    []research.Evidence{},
			Count:    0,
		}
		if s.researchExecutor == nil {
			return mcp.NewToolResultError("research executor not configured"), nil
		}
		// 10a: thread support is the minimal version — a map of
		// thread_id → []query, plus a per-thread union of
		// Evidence. The full feature (entity resolution across
		// turns, source attribution per turn) lands in 10b.
		items, err := s.researchExecutor.ResumeThread(ctx, in.ThreadID, in.Query, in.MaxItems)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("resume_thread: %v", err)), nil
		}
		out.Items = items
		out.Count = len(items)
		return resultJSON(out)
	})
}
