// Package auditgate is the consumer-side half of the
// dark-cli ↔ dark-memory vibe_publish gate contract. See
// /dark-cli/docs/AUDIT.md for the full cross-package protocol.
//
// This package RE-IMPLEMENTS the dark-cli `internal/audit/` surface
// in dark-memory-mcp because the two repos are separate Go modules.
// Divergence between the two is detected by the cross-version
// testvectors in record_test.go (a hand-written AuditRecord signed
// with a known seed must verify here AND in dark-cli).
//
// The contract surface is intentionally small:
//   - AuditRecord JSON shape
//   - ed25519 signature over CanonicalBytes
//   - 300s freshness window
//   - Store: one file per record at <DARK_AUDIT_DIR>/<sha256>.json
//   - Gate.Check(sha256, now) → (record, error)
//   - Provenance projection for downstream dark-memory metadata
package auditgate

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// SchemaVersion is the canonical schema identifier. Bumped on any
// breaking change to AuditRecord JSON shape. dark-memory MUST reject
// records with unknown schema_version (fail closed).
const SchemaVersion = "dark-cli/audit/v1"

// FreshnessWindow matches dark-cli TRUST.md §4 (300s). Outside this
// window the gate rejects with ErrRecordStale.
const FreshnessWindow = 300 * time.Second

// Kind discriminates the two record flavors.
type Kind string

const (
	KindBundleInstall   Kind = "bundle_install"   // sha256 = template bytes (primary producer: dark-cli `dark add`)
	KindExportProcessed Kind = "export_processed" // sha256 = export file (producer: dark-cli `dark lock apply`)
)

// AuditRecord is the JSON-encoded shape defined in
// docs/AUDIT.md §3. The Signature field is excluded from
// CanonicalBytes (it is meta — the signature is over everything else).
type AuditRecord struct {
	SchemaVersion       string    `json:"schema_version"`
	Kind                Kind      `json:"kind"`
	ArtifactSHA256      string    `json:"artifact_sha256"`
	TemplateName        string    `json:"template_name,omitempty"`
	TemplateVersion     string    `json:"template_version,omitempty"`
	VibeCase            string    `json:"vibe_case,omitempty"`
	Harness             string    `json:"harness"`
	HarnessConfigDir    string    `json:"harness_config_dir,omitempty"`
	DarkCLIVersion      string    `json:"dark_cli_version"`
	DarkCLICommit       string    `json:"dark_cli_commit,omitempty"`
	LockfilePath        string    `json:"lockfile_path,omitempty"`
	LockfileEntrySHA256 string    `json:"lockfile_entry_sha256,omitempty"`
	RecordedAt          int64     `json:"recorded_at_unix"`
	FilesWritten        []string  `json:"files_written,omitempty"`
	Outcome             string    `json:"outcome,omitempty"`
	PublisherPubkey     []byte    `json:"publisher_pubkey"`
	Signature           *AuditSig `json:"signature"`
}

// AuditSig is the signature envelope. Algorithm is fixed at
// "ed25519" (anything else is rejected). SignedAtUnix is checked
// against the gate's `now` for replay protection.
type AuditSig struct {
	Algorithm    string `json:"algorithm"`
	Sig          []byte `json:"sig"`
	SignedAtUnix int64  `json:"signed_at_unix"`
}

// Sentinel errors. All gate rejections wrap one of these so callers
// can branch on the exact reason. Same set as dark-cli/internal/audit.
var (
	ErrRecordNil          = errors.New("auditgate: record is nil")
	ErrRecordSchema       = errors.New("auditgate: unknown schema_version")
	ErrRecordKind         = errors.New("auditgate: unknown kind")
	ErrRecordNoArtifact   = errors.New("auditgate: invalid artifact_sha256")
	ErrRecordNoPubkey     = errors.New("auditgate: missing publisher_pubkey")
	ErrRecordUnsigned     = errors.New("auditgate: record missing signature")
	ErrRecordBadAlgorithm = errors.New("auditgate: non-ed25519 signature")
	ErrRecordSignature    = errors.New("auditgate: signature mismatch")
	ErrRecordStale        = errors.New("auditgate: signature outside freshness window")
	ErrRecordNotFound     = errors.New("auditgate: record not found")
)

