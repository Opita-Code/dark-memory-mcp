# SPEC-lucidez-rrules — LUCIDEZ R-rules v1.1 (documento canónico)

> **Estado**: Propuesto (2026-10-14, Sesión 8, spec 1997)
> **Versión del estándar**: v1.1 (R1–R17)
> **Predecesor**: v1.0 = R1–R10 (SPEC-alpha-11-phase13-house-keeping §6; fases cerradas quedan `grandfathered`)
> **Mirror**: SUMMARY pinned + 1 SECTION por bloque (R1–R10, R11–R17, versionado, gate)

## 1. Contexto

Las R-rules LUCIDEZ existen hoy solo incrustadas en el SPEC de Phase 13 §6,
contaminadas con contenido de fase (T-201..T-205), y el SPEC-TEMPLATE.md —
lo que los agentes realmente copian — no menciona LUCIDEZ en absoluto. El
estándar se transmite por imitación entre fases, no por referencia. Sesión 8
(Errores Observatory 539/540: drift_judge falla con mensaje genérico ante
SHA malformado o path ausente) demostró que los riesgos de operador no
tienen reglas: R1–R10 cubren honestidad de proceso, ninguna cubre
mitigación de error operativo.

## 2. Decisión

Crear este documento como ÚNICA fuente de verdad de las R-rules. Las fases
nuevas lo REFERENCIAN (`LUCIDEZ v1.1, 17/17 GREEN, ver SPEC-lucidez-rrules`),
no redefinen reglas. Se añaden R11–R17 (riesgo de operador) con esquema
fijo por regla: ID + nombre + enunciado + exige + verifica + aplica.

## 3. R1–R10 (v1.0, verbatim, con campo Aplica añadido)

| # | Regla | Aplica |
|---|---|---|
| R1 file:line | Todo commit cita file:line refs; hash pin unchanged | AMBOS |
| R2 no shallow | Trace + identify es mínimo 2 pasos; nada de fixes de 1 línea sin causa | GRANDE |
| R3 root cause over symptom | Elegir causa raíz entre hipótesis, no workaround | GRANDE |
| R4 honest cost | Costo honesto en LoC + tiempo antes de ejecutar | GRANDE |
| R5 3 real options | Decisiones con ≥3 opciones presentadas al operador | GRANDE |
| R6 declare unknowns | Preguntas abiertas declaradas, no escondidas | AMBOS |
| R7 honest cost (cierre) | Sin capacidad nueva; solo cierra diferido — declararlo | GRANDE |
| R8 audit trail | Atomic mirror por SPEC-TEMPLATE | AMBOS |
| R9 no lecture on protocol | Conciso; nada de sermones sobre el protocolo | AMBOS |
| R10 pause-and-summarize | Resumen fin-de-chunk en el commit message | GRANDE |

## 4. R11–R17 (v1.1, riesgo de operador)

### R11 inexpressible-by-default — Aplica: GRANDE
**Enunciado**: el camino correcto no requiere precisión manual.
**Exige**: todo input que hoy se copia a mano (SHA, path, endpoint) tiene un
modo que lo deriva del sistema (HEAD, repo root, defaults).
**Verifica**: el gate e2e del SPEC se ejecuta sin strings copiadas a mano;
si un SHA/path literal aparece en un log de sesión, R11 está en rojo.

### R12 no garbage rows — Aplica: AMBOS
**Enunciado**: ningún input inválido crea estado persistente.
**Exige**: validación completa antes del primer write.
**Verifica**: `TestOperatorRisk_<ID>_NoPersistentState`: conteos de tablas
iguales antes/después del input malo. (Referencia: publish_vibe pre-flight,
`publish_vibe_preflight_test.go`.)

### R13 actionable refusal — Aplica: toca-error
**Enunciado**: todo rechazo nombra qué falló, qué se esperaba, comando
copiable, y hasta 5 sugerencias cuando aplique.
**Exige**: si el diff toca un path de error, el mensaje lleva receta; si no
toca errores, N/A.
**Verifica**: el test asserts que el mensaje contiene la receta literal, no
solo el código. (Referencia: `resolveGit` + `PreflightGitRef`.)

### R14 noisy degradation — Aplica: GRANDE
**Enunciado**: ninguna degradación silenciosa; todo fallback emite caveat
con siguiente paso.
**Exige**: extiende honesty L11.2/L12.1 a errores de operador:
`needs_human` sin herramienta nombrada = rojo.
**Verifica**: el caveat nombra tool o doc; test de regresión por fallback.

### R15 recurring-error-to-runbook — Aplica: PROCESO
**Enunciado**: firma con count ≥ 2 y unresolved exige entrada de runbook +
fix + test; el evento solo se cierra citando el commit del fix.
**Exige**: no es por fase — se evalúa al cerrar sesión si el Observatory
tiene count≥2 en el área tocada.
**Verifica**: el evento enlaza commit; reapertura = fix incompleto.

### R16 negative test per gap — Aplica: GRANDE
**Enunciado**: cada riesgo del registro tiene su test adversarial
`TestOperatorRisk_<ID>_*`.
**Exige**: cobertura 1:1 riesgo↔test; lo sin test figura `accepted` con
disclosure, nunca `mitigated`.
**Verifica**: el register enlaza test por fila; fila sin test + estado
`mitigated` = rojo.

### R17 audit never weakens — Aplica: AMBOS
**Enunciado**: ninguna mejora de UX reduce fijación/auditoría.
**Exige**: la auto-derivación registra QUÉ fijó y exige precondición
verificable (tree limpio) o caveat explícito.
**Verifica**: modo auto sin clean-tree check = rojo aunque el UX sea perfecto.

## 5. Versionado y grandfathering

- v1.0 = R1–R10. Fases cerradas quedan `10/10 grandfathered`; no se reescriben.
- v1.1 = R1–R17. Rige desde su aceptación hacia adelante.
- R18+ requiere: decisión pinned en cuaderno + update de este doc + bump de
versión. Reglas existentes jamás se editan retroactivamente.

## 6. Formato de reporte del gate

Una línea por regla en commit message y v4-status.md:

```
LUCIDEZ v1.1 15/17 GREEN + 2 accepted
  R11 RED → accepted: modo auto diferido a Sesión 9 (riesgo R-auto, workaround: SHA vía git rev-parse)
  R15 GREEN (539/540 cerrados citando <sha>)
```

Lo único prohibido es un rojo sin disclosure. `15/17 + 2 accepted` es verde
honesto; `15/17` a secas es rojo.

## 7. Acceptance criteria

- [ ] Este doc existe con R1–R17 en esquema fijo
- [ ] SPEC-TEMPLATE.md §9 referencia este doc (no redefine reglas)
- [ ] `docs/operator-risk-register.md` seed R1–R10 existe
- [ ] INV-20 propuesto en INVARIANTS.md
- [ ] Mod `user/lucidez-operator-risk` registrado (visible en active_policy)
- [ ] Gate Sesión 8 reportado en formato §6 (dogfood)

## 8. Open questions

1. ¿`risk_class=governance` nuevo en `internal/mods/types.go`, o basta
   `research-only`/`local_only` para mods de gobernanza? (Sesión 8 usa los
   existentes + disclosure; el enum nuevo es follow-up con cambio de código.)
2. ¿Junction de enable/disable de mods (W4+ pendiente en active_policy.go:103)
   antes o después del modo `auto` de Sesión 9?
