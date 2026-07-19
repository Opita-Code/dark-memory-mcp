// Package store is the abstraction layer over the persistent backend.
// Both internal/store/sqlite and internal/store/postgres implement the
// Store interface; the factory in factory.go selects one based on Config.
//
// Every Save* method takes a WriteContext (see internal/audit/types.go)
// so the implementation can emit a write_audit row atomically with the
// data write — this is INV-1 (write-path audit) from the constitution.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/atomic"
	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/constitution"
	"github.com/dark-agents/dark-memory-mcp/internal/mods"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/research"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/ssd"
	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

// Driver identifies the backing store.
type Driver string

const (
	DriverSQLite   Driver = "sqlite"
	DriverPostgres Driver = "postgres"
)

// Config carries all knobs needed to open a Store. Required: Driver, DSN.
// Driver-specific options (BusyTimeout, MaxOpenConns, etc.) are best-effort
// — unknown keys are ignored.
type Config struct {
	Driver           Driver
	DSN              string        // file path for sqlite, URL for postgres
	MaxOpenConns     int           // postgres only
	MaxIdleConns     int           // postgres only
	ConnMaxLifetime  time.Duration // postgres only
	BusyTimeout      time.Duration // sqlite only
	WALMode          bool          // sqlite only, default true
	ForeignKeys      bool          // sqlite only, default true
	ConstitutionID   string        // active constitution id; written to write_audit
	ConstitutionVer  string        // active constitution version
	Operator         string        // who is running this process

	// ConstitutionFile is the path to the constitution TOML. When set,
	// Store.Open hashes the file and verifies it against the stored
	// SHA in the constitutions table (INV-4: watchdog). On mismatch
	// Store.Open returns ErrConstitutionDrift.
	ConstitutionFile string

	// Safety is injected at Open; optional. If nil, Store constructs
	// an empty Holder (canary unset). Tests use this to install a
	// specific canary in the holder before exercising Save* methods.
	Safety *SafetyHolder
}

// SafetyHolder is the canary/token holder surfaced via Config.Safety.
// Defined as a type alias here to avoid a cycle with internal/safety.
// The concrete implementation lives in internal/safety.
type SafetyHolder struct {
	// SetCanary installs a canary token. Empty clears the canary.
	SetCanary func(token string)
	// Active returns the currently installed canary (empty if unset).
	Active func() string
	// ValidatePayload returns ErrCanaryInPayload if payload contains canary.
	ValidatePayload func(payload string) error
}

// Stats is the aggregate health snapshot returned by Stats().
type Stats struct {
	Driver            string         `json:"driver"`
	Open              bool           `json:"open"`
	SchemaVersion     int            `json:"schema_version"`
	Tables            []string       `json:"tables"`
	RunsTotal         int            `json:"runs_total"`
	ItemsTotal        int            `json:"items_total"`
	LinksTotal        int            `json:"links_total"`
	SpecsTotal        int            `json:"specs_total"`
	ArtifactsTotal    int            `json:"artifacts_total"`
	DriftReportsTotal int            `json:"drift_reports_total"`
	SDDEvaluations    int            `json:"sdd_evaluations"`
	WriteAuditTotal   int            `json:"write_audit_total"`
	SessionsActive    int            `json:"sessions_active"`
	IndexHealth       map[string]int `json:"index_health,omitempty"` // table -> index count
}

// VacuumStats reports what Vacuum deleted.
type VacuumStats struct {
	TablesVacuumed []string `json:"tables_vacuumed"`
	RowsDeleted    int64    `json:"rows_deleted"`
	BytesReclaimed int64    `json:"bytes_reclaimed,omitempty"` // sqlite VACUUM reports; postgres approximate
	Duration       string   `json:"duration"`
}

// MigrationStatus describes one migration and whether it has been applied.
type MigrationStatus struct {
	Version   int    `json:"version"`
	Name      string `json:"name"`
	Applied   bool   `json:"applied"`
	AppliedAt string `json:"applied_at,omitempty"`
}