// NewBundleRecord creates a new audit record for a bundle install.
// artifactSHA256 is the sha256 of the original template bytes.
// publisherPubkey is the trust-root pubkey that will sign.
func NewBundleRecord(
	templateName, templateVersion, vibeCase string,
	harness, harnessConfigDir string,
	darkCLIVersion, darkCLICommit string,
	lockfilePath, lockfileEntrySHA256 string,
	artifactSHA256 string,
	filesWritten []string,
	outcome string,
	now time.Time,
	publisherPubkey ed25519.PublicKey,
) *AuditRecord {
	return &AuditRecord{
		SchemaVersion:       SchemaVersion,
		Kind:                KindBundleInstall,
		ArtifactSHA256:      artifactSHA256,
		TemplateName:        templateName,
		TemplateVersion:     templateVersion,
		VibeCase:            vibeCase,
		Harness:             harness,
		HarnessConfigDir:    harnessConfigDir,
		DarkCLIVersion:      darkCLIVersion,
		DarkCLICommit:       darkCLICommit,
		LockfilePath:        lockfilePath,
		LockfileEntrySHA256: lockfileEntrySHA256,
		RecordedAt:          now.Unix(),
		FilesWritten:        append([]string(nil), filesWritten...),
		Outcome:             outcome,
		PublisherPubkey:     append([]byte(nil), publisherPubkey...),
	}
}

// NewExportRecord creates a new audit record for an export file.
// artifactSHA256 is the sha256 of the export file bytes.
func NewExportRecord(
	harness, harnessConfigDir string,
	darkCLIVersion, darkCLICommit string,
	lockfilePath, artifactSHA256 string,
	now time.Time,
	publisherPubkey ed25519.PublicKey,
) *AuditRecord {
	return &AuditRecord{
		SchemaVersion:    SchemaVersion,
		Kind:             KindExportProcessed,
		ArtifactSHA256:   artifactSHA256,
		Harness:          harness,
		HarnessConfigDir: harnessConfigDir,
		DarkCLIVersion:   darkCLIVersion,
		DarkCLICommit:    darkCLICommit,
		LockfilePath:     lockfilePath,
		RecordedAt:       now.Unix(),
		PublisherPubkey:  append([]byte(nil), publisherPubkey...),
	}
}

// CanonicalBytes returns the deterministic byte representation that
// the signature covers. Mirrors dark-cli/internal/audit/record.go
// ::CanonicalBytes EXACTLY:
//   - Uses an inline canonicalRecord type WITHOUT the Signature
//     field, so the bytes the signature covers never include
//     `"signature": null` (which would be produced by
//     json.MarshalIndent on a struct that has the field set to nil).
//   - MarshalIndent with "  " (matches on-disk format).
//   - Sorts string-array fields (currently FilesWritten).
//
// Any divergence between producer (dark-cli) and consumer (this
// package) MUST be caught by the cross-version test vectors
// (TestExample_SignUnderlyingPackageStillDeterministic here +
// TestExample_CanonicalBytesHash_MatchesDarkMemoryMcp in dark-cli).
func (r *AuditRecord) CanonicalBytes() ([]byte, error) {
	if r == nil {
		return nil, ErrRecordNil
	}
	type canonicalRecord struct {
		SchemaVersion       string   `json:"schema_version"`
		Kind                Kind     `json:"kind"`
		ArtifactSHA256      string   `json:"artifact_sha256"`
		TemplateName        string   `json:"template_name,omitempty"`
		TemplateVersion     string   `json:"template_version,omitempty"`
		VibeCase            string   `json:"vibe_case,omitempty"`
		Harness             string   `json:"harness"`
		HarnessConfigDir    string   `json:"harness_config_dir,omitempty"`
		DarkCLIVersion      string   `json:"dark_cli_version"`
		DarkCLICommit       string   `json:"dark_cli_commit,omitempty"`
		LockfilePath        string   `json:"lockfile_path,omitempty"`
		LockfileEntrySHA256 string   `json:"lockfile_entry_sha256,omitempty"`
		RecordedAt          int64    `json:"recorded_at_unix"`
		FilesWritten        []string `json:"files_written,omitempty"`
		Outcome             string   `json:"outcome,omitempty"`
		PublisherPubkey     []byte   `json:"publisher_pubkey"`
	}
	files := append([]string(nil), r.FilesWritten...)
	sort.Strings(files)
	return json.MarshalIndent(canonicalRecord{
		SchemaVersion:       r.SchemaVersion,
		Kind:                r.Kind,
		ArtifactSHA256:      r.ArtifactSHA256,
		TemplateName:        r.TemplateName,
		TemplateVersion:     r.TemplateVersion,
		VibeCase:            r.VibeCase,
		Harness:             r.Harness,
		HarnessConfigDir:    r.HarnessConfigDir,
		DarkCLIVersion:      r.DarkCLIVersion,
		DarkCLICommit:       r.DarkCLICommit,
		LockfilePath:        r.LockfilePath,
		LockfileEntrySHA256: r.LockfileEntrySHA256,
		RecordedAt:          r.RecordedAt,
		FilesWritten:        files,
		Outcome:             r.Outcome,
		PublisherPubkey:     r.PublisherPubkey,
	}, "", "  ")
}

