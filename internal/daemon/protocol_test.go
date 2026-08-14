package daemon

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestRoundTrip_RequestResponse(t *testing.T) {
	params := map[string]any{"name": "agent_memory_save", "arguments": map[string]any{"x": 1}}
	req, err := NewRequestFrame("req-1", "tools/call", params)
	if err != nil {
		t.Fatalf("NewRequestFrame: %v", err)
	}
	reqJSON, err := MarshalFrame(req)
	if err != nil {
		t.Fatalf("MarshalFrame: %v", err)
	}
	got, err := UnmarshalFrame(reqJSON)
	if err != nil {
		t.Fatalf("UnmarshalFrame: %v", err)
	}
	if got.ID != "req-1" || got.Method != "tools/call" || got.Type != FrameTypeRPC {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
	if !bytes.Contains(got.Params, []byte("agent_memory_save")) {
		t.Errorf("params lost: %s", string(got.Params))
	}
}

func TestRoundTrip_NotifyAndPong(t *testing.T) {
	notify := NewNotifyFrame("daemon_shutdown", "idle_timeout")
	notifyJSON, _ := MarshalFrame(notify)
	got, err := UnmarshalFrame(notifyJSON)
	if err != nil {
		t.Fatalf("UnmarshalFrame: %v", err)
	}
	if got.Type != FrameTypeNotify || got.Event != "daemon_shutdown" || got.Reason != "idle_timeout" {
		t.Errorf("notify roundtrip: %+v", got)
	}

	pong := NewPongFrame(1234*1e9, "2.19.0")
	pongJSON, _ := MarshalFrame(pong)
	gotPong, _ := UnmarshalFrame(pongJSON)
	if gotPong.Type != FrameTypePong || gotPong.UptimeSec != 1234 || gotPong.Version != "2.19.0" {
		t.Errorf("pong roundtrip: %+v", gotPong)
	}
}

func TestReadFrame_AcceptsCRLF(t *testing.T) {
	req, _ := NewRequestFrame("req-x", "ping", nil)
	b, _ := MarshalFrame(req)
	var buf bytes.Buffer
	buf.Write(b)
	buf.WriteString("\r\n") // CRLF instead of LF
	r := bufio.NewReader(&buf)
	frame, err := ReadFrame(r)
	if err != nil {
		t.Fatalf("ReadFrame with CRLF: %v", err)
	}
	if frame.ID != "req-x" {
		t.Errorf("CRLF frame id: %q", frame.ID)
	}
}

func TestReadFrame_EmptyLinesTolerated(t *testing.T) {
	req, _ := NewRequestFrame("req-y", "ping", nil)
	b, _ := MarshalFrame(req)
	var buf bytes.Buffer
	buf.WriteString("\n")
	buf.Write(b)
	buf.WriteString("\n")
	r := bufio.NewReader(&buf)
	frame, err := ReadFrame(r)
	if err != nil {
		t.Fatalf("ReadFrame with empty lines: %v", err)
	}
	if frame.ID != "req-y" {
		t.Errorf("empty-line frame id: %q", frame.ID)
	}
}

func TestReadFrame_EOFIsClean(t *testing.T) {
	var buf bytes.Buffer
	r := bufio.NewReader(&buf)
	_, err := ReadFrame(r)
	if !errors.Is(err, io.EOF) {
		t.Errorf("expected io.EOF on empty stream, got %v", err)
	}
}

func TestUnmarshalFrame_MissingType(t *testing.T) {
	_, err := UnmarshalFrame([]byte(`{"id":"req","method":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "missing type") {
		t.Errorf("expected missing-type error, got %v", err)
	}
}

func TestRequestFrameValidation(t *testing.T) {
	if _, err := NewRequestFrame("", "method", nil); err == nil {
		t.Error("empty id should fail")
	}
	if _, err := NewRequestFrame("id", "", nil); err == nil {
		t.Error("empty method should fail")
	}
	if _, err := NewResponseFrame("", nil, nil); err == nil {
		t.Error("empty id in response should fail")
	}
}
