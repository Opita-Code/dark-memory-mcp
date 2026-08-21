package artifact

// Materialize shim (T03 of spec 1276 v2.20.0). Bridges the deprecation
// ladder from "caller passes Content string" to "ArtifactRef + Resolve":
//
//   v2.20.0 — caller passes Content → shim writes to disk, returns
//             ArtifactRef{Kind: KindFile}. Judge pipeline still sees a
//             SHA-256-anchored file, not caller-controlled words.
//   v2.21.0 — caller passes Content → judge verdict = needs_human.
//   v2.22.0 — Content field removed from Artifact entirely.
//
// The shim is the bridge. The Materialize function is its public API.
//
// Design:
//
//   - Materializer writes text to a content-addressed file. Path is
//     <BaseDir>/<sanitized-sourceTag>/<sha256>.txt. The SHA matches
//     what Resolver.Resolve will compute when it reads the file back.
//   - Writes are atomic: temp file in same dir + os.Rename. Readers
//     never see partial content.
//   - Idempotent: same text + same sourceTag → no rewrite, returns the
//     existing ArtifactRef. Concurrent callers race safely; the
//     rename-fail-but-content-equal fallback accepts a peer's file.
//   - File permissions are 0600 (text may carry secrets). Dir is 0700.
//   - sourceTag is sanitized: path separators and ".." segments are
//     neutralized to prevent traversal.
//   - HardMaxBytes (4 MiB) is enforced at Materialize entry. Larger
//     text → ErrMaterializeTooLarge before any I/O.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrMaterializeTooLarge: text length exceeds HardMaxBytes (4 MiB).
// Returned at Materialize entry; no file is created.
var ErrMaterializeTooLarge = errors.New("artifact: materialize text exceeds hard cap")

// Materializer writes text content to content-addressed files.
//
// A zero-value Materializer is not usable; BaseDir must be set. The
// Now hook is optional; if nil, time.Now is used (lets tests pin the
// clock for CleanupExpired).
//
// Materializer is safe for concurrent use. Concurrent Materialize
// calls with the same text and sourceTag return the same ArtifactRef
// without corrupting either file.
type Materializer struct {
	// BaseDir is the root directory under which materialized files
	// are written. Required.
	BaseDir string

	// Now returns the current time. nil → time.Now.
	Now func() time.Time
}

