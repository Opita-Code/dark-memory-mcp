// Package session_test — SPEC-204: el transportador de la identidad.
//
// La identidad de una llamada se resuelve UNA VEZ (el servidor) y viaja
// hasta donde se aplica (el store). Este test fija que el viaje es
// fiel: lo que el servidor resuelve es exactamente lo que el store
// recibe.
package session_test

import (
	"context"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/session"
)

func TestScope_TravelsAndComesBack(t *testing.T) {
	ctx := context.Background()

	if _, ok := session.ScopeFromContext(ctx); ok {
		t.Fatalf("a context without a scope must report none")
	}

	sc := session.Scope{SessionID: "sess-A", ProjectID: "acme"}
	ctx2 := session.WithScope(ctx, sc)

	got, ok := session.ScopeFromContext(ctx2)
	if !ok {
		t.Fatalf("the scope did not travel with the context")
	}
	if got != sc {
		t.Fatalf("the scope came back as %+v, want %+v", got, sc)
	}
}

// Un scope VACIO es una respuesta valida: el servidor no resolvio nada
// y el store decide. No es lo mismo que "no hay scope".
func TestScope_EmptyScopeIsPresentButEmpty(t *testing.T) {
	ctx := session.WithScope(context.Background(), session.Scope{})

	got, ok := session.ScopeFromContext(ctx)
	if !ok {
		t.Fatalf("an empty scope must still be present")
	}
	if got.SessionID != "" || got.ProjectID != "" {
		t.Fatalf("an empty scope came back as %+v", got)
	}
}

func TestResolveCallSession_ExplicitWinsOverTheContext(t *testing.T) {
	ctx := session.WithScope(context.Background(), session.Scope{
		SessionID: "sess-from-ctx",
		ProjectID: "acme",
	})

	if got := session.ResolveCallSession(ctx, "sess-explicit"); got != "sess-explicit" {
		t.Fatalf("the explicit session must win, got %q", got)
	}
	if got := session.ResolveCallSession(ctx, ""); got != "sess-from-ctx" {
		t.Fatalf("without an explicit session the context must answer, got %q", got)
	}
	if got := session.ResolveCallSession(context.Background(), ""); got != "" {
		t.Fatalf("without a scope the answer must be empty, got %q", got)
	}
}
