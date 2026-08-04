// Package onnx — Embed() implementation + WordPiece tokenizer glue.

package onnx

import (
	"context"
	"fmt"
	"strings"

	onnxruntime "github.com/yalue/onnxruntime_go"

	"github.com/dark-agents/dark-memory-mcp/internal/embedder"
)

// Embed returns one Vec per input text. Batches at cfg.BatchSize for
// predictable CPU inference latency.
//
// The model's output (last_hidden_state, [batch, seq, 384]) is
// mean-pooled across the sequence with attention_mask as the mask
// weight, then L2-normalized — matching the all-MiniLM-L6-v2
// sentence-transformers reference (the standard recipe for SBERT
// mean-pool models).
func (a *onnxAdapter) Embed(ctx context.Context, texts []string) ([]embedder.Vec, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.closeMu.Lock()
	if a.closed {
		a.closeMu.Unlock()
		return nil, ErrSessionClosed
	}
	a.closeMu.Unlock()

	if len(texts) == 0 {
		return nil, nil
	}

	out := make([]embedder.Vec, 0, len(texts))
	for start := 0; start < len(texts); start += a.cfg.BatchSize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := start + a.cfg.BatchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[start:end]
		vecs, err := a.embedBatch(ctx, batch)
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

// embedBatch runs one ONNX session call for the given batch and
// returns mean-pooled + L2-normalized vectors.
func (a *onnxAdapter) embedBatch(ctx context.Context, inputs []string) ([]embedder.Vec, error) {
	_ = ctx // yalue/onnxruntime_go does not take a context; cancellation
	// is honored at the loop boundary above.

	batchSize := len(inputs)
	maxLen := a.cfg.MaxSeqLen

	// 1. Tokenize each input into flat [B*L] slices.
	inputIDs := make([]int64, 0, batchSize*maxLen)
	attnMask := make([]int64, 0, batchSize*maxLen)
	tokenType := make([]int64, 0, batchSize*maxLen)
	for _, txt := range inputs {
		ids, mask := a.tokenize(txt, maxLen)
		tt := make([]int64, len(ids))
		inputIDs = append(inputIDs, ids...)
		attnMask = append(attnMask, mask...)
		tokenType = append(tokenType, tt...)
	}

	// 2. Wrap in [B, L] int64 tensors.
	shape := onnxruntime.NewShape(int64(batchSize), int64(maxLen))
	inIDsTensor, err := onnxruntime.NewTensor[int64](shape, inputIDs)
	if err != nil {
		return nil, fmt.Errorf("embedder.onnx: input_ids: %w", err)
	}
	defer inIDsTensor.Destroy()
	inMaskTensor, err := onnxruntime.NewTensor[int64](shape, attnMask)
	if err != nil {
		return nil, fmt.Errorf("embedder.onnx: attention_mask: %w", err)
	}
	defer inMaskTensor.Destroy()
	inTypeTensor, err := onnxruntime.NewTensor[int64](shape, tokenType)
	if err != nil {
		return nil, fmt.Errorf("embedder.onnx: token_type_ids: %w", err)
	}
	defer inTypeTensor.Destroy()

	// 3. Run. Outputs is nil → yalue auto-allocates the output tensor.
	inputs_ := []onnxruntime.Value{inIDsTensor, inMaskTensor, inTypeTensor}
	outputs := []onnxruntime.Value{nil}
	if err := a.session.Run(inputs_, outputs); err != nil {
		return nil, fmt.Errorf("embedder.onnx: run: %w", err)
	}
	outTensor, ok := outputs[0].(*onnxruntime.Tensor[float32])
	if !ok || outTensor == nil {
		return nil, fmt.Errorf("embedder.onnx: nil or wrong-type output")
	}
	defer outTensor.Destroy()

	hidden := outTensor.GetData()
	if hidden == nil {
		return nil, fmt.Errorf("embedder.onnx: nil output data")
	}

	// 4. Mean-pool + L2-normalize. Output shape: [B, L, 384] float32.
	out := make([]embedder.Vec, batchSize)
	for b := 0; b < batchSize; b++ {
		vec := meanPool(hidden, b*maxLen*DefaultDim, maxLen, attnMask[b*maxLen:(b+1)*maxLen], DefaultDim)
		vec = l2Normalize(vec)
		out[b] = vec
	}
	return out, nil
}

// meanPool computes mean(hidden_state[b], mask=attnMask).
// Returns a fresh slice of length dim.
func meanPool(hidden []float32, offset int, seqLen int, mask []int64, dim int) embedder.Vec {
	out := make([]float32, dim)
	var sumWeight float32
	for t := 0; t < seqLen; t++ {
		if mask[t] == 0 {
			continue
		}
		w := float32(1.0)
		sumWeight += w
		base := offset + t*dim
		for d := 0; d < dim; d++ {
			out[d] += hidden[base+d] * w
		}
	}
	if sumWeight == 0 {
		return out // all-zero input → all-zero output (rare; degenerate)
	}
	inv := 1 / sumWeight
	for d := 0; d < dim; d++ {
		out[d] *= inv
	}
	return out
}

// l2Normalize returns a fresh slice with unit L2 norm. Zero vectors
// pass through unchanged.
func l2Normalize(v embedder.Vec) embedder.Vec {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	inv := 1.0 / sqrt(sum)
	out := make(embedder.Vec, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) * inv)
	}
	return out
}

