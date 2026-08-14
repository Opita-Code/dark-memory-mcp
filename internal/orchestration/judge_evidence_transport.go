// Package orchestration — judge_evidence_transport.go
//
// v2.16.0: T0 — Artifact Transport Contract.
//
// The "recap problem" surfaced in dark-testing skill v4 evaluations
// (eval 919) was caused by the judge receiving a *summary* of an
// artifact instead of its verbatim bytes. Downstream validation (T2)
// cannot fix this — by the time the Validator runs, the recap is
// already baked into the judge's prompt.
//
// T0 specifies the upstream contract that guarantees verbatim bytes
// arrive at the judge:
//
//  1. LoadArtifact reads the file once via io.ReadAll and computes
//     its SHA256 at load time.
//  2. MinArtifactBytes = 5_000 rejects anything smaller — the
//     dark-testing skill v4 SKILL.md is ~50KB; a 200-word recap is
//     ~1.6KB. The size guard catches this with 100% confidence.
//  3. MaxArtifactBytes = 10MB rejects for chunking (out of scope for
//     v2.16.0).
//  4. ReadLine(line) gives the judge (and Validator) verifiable
//     line references — T2 uses this to verify each cited quote.
//
// Invariants enforced:
//
//   - Recap guard: artifact < 5KB → error
//   - Byte equality: bytes read once, never re-read
//   - Hash trail: SHA256 in audit log (verifiable post-hoc)
//   - Line index: LineCount + ReadLine for evidence validation
//
// Backward compatibility: this package is ADDITIVE. Existing code
// paths that pass content as a string (judge.go JudgeInput.Content)
// continue to work unchanged. The Transport is used by NEW code that
// has access to the artifact file path (e.g., smoke tests).
package orchestration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// MinArtifactBytes is the floor for accepting an artifact. Anything
// smaller is treated as a recap and rejected.
const MinArtifactBytes = 5_000

// MaxArtifactBytes is the ceiling. Anything larger requires chunking
// (not implemented in v2.16.0).
const MaxArtifactBytes = 10 * 1024 * 1024

// ErrArtifactTooSmall indicates the artifact is below MinArtifactBytes
// and is likely a recap, not verbatim content.
var ErrArtifactTooSmall = errors.New("artifact too small (likely a recap)")

// ErrArtifactTooLarge indicates the artifact exceeds MaxArtifactBytes
// and requires chunking.
var ErrArtifactTooLarge = errors.New("artifact too large (chunking required)")

// SourceOrigin identifies where the artifact bytes came from.
type SourceOrigin string

const (
	SourceFilesystem SourceOrigin = "filesystem"
	// SourceURL would be added when we support URL-fetched artifacts.
	SourceURL SourceOrigin = "url"
	// SourceGit would be added for git-tracked artifacts.
	SourceGit SourceOrigin = "git"
)

// LoadedArtifact is the verbatim-loaded artifact with audit trail
// metadata. Returned by Transport.LoadArtifact.
type LoadedArtifact struct {
	Path         string      // source path or identifier
	Bytes        []byte      // verbatim bytes (read once, never mutated)
	Sha256Hex    string      // SHA256 of Bytes, computed at load time
	LineCount    int         // count of newlines + 1
	LoadedAt     time.Time   // when the load happened
	SourceOrigin SourceOrigin // where the bytes came from
}

// ArtifactReader is the interface the Validator (T2) uses to verify
// evidence quotes. *LoadedArtifact implements it.
type ArtifactReader interface {
	// ReadLine returns the exact content of `line` (1-indexed) in
	// the artifact. Returns an error if line is out of range.
	ReadLine(line int) (string, error)
	// Sha256 returns the hex-encoded SHA256 of the artifact bytes.
	Sha256() string
	// TotalLines returns the total number of lines in the artifact.
	TotalLines() int
}

// Transport loads artifacts verbatim from a source. Construct one
// per source (filesystem / URL / git) and reuse it across multiple
// LoadArtifact calls.
type Transport struct{}

// NewTransport returns a Transport. Stateless — safe for concurrent
// use.
func NewTransport() *Transport {
	return &Transport{}
}

// LoadArtifact reads the artifact at path verbatim from the
// filesystem. Returns a *LoadedArtifact or an error.
//
// Errors:
//   - os.PathError: file not found / unreadable
//   - ErrArtifactTooSmall: file < MinArtifactBytes
//   - ErrArtifactTooLarge: file > MaxArtifactBytes
func (t *Transport) LoadArtifact(path string) (*LoadedArtifact, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("transport: empty path")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("transport: open %q: %w", path, err)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("transport: read %q: %w", path, err)
	}

	if len(data) < MinArtifactBytes {
		return nil, fmt.Errorf("%w: %q is %d bytes (< MinArtifactBytes=%d)",
			ErrArtifactTooSmall, path, len(data), MinArtifactBytes)
	}
	if len(data) > MaxArtifactBytes {
		return nil, fmt.Errorf("%w: %q is %d bytes (> MaxArtifactBytes=%d)",
			ErrArtifactTooLarge, path, len(data), MaxArtifactBytes)
	}

	sum := sha256.Sum256(data)
	shaHex := hex.EncodeToString(sum[:])

	return &LoadedArtifact{
		Path:         path,
		Bytes:        data,
		Sha256Hex:    shaHex,
		LineCount:    bytes.Count(data, []byte("\n")) + 1,
		LoadedAt:     time.Now().UTC(),
		SourceOrigin: SourceFilesystem,
	}, nil
}

// ReadLine returns the exact content of `line` (1-indexed) in the
// loaded artifact. Implements ArtifactReader.
func (la *LoadedArtifact) ReadLine(line int) (string, error) {
	if line < 1 || line > la.LineCount {
		return "", fmt.Errorf("transport: line %d out of range [1, %d]", line, la.LineCount)
	}
	var current int
	start := 0
	for i, b := range la.Bytes {
		if b == '\n' {
			current++
			if current == line {
				return string(la.Bytes[start:i]), nil
			}
			start = i + 1
		}
	}
	// Last line (no trailing newline).
	if current+1 == line {
		return string(la.Bytes[start:]), nil
	}
	return "", fmt.Errorf("transport: line %d not found", line)
}

// Sha256 returns the hex-encoded SHA256 of the artifact bytes.
// Implements ArtifactReader.
func (la *LoadedArtifact) Sha256() string {
	return la.Sha256Hex
}

// TotalLines returns the total number of lines in the artifact.
// Implements ArtifactReader.
func (la *LoadedArtifact) TotalLines() int {
	return la.LineCount
}
