# ADR-007: judge pipeline v4 — pipeline de verificación agnóstico con evidencia y rigor

> **Estado**: Propuesto — pendiente review del operador
> **Fecha**: 2026-09-27
> **Autor**: Opita-AI (session `sess-f16a105757ef3b4f`)
> **Sustituye a**: ningún ADR previo. Hereda patterns de `dark-sdd ADR-003 (LLM-as-judge local)` y `dark-sdd ADR-006 (auto-grounding)` — ver §6 Compatibilidad.

## 1. Contexto

`dark-memory-v4` necesita un juez que verifique que un artifact publicado cumple
la spec intent del operador. En `v4-alpha.1` el `NoOpJudge` (`internal/v4alpha/judge/noop.go`)
devuelve siempre `VerdictAligned` con `Confidence=1.0`. Es un placeholder honesto
(la doc lo dice) pero no cumple el rol real.

El operador pide un juez con cinco propiedades:

1. **Pipeline de verificación real**, no "ask an LLM and trust".
2. **Nota de temperatura y evidencias** explícitas en cada verdict.
3. **Rigor de pasos** verificable; separación entre extracción y decisión.
4. **Agnóstico**: lógica, código, videos visuales, pipelines, opiniones.
5. **Defensa contra prompt injection**; basado en evidencia, no en claims.
6. **Pesos declarados** sobre criterios atómicos; thresholds reproducibles.
7. **Edge cases catalogados** antes de producción.
8. **Vibe-case aware**: rubric distinto por caso (C1 código ≠ C2 texto ≠ C5 bundle).

Esta ADR diseña ese juez. La motivación central: **el juez debe ser el revisor
que mi propio trabajo necesita** — debe cazar las fallas que yo cometí en los
últimos turnos (lista en §1.1) y que un LLM genérico pasa por alto.

### 1.1 Fallas reales del operador (última semana) que el juez cazaría

| # | Falla real | Edge case que la cazaría |
|---|---|---|
| F1 | agent_memory row 2040: claim "switched to regular FTS5" contradice `agent_memory.go:116-117` que sigue contentless | **EC-010** doc-vs-code drift |
| F2 | `docs/v4-status.md` tabla "32 tools deferred" — suma aritmética real: 28 base + 6 judge_util = 34 | **EC-009** arithmetic mismatch |
| F3 | Afirmación "session.recover/resurrect faltan" sin grep output | **EC-014** evidence missing file:line |
| F4 | "BUG-9 = 32 tools + audit + Judge" — copy-paste del row 2040 sin reconteo | **EC-011** scope over-claim |
| F5 | "All 8 schemas apply" sin enumerar cuáles | **EC-014** evidence missing |
| F6 | Confusión "no existe internal/v4alpha/error_obs/" vs `transport/mcp/error_obs.go` registra las 4 tools | **EC-013** prior-version contamination (mezcla v4-alpha.1 layout con intent v3) |
| F7 | Verdict=aligned implícito cuando reasoning=listaba problemas | **EC-015** verdict/reasoning inconsistency |

El juez debe cazar F1-F7 de forma rutinaria. Las definiciones viven en §4.

## 2. Decisión

Adoptamos un **pipeline de 7 pasos** con un único LLM call en el centro y
deterministic gates alrededor. La separación extracción/decisión es la
propiedad central.

### 2.1 Diagrama del pipeline