// sqrt is a tiny Newton-iterated sqrt to avoid pulling in math.Sqrt's
// call overhead on the hot path. Inputs are non-negative.
func sqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 8; i++ {
		z = 0.5 * (z + x/z)
	}
	return z
}

// tokenize runs the basic tokenizer + WordPiece for one input and
// returns input_ids and attention_mask padded/truncated to maxLen.
//
// [CLS] is prepended, [SEP] is appended (BERT convention). The mask
// is 1 for real tokens, 0 for [PAD].
func (a *onnxAdapter) tokenize(text string, maxLen int) (inputIDs, attnMask []int64) {
	// Reserve 2 slots for [CLS] + [SEP].
	const specialBudget = 2
	budget := maxLen - specialBudget
	if budget < 1 {
		budget = 1
	}

	// 1. Basic tokenize → words.
	words := basicTokenize(text)

	// 2. WordPiece each word → subword IDs.
	ids := make([]int64, 0, len(words)+specialBudget)
	ids = append(ids, int64(a.idCLS))
	for _, w := range words {
		if len(ids) >= maxLen-1 {
			break
		}
		for _, id := range a.wordPieceIDs(w) {
			if len(ids) >= maxLen-1 {
				break
			}
			ids = append(ids, int64(id))
		}
	}
	ids = append(ids, int64(a.idSEP))

	// 3. Pad.
	mask := make([]int64, maxLen)
	for i, id := range ids {
		inputIDs = append(inputIDs, id)
		mask[i] = 1
		if len(inputIDs) >= maxLen {
			break
		}
	}
	for len(inputIDs) < maxLen {
		inputIDs = append(inputIDs, int64(a.idPAD))
	}
	return inputIDs, mask
}

