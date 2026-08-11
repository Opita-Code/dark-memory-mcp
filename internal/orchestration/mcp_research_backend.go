// MCPResearchBackend is a ResearchBackend that connects
// dark_memory_research_topic to the dark-research MCP server by
// spawning its binary and speaking JSON-RPC over stdio (the MCP
// wire protocol).
//
// Why stdio MCP instead of HTTP: dark-research-mcp exposes NO HTTP
// surface — it is an MCP server that speaks JSON-RPC on stdin/stdout
// (see dark-research-mcp cmd/dark-research-mcp/main.go). To route
// research_topic through the real OSINT backends (DuckDuckGo, OSV.dev,
// OpenAlex, HIBP, ...) we must either (a) spawn the binary and speak
// MCP, or (b) duplicate every backend inside dark-memory. (a) is the
// coexistence-contract-correct choice: dark-memory stays the policy
// gateway and delegates OSINT to the peer module.
//
// The backend is OPT-IN: it is only registered when the operator
// points DARK_RESEARCH_MCP_BIN at an existing binary (or leaves it
// unset and a canonical install location exists). Without it,
// research_topic behaves exactly as before (0 items, no error).
//
// Wire sequence per Research call (fresh spawn each call — dark
// research calls are rare, so the ~100ms spawn cost is acceptable
// and we never leak a long-lived subprocess):
//
//	initialize → notifications/initialized → tools/call
//	(dark_research_<intent> or dark_research meta-router) → shutdown
//
// The tools/call response's text field carries the dark-research
// Result JSON (backends.go Result: {results: [...], backend_used,
// errors, summary}). We parse it and map to internal/research.Item.
package orchestration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/research"
)

// MCPResearchBackend implements ResearchBackend against the
// dark-research MCP binary.
type MCPResearchBackend struct {
	// BinPath is the absolute path to dark-research-mcp.exe. When
	// empty, NewMCPResearchBackend resolves it via env then the
	// canonical install locations.
	BinPath string

	// Timeout bounds the whole Research call (spawn + handshake +
	// tools/call). Default 30s (matches opencode.jsonc's
	// dark-research timeout).
	Timeout time.Duration

	// mu guards calls to the internal client (spawn-per-call).
	mu sync.Mutex
}

// NewMCPResearchBackend resolves the dark-research binary path.
// Returns nil (no backend) when the binary is not found — the
// operator hasn't opted in or the peer isn't installed. Callers
// (main.go) skip registration on nil so research_topic degrades
// gracefully.
//
// Resolution order:
//  1. DARK_RESEARCH_MCP_BIN env var (explicit override).
//  2. Canonical install location
//     C:/Users/Nico/dark-research-mcp/dark-research-mcp.exe (dev box).
//  3. "dark-research-mcp.exe" on PATH.
func NewMCPResearchBackend() *MCPResearchBackend {
	b := &MCPResearchBackend{
		Timeout: 30 * time.Second,
	}
	if v := os.Getenv("DARK_RESEARCH_MCP_BIN"); v != "" {
		b.BinPath = v
	} else if _, err := os.Stat("C:/Users/Nico/dark-research-mcp/dark-research-mcp.exe"); err == nil {
		b.BinPath = "C:/Users/Nico/dark-research-mcp/dark-research-mcp.exe"
	} else if _, err := exec.LookPath("dark-research-mcp.exe"); err == nil {
		b.BinPath = "dark-research-mcp.exe"
	}
	if b.BinPath == "" {
		return nil
	}
	if _, err := os.Stat(b.BinPath); err != nil {
		return nil
	}
	if v := os.Getenv("DARK_RESEARCH_MCP_TIMEOUT_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			b.Timeout = time.Duration(n) * time.Millisecond
		}
	}
	return b
}

// Name implements ResearchBackend.
func (b *MCPResearchBackend) Name() string { return "dark_research_mcp" }

// Research implements ResearchBackend. Spawns the peer binary,
// performs an MCP handshake, calls the intent router (or the
// meta-router when intent is empty), and maps the result items.
func (b *MCPResearchBackend) Research(ctx context.Context, query, intent string) ([]research.Item, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, b.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, b.BinPath)
	cmd.Env = os.Environ() // inherit BRAVE_API_KEY, DARK_RESEARCH_CONFIG, ...
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp backend: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp backend: stdout pipe: %w", err)
	}
	// Stderr is left connected to the parent's stderr so the peer's
	// boot logs surface in the MCP server's own log stream.
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp backend: spawn %s: %w", b.BinPath, err)
	}
	defer func() {
		// Best-effort close. The process exits on EOF stdin; kill is
		// the backstop for a wedged peer.
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	c := &mcpStdioClient{in: stdin, out: bufio.NewReader(stdout), nextID: 1}
	if err := c.initialize(); err != nil {
		return nil, fmt.Errorf("mcp backend: initialize: %w", err)
	}

	toolName := "dark_research"
	if intent != "" {
		toolName = "dark_research_" + intent
	}
	args := map[string]any{"query": query, "limit": 20}
	respText, err := c.callTool(ctx, toolName, args)
	if err != nil {
		return nil, fmt.Errorf("mcp backend: tools/call %s: %w", toolName, err)
	}

	return mapDarkResearchResult(respText)
}