```
INPUT (artifact_ref + spec_intent + vibe_case + persona_id)
   │
   ▼
[1] Vibe-Case Router ───────────────────┐ deterministic
   │ Lookup persona default for          │ (table keyed by
   │ vibe_case + eval_type               │  vibe_case + eval_type)
   ▼                                     │
[2] Evidence Extractor ──────────────────┤ deterministic + LLM-free
   │ - file/git_sha/url/spec_id/         │ (regex, grep, byte-range
   │   artifact_id → fetch + chunk       │  read; never LLM)
   │ - claim-snippet pairs               │
   │ - structured fields only            │
   ▼                                     │
[3] Edge-Case Pre-Flight ───────────────┤ deterministic
   │ - empty artifact?                   │ (catalog check; see §4)
   │ - injection in artifact text?       │
   │ - size > context window?            │
   │ - self-reference (judge LLM == author LLM)?
   │ - position bias potential (pairwise)?
   │ → if any EC-NN severity >= error,   │
   │   short-circuit to needs_human      │
   ▼                                     │
[4] Persona + Rubric Resolver ──────────┤ deterministic
   │ - persona Markdown override         │ (spec 1155 v14 field-level
   │   merged with compiled default      │  merge, already implemented)
   │ - rubric weights loaded             │
   ▼                                     │
[5] LLM Verdict Call ────────────────────┘ single call, T=0.0 default
   │ - structured prompt (template,      │
   │   no concatenation with user content)│
   │ - CoT + form-filling (G-Eval §6)    │
   │ - output JSON schema strict         │
   ▼
[6] Verdict Aggregator ─────────────────┐ deterministic
   │ - extract per-criterion scores      │ (regex against schema)
   │ - apply weighted sum                │
   │ - threshold → label                 │
   │ - verifier: does evidence[i] snippet│
   │   actually support verdict label?   │
   │   (independent cheap check)         │
   ▼                                     │
[7] Audit Emitter ──────────────────────┘ deterministic
   - write_audit row (INV-1)
   - sdd_evaluations row (legacy compat, optional)
   - persona_id + rubric_version recorded

OUTPUT: structured Verdict (§3)
```

**Punto crítico**: solo [5] hace un LLM call. [1][2][3][4][6][7] son
deterministic y reproducibles bit-a-bit. Esto ataca F1 directamente — la
extracción de evidencia en [2] lee la fuente literal, no la resume un LLM.

### 2.2 Persona registry (heredado, extendido)

Se mantiene el modelo de 8 personas compiladas + Markdown overrides de
`docs/judge-personas.md` (legacy). Extensiones para v4:

| Persona (legacy) | v4 eval_types adicionales | Notas v4 |
|---|---|---|
| `judge-logical` | drift_judge, spec_test_alignment, **arithmetic_mismatch** | gana EC-009 como eval_type canónico |
| `judge-visual` | brand_match | unchanged |
| `judge-security` | pii_detect, prompt_injection_scan, security_coverage | gana EC-003 como prompt-injection scanner |
| `judge-compositional` | mindset_compose, mindset_quality | unchanged |
| `judge-mutation` | mutation_score_check | unchanged |
| `judge-resilience` | resilience_check | unchanged |
| `judge-evidential` | grounding_check, **doc_vs_code_drift** | gana EC-010 + EC-011 |
| `judge-coverage` | (none) | ahora también: edge_case_catalog_audit |

3 nuevas personas v4:

| Persona (v4) | eval_types | Cuándo se invoca |
|---|---|---|
| `judge-cross-modal` | visual_artifact_eval, audio_artifact_eval, video_artifact_eval | C3-C4 vibe-cases |
| `judge-pipeline` | pipeline_eval, workflow_eval | C6 vibe-case (infra + pipelines) |
| `judge-opinion` | opinion_eval, claim_audit | C2 vibe-case cuando spec_intent es opinión/argumento |

Las nuevas personas se compilan (no se override-only por Markdown) per
spec 1155 v14 §5. Ver `docs/judge-personas.md` línea 167.

### 2.3 Vibe-case rubric (núcleo del rigor)

Cada vibe_case (C1..C7) tiene su propio `Rubric` con criterios atómicos
y pesos. Pesos suman 1.0. Los criterios son **DAGMetric-style**:
independientes, verificables, con un yes/no o score [0,1] claro.

| Vibe | Persona default | Criterios (con pesos ejemplo) |
|---|---|---|
| **C1 code** | judge-logical | correctness 0.35, tests 0.25, security 0.20, idiomatic 0.10, docs 0.10 |
| **C2 text** | judge-logical | faithfulness 0.30, intent_match 0.25, style 0.15, completeness 0.15, grammar 0.15 |
| **C3 image** | judge-cross-modal | composition 0.25, color_palette 0.20, subject_match 0.25, accessibility 0.15, no_artifact 0.15 |
| **C4 video** | judge-cross-modal | subject_consistency 0.30, scene_transitions 0.20, audio_sync 0.15, narrative 0.20, duration_match 0.15 |
| **C5 bundle** | judge-coverage | provenance 0.30, lockfile_parity 0.25, transitive_safety 0.20, manifests_consistent 0.15, audit_chain 0.10 |
| **C6 infra** | judge-pipeline | idempotence 0.25, observability 0.20, rollback 0.20, blast_radius 0.20, security 0.15 |
| **C7 governance** | judge-evidential | invariant_compliance 0.30, audit_chain 0.25, rbac 0.20, provenance 0.15, documentation 0.10 |

