// Package daemon implements the bridge-daemon protocol for spec 1176
// (Daemon-Bridge wrapper architecture). The bridge is a thin stdio
// proxy; the daemon is the long-lived process that owns SQLite, the
// constitution watchdog, and the 53-tool registry.
//
// Spec 1176 (plan label "Spec 1150-equivalent") — v2.19.0 of dark-memory.
//
// The protocol between bridge and daemon is line-delimited JSON over
// either a Unix socket (macOS/Linux) or a Windows named pipe
// (\\.\pipe\dark-mem). Each line is one JSON-RPC 2.0 frame.
package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// FrameType identifies the kind of payload in a Frame.
type FrameType string

const (
	FrameTypeRPC      FrameType = "rpc"      // request/response (matches MCP wire format)
	FrameTypeNotify   FrameType = "notify"   // one-way notification (no response)
	FrameTypePing     FrameType = "ping"     // liveness probe
	FrameTypePong     FrameType = "pong"     // liveness reply
	FrameTypeShutdown FrameType = "shutdown" // daemon is going away
)

// Frame is one line on the bridge<->daemon socket. Exactly one of
// Request/Response/Notification/Ping is populated based on Type.
//
// JSON wire format:
//
//	{"id":"req-1","type":"rpc","method":"tools/call","params":{...}}
//	{"id":"req-1","type":"rpc","result":{"content":[...],"isError":false}}
//	{"type":"notify","event":"daemon_shutdown","reason":"idle_timeout"}
//	{"type":"ping"}
//	{"type":"pong","uptime_sec":1234,"version":"2.19.0"}
type Frame struct {
	// ID correlates request <-> response. Empty for notifications + pings.
	ID string `json:"id,omitempty"`
	// Type is the discriminator.
	Type FrameType `json:"type"`
	// Method is the JSON-RPC method (e.g. "tools/call", "tools/list"). RPC frames.
	Method string `json:"method,omitempty"`
	// Params is the JSON-RPC params. RPC frames (request).
	Params json.RawMessage `json:"params,omitempty"`
	// Result is the JSON-RPC result. RPC frames (response).
	Result json.RawMessage `json:"result,omitempty"`
	// Error is the JSON-RPC error. RPC frames (response).
	Error *RPCError `json:"error,omitempty"`
	// Event is the notification name (e.g. "daemon_shutdown"). Notify frames.
	Event string `json:"event,omitempty"`
	// Reason is the notification reason. Notify frames.
	Reason string `json:"reason,omitempty"`
	// UptimeSec is the daemon uptime. Pong frames.
	UptimeSec int64 `json:"uptime_sec,omitempty"`
	// Version is the daemon version. Pong frames.
	Version string `json:"version,omitempty"`
}

// RPCError is the JSON-RPC error object (subset of the spec).
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MarshalFrame encodes one frame as a single JSON line (no trailing
// newline; callers append `\n` before writing to the wire).
func MarshalFrame(f Frame) ([]byte, error) {
	b, err := json.Marshal(f)
	if err != nil {
		return nil, fmt.Errorf("marshal frame: %w", err)
	}
	return b, nil
}

// UnmarshalFrame parses one JSON line into a Frame.
func UnmarshalFrame(line []byte) (Frame, error) {
	var f Frame
	if err := json.Unmarshal(line, &f); err != nil {
		return f, fmt.Errorf("unmarshal frame: %w", err)
	}
	if f.Type == "" {
		return f, errors.New("frame missing type")
	}
	return f, nil
}

// ReadFrame reads one frame from r. Blocks until a newline is read or
// the reader is exhausted. The newline is consumed but not returned.
//
// Returns (frame, nil) on success, (Frame{}, io.EOF) on clean EOF, or
// (Frame{}, error) on protocol violation / I/O error.
func ReadFrame(r *bufio.Reader) (Frame, error) {
	line, err := r.ReadBytes('\n')
	if err != nil {
		if err == io.EOF {
			return Frame{}, io.EOF
		}
		return Frame{}, fmt.Errorf("read frame: %w", err)
	}
	line = []byte(strings.TrimRight(string(line), "\r\n"))
	if len(line) == 0 {
		// Empty line; tolerate as a no-op.
		return ReadFrame(r)
	}
	return UnmarshalFrame(line)
}

// WriteFrame encodes f and writes it as a single newline-terminated
// line on w.
func WriteFrame(w io.Writer, f Frame) error {
	b, err := MarshalFrame(f)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}
	return nil
}

// NewRequestFrame builds a JSON-RPC request frame.
func NewRequestFrame(id, method string, params any) (Frame, error) {
	if id == "" {
		return Frame{}, errors.New("request id is required")
	}
	if method == "" {
		return Frame{}, errors.New("request method is required")
	}
	var paramsJSON json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return Frame{}, fmt.Errorf("marshal params: %w", err)
		}
		paramsJSON = b
	}
	return Frame{
		ID:     id,
		Type:   FrameTypeRPC,
		Method: method,
		Params: paramsJSON,
	}, nil
}

// NewResponseFrame builds a JSON-RPC response frame.
func NewResponseFrame(id string, result any, err *RPCError) (Frame, error) {
	if id == "" {
		return Frame{}, errors.New("response id is required")
	}
	var resultJSON json.RawMessage
	if result != nil {
		b, err := json.Marshal(result)
		if err != nil {
			return Frame{}, fmt.Errorf("marshal result: %w", err)
		}
		resultJSON = b
	}
	return Frame{
		ID:     id,
		Type:   FrameTypeRPC,
		Result: resultJSON,
		Error:  err,
	}, nil
}

// NewNotifyFrame builds a one-way notification frame.
func NewNotifyFrame(event, reason string) Frame {
	return Frame{
		Type:   FrameTypeNotify,
		Event:  event,
		Reason: reason,
	}
}

// NewPingFrame builds a liveness probe frame.
func NewPingFrame() Frame {
	return Frame{Type: FrameTypePing}
}

// NewPongFrame builds a liveness reply with uptime + version.
func NewPongFrame(uptime time.Duration, version string) Frame {
	return Frame{
		Type:      FrameTypePong,
		UptimeSec: int64(uptime.Seconds()),
		Version:   version,
	}
}
