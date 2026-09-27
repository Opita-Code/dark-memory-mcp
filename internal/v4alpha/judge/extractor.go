// Package judge — Evidence Extractor (ADR-007 §2 step [2]).
//
// Pure deterministic function: pc.ArtifactContent -> []Evidence.
// NEVER calls the LLM. Reads source bytes literally.
//
// Per ADR-007 §1 F1 (doc-vs-code drift), the extractor is the
// attack surface for "the artifact claims X about file:line but the
// actual code says Y". By reading bytes literally (no summarization),
// the Pipeline guarantees that what the LLM sees in step [5] is
// what's actually in the artifact.
//
// Commit 1 scope:
//   - Chunk artifact content by paragraphs (default 4096 bytes/chunk)
//   - Score each chunk's Relevance heuristically (code-like > prose)
//   - No external file resolution (git_sha / url / spec_id / artifact_id
//     resolutions land in commit 2 alongside the LLM-backed Judge)
package judge

import (
	"context"
	"fmt"
	"strings"
)

// Extractor produces Evidence slices from raw artifact content.
// Stateless. Multiple Extract() calls with the same PipelineContext
// return byte-identical results.
type Extractor struct {
	// ChunkSize is the max bytes per chunk when splitting by
	// paragraph. Default 4096 (set by NewDefaultExtractor).
	ChunkSize int
}

// NewDefaultExtractor returns an Extractor with ChunkSize=4096.
func NewDefaultExtractor() *Extractor {
	return &Extractor{ChunkSize: 4096}
}

// Extract runs step [2] of the pipeline. Returns the list of
// Evidence for the LLM in step [5] (and for the ECs in step [3]).
//
// Empty ArtifactContent returns nil (no error). EC-001 fires in
// step [3] for the empty case; the extractor doesn't have to
// report it.
//
// Errors only happen on ctx cancellation or registry bugs
// (Personas/Rubrics being nil are tolerated; the extractor doesn't
// touch them).
func (e *Extractor) Extract(ctx context.Context, pc *PipelineContext) ([]Evidence, error) {
	if len(pc.ArtifactContent) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	chunkSize := e.ChunkSize
	if chunkSize <= 0 {
		chunkSize = 4096
	}

	chunks := chunkParagraphs(pc.ArtifactContent, chunkSize)
	out := make([]Evidence, 0, len(chunks))
	for i, c := range chunks {
		if len(strings.TrimSpace(string(c))) == 0 {
			continue
		}
		out = append(out, Evidence{
			Source:    fmt.Sprintf("artifact:chunk-%d", i),
			Snippet:   string(c),
			Relevance: relevanceFromSnippet(c),
		})
	}
	return out, nil
}

// chunkParagraphs splits content into chunks of at most maxSize
// bytes, preferring to split on paragraph boundaries (\n\n).
// Single-chunk fast path when content fits.
func chunkParagraphs(content []byte, maxSize int) [][]byte {
	if len(content) <= maxSize {
		// Return a defensive copy so callers can't mutate the input.
		out := make([]byte, len(content))
		copy(out, content)
		return [][]byte{out}
	}
	var chunks [][]byte
	for start := 0; start < len(content); {
		end := start + maxSize
		if end >= len(content) {
			out := make([]byte, len(content)-start)
			copy(out, content[start:])
			chunks = append(chunks, out)
			break
		}
		// Look for the last \n\n before end so chunks end on
		// paragraph boundaries.
		splitAt := -1
		for i := end; i > start+1; i-- {
			if content[i-1] == '\n' && content[i] == '\n' {
				splitAt = i + 1
				break
			}
		}
		if splitAt < 0 {
			splitAt = end
		}
		out := make([]byte, splitAt-start)
		copy(out, content[start:splitAt])
		chunks = append(chunks, out)
		start = splitAt
	}
	return chunks
}

// relevanceFromSnippet scores how evidence-worthy a chunk is.
// Heuristic: code-like patterns and file:line refs are more
// relevant than plain prose.
//
// The LLM in step [5] can override Relevance via its reasoning;
// this is just the prior.
func relevanceFromSnippet(b []byte) float64 {
	s := string(b)
	switch {
	case fileLineRe.MatchString(s):
		return 0.9 // explicit file:line claim
	case strings.Contains(s, "func ") || strings.Contains(s, "package "):
		return 0.8 // code chunk
	case strings.Contains(s, "TODO") || strings.Contains(s, "FIXME"):
		return 0.6 // marked-incomplete
	case strings.Contains(s, "```"):
		return 0.7 // fenced code block marker
	case strings.TrimSpace(s) == "":
		return 0.0
	default:
		return 0.5 // prose
	}
}