**Threshold table** (suma ponderada → label):

| Range | Label |
|---|---|
| `[0.85, 1.0]` | `aligned` |
| `[0.50, 0.85)` | `needs_human` |
| `[0.00, 0.50)` | `drift_detected` |

Por criterio individual, si algún criterio con peso ≥ 0.20 cae < 0.30,
override: `needs_human` (no `aligned` aunque la suma pase). Esto previene
que un criterio crítico pase desapercibido.

### 2.4 Anti-prompt-injection (3 capas)

| Capa | Mecanismo | Cuándo |
|---|---|---|
| **L1 — input** | Structured fields only. El artifact se **chunkifica** y se pasa como `evidence[]` (lista de `{source, snippet, hash}`), nunca como bloque libre concatenado al prompt. | [5] antes del LLM call |
| **L2 — prompt** | Template fijo con placeholders tipados. El provider HTTP recibe un JSON schema estricto (`response_format: {type: "json_schema", schema: VerdictSchema}`). Anthropic/OpenAI soportan esto nativamente. | [5] |
| **L3 — output** | Schema validation post-LLM. Cualquier desviación → `errored` + retry con T=0 + persona fallback. Si el response contiene texto libre fuera del schema → `errored` + needs_human. | [6] |

Las 3 capas juntas son las que cazan F3 (artifact con "this code is great"
intentando sesgar al juez): el artifact se chunkifica, las "opiniones" del
artifact van como evidencia con su `relevance_score` calculado, y el juez
evalúa cada criterio contra la evidencia — no contra el texto bruto.

### 2.5 Verifier independiente (cheap post-check)

Después del LLM verdict en [5], el paso [6] corre un **verifier** que
NO usa el mismo LLM:

```
para cada evidence[i] referenciada en la reasoning:
   si evidence[i].snippet contradice label == aligned → override drift_detected
   si verdict=aligned pero reasoning menciona "drift", "missing", "fail" → override needs_human
```

Esto caza F7 (verdict=aligned + reasoning=drift) y F1 (snippet="contentless"
+ verdict="regular FTS5" → override).

El verifier es **regex + string-match** sobre los snippets extraídos en [2].
No usa LLM. Es 100% reproducible.

### 2.6 Temperature note (auditable)

Cada verdict incluye un campo `TemperatureNote` con:

```yaml
provider: anthropic | minimax | minimax-cn | openai | deepseek
model: <model_id>
temperature: 0.0  # default; raise for diversity in consensus
seed: <int>  # if supported
max_tokens: 2048
timeout_ms: 15000
top_p: 1.0
persona_id: judge-logical
rubric_version: sha256:<hex16>
schema_version: v4alpha/2026-09-27/001
```

Reproducibilidad: el mismo input + la misma `temperature_note` produce el
mismo verdict (modulo provider non-determinism, que se reporta en el note).

## 3. Verdict shape (estructura del output)

