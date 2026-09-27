// agent_memory_* tools — operator-scoped memory store with FTS5
// recall. Wraps the agent_memory.Store methods to MCP tools:
//
//   - dark_memory_agent_memory_save    → agent_memory.Store.Save
//   - dark_memory_agent_memory_recall  → agent_memory.Store.Recall
//   - dark_memory_agent_memory_list    → agent_memory.Store.List
//   - dark_memory_agent_memory_get     → agent_memory.Store.Get
//   - dark_memory_agent_memory_update  → agent_memory.Store.Update
//   - dark_memory_agent_memory_archive → agent_memory.Store.Archive
//
// The agent_memory package enforces INV-1 (operator id non-empty)
// and the canonical kind allow-list. The tools pass-through
// without re-validating; the Store returns ErrEmptyOperator /
// ErrInvalidKind on bad input.
//
// subagent_register / subagent_unregister / delegate / entities
// land in BUG-9 (they require the C2 subagent binding surface +
// entity extraction pipeline, both deferred).
package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
)

const (
	agentMemorySaveToolName    = "dark_memory_agent_memory_save"
	agentMemoryRecallToolName  = "dark_memory_agent_memory_recall"
	agentMemoryListToolName    = "dark_memory_agent_memory_list"
	agentMemoryGetToolName     = "dark_memory_agent_memory_get"
	agentMemoryUpdateToolName  = "dark_memory_agent_memory_update"
	agentMemoryArchiveToolName = "dark_memory_agent_memory_archive"
)

func registerAgentMemoryTools(s *Server) {
	registerAgentMemorySave(s)
	registerAgentMemoryRecall(s)
	registerAgentMemoryList(s)
	registerAgentMemoryGet(s)
	registerAgentMemoryUpdate(s)
	registerAgentMemoryArchive(s)
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
	ID       int64  `json:"id"`
	Operator string `json:"operator"`
	Kind     string `json:"kind"`
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
	Operator string             `json:"operator"`
	Query    string             `json:"query"`
	Count    int                `json:"count"`
	Rows     []agent_memory.Row `json:"rows"`
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

// --- list ---

type agentMemoryListInput struct {
	Operator string `json:"operator" jsonschema:"required" jsonschema_description:"Operator id (scope axis)"`
	Limit    int    `json:"limit,omitempty" jsonschema_description:"Max rows; defaults 50"`
}

type agentMemoryListOutput struct {
	Operator string             `json:"operator"`
	Count    int                `json:"count"`
	Rows     []agent_memory.Row `json:"rows"`
}

func registerAgentMemoryList(s *Server) {
	tool := mcp.NewTool(agentMemoryListToolName,
		mcp.WithDescription("List rows for one operator, pinned first then newest. Read-only."),
		mcp.WithInputSchema[agentMemoryListInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in agentMemoryListInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if in.Operator == "" {
			return mcp.NewToolResultError("agent_memory_list: operator is required"), nil
		}
		rows, err := s.memories.List(ctx, in.Operator, in.Limit)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("agent_memory_list: %v", err)), nil
		}
		return resultJSON(agentMemoryListOutput{
			Operator: in.Operator,
			Count:    len(rows),
			Rows:     rows,
		})
	})
}

// --- get ---

type agentMemoryGetInput struct {
	ID int64 `json:"id" jsonschema:"required" jsonschema_description:"Row id"`
}

type agentMemoryGetOutput struct {
	ID  int64             `json:"id"`
	Row *agent_memory.Row `json:"row"`
}

func registerAgentMemoryGet(s *Server) {
	tool := mcp.NewTool(agentMemoryGetToolName,
		mcp.WithDescription("Read one row by id. Returns null row when not found."),
		mcp.WithInputSchema[agentMemoryGetInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in agentMemoryGetInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if in.ID <= 0 {
			return mcp.NewToolResultError("agent_memory_get: id must be positive"), nil
		}
		row, err := s.memories.Get(ctx, in.ID)
		if err != nil {
			// ErrNotFound returns empty result (not error) — the wire
			// schema is `row: null` so callers can distinguish.
			return resultJSON(agentMemoryGetOutput{ID: in.ID, Row: nil})
		}
		return resultJSON(agentMemoryGetOutput{ID: in.ID, Row: row})
	})
}

// --- update ---

// agentMemoryUpdateInput uses *string / *bool for optional fields
// so callers can choose which to mutate. nil = leave alone.
type agentMemoryUpdateInput struct {
	ID      int64   `json:"id" jsonschema:"required" jsonschema_description:"Row id"`
	Title   *string `json:"title,omitempty" jsonschema_description:"New title; pass empty string to clear"`
	Content *string `json:"content,omitempty" jsonschema_description:"New content (cannot be empty)"`
	Tags    *string `json:"tags,omitempty" jsonschema_description:"New tags; pass empty string to clear"`
	Pinned  *bool   `json:"pinned,omitempty" jsonschema_description:"New pinned flag"`
}

type agentMemoryUpdateOutput struct {
	ID      int64 `json:"id"`
	Updated bool  `json:"updated"`
}

func registerAgentMemoryUpdate(s *Server) {
	tool := mcp.NewTool(agentMemoryUpdateToolName,
		mcp.WithDescription("Mutate one row's title/content/tags/pinned. Operator + kind are immutable."),
		mcp.WithInputSchema[agentMemoryUpdateInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in agentMemoryUpdateInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if in.ID <= 0 {
			return mcp.NewToolResultError("agent_memory_update: id must be positive"), nil
		}
		err := s.memories.Update(ctx, in.ID, in.Title, in.Content, in.Tags, in.Pinned)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("agent_memory_update: %v", err)), nil
		}
		return resultJSON(agentMemoryUpdateOutput{ID: in.ID, Updated: true})
	})
}

// --- archive ---

type agentMemoryArchiveInput struct {
	ID int64 `json:"id" jsonschema:"required" jsonschema_description:"Row id"`
}

type agentMemoryArchiveOutput struct {
	ID       int64 `json:"id"`
	Archived bool  `json:"archived"`
}

func registerAgentMemoryArchive(s *Server) {
	tool := mcp.NewTool(agentMemoryArchiveToolName,
		mcp.WithDescription("Soft-delete one row. FTS5 index is synced in the same Tx."),
		mcp.WithInputSchema[agentMemoryArchiveInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in agentMemoryArchiveInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if in.ID <= 0 {
			return mcp.NewToolResultError("agent_memory_archive: id must be positive"), nil
		}
		err := s.memories.Archive(ctx, in.ID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("agent_memory_archive: %v", err)), nil
		}
		return resultJSON(agentMemoryArchiveOutput{ID: in.ID, Archived: true})
	})
}
