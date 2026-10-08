// vibe_* tools — the v4-alpha runtime of the vibe-loop workflow:
//
//   - dark_memory_vibe_spec            → SpecStore.Insert
//   - dark_memory_vibe_publish         → Pipeline.Publish
//   - dark_memory_vibe_pipeline_status → Pipeline.Status
//   - dark_memory_vibe_resolve_drift   → Pipeline.Resolve
//
// The vibe-loop is M1 movement (workflow = runtime mutable state):
// the spec is the input, the artifact is the publishable unit,
// the drift row is the judge verdict + audit hook. v4-alpha.1
// uses the NoOpJudge (always aligned); the LLM-backed judge
// lands in BUG-9.
package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/vibe"
	"github.com/dark-agents/dark-memory-mcp/internal/vibecase"
)

const (
	vibeSpecToolName           = "dark_memory_vibe_spec"
	vibePublishToolName        = "dark_memory_vibe_publish"
	vibePipelineStatusToolName = "dark_memory_vibe_pipeline_status"
	vibeResolveDriftToolName   = "dark_memory_vibe_resolve_drift"
)

func registerVibeTools(s *Server) {
	registerVibeSpec(s)
	registerVibePublish(s)
	registerVibePipelineStatus(s)
	registerVibeResolveDrift(s)
}

// --- vibe_spec ---

type vibeSpecTask struct {
	ID          string   `json:"id" jsonschema:"required" jsonschema_description:"Unique task id within the spec"`
	Description string   `json:"description" jsonschema:"required" jsonschema_description:"What this task produces"`
	DependsOn   []string `json:"depends_on,omitempty" jsonschema_description:"Task ids this task depends on"`
}

type vibeSpecInput struct {
	VibeCase string        `json:"vibe_case" jsonschema:"required" jsonschema_description:"C1..C8 (code, text, decision, research, video, audio, multi, vibe-flow). Canonical allow-list lives in internal/vibecase."`
	Intent   string        `json:"intent" jsonschema:"required" jsonschema_description:"One-paragraph 'what should this artifact be' hypothesis"`
	Tasks    []vibeSpecTask `json:"tasks" jsonschema:"required" jsonschema_description:"At least one task required"`
}

type vibeSpecOutput struct {
	SpecID    int64             `json:"spec_id"`
	VibeCase  string            `json:"vibe_case"`
	Intent    string            `json:"intent"`
	TaskCount int               `json:"task_count"`
	CreatedAt string            `json:"created_at"`
}

func registerVibeSpec(s *Server) {
	tool := mcp.NewTool(vibeSpecToolName,
		mcp.WithDescription("Create a vibe-loop spec with a validated task graph. Returns the new spec_id."),
		mcp.WithInputSchema[vibeSpecInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in vibeSpecInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if !vibe.IsValidVibeCase(in.VibeCase) {
			return mcp.NewToolResultError(fmt.Sprintf("vibe_spec: invalid vibe_case %q (canonical allow-list: %v)", in.VibeCase, vibecase.JSONSchemaEnum())), nil
		}
		tasks := make([]vibe.Task, 0, len(in.Tasks))
		for _, t := range in.Tasks {
			tasks = append(tasks, vibe.Task{
				ID:          t.ID,
				Description: t.Description,
				DependsOn:   t.DependsOn,
			})
		}
		spec := &vibe.Spec{
			VibeCase: in.VibeCase,
			Intent:   in.Intent,
			Tasks:    tasks,
		}
		if err := spec.Validate(); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("vibe_spec: %v", err)), nil
		}
		id, err := s.pipeline.Specs().Insert(ctx, spec)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("vibe_spec: %v", err)), nil
		}
		out := vibeSpecOutput{
			SpecID:    id,
			VibeCase:  spec.VibeCase,
			Intent:    spec.Intent,
			TaskCount: len(spec.Tasks),
			CreatedAt: spec.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"),
		}
		return resultJSON(out)
	})
}

// --- vibe_publish ---

type vibePublishInput struct {
	SpecID    int64  `json:"spec_id" jsonschema:"required" jsonschema_description:"Spec this artifact is published under"`
	Type      string `json:"type" jsonschema:"required" jsonschema_description:"code|text|image|video|audio|multi"`
	URL       string `json:"url,omitempty" jsonschema_description:"Public URL when type != text"`
	Text      string `json:"text,omitempty" jsonschema_description:"Inline text when type=text"`
	SpecIntent string `json:"spec_intent,omitempty" jsonschema_description:"Override of the spec's intent; defaults to spec.Intent"`
}

type vibePublishOutput struct {
	ArtifactID int64  `json:"artifact_id"`
	SpecID     int64  `json:"spec_id"`
	DriftID    int64  `json:"drift_id"`
	Verdict    string `json:"verdict"`
	Confidence float64 `json:"confidence"`
}