```go
package judge

type Verdict struct {
    // Core (heredado de v4-alpha.1)
    Verdict    string  // aligned | drift_detected | needs_human | errored
    Confidence float64 // [0.0, 1.0]
    Reasoning  string  // ≤ 280 chars; 1-3 sentences max

    // v4 extensions
    PersonaID       string         // persona que se aplicó
    RubricVersion   string         // sha256[:16] del rubric usado
    Criteria        []Criterion    // per-criterion score + weight
    Evidence        []Evidence     // file:line + snippet + relevance
    EdgeCaseHits    []EdgeCaseHit  // EC-001..EC-015 que se dispararon
    TemperatureNote TemperatureNote // provider/model/T/seed
    BiasAudit       BiasAudit      // position/verbosity/self-enhancement mitigations applied
}

type Criterion struct {
    Name   string  // e.g., "correctness", "tests"
    Score  float64 // [0,1]
    Weight float64 // sum across criteria = 1.0
    Note   string  // ≤ 120 chars
}

type Evidence struct {
    Source         string  // file:line, URL, or chunk-id
    Hash           string  // sha256 of the snippet (truncated)
    Snippet        string  // 1-3 line excerpt
    RelevanceScore float64 // [0,1], deterministic from extraction
}

type EdgeCaseHit struct {
    CatalogID   string // EC-001..EC-015
    Description string
    Severity    string // info | warn | error | fatal
    Action      string // flagged | escalated | overridden
}

type TemperatureNote struct {
    Provider     string  `json:"provider"`
    Model        string  `json:"model"`
    Temperature  float64 `json:"temperature"`
    Seed         int64   `json:"seed,omitempty"`
    MaxTokens    int     `json:"max_tokens"`
    TimeoutMs    int     `json:"timeout_ms"`
    TopP         float64 `json:"top_p"`
    PersonaID    string  `json:"persona_id"`
    RubricVersion string `json:"rubric_version"`
    SchemaVersion string `json:"schema_version"`
}

type BiasAudit struct {
    PositionSwap    bool   // applied for pairwise (Zheng et al. 2023)
    RandomOrderSeed int64  // seed used for randomization
    Counterfactual  bool   // counterfactual examples in prompt
    Notes           string
}
```

`Validate()` extiende el chequeo de v4-alpha.1 para exigir `len(Criteria) > 0`,
`sum(weight) ≈ 1.0` (tolerance 0.001), y `len(Evidence) > 0` cuando
`eval_type ∈ {drift_judge, grounding_check, doc_vs_code_drift}`.

## 4. Edge case catalog (v1 — extensible)

15 casos iniciales. Cada uno tiene un **deterministic check** en [3].
Si se dispara con `severity >= error`, el pipeline short-circuits a
`needs_human` (no gasta el LLM call).

| ID | Trigger | Severity | Short-circuit verdict | Caza falla real |
|---|---|---|---|---|
| EC-001 | Artifact vacío o `len(content) == 0` | error | needs_human | general |
| EC-002 | LLM provider devuelve 5xx o timeout | fatal | errored | infra |
| EC-003 | Prompt injection detectada en artifact (regex contra 10 patrones de spec v3 §6.6.3) | fatal | drift_detected | F-injection |
| EC-004 | Artifact size > 80% del context window del provider | error | needs_human | infra |
| EC-005 | `persona_id` no registrado en registry | error | errored | config |
| EC-006 | `spec_intent` vacío o ≤ 10 chars | error | needs_human | config |
| EC-007 | `persona_id == "judge-..."` && provider == `same_as_author_provider` && model fingerprint matches | warn | (continue) | self-bias |
| EC-008 | Eval type = pairwise && position swap no aplicado | warn | (continue) | position bias |
| EC-009 | Artifact contiene arithmetic claim (regex `\d+\s*[\+\-\*\/]\s*\d+\s*=\s*\d+`) que el extractor verifica con calculator | warn | (continue, downgraded verdict) | F2 |
| EC-010 | Artifact contiene path:line y claim sobre ese path; extractor re-lee y compara | error | drift_detected si contradice | F1 |
| EC-011 | Artifact lista N items enumerados; claim dice "all N" pero enumeración real ≠ N | error | needs_human | F4 |
| EC-012 | Artifact contiene assertion condicional ("assuming X", "if Y") que no se explicita en spec_intent | warn | (continue) | implicit assumption |
| EC-013 | Artifact menciona paths o behavior de v2.20.0 cuando schema = `v4alpha/*` | warn | (continue, EC-013 tag en output) | F6 |
| EC-014 | Artifact hace claim sin file:line ref para un dato verificable | warn | (continue, EC-014 tag en output) | F3, F5 |
| EC-015 | LLM verdict reasoning contiene "drift", "missing", "fail", "fail-closed" y verdict=aligned | error | override → needs_human (verifier) | F7 |

Los ECs 003/007/009/010/011/013/014 son **específicos a las fallas del operador**
y son el valor central de este diseño.