// Materialize writes text to a content-addressed file under
// <BaseDir>/<sourceTag>/<sha256>.txt and returns the ArtifactRef.
//
// The returned ArtifactRef is suitable for Resolver.Resolve: the
// Resolver will read the file, compute SHA-256 over the bytes, and
// get the same SHA encoded in the filename.
//
// Guarantees:
//
//   - Idempotent: same (text, sourceTag) → same file, no rewrite.
//   - Atomic: temp file + os.Rename; readers never see partial bytes.
//   - HardMaxBytes enforced at entry; oversized → ErrMaterializeTooLarge.
//   - File mode 0600, dir mode 0700.
//   - sourceTag is sanitized (no path separators, no ".." segments).
//
// Errors:
//   - ErrMaterializeTooLarge: text > HardMaxBytes.
//   - "artifact: Materializer.BaseDir required": BaseDir empty.
//   - os.MkdirAll/CreateTemp/Write/Sync/Rename errors wrap as-is.
func (m *Materializer) Materialize(_ context.Context, text, sourceTag string) (ArtifactRef, error) {
	if m.BaseDir == "" {
		return ArtifactRef{}, errors.New("artifact: Materializer.BaseDir required")
	}
	sourceTag = sanitizeSourceTag(sourceTag)
	bytes := []byte(text)
	if len(bytes) > HardMaxBytes {
		return ArtifactRef{}, fmt.Errorf("%w: %d > %d", ErrMaterializeTooLarge, len(bytes), HardMaxBytes)
	}
	sum := sha256.Sum256(bytes)
	hexSum := hex.EncodeToString(sum[:])

	subdir := filepath.Join(m.BaseDir, sourceTag)
	target := filepath.Join(subdir, hexSum+".txt")

	// Idempotency check: file exists → return without rewriting.
	if _, err := os.Stat(target); err == nil {
		return ArtifactRef{Kind: KindFile, Path: target}, nil
	}

	if err := os.MkdirAll(subdir, 0o700); err != nil {
		return ArtifactRef{}, fmt.Errorf("artifact: mkdir %s: %w", subdir, err)
	}

	// Atomic write: CreateTemp in same dir, write, sync, chmod, rename.
	tmp, err := os.CreateTemp(subdir, "."+hexSum+".tmp.")
	if err != nil {
		return ArtifactRef{}, fmt.Errorf("artifact: temp file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(bytes); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return ArtifactRef{}, fmt.Errorf("artifact: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return ArtifactRef{}, fmt.Errorf("artifact: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return ArtifactRef{}, fmt.Errorf("artifact: close: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		_ = os.Remove(tmpName)
		return ArtifactRef{}, fmt.Errorf("artifact: chmod: %w", err)
	}

	// Rename. On Windows, target existing or a brief residual lock
	// after a peer's rename can cause the OS to deny this rename. In
	// that case, check whether the target already exists with our
	// content; since the path is content-addressed (SHA in filename),
	// an existing target MUST contain our text. Retry briefly to ride
	// out transient Windows locks.
	for attempt := 0; attempt < 5; attempt++ {
		if err := os.Rename(tmpName, target); err == nil {
			return ArtifactRef{Kind: KindFile, Path: target}, nil
		}
		// Peer's file or transient lock? Check.
		if _, statErr := os.Stat(target); statErr == nil {
			_ = os.Remove(tmpName)
			return ArtifactRef{Kind: KindFile, Path: target}, nil
		}
		time.Sleep(time.Duration(attempt+1) * time.Millisecond)
	}
	_ = os.Remove(tmpName)
	return ArtifactRef{}, errors.New("artifact: rename failed after retries")
}

// CleanupExpired removes files under BaseDir whose mtime is older
// than ttl (relative to the Materializer's Now clock). Returns the
// count of files removed. Subdirectories are NOT removed even if
// empty (cheap, idempotent, can be done out-of-band).
//
// Empty BaseDir → error (the production wiring is required to set it).
// CleanupExpired never returns an error for "nothing to do"; only
// filesystem errors (rare) are surfaced.
func (m *Materializer) CleanupExpired(_ context.Context, ttl time.Duration) (int, error) {
	if m.BaseDir == "" {
		return 0, errors.New("artifact: Materializer.BaseDir required")
	}
	nowFn := m.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	cutoff := nowFn().Add(-ttl)

	var removed int
	err := filepath.WalkDir(m.BaseDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			if rerr := os.Remove(path); rerr == nil {
				removed++
			}
		}
		return nil
	})
	return removed, err
}

// sanitizeSourceTag makes sourceTag safe to use as a directory name.
// Strips path separators and ".." segments. Empty input defaults to
// "default".
func sanitizeSourceTag(tag string) string {
	tag = strings.ReplaceAll(tag, "/", "_")
	tag = strings.ReplaceAll(tag, "\\", "_")
	tag = strings.ReplaceAll(tag, "..", "_")
	if tag == "" {
		return "default"
	}
	return tag
}

// MaterializeFromText is the convenience wrapper for callers that do
// not want to manage a Materializer instance. The BaseDir is resolved
// on every call:
//
//  1. DARK_MATERIALIZE_DIR env var (if set).
//  2. os.UserCacheDir()/dark-materialized (Unix/macOS/Windows standard).
//  3. os.TempDir()/dark-materialized (last-resort fallback).
//
// Production callers that need a stable, configurable BaseDir should
// construct their own *Materializer instead.
func MaterializeFromText(ctx context.Context, text, sourceTag string) (ArtifactRef, error) {
	baseDir := os.Getenv("DARK_MATERIALIZE_DIR")
	if baseDir == "" {
		cacheDir, err := os.UserCacheDir()
		if err != nil || cacheDir == "" {
			cacheDir = os.TempDir()
		}
		baseDir = filepath.Join(cacheDir, "dark-materialized")
	}
	return (&Materializer{BaseDir: baseDir}).Materialize(ctx, text, sourceTag)
}