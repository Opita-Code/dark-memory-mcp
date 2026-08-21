// Package artifact resolves ArtifactRefs into concrete bytes the judge
// pipeline can NLI-anchor against. The judge never accepts raw text
// from the caller; every evaluation goes through Resolve which
// produces a SHA-256 over what the resolver actually read.
//
// Source of truth for the artifact model is the spec 1276 (v2.20.0):
//   - file/git_sha/url/spec_id/artifact_id are the canonical kinds.
//   - Caller-supplied inline text is handled by MaterializeFromText
//     (T03), which writes to $DARK_DATA_DIR/materialized/ and returns
//     an ArtifactRef{kind: "file"} pointing at the dark-memory-owned
//     file. The caller cannot tamper post-write.
//
// Hard constraints enforced by tests:
//   - H2: NLI happens after Resolve, never on caller-supplied bytes.
//   - H4: materialized paths must live inside $DARK_DATA_DIR.
//   - H5: URL fetching is guarded by an SSRFGuard interface; T02 owns
//     the production implementation.
package artifact

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

const (
	// DefaultMaxBytes is the soft cap for Resolve output (256 KiB).
	// Tunable via ArtifactRef.MaxBytes (H-configurable).
	DefaultMaxBytes = 256 * 1024

	// HardMaxBytes is the absolute ceiling (4 MiB). Resolves above
	// this are rejected regardless of MaxBytes. Prevents DoS via
	// caller-supplied refs.
	HardMaxBytes = 4 * 1024 * 1024
)

// ArtifactRefKind enumerates the supported resolution modes.
type ArtifactRefKind string

const (
	KindFile       ArtifactRefKind = "file"
	KindGitSHA     ArtifactRefKind = "git_sha"
	KindURL        ArtifactRefKind = "url"
	KindSpecID     ArtifactRefKind = "spec_id"
	KindArtifactID ArtifactRefKind = "artifact_id"
)

// SourceLabel describes the provenance of the resolved bytes. Persisted
// in judgment_history.artifact_source for audit queries.
type SourceLabel string

const (
	SourceFile               SourceLabel = "file"
	SourceGitSHA             SourceLabel = "git_sha"
	SourceURL                SourceLabel = "url"
	SourceSpecID             SourceLabel = "spec_id"
	SourceArtifactID         SourceLabel = "artifact_id"
	SourceMaterializedInline SourceLabel = "materialized_inline"
)

// Range optionally restricts the resolved content to a byte window
// within the underlying source. Both bounds are inclusive; End=0 means
// "to EOF". When Range is non-nil, Truncated=true is forced.
type Range struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// ArtifactRef is the canonical pointer used by the judge pipeline to
// locate content. JSON-compatible for ease of MCP serialization.
//
// Exactly one of (Path | URL | SpecID | ArtifactID) is meaningful per
// Kind; Validate() enforces this. The optional Range and MaxBytes
// bound the output; both default conservatively.
type ArtifactRef struct {
	Kind       ArtifactRefKind `json:"kind"`
	Path       string          `json:"path,omitempty"`
	GitRepo    string          `json:"git_repo,omitempty"`
	GitSHA     string          `json:"git_sha,omitempty"`
	URL        string          `json:"url,omitempty"`
	SpecID     int64           `json:"spec_id,omitempty"`
	ArtifactID int64           `json:"artifact_id,omitempty"`
	Range      *Range          `json:"range,omitempty"`
	MaxBytes   int             `json:"max_bytes,omitempty"`
}

// Resolved is the output of Resolve: the bytes the judge will see,
// their cryptographic hash, and the provenance label that downstream
// code uses in audit logs.
type Resolved struct {
	Bytes         []byte
	ContentSHA256 [32]byte
	Source        SourceLabel
	Path          string // canonical identifier: file path / git-sha:path / URL / spec_id:N / artifact_id:N
	Truncated     bool   // true if Range or MaxBytes cut off the content
}

// SpecLookup is the subset of store.Store needed for spec_id and
// artifact_id resolution. Defined as an interface so resolver_test.go
// can inject a mock; production wiring happens in T08 (judge.go).
type SpecLookup interface {
	GetSpecText(ctx context.Context, id int64) (string, error)
	GetArtifactURL(ctx context.Context, id int64) (string, error)
}