// Typed errors that callers branch on. Implementations return these
// verbatim (errors.Is matches). Never masquerade as 200 OK.
var (
	ErrDriverMismatch     = errors.New("store: driver does not support this operation")
	ErrVersionMismatch    = errors.New("store: schema version incompatible with call")
	ErrNotConfigured      = errors.New("store: required dependency not configured")
	ErrConstitutionDrift  = errors.New("store: constitution file SHA256 drifted from stored value")
	ErrCanaryInPayload    = errors.New("store: payload contains active canary token (likely extraction attempt)")
	ErrModContentRefused  = errors.New("store: mod content sanitization refused load")
	ErrSessionRequired    = errors.New("store: workflow tool requires an active session")
	ErrArmedRequired      = errors.New("store: red-team tool requires DARK_REDTEAM=armed")
	ErrAlreadyExists      = errors.New("store: row already exists")
	ErrNotFound           = errors.New("store: row not found")
	ErrInvalidArgument    = errors.New("store: invalid argument")
	// ErrInvalidState: state transition is invalid given the row's
	// current state (e.g. resolving a drift that was already
	// reconciled). Distinct from ErrAlreadyExists (which is about
	// row creation) and ErrNotFound (which is about row absence).
	ErrInvalidState       = errors.New("store: invalid state for requested operation")
)

// FieldError carries the offending JSON field name AND the sentinel
// it wraps, so the tools layer (ToToolError in internal/tools/errors.go)
// can populate ToolError.Field for the harness. errors.As extracts it.
//
// F35 wire-propagation: previously only json.UnmarshalTypeError set
// ToolError.Field; orchestrator-level semantic errors (e.g. missing
// required field, parseTasksField rejecting a non-array non-string)
// silently dropped the field info. FieldError fixes that.
//
// Use NewFieldError from any package; do not construct the struct
// directly (the Store field is unexported).
type FieldError struct {
	Store error
	Field string
}

func (e *FieldError) Error() string {
	if e.Field == "" {
		return e.Store.Error()
	}
	return e.Store.Error() + " (field=" + e.Field + ")"
}

func (e *FieldError) Unwrap() error { return e.Store }

// NewFieldError wraps store-err as an ErrInvalidArgument-wrapping
// FieldError carrying field name. ToToolError extracts both via
// errors.Is (for the sentinel) and errors.As (for Field).
func NewFieldError(storeErr error, field string) error {
	return &FieldError{Store: storeErr, Field: field}
}