// basicTokenize implements the BERT-style basic tokenizer: lowercased,
// split on whitespace and punctuation, strips accents.
//
// This is intentionally minimal. It is NOT a full CJK-aware
// tokenizer; for non-Latin scripts, the all-MiniLM-L6-v2 model is
// itself limited (the SBERT reference recommends other models for
// multilingual use). Operators wanting CJK / Arabic / Cyrillic
// embeddings should switch to a multilingual embedder (e.g., the
// voyage-3 adapter for Claude harness, or a custom endpoint).
func basicTokenize(text string) []string {
	text = strings.ToLower(text)
	// Strip common diacritics minimally (NFKD would be more correct
	// but adds an import).
	text = stripAccents(text)

	// Split on whitespace + punctuation, keeping the punctuation as
	// its own token. BERT convention: "don't" → ["don", "'", "t"].
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range text {
		switch {
		case isWhitespace(r):
			flush()
		case isPunctuation(r):
			flush()
			out = append(out, string(r))
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

func isWhitespace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r':
		return true
	}
	return false
}

func isPunctuation(r rune) bool {
	// ASCII punctuation that BERT treats as separators. CJK,
	// Arabic, and other non-Latin punctuation falls through.
	switch {
	case r >= 33 && r <= 47,
		r >= 58 && r <= 64,
		r >= 91 && r <= 96,
		r >= 123 && r <= 126:
		return true
	}
	return false
}

// stripAccents removes combining diacritics by dropping marks in the
// Mn (Mark, Nonspacing) Unicode category. Minimal but sufficient
// for Western scripts. For absolute fidelity, import golang.org/x/text/unicode/norm.
func stripAccents(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		// Quick mnemonic: Mn is 0x0300..0x036F. This skips the
		// full unicode table while covering Latin/Cyrillic/Greek
		// accents which are the common cases.
		if r >= 0x0300 && r <= 0x036F {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// wordPieceIDs runs greedy longest-match-first WordPiece against the
// loaded vocab. Returns one or more token IDs (subwords). If any
// subword fails to match, the whole word falls back to [UNK] (BERT's
// official behavior — avoids mid-word UNK pollution that hurts
// downstream attention).
//
// Algorithm: for input "running" with vocab {run, ##ning, run, …},
// we try "running" → miss → "runnin" → miss → "runni" → miss → "runn"
// → miss → "run" → hit, advance past "run", try "ning" with prefix
// "##" → "##ning" → hit. Returns [run_id, ##ning_id].
func (a *onnxAdapter) wordPieceIDs(word string) []int {
	if id, ok := a.vocab.id[word]; ok {
		return []int{id}
	}
	var out []int
	start := 0
	for start < len(word) {
		end := len(word)
		hit := -1
		for end > start {
			sub := word[start:end]
			if start > 0 {
				sub = "##" + sub
			}
			if id, ok := a.vocab.id[sub]; ok {
				hit = id
				break
			}
			// Reduce one trailing rune (UTF-8 safe).
			_, sz := utf8LastRuneLen(word[start:end])
			end -= sz
		}
		if hit < 0 {
			// Whole-word UNK fallback (BERT convention).
			return []int{a.idUNK}
		}
		out = append(out, hit)
		start = end
	}
	return out
}

// utf8LastRuneLen returns the byte length of the last rune in s.
// Used by wordPiece to shrink the candidate window one rune at a
// time. Avoids the strings/utf8 import on the hot path.
func utf8LastRuneLen(s string) (rune, int) {
	if len(s) == 0 {
		return 0, 0
	}
	b := s[len(s)-1]
	if b < 0x80 {
		return rune(b), 1
	}
	// Multi-byte tail. Walk back to the leading byte.
	for i := len(s) - 1; i >= 0 && i > len(s)-5; i-- {
		bb := s[i]
		if bb&0xC0 != 0x80 {
			// Found leading byte.
			size := 0
			switch {
			case bb&0xE0 == 0xC0:
				size = 2
			case bb&0xF0 == 0xE0:
				size = 3
			case bb&0xF8 == 0xF0:
				size = 4
			}
			if len(s)-i == size {
				r, _ := decodeRune([]byte(s[i:]))
				return r, size
			}
			return 0, 1
		}
	}
	return 0, 1
}

// decodeRune is a minimal UTF-8 decoder for the multi-byte tail.
// We only need the rune value to advance the window; we do NOT
// emit the rune anywhere.
func decodeRune(s []byte) (rune, int) {
	if len(s) == 0 {
		return 0, 0
	}
	b := s[0]
	switch {
	case b < 0x80:
		return rune(b), 1
	case b&0xE0 == 0xC0:
		if len(s) < 2 {
			return 0, 1
		}
		return rune(b&0x1F)<<6 | rune(s[1]&0x3F), 2
	case b&0xF0 == 0xE0:
		if len(s) < 3 {
			return 0, 1
		}
		return rune(b&0x0F)<<12 | rune(s[1]&0x3F)<<6 | rune(s[2]&0x3F), 3
	case b&0xF8 == 0xF0:
		if len(s) < 4 {
			return 0, 1
		}
		return rune(b&0x07)<<18 | rune(s[1]&0x3F)<<12 | rune(s[2]&0x3F)<<6 | rune(s[3]&0x3F), 4
	}
	return 0, 1
}

// vocab is the loaded token → id map. Reverse map (id → token) is
// not currently needed; reserved for future token diagnostics.
type vocab struct {
	id map[string]int
}

// loadVocab parses a BERT-format vocab.txt (one token per line).
// Returns an error if any line is empty or duplicated.
func loadVocab(b []byte) (vocab, error) {
	v := vocab{id: make(map[string]int, 32000)}
	line := 0
	start := 0
	for i := 0; i <= len(b); i++ {
		if i == len(b) || b[i] == '\n' {
			// Skip the synthetic empty line that comes from a
			// trailing newline (vocab.txt always has one). The
			// inner check `tok == ""` still catches truly empty
			// middle lines.
			if i == len(b) && start == i {
				break
			}
			tok := string(b[start:i])
			// Trim trailing \r for CRLF files.
			if len(tok) > 0 && tok[len(tok)-1] == '\r' {
				tok = tok[:len(tok)-1]
			}
			if tok == "" {
				return v, fmt.Errorf("embedder.onnx: vocab line %d empty", line)
			}
			if _, dup := v.id[tok]; dup {
				return v, fmt.Errorf("embedder.onnx: vocab duplicate %q at line %d", tok, line)
			}
			v.id[tok] = line
			line++
			start = i + 1
		}
	}
	return v, nil
}
