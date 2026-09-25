package research

import (
	"testing"

	"pgregory.net/rapid"
)

func TestProperty_CleanProseAlwaysAllows(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		text := rapid.StringMatching(`[A-Za-z0-9 .,;:\-]{1,80}`).Draw(t, "text")
		b, f := ScanContent(text)
		// The generator alphabet cannot produce multi-word patterns, so
		// any hit here is a false positive by construction.
		if len(b) != 0 {
			t.Fatalf("false block on %q: %v", text, b)
		}
		_ = f
	})
}

func TestProperty_BlockPatternAlwaysBlocks(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := rapid.SampledFrom(blockPatterns).Draw(t, "pattern")
		pad := rapid.StringMatching(`[A-Za-z0-9 .]{0,20}`).Draw(t, "pad")
		b, _ := ScanContent(pad + " " + p + " " + pad)
		if len(b) == 0 {
			t.Fatalf("pattern %q not detected", p)
		}
		if VerdictFor(b, nil) != VerdictBlock {
			t.Fatalf("pattern %q not block verdict", p)
		}
	})
}

func TestProperty_FlagPatternNeverBlocksAlone(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := rapid.SampledFrom(flagPatterns).Draw(t, "pattern")
		b, f := ScanContent("prefix " + p + " suffix")
		if len(b) != 0 {
			t.Fatalf("flag pattern %q blocked: %v", p, b)
		}
		if len(f) == 0 {
			t.Fatalf("flag pattern %q not detected", p)
		}
	})
}

func TestProperty_GateMatchesDirectScan(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		r := validResult()
		r.Items[0].Snippet = rapid.StringMatching(`[A-Za-z0-9 .,;:\-]{1,60}`).Draw(t, "snippet")
		v, red := Gate(r)
		b, f := ScanContent(red.Items[0].Snippet)
		if VerdictFor(b, f) != v {
			t.Fatalf("gate %v != direct %v", v, VerdictFor(b, f))
		}
	})
}
