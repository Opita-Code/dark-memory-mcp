// Package orchestration — judge_personas_registry.go
//
// v2.17.0 (spec 1155): PersonaRegistry — runtime resolution layer
// over the compiled default personas plus any Markdown overrides.
//
// Per spec 1155 v14 §7:
//
//   - Resolve(evalType, personaID) returns the persona to use for a
//     judge call. Explicit persona_id wins; otherwise the default for
//     eval_type (lex-smallest ID wins); fallback to judge-logical.
//   - List() returns the registered personas sorted by ID.
//   - The registry is read-only after construction.
//
// The registry is the single entry point for callers (T6 prompt
// builder, T7 wire protocol). Constructed once at startup, frozen
// thereafter.
package orchestration

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// PersonaRegistry is the resolved Persona scope. Read-only after
// construction. Safe for concurrent reads via sync.RWMutex (writes
// only happen during construction).
type PersonaRegistry struct {
	mu       sync.RWMutex
	personas map[string]*Persona // by id
	byEval   map[string][]*Persona // by eval_type, ordered (default first, then lex-smallest ID)
	fallback *Persona             // always judge-logical if present
}

// RegistryOptions controls how NewPersonaRegistry loads personas.
type RegistryOptions struct {
	// IncludeMarkdownOverrides reads DARK_JUDGE_PERSONAS_DIR and merges
	// any *.md files with the compiled defaults. Default true.
	IncludeMarkdownOverrides bool
}

// NewPersonaRegistry constructs the registry from the compiled default
// personas, optionally merging Markdown overrides, and indexes by
// eval_type. The registry is read-only after this returns.
func NewPersonaRegistry(opts RegistryOptions) (*PersonaRegistry, error) {
	compiled := CompiledDefaultPersonas()

	// Optional Markdown overrides.
	var overridden []*Persona
	if opts.IncludeMarkdownOverrides {
		var err error
		overridden, err = LoadOverriddenPersonas(compiled)
		if err != nil {
			return nil, fmt.Errorf("NewPersonaRegistry: %w", err)
		}
	}

	r := &PersonaRegistry{
		personas: make(map[string]*Persona, len(compiled)),
		byEval:   make(map[string][]*Persona),
	}

	// Step 1: register compiled defaults.
	for _, p := range compiled {
		r.personas[p.ID] = p
	}

	// Step 2: apply overrides on top of compiled defaults.
	for _, merged := range overridden {
		r.personas[merged.ID] = merged
	}

	// Step 3: require judge-logical as the fallback.
	fallback, ok := r.personas["judge-logical"]
	if !ok {
		return nil, errors.New("NewPersonaRegistry: judge-logical is the mandatory fallback; cannot be missing")
	}
	r.fallback = fallback

	// Step 4: index by eval_type.
	r.reindex()

	return r, nil
}

// reindex rebuilds the byEval map. Each eval_type list is sorted with
// default=true first, then by lex-smallest ID. This is the deterministic
// tie-break per spec 1155 v14 §7.
func (r *PersonaRegistry) reindex() {
	r.byEval = make(map[string][]*Persona)
	for _, p := range r.personas {
		// Custom personas (e.g., a new persona compiled from
		// extension packages) may have no eval_types. judge-coverage
		// intentionally has nil EvalTypes per spec 1155 v14 §4.1.
		for _, et := range p.EvalTypes {
			r.byEval[et] = append(r.byEval[et], p)
		}
	}
	for et := range r.byEval {
		sort.SliceStable(r.byEval[et], func(i, j int) bool {
			pi, pj := r.byEval[et][i], r.byEval[et][j]
			if pi.Default != pj.Default {
				return pi.Default // default=true first
			}
			return pi.ID < pj.ID // lex-smallest ID wins
		})
	}
}

// Resolve returns the persona for a judge call. Per spec 1155 v14 §7:
//
//  1. If personaID != "", look up by id; error if not found.
//  2. Else look up default for evalType (default=true, lex-smallest ID).
//  3. Fallback to judge-logical if no default found.
func (r *PersonaRegistry) Resolve(evalType, personaID string) (*Persona, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if personaID != "" {
		p, ok := r.personas[personaID]
		if !ok {
			return nil, fmt.Errorf("PersonaRegistry.Resolve: persona_id %q not found", personaID)
		}
		return p, nil
	}

	if list, ok := r.byEval[evalType]; ok && len(list) > 0 {
		// First entry is the default (per deterministic tie-break).
		return list[0], nil
	}

	return r.fallback, nil
}

// List returns all registered personas sorted by ID (lexicographic).
// Useful for the `judge_list_personas` MCP tool.
func (r *PersonaRegistry) List() []*Persona {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Persona, 0, len(r.personas))
	for _, p := range r.personas {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

// Get returns the persona by id (no default resolution). Returns
// (nil, false) if not found.
func (r *PersonaRegistry) Get(id string) (*Persona, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.personas[id]
	return p, ok
}

// HasEvalType reports whether any registered persona handles the given
// eval_type (with default=true).
func (r *PersonaRegistry) HasEvalType(evalType string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list, ok := r.byEval[evalType]
	return ok && len(list) > 0
}