// Sign fills r.Signature with an ed25519 signature over
// CanonicalBytes(r). seed is the 32-byte ed25519 private key seed
// (same shape as dark-cli). The signature also captures
// signed_at_unix so the gate can reject stale replays.
func (r *AuditRecord) Sign(seed []byte, now time.Time) error {
	if len(seed) != ed25519.SeedSize {
		return fmt.Errorf("auditgate: seed must be %d bytes, got %d", ed25519.SeedSize, len(seed))
	}
	if r.PublisherPubkey == nil {
		priv := ed25519.NewKeyFromSeed(seed)
		r.PublisherPubkey = priv.Public().(ed25519.PublicKey)
	}
	msg, err := r.CanonicalBytes()
	if err != nil {
		return fmt.Errorf("auditgate: canonicalize: %w", err)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	sig := ed25519.Sign(priv, msg)
	r.Signature = &AuditSig{
		Algorithm:    "ed25519",
		Sig:          sig,
		SignedAtUnix: now.Unix(),
	}
	return nil
}

// Verify checks the signature, schema, kind, algorithm, and
// freshness against `now`. Returns nil on full accept; on rejection
// returns the appropriate sentinel so the gate can report it.
func (r *AuditRecord) Verify(now time.Time) error {
	if r == nil {
		return ErrRecordNil
	}
	if r.SchemaVersion != SchemaVersion {
		return ErrRecordSchema
	}
	if r.Kind != KindBundleInstall && r.Kind != KindExportProcessed {
		return ErrRecordKind
	}
	if !isValidSHA256Hex(r.ArtifactSHA256) {
		return ErrRecordNoArtifact
	}
	if len(r.PublisherPubkey) != ed25519.PublicKeySize {
		return ErrRecordNoPubkey
	}
	if r.Signature == nil {
		return ErrRecordUnsigned
	}
	if r.Signature.Algorithm != "ed25519" {
		return ErrRecordBadAlgorithm
	}
	if len(r.Signature.Sig) != ed25519.SignatureSize {
		return ErrRecordBadAlgorithm
	}
	msg, err := r.CanonicalBytes()
	if err != nil {
		return fmt.Errorf("auditgate: canonicalize during verify: %w", err)
	}
	if !ed25519.Verify(ed25519.PublicKey(r.PublisherPubkey), msg, r.Signature.Sig) {
		return ErrRecordSignature
	}
	delta := now.Unix() - r.Signature.SignedAtUnix
	if delta < 0 {
		delta = -delta
	}
	if time.Duration(delta)*time.Second > FreshnessWindow {
		return ErrRecordStale
	}
	return nil
}

// HashFileBytes returns the lowercase hex sha256 of b.
func HashFileBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// MarshalJSON re-implements the JSON encoder so we can match
// dark-cli's on-disk format exactly: indented (MarshalIndent), with
// raw bytes for PublisherPubkey/Sig encoded as base64 (Go's default
// for []byte). This is what gets hashed by CanonicalBytes — so any
// change to encoding MUST be mirrored in dark-cli.
func (r *AuditRecord) MarshalJSONOnDisk() ([]byte, error) {
	// Encode via a temp struct so we don't mutate the receiver —
	// and so PublisherPubkey / Sig go out as base64 strings (NOT
	// []byte, which Go would base64-encode by default). The wire
	// format is therefore: PublisherPubkey is a base64 string,
	// Sig is a base64 string. Matches dark-cli's on-disk format.
	tmp := struct {
		SchemaVersion       string   `json:"schema_version"`
		Kind                Kind     `json:"kind"`
		ArtifactSHA256      string   `json:"artifact_sha256"`
		TemplateName        string   `json:"template_name,omitempty"`
		TemplateVersion     string   `json:"template_version,omitempty"`
		VibeCase            string   `json:"vibe_case,omitempty"`
		Harness             string   `json:"harness"`
		HarnessConfigDir    string   `json:"harness_config_dir,omitempty"`
		DarkCLIVersion      string   `json:"dark_cli_version"`
		DarkCLICommit       string   `json:"dark_cli_commit,omitempty"`
		LockfilePath        string   `json:"lockfile_path,omitempty"`
		LockfileEntrySHA256 string   `json:"lockfile_entry_sha256,omitempty"`
		RecordedAt          int64    `json:"recorded_at_unix"`
		FilesWritten        []string `json:"files_written,omitempty"`
		Outcome             string   `json:"outcome,omitempty"`
		PublisherPubkey     string   `json:"publisher_pubkey"`
		Signature           *struct {
			Algorithm    string `json:"algorithm"`
			Sig          string `json:"sig"`
			SignedAtUnix int64  `json:"signed_at_unix"`
		} `json:"signature"`
	}{
		SchemaVersion:       r.SchemaVersion,
		Kind:                r.Kind,
		ArtifactSHA256:      r.ArtifactSHA256,
		TemplateName:        r.TemplateName,
		TemplateVersion:     r.TemplateVersion,
		VibeCase:            r.VibeCase,
		Harness:             r.Harness,
		HarnessConfigDir:    r.HarnessConfigDir,
		DarkCLIVersion:      r.DarkCLIVersion,
		DarkCLICommit:       r.DarkCLICommit,
		LockfilePath:        r.LockfilePath,
		LockfileEntrySHA256: r.LockfileEntrySHA256,
		RecordedAt:          r.RecordedAt,
		Outcome:             r.Outcome,
		PublisherPubkey:     base64.StdEncoding.EncodeToString(r.PublisherPubkey),
	}
	if r.FilesWritten != nil {
		sorted := append([]string(nil), r.FilesWritten...)
		sort.Strings(sorted)
		tmp.FilesWritten = sorted
	}
	if r.Signature != nil {
		tmp.Signature = &struct {
			Algorithm    string `json:"algorithm"`
			Sig          string `json:"sig"`
			SignedAtUnix int64  `json:"signed_at_unix"`
		}{
			Algorithm:    r.Signature.Algorithm,
			Sig:          base64.StdEncoding.EncodeToString(r.Signature.Sig),
			SignedAtUnix: r.Signature.SignedAtUnix,
		}
	}
	return json.MarshalIndent(&tmp, "", "  ")
}

// UnmarshalJSONOnDisk is the inverse of MarshalJSONOnDisk.
func UnmarshalJSONOnDisk(b []byte) (*AuditRecord, error) {
	var raw struct {
		SchemaVersion       string   `json:"schema_version"`
		Kind                Kind     `json:"kind"`
		ArtifactSHA256      string   `json:"artifact_sha256"`
		TemplateName        string   `json:"template_name"`
		TemplateVersion     string   `json:"template_version"`
		VibeCase            string   `json:"vibe_case"`
		Harness             string   `json:"harness"`
		HarnessConfigDir    string   `json:"harness_config_dir"`
		DarkCLIVersion      string   `json:"dark_cli_version"`
		DarkCLICommit       string   `json:"dark_cli_commit"`
		LockfilePath        string   `json:"lockfile_path"`
		LockfileEntrySHA256 string   `json:"lockfile_entry_sha256"`
		RecordedAt          int64    `json:"recorded_at_unix"`
		FilesWritten        []string `json:"files_written"`
		Outcome             string   `json:"outcome"`
		PublisherPubkey     string   `json:"publisher_pubkey"`
		Signature           *struct {
			Algorithm    string `json:"algorithm"`
			Sig          string `json:"sig"`
			SignedAtUnix int64  `json:"signed_at_unix"`
		} `json:"signature"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	pub, err := base64.StdEncoding.DecodeString(raw.PublisherPubkey)
	if err != nil {
		return nil, fmt.Errorf("auditgate: publisher_pubkey not base64: %w", err)
	}
	r := &AuditRecord{
		SchemaVersion:       raw.SchemaVersion,
		Kind:                raw.Kind,
		ArtifactSHA256:      raw.ArtifactSHA256,
		TemplateName:        raw.TemplateName,
		TemplateVersion:     raw.TemplateVersion,
		VibeCase:            raw.VibeCase,
		Harness:             raw.Harness,
		HarnessConfigDir:    raw.HarnessConfigDir,
		DarkCLIVersion:      raw.DarkCLIVersion,
		DarkCLICommit:       raw.DarkCLICommit,
		LockfilePath:        raw.LockfilePath,
		LockfileEntrySHA256: raw.LockfileEntrySHA256,
		RecordedAt:          raw.RecordedAt,
		FilesWritten:        raw.FilesWritten,
		Outcome:             raw.Outcome,
		PublisherPubkey:     pub,
	}
	if raw.Signature != nil {
		sig, err := base64.StdEncoding.DecodeString(raw.Signature.Sig)
		if err != nil {
			return nil, fmt.Errorf("auditgate: signature.sig not base64: %w", err)
		}
		r.Signature = &AuditSig{
			Algorithm:    raw.Signature.Algorithm,
			Sig:          sig,
			SignedAtUnix: raw.Signature.SignedAtUnix,
		}
	}
	return r, nil
}

// isValidSHA256Hex reports whether s is 64 lowercase hex chars.
func isValidSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// MustValidateSchema is a development-time helper that asserts an
// audit record JSON blob has the right shape. Returns the parsed
// record. Used by tests; production code uses Store.Lookup.
func MustValidateSchema(b []byte) (*AuditRecord, error) {
	r, err := UnmarshalJSONOnDisk(b)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(r.SchemaVersion) == "" {
		return nil, ErrRecordSchema
	}
	return r, nil
}
