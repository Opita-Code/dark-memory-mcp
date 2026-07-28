// Package agentbootstrap - clientinfo_test.go: tests for the dual-spec
// clientInfo reader.
//
// What this verifies:
//
//   - The global store is single-instance (singleton).
//   - SetFromInitialize stores the captured clientInfo with the
//     legacy "initialize.clientInfo" source label.
//   - SetFromMeta stores the captured clientInfo with the new
//     "_meta.clientInfo" source label.
//   - SetFromMeta handles the three shapes we accept: typed
//     Implementation, *Implementation pointer, and map[string]any.
//   - NormalizeClientName maps known harness names to canonical short
//     names (claude-desktop, claude-code, opencode, cline, cursor,
//     continue); unknown names return "unknown".
//   - The dual-spec path converges on the same CurrentClientInfo()
//     accessor regardless of which spec the harness used.
package agentbootstrap

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestNormalizeClientName(t *testing.T) {
	cases := []struct {
		name string
		in   mcp.Implementation
		want string
	}{
		{
			name: "claude-desktop via Name",
			in:   mcp.Implementation{Name: "claude-desktop", Title: "Claude Desktop", Version: "1.0.0"},
			want: "claude-desktop",
		},
		{
			name: "claude-code via Name",
			in:   mcp.Implementation{Name: "claude-code", Title: "Claude Code", Version: "1.0.0"},
			want: "claude-code",
		},
		{
			name: "opencode via Name",
			in:   mcp.Implementation{Name: "opencode", Title: "opencode", Version: "1.0.0"},
			want: "opencode",
		},
		{
			name: "cline via Name",
			in:   mcp.Implementation{Name: "cline", Title: "Cline", Version: "3.0.0"},
			want: "cline",
		},
		{
			name: "roo-cline normalized to cline",
			in:   mcp.Implementation{Name: "roo-cline", Title: "Roo Cline", Version: "1.0.0"},
			want: "cline",
		},
		{
			name: "cursor via Name",
			in:   mcp.Implementation{Name: "cursor", Title: "Cursor", Version: "0.40.0"},
			want: "cursor",
		},
		{
			name: "continue via Name",
			in:   mcp.Implementation{Name: "continue", Title: "Continue", Version: "0.9.0"},
			want: "continue",
		},
		{
			name: "unknown harness",
			in:   mcp.Implementation{Name: "some-other-tool", Title: "Some Other Tool", Version: "0.1.0"},
			want: "unknown",
		},
		{
			name: "empty Name + empty Title returns unknown",
			in:   mcp.Implementation{Name: "", Title: "", Version: ""},
			want: "unknown",
		},
		{
			name: "bare 'claude' prefix without disambiguator",
			in:   mcp.Implementation{Name: "claude", Title: "Claude", Version: "1.0.0"},
			want: "claude-family",
		},
		{
			name: "match via Title field (Name is generic SDK default)",
			in:   mcp.Implementation{Name: "mcp-client", Title: "Claude Desktop", Version: "1.0.0"},
			want: "claude-desktop",
		},
		{
			name: "match via Version substring (defensive)",
			in:   mcp.Implementation{Name: "generic", Title: "Generic", Version: "opencode-0.5.0"},
			want: "opencode",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeClientName(tc.in)
			if got != tc.want {
				t.Errorf("NormalizeClientName(%+v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestStore_SetFromInitialize verifies the legacy-spec path.
func TestStore_SetFromInitialize(t *testing.T) {
	s := NewClientInfoStore()
	s.SetFromInitialize(&mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: "2025-06-18",
			ClientInfo: mcp.Implementation{
				Name:    "claude-desktop",
				Title:   "Claude Desktop",
				Version: "1.0.0",
			},
		},
	})

	got := s.Current()
	if got.Info.Name != "claude-desktop" {
		t.Errorf("Info.Name = %q, want %q", got.Info.Name, "claude-desktop")
	}
	if got.Source != "initialize.clientInfo" {
		t.Errorf("Source = %q, want %q", got.Source, "initialize.clientInfo")
	}
	if got.SpecVersion != "2025-06-18" {
		t.Errorf("SpecVersion = %q, want %q", got.SpecVersion, "2025-06-18")
	}
	if got.LastSeenUnixMill == 0 {
		t.Error("LastSeenUnixMill should be set after SetFromInitialize")
	}
}

// TestStore_SetFromMeta_TypedImplementation verifies the new-spec path
// with the strongly-typed Implementation value.
func TestStore_SetFromMeta_TypedImplementation(t *testing.T) {
	s := NewClientInfoStore()
	meta := &mcp.Meta{
		AdditionalFields: map[string]any{
			MetaClientInfoKey: mcp.Implementation{
				Name:    "opencode",
				Title:   "opencode",
				Version: "1.0.5",
			},
		},
	}
	if !s.SetFromMeta(meta) {
		t.Fatal("SetFromMeta returned false on typed Implementation")
	}

	got := s.Current()
	if got.Info.Name != "opencode" {
		t.Errorf("Info.Name = %q, want %q", got.Info.Name, "opencode")
	}
	if got.Source != "_meta.clientInfo" {
		t.Errorf("Source = %q, want %q", got.Source, "_meta.clientInfo")
	}
}

// TestStore_SetFromMeta_PointerImplementation verifies the *mcp.Implementation
// path (some SDKs may pass the pointer).
func TestStore_SetFromMeta_PointerImplementation(t *testing.T) {
	s := NewClientInfoStore()
	info := mcp.Implementation{Name: "claude-code", Title: "Claude Code", Version: "2.0.0"}
	meta := &mcp.Meta{
		AdditionalFields: map[string]any{
			MetaClientInfoKey: &info,
		},
	}
	if !s.SetFromMeta(meta) {
		t.Fatal("SetFromMeta returned false on pointer Implementation")
	}

	got := s.Current()
	if got.Info.Name != "claude-code" {
		t.Errorf("Info.Name = %q, want %q", got.Info.Name, "claude-code")
	}
}

// TestStore_SetFromMeta_MapImplementation verifies the defensive
// map[string]any decoding path (early 2026-07-28 SDKs may emit maps).
func TestStore_SetFromMeta_MapImplementation(t *testing.T) {
	s := NewClientInfoStore()
	meta := &mcp.Meta{
		AdditionalFields: map[string]any{
			MetaClientInfoKey: map[string]any{
				"name":    "cursor",
				"title":   "Cursor",
				"version": "0.40.0",
			},
		},
	}
	if !s.SetFromMeta(meta) {
		t.Fatal("SetFromMeta returned false on map[string]any Implementation")
	}

	got := s.Current()
	if got.Info.Name != "cursor" {
		t.Errorf("Info.Name = %q, want %q", got.Info.Name, "cursor")
	}
}

// TestStore_SetFromMeta_NoClientInfoKey verifies that meta bags
// without the clientInfo key return false (no change to store).
func TestStore_SetFromMeta_NoClientInfoKey(t *testing.T) {
	s := NewClientInfoStore()
	meta := &mcp.Meta{
		AdditionalFields: map[string]any{
			"unrelated-key": "some-value",
		},
	}
	if s.SetFromMeta(meta) {
		t.Error("SetFromMeta returned true on meta without clientInfo key")
	}
	// Current should still be the zero value.
	if got := s.Current(); got.Info.Name != "" {
		t.Errorf("Current().Info.Name = %q, want empty", got.Info.Name)
	}
}

// TestStore_SetFromMeta_NilMeta is defensive — never panic.
func TestStore_SetFromMeta_NilMeta(t *testing.T) {
	s := NewClientInfoStore()
	if s.SetFromMeta(nil) {
		t.Error("SetFromMeta(nil) returned true")
	}
}

// TestStore_LastWriteWins verifies that the most recent clientInfo
// overwrites earlier ones (the dual-spec path converges on the most
// recent observation).
func TestStore_LastWriteWins(t *testing.T) {
	s := NewClientInfoStore()
	// First write: legacy spec.
	s.SetFromInitialize(&mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ClientInfo: mcp.Implementation{Name: "claude-desktop", Version: "1.0.0"},
		},
	})
	if got := s.Current(); got.Source != "initialize.clientInfo" {
		t.Fatalf("after legacy set, Source = %q", got.Source)
	}
	// Second write: new spec. Should overwrite.
	meta := &mcp.Meta{
		AdditionalFields: map[string]any{
			MetaClientInfoKey: mcp.Implementation{Name: "opencode", Version: "2.0.0"},
		},
	}
	if !s.SetFromMeta(meta) {
		t.Fatal("SetFromMeta returned false")
	}
	got := s.Current()
	if got.Info.Name != "opencode" {
		t.Errorf("after new-spec set, Info.Name = %q, want %q", got.Info.Name, "opencode")
	}
	if got.Source != "_meta.clientInfo" {
		t.Errorf("after new-spec set, Source = %q, want %q", got.Source, "_meta.clientInfo")
	}
}

// TestGlobalStore_Singleton verifies the process-wide singleton is
// stable across calls (defensive — tests should not depend on
// singletons, but this is a public global so the invariant matters).
func TestGlobalStore_Singleton(t *testing.T) {
	a := globalClientInfoStore()
	b := globalClientInfoStore()
	if a != b {
		t.Error("globalClientInfoStore returned different instances on successive calls")
	}
}

// TestMetaPropagator_ExtractMeta_IsPassThrough verifies that our
// MetaPropagator does not consume or rewrite _meta fields (defensive
// against trace-context propagator collisions).
func TestMetaPropagator_ExtractMeta_IsPassThrough(t *testing.T) {
	p := &ClientInfoMetaPropagator{}
	meta := &mcp.Meta{
		AdditionalFields: map[string]any{
			"unrelated-key": "should-be-untouched",
		},
	}
	ctx := p.ExtractMeta(t.Context(), meta)
	if ctx == nil {
		t.Error("ExtractMeta returned nil context")
	}
	if meta.AdditionalFields["unrelated-key"] != "should-be-untouched" {
		t.Error("ExtractMeta modified the unrelated _meta field")
	}
}