// URLFetcher fetches URL content. T02 (ssrf.go) will provide the
// production SSRF-guarded implementation; T01 stubs it via this
// interface so unit tests don't need a real http client.
type URLFetcher interface {
	Fetch(ctx context.Context, url string, maxBytes int) ([]byte, error)
}

// Resolver resolves ArtifactRefs into Resolved content. Inject
// SpecLookup and URLFetcher via struct fields; nil-safe (returns
// ErrUnresolved for kinds that need them).
type Resolver struct {
	Spec SpecLookup
	URLs URLFetcher
}

// Sentinel errors. Callers use errors.Is to classify failures.
var (
	ErrInvalidKind = errors.New("artifact: invalid kind")
	ErrEmptyPath   = errors.New("artifact: empty path or identifier")
	ErrZeroID      = errors.New("artifact: spec_id / artifact_id must be > 0")
	ErrSizeExceeded = errors.New("artifact: size exceeds hard cap")
	ErrUnresolved  = errors.New("artifact: unresolved")
	ErrNotConfigured = errors.New("artifact: required dependency not configured")
)

// Validate checks that an ArtifactRef is well-formed. Cheap; called by
// Resolve but also exposed so callers can pre-flight.
func (r ArtifactRef) Validate() error {
	switch r.Kind {
	case KindFile:
		if r.Path == "" {
			return fmt.Errorf("%w: file kind requires Path", ErrEmptyPath)
		}
	case KindGitSHA:
		if r.Path == "" || r.GitSHA == "" {
			return fmt.Errorf("%w: git_sha kind requires Path and GitSHA", ErrEmptyPath)
		}
	case KindURL:
		if r.URL == "" {
			return fmt.Errorf("%w: url kind requires URL", ErrEmptyPath)
		}
	case KindSpecID:
		if r.SpecID <= 0 {
			return fmt.Errorf("%w: spec_id", ErrZeroID)
		}
	case KindArtifactID:
		if r.ArtifactID <= 0 {
			return fmt.Errorf("%w: artifact_id", ErrZeroID)
		}
	case "":
		return fmt.Errorf("%w: Kind is empty", ErrInvalidKind)
	default:
		return fmt.Errorf("%w: %q", ErrInvalidKind, r.Kind)
	}
	return nil
}

// effectiveMaxBytes returns the configured cap, the default, or the
// hard cap — in that priority order. Tunable via ArtifactRef.MaxBytes.
func (r ArtifactRef) effectiveMaxBytes() int {
	if r.MaxBytes <= 0 {
		return DefaultMaxBytes
	}
	if r.MaxBytes > HardMaxBytes {
		return HardMaxBytes
	}
	return r.MaxBytes
}

// Resolve reads the artifact, computes its SHA-256, and returns the
// provenance label. The returned Resolved.Bytes is bounded by MaxBytes
// (default 256 KiB, hard cap 4 MiB). When a Range is present, the
// returned bytes are restricted to that window and Truncated=true.
//
// SHA-256 is computed over the bytes returned — i.e. the windowed
// slice, not the underlying source. This is what the judge will see.
func (r *Resolver) Resolve(ctx context.Context, ref ArtifactRef) (*Resolved, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	if err := ref.checkRangeFitsCap(); err != nil {
		return nil, err
	}
	cap := ref.effectiveMaxBytes()

	switch ref.Kind {
	case KindFile:
		return r.resolveFile(ctx, ref, cap)
	case KindGitSHA:
		return r.resolveGit(ctx, ref, cap)
	case KindURL:
		return r.resolveURL(ctx, ref, cap)
	case KindSpecID:
		return r.resolveSpecID(ctx, ref, cap)
	case KindArtifactID:
		return r.resolveArtifactID(ctx, ref, cap)
	default:
		return nil, fmt.Errorf("%w: %q", ErrInvalidKind, ref.Kind)
	}
}