## 5. Plan de implementación (orden de commits)

### Commit 1: ADR + interfaces (este, si se aprueba)

- `docs/decisions/ADR-007-judge-pipeline-v4.md` (este doc)
- `internal/v4alpha/judge/types.go` — Verdict + Criterion + Evidence + EdgeCaseHit + TemperatureNote + BiasAudit
- `internal/v4alpha/judge/rubric.go` — Rubric interface + registry keyed by vibe_case + weights
- `internal/v4alpha/judge/edge_cases.go` — los 15 ECs como `func(ctx, input) []EdgeCaseHit`
- `internal/v4alpha/judge/extractor.go` — Evidence Extractor (deterministic)
- `internal/v4alpha/judge/verifier.go` — cheap post-check
- `internal/v4alpha/judge/pipeline.go` — los 7 pasos como composable functions
- `internal/v4alpha/judge/pipeline_test.go` — tests for each EC + adversarial fixtures

### Commit 2: LLM-backed Judge + Consensus

- `internal/v4alpha/judge/llm.go` — `LLMJudge` con HTTP client a `DARK_JUDGE_PROVIDER`
- `internal/v4alpha/judge/consensus.go` — N-shot (N ≤ 7) con modal verdict
- `internal/v4alpha/judge/personas_v4.go` — 3 personas nuevas (judge-cross-modal, judge-pipeline, judge-opinion)
- `internal/v4alpha/judge/personas_v4_test.go`
- Wiring en `transport/mcp/server.go` (4 nuevos tools: judge, consensus, judgment_history, judge_list_personas)
- T0 test fixtures con `httptest.Server` (mock provider, sin key real)

### Commit 3: Audit emission + INV-1 closure

- `agent_memory.Save/Update/Archive` emiten `write_audit` row (INV-1 closure)
- `judge.Pipeline.Evaluate` emite `sdd_evaluations` row (legacy compat)
- `error_resolve` audit emission (ya lo hace, verificar)
- `audit_log` queries en `judge.judgment_history` tool

### Commit 4: Operator-facing docs

- `docs/judge-pipeline-v4.md` — el "como usar el juez" para operadores
- `docs/edge-case-catalog.md` — los 15 ECs + cómo extender
- `docs/persona-registry-v4.md` — 11 personas (8 legacy + 3 v4) + overrides
- Update `docs/v4-status.md` + `CHANGELOG.md` (entry [4.0.0-alpha.2])

### Test discipline (per `dark-testing` skill)

- **L1 rapid**: 20 iters de property-based tests sobre inputs random
- **L2 stdlib**: cada EC con 1 fixture positive + 1 negative (gold-standard)
- **L3 mutation**: ≥ 0.80 sobre `judge/edge_cases.go` + `verifier.go`
- **L4 drift_judge**: ≥ 0.85 sobre el prompt template (cuando vuelva el drift_judge real, no NoOp)

### Deliberate breaks (per `dark-cli` TDD discipline)

Por cada EC nuevo (15 total), introducir 1 break deliberado y verificar que el test lo caza:

| EC | Break | Test que cazaría |
|---|---|---|
| EC-009 | quitar el calculator → el extractor devuelve 0.0 relevance en evidence de arithmetic claims | TestEdgeCase009_ArithmeticMismatch |
| EC-010 | cambiar el extractor para no re-leer el path:line | TestEdgeCase010_DocVsCodeDrift |
| EC-015 | quitar el override del verifier | TestEdgeCase015_VerdictReasoningInconsistency |

## 6. Compatibilidad con lo que ya existe

| Componente legacy | Estado en v4 | Notas |
|---|---|---|
| `internal/v4alpha/judge/client.go::Judge` interface | **se mantiene** | signature: `Evaluate(ctx, evalType, specIntent, ref) (*Verdict, error)` |
| `NoOpJudge` | **se mantiene** como fallback cuando no hay provider configurado | tests + dev |
| 8 personas compiladas en spec 1155 | **se mantienen** | `judge-personas.md` line 22-30 |
| Markdown override via `DARK_JUDGE_PERSONAS_DIR` | **se mantiene** | field-level merge ya funciona |
| `SDDEvaluation` audit row | **se mantiene** | legacy compat; v4 añade `verdict_v4` que apunta al struct nuevo |
| `DARK_JUDGE_PROVIDER` env | **se mantiene** + se documenta la lista de providers | anthropic, minimax, minimax-cn, openai, deepseek |
| `dark-sdd ADR-003` (MiniMax-M3 local) | **heredado** | default provider = minimax con `DARK_JUDGE_DIALECT=anthropic` |
| `dark-sdd ADR-006` (auto-grounding interceptors) | **NO se aplica aquí** | eso es dark-sdd runtime; este ADR es el juez estático |

