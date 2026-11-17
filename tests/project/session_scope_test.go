// Package project_test — SPEC-204: la identidad se resuelve una vez y
// viaja hasta donde se aplica.
//
// SPEC-203 hizo que la ESCRITURA fuera al proyecto de la sesion, pero
// solo en las tools que declaraban session_id, y dejo la LECTURA y 12
// de las ~28 rutas de escritura siguiendo el slot global. El resultado
// era una asimetria: el que escribia no veia lo que habia escrito.
//
// SPEC-204 lo cierra por arriba: el servidor resuelve la identidad una
// vez (el middleware), la viaja en el context.Context, y el store la
// aplica en todas las rutas — escritura y lectura — con la misma regla
// y sin degradar al slot ante un error.
//
// ESTE FICHERO FIJA LOS CRITERIOS DE ACEPTACION DE LA SPEC:
//
//	AC1 la escritura sin session_id en la tool aterriza en el proyecto
//	    de la sesion;
//	AC3 un error de resolucion FALLA, no degrada al slot;
//	AC4 vlp_state no mezcla tenants;
//	AC5 recall y list coinciden con scope=session;
//	AC7 el write_audit lleva la sesion de la llamada;
//	AC8 sessions.session_id es UNIQUE.
package project_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// AC1 — la escritura sin session_id en la tool aterriza en el proyecto
// de la sesion, aunque el slot diga otra cosa.
//
// Esta es la diferencia con SPEC-203: alli, una tool que no declarara
// session_id escribia en el slot. Aqui escribe en el proyecto de la
// sesion de la llamada, porque la sesion viaja en el contexto.
func TestSpec204_AC1_WriteWithoutSessionIDLandsInTheSessionProject(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	for _, id := range []string{"acme", "globex"} {
		if err := s.CreateProject(ctx, &project.Project{ProjectID: id, DisplayName: id}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}

	// Session A, in acme.
	s.SetActiveProject(ctx, "acme")
	wcA := store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test", ProjectID: "acme"}
	if _, err := s.SaveSession(ctx, wcA, &session.Session{SessionID: "sess-A", Status: string(session.StatusOpen)}); err != nil {
		t.Fatalf("save session A: %v", err)
	}

	// THE SLOT MOVES: another client of the same server process opened
	// a session in globex. This is the measured situation that broke
	// the read/write loop.
	s.SetActiveProject(ctx, "globex")

	// The call's identity travels in the context (the middleware
	// resolved it). The tool itself declares NO session_id — that is
	// the point: no tool has to.
	callCtx := session.WithScope(ctx, session.Scope{SessionID: "sess-A", ProjectID: "acme"})

	mem := &agentmemory.AgentMemory{Operator: "test", Kind: "note", Content: "the agent's own memory"}
	if _, err := s.SaveAgentMemory(callCtx, store.WriteContext{Actor: "test", WritePath: "test"}, mem); err != nil {
		t.Fatalf("save agent memory: %v", err)
	}
	if mem.ProjectID != "acme" {
		t.Fatalf("SPEC-204: the write landed in %q, want the session's project %q", mem.ProjectID, "acme")
	}
}

// AC1 (bis) + coherencia — la lectura sigue a la sesion de la llamada,
// no al slot.
//
// Esta es la asimetria que SPEC-203 dejo abierta: la escritura iba a
// la sesion y la lectura iba al slot. Ahora las dos sides siguen la
// MISMA identidad, la de la llamada.
func TestSpec204_WriteAndReadAgreeOnTheNamedSession(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	for _, id := range []string{"acme", "globex"} {
		if err := s.CreateProject(ctx, &project.Project{ProjectID: id, DisplayName: id}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	s.SetActiveProject(ctx, "acme")
	wcA := store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test", ProjectID: "acme"}
	if _, err := s.SaveSession(ctx, wcA, &session.Session{SessionID: "sess-A", Status: string(session.StatusOpen)}); err != nil {
		t.Fatalf("save session A: %v", err)
	}
	s.SetActiveProject(ctx, "globex")
	wcB := store.WriteContext{Actor: "test", SessionID: "sess-B", WritePath: "test", ProjectID: "globex"}
	if _, err := s.SaveSession(ctx, wcB, &session.Session{SessionID: "sess-B", Status: string(session.StatusOpen)}); err != nil {
		t.Fatalf("save session B: %v", err)
	}

	// One row per project, written with the identity of its own call.
	acmeCtx := session.WithScope(ctx, session.Scope{SessionID: "sess-A", ProjectID: "acme"})
	globexCtx := session.WithScope(ctx, session.Scope{SessionID: "sess-B", ProjectID: "globex"})
	if _, err := s.SaveAgentMemory(acmeCtx, store.WriteContext{Actor: "test", WritePath: "test"},
		&agentmemory.AgentMemory{Operator: "test", Kind: "note", Content: "coherenciaspec204 acme"}); err != nil {
		t.Fatalf("save agent memory (acme): %v", err)
	}
	if _, err := s.SaveAgentMemory(globexCtx, store.WriteContext{Actor: "test", WritePath: "test"},
		&agentmemory.AgentMemory{Operator: "test", Kind: "note", Content: "coherenciaspec204 globex"}); err != nil {
		t.Fatalf("save agent memory (globex): %v", err)
	}

	// THE SLOT MOVES to globex before the read. A read that follows
	// the slot would answer with globex's row.
	s.SetActiveProject(ctx, "globex")

	// The read carries the identity of the call (no filter at all).
	rows, err := s.ListAgentMemory(acmeCtx, agentmemory.AgentMemoryListFilters{})
	if err != nil {
		t.Fatalf("list with the call's scope: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("SPEC-204: list with the call's scope returned %d rows, want 1", len(rows))
	}
	if rows[0].ProjectID != "acme" {
		t.Fatalf("SPEC-204: the read returned a row of %q, want %q", rows[0].ProjectID, "acme")
	}

	// AC5 — recall (BM25) must behave like list: the same question,
	// the same answer. Before SPEC-204, recall ignored the named
	// session and used the slot's active session.
	hits, err := s.SearchAgentMemory(acmeCtx, agentmemory.SearchFilters{
		Query: "coherenciaspec204",
	})
	if err != nil {
		t.Fatalf("search with the call's scope: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("SPEC-204 (M1): recall with the call's scope returned %d hits, want 1", len(hits))
	}
	if hits[0].ProjectID != "acme" {
		t.Fatalf("SPEC-204 (M1): the recall returned a row of %q, want %q", hits[0].ProjectID, "acme")
	}

	// AC5 (bis) — the NAMED session wins over the call's scope when it
	// contradicts it: the session is the authority.
	hits, err = s.SearchAgentMemory(ctx, agentmemory.SearchFilters{
		Query:     "coherenciaspec204",
		SessionID: "sess-B",
	})
	if err != nil {
		t.Fatalf("search with session B: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("SPEC-204 (M1): recall with session B returned %d hits, want 1", len(hits))
	}
	if hits[0].ProjectID != "globex" {
		t.Fatalf("SPEC-204 (M1): the recall returned a row of %q, want %q", hits[0].ProjectID, "globex")
	}
}

// AC3 — un error de resolucion es un ERROR, no un degradado al slot.
//
// Un contexto cancelado hace fallar la consulta que resuelve el
// proyecto de la sesion. El store debe FALLAR: caer al slot seria
// reintroducir el bug original como camino silencioso.
func TestSpec204_AC3_ResolutionFailureDoesNotDegradeToTheSlot(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	for _, id := range []string{"acme", "globex"} {
		if err := s.CreateProject(ctx, &project.Project{ProjectID: id, DisplayName: id}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	s.SetActiveProject(ctx, "acme")
	wcA := store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test", ProjectID: "acme"}
	if _, err := s.SaveSession(ctx, wcA, &session.Session{SessionID: "sess-A", Status: string(session.StatusOpen)}); err != nil {
		t.Fatalf("save session A: %v", err)
	}
	s.SetActiveProject(ctx, "globex")

	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	mem := &agentmemory.AgentMemory{Operator: "test", Kind: "note", Content: "must not land in the slot"}
	_, err := s.SaveAgentMemory(cancelled, store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test"}, mem)
	if err == nil {
		t.Fatalf("SPEC-204 (A1): the write succeeded although the resolution failed: it degraded to the slot")
	}
	if mem.ProjectID == "globex" {
		t.Fatalf("SPEC-204 (A1): the write degraded to the slot (%q)", mem.ProjectID)
	}
}

// AC4 — vlp_state es UNIQUE(project_id, session_id): el esquema permite
// el mismo session_id en dos proyectos. La lectura por id, sin proyecto,
// puede devolver el estado de otro tenant.
func TestSpec204_AC4_VLPStateIsNotMixedBetweenTenants(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	for _, id := range []string{"acme", "globex"} {
		if err := s.CreateProject(ctx, &project.Project{ProjectID: id, DisplayName: id}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	// The session exists in acme, so it is acme's session.
	s.SetActiveProject(ctx, "acme")
	wcA := store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test", ProjectID: "acme"}
	if _, err := s.SaveSession(ctx, wcA, &session.Session{SessionID: "sess-A", Status: string(session.StatusOpen)}); err != nil {
		t.Fatalf("save session A: %v", err)
	}
	if _, err := s.SaveVLPState(ctx, wcA, &store.VLPStateRow{SessionID: "sess-A", State: 1, TurnCount: 1}); err != nil {
		t.Fatalf("save vlp state (acme): %v", err)
	}

	// Another tenant has a row with the SAME session id. The schema
	// allows it (the uniqueness is the pair).
	s.SetActiveProject(ctx, "globex")
	wcB := store.WriteContext{Actor: "test", WritePath: "test", ProjectID: "globex"}
	if _, err := s.SaveVLPState(ctx, wcB, &store.VLPStateRow{SessionID: "sess-A", State: 2, TurnCount: 9}); err != nil {
		t.Fatalf("save vlp state (globex): %v", err)
	}

	// The read for sess-A must return ACME's row (the project that
	// owns the session), never globex's.
	got, err := s.GetVLPStateByID(ctx, "sess-A")
	if err != nil {
		t.Fatalf("get vlp state by id: %v", err)
	}
	if got == nil {
		t.Fatalf("SPEC-204 (A2): the vlp_state row of sess-A was not found")
	}
	if got.ProjectID != "acme" {
		t.Fatalf("SPEC-204 (A2): the read returned the row of %q, want %q", got.ProjectID, "acme")
	}
	if got.State != 1 {
		t.Fatalf("SPEC-204 (A2): the read returned the state %d of another tenant, want 1", got.State)
	}
}

// AC7 — el write_audit lleva la sesion de la llamada, aunque la tool
// no la declarara: el rastro dice quien escribio.
func TestSpec204_AC7_AuditCarriesTheSessionOfTheCall(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "acme"}); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	s.SetActiveProject(ctx, "acme")
	wcA := store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test", ProjectID: "acme"}
	if _, err := s.SaveSession(ctx, wcA, &session.Session{SessionID: "sess-A", Status: string(session.StatusOpen)}); err != nil {
		t.Fatalf("save session A: %v", err)
	}

	callCtx := session.WithScope(ctx, session.Scope{SessionID: "sess-A", ProjectID: "acme"})
	mem := &agentmemory.AgentMemory{Operator: "test", Kind: "note", Content: "with trace"}
	if _, err := s.SaveAgentMemory(callCtx, store.WriteContext{Actor: "test", WritePath: "test"}, mem); err != nil {
		t.Fatalf("save agent memory: %v", err)
	}

	writes, err := s.ListWrites(ctx, audit.ListFilters{SessionID: "sess-A", TableName: "agent_memory"})
	if err != nil {
		t.Fatalf("list writes: %v", err)
	}
	if len(writes) == 0 {
		t.Fatalf("SPEC-204: the write left no trace of the session")
	}
	for _, w := range writes {
		if w.SessionID != "sess-A" {
			t.Fatalf("SPEC-204: the audit row carries session %q, want %q", w.SessionID, "sess-A")
		}
	}
}

// AC8 — el invariante que sostiene la correccion: sessions.session_id es
// UNIQUE global. Sin el, ProjectIDForSession podria devolver la fila
// arbitraria de dos, y con ella el proyecto equivocado.
//
// Este test falla si alguien quita el UNIQUE del esquema.
func TestSpec204_AC8_SessionsSessionIDIsUnique(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "acme"}); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	s.SetActiveProject(ctx, "acme")
	wcA := store.WriteContext{Actor: "test", SessionID: "sess-U", WritePath: "test", ProjectID: "acme"}
	if _, err := s.SaveSession(ctx, wcA, &session.Session{SessionID: "sess-U", Status: string(session.StatusOpen)}); err != nil {
		t.Fatalf("save session U: %v", err)
	}

	// The same session id, again: the schema must refuse it.
	_, err := s.SaveSession(ctx, wcA, &session.Session{SessionID: "sess-U", Status: string(session.StatusOpen)})
	if err == nil {
		t.Fatalf("SPEC-204 (L5): sessions.session_id is no longer UNIQUE: a duplicate was accepted")
	}
}

// La correccion de A1 en la lectura: resolver el proyecto de la sesion
// con un error de base de datos debe fallar, no devolver un conjunto
// vacio (que seria indistinguible de "no hay memoria").
func TestSpec204_ReadResolutionFailureIsAnErrorNotAnEmptyResult(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateProject(ctx, &project.Project{ProjectID: "acme", DisplayName: "acme"}); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	s.SetActiveProject(ctx, "acme")
	wcA := store.WriteContext{Actor: "test", SessionID: "sess-A", WritePath: "test", ProjectID: "acme"}
	if _, err := s.SaveSession(ctx, wcA, &session.Session{SessionID: "sess-A", Status: string(session.StatusOpen)}); err != nil {
		t.Fatalf("save session A: %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	_, err := s.ListAgentMemory(cancelled, agentmemory.AgentMemoryListFilters{SessionID: "sess-A"})
	if err == nil {
		t.Fatalf("SPEC-204 (A1): the read returned an empty result although the resolution failed")
	}
	if errors.Is(err, context.Canceled) {
		return // the error travels as-is: the point is that it is an error
	}
}
