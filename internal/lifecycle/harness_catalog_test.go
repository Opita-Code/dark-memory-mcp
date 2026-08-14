package lifecycle

import "testing"

func TestLookupHarnessNative_AllKnownHarnesses(t *testing.T) {
	cases := []struct {
		canonical     string
		wantFamily    string
		wantRung      HarnessRung
		wantCanWrite  bool
		wantModelZero string // first model in NativeModels, or "" if empty
	}{
		{"opencode", "minimax", RungMedium, true, "MiniMax-M3"},
		{"claude-code", "anthropic", RungMedium, true, "claude-sonnet-4.5"},
		{"claude-desktop", "anthropic", RungMedium, false, "claude-sonnet-4.5"},
		{"claude-family", "anthropic", RungMedium, false, "claude-sonnet-4.5"},
		{"codex", "openai", RungMedium, true, "gpt-5"},
		{"cursor", "anthropic", RungMedium, true, "claude-sonnet-4.5"},
		{"continue", "multi", RungMedium, true, ""},
		{"cline", "multi", RungMedium, true, ""},
		{"unknown", "unknown", RungUnknown, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.canonical, func(t *testing.T) {
			got := LookupHarnessNative(tc.canonical)
			if got.Family != tc.wantFamily {
				t.Errorf("Family = %q, want %q", got.Family, tc.wantFamily)
			}
			if got.Rung != tc.wantRung {
				t.Errorf("Rung = %q, want %q", got.Rung, tc.wantRung)
			}
			if got.CanWriteConfig != tc.wantCanWrite {
				t.Errorf("CanWriteConfig = %v, want %v", got.CanWriteConfig, tc.wantCanWrite)
			}
			if len(got.NativeModels) > 0 && got.NativeModels[0] != tc.wantModelZero {
				t.Errorf("NativeModels[0] = %q, want %q", got.NativeModels[0], tc.wantModelZero)
			}
			if len(got.NativeModels) == 0 && tc.wantModelZero != "" {
				t.Errorf("NativeModels is empty, want first = %q", tc.wantModelZero)
			}
		})
	}
}

func TestLookupHarnessNative_UnknownFallback(t *testing.T) {
	// unknown canonical names should fall through to the "unknown" entry
	cases := []string{"", "some-bogus-harness", "claude-anything-else"}
	for _, c := range cases {
		t.Run("fallback_"+c, func(t *testing.T) {
			got := LookupHarnessNative(c)
			if got.Family != "unknown" {
				t.Errorf("LookupHarnessNative(%q).Family = %q, want %q", c, got.Family, "unknown")
			}
			if got.Rung != RungUnknown {
				t.Errorf("LookupHarnessNative(%q).Rung = %q, want %q", c, got.Rung, RungUnknown)
			}
		})
	}
}

func TestLookupHarnessNative_OmittedModelIsEmpty(t *testing.T) {
	// continue and cline are multi-provider harnesses; their NativeModels
	// is intentionally empty.
	for _, c := range []string{"continue", "cline"} {
		got := LookupHarnessNative(c)
		if len(got.NativeModels) != 0 {
			t.Errorf("LookupHarnessNative(%q).NativeModels should be empty, got %v", c, got.NativeModels)
		}
	}
}

func TestLookupHarnessNative_ConfigPathOnlyWhenCanWrite(t *testing.T) {
	// CanWriteConfig=true means ConfigPath is non-empty (or at least
	// populated for the entry). CanWriteConfig=false means ConfigPath is "".
	for name, hn := range harnessCatalog {
		if hn.CanWriteConfig && hn.ConfigPath == "" {
			t.Errorf("%s: CanWriteConfig=true but ConfigPath is empty", name)
		}
		if !hn.CanWriteConfig && hn.ConfigPath != "" {
			t.Errorf("%s: CanWriteConfig=false but ConfigPath=%q", name, hn.ConfigPath)
		}
	}
}