**Breaking change**: `Verdict` struct cambia. Los campos legacy
`(Verdict, Confidence, Reasoning)` se mantienen pero el struct se extiende.
Pipeline callers deben pasar de `Verdict{Verdict: "aligned"}` a
`Verdict{PersonaID: "judge-logical", ...}`. Migración: 1 commit + update
callers in `vibe/pipeline.go`.

## 7. Trade-offs (lo que NO se hace)

1. **No** añadimos multi-judge ensemble por defecto. Consensus (N-shot) está disponible via tool `consensus`, pero el flow normal es 1 call. Razón: costo y latencia; los ECs ya cubren las fallas más comunes.
2. **No** añadimos streaming del LLM. Pipeline es request/response. Razón: para evaluar artifacts típicamente < 8KB, el streaming no aporta.
3. **No** añadimos embeddings para semantic dedupe de evidence. La relevance_score es determinista (BM25 o char-overlap). Razón: dependencia de embeddings todavía no es v4-ready.
4. **No** añadimos un modelo de judge local fine-tuned (Prometheus 2-style). ADR-003 lo menciona como opción futura; este ADR lo deja como "v4.1 si los 3 personas nuevas no son suficientes".
5. **No** añadimos un test suite adversarial con red-teaming automático. Los 15 ECs son los tests; el catálogo se extiende a medida que aparezcan nuevas fallas.
6. **No** cambiamos el `NoOpJudge` para que devuelva algo distinto. Sigue siendo fallback honesto: si no hay provider, verdict="aligned" + reasoning explica por qué (transparencia).

## 8. Criterios de aceptación para considerar este ADR "shipped"

- [ ] Los 15 ECs tienen 1 fixture positive + 1 fixture negative cada uno (30 tests mínimo)
- [ ] Mutation score ≥ 0.80 sobre `judge/edge_cases.go` + `judge/verifier.go`
- [ ] Live verification: 1 artifact C1 code + 1 artifact C5 bundle, ambos veredictos coherentes con gold standard
- [ ] Los 3 ECs que cazan fallas reales del operador (EC-009, EC-010, EC-015) tienen tests que fallan si los rompes deliberadamente
- [ ] Documentation: `docs/judge-pipeline-v4.md` explica cómo extender el catálogo de ECs
- [ ] Audit trail: cada verdict persiste `PersonaID` + `RubricVersion` + `TemperatureNote` (reproducibilidad)
- [ ] Consensus tool funciona (N=1, 3, 5) con tests que verifican modal verdict + confidence por agreement

## 9. Decisión solicitada al operador

1. **¿Apruebas el ADR tal cual?** O quieres ajustes en:
   - Los 7 pasos del pipeline (¿falta alguno? ¿sobra alguno?)
   - Los 15 ECs (¿faltan los específicos a tu workflow?)
   - Las 11 personas (8 legacy + 3 v4) — ¿necesitamos alguna específica de dark-opita-market?
   - Las threshold tables (0.85 / 0.50 — ¿estrictas o laxas?)
2. **¿Empezamos por commit 1 (interfaces) o commit 2 (LLM-backed + 4 tools)?** Mi recomendación: commit 1 primero para que el contrato esté pinned antes de tocar el LLM.
3. **¿Hay alguna falla más que el operador haya visto en su propio trabajo y que el juez deba cazar?** Si la lista de §1.1 está incompleta, dímelo y agrego los ECs antes de empezar.

---

## Apéndice A: Referencias tier-1 (papers)

