# ADR-008: Estándar de trabajo — atomic spec mirror (specs + dark-memory)

> **Estado**: Aceptado (2026-09-27)
> **Autor**: Opita-AI + operador nico
> **Sustituye a**: ninguno. Establece una nueva convención para todas las specs futuras.

## 1. Contexto

Cada spec/ADR que escribimos termina como un archivo monolítico de 200-600
líneas. Para responder una pregunta puntual ("¿cuál es el rubric de C3?",
"¿qué hace EC-009?", "¿cuáles son las 5 preguntas para el operador?"),
yo (Opita-AI) tengo que releer el archivo entero cada vez. Eso:

- **Gasta tokens** que podrían usarse para razonar
- **Rompe la audit trail** cross-session: si el operador pregunta en sesión
  nueva "¿qué adoptamos en el ADR-007?", tengo que re-derivar todo
- **Confunde el recall**: `agent_memory_recall` devuelve la fila densa o
  nada; el LLM no puede recuperar "solo el EC-010"

El operador pidió (2026-09-27, sesión `sess-0afd66f0ffe5d5d6`):

> "Organiza el trabajo en documentos specs y en dark-memory para trabajar
> con el contexto ordenado y poderlo traer de manera atómica y este será
> nuestro estándar de trabajo. Haz que no lo olvides nunca."

## 2. Decisión

Adoptamos **atomic spec mirror** como disciplina obligatoria. Cada spec
vive en **dos lugares sincronizados**:

1. **Archivo monolítico** (`docs/decisions/ADR-NNN-slug.md` o
   `docs/specs/SPEC-NNN-slug.md`) — para humanos, sirve como narrative
   source of truth y como changelog
2. **Mirror atómico en dark-memory** — una `agent_memory_save` por
   sección atómica. Cada sección es **atómica** = una sola pregunta
   respondible sin leer otras secciones.

## 3. Reglas del mirror atómico

### 3.1 Esquema obligatorio por spec

Cada spec produce **N+1 filas** en `agent_memory`:

| # | Fila | Contenido | Tag distintivo |
|---|---|---|---|
| 0 | **SUMMARY** | 1-paragraph description + scope + status + file path | `spec-summary` |
| 1..N | **SECTION** (una por sección mayor del doc) | contenido denso de esa sección (≤4000 chars) | `spec-section` |

Cada sección mayor del archivo monolítico **debe** tener su fila espejo.
No se saltan secciones. Si una sección es trivial, su fila es de 1 línea.
Si una sección es larga (>4000 chars), se divide en sub-secciones, cada
una con su propia fila.

### 3.2 Tags de identificación

Toda fila del mirror usa estos tags (en orden):
```
spec:<id>           ← ej: spec:ADR-007
mirror:atomic       ← marca que es parte del mirror
<section-kind>      ← ej: pipeline, rubric, edge-cases, personas
```

Ejemplo para ADR-007 sección de pipeline:
```
tags: spec:ADR-007, mirror:atomic, pipeline, deterministic, llm-call
```

### 3.3 Pinned status

- Fila **SUMMARY**: `pinned=true`
- Filas **SECTION**: `pinned=false` (deja pinned para findings
  duraderos, no para mirror mecánico)

