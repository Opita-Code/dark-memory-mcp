# Pull Request

## What does this PR do?

One-sentence summary. Link the issue it closes (`Closes #123`).

## Type of change

- [ ] Bug fix (non-breaking change that fixes an issue)
- [ ] New feature (non-breaking change that adds functionality)
- [ ] Breaking change (fix or feature that would cause existing functionality to change)
- [ ] Documentation only

## How was this tested?

- [ ] Ran `go test -count=1 ./...` — all 9 suites green
- [ ] Added new tests for the change (which?)
- [ ] Tested manually (describe)

If this affects an operational invariant (INV-1..7), name it and the defensive
test that covers it:

```
INV-X: <which one> — defensive test: tests/<suite>/<file>.go
```

## Spec

If this is a non-trivial change, link the spec:

```
Spec: dark.db spec_id N (session: <session_id>)
Drift log: drift_id M (verdict: aligned | drift_resolved)
```

Per [CONTRIBUTING.md](CONTRIBUTING.md), non-trivial changes must go through
spec → artifact → drift → publish.

## Checklist

- [ ] Code is formatted (`gofmt -s -w .`)
- [ ] All exported types/functions have godoc comments
- [ ] No new linting warnings
- [ ] Tests pass locally
- [ ] Docs updated (if operator-facing change)
- [ ] No secrets in code or comments
- [ ] Branch is up to date with `main`

## Breaking changes

If you checked "Breaking change" above, describe the migration path:

```text
Before: <old behavior>
After: <new behavior>
Migration: <how to upgrade>
```

## Screenshots / logs

If relevant, paste logs or screenshots.

## Reviewer notes

Anything specific you want reviewers to look at. Cite line numbers or files.