// mcpStdioClient is the minimal MCP JSON-RPC client over stdio.
// It speaks just enough of the protocol for initialize + tools/call.
type mcpStdioClient struct {
	in     io.WriteCloser
	out    *bufio.Reader
	nextID int64
}

type mcpRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      int64         `json:"id"`
	Result  *mcpRespInner  `json:"result,omitempty"`
	Error   *mcpRespErr    `json:"error,omitempty"`
}

type mcpRespInner struct {
	ProtocolVersion string          `json:"protocolVersion,omitempty"`
	Capabilities    map[string]any  `json:"capabilities,omitempty"`
	Content         []mcpContent    `json:"content,omitempty"`
	IsError         bool            `json:"isError,omitempty"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type mcpRespErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// initialize performs the MCP initialize handshake + the
// notifications/initialized follow-up (non-blocking; the server
// doesn't reply to notifications).
func (c *mcpStdioClient) initialize() error {
	req := mcpRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "dark-memory-mcp",
				"version": "2.15.0",
			},
		},
	}
	if err := c.write(req); err != nil {
		return err
	}
	resp, err := c.read()
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("initialize error: %s", resp.Error.Message)
	}
	// Follow-up notification (no response expected).
	_, err = c.in.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"))
	return err
}

// callTool invokes tools/call and returns the concatenated text of
// the first text content block. Errors surface as *mcpRespErr.
func (c *mcpStdioClient) callTool(ctx context.Context, name string, args map[string]any) (string, error) {
	c.nextID++
	req := mcpRequest{
		JSONRPC: "2.0",
		ID:      c.nextID,
		Method:  "tools/call",
		Params: map[string]any{
			"name":      name,
			"arguments": args,
		},
	}
	if err := c.write(req); err != nil {
		return "", err
	}
	// Read until we get the response with our ID (the server may
	// emit log notifications first).
	for {
		resp, err := c.read()
		if err != nil {
			return "", err
		}
		if resp.ID != c.nextID {
			continue // notification or another frame; skip
		}
		if resp.Error != nil {
			return "", fmt.Errorf("%s", resp.Error.Message)
		}
		if resp.Result == nil {
			return "", fmt.Errorf("empty result frame")
		}
		if resp.Result.IsError {
			// tools/call error: the content carries the error text.
			var sb strings.Builder
			for _, ct := range resp.Result.Content {
				sb.WriteString(ct.Text)
			}
			if sb.Len() == 0 {
				return "", fmt.Errorf("tool returned isError with no content")
			}
			return "", fmt.Errorf("%s", sb.String())
		}
		var sb strings.Builder
		for _, ct := range resp.Result.Content {
			sb.WriteString(ct.Text)
		}
		return sb.String(), nil
	}
}

func (c *mcpStdioClient) write(req mcpRequest) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = c.in.Write(b)
	return err
}

func (c *mcpStdioClient) read() (*mcpResponse, error) {
	line, err := c.out.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var resp mcpResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &resp); err != nil {
		return nil, fmt.Errorf("malformed MCP frame: %w: %s", err, truncateForErr(string(line), 300))
	}
	return &resp, nil
}

// darkResearchResult mirrors the dark-research Result JSON shape
// (internal/research/backends.go Result) for the fields we consume.
type darkResearchResult struct {
	Intent      string              `json:"intent"`
	Query       string              `json:"query"`
	BackendUsed string              `json:"backend_used"`
	Items       []darkResearchItem  `json:"results"`
	Summary     string              `json:"summary,omitempty"`
	Errors      []darkResearchErr   `json:"errors,omitempty"`
}

type darkResearchErr struct {
	Backend string `json:"backend"`
	Err     string `json:"error"`
}

// darkResearchItem mirrors one normalized result item.
type darkResearchItem struct {
	Title       string         `json:"title"`
	URL         string         `json:"url"`
	Snippet     string         `json:"snippet"`
	Source      string         `json:"source"`
	Confidence  float32        `json:"confidence"`
	FreshnessAt string         `json:"freshness_at,omitempty"`
	Lang        string         `json:"lang,omitempty"`
	Raw         map[string]any `json:"raw,omitempty"`
}

// mapDarkResearchResult parses the tools/call text payload (the
// dark-research Result JSON) into []research.Item. Returns an error
// when the payload is not the expected shape (so the caller logs the
// backend error instead of fabricating empty items).
func mapDarkResearchResult(text string) ([]research.Item, error) {
	var res darkResearchResult
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		return nil, fmt.Errorf("parse dark-research result: %w", err)
	}
	items := make([]research.Item, 0, len(res.Items))
	now := time.Now().UTC().Format(time.RFC3339)
	for _, it := range res.Items {
		item := research.Item{
			Title:       it.Title,
			URL:         it.URL,
			Snippet:     it.Snippet,
			Source:      it.Source,
			Confidence:  it.Confidence,
			Lang:        it.Lang,
			CreatedAt:   now,
			FreshnessAt: it.FreshnessAt,
		}
		if it.Raw != nil {
			if b, err := json.Marshal(it.Raw); err == nil {
				item.Raw = string(b)
			}
		}
		items = append(items, item)
	}
	return items, nil
}
