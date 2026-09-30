// Package project — types. Defines the Project struct (7 fields,
// intentionally minimal vs v3.0.0-docfix's 13) and the validation
// helpers used by Create.
package project

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// reservedProjectIDs are rejected by Store.Create. 'default' is
// seeded on Open and cannot be re-created. 'dark' is reserved for
// internal use (e.g., system writes, observability backfill).
var reservedProjectIDs = map[string]bool{
	"default": true,
	"dark":    true,
}

// projectIDPattern enforces the kebab-case rule from the plan:
// lowercase alnum + hyphen, must start and end with alnum, 3-64
// chars. Same regex dark-memory-mcp uses for project_id elsewhere
// (v3 internal/project/types.go).
var projectIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`)

// MaxDisplayNameLen is the upper bound on DisplayName. Same as
// v3 internal/project/types.go (1-128 chars).
const MaxDisplayNameLen = 128

// MaxDescriptionLen is the upper bound on Description. Same as v3.
const MaxDescriptionLen = 512

// MaxDefaultAgentIDLen is the upper bound on DefaultAgentID. Same
// as v3 (Mem0 agent_id, ≤128 chars).
const MaxDefaultAgentIDLen = 128

// Project is the namespace unit. One Project = one workstream
// within one MCP instance. Cross-project reads are opt-in but the
// default is strict isolation (cf. INV-7 tenant primitive).
type Project struct {
	// ProjectID is the kebab-case public id (PRIMARY KEY). Examples:
	// "default", "opita-market", "pasiones", "dark-memory-mcp".
	// 3-64 chars, lowercase alnum + hyphen, must start and end
	// with alnum. NOT the operator id (operator != project).
	ProjectID string

	// DisplayName is the human-readable label (1-128 chars).
	DisplayName string

	// Description is the optional free-form description (≤512
	// chars). Empty string is allowed.
	Description string

	// DefaultAgentID is the optional Mem0 agent_id (LLM identity)
	// that owns the project. session_start and publish_vibe resolve
	// agent_id with priority: caller input > project.default_agent_id
	// > empty string. ≤128 chars when set.
	DefaultAgentID string

	// CreatedAt is the wall-clock time the project was created.
	// Set by Store.Create via time.Now().UTC() — never caller-supplied.
	CreatedAt time.Time

	// ArchivedAt is the soft-delete timestamp. nil = active project.
	// Set by Store.Archive via time.Now().UTC() — never caller-supplied.
	ArchivedAt *time.Time
}

// Validate enforces the v4 Project invariants. Called by Store.Create
// BEFORE the INSERT so reserved/invalid ids never reach the DB.
//
// Invariant 1: ProjectID is non-empty.
// Invariant 2: ProjectID is NOT reserved ('default', 'dark').
// Invariant 3: ProjectID matches the kebab-case regex.
// Invariant 4: DisplayName is 1-MaxDisplayNameLen chars.
// Invariant 5: Description is ≤MaxDescriptionLen chars (empty OK).
// Invariant 6: DefaultAgentID is ≤MaxDefaultAgentIDLen chars (empty OK).
//
// Returns nil on success; one of the sentinel errors below on failure.
func (p *Project) Validate() error {
	if p == nil {
		return ErrInvalidProject
	}
	if p.ProjectID == "" {
		return fmt.Errorf("%w: project_id is required", ErrInvalidProject)
	}
	if reservedProjectIDs[p.ProjectID] {
		return fmt.Errorf("%w: %q is reserved", ErrReservedProjectID, p.ProjectID)
	}
	if !projectIDPattern.MatchString(p.ProjectID) {
		return fmt.Errorf("%w: %q must be kebab-case (lowercase alnum + hyphen, 3-64 chars, start/end alnum)",
			ErrInvalidProjectID, p.ProjectID)
	}
	if p.DisplayName == "" {
		return fmt.Errorf("%w: display_name is required (1-%d chars)", ErrInvalidProject, MaxDisplayNameLen)
	}
	if len([]rune(p.DisplayName)) > MaxDisplayNameLen {
		return fmt.Errorf("%w: display_name exceeds %d chars", ErrInvalidProject, MaxDisplayNameLen)
	}
	if len([]rune(p.Description)) > MaxDescriptionLen {
		return fmt.Errorf("%w: description exceeds %d chars", ErrInvalidProject, MaxDescriptionLen)
	}
	if len(p.DefaultAgentID) > MaxDefaultAgentIDLen {
		return fmt.Errorf("%w: default_agent_id exceeds %d chars", ErrInvalidProject, MaxDefaultAgentIDLen)
	}
	return nil
}

// IsArchived returns true if the project has been soft-deleted
// (ArchivedAt != nil). Helper for callers that want to skip archived
// rows without inspecting ArchivedAt directly.
func (p *Project) IsArchived() bool {
	return p != nil && p.ArchivedAt != nil
}

// IsDefault returns true if this is the seeded catch-all project.
// Used by tests to identify the auto-seeded row.
func (p *Project) IsDefault() bool {
	return p != nil && p.ProjectID == "default"
}

// NormalizeDisplayName trims whitespace and collapses internal
// whitespace runs to single spaces. Applied by Store.Create before
// INSERT so the canonical display_name is stable across operators
// (e.g., "  opita  market  " → "opita market").
func NormalizeDisplayName(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// errors.go content (errors are exported via package vars below).

// Sentinel errors. Callers use errors.Is to discriminate.
//
// ErrProjectNotFound: Store.Lookup did not find a row with the given
//   project_id, OR Store.Archive target doesn't exist. Phase 4
//   Chunk 4.3's session_start uses this to reject unknown projects
//   at session creation time.
//
// ErrReservedProjectID: Store.Create was called with a reserved id
//   ('default' or 'dark'). Cannot be overridden.
//
// ErrInvalidProjectID: Store.Create was called with a project_id that
//   fails the kebab-case regex (uppercase, too short, too long,
//   starts/ends with hyphen, contains special chars).
//
// ErrInvalidProject: Generic validation failure (display_name,
//   description, default_agent_id length violations, or nil
//   receiver).
var (
	ErrProjectNotFound    = errors.New("project: not found")
	ErrReservedProjectID  = errors.New("project: project_id is reserved")
	ErrInvalidProjectID   = errors.New("project: project_id is malformed")
	ErrInvalidProject     = errors.New("project: invalid")
	ErrProjectAlreadyGone = errors.New("project: already archived")
)