Excepción: si una sección es **axiomática** (ej: "rubric table es la
source of truth para pesos"), se pinea.

### 3.4 Bind session

Todas las filas del mirror usan `bind_session=true` para que la
modificación quede registrada en la misma sesión donde se escribió el
spec. Esto permite auditar: "esta fila se actualizó cuando ADR-007 se
revisó en sesión sess-X".

### 3.5 Recall pattern

Antes de leer un archivo de spec, **siempre** intentar:

```python
recall(query=<keywords>, scope=project, kind=note)
```

Si el recall devuelve la fila SUMMARY + la(s) fila(s) SECTION relevante(s),
NO leer el archivo. Si el recall es insuficiente (fila falta, sección
nueva), entonces leer la sección específica del archivo + actualizar el
mirror.

### 3.6 Update pattern

Cuando una sección cambia:

```python
1. Update archivo monolítico (Edit tool)
2. Update fila SECTION correspondiente (agent_memory_update)
3. NO crear nueva fila con sufijo "-v2" — update in-place
4. Si cambia scope/status, update también fila SUMMARY
```

Cuando una sección se agrega:

```python
1. Agregar al archivo monolítico
2. Crear nueva fila SECTION (nuevo id, mismo spec:<id>)
3. Update fila SUMMARY si cambia scope
```

Cuando una sección se elimina:

```python
1. Quitar del archivo monolítico
2. agent_memory_archive(id=<fila_sección>)
3. Update fila SUMMARY si cambia scope
```

### 3.7 Checklist obligatorio (no saltable)

Antes de cerrar cualquier spec, ejecutar este checklist:

- [ ] Archivo monolítico escrito en `docs/decisions/` o `docs/specs/`
- [ ] Fila SUMMARY guardada con `pinned=true`, `bind_session=true`
- [ ] Una fila SECTION por cada sección mayor del archivo
- [ ] Cada fila usa tags `spec:<id>, mirror:atomic, <section-kind>`
- [ ] Recall de prueba: query retorna SUMMARY + SECTIONs esperadas
- [ ] Si algún EC (edge case) está documentado, fila(s) dedicada(s)

## 4. Aplicación retroactiva

Las specs existentes (ADR-007 + las futuras) usan el estándar desde
día 1. Las filas densas previas (agent_memory rows 2033, 2036, 2037,
2040, 2042) se **dejan como están** — son resúmenes comprehensivos de
commits. La atomic mirror es para specs, no para commit summaries.

Si en el futuro el operador pide "mirror atómico retroactivo de las
filas densas de BUG-5/6/7/8", se hace bajo demanda (no preventivo).

## 5. Anti-patterns explícitos

| Anti-pattern | Por qué está mal |
|---|---|
| Crear la spec sin mirror | Rompe la promesa de "no olvidar"; pierde atomic recall |
| Mirror de un párrafo entero del archivo en una sola fila | No es atómico — vuelve al problema original |
| Mirror sin tag `spec:<id>` | No se puede agrupar por spec en queries |
| Crear fila nueva en lugar de update | Historial de auditoría se pierde |
| Skip el checklist | El próximo LLM en sesión nueva no sabrá que existe el mirror |
| Olvidar `bind_session=true` | El mirror queda flotando sin contexto de la spec session |

## 6. Consecuencias

### Positivas

- **Recall atómico**: "¿cuál es el threshold de aligned?" → 1 fila
- **Cross-session continuity**: nueva sesión arranca con context_recap
  que incluye la fila SUMMARY pinned
- **Audit granular**: cada update de una sección es trazable
- **Tokens ahorrados**: read 5 filas de 400 chars vs 1 archivo de 600 líneas
- **Disciplina explícita**: el checklist es la prueba de cumplimiento

### Negativas

- **Costo de setup**: escribir N+1 filas por spec agrega 5-10 min
- **Riesgo de drift**: si se actualiza el archivo sin actualizar el mirror,
  hay divergencia → el checklist lo mitiga pero no lo elimina
- **Tamaño de dark.db**: las filas del mirror ocupan espacio (mitigable
  con archive cuando la spec se depreca)

### Mitigaciones

- El checklist (§3.7) es ley: si no se cumple, la spec no se considera
  "shipped"
- El patrón `mirror:atomic` permite buscar todas las specs que cumplen
  el estándar y todas las que NO (`agent_memory_recall(query="mirror:atomic")`)

## 7. Estructura de directorios

```
docs/
├── decisions/              ← ADRs (arquitectura, decisiones)
│   ├── ADR-007-judge-pipeline-v4.md
│   └── ADR-008-work-standard.md (este)
├── specs/                  ← SPECs (implementación, contratos)
│   ├── SPEC-TEMPLATE.md    ← plantilla con checklist
│   └── SPEC-NNN-...md      ← futuras specs de implementación
└── ...
```

`docs/decisions/` para decisiones de arquitectura.
`docs/specs/` para contratos de implementación.

## 8. Estado actual de adopción

| Spec | Archivo | Mirror atómico | Status |
|---|---|---|---|
| ADR-007 | `docs/decisions/ADR-007-judge-pipeline-v4.md` | (pendiente en este commit) | in_progress |
| ADR-008 | `docs/decisions/ADR-008-work-standard.md` | este mismo commit | accepted |

A partir de ahora: **toda spec nueva DEBE** cumplir el estándar antes
de declararse shipped. ADR-007 + ADR-008 son las primeras en adoptarlo.

## 9. Decisión solicitada

El estándar queda **accepted** si:
- El operador confirma que la disciplina está clara
- El template en `docs/specs/SPEC-TEMPLATE.md` está alineado
- El primer mirror (ADR-007) cumple el checklist §3.7