// Store is the abstraction over the persistent backend. Two implementations:
// internal/store/sqlite (modernc.org/sqlite) and internal/store/postgres
// (jackc/pgx/v5). The factory in factory.go selects the right one based on
// Config.Driver.
//
// Both implementations MUST pass the contract tests in tests/dual_driver/.
// Adding a new method to this interface requires adding the same method
// to both impls AND a test case in tests/dual_driver/store_test.go.
type Store interface {
	// --- Lifecycle ---
	Close() error
	Ping(ctx context.Context) error
	DriverName() string

	// --- Migrations ---
	Migrate(ctx context.Context) error
	SchemaVersion(ctx context.Context) (int, error)
	MigrationStatus(ctx context.Context) ([]MigrationStatus, error)

	// --- Safety (INV-3, INV-4) ---
	// SetCanary installs a canary token used by INV-3 (reject payloads
	// that contain the canary) and INV-4 (verify constitution SHA).
	// Empty string clears.
	SetCanary(token string)
	// CanaryPresent reports whether a canary is currently installed.
	// Used by dark-mem-inspect to verify INV-3 status without exposing
	// the token value (an unprivileged read). Takes no ctx because the
	// canary lives in-process in a sync-protected Holder; no DB hop.
	// Review-w4-001: prior to this, dark-mem-inspect reported a fresh
	// empty Holder and always printed canary_present=false — operators
	// were being lied to. See drift_log 206.
	CanaryPresent() bool
	// ActiveConstitution is GLOBAL by design (spec 171 T4g).
	// Returns the (id, version, sha256) of the active constitution,
	// as currently seen by the watchdog. Empty values if no
	// constitution has been registered. The active constitution is
	// a system-level property used by runWatchdog (INV-4) — there is
	// no per-project active constitution.
	ActiveConstitution(ctx context.Context) (id, version, sha256 string)

	// --- Project namespace (INV-7) ---
	// SetActiveProject installs the project_id that the Store will use
	// to filter every read and tag every write. Empty string clears
	// (denies all reads until a project is set). Project isolation is
	// the default; pass CrossProject=true to opt-out for a single read.
	//
	// Returns ErrInvalidArgument if projectID is non-empty and does not
	// exist in the projects table (catches typos at set-time). The
	// special id "default" is always allowed even if no row exists yet
	// (legacy compat; the auto-seed in T3 makes the row materialise
	// before any production caller runs). On rejection the previous
	// active project is preserved.
	SetActiveProject(ctx context.Context, projectID string) error
	// ActiveProject returns the currently installed project_id.
	ActiveProject() string
	// CreateProject inserts a new project. Idempotent on (project_id).
	CreateProject(ctx context.Context, p *project.Project) error
	// GetProject returns a project by id, or nil if missing.
	GetProject(ctx context.Context, projectID string) (*project.Project, error)
	// ListProjects returns all non-archived projects newest-first.
	ListProjects(ctx context.Context, limit int) ([]project.Project, error)
	// ArchiveProject soft-deletes (sets archived_at). Idempotent.
	ArchiveProject(ctx context.Context, projectID string) error

	// --- Audit (INV-1) ---
	RecordWrite(ctx context.Context, ev audit.WriteEvent) error
	ListWrites(ctx context.Context, f audit.ListFilters) ([]audit.WriteEvent, error)

	// --- Sessions ---
	// SaveSession requires sess.ProjectID != "" (project isolation).
	SaveSession(ctx context.Context, wc WriteContext, s *session.Session) (int64, error)
	GetSession(ctx context.Context, sessionID string) (*session.Session, error)
	// CloseSession closes a session with the given operator reason.
	// reason ∈ {clean, aborted, archived} — see session.CloseReason.
	// Maps to {closed_clean, closed_aborted, archived} per INV-8.
	CloseSession(ctx context.Context, wc WriteContext, sessionID, reason string) error
	ListSessions(ctx context.Context, limit int) ([]session.Session, error)

	// SaveHeartbeat refreshes last_heartbeat_at on an `open` session.
	// Returns ErrNotFound if the session doesn't exist OR isn't open/idle.
	// (Wave 5E.ii; INV-9 update path.)
	SaveHeartbeat(ctx context.Context, wc WriteContext, sessionID string) error

	// FindClosedAbortedForActor returns the most recent closed_aborted
	// session for the operator within lookback. Read-only — caller (the
	// Recover orchestrator) decides whether to resurrect. Returns nil
	// (not error) if no candidate exists. (Wave 5E.ii.)
	FindClosedAbortedForActor(ctx context.Context, actor, operator, projectID, lookback string) (*session.Session, error)

	// SaveResurrect creates a new sessions row representing the
	// resurrection of a closed_aborted session. The original is NOT
	// modified — it remains as the audit-history anchor. Returns the
	// new session row with both ID and SessionID populated so the
	// caller can resume without a follow-up read. (Wave 5E.ii +
	// 5E.iv.b signature tightening.)
	//
	// History: pre-5E.iv.b this returned (int64, error) where the
	// int64 was the internal row ID. The SessionID was generated
	// inside the function via session.NewSessionID() and not
	// exposed; the orchestrator recovered it via a fragile
	// ListSessions(50)+Go-filter scan. 5E.iv.b tightened the
	// signature to (*session.Session, error) so the returned row
	// is canonical. Build+vet validate the type-system change; CI
	// validates the runtime behavior (atomic INSERT + RETURNING +
	// write_audit emission in a single tx).
	SaveResurrect(ctx context.Context, wc WriteContext, original *session.Session) (*session.Session, error)

	// ListStaleSessions returns sessions whose last_heartbeat_at is
	// older than cutoff AND whose status is in the given set. Used by
	// the Sweeper (Wave 5E.iii) to find candidates for promotion
	// (open → idle, idle → closed_aborted). Read-only — caller decides
	// whether to transition. Project-scoped (INV-7).
	ListStaleSessions(ctx context.Context, statuses []string, cutoff time.Time, limit int) ([]session.Session, error)

	// PromoteSessionStatus transitions a session to newStatus without
	// touching closed_at. Used by the Sweeper (Wave 5E.iii) for the
	// open → idle promotion (idle is NOT a closed state — the
	// session stays resumable). Emits a write_audit row with
	// session_event='promote' so the transition is auditable.
	//
	// Valid transitions: open → idle, idle → open (re-activation
	// after a stray idle). Anything else returns ErrInvalidState.
	// Caller is the Sweeper — operators do NOT call this directly;
	// they call SessionHeartbeat (refreshes timestamp, doesn't
	// change status) or SessionClose (closes with reason).
	//
	// RACE-TOLERANCE CONTRACT: ErrInvalidState can also be returned
	// when the session's status has CHANGED between the sweeper's
	// ListStaleSessions read and this call. This is the expected
	// race pattern (e.g. session_heartbeat re-activated it). The
	// Sweeper treats ErrInvalidState as benign and continues to the
	// next session. Closed-session→open transitions also return
	// ErrInvalidState — that is a misuse, not a race. The contract
	// test in tests/dual_driver/store_test.go asserts both paths
	// (closed_clean → open must error; the sweeper's race tolerance
	// is documented in internal/orchestration/session_sweeper.go).
	PromoteSessionStatus(ctx context.Context, wc WriteContext, sessionID, newStatus string) error

	// --- Research (INV-2: session_scope parameter on Recall) ---
	SaveRun(ctx context.Context, wc WriteContext, run *research.ResearchRun) (int64, error)
	GetRun(ctx context.Context, id int64) (*research.ResearchRun, error)
	ListRuns(ctx context.Context, intent string, limit int) ([]research.ResearchRun, error)
	Recall(ctx context.Context, opts research.RecallOptions) ([]research.Item, error)
	ListItems(ctx context.Context, runID int64, source string, limit int) ([]research.Item, error)
	// CountItemsForProject returns the total count of research_items
	// in the given project. O(log n) via the idx_research_items_project
	// index. Read-only. Used by SessionClose to avoid the N+1 query
	// pattern that listed all runs and counted items per run.
	// (Wave 5E.v; replaces the ListRuns+N×ListItems loop with a single
	// indexed COUNT.)
	CountItemsForProject(ctx context.Context, projectID string) (int, error)
	// CountRunsForProject returns the total count of research_runs
	// in the given project. O(log n) via idx_research_runs_project.
	// Read-only. Same motivation as CountItemsForProject — replaces
	// the "load all runs just to get the count" pattern with an
	// indexed COUNT. (Wave 5E.v.)
	CountRunsForProject(ctx context.Context, projectID string) (int, error)
	LinkResearch(ctx context.Context, wc WriteContext, link *research.Link) error
	ResearchStatus(ctx context.Context) (*research.Status, error)

	// --- Vibeflow: specs ---
	SaveSpec(ctx context.Context, wc WriteContext, sp *vibeflow.Spec) (int64, error)
	GetSpec(ctx context.Context, id int64) (*vibeflow.Spec, error)
	UpdateSpec(ctx context.Context, wc WriteContext, id int64, sp *vibeflow.Spec) error
	DeleteSpec(ctx context.Context, wc WriteContext, id int64) error
	ListSpecs(ctx context.Context, f vibeflow.SpecListFilters) ([]vibeflow.Spec, error)

	// --- Vibeflow: brand guides ---
	SaveBrandGuide(ctx context.Context, wc WriteContext, b *vibeflow.BrandGuide) error
	GetBrandGuide(ctx context.Context, brandID string) (*vibeflow.BrandGuide, error)
	DeleteBrandGuide(ctx context.Context, wc WriteContext, brandID string) error
	ListBrandGuides(ctx context.Context, limit int) ([]vibeflow.BrandGuide, error)

	// --- Vibeflow: compliance rules ---
	// vibe_compliance is GLOBAL by jurisdiction. A rule for "EU" is
	// visible from any project. See spec 171 T4c decision rationale:
	// jurisdiction is a property of law (GDPR is GDPR), not of
	// project. Save/Get/List ignore project_id on purpose. If
	// per-project compliance ever becomes a requirement, swap the
	// PK to (project_id, jurisdiction) in a follow-up migration.
	SaveComplianceRule(ctx context.Context, wc WriteContext, r *vibeflow.ComplianceRule) error
	GetComplianceRule(ctx context.Context, jurisdiction string) (*vibeflow.ComplianceRule, error)
	ListComplianceRules(ctx context.Context, limit int) ([]vibeflow.ComplianceRule, error)

	// --- Vibeflow: artifacts ---
	SaveArtifact(ctx context.Context, wc WriteContext, a *vibeflow.Artifact) (int64, error)
	GetArtifact(ctx context.Context, id int64) (*vibeflow.Artifact, error)
	UpdateArtifact(ctx context.Context, wc WriteContext, id int64, u *vibeflow.ArtifactUpdate) error
	DeleteArtifact(ctx context.Context, wc WriteContext, id int64) error
	ListArtifacts(ctx context.Context, f vibeflow.ArtifactListFilters) ([]vibeflow.Artifact, error)
	SetArtifactValidation(ctx context.Context, wc WriteContext, id int64, status string) error

	// --- Vibeflow: drift reports ---
	SaveDriftReport(ctx context.Context, wc WriteContext, d *vibeflow.DriftReport) (int64, error)
	LatestDriftForArtifact(ctx context.Context, artifactID int64) (*vibeflow.DriftReport, error)
	ListDriftReports(ctx context.Context, artifactID int64, verdict string, limit int) ([]vibeflow.DriftReport, error)

	// --- SSD ---
	SaveSDDEvaluation(ctx context.Context, wc WriteContext, e *ssd.SDDEvaluation) (int64, error)
	LatestSDDEvaluation(ctx context.Context, evalType, targetType, targetID string) (*ssd.SDDEvaluation, error)
	ListSDDEvaluations(ctx context.Context, f ssd.ListFilters) ([]ssd.SDDEvaluation, error)

	// --- Constitution (INV-4: watchdog verifies SHA256 on Open) ---
	SaveConstitution(ctx context.Context, wc WriteContext, c *constitution.Constitution) error
	GetConstitution(ctx context.Context, constitutionID, version string) (*constitution.Constitution, error)
	ListConstitutions(ctx context.Context, limit int) ([]constitution.Constitution, error)
	VerifyConstitutionHash(ctx context.Context, constitutionID, sha256 string) (bool, error)

	// --- Mods (INV-6: content sanitization enforced by the loader; impl
	// stores the audit row) ---
	SaveMod(ctx context.Context, wc WriteContext, m *mods.Mod) error
	GetMod(ctx context.Context, modID string) (*mods.Mod, error)
	ListMods(ctx context.Context, limit int) ([]mods.Mod, error)
	RecordModLoad(ctx context.Context, wc WriteContext, load *mods.ModLoad) (int64, error)
	ListModLoads(ctx context.Context, modID string, limit int) ([]mods.ModLoad, error)

	// --- Admin ---
	Vacuum(ctx context.Context, policy VacuumPolicy) (VacuumStats, error)
	// Stats is GLOBAL by design (spec 171 T4g). Returns aggregate
	// counters across the entire dark.db: schema version, table list,
	// total rows per table. Operator observability entry point —
	// does NOT filter by active project. If per-project stats are
	// ever needed, add a sister method StatsForProject(ctx, projectID).
	Stats(ctx context.Context) (*Stats, error)

	// --- Atomic frames (Wave 5A.ii.a; v11 schema vibe_frames; INV-5) ---
	// SaveFrame inserts-or-updates-most-recent a frame envelope keyed by
	// (project_id, session_id, scope_level, scope_id, frame_kind).
	// When a row already exists for the same key, the implementation
	// overwrites frame_json + content_sha256 + expires_at +
	// last_write_id + composed_at and returns the row id of the
	// existing row. A write_audit row is emitted in the SAME
	// transaction (INV-1: atomic audit).
	//
	// NOTE: this is NOT a true UPSERT. v11's `vibe_frames` ships
	// without a UNIQUE INDEX on the key (that arrives in v11b with
	// the cache layer). The implementation uses SELECT-then-UPDATE
	// under s.mu (sqlite) or SELECT FOR UPDATE inside runInTx
	// (postgres). Race-safety is guaranteed at the driver level, not
	// at the schema level. Long-term fix: v11b UNIQUE INDEX.
	//
	// Returns the row id. The envelope's ID + CreatedAt fields are
	// populated on the returned envelope.
	SaveFrame(ctx context.Context, wc WriteContext, env *atomic.FrameEnvelope) (int64, error)
	// GetFrame returns the freshest non-expired frame matching the
	// (session_id, scope_level, scope_id, frame_kind) tuple within
	// the active project. Returns (nil, nil) when no row matches
	// (cache miss is not an error).
	//
	// INV-5 (cache integrity): on read, recompute
	// sha256(frame_json) and compare against the stored
	// content_sha256. On mismatch, emit an INV-5 anomaly via the
	// audit log (table=anomaly_events when that lands; until then,
	// emit a write_audit row with session_event="cache_mismatch")
	// and return (nil, nil) so the caller treats it as a cache miss.
	// The frame is re-composed by the cache layer (5A.ii.b).
	GetFrame(ctx context.Context, sessionID string, scope atomic.ScopeLevel, scopeID string, kind atomic.FrameKind) (*atomic.FrameEnvelope, error)
	// ListFrames returns frames filtered by FrameListFilters. By
	// default expired rows are excluded; pass IncludeExpired=true
	// for cache-decay inspection. Newest-first (composed_at DESC).
	// Limit <= 0 means no limit. INV-7: project_id is required.
	ListFrames(ctx context.Context, filter FrameListFilters) ([]atomic.FrameEnvelope, error)
	// DeleteFrame removes a frame by id. Emits a write_audit row in
	// the SAME transaction (INV-1). Used by cache eviction when a
	// frame is invalid, expired beyond grace, or explicitly
	// invalidated via a write_audit.last_write_id cursor change.
	// Returns store.ErrNotFound if the id doesn't exist.
	DeleteFrame(ctx context.Context, wc WriteContext, id int64) error

	// --- Recall subscriptions (Wave 5A.ii.b.1; v11 vibe_recall_subscriptions) ---
	//
	// # Contract (5A.ii.b.1)
	//
	// The subscription table has v11's UNIQUE constraint on
	// (session_id, scope_level, scope_id). The persistence layer
	// commits to the following behavioral contract that the recall
	// orchestrator (5A.ii.b.2) will depend on. This is an explicit
	// cross-wave coupling: 5A.ii.b.1 owns the contract; 5A.ii.b.2
	// owns the policy. They MUST agree.
	//
	//   1. **ID-stable upsert**: SaveRecallSubscription returns the
	//      row id AND mutates the passed `sub.ID` field to the row id
	//      (callers can read either). When the natural key already
	//      exists (ON CONFLICT branch), the SAME row id is returned
	//      and set. This lets the orchestrator track "this is the
	//      same subscription row" across upserts.
	//
	//   2. **LastSeenToken default**: the orchestrator must initialize
	//      LastSeenToken=0 when first creating a subscription. The
	//      persistence layer does NOT auto-zero on conflict; whatever
	//      value the caller passes in `sub.LastSeenToken` is what
	//      gets persisted. The orchestrator decides the seed value.
	//
	//   3. **ErrNotFound on missing-row updates**: when the natural
	//      key doesn't exist for UpdateRecallSubscriptionLastSeenToken,
	//      the implementation returns store.ErrNotFound (NOT nil, NOT
	//      silent success). This matches the codebase-wide convention
	//      (DeleteFrame, DeleteArtifact, etc.) and lets the
	//      orchestrator distinguish "subscription was deleted between
	//      reads" from "successful update."
	//
	//   4. **Get returns (nil, nil) on miss**: GetRecallSubscription
	//      returns (nil, nil) when no row matches the natural key
	//      (NOT store.ErrNotFound). This matches the GetSession /
	//      GetFrame pattern in the rest of the Store. Update methods
	//      return ErrNotFound; Get methods return (nil, nil). The
	//      asymmetry is intentional.
	//
	// SaveRecallSubscription inserts-or-updates a subscription row keyed
	// by (session_id, scope_level, scope_id). Returns the row id.
	// Mutates sub.ID to the row id. Emits a write_audit row in the
	// SAME tx (INV-1).
	SaveRecallSubscription(ctx context.Context, wc WriteContext, sub *RecallSubscription) (int64, error)
	// GetRecallSubscription returns the row matching the natural key,
	// or (nil, nil) if not found. INV-7: filtered by active project.
	// Per contract #4: (nil, nil) on miss, NOT ErrNotFound.
	GetRecallSubscription(ctx context.Context, sessionID string, scope atomic.ScopeLevel, scopeID string) (*RecallSubscription, error)
	// UpdateRecallSubscriptionLastSeenToken advances the cursor to
	// last_seen_token for the matching subscription. Returns
	// store.ErrNotFound if no subscription exists for the key (per
	// contract #3).
	UpdateRecallSubscriptionLastSeenToken(ctx context.Context, wc WriteContext, sessionID string, scope atomic.ScopeLevel, scopeID string, newToken int64) error

	// --- VLP state (atomic spec 2.3 VLPPersistence) ---
	// SaveVLPState upserts a per-session VLP state row. One row per
	// (project_id, session_id) composite — see migration v9's UNIQUE INDEX
	// idx_vlp_state_project_session. Returns the row ID. Writes a
	// write_audit row in the SAME transaction as the UPSERT (INV-1:
	// atomic audit — either both rows land or neither does).
	SaveVLPState(ctx context.Context, wc WriteContext, row *VLPStateRow) (int64, error)
	// SaveVLPStateWithTransition is the atomic combo used by spec 2.5
	// VLPLoopUseCase. In a single DB transaction:
	//   1. UPSERT vlp_state row (returns row id)
	//   2. INSERT write_audit row for the data change (row-level audit)
	//   3. INSERT write_audit row for the transition (transition-level
	//      audit, with notes=transitionJSON — typically a TransitionRecord
	//      serialized to JSON)
	// Closes the 2.5 atomicity gap from spec 2.4 (where Save + Audit
	// were 2 separate calls). Returns the vlp_state row id.
	SaveVLPStateWithTransition(ctx context.Context, wc WriteContext, row *VLPStateRow, transitionNotes string) (int64, error)
	// GetVLPState returns the row for sessionID under the active project,
	// or nil if not found. Cross-project reads are denied (INV-7).
	GetVLPState(ctx context.Context, sessionID string) (*VLPStateRow, error)
	// ListVLPStates returns rows filtered by stateFilter (NUMERIC — int
	// value of internal/vlp.State as a string; empty = all states),
	// newest-first. Limit <= 0 means no limit (caller is responsible
	// for result-set size). stateFilter names like "drafting_spec" are
	// NOT resolved; the internal/vlp.Persistence wrapper handles
	// State enum → numeric conversion.
	ListVLPStates(ctx context.Context, stateFilter string, limit int) ([]VLPStateRow, error)
}

