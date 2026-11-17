// Package session: el transportador de la identidad de una llamada.
//
// SPEC-204: la identidad (sesion, proyecto) se resuelve UNA VEZ, en un
// sitio, y viaja hasta el punto donde se aplica. Este fichero es el
// transporte: el servidor la resuelve y la deja en el contexto; el store
// la lee y la aplica.
//
// Sin este transporte habia DOS resoluciones paralelas de la misma
// pregunta -la del gate y la del store- y cuando no coincidian el
// sistema se rompia por el lado mas silencioso: no habia error, habia
// un vacio (la fila escrita caia en un proyecto y la lectura la buscaba
// en otro).
package session

import (
	"context"
	"strings"
)

// Scope es la identidad de una llamada: la sesion que la hizo y el
// proyecto al que pertenece esa sesion.
//
// SessionID es la sesion resuelta (la que nombro la llamada, o la que
// el servidor resolvio para el gate y fallback). ProjectID es el
// proyecto DE ESA SESION: la autoridad. No es el project_id explicito
// de la llamada, que puede contradecirlo y es un estrechamiento, nunca
// una ampliacion.
//
// Un Scope vacio significa "el servidor no resolvio identidad": el
// store cae entonces en su propia resolucion (y, en su ultimo recurso,
// en el slot global, que esta marcado como deprecado).
type Scope struct {
	SessionID string
	ProjectID string
}

// scopeKey es la clave privada del valor: un tipo no exportado y sin
// campos, de modo que ningun otro paquete puede colar un valor con la
// misma clave.
type scopeKey struct{}

// WithScope returns a copy of ctx carrying sc.
//
// The scope travels with the call: every layer below the server reads
// it from the context instead of resolving the identity again.
func WithScope(ctx context.Context, sc Scope) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, scopeKey{}, sc)
}

// ResolveCallSession returns the session that identifies a call: the
// explicit one when the caller named it, and otherwise the one the
// server resolved and injected into the context.
//
// The explicit one wins: the context is the fallback for the tools that
// do not declare session_id, which is most of them (SPEC-204).
func ResolveCallSession(ctx context.Context, explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return explicit
	}
	if sc, ok := ScopeFromContext(ctx); ok {
		return sc.SessionID
	}
	return ""
}

// ScopeFromContext returns the scope carried by ctx, if any.
//
// The bool reports whether a scope was present at all. A scope that IS
// present but empty (no session, no project) is a valid answer: the
// server resolved nothing and the store decides.
func ScopeFromContext(ctx context.Context) (Scope, bool) {
	if ctx == nil {
		return Scope{}, false
	}
	sc, ok := ctx.Value(scopeKey{}).(Scope)
	if !ok {
		return Scope{}, false
	}
	return sc, true
}