func registerVibePublish(s *Server) {
	tool := mcp.NewTool(vibePublishToolName,
		mcp.WithDescription("Publish one artifact under a spec. Triggers judge + drift_log; returns drift verdict."),
		mcp.WithInputSchema[vibePublishInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in vibePublishInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if !vibe.IsValidArtifactType(in.Type) {
			return mcp.NewToolResultError(fmt.Sprintf("vibe_publish: invalid type %q", in.Type)), nil
		}
		if in.Type == vibe.ArtifactTypeText && in.Text == "" {
			return mcp.NewToolResultError("vibe_publish: text artifacts require content"), nil
		}
		if in.Type != vibe.ArtifactTypeText && in.URL == "" {
			return mcp.NewToolResultError("vibe_publish: non-text artifacts require url"), nil
		}

		specIntent := in.SpecIntent
		if specIntent == "" {
			spec, err := s.pipeline.Specs().Get(ctx, in.SpecID)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("vibe_publish: %v", err)), nil
			}
			specIntent = spec.Intent
		}

		art := &vibe.Artifact{
			SpecID: in.SpecID,
			Type:   in.Type,
			URL:    in.URL,
			Text:   in.Text,
		}
		drift, err := s.pipeline.Publish(ctx, art, specIntent)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("vibe_publish: %v", err)), nil
		}
		out := vibePublishOutput{
			ArtifactID: drift.ArtifactID,
			SpecID:     in.SpecID,
			DriftID:    drift.ID,
			Verdict:    drift.Verdict,
			Confidence: drift.Confidence,
		}
		return resultJSON(out)
	})
}

// --- vibe_pipeline_status ---

type vibePipelineStatusInput struct {
	ArtifactID int64 `json:"artifact_id" jsonschema:"required" jsonschema_description:"Artifact to query"`
}

type vibePipelineStatusOutput struct {
	ArtifactID  int64   `json:"artifact_id"`
	DriftID     int64   `json:"drift_id"`
	Verdict     string  `json:"verdict"`
	Confidence  float64 `json:"confidence"`
	Reasoning   string  `json:"reasoning,omitempty"`
	EvaluatedAt string  `json:"evaluated_at"`
}

func registerVibePipelineStatus(s *Server) {
	tool := mcp.NewTool(vibePipelineStatusToolName,
		mcp.WithDescription("Latest drift verdict for one artifact."),
		mcp.WithInputSchema[vibePipelineStatusInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in vibePipelineStatusInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if in.ArtifactID <= 0 {
			return mcp.NewToolResultError("vibe_pipeline_status: artifact_id must be positive"), nil
		}
		drift, err := s.pipeline.Status(ctx, in.ArtifactID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("vibe_pipeline_status: %v", err)), nil
		}
		out := vibePipelineStatusOutput{
			ArtifactID:  drift.ArtifactID,
			DriftID:     drift.ID,
			Verdict:     drift.Verdict,
			Confidence:  drift.Confidence,
			Reasoning:   drift.Reasoning,
			EvaluatedAt: drift.EvaluatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"),
		}
		return resultJSON(out)
	})
}

// --- vibe_resolve_drift ---

type vibeResolveDriftInput struct {
	DriftID  int64  `json:"drift_id" jsonschema:"required" jsonschema_description:"Drift row to resolve"`
	Decision string `json:"decision" jsonschema:"required" jsonschema_description:"accept|reject"`
	Note     string `json:"note,omitempty" jsonschema_description:"Operator note explaining the decision"`
	Operator string `json:"operator" jsonschema:"required" jsonschema_description:"Operator id (INV-1 audit owner)"`
}

type vibeResolveDriftOutput struct {
	DriftID    int64  `json:"drift_id"`
	Decision   string `json:"decision"`
	Operator   string `json:"operator"`
	ResolvedAt string `json:"resolved_at"`
}

func registerVibeResolveDrift(s *Server) {
	tool := mcp.NewTool(vibeResolveDriftToolName,
		mcp.WithDescription("Operator gate: accept (artifact correct as-is) or reject (artifact wrong)."),
		mcp.WithInputSchema[vibeResolveDriftInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in vibeResolveDriftInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if in.DriftID <= 0 {
			return mcp.NewToolResultError("vibe_resolve_drift: drift_id must be positive"), nil
		}
		if in.Decision != "accept" && in.Decision != "reject" {
			return mcp.NewToolResultError("vibe_resolve_drift: decision must be accept or reject"), nil
		}
		if in.Operator == "" {
			return mcp.NewToolResultError("vibe_resolve_drift: operator is required"), nil
		}
		if err := s.pipeline.Resolve(ctx, in.DriftID, in.Decision, in.Note, in.Operator); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("vibe_resolve_drift: %v", err)), nil
		}
		out := vibeResolveDriftOutput{
			DriftID:    in.DriftID,
			Decision:   in.Decision,
			Operator:   in.Operator,
			ResolvedAt: nowRFC3339(),
		}
		return resultJSON(out)
	})
}
