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
)

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
	CloseSession(ctx context.Context, wc WriteContext, sessionID string) error
	ListSessions(ctx context.Context, limit int) ([]session.Session, error)

	// --- Research (INV-2: session_scope parameter on Recall) ---
	SaveRun(ctx context.Context, wc WriteContext, run *research.ResearchRun) (int64, error)
	GetRun(ctx context.Context, id int64) (*research.ResearchRun, error)
	ListRuns(ctx context.Context, intent string, limit int) ([]research.ResearchRun, error)
	Recall(ctx context.Context, opts research.RecallOptions) ([]research.Item, error)
	ListItems(ctx context.Context, runID int64, source string, limit int) ([]research.Item, error)
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
