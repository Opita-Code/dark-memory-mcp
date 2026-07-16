---
name: Feature request
about: Propose a new orchestrator, tool, or improvement
title: '[FEAT] '
labels: enhancement, needs-design
assignees: ''
---

## Summary

One-sentence description of the proposed feature.

## Motivation

What problem does this solve? Who benefits? What are the alternatives?

## Detailed design

How would this fit into the existing architecture?

### Affected components

- [ ] New tool in `internal/tools/<namespace>/`
- [ ] New orchestrator in `internal/orchestration/`
- [ ] New context projection in `internal/context/`
- [ ] New store method in `internal/store/`
- [ ] Schema migration
- [ ] Documentation only
- [ ] Other: ___________

### Wire-format impact

Does this change the canonical 26-tool order? If yes, this is a breaking change
for harnesses that index by position — needs a major version bump.

- [ ] No wire change (additive)
- [ ] Renumbering (breaking)
- [ ] New tool outside the canonical 25 (extended surface)

### INV-* impact

Which operational invariants are touched?

- [ ] INV-1 (write-path audit) — needs `WriteContext` plumbing
- [ ] INV-2 (per-session scoping)
- [ ] INV-3 (canary in writes)
- [ ] INV-4 (constitution audit)
- [ ] INV-5 (cache integrity)
- [ ] INV-6 (mod content sanitization)
- [ ] INV-7 (multi-tenancy / project isolation)
- [ ] None

## Alternatives considered

What other approaches did you consider and why didn't you pick them?

## Test plan

How will this be tested? Reference existing test suites or propose new ones.

## Backward compatibility

Is this backward-compatible with v1.0.x? If not, what's the migration path?

## Open questions

Anything unresolved. We discuss these in the issue thread before implementation.