// checkRangeFitsCap rejects ranges that exceed the byte cap. Open-ended
// ranges (End=0) are bounded by Start+MaxBytes.
func (r ArtifactRef) checkRangeFitsCap() error {
	if r.Range == nil {
		return nil
	}
	start := r.Range.Start
	end := r.Range.End
	cap := int64(r.effectiveMaxBytes())
	if end == 0 {
		end = start + cap
	}
	if start < 0 {
		start = 0
	}
	if end-start > cap {
		return fmt.Errorf("%w: range size %d exceeds cap %d", ErrSizeExceeded, end-start, cap)
	}
	return nil
}

// applyRange restricts bytes to [start, end) if Range is set. Returns
// the windowed slice and a truncated flag. end=0 means "to EOF".
func applyRange(b []byte, rng *Range) ([]byte, bool) {
	if rng == nil {
		return b, false
	}
	start := rng.Start
	end := rng.End
	if end == 0 || end > int64(len(b)) {
		end = int64(len(b))
	}
	if start < 0 {
		start = 0
	}
	if start > end {
		start = end
	}
	return b[start:end], true
}

// readLimitFor returns the number of source bytes the resolver needs
// to read to satisfy both MaxBytes and Range. Always at least cap+1
// (to detect overflow); expands to Range.End+1 if a Range is set.
func readLimitFor(cap int, rng *Range) int64 {
	limit := int64(cap) + 1
	if rng != nil && rng.End > 0 {
		if int64(rng.End)+1 > limit {
			limit = int64(rng.End) + 1
		}
	}
	return limit
}

// resolveFile reads a file from disk with a hard cap. Read order:
//  1. Read up to max(cap+1, Range.End+1) to satisfy both limits.
//  2. Apply Range if set (windowing the source).
//  3. Truncate to cap if the result still exceeds MaxBytes.
//
// io.LimitReader caps the read at the byte level, so a maliciously
// huge file can't allocate unbounded memory.
func (r *Resolver) resolveFile(ctx context.Context, ref ArtifactRef, cap int) (*Resolved, error) {
	f, err := os.Open(ref.Path)
	if err != nil {
		return nil, fmt.Errorf("%w: open %q: %v", ErrUnresolved, ref.Path, err)
	}
	defer f.Close()

	bytes, err := io.ReadAll(io.LimitReader(f, readLimitFor(cap, ref.Range)))
	if err != nil {
		return nil, fmt.Errorf("%w: read %q: %v", ErrUnresolved, ref.Path, err)
	}

	windowed, ranged := applyRange(bytes, ref.Range)
	truncated := ranged
	if len(windowed) > cap {
		windowed = windowed[:cap]
		truncated = true
	}

	sum := sha256.Sum256(windowed)
	return &Resolved{
		Bytes:         windowed,
		ContentSHA256: sum,
		Source:        SourceFile,
		Path:          ref.Path,
		Truncated:     truncated,
	}, nil
}

// resolveGit uses `git cat-file -p <sha>:<path>` to read a file at a
// pinned commit. The git binary is assumed present in PATH (most dev
// environments have it; production SST deploys include it). We never
// touch the working tree because that's mutable.
func (r *Resolver) resolveGit(ctx context.Context, ref ArtifactRef, cap int) (*Resolved, error) {
	cmd := exec.CommandContext(ctx, "git", "cat-file", "-p", ref.GitSHA+":"+ref.Path)
	if ref.GitRepo != "" {
		cmd.Dir = ref.GitRepo
	}
	cw := &capWriter{cap: int(readLimitFor(cap, ref.Range))}
	cmd.Stdout = cw
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: git cat-file %s:%s: %v (stderr: %s)",
			ErrUnresolved, ref.GitSHA, ref.Path, err, stderr.String())
	}

	windowed, ranged := applyRange(cw.buf, ref.Range)
	truncated := ranged
	if len(windowed) > cap {
		windowed = windowed[:cap]
		truncated = true
	}

	sum := sha256.Sum256(windowed)
	return &Resolved{
		Bytes:         windowed,
		ContentSHA256: sum,
		Source:        SourceGitSHA,
		Path:          ref.GitSHA + ":" + ref.Path,
		Truncated:     truncated,
	}, nil
}