1. **G-Eval** — Liu et al. 2023, arxiv [2303.16634](https://arxiv.org/abs/2303.16634). NLG evaluation con CoT + form-filling. Spearman 0.514 con humanos en summarization. Sesgo hacia LLM-generated texts detectado.
2. **Prometheus 2** — Kim et al. 2024, arxiv [2405.01535](https://arxiv.org/abs/2405.01535), EMNLP 2024. Open-source evaluator LM, soporta direct assessment + pairwise ranking + custom criteria. Mayor correlación con humanos y GPT-4 que otros open judges.
3. **MT-Bench / Chatbot Arena** — Zheng et al. 2023, arxiv [2306.05685](https://arxiv.org/abs/2306.05685), NeurIPS 2023. GPT-4 judge alcanza >80% agreement con humanos. Identifica position bias, verbosity bias, self-enhancement bias, limited reasoning.
4. **Self-RAG** — Asai et al. 2023, arxiv [2310.11511](https://arxiv.org/abs/2310.11511). Reflection tokens (`<retrieve>`, `<isrel>`, `<issup>`, `<isuse>`) para self-critique. Outperforms ChatGPT + Llama2-chat en factuality.
5. **dark-sdd ADR-003** (2026-07-10) — MiniMax-M3 local como juez primario. Heredado por este ADR.
6. **dark-sdd ADR-006** (2026-07-10) — Auto-grounding via interceptors. NO aplica a este ADR (es runtime en dark-sdd, no en dark-memory).
7. **dark-sdd findings/03** — Anti-hallucination state-of-art (G-Eval, RAGAS, DeepEval, prompt injection patterns).
8. **dark-memory-mcp docs/judge-personas.md** — 8 personas compiladas + Markdown override mechanism (spec 1155 v14).
9. **Play Favorites** — Spiliopoulou, Fogliato et al. 2025, *Play Favorites: A Statistical Method to Measure Self-Bias in LLM-as-a-Judge*, arxiv:2508.06709 (8 Aug 2025). Empirical study (>5000 prompt-completion pairs, 9 LLM judges) finding GPT-4o + Claude 3.5 Sonnet systematically self-bias, plus a family-bias toward same-family models. Grounds the design of EC-007 (self-bias detection) and motivates `judge-pipeline-v4.md` §4.4 (Evidence structure). URL: <https://arxiv.org/abs/2508.06709>

## Apéndice B: Anti-patterns evitados (per `dark-testing` skill)

| Anti-pattern | Cómo lo evitamos |
|---|---|
| Assertion roulette | Cada EC tiene UN assertion claro (severity + short-circuit verdict) |
| Eager test | Cada EC test es un solo caso; no bundled con otros |
| Mystery guest | Fixtures explícitas por EC; nada hidden |
| Over-mocking | Mock solo en el boundary (HTTP client); ECs son puros deterministic |
| Conditional test | No hay `if testing.Short() { skip }`; cada test corre siempre |
| Sleep-driven wait | HTTP client usa timeout; no sleeps |
| Magic number | Thresholds (0.85, 0.50) son constantes nombradas (`ThresholdAligned`, `ThresholdNeedsHuman`) |
| Test placebo | Los ECs tienen tests que fallan si los rompes (deliberate breaks §5) |

## Apéndice C: Glosario

- **vibe_case**: enum C1..C7 que categoriza el artifact. **Canonical
  mapping per `internal/v4alpha/vibe/spec.go:14-16`** (Phase 6 alpha.18.1):
  C1=code, C2=text, C3=decision, C4=research, C5=video, C6=audio,
  C7=multi. (Legacy v3 mapping: C3=image, C4=video, C5=bundle,
  C6=infra, C7=governance — DEPRECATED.)
- **rubric**: lista de criterios atómicos con pesos (sum=1.0) usados por una persona para evaluar un vibe_case.
- **persona**: system prompt especializado + lens + rubric + constraints + voice que el LLM adopta como juez.
- **edge case (EC)**: condición de input que dispara un short-circuit deterministic antes del LLM call.
- **bias audit**: metadata sobre mitigaciones aplicadas (position swap, random order, counterfactual examples) por Zheng et al. 2023.
- **verifier**: cheap post-check independiente del LLM, regex-based, que override el verdict si encuentra inconsistencia.
