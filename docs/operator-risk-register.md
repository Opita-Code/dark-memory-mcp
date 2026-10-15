# Operator Risk Register (vivo)

> Fuente canónica de reglas: `docs/specs/SPEC-lucidez-rrules.md` v1.1.
> Cada fila enlaza: trigger, evidencia, capa de mitigación (1–7), mitigación
> con file:line, test adversarial, firma del Observatory, estado.
> Estados: `open` | `mitigated` (con test) | `accepted` (con disclosure abajo).

## Filas

| ID | Trigger (input exacto) | Evidencia | Capa | Mitigación (file:line) | Test adversarial | Firma Observatory | Estado |
|---|---|---|---|---|---|---|---|
| R1 | `artifact_ref.git_sha` malformado/inexistente | drift 1587 (SHA inventado por subagente) | 2 | `internal/artifact/git_preflight.go:PreflightGitRef` + pre-flight en `publish_vibe.go` | `TestPreflight_MalformedSHA`, `TestPublishVibe_Preflight_BadSHACreatesZeroRows` | ErrInternal `git cat-file … exists on disk, but not in` | mitigated (Sesión 8) |
| R2 | `artifact_ref.path` ausente en el SHA (branch stale) | drifts 1585, errores 539/540 | 2 | idem R1 + sugerencias basename | `TestPreflight_MissingPath*`, `TestPublishVibe_Preflight_MissingPathCreatesZeroRows` | idem R1 | mitigated (Sesión 8) |
| R3 | Publicar creyendo que el judge ve el tree sucio (`kind: file` sin pin) | latente, sin incidente | 1 | DIFERIDO a Sesión 9: modo `auto` (`kind: head` + clean-tree check) | — | — | accepted (ver disclosure) |
| R4 | NLIConfig con endpoint/token erróneo | row 1370 / Phase H5 (histórico) | 2+4 | `llm_provider_probe` + mensajes accionables | existentes (probe) | — | mitigated (histórico) |
| R5 | Token OAuth expirado sin refresh | — (preventivo, Sesión 7) | 3 | `ErrConnectionExpired` sticky + mensaje con `oauth_connect` | `TestOAuthNLIProvider_HandlesConnectionExpired` | — | mitigated (Sesión 7) |
| R6 | Operar contra binario viejo sin saberlo (tag publicado = 73 tools vs HEAD local = 78) | Sesión 8: live en alpha.31 con Sesión 6/7 dormidas; doc-audit 2026-10-15: el check `health_ping.git.commit` vs `rev-parse HEAD` es el que delata al binario stale | 4 | check binario-vs-HEAD al abrir sesión (agent config) | manual (health_ping vs rev-parse) | — | mitigated-proceso (Sesión 8) |
| R7 | Secretos en env vars plaintext en producción | documentado en `keystore.go` (MUST NOT) | 4 | Solo documentado; sin enforcement | — | — | accepted (ver disclosure) |
| R8 | `resolve_drift(accept)` a ciegas | anti-patrón #4 del harness | 4 | Solo documentado | — | — | accepted (ver disclosure) |
| R9 | Sesiones colgadas / `session_start` por turno | `sess-5dbc5f8c22dffbdc` colgada | 4 | Solo documentado | — | — | accepted (ver disclosure) |
| R10 | Cuaderno ensuciado con datos efímeros | anti-patrón ("si no sirve la próxima semana…") | 4 | Solo documentado | — | — | accepted (ver disclosure) |

## Disclosures de riesgos aceptados

- **R3 aceptado**: publicar contra working tree sin pin puede juzgar bytes
  distintos a los commiteados. Workaround: commitear antes de publicar y
  pasar el SHA de `git rev-parse HEAD`. Revisión: Sesión 9 (modo `auto`).
- **R7 aceptado**: nada impide `DARK_OAUTH_*` con secretos en plaintext en
  producción. Workaround: OS keyring siempre que exista; env solo en CI/dev
  efímero. Revisión: cuando haya despliegue multi-operador.
- **R8/R9/R10 aceptados**: mitigación solo-documentada en el agent config.
  Workaround: disciplina del operador + revisión en cada apertura de sesión.
  Revisión: si aparecen incidentes (firma: sesiones `closed_aborted`,
  verdicts aceptados con drift real).

## Hallazgo Sesión 8 (R6 aplicado al propio proceso)

- `mods/vibe-loop-git/LICENSE` (CC-BY-NC-4.0 verbatim, requerido por
  `mod.json:licenseNote` "See ./LICENSE file") existía en disco pero NUNCA
  se commiteó (commit 499466b tocó `LICENSE` raíz + headers, no este
  archivo). El dual-licensing estaba incompleto en git. Incluido en el
  commit de Sesión 8 con este disclosure. Detectado gracias al cambio
  `.gitignore` `mods/` → `mods/*` (los untracked ocultos emergieron).