// resolveURL delegates to the injected URLFetcher (which T02 will
// implement with SSRF guards). Returns ErrNotConfigured if no fetcher
// is wired (helps tests; production wiring in T08).
func (r *Resolver) resolveURL(ctx context.Context, ref ArtifactRef, cap int) (*Resolved, error) {
	if r.URLs == nil {
		return nil, fmt.Errorf("%w: URLFetcher", ErrNotConfigured)
	}
	bytes, err := r.URLs.Fetch(ctx, ref.URL, int(readLimitFor(cap, ref.Range)))
	if err != nil {
		return nil, fmt.Errorf("%w: fetch %q: %v", ErrUnresolved, ref.URL, err)
	}
	windowed, ranged := applyRange(bytes, ref.Range)
	truncated := ranged
	if len(windowed) > cap {
		windowed = windowed[:cap]
		truncated = true
	}

	sum := sha256.Sum256(windowed)
	return &Resolved{
		Bytes:         windowed,
		ContentSHA256: sum,
		Source:        SourceURL,
		Path:          ref.URL,
		Truncated:     truncated,
	}, nil
}

// resolveSpecID looks up the spec text from the store and uses it as
// the artifact content. For specs, the spec_intent IS the content
// being judged — there is no separate artifact URL.
func (r *Resolver) resolveSpecID(ctx context.Context, ref ArtifactRef, cap int) (*Resolved, error) {
	if r.Spec == nil {
		return nil, fmt.Errorf("%w: SpecLookup", ErrNotConfigured)
	}
	text, err := r.Spec.GetSpecText(ctx, ref.SpecID)
	if err != nil {
		return nil, fmt.Errorf("%w: spec_id=%d: %v", ErrUnresolved, ref.SpecID, err)
	}
	bytes := []byte(text)
	windowed, ranged := applyRange(bytes, ref.Range)
	truncated := ranged
	if len(windowed) > cap {
		windowed = windowed[:cap]
		truncated = true
	}

	sum := sha256.Sum256(windowed)
	return &Resolved{
		Bytes:         windowed,
		ContentSHA256: sum,
		Source:        SourceSpecID,
		Path:          fmt.Sprintf("spec_id:%d", ref.SpecID),
		Truncated:     truncated,
	}, nil
}

// resolveArtifactID looks up the artifact's URL from the store and
// delegates to resolveURL. Two-step because the DB stores metadata
// (URL, type, brand_id), not the content itself.
func (r *Resolver) resolveArtifactID(ctx context.Context, ref ArtifactRef, cap int) (*Resolved, error) {
	if r.Spec == nil {
		return nil, fmt.Errorf("%w: SpecLookup (needed for artifact_id URL)", ErrNotConfigured)
	}
	if r.URLs == nil {
		return nil, fmt.Errorf("%w: URLFetcher (needed for artifact URL fetch)", ErrNotConfigured)
	}
	url, err := r.Spec.GetArtifactURL(ctx, ref.ArtifactID)
	if err != nil {
		return nil, fmt.Errorf("%w: artifact_id=%d: %v", ErrUnresolved, ref.ArtifactID, err)
	}
	if url == "" {
		return nil, fmt.Errorf("%w: artifact_id=%d has no URL", ErrUnresolved, ref.ArtifactID)
	}
	return r.resolveURL(ctx, ArtifactRef{
		Kind:     KindURL,
		URL:      url,
		Range:    ref.Range,
		MaxBytes: ref.MaxBytes,
	}, cap)
}

// capWriter caps the bytes written into buf to `cap`. Used as stdout
// for git cat-file so a maliciously-large git blob can't allocate
// unbounded memory.
type capWriter struct {
	cap int
	buf []byte
}

// Write accumulates up to `cap` bytes into buf. Excess bytes are
// "consumed" (return full length, no error) so the underlying process
// doesn't block on a full pipe, but we don't grow the buffer.
func (w *capWriter) Write(p []byte) (int, error) {
	remaining := w.cap - len(w.buf)
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		w.buf = append(w.buf, p[:remaining]...)
		return len(p), nil
	}
	w.buf = append(w.buf, p...)
	return len(p), nil
}