// VLPStateRow is the flat row type persisted in vlp_state. State is the
// integer value of internal/vlp.State (kept as int to avoid an import
// cycle between internal/store and internal/vlp). Conversion is done by
// internal/vlp.Persistence wrapper. LastEvent and LastVerdict are the
// canonical string forms for human-readable audit.
//
// Migration: v9. tenant-scoped via project_id (INV-7).
type VLPStateRow struct {
	ID              int64
	SessionID       string
	State           int
	LastEvent       string
	LastVerdict     string
	TurnCount       int
	MinsetCurrent   string
	ConstitutionID  string
	ConstitutionVer string
	CreatedAt       string
	UpdatedAt       string
	ProjectID       string
	// OpenSpecID (Wave 5X.4) is the spec_id this session is currently
	// working on. ScopeFrame.OpenSpecID reads from this column
	// directly (5A.ii.b.2.c had to use vlp_state.ID as a proxy). 0
	// means "no spec is open" (ScopeFrame.HasOpenSpec() returns false).
	OpenSpecID int64
}

// RecallSubscription is one row in `vibe_recall_subscriptions`. The
// (session_id, scope_level, scope_id) tuple is the natural key (UNIQUE
// constraint in v11). `last_seen_token` is the write_audit max(id) the
// session has consumed — the recall orchestrator (Wave 5A.ii.b.2)
// uses this to compute deltas via `dark_memory_recall(scope, since_token)`.
//
// Persistence: pure storage. The recall orchestrator (5A.ii.b.2) is the
// trust boundary that decides when to advance last_seen_token.
type RecallSubscription struct {
	ID            int64
	ProjectID     string
	SessionID     string
	ScopeLevel    atomic.ScopeLevel
	ScopeID       string
	LastSeenToken int64
	CreatedAt     string
	UpdatedAt     string
}

