# SPEC-TEMPLATE — Plantilla para especificaciones con atomic mirror

> **Estado**: Aceptado (2026-09-27)
> **Gobernado por**: `docs/decisions/ADR-008-work-standard.md`
> **Uso**: copy-paste este archivo → renombrar a `SPEC-NNN-slug.md` → llenar

---

## Reglas duras

1. **Toda spec usa atomic mirror** (ver ADR-008 §3).
2. **Antes de declarar "shipped"**, ejecutar el checklist de §7.
3. **Recall antes de read**: si la pregunta ya tiene fila en dark-memory, NO leer el archivo.

---

## Plantilla del archivo monolítico

```markdown
# SPEC-NNN: <título>

> **Estado**: Propuesto | Accepted | Deprecated
> **Fecha**: YYYY-MM-DD
> **Autor**: <quien escribe>
> **Spec id**: SPEC-NNN
> **Mirror**: 1 fila SUMMARY + N filas SECTION en dark-memory

## 1. Contexto

<problema, motivación, antecedentes. ≤3 párrafos>

## 2. Decisión

<la decisión. 1 párrafo concreto>

## 3. <Sección mayor A>

<contenido>

## 4. <Sección mayor B>

<contenido>

## 5. <Sección mayor C>

<contenido>

## 6. Compatibilidad

<con qué interactúa, qué rompe, qué conserva>

## 7. Acceptance criteria

- [ ] criterio observable 1
- [ ] criterio observable 2
- [ ] criterio observable 3

## 8. Open questions

1. pregunta para el operador
2. pregunta para el operador
```

---

## Esquema de mirror atómico (obligatorio)

Por cada sección mayor del archivo (Contexto, Decisión, A, B, C, ...),
crear una fila en `agent_memory`:

```go
agent_memory_save(
  operator=<op>,
  kind=<kind>,                    // note | observation | decision | finding | todo | link | context
  title="SPEC-NNN §<n> <section-name>",
  content=<contenido denso de la sección, ≤4000 chars>,
  tags="spec:SPEC-NNN, mirror:atomic, <section-kind>, <topic-tags>",
  pinned=<true|false>,           // SUMMARY siempre true; SECTION false por defecto
  bind_session=true,
)
```

### Fila 0 — SUMMARY (siempre pinned)

```go
agent_memory_save(
  operator=<op>,
  kind="decision",
  title="SPEC-NNN summary",
  content=`
SPEC-NNN: <título corto>
Status: <Propuesto|Accepted|Deprecated>
File: docs/specs/SPEC-NNN-slug.md
Date: YYYY-MM-DD

TL;DR:
<1 párrafo>

Sections (mirror atómico):
- §1 Contexto: <1 línea>
- §2 Decisión: <1 línea>
- §3 <A>: <1 línea>
- §4 <B>: <1 línea>
- ...

Open questions:
- <q1>
- <q2>
`,
  tags="spec:SPEC-NNN, mirror:atomic, summary",
  pinned=true,
  bind_session=true,
)
```

### Filas 1..N — SECTION (una por sección mayor)

```go
agent_memory_save(
  operator=<op>,
  kind="note",  // o "decision" si la sección es una decisión
  title="SPEC-NNN §3 <A-title>",
  content=`
<contenido completo de la sección A, ≤4000 chars>
`,
  tags="spec:SPEC-NNN, mirror:atomic, <section-kind>, <topic-tags>",
  pinned=false,  // true solo si la sección es axiomática
  bind_session=true,
)
```

---

## Checklist §7 — antes de declarar shipped

Ejecutar en orden. **NO skip ninguno**.

```bash
# 1. Verificar archivo monolítico
[ -f "docs/specs/SPEC-NNN-slug.md" ] && echo "OK file"

# 2. Verificar fila SUMMARY
agent_memory_recall(query="spec:SPEC-NNN mirror:atomic summary", operator=<op>)
# Debe devolver 1 fila con title="SPEC-NNN summary"

# 3. Verificar filas SECTION (1 por sección mayor)
for sec in contexto decision <A> <B> ...; do
  agent_memory_recall(query="spec:SPEC-NNN $sec", operator=<op>)
done
# Cada sección debe devolver su fila

# 4. Verificar tags
# Cada fila debe tener: spec:SPEC-NNN, mirror:atomic

# 5. Verificar pinned
# SUMMARY: pinned=true
# SECTION: pinned=false (o true con justificación)

# 6. Verificar bind_session
# Todas las filas deben tener session_id != null

# 7. Live test
agent_memory_recall(query="<pregunta concreta de la spec>", operator=<op>)
# Debe devolver fila(s) relevantes sin necesidad de leer el archivo
```

---

## Anti-patterns (per dark-testing skill)

| Anti-pattern | Cómo lo evitamos |
|---|---|
| Spec sin mirror | Checklist §7 detecta antes de shipped |
| Mirror monolítico (1 sola fila) | El esquema §3 obliga a 1 fila por sección mayor |
| Tags sin `spec:<id>` | Checklist §4 lo verifica |
| Update en lugar de archive | §3.6 lo regla |
| Drift archivo vs mirror | Checklist §7 paso 7 (live test) lo detecta |

---

## Ejemplo: SPEC-001 mínimo

(Ver `docs/decisions/ADR-008-work-standard.md` §8 para el ejemplo vivo
de adoption retroactiva en ADR-007.)
