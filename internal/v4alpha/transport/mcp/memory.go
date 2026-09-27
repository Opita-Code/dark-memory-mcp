// agent_memory_* tools — operator-scoped memory store with FTS5
// recall. Wraps the agent_memory.Store methods to MCP tools:
//
//   - dark_memory_agent_memory_save    → agent_memory.Store.Save
//   - dark_memory_agent_memory_recall  → agent_memory.Store.Recall
//
// The agent_memory package enforces INV-1 (operator id non-empty)
// and the canonical kind allow-list. The tools pass-through
// without re-validating; the Store returns ErrEmptyOperator /
// ErrInvalidKind on bad input.
package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
)

const (
	agentMemorySaveToolName   = "dark_memory_agent_memory_save"
	agentMemoryRecallToolName = "dark_memory_agent_memory_recall"
)

func registerAgentMemoryTools(s *Server) {
	registerAgentMemorySave(s)
	registerAgentMemoryRecall(s)
}

// --- save ---

type agentMemorySaveInput struct {
	Operator string `json:"operator" jsonschema:"required" jsonschema_description:"Operator id (INV-1 audit owner)"`
	Kind     string `json:"kind" jsonschema:"required" jsonschema_description:"One of: note, observation, decision, finding, todo, link, context"`
	Title    string `json:"title,omitempty" jsonschema_description:"Optional short title"`
	Content  string `json:"content" jsonschema:"required" jsonschema_description:"Memory body"`
	Tags     string `json:"tags,omitempty" jsonschema_description:"Comma-separated tags for grouping"`
	Pinned   bool   `json:"pinned,omitempty" jsonschema_description:"Surface this row first in List"`
}

type agentMemorySaveOutput struct {
	ID      int64  `json:"id"`
	Operator string `json:"operator"`
	Kind    string `json:"kind"`
}

func registerAgentMemorySave(s *Server) {
	tool := mcp.NewTool(agentMemorySaveToolName,
		mcp.WithDescription("Save one row of operator-scoped memory. Returns the new id."),
		mcp.WithInputSchema[agentMemorySaveInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in agentMemorySaveInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		id, err := s.memories.Save(ctx, in.Operator, in.Kind, in.Title, in.Content, in.Tags, in.Pinned)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("agent_memory_save: %v", err)), nil
		}
		return resultJSON(agentMemorySaveOutput{
			ID:       id,
			Operator: in.Operator,
			Kind:     in.Kind,
		})
	})
}

// --- recall ---

type agentMemoryRecallInput struct {
	Operator string `json:"operator" jsonschema:"required" jsonschema_description:"Operator id (scope axis)"`
	Query    string `json:"query" jsonschema:"required" jsonschema_description:"FTS5 query string"`
	Limit    int    `json:"limit,omitempty" jsonschema_description:"Max rows; defaults 10"`
}

type agentMemoryRecallOutput struct {
	Operator string                `json:"operator"`
	Query    string                `json:"query"`
	Count    int                   `json:"count"`
	Rows     []agent_memory.Row    `json:"rows"`
}

func registerAgentMemoryRecall(s *Server) {
	tool := mcp.NewTool(agentMemoryRecallToolName,
		mcp.WithDescription("FTS5 search across (content, title, tags). Operator-scoped."),
		mcp.WithInputSchema[agentMemoryRecallInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in agentMemoryRecallInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if in.Operator == "" {
			return mcp.NewToolResultError("agent_memory_recall: operator is required"), nil
		}
		if in.Query == "" {
			return mcp.NewToolResultError("agent_memory_recall: query is required"), nil
		}
		rows, err := s.memories.Recall(ctx, in.Operator, in.Query, in.Limit)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("agent_memory_recall: %v", err)), nil
		}
		return resultJSON(agentMemoryRecallOutput{
			Operator: in.Operator,
			Query:    in.Query,
			Count:    len(rows),
			Rows:     rows,
		})
	})
}