// FrameListFilters is the optional filter set for ListFrames. Zero-valued
// fields mean "no filter on this dimension". ProjectID is required (INV-7);
// the others narrow the result set. Expired rows (expires_at <= now) are
// ALWAYS filtered out at the SQL layer — that's hygiene, not policy. The
// cache layer (Wave 5A.ii.b) decides whether to recompose on miss.
type FrameListFilters struct {
	ProjectID  string
	SessionID  string
	ScopeLevel atomic.ScopeLevel // empty = all scope levels
	Kind       atomic.FrameKind  // empty = all kinds
	Limit      int               // <= 0 means no limit
}

// WriteContext is the provenance header passed to every Save* method.
// Carries INV-1 (write-path audit) information that the impl uses to
// populate write_audit atomically with the data write. ProjectID is
// INV-7: every write is tagged with the active project so cross-project
// reads never see unrelated rows.
type WriteContext struct {
	Actor          string // "dark_research_spec_create" | "auto-link-v2" | operator id
	SessionID      string // operational session id (matches session.Session.SessionID)
	WritePath      string // method name on Store, or MCP tool name
	ConstitutionID string // active constitution id
	ConstitutionVer string
	ProjectID      string // INV-7: must equal Store.ActiveProject() or be empty (Store fills)

	// SessionEvent is the v12 audit breadcrumb for session-related
	// writes. Allowed values per internal/audit/types.go: NULL,
	// 'open', 'heartbeat', 'idle_timeout', 'close_clean',
	// 'close_aborted', 'resurrect', 'recover', 'frame_upsert',
	// 'cache_mismatch', 'promote'. Empty string means "not a
	// session-related event" (NULL in DB).
	//
	// Wave 5X.1: prior to this wave the v12 column was dropped by
	// the INSERT statements. Adding this field + threading through
	// the INSERT closes the round-trip gap.
	SessionEvent string
}

// VacuumPolicy controls what Vacuum deletes.
type VacuumPolicy struct {
	// DaysOld: delete rows older than this many days (0 = no time-based GC).
	DaysOld int

	// Tables: restrict vacuum to these table names. Empty = all tables
	// that have a documented retention policy.
	Tables []string

	// DryRun: if true, report counts but do not delete.
	DryRun bool
}
