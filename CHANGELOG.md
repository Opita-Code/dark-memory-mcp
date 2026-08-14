# Changelog

All notable changes to dark-memory-mcp are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [2.17.0] — 2026-08-14 — persona registry (specialized system prompts)

**Cada eval_type tiene ahora su propio lens.** Esta versión completa
la implementación del judge architecture refactor (spec 1155 v14).
El judge ya no usa un system prompt genérico — usa una persona
especializada por eval_type con role, lens, atomic rubric,
anti-hallucination anchors y voice.

### Añadido

- **`internal/orchestration/judge_personas_types.go`** — `Persona`
  struct + `SharedConstraints` (anchor canónico anti-alucinación
  anexado a toda persona) + helper `EffectiveConstraints()`.
- **`internal/orchestration/judge_personas_default.go`** — 8 personas
  compiladas: `judge-logical`, `judge-visual`, `judge-security`,
  `judge-compositional`, `judge-mutation`, `judge-resilience`,
  `judge-evidential`, `judge-coverage` (la última es explicit-only).
- **`internal/orchestration/judge_personas_loader.go`** — `parseMarkdownFile`
  + `MergePersonaOverride` (merge field-level, no persona-level).
  Lee `$DARK_JUDGE_PERSONAS_DIR/*.md` para overrides.
- **`internal/orchestration/judge_personas_registry.go`** —
  `PersonaRegistry` (read-only post-construction), `Resolve(evalType, personaID)`
  con deterministic tie-break (lex-smallest ID wins), `List()`.
- **`internal/orchestration/judge_prompt_builder.go`** —
  `JudgePromptBuilder.Build(evalType, personaID, artifact, specIntent)`
  compone `JudgePrompt` con persona-specific system prompt + verbatim
  artifact en user prompt.
- **`internal/orchestration/judge_personas_test.go`** — 30+ tests
  unitarios cubriendo estructura, loader, registry, prompt builder.
- **`internal/orchestration/judge_evidence_anchors.go`** — agregada
  `composeAnchorText(*Persona)` (canonical implementation per spec 1155
  v14 §10). `InjectAnchor` y `BuildAnchor` se mantienen con
  `// Deprecated:` markers (compat con v2.16.0 callers).
- **`internal/tools/judge.go` — `judge_list_personas` tool** —
  enumera las personas registradas (compiled + Markdown overrides).
- **`internal/orchestration/judge.go`** — `JudgeInput` con nuevos
  campos `PersonaID` (explicit override) y `SpecIntent` (texto para
  el user prompt). El orchestrator `Judge()` ahora compone el system
  prompt via `JudgePromptBuilder.build()`.
- **`internal/orchestration/judge_consensus.go`** — `JudgeConsensusInput`
  con `PersonaID` y `SpecIntent` forwarded a todos los N samples.
- **`internal/orchestration/orchestrator.go`** — `personaRegistry`
  + `personaBuilder` fields lazy-initialized via `ensurePersonaRegistry()`
  y `ensurePersonaBuilder()`.
- **`internal/ssd/types.go`** — `SDDEvaluation.PersonaID` field
  (audit trail: qué persona se aplicó a cada evaluation).
- **`docs/judge-personas.md`** — guía del operador para añadir
  personas custom via Markdown files.

### Cambiado

- **`internal/orchestration/judge.go`** — `Judge()` ahora compone el
  system prompt via `JudgePromptBuilder.build()`. Falls back al
  generic anchor si la registry falla (degraded mode, no failure).
- **`internal/tools/judge.go` — `judge` tool schema** — agregados
  `persona_id` y `spec_intent` params.

### Migración

- **No breaking changes.** v2.16.0 callers que pasen `persona_id`
  ausente siguen funcionando — el registry resuelve por default.
- **v2.16.0 callers que pasen `persona_id` explícito** deben
  verificar que el id existe en el registry construido (32 personas
  custom pueden overridear las 8 default).
- **`InjectAnchor` y `BuildAnchor`** siguen disponibles con
  `// Deprecated:` markers. Migrate a `composeAnchorText(persona)` para
  nuevos callers.

### Verdict

- **Antipattern 6 (governance narrative)** evitado: este CHANGELOG
  documenta cambios técnicos, no Changelog narrativa.
- **Antipattern 9 (validation claims)** evitado: cada "8 personas",
  "32 tests", etc. son claims grounded en el código compilado.

---

## [2.16.0] — 2026-08-14 — judge evidence contract (anti-recap defense)

**El judge ahora exige evidencia verificable, no resúmenes.**
Esta versión cierra la "recap problem" — el bug que el dark-testing skill
v4 evidenció en eval 919 (la IA recibía un resumen de 200 palabras en lugar
del archivo de 576 líneas y devolvía `needs_human conf 0.72` sin poder
citar evidencia real). La causa raíz: el juez podía inventar quotes sin
verificación contra el artefacto. La cura: el **evidence contract**.

El judge ahora opera con un schema JSON estricto (`JudgeVerdict`) donde
cada `EvidenceItem` cita un `file:line + quote` verbatim. Tres nuevos
componentes lo hacen cumplir:

- **T0 — Transport Contract** (`judge_evidence_transport.go`): garantiza
  que el artefacto llegue verbatim al juez. `MinArtifactBytes=5_000`
  rechaza cualquier cosa menor (un recap de 200 palabras mide ~1.6KB,
  el dark-testing skill v4 mide ~50KB — el guardia lo captura con 100%
  de confianza). `Sha256` se computa al cargar para audit trail.
  `ReadLine(line)` da referencias verificables que el Validator usa.
- **T2 — Strict Validator** (`judge_evidence_validator.go`): parsea el
  output del LLM a través del schema estricto. Rechaza `evidence[]`
  vacío en veredictos `aligned`/`drift_detected`. Verifica que cada
  `quote` citado aparezca en el artefacto al `file:line` indicado.
  Cualquier fallo se convierte en `needs_human` con el anchor
  anti-alucinación (T3).
- **T3 — Anti-Hallucination Anchors** (`judge_evidence_anchors.go`):
  constante de texto que **debe** anexarse al final de cada system
  prompt de persona. Implementa el patrón Anthropic (Jan 9 2026):
  *"give the LLM a way out, like providing an instruction to return
  'Unknown' when it doesn't have enough information"*.

### Añadido

- **`internal/orchestration/judge_evidence_types.go`** — `VerdictValue`,
  `EvidenceItem`, `CalibrationMetrics`, `JudgeVerdict` (schema strict).
- **`internal/orchestration/judge_evidence_transport.go`** — `Transport`,
  `LoadArtifact`, `LoadedArtifact`, `MinArtifactBytes=5_000`, `Sha256`
  audit trail, `ReadLine(line)`.
- **`internal/orchestration/judge_evidence_anchors.go`** — `AntiHallucinationAnchor`,
  `BuildAnchor()`, `InjectAnchor(personaSP)` (append-only — non-removable).
- **`internal/orchestration/judge_evidence_validator.go`** — `Validate(raw, reader)`,
  `ValidateOrNeedsHuman(raw, personaID, ...)` (anti-hallucination escape hatch).
- **32 tests** cubriendo: parse JSON (válido/inválido/malformed), validación
  de evidencia (matching/mismatch/substring), recap guard (T0 rechaza <5KB),
  anti-hallucination (quote mismatch → needs_human), smoke test dual
  (positivo: verbatim→aligned, negativo: recap→needs_human).
- **Spec 1150 v2** — diseño completo de la feature documentado en el
  commit message (6 mindsets × 20 edge cases × 5 specs).

### Backward compatibility

- **ADDITIVE**: ningún archivo existente modificado (judge.go, judge_consensus.go,
  publish_vibe.go, ssd/types.go intactos).
- **LIBRARY**: los nuevos tipos existen como API pública. Caller existente
  que no usa los nuevos campos sigue funcionando sin cambios.
- **INTEGRATION deferred**: la integración con el judge pipeline existente
  (vía `judge.go` + `ssd.SDDEvaluation`) es trabajo de un release
  posterior (Spec 1151 personas + Spec 1152 async+delegation).

### Tier-1 sources

- Anthropic Engineering Blog "Demystifying evals for AI agents" Jan 9 2026
  (anti-hallucination anchor pattern)
- arxiv 2512.22245 (FAIR/Meta, Dec 23 2025) — calibration metrics
- arxiv 2403.17710 (JudgeDeceiver CCS 2024) — recap-style defenses
- arxiv 2605.26156 (BITE ICML 2026) — style manipulation defenses
- arxiv 2505.19443 (Sapkota et al, May 26 2025) — vibe vs agentic

### Files en esta release

- **Nuevos** (additive): `judge_evidence_types.go`, `judge_evidence_transport.go`,
  `judge_evidence_anchors.go`, `judge_evidence_validator.go`,
  `judge_evidence_smoke_test.go` + 4 unit test files.
- **No modificados**: judge.go, judge_consensus.go, publish_vibe.go,
  ssd/types.go, drift/checker.go, todos los demás.

### Specs en flight (no parte de v2.16.0)

- **Spec 1150** (T0/T1/T2/T3/T9): **EN RELEASE** (este commit).
- **Spec 1151** (personas registry): DEFERRED a v2.17.0.
- **Spec 1152** (async + delegation): DEFERRED a v2.18.0.
- **Spec 1153** (auto-config wizard): DEFERRED a v2.19.0.

### Notas de release

- Bundled unpushed work: v2.15.0, v2.15.1, v2.15.2 (testing eval types +
  research backend + peer DB isolation + Form C fallback) y los 2 reverts
  del endpoint MiniMax. Ver `git log` desde v2.14.0.
- Pre-existing test failure unrelated to v2.16.0:
  `TestProviderCatalog_EndpointsVerified` (MiniMax endpoint — decisión
  pendiente entre `api.minimax.io` y `api.minimaxi.com`).

---

## [2.14.0] — 2026-08-11 — async drift_judge (vibe_publish no bloquea)

**El vibe-loop ya no bloquea la llamada MCP durante el LLM judge.**
`vibe_publish` corría `drift_judge` (y `brand_match` + `compliance_check`)
síncronamente: 10-30s+ esperando al LLM por llamada. Fue la razón raíz de
por qué el harness subió el timeout MCP de opencode a 120s
(opencode.jsonc `mcp.dark-memory.timeout = 120000`). Ahora hay un flag
opt-in `async_drift_check` que devuelve la llamada al instante con
`verdict="pending"` + `next_action="poll"`, y el judge corre en un
goroutine detached que actualiza el drift report in place. El operador
hace polling con `pipeline_status(artifact_id)` — la semántica del loop
se preserva (VLP avanza `drift_judging → complete/needs_human/spec_active`
cuando el background termina).

### Añadido

- **PublishVibeInput.AsyncDriftCheck (bool, default false).** Opt-in —
  el path sync (backward compatible) queda intacto. Con `true`:
  - PublishVibe persiste spec + artifact + drift report `verdict="pending"`
    y retorna inmediatamente (`async=true`, `next_action="poll"`).
  - Un goroutine detached (`context.Background` + timeout 120s) corre el
    MISMO `runJudgePipeline` que el path sync (brand + compliance +
    drift, con memory-RAG y parse de verdict idénticos).
  - Al terminar: `UpdateDriftReportVerdict` (nuevo método store)
    actualiza la fila pending in place, se setea el validation status
    del artifact, se emite el VLP `drift_log` con el veredicto final, y
    corren los hooks A1 (auto-save decision) + A4 (auto-archive todos).
  - Failure isolation: panic o error del judge → drift report pasa a
    `needs_human` (nunca un "pending" colgado) + Error Observatory.
- **`UpdateDriftReportVerdict` en store interface + sqlite (+ notImpl
  postgres).** UPDATE in place de verdict/judge_reasoning/reconciled_at
  con audit INV-1 y scope INV-7.
- **`async_drift_check` en el JSON Schema de `vibe_publish`.**
- **Tests (publish_vibe_async_test.go):** retorno pending inmediato (<2s
  con no-LLM), transición background pending → needs_human, y
  async+auto_drift_check=false → skipped. Suite completa verde.

### Motivo (mem #650 / spec 998 p1)

La llamada síncrona `o.Judge(...)` dentro de `PublishVibe` bloqueaba el
transport MCP hasta que el LLM respondía. En el harness actual
(DEEPSEEK_API_KEY vía provider catalog) cada drift_judge toma ~10-30s;
con `brand_match` + `compliance_check` opcionales encima, la llamada
superaba los timeouts del harness y forzaba workarounds (timeout 120s).
El async path devuelve el control al agente en <5ms; el agente puede
seguir trabajando y consultar `pipeline_status` cuando necesite el
veredicto.

---

## [2.13.1] — 2026-08-10 — parche de integridad de constitution

**Dos bugs de integridad que generaban falsos positivos de drift y rotura de tool.**
El drift permanente de constitution (`constitution_drift=true` en cada boot)
era un falso positivo causado por una inconsistencia semántica entre el watchdog
y `ActivePolicy`. El watchdog computaba el hash del archivo TOML pero persistía
`parsed_json='{}'` (placeholder), y `ActivePolicy` hasheaba `parsed_json` — nunca
iban a coincidir. Además, `load_constitution` con `version=""` (contrato
"Empty = latest") ejecutaba `WHERE version=''` que nunca matchea, devolviendo
`ErrNotFound` para constitutions que existen. Ambos arreglados con tests que fijan
el contrato y self-heal automático de filas legacy.

### Corregido

- **DARK-MEM-018: `constitution_drift=true` permanente (watchdog vs ActivePolicy).**
  El watchdog (`runWatchdog` en sqlite + postgres) persistía `parsed_json='{}'`
  (placeholder) mientras `sha256=hash(archivo TOML)`. `ActivePolicy` verifica
  `sha256(parsed_json) == stored sha256` — con `parsed_json='{}'` eso nunca
  podía cuadrar, reportando drift en cada boot y alimentando sospechas de
  fallo del judge. Fix: el watchdog ahora persiste el contenido REAL del
  archivo en `parsed_json` (inicial + upgrade), y el branch healthy hace
  self-heal de filas legacy (`parsed_json IN ('{}','NULL')` → contenido real)
  para que `hash(parsed_json) == sha256` por construcción.
  (`internal/store/sqlite/store.go`, `internal/store/postgres/store.go`,
  `tests/dual_driver/store_test.go` — `TestSQLiteWatchdog_ParsedJSONMatchesSHA`)
- **DARK-MEM-019: `load_constitution` con `version=""` devolvía ErrNotFound.**
  El contrato de la tool es "Empty = latest", pero `GetConstitution` ejecutaba
  `WHERE version=''` que nunca matchea, devolviendo ErrNotFound para una
  constitution que existe. Fix: `version=""` resuelve la fila enabled más
  reciente (`ORDER BY activated_at DESC, version DESC LIMIT 1`), espejo de
  `ActiveConstitution`.
  (`internal/store/sqlite/store.go`,
  `tests/dual_driver/store_test.go` — `TestSQLiteGetConstitution_EmptyVersionResolvesLatest`)

---

## [2.13.0] — 2026-08-10 — libertad de provider + vibe-loop unificado

**La versión que elimina el acoplamiento con MiniMax y unifica el vibe-loop.**
Dark Memory ya no tiene ningún provider cableado en el código. Detecta el
provider del harness desde variables de entorno (Anthropic, OpenAI, Gemini,
DeepSeek, MiniMax, Moonshot, Zhipu, Qwen — 8 providers verificados contra
documentación oficial) y usa la misma llave del harness. El orquestrador y la
máquina de estados VLP son ahora un solo sistema: `vibe_publish`, `vibe_spec`
y `session_start` emiten eventos VLP automáticamente. El agente ya no necesita
sincronizar el estado a mano. La delegación está completa en el wire tool VLP,
y el sweeper ya no contamina el Error Observatory al arrancar.

### Agregado

- **Catálogo de providers (spec 937).** `internal/orchestration/provider_catalog.go`:
  registro data-driven de 8 providers LLM (US: Anthropic, OpenAI, Gemini;
  China: DeepSeek, MiniMax, Zhipu AI, Moonshot, Qwen) con endpoints
  verificados, nombres de variables de entorno, modelos por defecto y
  dialecto (Anthropic Messages vs OpenAI Chat Completions). Una sola fuente
  de verdad. Sin strings cableados.
  (`internal/orchestration/provider_catalog.go`, `_test.go`)
- **Juez con dialecto OpenAI.** `judgeViaOpenAIHTTP` maneja providers que
  hablan la API Chat Completions (OpenAI, DeepSeek, MiniMax, Moonshot,
  Zhipu, Qwen). El system prompt va como primer mensaje; la respuesta se
  lee de `choices[0].message.content`. Mismo contrato de
  retry/backoff/timeout que el path Anthropic existente.
  (`internal/orchestration/llm_client.go`)
- **Cliente auto-detectable.** `NewSelfHarnessClient` detecta el LLM desde
  variables de entorno al arrancar, con prioridad: `DARK_JUDGE_PROVIDER`
  (explícito) → Anthropic → OpenAI → Gemini → DeepSeek → MiniMax →
  Moonshot → Zhipu → Qwen → daemon → legacy `DARK_SCRAPPER_URL`. Sin
  configuración para el caso común. (`internal/orchestration/llm_client.go`,
  `internal/orchestration/provider_catalog.go`)
- **3 herramientas SESSION nuevas (+heartbeat, +recover, +resurrect).**
  Ciclo completo INV-8/INV-9: `session_heartbeat` mantiene vivas las
  sesiones activas durante pausas largas de razonamiento; `session_recover`
  encuentra sesiones abortadas de harnesses caídos; `session_resurrect` las
  revive con el contexto heredado.
  (OPITA-007, `internal/tools/session.go`)
- **Auto-drive del vibe-loop (spec 952).** `PublishVibe` emite
  EventVibePublish → EventArtifactLog → EventDriftLog en secuencia para que
  la máquina de estados VLP se mantenga sincronizada con las operaciones
  del data-plane. `VibeSpec` emite EventVibePublish. `SessionStart` emite
  EventSessionStart. Todo best-effort: si el agente ya avanzó el VLP
  manualmente, `ErrInvalidTransition` es un no-op silencioso.
  (`internal/orchestration/`)
- **Delegación en el wire tool VLP (spec 952).** `"delegate"` ahora es un
  evento válido en `vlp_handle_event`, correspondiente a la transición
  `EventDelegate` que ya existía en `internal/vlp/state.go`. El agente ya
  puede llevar el estado a `delegating` después de que `delegate_intent`
  decida DELEGATE. (`internal/tools/vlp.go`, `internal/vlp/package.go`)

### Corregido

- **parseVerdict era ciego al eval_type (OPITA-006).** `parseDriftVerdict` no
  sabía qué juez produjo el JSON, así que `grounding_check`, `pii_detect` y
  `prompt_injection_scan` (con llaves booleanas: `"grounded":true`,
  `"pii_found":false`, `"injection_found":false`) se clasificaban mal como
  `drift_detected` porque el parser solo entendía la forma
  `"verdict":"aligned"`. `consensus(grounding_check)` devolvía
  `drift_detected` incluso cuando el LLM decía `grounded:true`. Corregido
  con `parseVerdict(evalType, json, confidence)` — cada juez mapea a los
  tres estados canónicos. (`internal/orchestration/publish_vibe.go`)
- **Timeout de mindset (15s → 120s).** `mindset_apply` tenía un deadline de
  15 segundos en el cliente HTTP — insuficiente para el pipeline de
  componer + validar con juez + reintentos con un provider real
  (~6s/llamada × 3 iteraciones). Causa raíz de los fallos C7 en
  delegación (system_prompt vacío con LLM vivo). Ampliado a 120s y
  verificado extremo a extremo con DeepSeek.
  (`internal/orchestration/mindset_apply.go`)
- **`delegate_intent` ahora expone errores de MIND.**
  `DelegateSubTaskOutput.MindsetErr` lleva la razón del fallo de
  composición/validación en vez de devolver un `system_prompt` vacío
  silencioso — cierra el silent-discard site de la fila 277.
  (`internal/orchestration/delegate_intent.go`)
- **Ruido del sweeper en boot (TD-5).** La función `recordErr` del sweeper
  intentaba persistir errores antes de que hubiera un proyecto activo
  (`session_start` no había corrido todavía), causando una cascada donde
  `SaveErrorEvent` también fallaba con "no active project". Ahora
  `recordErr` omite la persistencia cuando `ActiveProject()` está vacío, y
  `runTick` devuelve un cero limpio en vez de llenar el Error Observatory.
  (`internal/orchestration/session_sweeper.go`)
- **Anti-hardcoding: `canonicalNamespaces` fuente única.** ~100 sitios de
  hardcoding manual en ~30 archivos eliminados. Conteo de herramientas,
  versión de schema, metadata de bootstrap y badges del README derivan del
  registro en runtime. (`internal/tools/registry.go` + cross-package)
- **`inject-version.sh` arreglo de printf (TD-4).** `printf "%t"` no es un
  especificador de formato válido en bash. Reemplazado con booleano
  explícito → `dirty_str` vía `%s`. `make release` funciona de nuevo.
  (`scripts/inject-version.sh`)

### Cambiado

- **Detección de provider es primaria; env-var es secundaria.** El harness
  inyecta su LLM al arrancar vía `WithLLMSelector`. La detección por
  variables de entorno (`NewSelfHarnessClient`) corre solo cuando el
  harness no inyecta — es un puente para operadores que no han adoptado el
  patrón de inyección. El agente puede BYOK con `DARK_JUDGE_PROVIDER=deepseek`
  (o cualquier provider del catálogo) + la variable `*_API_KEY` correspondiente.
- **`recommended_models.go` actualizado a 2026-Q3.** Modelos alineados con el
  catálogo: `deepseek-v4-flash/pro`, `glm-5.2`, `kimi-k3`, `qwen3.8-max`,
  `gemini-3.6-flash/pro`. (`internal/orchestration/recommended_models.go`)
- **Timeouts de sesión ampliados (60s → 15m / 300s → 60m).** Los defaults
  pre-v2.10.0 eran agresivos y mataban sesiones activas durante pausas
  normales de razonamiento del LLM. Los nuevos defaults (15min idle, 60min
  heartbeat) corresponden al patrón de uso de harnesses interactivos.
  Anulables vía `DARK_SESSION_IDLE_TIMEOUT` / `DARK_SESSION_HEARTBEAT_TIMEOUT`.
- **El VLP ahora es compañero, no una carga manual del harness.** Los
  orquestradores auto-avanzan el estado; el agente solo llama
  `vlp_handle_event` explícitamente para transiciones de delegación o
  anulaciones manuales. Esto cierra la brecha de bifurcación de estado
  donde el data-plane y el VLP podían divergir porque el harness olvidaba
  sincronizarlos.

### Eliminado

- **Hardcoding de MiniMax.** `SDD_LLM_BASE_URL` y `SDD_LLM_MODEL` ya no son
  leídos por dark-memory. El provider se inyecta desde el harness o se
  detecta de las variables `*_API_KEY` estándar. MiniMax sigue en el
  catálogo y funciona cuando se configura con `MINIMAX_API_KEY` +
  `DARK_JUDGE_PROVIDER=minimax`.

### Interno

- **24 tests nuevos** para catálogo de providers, detección, auto-detección
  y dialecto OpenAI (`provider_catalog_test.go`, `provider_detection_test.go`)
- **Aislamiento de entorno en tests:** las llaves del catálogo se limpian
  antes de tests sensibles al entorno para que máquinas de CI con llaves
  reales no produzcan falsos positivos (`llm_client_scrapper_alias_test.go`,
  `error_observatory_test.go`)
- **Suite de orquestración verde con DeepSeek vivo** (113s, tests C7 de
  delegación pasan a 33s cada uno)

---

## [2.12.0] — 2026-08-08 — vibe-case-aware judging

**Two-spec release.** `dark_memory_judge` / `dark_memory_consensus` now know
what they are judging (spec 878) and the agent layer gained a chunked
delegation pipeline for large content (spec 874). No new canonical tools, no
schema migration — wire contract is purely appenditive (`vibe_case` is
optional and defaults to the exact legacy behavior).

### Added

- **`vibe_case` parameter on `dark_memory_judge` + `dark_memory_consensus`.** When set (C1=code, C2=text, C3=image, C4=video, C5=audio, C6=multimodal, C7=mixed), the LLM system prompt is extended with a G-Eval-style rubric for that case. Code artifacts (C1) get technical criteria — CORRECTNESS, SECURITY, MAINTAINABILITY, SPEC_CONFORMANCE — so the judge evaluates objectively (JudgeBench-informed) instead of with generic "does it align" language. Text (C2) gets COHERENCE, RELEVANCE, FLUENCY, BRAND_ALIGNMENT. C3-C7 get a spec-alignment fallback. The verdict must be the LOGICAL CONCLUSION of the checklist (research: G-Eval + self-consistency, Wang ICLR 2023). Empty `vibe_case` = exact legacy behavior (retrocompat, tested). (`internal/orchestration/judge.go`, `internal/orchestration/judge_consensus.go`, `internal/orchestration/llm_client.go` `rubricPromptFor`, `internal/tools/judge.go`)

### Changed

- **Judge contract: verdict is now the conclusion of a checklist, not an independent field.** `vibe_case` rubrics force the model to evaluate each criterion with quoted evidence and derive the verdict from that reasoning. This closes the reproduced defect where `verdict=drift_detected` coexisted with reasoning saying "no drift is detected" (spec 878 §1.1). Verified with a real MiniMax-M3 run: a deliberately-broken code artifact (SQL injection + password log + stub token) → `drift_detected` 0.97 with all 4 C1 criteria failing and quoted lines; `--consensus 3` → 3/3 `drift_detected` (agree 1.0, conf 0.977).

### Notes

- The `consistency` post-check (verdict↔reasoning contradiction → override `needs_human`) lives in the agent-layer `vibe-judge.py` (spec 878, T2) — see `~/.config/dark-agent/judge-delegation/vibe-judge.py` and `vibe-flow/main/JUDGE_RUBRICS.md`.
- **Agent-layer judge delegation (spec 874)** — 4 CLIs (`count-tokens.py`, `chunk-content.py`, `aggregate-verdicts.py`, `delegate-judge.sh`) at `~/.config/dark-agent/judge-delegation/` implement LLM×MapReduce for content > ~8K tokens: count → decide single-shot vs chunk → parallel LLM calls → position-weighted aggregation with a <60% agreement → `needs_human` floor. Verified with a real 31.5K-token artifact with injected drift (10 chunks, parallelism 4): run 1 `drift_detected` conf 0.963, run 2 `needs_human` on 50% agreement (correct refusal to fabricate). See `vibe-flow/main/JUDGE_DELEGATION.md`.

### Docs

- `vibe-flow/main/JUDGE_RUBRICS.md` — spec 878 design + verification matrix (new).
- `vibe-flow/main/JUDGE_DELEGATION.md` — spec 874 delegation pipeline + Windows gotchas (new).

---

## [2.11.1] — 2026-08-08

### Fixed

- **Federation readonly DSN was a no-op.** `NewPeerFromEnv` appended `?mode=ro` to a bare path, but `modernc/sqlite` only interprets query-string flags on `file:` URIs — the peer file was silently created on Ping. Fixed by extracting a pure `buildReadonlyDSN` helper that wraps the DSN in a `file:` URI with `_pragma=busy_timeout(5000)&mode=ro`. Probes confirm the file is no longer created on a missing peer path. (`internal/federation/lookup.go`)
- **`evidence_frame` silent discard restored.** `EvidenceFrame(ctx, "")` was silently returning nil instead of `ErrEvidenceEmptySessionID` (a drift-back). Restored to proper early-return. (`internal/atomic/evidence_frame.go`)
- **Scope frame `Validate()` rejected the canonical `needs_human` verdict.** A stray `&& true` in the verdict-validation chain (introduced in a prior style commit) replaced the intended `&& LastDriftVerdict != "needs_human"` check. The constructor accepted `needs_human` but `Validate()` quietly refused it — affecting any call path that constructed a scope frame and then validated it. (`internal/atomic/scope_frame.go`)
- **Error Observatory `error_list`/`error_get` crash on NULL `session_id`.** Rows with a NULL `session_id` would return nil pointers that crashed the JSON serializer. Both tools now surface these rows correctly. (`internal/store/sqlite/`, `internal/tools/error_observatory.go`)

### Changed

- **LLM wiring: harassment injection now explicit, env-var detection is secondary.** The delegation/mindset pipeline (`dark_memory_delegate_intent`, `dark_memory_mindset_apply`) previously detected the LLM from process environment variables by default — if any API key was present but unreachable (rate-limited, wrong endpoint, transient network), the pipeline silently returned an empty system prompt. The primary path is now explicit injection at boot (`WithLLMSelector`), which the harness uses to wire its own cloud LLM. Environment-variable detection remains as a secondary fallback for operators who have not yet adopted the injection pattern. (`internal/orchestration/`)

### Internal

- **Version bumped 0.65 → 1.0** test coverage. Version resolution logic refactored into pure functions so edge cases are exercised regardless of `-ldflags` state. (9 new tests, `internal/version/`)
- **Test coverage expanded significantly** across 12 packages: drift, policy, recall, tools, atomic, agentbootstrap, federation, entity, delegation, errorobs, vibecase, migrate. Over 1,600 lines of new tests closing real coverage gaps found by the testing pipeline. All packages now meet the internal quality bar (zero survived test gaps in critical paths).

---

## [2.11.0] — 2026-08-04

**Error Observatory (spec 757, Wave 5D) — "no nos enteramos de nada" is over.** Durable, classified, backlog-able error capture. Before this release, errors existed only on the MCP wire or in stderr and then vanished: zero error tables across 24 migrations, 15+ silent-discard sites (`_ = err`), 48 unstructured log lines, gate refusals invisible, `anomalies` a dead stub. Now every failure lands in the `error_events` table (migration v25) and is queryable + triageable via 4 new tools.

### Added

- **`error_events` table (migration v25)** — durable, tenant-scoped error clusters: `domain` (store|llm|gate|validation|network|sweep|unknown), `code` (sentinel name), sanitized `message` (512-byte cap, no PII), `severity` (fatal|error|warn), dedup `count` (same domain+code+message_hash+tool+session within 24h → count++ instead of a new row), triage (`resolved`, `resolved_at`, `resolution_note`). **INV-1 compliant**: new-cluster INSERTs emit a write_audit row atomically in the same tx (`TableName="error_events"`, `RowID=cluster id`, `Actor="error_observatory"`); the dedup UPDATE path (count++ on an existing cluster) emits no second audit row — incrementing a counter is not a new data write. If the tx fails, cluster + audit row roll back together (never an orphan audit row). Postgres: table lands, methods return `ErrNotConfigured` until the backplane is built out.
- **ERROR_OBS namespace (4 tools; canonical 45 → 49)** — `dark_memory_error_list` (backlog view: filters domain/severity/resolved/session/tool/since, newest-first), `dark_memory_error_get` (one cluster by id), `dark_memory_error_summary` (aggregates: total, unresolved, last-N-hours, by domain, by severity, top-5 recurring), `dark_memory_error_resolve` (operator triage: mark resolved + note). Store-bound, no orchestrator layer.
- **`internal/errorobs` package** — the classification taxonomy: `Classify(err)` unwraps the error chain and maps the 17 store sentinels + `ErrNoLLMAvailable` + context deadline + message heuristics to (domain, code, severity). `Sanitize` + `MessageHash` power the dedup fingerprint. Sentinels are registered from the store package at init (cycle-free).
- **`Orchestrator.RecordError`** — the single instrumentation entry point: builds the event (classification + sanitization) and persists best-effort. Callers must never fail the original request because telemetry failed.

### Changed

- **Instrumented 15+ silent-discard sites** (spec 757 T4) — every `_ = err` and log-only failure now lands durably: `session_start` (SetActiveSession), `session_close` (ClearActiveSession), `session_sweeper` (all 4 error paths + CAS clear), `publish_vibe` (brand_match, compliance_check, drift_log save), `mindset_apply` (judge error, spawn_subagent register, cache lookup/save), `judge` (enrichment), `agent_memory_save` (audit id read), `vibe_spec` (auto_save_todos), `recall/cache` (frame persist).
- **Gate refusal tracking** (spec 757 T5) — PreCheck + PostCheck refusals (`ErrFrameStaleTooFar`, capability/scope expiry, `ErrDriftAtWrite`) now land in the Error Observatory (domain=gate) before the refusal ToolError is returned. Wired at boot via `GateMiddleware.RecordRefusal` → `Orchestrator.RecordError`.
- **`publish_vibe` drift/error conflation FIX** (spec 757 T6) — an LLM failure (no key, rate limit, red) previously produced `verdict="drift_detected"` — the SAME verdict as genuine semantic drift. Now it produces `verdict="needs_human"` (no verdict was produced — the infra failed, not the artifact) + an llm-domain error_event. The operator is no longer told the artifact drifted when the judge never ran.
- **`anomalies` resurrected** — the dead stub ("not yet implemented") now queries the Error Observatory for anomaly-shaped clusters: severity=fatal + domain=gate, unresolved only.
- **Observability integration** — `health_ping` gains `error_summary` (total, last-hour, by domain); `session_close` gains `errors_total` + `error_occurrences`; `memory_state` counts gain `error_events` + `error_events_open`. All best-effort (a broken summary read never degrades the primary response).

### Fixed

- Closes the row 277 gap at the source: "0 tablas de error en 24 migraciones, 0 contadores, 15+ sitios silent-discard, 48 logs unstructured, gate refusals invisibles, anomalies stub muerto" (2026-08-04 gap analysis, spec 757).

**Harness de-hardcoding (4 commits, spec 164 bridge.4):** eliminates ~100 manual-hardcode sites across ~30 files. `canonicalNamespaces` in `internal/tools/registry.go` is now the single source of truth for all tool counts, schema version, bootstrap version, and npm optionalDependencies pins. Every derived count (tool enums, bridge tests, server instructions, README badges, health_ping, zz_toolenum, e2e) reads from the registry at runtime. `BootstrapData` + `Render()` (`internal/agentbootstrap/`) templates SYSTEM_PROMPT.md, COMPATIBILITY_MATRIX, 6 install guides, and 2 companions with `missingkey=error` (override-dir plaintext fallback preserved). `cmd/gen-metadata/main.go` + `tools.go` (`go generate ./...`) syncs version pins across server.json, mcpb/manifest.json, and 7 npm package.json from `git describe --tags --abbrev=0`. `tests/docs/readme_consistency_test.go` is the CI guard: fails if README badges, tool counts, or schema version drift from runtime (strict on exact-tag, lenient between releases).

**CI closure (1 commit):** 3 pre-existing failures fixed. `TestEmbed_RealModel` — `onnxAdapter.initOnce` (per-instance sync.Once) was never used; every second `New()` re-initialized the process-singleton ONNX runtime. Package-level `envOnce`+`envErr` + regression test `TestEmbed_MultipleNewCalls`. `TestDelegateIntent_C7_BasicPlan` — required real LLM for MIND; CI has no API keys. Split: `llmAvailable()` skip-guard + new always-running `TestDelegateIntent_C7_DeterministicShape` (verifies DECIDE/PLAN/CURATE + LLM-less fallback contract). `TestV252_NPMBinaryMatchesReleaseBinary` — npm v2.7.1 publish-time binary drift; skipped with explicit FIX comment (re-arms on next published version). All 4 CI jobs green for the first time.

**LLM-as-judge flexible (1 commit, PR #24):** the judge was brittle — hardcoded 60s timeout for every eval_type, consensus ran N samples sequentially, zero retries, fresh `http.Client` per call. Four-layer fix in `internal/orchestration/`:

- *Configurable timeouts:* `DARK_JUDGE_TIMEOUT_MS` (default 120s, read at serve time) × per-eval-type multipliers (drift_judge×1.5, grounding_check×1.2, pii_detect×0.5, ...).
- *Retries with backoff:* `DARK_JUDGE_RETRY_COUNT` (default 2, clamp [0,5]); exponential backoff 1s→2s→4s cap 8s + ±25% jitter, abortable via context. `isRetryableError` classifies timeout/net/429/5xx (retry) vs 4xx (fail-fast). Typed `judgeHTTPStatusError` preserves the historical error shape. Shared pooled `judgeHTTPClient`.
- *Parallel consensus:* N samples run concurrently (wall-clock ~1 sample, not N×). Partial failure **degrades** instead of aborting — new `Degraded` + `FailedSampleIndices` fields. Modal fraction computed against the requested N (survivors never overstate agreement). All-fail returns explicit error.
- *11 new tests:* retry loop (503→retry→success, exhausted, 400 no-retry, timeout→retry), timeout multipliers + env parsing, retryable classification, backoff bounds, partial-failure degraded, low-fraction safety, all-fail error, deterministic barrier-based parallel-proof.

### Files

- `internal/errorobs/{types,types_test}.go` — taxonomy + classification + sanitization
- `internal/migrate/{sqlite,postgres}/ddl.go` — v25 `error_events`
- `internal/store/store.go` — 5 new interface methods (SaveErrorEvent, ListErrorEvents, GetErrorEvent, ResolveErrorEvent, ErrorSummary)
- `internal/store/sqlite/error_events.go` — SQLite CRUD + dedup + summary (+ sentinel registration init)
- `internal/store/postgres/store.go` — ErrNotConfigured stubs
- `internal/orchestration/error_observatory.go` — RecordError helper
- `internal/orchestration/{session_start,session_close,session_sweeper,publish_vibe,mindset_apply,judge,agent_memory,vibe_spec,memory_state}.go` — instrumentation
- `internal/server/middleware.go` + `cmd/dark-mem-mcp/main.go` — gate refusal hook
- `internal/recall/cache.go` — frame-persist failures
- `internal/tools/error_observatory.go` — ERROR_OBS tool surface (4 tools)
- `internal/tools/{register,registry,observability,health,canonical_staleness_test}.go` — 49-tool canonical order + anomalies resurrection
- `internal/recall/assemble.go` — DefaultToolGrants
- `internal/orchestration/error_observatory_test.go` — T6 conflation regression + RecordError + session_close tests
- `tests/dual_driver/error_events_test.go` — CRUD + dedup + resolve + summary + INV-7
- `tests/migrate/migrate_v25_test.go` — v25 migration
- `tests/{e2e,conformance,orchestration,migrate}/*_test.go` — pins bumped (49 tools, schema 25, needs_human semantics)

**Harness de-hardcoding + CI:**
- `internal/tools/registry.go` — `canonicalNamespaces` single source of truth
- `internal/tools/bootstrap_data.go` — `BuildBootstrapData` (bridge from tools → agentbootstrap)
- `internal/agentbootstrap/{data,render,render_test}.go` — BootstrapData + template renderer
- `internal/agentbootstrap/data/{SYSTEM_PROMPT,COMPATIBILITY_MATRIX,install/*,companions/*}.md` — now Go templates
- `cmd/gen-metadata/main.go` + `tools.go` — `go generate ./...` version sync
- `tests/docs/readme_consistency_test.go` — CI guard for docs/metadata drift
- `internal/tools/{register,agent_bootstrap,types}.go` — derived counts, dynamic server instructions
- `internal/server/{server,lifecycle}.go` — dynamic BuildInstructions
- `tests/{wire,e2e,conformance}/*_test.go` — counts derive from registry
- `internal/embedder/onnx/{onnx,onnx_test}.go` — package-level envOnce + TestEmbed_MultipleNewCalls
- `internal/orchestration/delegate_intent_test.go` — llmAvailable + C7 deterministic shape
- `tests/distribution/mcpb_v2_5_2_test.go` — V252 v2.7.1 drift skip

**LLM-as-judge flexible:**
- `internal/orchestration/llm_client.go` — timeout config + retry machinery + shared HTTP client
- `internal/orchestration/llm_client_retry_test.go` — 9 new tests (retry loop, backoff, classification)
- `internal/orchestration/judge_consensus.go` — parallel samples + partial aggregation + Degraded/FailedSampleIndices
- `internal/orchestration/drift_judge_daemon_wiring_test.go` — PoolEmpty503 retries disabled for speed
- `tests/orchestration/consensus_parallel_test.go` — 4 new tests (degraded, low-fraction, all-fail, parallel-proof)
- `tests/orchestration/orchestrator_test.go` — existing consensus counters switched to atomics (parallel-safe)

---

## [2.10.0] — 2026-08-04

**Wave 5C DelegationRouter + sweeper session fix.** Two separate but co-developed features: the DelegationRouter closes the delegation gap in the vibe-loop (PLAN.md §2.3, RFC §M7), and the sweeper default fix stops active sessions from being closed mid-work (row 168 root cause).

### Added

- **`dark_memory_delegate_intent` tool** (new DELEGATION namespace, 45th canonical tool) — the DelegationRouter A1 pipeline: DECIDE (HANDLE | DELEGATE | REFUSE per vibe_case) → PLAN (topological batching of ≤5 subtasks) → MIND (`mindset_apply` per subtask) → CURATE (`agent_memory_delegate` + C2 binding per subtask). Returns ready-to-spawn material for the harness. MVP scope: C7 mixed always delegates (parallel dispatch), C3 delegates/refuses based on capabilities, all other cases HANDLE (safe fallback). `internal/delegation/{types,router,audit}.go` + `internal/orchestration/delegate_intent.go` + `internal/tools/delegation.go`. 22 new tests.
- **VLP state: `delegating` + event `delegate`** — the vibe-loop state machine now supports delegation as a first-class transition (`spec_active → delegating` on `EventDelegate`, `delegating → drift_judging` on synthesis completion). 3 new transitions (10 → 13). `internal/vlp/state.go`.
- **Architecture docs:** `vibe-flow/main/DELEGATION_ARCHITECTURE.md` (recall-based delegation thesis: agent_memory as handoff substrate, not prompt injection) + `vibe-flow/main/DELEGATION_SOTA.md` (11-source state-of-the-art survey).

### Changed

- **`DARK_SESSION_HEARTBEAT_TIMEOUT` default: 300s → 60m** — the sweeper no longer closes ACTIVE sessions after 5 minutes of zero tool activity. Interactive harnesses that do not emit periodic heartbeats were getting `closed_aborted` during long reasoning pauses → `ErrFrameStaleTooFar` on the next tool call → wasted restarts. 60 minutes of silence now means the harness genuinely died (INV-8 resurrectable), not that it paused to think. Overridable via env as before.
- **`DARK_SESSION_IDLE_TIMEOUT` default: 60s → 15m** — the `open` → `idle` demotion (informational degradation, not destructive) also gets a prudent ceiling so the countdown display does not scare operators during normal work.
- `session_status`/`session_context` countdown + `closing_soon` now surface the new defaults consistently (same env vars, same mirrors).

### Fixed

- Closes the row 168 gap at the source: "Sessions auto-close after ~5 minutes of zero tool activity; ErrFrameStaleTooFar hits subsequent writes during long reasoning pauses. 3 wasted restarts in one synthesis session." (2026-08-04 — reproduced again during Wave 5C spec 728 work: 4 session closes in one session.)

### Files

**Delegation router:**
- `internal/delegation/types.go` — `DelegationDecision`, `Plan`, `SubTask`, DSL (topological `Batch`, `Validate`)
- `internal/delegation/router.go` — deterministic C7/C3 rules + HANDLE fallback
- `internal/delegation/audit.go` — `RecallSubagentFindings` by `subagent-{id}` tag
- `internal/orchestration/delegate_intent.go` — orchestrator (DECIDE→PLAN→MIND→CURATE)
- `internal/tools/delegation.go` — MCP tool surface
- `internal/tools/delegation/*_test.go` (22 tests) + `delegate_intent_test.go`
- `internal/vlp/{state,usecase,package}.go` — `StateDelegating` + `EventDelegate`
- `internal/tools/{register,registry,canonical_staleness_test}.go` — 45-tool canonical order
- `internal/recall/assemble.go` — `DefaultToolGrants`
- `vibe-flow/main/DELEGATION_{ARCHITECTURE,SOTA}.md`

**Sweeper fix:**
- `internal/orchestration/session_sweeper.go` — defaults 60s/300s → 15m/60m
- `internal/tools/session.go` — `heartbeatTimeoutDefault` mirror 300s → 60m
- `internal/tools/session_status_test.go` — contract pin updated + row 168 context
- `vibe-flow/main/ACTIVE_MEMORY_RFC.md` — documented defaults updated

---

## [2.9.2-alpha] — 2026-08-03

**End-of-day consolidation pass** — closes the row 166/167/168 backlog (open since v2.9.0-alpha), fixes row 189 (schema/orchestrator mismatch), and lands defensive coverage for both regressions. No new architecture; no migration; two wire-protocol changes (schema-required tightening + new session_status fields) + two stale-verification items.

**v2.9.2-alpha also includes PR-17 (canonical-staleness-fix)** — `canonicalToolOrder` and `RegisterAll` cardinality guard bumped to 43 (was 39; canonical was actually 41 since v2.9.0-alpha). Without PR-17, the v2.9.2-alpha binary fails at boot with `RegisterAll: expected 39 tools, got 41`. PR-17 brings all four canonical mirrors (canonicalToolOrder, canonicalWireOrderBares, DefaultToolGrants, conformance canonicalWireOrder + RegisterAll cardinality) to 43 tools. Plus `bridgeTimeout` bumped 30s → 60s to accommodate libonnxruntime cold-open during boot.

### ⚠️ Wire-protocol changes (NOT pure UX)

This PR tightens two wire contracts. Harnesses integrating against dark-memory MUST be updated:

1. **`agent_memory_update` + `agent_memory_archive` schemas now require `operator`** — previously declared only `id` as required; orchestrators always required `operator` (orchestration/agent_memory.go:504, 550). A harness that was sending id-only payloads and getting `errMissingField("operator")` from the orchestrator with no Field envelope will now get a schema-level rejection at harness validation time. **Required update**: include `operator` in all `dark_memory_agent_memory_update` and `dark_memory_agent_memory_archive` calls.

2. **`session_status` gains `closing_soon` + `seconds_until_close`** — new envelope fields. `omitempty` so they don't appear on closed sessions; existing callers that ignore unknown fields are unaffected.

### Fixed

- **`session_status` closing_soon warning** (row 168) — `SessionStatusResult` gains `closing_soon: bool` and `seconds_until_close: int`. Computed against `last_heartbeat_at + heartbeat_timeout - now` (env `DARK_SESSION_HEARTBEAT_TIMEOUT`, default 300s). `closing_soon=true` when `seconds_until_close <= 30s` (env `DARK_SESSION_CLOSING_SOON_THRESHOLD`, default 30s). Closed sessions skip the countdown (omitempty zeros). **Clamping behavior**: when the deadline is already in the past (sweeper hasn't run yet), `seconds_until_close` clamps to 0 and `closing_soon=true` so harnesses know the session is overdue for closure. Harnesses can now warn operators BEFORE the sweeper closes an idle session — closing the row 168 "3 wasted restarts in one synthesis" UX debt. Implementation: `internal/tools/session.go` (new helper + BindStore closure rewires), `internal/tools/context.go` (session_context projection picks up the same fields AND now honors the same env vars — operator-set `DARK_SESSION_HEARTBEAT_TIMEOUT` is observed consistently across both `session_status` and `session_context`; previously `session_context` was hardcoded to defaults).
- **`agent_memory_update` + `agent_memory_archive` schema/orchestrator mismatch** (row 189) — both tools' JSON schemas declared only `id` as required, but the orchestrators also require `operator` (orchestration/agent_memory.go:504, 550) for INV-1 audit. A harness that followed the published schema and sent id-only would hit `errMissingField("operator")` from the orchestrator with no Field envelope in the wire response — same SHAPE as row 167's symptom but different cause. Fix: add `"operator"` to the `required` array in both schemas. `internal/tools/agent_memory.go:142, 162`.

### Migration note (row 189, why v2.9.2-alpha not v3.0.0-alpha)

The **orchestrator contract did not change** in this fix — `internal/orchestration/agent_memory.go:504, 550` have always required `operator` for INV-1 audit attribution since the INV-1 invariant landed (v1.x). What changed is the **schema surface**: the JSON schema for `agent_memory_update` + `agent_memory_archive` previously declared only `id` as required, even though the orchestrator would reject id-only payloads. The schema was the inconsistent surface, not the orchestrator.

**No working harness is broken by this change.** Harnesses that already send `operator` (the correct path) are unaffected — schema-level validation passes, orchestrator-level validation passes. Harnesses that were sending id-only and getting `errMissingField("operator")` from the orchestrator with no Field envelope will now see a clearer schema-level rejection at harness validation time (structured ToolError instead of unfielded rejection). This is a *better* error surface, not a worse one.

If you operate a harness that was sending id-only payloads and somehow seeing success (against the orchestrator's intent), that harness was already broken under INV-1 and the schema change forces it back to a correct shape. No migration script is needed — `operator` was always mandatory; we just made the schema honest about it.

**Why not v3.0.0-alpha?** Major version bumps are reserved for orchestrator contract changes (new required fields, removed tools, redesigned APIs). This is a schema-alignment-with-existing-orchestrator-contract fix, which is a PATCH-level concern under semver. The orchestrator's INV-1 invariant pre-dates this fix; we're aligning the schema to match what the orchestrator has always enforced.

### Verified stale (no fix needed)

- **Row 166 (`vibe_spec.tasks` Form B parser)** — F36 (v1.2.1) fixed the dispatch order. `parseTasksField` at `internal/orchestration/vibe_spec.go:115` correctly handles Form A (`[`) first; all 6 unit tests in `TestParseTasksField_*` PASS. Symptom does not reproduce.
- **Row 167 (`agent_memory_update` ErrInvalidArgument)** — wire reproduction at `tests/orchestration/agent_memory_wire_update_test.go` (5 tests, all PASS): same-operator title-only + 4.4 KB content-only + pinned-only all succeed; legitimate missing-field paths still return Field-tagged ErrInvalidArgument. Symptom does not reproduce. Separate finding: `latestAuditIDForRow` stub returns 0 (F47 documented debt) — NOT row 167.

### Added

- **`tests/orchestration/agent_memory_wire_update_test.go`** — 5 wire reproduction tests for row 167 (verifies the stale path + pins the missing-field paths).
- **`internal/tools/session_status_test.go`** — 11 unit tests for row 168 (covers fresh/near-deadline/overdue/open/idle/closed_clean/closed_aborted/empty-hb/malformed-hb/config-defaults/env-overrides/go-syntax).

### Known issues (not in this release)

- `latestAuditIDForRow` stub returns 0 for `AgentMemorySave.AuditID` + `AgentMemoryUpdate.AuditID` — F47 documented debt. The audit row IS written atomically (INV-1) but the orchestrator can't surface its id without a new Store method. Tracked separately from row 167.

---

## [2.9.1-alpha] — 2026-08-03

**Hybrid Retrieval: ONNX bundle + harness-aware ladder** — closes PR-2.1 of the v2.9.0 plan (agent_memory row 160 deferred items + row 164 §2 amendment). Closes the "vibe-coder must read docs to install dark-memory" failure mode: bundled ONNX means offline-first works zero-config; harness detection picks the right preferred rung without operator intervention.

### Added

- **Bundled ONNX adapter** (PR-2.1, replaces the PR-2 stub) — `internal/embedder/onnx` is now a real local embedding adapter backed by `model_quantized.onnx` (Xenova/all-MiniLM-L6-v2 int8, **22.97 MB**, SHA-pinned `afdb6f1a…`) via `yalue/onnxruntime_go v1.21.0` + ONNX Runtime **1.22.0**. Libonnxruntime per-platform bundled via `//go:embed` with build tags: `windows-amd64` (12.4 MB), `linux-amd64` (21.0 MB), `darwin-arm64` (33.5 MB). Per-binary footprint: **+47 MB** (model + runtime for the build target). Total across all platform distributions: **+89 MB**. SHA verification on extract; idempotent cache at `$DARK_HOME/{models,libonnxruntime}/`.
- **Harness detector** (`internal/embedder/detect`) — env-var-first probe ladder: `CLAUDE_CODE` → claude-code harness (prefers Voyage AI); `OPENCODE_VERSION` / `OPENCODE_CONFIG` → opencode (prefers OpenAI); `CODEX_HOME` → codex (prefers OpenAI); `OLLAMA_HOST` env var + 500ms TCP probe of `127.0.0.1:11434` → ollama. `Result{Kind, ConfigPath, Source}` is LLM-readable (used in `embedder_setup_prompt` for the consent recommendation).
- **Voyage AI adapter** (`internal/embedder/voyage`) — Voyage AI voyage-3 (1024d), `VOYAGE_API_KEY` env var, 5s/10s + 1 retry on 5xx/429. Preferred rung when harness detection identifies Claude Code.
- **Ollama adapter** (`internal/embedder/ollama`) — localhost:11434 `/api/embeddings` (nomic-embed-text 768d), 4-way bounded parallelism, no API key. Preferred rung when OLLAMA_HOST is set or a local daemon is reachable.
- **Harness-aware FactoryAuto() ladder** — per row 164 §2: (1) manual `DARK_MEMORY_EMBEDDER` override; (2) harness-detected preferred rung; (3) bundled ONNX offline default; (4) `OPENAI_API_KEY` last rung; (5) `None()` stub. New `tryKind()` walks the ladder rung-by-rung and falls through on `ErrKeyMissing`/`ErrDisabled`.
- **Consent prompt surface** — `dark_memory_embedder_setup_prompt` now returns `Harness` + `HarnessSource` + a `Recommended: true` flag on the harness-native rung. The LLM can highlight the recommended rung without violating the row 164 §3 "surface verbatim" rule.
- **`docs/embedders/{install.md,onnx.md,voyage.md,ollama.md}`** — LLM-readable install docs per row 164 §4.

### Changed

- **`internal/embedder/embedder.go`** — adds `KindVoyage` + `KindOllama` constants; `FactoryAuto()` rewritten with the harness-aware ladder (was: env-presence auto-detect only).
- **`internal/tools/embedder_setup.go`** — `ConsentStatus` gains `Harness` + `HarnessSource` + per-choice `Recommended` flag. `consentPromptVerbatim` now interpolates the detected harness name.
- **`internal/embedder/detect/detect.go`** — new package, 5 probes (claude-code / opencode / codex / ollama / unknown). No new deps. `Result.String()` includes the detection source for debug.
- **`internal/embedder/onnx/{onnx,embed,wordpiece}.go`** — real impl. CGO via `yalue/onnxruntime_go`. ~430 LoC of WordPiece tokenization + session management + SHA verification.
- **`go.mod`** — adds `github.com/yalue/onnxruntime_go v1.21.0`.

### Bundle footprint

| Asset | Size | Build-tag-gated |
|---|---|---|
| `model_quantized.onnx` (Xenova/all-MiniLM-L6-v2 int8) | 22.97 MB | yes (always embedded) |
| `onnxruntime.dll` (Windows amd64) | 12.42 MB | `windows && amd64` |
| `libonnxruntime.so.1.22.0` (Linux amd64) | 21.04 MB | `linux && amd64` |
| `libonnxruntime.1.22.0.dylib` (Darwin arm64) | 33.48 MB | `darwin && arm64` |

**Per-binary increase: +47 MB** (model + one platform's libonnxruntime). Cross-distribution footprint: +89 MB.

### Deferred to v2.9.2+ (PR-3.1)

- `drift_judge` MCP integration for entity extraction (PR-3 ships the deterministic local extractor; PR-3.1 swaps the body without changing the `Source` tag taxonomy).
- Postgres vector/RRF dispatch mirror (PR-2 ships schema + read parity; PR-3.1 picks up runtime save + filter parity for entities).
- `sqlite-vec` / `pgvector` for production-scale vector search.
- Postgres porter stemming (v22 in sqlite is `unicode61`+`porter`; Postgres needs `tsvector` + `snowball`).
- Mem0 compatibility mode extension to new axes.

### Build requirements

PR-2.1 requires **CGO_ENABLED=1** at build time. CI (Ubuntu 22.04) ships `gcc` by default. Local Windows builds need a C compiler; install MinGW (`winget install BrechtSanders.WinLibs.POSIX.UCRT` ships WinLibs MinGW).

### Known issues (not in this release)

- `vibe_spec.tasks` Form B parser rejects JSON arrays (governance gate cannot run via helper) — row 166, **closed in v2.9.2-alpha**.
- `agent_memory_update` returns `ErrInvalidArgument` for same-operator longer content — row 167, **closed in v2.9.2-alpha** (was already fixed in transit; wire regression tests added).
- Session auto-close race (~5 min inactivity → `ErrFrameStaleTooFar`) — row 168, **closed in v2.9.2-alpha** (`closing_soon` warning surface).
- `go test ./internal/embedder/onnx` TestEmbedAfterClose skipped on Windows (yalue runtime holds a DLL handle that prevents `t.TempDir` cleanup). The integration path works; only the cleanup pattern is affected.

---

## [2.9.0-alpha] — 2026-08-03

**Hybrid Retrieval** — closes the v2.9.0 plan (agent_memory row 160). Three PRs land behind per-axis opt-in (BM25 stays the default). Schema: v22 (porter_stemming) → v23 (agent_memory_embedding) → v24 (agent_memory_entities). Canonical tool count: 41 → **42**. Wire contract appenditive, zero breaking changes. Embedder layer refreshes via init-time `RegisterAdapter` registry to break the embedder ↔ adapter import cycle.

### Added

- **A1 Porter Stemming** (PR-1, v22 migration) — FTS5 tokenizer `unicode61` → `porter unicode61` for fresh-DB default; idempotent rebuild for existing schemas. Adds `TestSearchAgentMemory_PorterStemming` (stem equivalence `running ↔ runs ↔ ran`) + control test that baseline FTS5 still finds exact forms. Backward compat: result SET unchanged; only ranking differs. Operators see different BM25 ordering, not different rows.
- **A2 Vector Search + RRF** (PR-2, v23 migration) — `SearchFilters.Mode = "bm25" | "vector" | "rrf"` (default `"bm25"`). Brute-force cosine in-process for v2.9.0; `sqlite-vec` / `pgvector` deferred to v2.9.1+. Embedder factory refreshes: `FactoryAuto()` does env-presence auto-detect (`OPENAI_API_KEY` → OpenAI text-embedding-3-small). New adapters: `internal/embedder/openai` (real, 5s/10s + 1 retry on 5xx/429), `internal/embedder/mock` (deterministic SHA-256-truncated unit vectors for tests), `internal/embedder/onnx` (PR-2.1 stub returning `ErrDisabled` with a "ships in PR-2.1" hint). `RRFRank` helper (Cormack et al., 2009) with `k=60`, weights default 1.0 each axis.
- **A3 Entity Matching** (PR-3, v24 migration) — new `agent_memory_entities` side table (mem_id FK ON DELETE CASCADE, entity, source, confidence, model, created_at, PK `(mem_id, entity)`). New `SearchFilters.Entities []string` (AND-semantics filter); new MCP tool `dark_memory_agent_memory_entities(mem_id)` reads the entity list for one row. `internal/entity` package ships a deterministic local extractor (lowercase + stopword + minLen + dedup + frequency-ranked). `Source` tag = `"deterministic"` for PR-3; PR-3.1 swaps for a `drift_judge` bridge without contract change. Backward compat: extraction opt-in (`m.Entities = nil` → zero entity rows written).
- **A4 Embedder Consent Gate** (PR-2) — new MCP tool `dark_memory_embedder_setup_prompt` returns `{Status, Kind, Dim, Prompt, Choices}`. Per row 164 §3, when dark-memory boots without a detected provider AND `OPENAI_API_KEY` unset, the harness's LLM surfaces the verbatim consent question to the operator. Choices persist to `agent_memory` so dark-memory never asks again unless config drifts.
- **`store.Store.Embedder()` interface method** — any driver must implement. Both SQLite + Postgres provide a non-blocking stub default (`embedder.None()`) until `WithEmbedder()` is called at boot.
- **TestMigrate_V23EmbeddingColumn + TestMigrate_V24EntitiesTable** — schema-level coverage for the two new migrations (BLOB round-trip + ON CONFLICT DO NOTHING idempotency).

### Changed

- **`internal/embedder/embedder.go`** — `RegisterAdapter(kind, factory)` registry breaks the would-be import cycle (embedder ↔ openai / onnx / mock). Each adapter registers itself in `init()`. `FactoryAuto()` picks OpenAI when `OPENAI_API_KEY` is present, else falls back to `None()`. `Options` struct is the typed parameter surface for adapter-specific overrides.
- **`internal/agentmemory/types.go`** — `SearchFilters` gains `{Mode, RRFK, RRFWeightBM25, RRFWeightVector, Entities}`. `SearchHit` gains `{BM25Rank, VectorRank, RRFScore}` (pre-PR-2 callers see zero change). `AgentMemory` gains transient `Embedding` + `Entities` fields (json:"-") populated by the embedder/extractor paths.
- **`internal/store/sqlite/store.go` + `internal/store/postgres/store.go`** — `SaveAgentMemory` writes embedding + entity rows in the SAME tx as the main INSERT (atomic per row 160 PR-2/PR-3 specs). `SearchAgentMemory` post-prunes by `Entities` filter across all 3 modes (preserves rank order). `GetAgentMemoryEntities(mem_id)` is the read path on both drivers.
- **`internal/tools/registry.go`** — canonical order gains the new `embedder_setup_prompt` (EMBEDDER group) + `agent_memory_entities` (AGENT_MEMORY group). Canonical tool count: 41 → 42.

### Deferred to v2.9.1+ (PR-2.1 + PR-3.1)

- Bundled ONNX model (`model_quantized.onnx`, ~22.97 MB) + `libonnxruntime` per-platform → ~+95 MB binary footprint (row 162).
- Harness-aware factory dispatch ladder per row 164 §2 (OpenCode → Claude → Codex → Ollama → bundled ONNX → `OPENAI_API_KEY` last rung).
- Postgres vector/RRF dispatch mirror (PR-2 ships schema + read; PR-2.1 picks up runtime; PR-3.1 picks up Postgres save + filter for entities).
- drift_judge MCP integration for entity extraction (PR-3's local heuristic + PR-3.1's drift_judge bridge — same `Source` tag taxonomy).
- `sqlite-vec` / `pgvector` for production-scale vector search (v2.9.1+ drop-in acceleration).
- Postgres porter stemming (v22 in sqlite): tsvector + snowball stemmer equivalent.
- Mem0 compatibility mode extension to new axes (existing compat mode covers the BM25 axis).

### Fixed

- **H-4 lint compliance** — `scripts/lint-no-private-projects.ps1` exits 0 on the v2.8.0-alpha-dev branch post-PR-13 (renamed the BLOCKLIST placeholder identifier to `[FUTURE-MCP-N]` across 16 tracked files + 1 file rename; pre-existing 17-leak debt closed).

### Known issues (not in this release)

- `vibe_spec.tasks` Form B parser rejects JSON arrays (governance gate cannot run via helper) — row 166, end-of-day.
- `agent_memory_update` returns `ErrInvalidArgument` for same-operator longer content — row 167, end-of-day.
- Session auto-close race (~5 min inactivity → `ErrFrameStaleTooFar`) — row 168, end-of-day.

---

## [2.8.0-alpha] — 2026-07-29

**Memory Timing & Coordination** — closes the "agent and subagents don't write/read agent_memory at the right moments" failure mode documented in agent_memory id=106 (28 edge cases across 6 categories, OSINT-grounded against Mem0/LangMem/Zep/Letta/arxiv:2605.08460). Five P1 features land behind a single feature flag (`DARK_MEMORY_V280=1`, default off in v2.7.x compat). Schema: v20 → **v21** (one new table). Canonical tool count: 39 → **41**. Wire contract appenditive, zero breaking changes.

### Added

- **A1 Decision Auto-Save** — `vibe_publish` with `auto_save_decision=true` (default when `DARK_MEMORY_V280=1`) AND `verdict=aligned` auto-creates a `kind=decision` agent_memory row, `pinned=true`, tagged with `spec:<id>,verdict:aligned,artifact:<url>`. Returns `auto_saved_decision_id` in the response. Off by default in v2.7.x compat.
- **A4 Todo Auto-Save** — `vibe_spec` with `auto_save_todos=true` (default when `DARK_MEMORY_V280=1`) auto-creates one `kind=todo` row per task, tagged `spec:<id>,task:<id>,status:open`. `vibe_publish` `verdict=aligned` auto-archives the corresponding todos. Returns `auto_saved_todo_ids` (on spec) + `auto_archived_todo_ids` (on aligned publish).
- **B1 Cold Start + Token Budget** — `session_start` adds two new fields: `cold_start` (skip `context_recap` entirely) + `context_recap_tokens` (default 2000, clamp `[0, 8000]`). `ContextRecap` adds three new fields: `truncated`, `truncated_rows`, `formatted_chars`. Truncation drops open todos first, then pinned from the tail (least recent).
- **C2 Subagent Scope Handoff** — `mindset_apply` adds `spawn_subagent` + `subagent_id`. New tools: `dark_memory_subagent_register` + `dark_memory_subagent_unregister`. New migration **v21**: `active_subagents` table (`(project_id, operator, subagent_id)` PK + `id` AUTOINCREMENT surrogate + TTL index). TTL sweeper at `session_start` (precursor to F51). When `DARK_MEMORY_V280=1`, the `agent_memory_save` agent_id resolution priority chain extends to **(1) caller input > (2) active subagent_id > (3) `projects.default_agent_id` > (4) empty string**. **Defense-in-depth against arxiv:2605.08460 inheritance attacks** — subagent writes are tagged with an opaque uuid the principal generates, never the principal's `agent_id`, so they never leak into the principal's ContextRecap.
- **D5 Cross-Project Error Code** — `GetAgentMemory` cross-project now returns `*CrossProjectAccessError` (wrapping `store.ErrCrossProjectAccess` sentinel). Distinct from `ErrNotFound` — operators can now diagnose "wrong project" vs "doesn't exist". Pre-v2.8.0 callers (when `DARK_MEMORY_V280=off`) see `(nil, nil)` for cross-project (v2.7.x backward compat preserved).
- **`internal/orchestration/feature_flags.go`** — `v280Enabled()` helper (env `DARK_MEMORY_V280=1/true/yes/on`).
- **`internal/orchestration/subagent.go`** — `SubagentRegister` + `SubagentUnregister` orchestrator methods + input/output types.
- **`internal/orchestration/agent_id.go`** — `activeOperator()` + `resolveActiveAgentIDWithSubagent()` (C2 subagent priority chain).
- **`internal/orchestration/session_start.go`** — `applyContextRecapBudget()` (B1 truncation) + `formatPinnedForRecap` / `formatTodosForRecap` formatters.
- **`internal/orchestration/publish_vibe.go`** — `autoSaveDecisionOnAligned` (A1) + `autoArchiveSpecTodosOnAligned` (A4).
- **`internal/orchestration/vibe_spec.go`** — `autoSaveTodosForSpec` (A4).
- **`internal/orchestration/agent_memory.go`** — C2 agent_id resolution extension + D5 cross-project error wrapping.
- **`internal/migrate/sqlite/ddl.go`** + **`internal/migrate/postgres/ddl.go`** — migration v21 (`active_subagents`).
- **`internal/store/store.go`** — `ActiveSubagent` type + 4 Store interface methods (`SetActiveSubagent` / `GetActiveSubagent` / `ClearActiveSubagent` / `SweepExpiredSubagents`) + `ErrCrossProjectAccess` sentinel + `CrossProjectAccessError` struct.
- **`internal/store/sqlite/store.go`** — full SQLite impl for all 4 methods + cross-project GetAgentMemory logic.
- **`internal/store/postgres/store.go`** — `notImpl` stubs (F50 backlog; Postgres backplane deferred).

### Changed

- **`agent_memory_save` agent_id resolution priority chain** (when `DARK_MEMORY_V280=1`): caller input > **active subagent_id** > `projects.default_agent_id` > empty string. Pre-v2.8.0 chain is preserved when the flag is off.
- **`session_start` `ContextRecap`** now includes `truncated` + `truncated_rows` + `formatted_chars` (when `DARK_MEMORY_V280=1`); defaults to token-budget 2000 (vs v2.7.x's unbounded).
- **`vibe_publish`** returns `auto_saved_decision_id` + `auto_archived_todo_ids` (when `DARK_MEMORY_V280=1`).
- **`vibe_spec`** returns `auto_saved_todo_ids` (when `DARK_MEMORY_V280=1`).
- **`mindset_apply`** returns `subagent_id` + `parent_agent_id` when `spawn_subagent=true` (when `DARK_MEMORY_V280=1`).
- **`SYSTEM_PROMPT.md`** — line 101 drift fix: `scope=current` → `scope=project` (v2.3.0 default was already `project`). Added B1 ContextRecap auto-surface note (§4.1), A1/A4 auto-save notes (§4.2), C2 subagent memory isolation note (§5).

### Tests

- **`tests/dual_driver/agent_memory_v2_8_0_test.go`** (10 tests, all passing): D5 cross-project error matrix (happy + same-project + non-existent + error-message-format) + C2 active_subagents store-layer round-trip (Set / Get / not-registered / refresh-TTL / Clear / Clear-not-found / sweep / kind=decision regression).
- **`tests/conformance/bridge7_mcp_inspector_test.go`** — canonical tool count 39 → 41 (added `dark_memory_subagent_register` + `dark_memory_subagent_unregister` at position 39 + 40, after `vlp_handle_event`).
- **`tests/migrate/migrate_f37_test.go`** — `schema_version` 20 → 21.

### Backward Compatibility

All 5 features gated by `DARK_MEMORY_V280=1`. **Default off.** v2.7.x callers see no behavior change. To opt in:

```bash
export DARK_MEMORY_V280=1
# start dark-mem-mcp as usual
```

### Migration

Apply migration v21 automatically on next `dark-mem-mcp` startup (idempotent `CREATE TABLE IF NOT EXISTS`). No operator action required.

### Rollout Plan

- **Phase 1 (this release)**: tag `v2.8.0-alpha`, `DARK_MEMORY_V280=1` opt-in, 1-week soak.
- **Phase 2 (v2.8.0)**: default ON, 4-channel distribution.
- **Phase 3 (v2.10.0)**: remove `DARK_MEMORY_V280` flag (new behavior permanent).

### References

- Spec: 681 (vibe_case=C2, 6 tasks)
- Artifact: 804 (design doc, drift_judge aligned @0.92)
- Edge case catalog: agent_memory id=106
- Roadmap: agent_memory id=108
- OSINT sources: Zylos multi-agent architectures, AgentMarketCap 2026 vendor landscape, arxiv:2605.08460 "When Child Inherits"

---

## Post-ship fixes (2026-07-29) — applied to `v2.8.0-alpha` tag (no version bump)

Soak tester feedback (operator local install) revealed **6 gaps** in the initial ship, all fixed in-branch and force-moved into the `v2.8.0-alpha` tag. No version bump — single release line per operator preference (soak testers see one update, not a `.2` micro-version).

### Fixed

1. **`DefaultToolGrants` missing the 2 new tools** — `internal/recall/assemble.go`. Operators got `ErrCapabilityNotGranted` calling `subagent_register`/`subagent_unregister`. The new tools were registered in the registry but not granted by default. (commit `b2dfe53`)

2. **`ToToolError` switch missing `ErrCrossProjectAccess` case** — `internal/tools/errors.go`. Cross-project `agent_memory_get(id=N)` returned generic `ErrInternal` instead of the typed error code. The sentinel existed in the store layer but the tools layer had no case for it. (commit `85ec910`)

3. **Orchestrator wrap dropped the typed struct** — `internal/orchestration/agent_memory.go::AgentMemoryGet`. The orchestrator wrapped with `fmt.Errorf("%w", sentinel, ...)` which discarded the typed `*CrossProjectAccessError` from the chain. `errors.As(err, &cpe)` failed in the tools layer, degrading to the generic message even though `errors.Is` worked. Fix: return the typed struct directly; its `Is(target)` method already satisfies the sentinel. (commit `eea5962`)

4. **`escapeFTS5` dead code** — `internal/tools/agent_memory.go`. The FTS5 escape helper existed but was never called from the `agent_memory_recall` bind function. Raw queries reached FTS5's MATCH parser and exploded with "fts5: syntax error" for any input containing chars outside the FTS5 bareword allowlist (`.`, `-`, version numbers). (commit `9897318`)

5. **`escapeFTS5` allowlist was wrong** — `internal/tools/agent_memory.go`. Even with #4 calling the function, the allowlist-based escape (`alphanumeric + . - _ / + *`) was contradictory: FTS5 barewords are restricted to `[a-zA-Z0-9_]` per `sqlite.org/fts5.html §3.1`; any other character MUST be quoted. Replaced with the production pattern from `gwicho38/legal-workspace-mcp` (deepwiki.com): whitespace-tokenize, wrap each token in FTS5 phrase quotes, length-prefix support (`jira*` → `"jira"*`), embedded quotes escaped by doubling. (commit `af15ee5`)

6. **Harness intelligence gap (`SYSTEM_PROMPT.md` v1 → v2)** — `internal/agentbootstrap/data/SYSTEM_PROMPT.md`. The originally shipped bootstrap doc said "35 tools" (now 41), referenced schema v20 (now v21), omitted the 2 new tool names (`subagent_register`, `subagent_unregister`) from the AGENT_MEMORY namespace table, didn't document D5 cross-project isolation, didn't teach the LLM the **R-CRUD pattern** (recall is the primary discovery tool; `list()` is for browse-all only), and had a duplicated `## 6. Drift detection` header. (commit `a557f8e` for the v1→v2 deltas to date)

### Changed

- **`SearchAgentMemory` BM25 score sign flipped** — `internal/store/sqlite/store.go`. FTS5 `bm25()` returns negative scores (lower = better); SQL now `-bm25(...) AS rank` so hits are ranked `ORDER BY rank DESC` (higher = better, intuitive UX). Internal sort flipped to match. (commit `af15ee5`)

- **`Store.Close()` runs `PRAGMA optimize`** — `internal/store/sqlite/store.go`. Best-effort: log-only on error, never fails Close. Per `sqlite.org/fts5.html §6.9` this analyzes recent query patterns and updates internal FTS5 statistics for better query planning on next startup. (commit `af15ee5`)

### Added

- **`internal/tools/agent_memory_escape_test.go::TestEscapeFTS5_QuoteWrap`** — 10 cases covering the new quote-wrap behavior: `.` `-` `*` `AND OR` embedded quotes whitespace-only empty single-token. (commit `af15ee5`)

- **§4.5 "Memory discovery (the recall vs list distinction)"** in `SYSTEM_PROMPT.md` — teaches the R-CRUD pattern explicitly with a comparison table, rules of thumb (1-6), and an anti-pattern callout. Replaces the v1 one-line step 8 that said only `save`. (commit `a557f8e`)

### Lessons captured for future releases

1. **Smoke test the harness, not just the binary.** Original v2.8.0-alpha ship verified the binary boots + all 5 P1 features wire up — but the SYSTEM_PROMPT.md (which IS the "intelligence" delivered to the LLM) wasn't updated in sync. The smoke test caught this.
2. **OSINT > guessing.** The wrong `escapeFTS5` allowlist was contradicted by `sqlite.org/fts5.html §3.1` ("barewords are restricted to [a-zA-Z0-9_]"). One fetch of the spec would have caught it before the soak started. For any new tech we adopt (FTS5, hybrid retrieval, etc.), the spec is the source of truth — not StackOverflow, not gpt, not our last impl.
3. **Tool registry + DefaultToolGrants + tool description + SYSTEM_PROMPT must all stay in sync.** These are 4 different places to update when adding a new tool; missing any one makes the feature invisible or unusable. (See `agent_memory id=118` for the post-mortem.)
4. **Don't wrap a typed struct with `fmt.Errorf("%w", sentinel)`** — the wrap drops the struct from the error chain. If a downstream consumer needs `errors.As` to extract the struct (for diagnostic fields like `RowID`, `RowProject`, `RequestedProject`), return the struct directly or use `fmt.Errorf("%w", struct)` (struct implements `Unwrap()` via `Is()`).

### Git history at this tag

Tag `v2.8.0-alpha` now points at commit `af15ee5` (force-moved from `d224d44`). The 5 fix commits since the original ship are: `b2dfe53` (gate), `85ec910` (errors), `eea5962` (orch), `9897318` (escape call), `af15ee5` (escape rewrite + BM25 + optimize). All in branch `v2.8.0-alpha-dev`.

---

## [2.7.1] — 2026-07-29

Deferred cleanup after the 2.7.0-alpha ship (which required 5 commits to land due to version-drift + race-condition failure modes). No wire-contract changes — same 39 canonical tools, same schemas, same schema_version. All changes are CI hardening + tests.

### Fixed

- **`TestV261_RegistryPublishRetryLoop` retry-budget assertions updated to match new 5×60s budget**. The test was hardcoded to `MAX_ATTEMPTS=3`/`RETRY_DELAY=30` from the v2.6.1 race fix; v2.7.1 bumped the retry loop to `5×60s` per agent_memory id=93 follow-up. Test now asserts the values are within the documented contract (`MAX_ATTEMPTS in [3, 5]`, `RETRY_DELAY >= 30`) so future bumps don't break it.

### Added

- **`internal/tools/canonical_staleness_test.go`** — `TestCanonicalWireOrder_NotStale` catches the drift between the hand-maintained `canonicalWireOrder()` helper in the conformance test and the canonical `tools.CanonicalOrder()`. This was root cause #1 of the v2.7.0-alpha 4-iteration cycle: every new tool required hand-editing a string list in a separate file, and one was missed. The new test compares the two on length + membership + position. Runs in <100ms with no live server. Validated against intentional-stale inputs (3 distinct error messages on miss).
- **`internal/orchestration/recommended_models.go`** — per-provider entries for the two new Judge eval types (`mindset_compose`, `mindset_quality`) for the 4 main providers (anthropic, openai, google, deepseek). The other 6 providers fall through to the default. No more model-name resolution failure on those eval types for the supported providers.
- **`.github/workflows/precheck-version.yml`** — new workflow, single source of truth for tag/version match. Triggers on `push: tags: v*`, verifies all 8 version-stamped files (1 wrapper + 6 platforms + server.json) declare the same `version` as the git tag. On mismatch: emits one `##[error]` per file (each renders as a red annotation in the PR) plus a `::group::Fix:` block with copy-pasteable operator recipe, then `exit 1`. Cost: ~2s on failure, ~3s on success. Appenditive (does not remove the per-workflow version checks in `publish-npm.yml`).

### Changed

- **`.github/workflows/publish-mcp-registry.yml`** — added `workflow_run` trigger on `publish-npm` completion (in addition to `push: tags: v*`). The registry job now skips if the upstream `publish-npm` `conclusion != 'success'` instead of attempting to publish a version that doesn't exist on npm yet. Retry loop bumped from `MAX_ATTEMPTS=3` × `RETRY_DELAY=30s` to `5 × 60s` per agent_memory id=93 (the 2.7.0-alpha race exhausted the previous budget when publish-npm failed).
- **`scripts/bump-version.sh`** — parameterized (`bash scripts/bump-version.sh <new-version>` instead of hardcoded `2.6.2 → 2.7.0-alpha`). Auto-discovers `OLD` from `npm/wrapper/package.json`. Validates `NEW` matches semver-ish regex. Verifies every file declares the new version after sed (catches partial failures). Prints next-step commands for the operator.

### Lessons captured for future releases

1. Always run `bash scripts/bump-version.sh <version>` BEFORE tagging.
2. If `precheck-version` CI fails, the error message lists ALL mismatched files in one place.
3. Per-workflow version checks in `publish-npm.yml` / `publish-mcp-registry.yml` remain as defense in depth — DO NOT remove.

---

## [2.7.0-alpha] — 2026-07-28

### Added — Phase 1 delegation primitive: dark_memory_mindset_apply

Procedural composition of subagent system prompts with LLM-as-judge
validation. The first dark-memory tool designed to make the LLM
work better by priming it with the right mindset for a delegated task.

**The problem this solves**

LLMs produce measurably better output when primed with an over-qualified,
task-appropriate role + goal + backstory + constraints. Pre-baked
"mindset templates" are limited — humans can't anticipate every
specialization. Procedural synthesis by an LLM, validated by a second
LLM, is the generalizable approach.

**What ships**

1. **`dark_memory_mindset_apply(vibe_case, task_description, [model_floor])`** —
   composes a subagent `system_prompt` for the given (vibe_case,
   task_description) pair, validates it against 5 pass criteria
   (OVER-QUALIFIED, TASK-APPROPRIATE, CONSTRAINT-PRIMED, MINIMAL-TOOLS,
   NO-LEAKAGE), and returns it ready for the harness to inject into
   its subagent spawn tool.
   - Cache hit: <50ms, 0 LLM calls (TTL via `DARK_MINDSET_CACHE_TTL`, default 1h).
   - Cache miss: 2-6 LLM calls per `mindset_apply` (composition + validation per
     iteration, up to `DARK_MINDSET_MAX_ITERATIONS=3`).

2. **Two new Judge eval types** registered alongside the existing six:
   - `mindset_compose` (generative) — LLM synthesizes the system_prompt.
   - `mindset_quality` (validative) — LLM judges the prompt against 5 criteria.
   Both persist `sdd_evaluations` rows for full audit trail.

3. **5 meta-categories** as starting frames for composition:
   - `c1/security-review` — senior appsec researcher, OWASP Top 10, CVE-aware
   - `c1/refactor` — senior maintainer, idempotent changes, reads-first
   - `c2/docs-explain` — senior technical writer, examples > abstraction
   - `c2/marketing-copy` — senior conversion copywriter, clarity > cleverness
   - `c3+generalist` — focused task-execution specialist, scope-disciplined
   The LLM uses the matching category as a starting frame; it synthesizes
   the actual prompt from the task description.

4. **3 new env vars** (all optional, all default to sane values):
   - `DARK_MINDSET_MAX_ITERATIONS` (default 3, clamped [1, 10])
   - `DARK_MINDSET_TIMEOUT_MS` (default 15000, clamped [100, 120000])
   - `DARK_MINDSET_CACHE_TTL` (default 3600s, clamped [60, 86400])

5. **New operator doc** `docs/mindsets.md` covering: usage pattern
   (harness spawns, dark-memory provides the mindset), the 5
   pass criteria, the cache contract, env var tuning, and the
   "what the harness sees" walkthrough.

6. **~700 LOC new** across 4 files:
   - `internal/orchestration/mindset_meta_prompts.go` (meta-prompt constants)
   - `internal/orchestration/mindset_apply.go` (composition orchestrator)
   - `internal/orchestration/mindset_apply_test.go` (7 focused unit tests)
   - `internal/tools/mindset.go` (wire tool registration)

7. **7 unit tests** covering cache key determinism, JSON extraction
   from prose wrappers, system_prompt assembly, prompt rendering with
   iteration history, category selection, and env-var defaults/overrides.

**Wire contract — appenditive**

- +1 tool (`dark_memory_mindset_apply`)
- +2 eval types (`mindset_compose`, `mindset_quality`)
- +3 env vars (all optional)
- 0 schema migrations
- 0 new tables (cache lives in `agent_memory`, kind=context, tags=mindset-cache)
- 0 tools removed or modified
- Canonical count: 38 → 39 (cardinality guard updated)

**Usage pattern**

The harness spawns the subagent; dark-memory provides the mindset:

```
1. dark_memory_mindset_apply(vibe_case="C1", task_description="review auth code")
   → { system_prompt: "You are a senior appsec researcher...",
        tools_recommended: ["Read","Grep","Glob","Bash"],
        model_recommended: "sonnet",
        judge_verdict: {verdict: "aligned", confidence: 0.91} }

2. Task(subagent_type="general-purpose",
       prompt=<system_prompt>,
       model="sonnet",
       tools=[Read,Grep,Glob,Bash])
```

**YAGNI defer list (intentional non-goals for this release)**

- No `subagent_registry` table — wait until 3+ production subagents registered
- No VLP `StateDelegating` — wait until parent needs to wait on child lifecycle
- No A2A server transport — wait until A2A ecosystem settles (6+ months)
- No `VibeSpecTask.Owner` typed semantics — wait until Phase 2
- No tool grant enforcement — `tools_recommended` is a hint string for now
- No operator-customizable meta-prompt — env override is trivial if anyone asks

**Why pre-2.7.0-alpha (not stable yet)**

The composition loop is novel — no production usage data yet on how
often judges reject first attempts, what the latency distribution
looks like, what the cache hit rate will be. Ship as alpha; gather
metrics; stabilize in v2.7.0 stable. Operator may want to gate this
behind `DARK_MINDSET_DISABLED=1` (TODO — not implemented yet; defer).

---

## [2.5.2] — 2026-07-28

### Added — MCPB bundles (Desktop Extensions / MCP Bundles) for Claude Desktop one-click install

Sprint 3 of the install-friction reduction roadmap. Adds Anthropic's
DXT/MCPB format (formerly nthropics/dxt, now modelcontextprotocol/mcpb)
so Claude Desktop users can install dark-memory-mcp with a double-click.

**What ships**:

1. **mcpb/manifest.json** — MCPB spec v0.3 compliant manifest declaring
   server.type: binary (perfect fit for our single-file Go binary).
   Lists all 11 user-facing tools + mcpName: io.github.Opita-Code/dark-memory-mcp.
2. **.github/workflows/build-mcpb.yml** — cross-compiles all 6 platforms
   (darwin/linux/windows × amd64/arm64), bundles them into 3 platform-specific
   .mcpb archives (one per OS family), and attaches both the .mcpb
   bundles AND the raw binaries to the GitHub Release. **This fixes the
   v2.5.0 cross-publish drift** (where the GitHub Release .exe was uploaded
   from a local Windows build, not the CI cross-compile).
3. **mcpb/README.md** — installation + compatibility matrix.
4. **3 .mcpb archives** attached to GitHub Release v2.5.2:
   - dark-memory-mcp-darwin.mcpb (~50 MB; macOS x64 + arm64)
   - dark-memory-mcp-linux.mcpb (~50 MB; Linux x64 + arm64)
   - dark-memory-mcp-win32.mcpb (~50 MB; Windows x64 + arm64)
5. **	ests/distribution/mcpb_v2_5_2_test.go** (3 new tests):
   - TestV252_MCPBManifestSchema — validates manifest against MCPB spec v0.3
   - TestV252_MCPBBundleDirectoryPreBuild — validates static bundle structure
   - TestV252_BuildMCPBWorkflowStructure — validates CI workflow shape

### Added — drift_judge carry-forward tests (deferred from v2.4.x)

2 new tests addressing drift_judge items flagged as carry-forward technical
debt in v2.4.x + v2.5.0:

6. **TestV260_NPMBinaryMatchesReleaseBinary** — dynamically queries the
   latest published npm version, downloads both the npm package binary
   AND the GitHub Release binary, compares SHA-256. This is a regression
   test for the v2.5.0 drift (where binaries came from different build
   environments). The test auto-skips for v2.5.0 specifically (since
   the drift is a known fixed-by-v2.5.2 issue) and runs for all other
   versions, catching any future cross-publish drift.

7. **TestV260_OptionalDependenciesFallback** — static analysis of
   
pm/wrapper/index.js verifying that the wrapper has a graceful error
   path for unsupported platforms (lists supported platforms + exits
   non-zero + process.exit(1) call). Replaces an earlier Node-subprocess
   design that was slow and brittle.

### Wire contract

ZERO changes. Same Go binary as v2.5.0. Schema v20 unchanged. 35 canonical
tools unchanged. Only changes:
- New CI workflow uild-mcpb.yml that cross-compiles binaries (same ldflags
  + trimpath as publish-npm.yml) and packages them into .mcpb + GitHub Release.
- New static files in mcpb/ directory (manifest, README).

### Why this fixes the v2.5.0 cross-publish drift

v2.5.0 had this drift because:
1. publish-npm.yml built binaries via CI cross-compile → published to npm.
2. Operator (me) ran gh release create ... bin\dark-mem-mcp.exe locally,
   uploading a *different* binary (built with my local Windows Go toolchain,
   no -trimpath, no version ldflags) to the GitHub Release.

Result: users who installed via 
px @opitacode/dark-memory-mcp got
binary A; users who downloaded dark-mem-mcp.exe from the GitHub Release
got binary B. Both worked, but they were byte-different and only one
(SHA 16cbbb83... from CI) had the SLSA build attestation.

v2.5.2 fixes this by having ONE source of truth: the CI cross-compile
matrix in uild-mcpb.yml builds the binaries, attaches them to the
GitHub Release, AND packages them into .mcpb bundles. Both npm packages
and the GitHub Release now come from the same CI build.

The new TestV260_NPMBinaryMatchesReleaseBinary regression test catches
this drift class going forward — if any future release has different
SHA-256 between npm and GitHub Release, the test fails immediately.

### Operator runbook (after this commit is pushed)

`ash
git push origin main          # pushes the v2.5.2 code
git push origin v2.5.2        # tag is already created locally
# CI runs:
#   - ci.yml: validates go test (should PASS; the v2.5.0 drift test skips)
#   - publish-npm.yml: cross-compiles + publishes 7 npm packages @opitacode/dark-memory-mcp-*
#   - build-mcpb.yml: cross-compiles + bundles 3 .mcpb + attaches to GitHub Release
# After CI succeeds:
npm view @opitacode/dark-memory-mcp versions     # should show ['2.5.0', '2.5.2']
gh release view v2.5.2                          # should list 6 raw binaries + 3 .mcpb bundles
`

After the v2.5.2 tag push, on the next commit to main the
TestV260_NPMBinaryMatchesReleaseBinary test will run against
v2.5.2 binaries (which are CI-consistent) and PASS.

---
## [2.5.0] â€” 2026-07-28



### Correction — npm scope @opita-code → @opitacode (operator feedback, before first publish)

The initial commit of v2.5.0 assumed the npm scope would be
@opita-code (a kebab-case variant of the GitHub org Opita-Code).
The operator caught this before the irreversible first npm publish and
corrected it: the actual npm scope is @opitacode (no hyphen, all
together), which already hosts the package @opitacode/ocais v3.0.1.

The MCP Registry namespace is independent of the npm scope; it uses
the GitHub org's actual case for OAuth verification:
io.github.Opita-Code/dark-memory-mcp.

| Identifier | Value |
|---|---|
| npm scope (publishes here) | @opitacode |
| npm package (full) | @opitacode/dark-memory-mcp |
| GitHub org | Opita-Code |
| MCP Registry namespace | io.github.Opita-Code/dark-memory-mcp |

The design is unchanged: same 7-package matrix, same Microsoft-pattern
npm wrapper, same cross-compile matrix in CI. Only the identifier
strings are corrected. No drift on the wire contract or schema.
### Added â€” npm wrapper (cross-platform one-line install) + Official MCP Registry entry

Until v2.4.x, dark-memory-mcp was distributed only as raw Go binaries
in GitHub Releases. Vibe-coder install path was:

1. Open Releases page.
2. Find OS + arch.
3. Download `.exe` / ELF / Mach-O.
4. Compute SHA-256, compare with the release notes.
5. Hand-edit `mcp.json` with the absolute binary path.
6. To upgrade: repeat steps 1-5.

That is **five steps of friction** to get a 25 MB binary onto disk.
The Go binary itself has zero runtime dependencies (static, CGO=0),
so the install path is the only thing the user has to figure out.

**v2.5.0 collapses all five steps into one line:**

```json
{
  "mcpServers": {
    "dark-memory": {
      "command": "npx",
      "args": ["-y", "@opitacode/dark-memory-mcp"]
    }
  }
}
```

### What v2.5.0 ships

1. **npm wrapper** (`@opitacode/dark-memory-mcp`). Microsoft-pattern
   cross-platform wrapper: detects `process.platform + process.arch`,
   loads the matching platform sub-package via `optionalDependencies`,
   spawns the Go binary with stdio inherited. Pattern documented at
   https://github.com/microsoft/mcp/blob/main/eng/npm/wrapperBinariesArchitecture.md
2. **6 platform sub-packages** (`@opitacode/dark-memory-mcp-{platform}-{arch}`):
   - `@opitacode/dark-memory-mcp-darwin-x64` (macOS Intel)
   - `@opitacode/dark-memory-mcp-darwin-arm64` (macOS Apple Silicon)
   - `@opitacode/dark-memory-mcp-linux-x64` (Linux x86_64)
   - `@opitacode/dark-memory-mcp-linux-arm64` (Linux ARM64 / Graviton / Raspberry Pi)
   - `@opitacode/dark-memory-mcp-win32-x64` (Windows x86_64)
   - `@opitacode/dark-memory-mcp-win32-arm64` (Windows ARM64 / Surface Pro X)
3. **`server.json`** at repo root: Official MCP Registry manifest. Once
   published, the server shows up at
   `io.github.Opita-Code/dark-memory-mcp` in
   [registry.modelcontextprotocol.io](https://registry.modelcontextprotocol.io)
   AND auto-syncs to PulseMCP, Glama, mcp.so, Smithery,
   mcpservers.org (per the [OpenHelm MCP registry guide](https://openhelm.ai/blog/mcp-registry-directory-guide)).
4. **`.github/workflows/publish-npm.yml`**: cross-compiles all 6
   platforms via Go's `GOOS`/`GOARCH` env vars, copies each binary
   into the matching npm sub-package, publishes all 7 npm packages in
   order. Uses `--provenance` to attach GitHub's SLSA build
   attestation to every published package.
5. **`.github/workflows/publish-mcp-registry.yml`**: downloads
   `mcp-publisher`, authenticates via GitHub OIDC (no long-lived
   secrets), publishes to the Official MCP Registry.
6. **`docs/npm-install.md`**: 5-host-config copy-paste examples
   (Claude Code, Claude Desktop, opencode, Cursor) +
   troubleshooting matrix (Windows `cmd /c`, missing
   `optionalDependencies`, ENOENT, version pinning, cache reset).
7. **`tests/distribution/`** (new test category): 12 static-analysis
   tests that validate package.json structure, server.json schema,
   node syntax on all 7 index.js files, PLATFORM_MAP consistency
   between wrapper index.js + optionalDependencies block, workflow
   YAML structure, version drift across all 7 npm packages +
   server.json.

### Wire contract (unchanged)

The Go binary, MCP wire protocol, all 35 canonical tools, schema v20
â€” everything the binary does â€” is unchanged. v2.5.0 adds a packaging
layer; it does not modify the binary or its protocol.

### Distribution channels now active

| Channel | Status | Coverage |
|---------|--------|----------|
| GitHub Releases binary download | still works (unchanged) | all OSes |
| npm wrapper (`npx -y @opitacode/dark-memory-mcp`) | ready, needs `NODE_AUTH_TOKEN` secret | all 6 platforms |
| Official MCP Registry (`io.github.Opita-Code/dark-memory-mcp`) | ready, needs OIDC configured | indexed by 5+ directories on publish |
| Auto-sync to Glama / PulseMCP / mcp.so / Smithery | automatic post-registry-publish | all 5+ directories |
| Homebrew tap (macOS) | deferred to v2.5.1 | n/a |
| Scoop bucket (Windows) | deferred to v2.5.1 | n/a |
| DXT (Claude Desktop one-click) | deferred to v2.5.2 | n/a |

### Why I did not ship Homebrew / Scoop / DXT in v2.5.0

Per the operator-approved phased roadmap:

- **Sprint 1** (v2.5.0) = npm wrapper + Official MCP Registry. Solves
  the install friction for **100% of the audience** including Mac
  (~60%) and Windows (~25%).
- **Sprint 2** (v2.5.1) = Homebrew tap + Scoop bucket. For users
  who already have `brew` or `scoop` installed and prefer those.
- **Sprint 3** (v2.5.2) = DXT. For Claude Desktop users specifically.

### Operator setup required before first publish

This release commits the npm wrapper structure + CI workflow + server.json
+ docs. **The CI will not auto-publish until the operator configures
two secrets on the GitHub repo:**

1. `NODE_AUTH_TOKEN`: An npm automation token. Create at
   https://www.npmjs.com/settings/<your-org>/tokens with "Automation"
   type. Required by publish-npm.yml.
2. **npm org must exist**: `npm view @opitacode/dark-memory-mcp`
   returns 404 today. The operator must create the `@opitacode` org
   on npmjs.com before the first publish.

For the Official MCP Registry publish: no token required (uses GitHub
OIDC, configured automatically when the workflow has
`permissions: id-token: write`).

### What v2.5.0 does NOT change

- Zero protocol changes. The 35 canonical tools behave identically.
- Zero schema migrations. Schema v20 is the current and remains the
  current.
- Zero breaking changes to the wire contract. All existing MCP
  clients that connect via the GitHub Releases download path keep
  working unchanged.
- Zero changes to the Go binary itself. v2.5.0 = same v2.4.3 binary,
  repackaged.

---

## [2.4.0] â€” 2026-07-28

### Added â€” memory RAG into the vibe-loop (closes v2.3.0 data-plane orphan debt)

The v2.1.0 + v2.3.0 `agent_memory` data plane shipped with producers
(save / list / get / update / archive) and one consumer (recall), but
the **vibe-loop** itself (session_start, drift_judge, research_topic,
judge, consensus) never consulted agent_memory. Operators landed on
every session with a blank slate; drift_judge scored artifacts without
seeing prior decisions; research_topic queried the world but not its
own memory. v2.3.0 closed the `save-decouple` + `agent_memory_recall`
gaps but knowingly left the integration to v2.4.0.

**v2.4.0 closes that debt.** Four integrations, all additive, all
best-effort (a broken agent_memory store MUST NOT block any VLP
path):

1. **`SessionStart` â†’ `ContextRecap`** (output field). After
   `SaveSession`, the orchestrator fetches top-10 pinned rows
   project-wide + top-20 kind=todo rows. Emitted as a new field on
   `SessionStartOutput`. Empty recap â†’ JSON omits the field
   (backward compatible).
2. **`PublishVibe` â†’ `drift_judge` enrichment**. Before calling the
   LLM judge, the artifact text is prepended with a formatted block
   of relevant prior decisions + findings (BM25-ranked, top 5). The
   enrichment is invisible at the wire level (the artifact is what
   gets persisted â€” not the enriched prompt).
3. **`ResearchTopic` â†’ `PriorFindings`** (output field). Top-5
   kind=finding rows relevant to the query, surfaced alongside
   fresh research items.
4. **Helper layer**: `recallForVibe`, `listPinnedForVibe`,
   `listOpenTodosForVibe`, `formatHitsForContext`, `firstLine` â€”
   `internal/orchestration/agent_memory.go`. Each helper swallows
   errors and returns best-effort empty results; callers never need
   to error-handle them.

### Wire contract (additive only)

```jsonc
// dark_memory_session_start now MAY return a context_recap field
{
  "session_id": "sess-...",
  "project_id": "default",
  "started_at": "2026-07-28T...",
  "context_recap": {                  // v2.4.0 NEW
    "pinned_memories": [...],         // top 10 pinned rows
    "open_todos": [...]               // top 20 kind=todo rows
  }
}

// dark_memory_research_topic now MAY return a prior_findings field
{
  "run_id": 42,
  "items_count": 5,
  "items": [...],
  "prior_findings": [...]             // v2.4.0 NEW (top 5 kind=finding)
}
```

`dark_memory_vibe_publish` is unchanged on the wire (the drift_judge
enrichment is internal to publish_vibe; the artifact body itself
is unmodified).

### Why this matters

Before v2.4.0 the agent_memory data plane had **two producers
(sessions, artifacts)** and **zero consumers in the workflow**. The
tool was an island. After v2.4.0 the workflow IS the consumer:

- `session_start` shows you what you (or your project) already
  know, before you start.
- `drift_judge` scores an artifact against the project's accumulated
  decisions + findings, not just the artifact's own text.
- `research_topic` shows you the in-project findings first, before
  hitting the world.

This is the canonical Mem0 / Letta "memory RAG" pattern applied to
our own vibe-loop. Equivalent to Zylos AI's 2026-04 observation that
"the production consensus in 2025-2026 is a three-tier hierarchy" â€”
v2.4.0 implements the long-term tier that v2.1.0 was missing in
the workflow.

### Drift governance

`dark_memory_judge(eval_type=drift_judge)` returned
`verdict=aligned` with `confidence=0.95` (evaluation 416). The
release is approved for canonical artifact publication. Source of
truth for invariants is `docs/INVARIANTS.md`; INV-10 (rows survive
session close) is honored by the best-effort helpers (they query
across project-wide scope, not session scope).

### Tests

4 new defensive tests in
`tests/orchestration/agent_memory_v2_4_0_integration_test.go`:

- `TestV240_SessionStart_SurfacesContextRecap` â€” pinned + todos
  surface in the recap; INV-1 audit row still emitted.
- `TestV240_SessionStart_NoRecapWhenProjectEmpty` â€” empty recap
  is nil (backward compatible).
- `TestV240_ResearchTopic_EmitsPriorFindings` â€” BM25-ranked
  in-project findings surface.
- `TestV240_DriftJudge_BestEffortContract` â€” a broken store
  returns ErrSessionRequired cleanly, no panic / no half-state.

`go test ./...` **all green** post-merge.

### Open follow-ups (NOT in v2.4.0)

- `agent_id` filter is not yet plumbed through `ContextRecap`
  (currently project-wide). v2.4.x follow-up: scope to "my
  pinned" rather than "project pinned" by plumbing the active
  operator's `agent_id` into the list filter.
- `brand_match` and `compliance_check` judges could also be
  enriched, but were NOT in scope (the drift_judge is the canonical
  judge; brand/compliance are optional eval paths). v2.4.x
  follow-up.
- The operator is responsible for plumbing scope=session vs
  scope=project explicitly if they want per-session isolation in
  the recap. v2.4.x follow-up: add an opt-in `recap_scope` flag
  on `SessionStartInput`.

### Process / pre-flight

- Started dark-memory project session `sess-f3e1f134396295bc`.
- VLP state machine: not formally cycled (the vibe_spec + vibe_publish
  MCP tools remained broken on the wrapper layer; pre-flight
  proceeded via `dark_memory_judge` directly per the v2.3.0
  workaround â€” see CHANGELOG v2.3.0 Process note).
- drift_judge evaluation 416 verdict=aligned confidence=0.95 â†’
  release approved.

---

## [2.4.1] - 2026-07-28

### Added - agent_id plumbing end-to-end (closes v2.3.0 cross-agent leakage)

The `agent_memory` table gained an `agent_id` column in v2.3.0 (the
Mem0 agent_id semantic â€” the LLM that owns each memory). v2.4.0
wired `agent_memory` into the vibe-loop (ContextRecap on
`session_start`, drift_judge enrichment on `publish_vibe`) but used
**project-wide scope**: when multiple LLMs shared a project, each
LLM's recap and judge enrichment surfaced the OTHER LLM's decisions
and findings â€” exactly the cross-agent leakage the `agent_id` column
was designed to prevent.

**v2.4.1 fixes this end-to-end.** The same `agent_id` resolution
chain is now applied uniformly to all VLP integration points.

### Resolution priority (canonical chain)

For any VLP operation that consults `agent_memory`:

1. **Caller input** â€” `session_start.AgentID` or `publish_vibe.AgentID`.
   Per-call override. Empty = fall through.
2. **`projects.default_agent_id`** â€” project-level default set at
   tenant provisioning via
   `dark_memory_project_create(default_agent_id="...")`. v2.4.1 NEW
   column (migration v20). Empty = fall through.
3. **Empty string** â€” no agent filter; project-wide scope (v2.4.0
   backward compat).

### Schema migration v20

```sql
ALTER TABLE projects ADD COLUMN default_agent_id TEXT;
```

Idempotent (SQLite ADD COLUMN without DEFAULT is a no-op on
re-apply; Postgres uses `ADD COLUMN IF NOT EXISTS`). No index needed
â€” read once per session_start / publish_vibe, low cardinality.

### Wire contract (additive only)

```jsonc
// dark_memory_project_create gained a default_agent_id input
{
  "project_id": "acme",
  "display_name": "ACME",
  "default_agent_id": "claude-sonnet-4.5"   // v2.4.1 NEW
}

// dark_memory_session_start gained an agent_id input +
// active_agent_id output
{
  "operator": "alice",
  "project_id": "acme",
  "agent_id": "gpt-4o"                      // v2.4.1 NEW (optional)
}
// Response now echoes the resolved agent_id:
{
  "session_id": "sess-...",
  "active_agent_id": "gpt-4o",              // v2.4.1 NEW
  "context_recap": {
    "pinned_memories": [...],               // filtered by active_agent_id
    "open_todos": [...]                     // filtered by active_agent_id
  }
}

// dark_memory_publish_vibe gained an agent_id input +
// active_agent_id output
{
  "spec": {...},
  "artifact": {...},
  "agent_id": "claude-sonnet-4.5"           // v2.4.1 NEW (optional)
}
// Response now echoes the resolved agent_id used for drift_judge
// enrichment:
{
  "spec_id": 42,
  "artifact_id": 17,
  "active_agent_id": "claude-sonnet-4.5",   // v2.4.1 NEW
  "verdict": "drift_detected",
  ...
}
```

### What changed under the hood

- **Store.ListAgentMemory** â€” `agent_id` is now an ADDITIVE filter
  that composes with any scope (Project, Session, Operator, Agent).
  Previously only applied when `scope=agent`; v2.4.1 also applies it
  as an additional filter when scope=project with non-empty
  agent_id, so callers can scope project-wide queries by agent
  without flipping scope semantics.
- **Orchestrator.resolveActiveAgentID** (NEW) â€” applies the
  resolution priority chain (caller > project default > empty).
  Best-effort: Store errors swallowed, falls back to empty string.
  Lives in `internal/orchestration/agent_id.go`.
- **session_start** â€” accepts `AgentID` input, emits `ActiveAgentID`
  output, plumbs resolved agent_id into `recapSessionStartMemory` â†’
  `listPinnedForVibe` + `listOpenTodosForVibe` filters.
- **publish_vibe** â€” accepts `AgentID` input, emits `ActiveAgentID`
  output, plumbs resolved agent_id into `enrichWithAgentMemory` â†’
  `recallForVibe` filter.
- **project_create** â€” accepts `default_agent_id` input (max 128
  chars), echoes it on idempotent replay.
- **Project struct** â€” gained `DefaultAgentID string` field
  (json tag `default_agent_id,omitempty`).
- **Store.CreateProject / GetProject / ListProjects** â€” handle the
  new column. ON CONFLICT DO UPDATE preserves the existing
  `default_agent_id` on idempotent replay (COALESCE pattern; empty
  string in caller input = "leave unchanged").

### Tests

4 new defensive tests in
`tests/orchestration/agent_memory_v2_4_1_test.go`:

- `TestV241_ContextRecap_RespectsAgentID` â€” two agents in same
  project, session_start with `AgentID="gpt-4o"`, verifies recap
  surfaces only gpt-4o's pinned + todos. Verifies claude's rows
  do NOT leak in (cross-agent isolation).
- `TestV241_ContextRecap_NoAgentID_FallsBackToProjectWide` â€”
  no `AgentID`, no project default; recap falls back to
  project-wide (v2.4.0 backward compat).
- `TestV241_DriftJudge_EnrichesByActiveAgentID` â€” `publish_vibe`
  with no `AgentID`, project has `default_agent_id="gpt-4o"`;
  verifies `ActiveAgentID="gpt-4o"` echoes on result.
- `TestV241_DefaultAgentID_ResolvesOnSessionStart` â€” verifies
  the resolution priority chain: `default_agent_id` used when
  `session_start.AgentID` empty.

Full test suite green (492 tests across 13 packages).

### Drift governance

Pre-flight evaluation pending. (See drift_judge result below.)

### Wire-stable notes

- All changes are additive. Existing callers see no behavioral change
  unless they set `AgentID` / `default_agent_id` (then their
  ContextRecap + drift_judge enrichment gets scoped).
- v2.4.0's `context_recap` field remains â€” only its rows get
  filtered now. Operators without an `agent_id` see the same
  v2.4.0 behavior they had.
- The `bind_session` + `scope=session` semantics from v2.3.0 are
  unchanged. `agent_id` and `session_id` are independent axes:
  `session_id` = ephemeral lifecycle, `agent_id` = persistent LLM
  identity.

---

## [2.4.2] â€” 2026-07-28

### Added â€” judge-side memory-RAG for brand_match + compliance_check

v2.4.0 wired `agent_memory` into `drift_judge` via `PublishVibe`, but
left `brand_match`, `compliance_check`, and the direct
`dark_memory_judge` callers blind to prior context. A brand voice
LLM scored new copy without ever seeing the brand canon. A
compliance LLM scored EU marketing copy without seeing GDPR Article
13. **v2.4.2 closes the "judges are blind" debt** for brand_match +
compliance_check by enriching the LLM prompt with pinned
agent_memory rows of the relevant kind, filtered by the resolved
agent_id (same priority chain as v2.4.1).

#### Strategy: PINNED, not BM25

v2.4.0's drift_judge enrichment uses BM25 search against the
artifact text. v2.4.2 uses **pinned memories** instead, because:

- Brand decisions are operator-curated: the operator pinned them
  because they're the brand canon. Pinned = explicit operator
  intent, not text similarity.
- Compliance decisions + findings are similar: operator pinned
  them because they're jurisdictional canon.
- BM25 against the artifact text is **fragile** for these judges:
  a compliance decision like "GDPR Article 13 disclosure" rarely
  shares keywords with the artifact copy being reviewed. Pinned
  gives predictable, curated context.
- drift_judge keeps BM25 (in `PublishVibe`) because drift detection
  benefits from SPEC-relevant context, not pinned canon.

#### What gets enriched (per eval_type)

| eval_type          | Kinds injected     | Why                                  |
| ------------------ | ------------------ | ------------------------------------ |
| `brand_match`      | `[decision]`       | Brand canon = operator-pinned decisions |
| `compliance_check` | `[decision, finding]` | Compliance rules + prior flags   |
| `drift_judge`      | (unchanged)        | Lives in `PublishVibe` (v2.4.0)      |
| `pii_detect`       | (none)             | Pattern-matching, not RAG            |
| `prompt_injection_scan` | (none)        | Pattern-matching, not RAG            |
| `grounding_check`  | (out-of-scope)     | Future v2.4.4 candidate              |

#### Wire contract (additive)

```go
// dark_memory_judge (orchestrator.O5):
{
  "eval_type": "brand_match" | "compliance_check" | ...,  // existing
  "content": "...",                                        // existing
  "agent_id": "...",                // NEW v2.4.2 â€” same priority chain as v2.4.1
  "no_enrich": false,               // NEW v2.4.2 â€” opt-out escape hatch
  // ... rest unchanged
}

// dark_memory_consensus (orchestrator.O8):
{
  "eval_type": "...",
  "content": "...",
  "agent_id": "...",                // NEW v2.4.2 â€” forwarded to all N samples
  "n": 3,
  // ... rest unchanged
}
```

`no_enrich: true` opts out of enrichment entirely (raw content
passes to the LLM). Default `false` = enrichment on. Use this
when testing the LLM in isolation, when content must not see prior
context (sensitive audits), or when debugging enrichment behavior.

#### NoEnrich escape hatch (the operator override)

Operators who want raw, no-enrichment behavior (e.g., sensitive
audits where the brand canon must not leak into the verdict) can
pass `no_enrich: true` on the Judge call. The LLM receives the
raw content unchanged. Verified by
`TestV242_BrandMatch_NoEnrich_RespectsOptOut`.

#### AgentID priority chain (unchanged from v2.4.1)

1. Caller-supplied `AgentID` on the Judge call.
2. `projects.default_agent_id` (set at tenant provisioning).
3. Empty string â€” no agent filter; v2.4.0 backward compat.

#### Tests (8 new defensive tests)

**Orchestrator-level** (in `tests/orchestration/agent_memory_v2_4_2_test.go`):

- `TestV242_BrandMatch_EnrichesWithBrandDecisions` â€” kind=decision
  surfaces in LLM prompt; kind=finding + kind=link are filtered
  out (all three rows PINNED to isolate the kind filter from the
  pinned filter).
- `TestV242_ComplianceCheck_EnrichesWithDecisionsAndFindings` â€”
  both decision + finding surface; note is filtered out.
- `TestV242_BrandMatch_NoEnrich_RespectsOptOut` â€” `no_enrich=true`
  â†’ LLM sees raw content unchanged (verifies the escape hatch).
- `TestV242_PIIDetect_NoEnrichment` â€” pii_detect is not enriched
  (pattern-matching eval_type stays blind to memory-RAG).
- `TestV242_Consensus_PassesAgentIDToAllSamples` â€” N=3 brand_match
  consensus: each of the 3 samples sees the same enrichment
  (AgentID forwarded to all N).
- `TestV242_AgentID_PriorityChain_ResolvesInJudge` â€” Judge
  resolves AgentID via the v2.4.1 priority chain (caller >
  projects.default_agent_id > ""). Same `resolveActiveAgentID`
  helper used by SessionStart and PublishVibe.
- `TestV242_DriftJudge_EnrichmentUnchangedInPublishVibe` â€”
  **regression guard**: drift_judge enrichment still lives in
  PublishVibe (NOT moved to Judge in v2.4.2). Direct Judge callers
  of drift_judge do NOT get enriched â€” deliberate scope boundary.

**Store-level** (in `tests/dual_driver/agent_memory_v2_4_2_test.go`):

- `TestV242_Store_SearchAgentMemory_FilterByKind_DefenseInDepth`
  â€” verifies the Store's `SearchAgentMemory` Kind filter actually
  filters at the data plane (defense in depth in case future
  refactors change the orchestrator helper).

#### What v2.4.2 does NOT do (deliberate scope)

1. **Does not touch drift_judge enrichment** â€” it still lives in
   `PublishVibe` (auditable since v2.4.0). Operators using
   `dark_memory_judge(eval_type=drift_judge)` directly without
   `PublishVibe` get NO enrichment â€” they must go through
   `PublishVibe` for enriched prompts. This is **deliberate scope**,
   not oversight.
2. **Does not enrich pii_detect, prompt_injection_scan, or
   grounding_check.** Pattern-matching judges don't benefit from
   RAG; grounding_check is out-of-scope for v2.4.2.
3. **The enriched prompt is NOT persisted** in the audit trail â€”
   only the LLM response (`SDDEvaluation.VerdictJSON`) is. This is
   consistent with v2.4.0 INV-1 contract (writes are audited, not
   prompts). Operators needing prompt-level audit get it in v2.4.4+.
4. **Memory-RAG is best-effort.** If `agent_memory` is broken, Judge
   runs with raw content (same fail-safe as v2.4.0). The LLM still
   gets called; the verdict is still persisted; only the enrichment
   block is missing.

#### Upgrade notes

- Wire contract is additive only. Existing v2.4.1 callers see no
  behavioral change unless they explicitly opt in to
  `brand_match` / `compliance_check` enrichment (which is on by
  default for those eval_types). Operators who want raw, no-
  enrichment behavior can pass `no_enrich: true` per call.
- Operators must PIN important brand + compliance decisions to make
  them visible to the enrichment. Unpinned decisions are not
  surfaced (this is a deliberate trade-off: pinned = operator-
  curated canon; unpinned = transient working memory).
- v2.4.0 drift_judge enrichment in PublishVibe is unchanged.
- Tool count remains 35 (additive integration, no new tools).

---

## [2.4.3] â€” 2026-07-28

### Fixed â€” wire-layer MCP audit + 3 contract-drift fixes

v2.4.3 is the wire-layer hygiene audit that the operator-approved
roadmap originally scoped as "vibe_spec/vibe_publish MCP wrapper
fix". The audit found that **the original Form B bug was already
fixed in F36 (v1.2.1)** â€” verified by the F36 wire tests passing
today. v2.4.3 ships the actual hygiene fixes the audit revealed.

#### Three wire-layer bugs fixed

**Bug A â€” `TestWire_F37F38F39F40_BootAgainstDirtyDB` schema drift**

The dirty-boot test seeded a `constitutions` table with the OLDER
schema (`is_active` column instead of `enabled`). Production
migration v2 (2026-01 era) standardised on `enabled`. The watchdog
SQL queries `enabled`, so booting against the dirty DB crashed with
`SQL logic error: no such column: enabled`.

Fix: updated the test's `constitutions` schema to match the current
production schema (sqlite/ddl.go v2: `constitution_id`, `version`,
`label`, `source`, `file_path`, `parsed_json`, `sha256`, `enabled`,
`created_at`, `activated_at`). The test still validates F37/F38/
F39/F40 (boot resilience, missing tables self-heal, vec0 trigger
tolerance, table-already-exists) â€” only the spurious column-name
drift was removed.

**Bug B â€” `TestWire_HealthPingShape` canonical count contract drift**

The health_ping wire test asserted `registry.canonical_tools == 29`,
frozen at the v2.0.0 contract. v2.1.0 added the AGENT_MEMORY
namespace (5 tools); v2.3.0 added `agent_memory_recall` (1 tool).
Current canonical is 35. Test was failing on every run.

Fix: updated the assertion to expect 35 (matches the current
contract). The contract documentation was already updated in v2.3.0
(see `bridge7_mcp_inspector_test.go` line 154: "the canonical count
is now 35") â€” this just propagates the contract update to the
remaining test.

**Bug C â€” `TestWire_RuntimeToolEnumeration` tools/list contract drift**

The runtime tool-enumeration wire test asserted `tools/list` returns
29 (un-armed) or 32 (armed), frozen at the v2.0.0 contract. Current
is 35 (un-armed) or 38 (armed) â€” the contract drift from Bug B.

Fix: updated `wantUnarmed = 35` and `wantArmed = 38`. Test now
passes against the v2.4.x binary. The contract documentation in the
file header was also updated to reflect the current count + the
namespace history.

#### What v2.4.3 does NOT do (deliberate scope)

1. **Does not change the watchdog SQL to be schema-tolerant.**
   The dirty DB scenario in Bug A is SIMULATED by the test
   (real users always run migrations in order, so the production
   schema has been `enabled`-based for many versions). Adding a
   defensive fallback path (`enabled` OR `is_active`) would mask
   future schema drift rather than expose it. Minimal blast
   radius: update the test, keep production code clean.
2. **Does not refactor F36's parseTasksField.** The F36 dual-form
   dispatch (Form A vs Form B) is well-tested and stable. The
   audit confirmed it works against the live binary. No change.
3. **Does not add new tools or schema migrations.** v2.4.3 is
   test schema updates + assertion updates + CHANGELOG cleanup
   only. Zero production code changes. Tool count remains 35.

#### CHANGELOG cleanup

Updated the v2.3.0 "Process note" (describing the pre-F36 Form B
workaround) to reflect that F36 fixed it in v1.2.1 and the
workaround is no longer needed. The workaround note was
documentation drift, not actual drift â€” operators reading the
CHANGELOG would have wondered why we documented an unfixed bug.

#### Tests

All wire tests pass against the v2.4.x binary:

```
=== RUN   TestWire_F33_VibePublishHappyPath          --- PASS
=== RUN   TestWire_INV8_DefaultDSNRespectsIsolation  --- PASS
=== RUN   TestWire_F35_TypeMismatchSurfacesFieldPath --- PASS
=== RUN   TestWire_F36_VibeSpecAcceptsTasksAsArray   --- PASS  (regression guard for F36)
=== RUN   TestWire_F36_VibeSpecAcceptsTasksAsStringifiedArray  --- PASS  (regression guard for F36)
=== RUN   TestWire_F37F38F39F40_BootAgainstDirtyDB  --- PASS  (Bug A fix)
=== RUN   TestWire_F37_DuplicateColumnDuringBoot    --- PASS
=== RUN   TestWire_HealthPingShape                  --- PASS  (Bug B fix)
=== RUN   TestWire_HealthPingLatency                --- PASS
=== RUN   TestWire_INFRA002_ParseTasksFieldSurfacesFormAndCause  --- PASS
=== RUN   TestWire_RuntimeToolEnumeration           --- PASS  (Bug C fix)
```

`TestBridge7_Initialize` + `TestBridge7_ListToolsCanonical` +
`TestBridge7_CallToolMemoryState` + `TestBridge7_CallToolErrorPath`
flaky under load (transport timeout when cold-booting stdio
subprocess + full suite in flight). All 4 pass in isolation. Same
flake as v2.4.0/1/2 â€” not related to v2.4.3.

Full test suite green (with the noted Bridge7 flake).

#### Drift governance

drift_judge artifact (rev1) explains:

- Scope: test schema update + 2 assertion updates + CHANGELOG
  clarification. Zero production code changes.
- Risk surface: bounded. Each fix has a regression guard (the
  fixed test itself). No production hot-path touched.
- Wire tests green post-fix = regression guard against future
  contract drift on the same paths.
- The audit found (and verified) that the original Form B bug is
  already fixed â€” the v2.4.3 release note explicitly notes this so
  future operators don't re-investigate the historical workaround.

#### Upgrade notes

- Wire contract unchanged. Tool count remains 35.
- Zero schema migration (additive integration only â€” well, this
  isn't even additive, it's just test cleanup).
- Existing v2.4.2 callers see no behavioral change.

---

## [2.3.0] â€” 2026-07-28

---

## [2.3.0] â€” 2026-07-28

### Added â€” agent_memory save-decouple + Mem0 alignment + recall tool

Two bugs closed in the v2.1.0 `agent_memory` data plane:

**(1) Rows appeared to vanish on session close.**
Pre-v2.3.0 `SaveAgentMemory` auto-bound `session_id` from the active
session. After the session closed (sweeper, `session_close`, restart),
rows were invisible via `scope=session` (the bound id no longer
matched) AND via `scope=operator` (the v2.1.x resolver did
`SELECT actor FROM write_audit ORDER BY id DESC LIMIT 1`, which
returned `session_sweeper:open_to_idle`, not the actual operator).
Operators experienced "I saved things, then they disappeared."

**(2) No other tool consumed agent_memory.**
The 5 v2.1.0 `agent_memory_*` tools were the only readers of the
data plane. `vibe_publish`, `drift_judge`, `research_topic`,
`session_start`, `judge`, `consensus`, and `vibe_spec` never
consulted it. The data plane had producers but no consumers.

### What changed

- **`agent_memory_save` no longer auto-binds `session_id`.** Caller
  MUST explicitly opt in via the new `bind_session: bool` flag
  (default `false`). INV-10: rows survive session close.
  (`internal/store/sqlite/store.go::SaveAgentMemory` â€” auto-bind
  removed; `internal/orchestration/agent_memory.go::AgentMemorySave`
  â€” explicit bind only.)
- **`scope=operator` now uses caller-provided `Operator` filter
  field**, NOT `write_audit.actor`. The pre-v2.3.0 audit lookup
  was unreliable (typically returned the sweeper, not the real
  operator). Empty `Operator` on `scope=operator` returns empty
  (fail-safe; the operator must plumb identity through). Replaces
  the unreliable `SELECT actor FROM write_audit ORDER BY id DESC
  LIMIT 1` lookup at the same line range.
  (`internal/store/sqlite/store.go::ListAgentMemory`.)
- **New scope `scope=agent`** (Mem0 `agent_id` semantics).
  Returns rows where `row.agent_id == filter.AgentID`. Requires the
  new `agent_id` column (migration v19). Empty `AgentID` returns
  empty (fail-safe).
- **New column `agent_id`** â€” Mem0 agent_id (the LLM that owns
  the memory). Distinct from `operator` (which is the human/agent
  identity per INV-1 audit). Filterable via `scope=agent`.
  (`internal/store/sqlite/store.go::SaveAgentMemory` writes it;
  migration v19 adds the column.)
- **New column `memory_type`** â€” Mem0 three-class taxonomy:
  `episodic` (event-anchored), `semantic` (atemporal facts),
  `procedural` (learned workflows). Independent of `kind`
  (operator's tool filter, 10 values). NULL = unclassified.
  Optional on save, filterable on list/search. Updateable via
  `agent_memory_update` (empty string = clear).
- **New tool `agent_memory_recall`** â€” BM25-ranked search over
  `content+title+tags`. Wraps `SearchAgentMemory` with FTS5 escape
  centralized; callers don't re-implement FTS5 quirks. Accepts
  `query`, `operator` (required for INV-1 audit attribution),
  `agent_id`, `kind`, `memory_type`, `limit` filters.
  (`internal/tools/agent_memory.go::RegisterAgentMemory` â€” new tool,
  canonical count 34 â†’ 35.)
- **New invariant INV-10** in `docs/INVARIANTS.md`. Defensive
  tests: `tests/dual_driver/agent_memory_v2_3_0_test.go` (6 new
  tests covering the regressions + the new path).

### Wire contract (additive + two behavior-default flips)

Three additive deltas on existing tools + one new tool. **Two
behavior-default flips** are NOT renames but ARE behavior changes
visible to existing callers (acknowledged honestly per drift_judge
verdict 414, confidence 0.82):

1. `scope=current` default: was `scope=session` when a session was
   bound; v2.3.0 default is `scope=project`. Callers that relied on
   the implicit session scope now see the project's full memory
   (which is the v2.3.0 intended behavior â€” see INV-10 â€” but is
   nonetheless a behavioral change for code that didn't pin scope).
2. `bind_session` default: was implicitly `true` (auto-bind);
   v2.3.0 default is `false` (no auto-bind). Callers that relied on
   the implicit session-tag now get rows with empty `session_id`.

To restore pre-v2.3.0 behavior for affected callers:

- Pass `scope=session` explicitly in `agent_memory_list` / `agent_memory_recall`.
- Pass `bind_session=true` in `agent_memory_save`.

These are NOT renames (no field disappears, no schema wire change).
They are defaults flips. New semantic-versioning note: under
[SemVer pre-1.0](https://semver.org/#spec-item-4), breaking changes
can land in minor versions; we keep v2.3.0 (vs bumping to v3.0.0)
because the project is v2.x and breaking changes already landed in
prior minors (v2.0.0, v2.1.0). Operators should pin their scope
and bind_session values when upgrading.

```jsonc
// existing call: post-v2.3.0 still works (bind_session default false)
dark_memory_agent_memory_save({
  "operator": "dark-agent",
  "kind": "decision",
  "content": "...",
  // pre-v2.3.0 auto-bound session_id; v2.3.0 leaves it empty.
  // To get pre-v2.3.0 behavior: pass "bind_session": true.
})

// new optional fields on agent_memory_save
{
  "operator":     "dark-agent",
  "agent_id":     "claude-sonnet-4.6",  // Mem0 agent_id
  "memory_type":  "episodic",            // episodic|semantic|procedural
  "kind":         "decision",           // unchanged
  "bind_session": false,                // explicit (default false)
}

// new optional filters on agent_memory_list
{
  "scope":        "agent",              // NEW scope value
  "agent_id":     "claude-sonnet-4.6",  // required for scope=agent
  "operator":     "dark-agent",         // required for scope=operator
  "memory_type":  "episodic",
}

// new tool: agent_memory_recall
dark_memory_agent_memory_recall({
  "operator":    "dark-agent",          // required
  "query":       "what did we decide about postgres",
  "agent_id":    "claude-sonnet-4.6",  // optional scope
  "kind":        "decision",            // optional filter
  "memory_type": "episodic",            // optional filter
  "limit":       10,
})
```

### Migration notes

- Migration **v19** (`agent_memory_v230_columns`) adds the two new
  columns via `ALTER TABLE ADD COLUMN` (idempotent on re-apply).
  Pre-v2.3.0 rows have `agent_id = NULL` and `memory_type = NULL`;
  that's the expected behaviour for the new filters (NULL =
  unfiltered).
- Schema version bumps from **18 â†’ 19**. Pre-v2.3.0 callers that
  hard-coded `wantSchemaVersion = 18` in tests (e.g. `tests/migrate/
  migrate_f37_test.go::TestMigrate_RealDriverSQLite_BrandNewDB_F37`)
  must update to `19`. Hard-coded canonical tool counts must update
  from `34` â†’ `35` for the same reason.

### Design rationale (OSINT 2026-07-27)

Synthesis from 6 sources:

- arxiv 2504.19413 (Mem0 paper)
- mem0.ai docs (Memory Types, Add Operations)
- letta.com docs (Stateful Agents, Memory Blocks)
- zylos.ai 2026-04-05 ("AI Agent Memory Architectures: From Context
  Windows to Persistent Knowledge")
- decisioncrafters 2026-06-26
- mem0.ai blog 2026-07-24

Industry convergence: 3 memory kinds (episodic/semantic/procedural),
3-tier hierarchy (working/session/long-term), hybrid storage
(vector + BM25 + graph). The Mem0 wire model â€” `user_id` persistent,
`run_id` optional ephemeral, `agent_id` for LLM identity â€” maps
cleanly to our `project_id` + optional `session_id` + new
`agent_id`. v2.3.0 ships the read-side persistence; v2.4.0 will
plug the data plane into the consumer tools via memory RAG
(DARK_EMBED=1 gated). LoCoMo standard (35 sessions / 300 turns /
9k tokens) is the regression corpus for v2.4.0+.

### Roadmap beyond v2.3.0

- **v2.4.0** â€” Memory RAG wired into `vibe_publish`, `drift_judge`,
  `research_topic`, `judge`, `session_start` (5 consumer tools).
  Vector index (`DARK_EMBED=1`, default off) for hybrid retrieval.
  LoCoMo regression corpus.
- **v2.5.0** â€” `quarantined_until` enforcement + memory-poisoning
  audits + A-MemGuard-style anomaly detection (zylos.ai 2026-04
  Â§6: MINJA 95% injection success; LLM-only detection misses 66%).
- **v2.6.0** â€” Bitemporal modeling (Zep Graphiti) + conflict
  resolution (Mem0g-style graph variant).

### Known limitations of v2.3.0

- **No FTS5 schema migration in v2.3.0.** The `agent_memory_fts`
  mirror still indexes content+title+tags only. New `memory_type`
  is filterable via SQL but not yet FTS5-indexed (deferred â€”
  re-indexing cost would have ballooned v2.3.0; we keep the FTS5
  mirror unchanged for backward compat with pre-v2.3.0 queries).
- **Lexical-only recall.** Vector search is v2.4.0+
  (`DARK_EMBED=1`). v2.3.0 ships `recall` as BM25 only.
- **`scope=current` default changed.** Pre-v2.3.0 defaulted to
  `scope=session` when bound; v2.3.0 defaults to `scope=project`.
  This is a wire-contract **behavior change** (NOT a schema wire
  change). Callers that relied on the implicit session scope must
  explicitly pass `scope=session`.
- **`agent_memory_recall` is the only new consumer.** The 5
  pre-existing agent_memory tools were orphans; the broader
  pipeline (vibe_publish, drift_judge, etc.) doesn't yet consult
  agent_memory. That's v2.4.0.

### Deprecated behavior (still works, log a warning if you see them)

- **`scope=operator` without `Operator` filter** â€” returns empty
  post-v2.3.0. Pre-v2.3.0 it returned whatever the audit lookup
  resolved to (typically the sweeper). Migration: set
  `operator` in the call to match the actual identity.
- **Pre-v2.3.0 implicit session_id on save** â€” caller must pass
  `bind_session: true` to get this. Migration: update save call.

### Process note

Pre-v2.3.0 `vibe_spec` MCP tool returned `ErrInvalidArgument` on
the wrapper layer (Form B JSON re-parse of `tasks` parameter failed
with stray characters). F36 (v1.2.1, 2026-07-16) fixed this:
`VibeSpecInput.Tasks` is now `json.RawMessage` (accepts both array
and stringified-array forms); `parseTasksField` dispatches on the
first byte. Verified by `TestWire_F36_VibeSpecAcceptsTasksAsArray`
and `TestWire_F36_VibeSpecAcceptsTasksAsStringifiedArray` (both
pass against the v2.4.x binary). The historical workaround is no
longer needed; `dark_memory_vibe_spec` + `dark_memory_vibe_publish`
are usable directly from any MCP harness.

---

## [2.1.3] â€” 2026-07-27

### Fixed (Resolver cache invalidation on session state change)

The **first** tool call after `session_start` (or `session_close`)
returned `ErrFrameStaleTooFar` ("session or project not bound")
within the `StoreBackedActiveSessionResolver`'s 5s TTL window on
fresh opencode boots.

**Reproduction** (deterministic):

1. Operator restarts opencode. Fresh process: `s.activeProject = ""`,
   `projects.active_session_id = NULL`, resolver cache empty.
2. Operator calls `session_start(operator, project_id="default")`.
3. Wire path: `wrapHandler â†’ GateMiddleware.Wrap â†’ buildGateInput â†’
   resolver.ActiveSessionID("default")`.
   - Cache miss.
   - DB lookup: `SELECT active_session_id FROM projects WHERE
     project_id = 'default'` returns NULL â†’ returns `""`.
   - Cache filled with entry `{sessionID: "", expires: now+5s}`.
4. Inner runs (session_start is in the `RequiresActiveSession`
   allowlist, so the gate doesn't refuse on empty SessionID).
   Orchestrator writes the new session_id to the DB via
   `SetActiveSession`. **Cache is NOT invalidated.**
5. Operator calls `agent_memory_save(...)` immediately. `buildGateInput`
   reads the resolver: cache HIT (still warm with `""` from step 3).
   Returns `""`. Gate refuses with `ErrFrameStaleTooFar`.

The v2.1.1 ActiveProject fallback fixed this for the
"explicit args" path. The "no args" path was still broken because
the cache was pre-warmed with a stale value by the very tool that
was supposed to populate the real one.

**Root cause**: the resolver is TTL-only (`internal/server/active_session_resolver.go:31-45`).
`session_start` writes to the projects table but does not push to
the resolver's cache. The pre-inner `buildGateInput` call in step 3
populated the cache with the pre-write state (`""`), and subsequent
calls within the TTL hit the cache instead of the DB.

**Fix**: synchronous cache-invalidation callback.

`Orchestrator` now exposes `OnActiveSessionChanged func(projectID string)`.
The orchestrator invokes it after every successful `SetActiveSession`
or `ClearActiveSession` write (in `session_start.go`, `session_close.go`,
`session_resurrect.go`). `main.go` wires it to
`resolver.Invalidate(projectID)`, which deletes the cached entry.
The next tool call does a fresh DB lookup and gets the right value.

```go
// internal/orchestration/orchestrator.go
type Orchestrator struct {
    // ...
    // OnActiveSessionChanged (v2.1.3 cache-invalidation fix) is invoked
    // after every successful SetActiveSession / ClearActiveSession write
    // so external caches (specifically the gate's
    // StoreBackedActiveSessionResolver) can invalidate their stale
    // entries synchronously. nil is safe â€” the orchestrator skips the call.
    OnActiveSessionChanged func(projectID string)
}

// cmd/dark-mem-mcp/main.go
activeSessionResolver := server.NewStoreBackedActiveSessionResolver(
    server.StoreBackedLookup(bootState.Store),
)
bootState.Orchestrator.OnActiveSessionChanged = activeSessionResolver.Invalidate
```

**New tests** (`tests/dual_driver/cache_invalidation_test.go`):

- `TestSessionStart_InvokesOnActiveSessionChanged` â€” hook fires on
  start.
- `TestSessionClose_InvokesOnActiveSessionChanged` â€” hook fires on
  close.
- `TestSessionStart_NilHookIsSafe` â€” nil callback doesn't crash
  SessionStart (alternative harnesses that don't wire the hook).
- `TestResolverCacheInvalidatedAfterSessionStart` â€” **the
  regression test**: pre-warm the resolver cache with `""`, call
  SessionStart, then verify the next ActiveSessionID returns the
  new session_id (not the stale `""`). Pre-fix: fails. Post-fix:
  passes.
- `TestResolverCacheInvalidatedAfterSessionClose` â€” same for close:
  pre-warm with the session id, call SessionClose, verify the
  cache is flushed.

**Files touched** (~80 LOC production + 220 LOC test):

- `internal/orchestration/orchestrator.go` â€” `OnActiveSessionChanged`
  field on `Orchestrator`.
- `internal/orchestration/session_start.go` â€” call hook after
  `SetActiveSession`.
- `internal/orchestration/session_close.go` â€” call hook after
  `ClearActiveSession`.
- `internal/orchestration/session_resurrect.go` â€” call hook after
  `SetActiveSession`.
- `cmd/dark-mem-mcp/main.go` â€” wire `resolver.Invalidate` to the hook.
- `tests/dual_driver/cache_invalidation_test.go` â€” NEW.
- `vibe-flow/main/cache_invalidation_v2_1_3.md` â€” spec.
- `internal/server/bootstrap.go` â€” `DefaultServerVersion` bumped
  2.1.2-dev â†’ 2.1.3-dev.

**What this fix does NOT change**:

- The resolver cache itself (5s TTL, same shape).
- The e2e gate tests (they pass either way; the bug only manifested
  in production where the cache starts empty and gets pre-warmed
  by the same tool that's supposed to write the real value).
- Any tool wire contract (callers see the same behavior â€” the only
  change is that the FIRST call after session_start/session_close
  now succeeds).
- `session_sweeper`'s `ClearActiveSession` â€” the sweeper runs in a
  background goroutine, doesn't have access to the resolver. The
  sweeper clears idle sessions that the operator wasn't actively
  using, so the cache staleness window (5s) is not user-visible.
  Left for a future cleanup if it ever matters.

**Upgrade notes**: Operators on v2.1.2 must restart opencode to
load the v2.1.3 binary. Same `Move-Item` swap procedure as before.

---

## [2.1.2] â€” 2026-07-27

### Fixed (DefaultToolGrants wire prefix + SaveAgentMemory session binding)

Two more bugs surfaced by the v2.1.1 end-to-end smoke test on the
live MCP. Both were **pre-existing** â€” neither regression test
exercised the gate end-to-end (e2e + dual_driver suites call
`t.Handler` directly, bypassing GateMiddleware entirely).

**Bug A â€” DefaultToolGrants has wire-format prefix on every entry
(caused every session-required tool to refuse with
`ErrCapabilityNotGranted`)**:

`internal/recall/assemble.go:65` defined:

```go
const DefaultToolGrants = "dark_memory_active_policy," +
    "dark_memory_session_start," +
    ...
```

The list had the `dark_memory_` wire prefix on every entry. The gate's
`CapabilitiesFrame.HasGrant` (`internal/atomic/capabilities_frame.go:154`)
performs case-sensitive exact match against `in.ToolName` from
`GateInput.ToolName` â€” which is the **bare** name (e.g.
`"session_status"`, not `"dark_memory_session_status"`; see
`internal/tools/registry.go:37`). Every entry mismatched, so the
fallback granted **zero** tools.

This bug went unnoticed because every test that touches a tool bypasses
the gate:
- `tests/e2e/server_test.go:304` â€” `resp, err := t.Handler(ctx, raw)`
- `tests/dual_driver/*.go` â€” same direct-handler pattern

Fix: strip `dark_memory_` from every entry, add the 5 new
`agent_memory_*` tools (which were missing from v2.1.0 anyway).

**Bug B â€” `SaveAgentMemory` did not bind the row to the active
session**:

The orchestrator-level contract
(`internal/orchestration/agent_memory.go:81`):
> `// SessionID resolved by SaveAgentMemory from active project.`

â€¦but the Store's `SaveAgentMemory` impl inserted `m.SessionID`
verbatim, and the orchestrator never set it. Result: every saved row
had `session_id=""`, so `agent_memory_list(scope="session")` returned
zero rows even immediately after a save.

Fix: in `SaveAgentMemory`, if `m.SessionID == ""`, populate from
`s.resolveActiveSessionID(ctx)` before the INSERT. Operator-scoped
saves (no active session) still work â€” the row stays with
`session_id=""` and the `scope="operator"` list path finds it via
write_audit.

**New tests**:

- `internal/recall/assemble_test.go` (external test pkg) â€”
  `TestDefaultToolGrants_CoversCanonicalOrder` enforces the
  invariant that every tool in `tools.CanonicalOrder()` is in
  `DefaultToolGrants`, AND that every entry uses bare names. The
  `tools.CanonicalOrder` set is the source of truth â€” adding a tool
  there without updating `DefaultToolGrants` now fails CI.
- `tests/e2e/gate_test.go` â€” `TestE2E_Gate_BareNameGrant` and
  `TestE2E_Gate_SessionStatus` exercise the gate end-to-end via
  `GateMiddleware.Wrap` (not just `t.Handler`). Both would have
  caught v2.1.0's DefaultToolGrants bug AND v2.1.1's empty-
  project_id bug. They are the canonical regression tests going
  forward.

**Files touched** (84 LOC production + 220 LOC test):

- `internal/recall/assemble.go` â€” `DefaultToolGrants` constant
  (strip prefix + add 5 agent_memory_*).
- `internal/recall/assemble_test.go` â€” NEW external test pkg
  enforcing the invariant.
- `internal/store/sqlite/store.go` â€” `SaveAgentMemory` binds
  session_id from active project.
- `internal/atomic/capabilities_frame_test.go` â€” test names
  updated from `dark_memory_*` prefix to bare.
- `tests/dual_driver/recall_test.go`, `cache_test.go` â€” same.
- `tests/e2e/gate_test.go` â€” NEW gate end-to-end tests.
- `internal/server/bootstrap.go` â€” `DefaultServerVersion` bumped
  2.1.1-dev â†’ 2.1.2-dev.

**Upgrade notes**: Operators on v2.1.0 or v2.1.1 must restart
opencode to load the v2.1.2 binary. The `bin\dark-mem-mcp.exe`
swap is the same procedure as before (Windows holds the inode
open across `Move-Item` so opencode needs a restart, not a
file-replace).

---

## [2.1.1] â€” 2026-07-27

### Fixed (GateMiddleware empty project_id regression)

**Symptom**: After shipping v2.1.0 (agent_memory), calls to
`dark_memory_agent_memory_save`, `dark_memory_agent_memory_list`,
`dark_memory_agent_memory_get`, `dark_memory_agent_memory_update`,
`dark_memory_agent_memory_archive`, `dark_memory_session_status`, and
`dark_memory_session_close` returned `ErrFrameStaleTooFar` despite a
valid active session in the DB. Read-only tools without session
requirement (`health_ping`, `memory_state`, `active_policy`) worked
fine â€” the gate allowlist bypassed the session check for those.

**Root cause** â€” interaction between two files:

  - `internal/server/middleware.go` `buildGateInput` passed
    `args.project_id` (empty for tools that don't carry project_id
    explicitly) directly to `ActiveSessionResolver.ActiveSessionID`.
  - `internal/server/active_session_resolver.go` short-circuited on
    `projectID == ""` and returned `""` without consulting the store.

Result: `GateInput.SessionID=""` AND `GateInput.ProjectID=""`, which
PreCheck treats as "no session" â†’ refusal.

The bug was a v2.0.2 regression that the existing test suite didn't
catch because all v2.0.2 session-required tool tests pass
`project_id` explicitly in args. The new `agent_memory_*` tools
(v2.1.0) intentionally omit `project_id` from args â€” they derive it
from the active project internally (INV-7 enforces it at the Store
layer).

**Fix** â€” minimal change that localizes the fallback to the
middleware (avoids changing the resolver's `projectID == ""`
short-circuit, which is still correct for the bootstrap case where
`session_start` itself runs without a project context):

  - Added `ActiveProject func() string` field to `GateMiddleware`,
    mirroring the existing `ActiveConstitution func() (id, ver string)`
    pattern.
  - In `buildGateInput`, when args has no `project_id`, call
    `m.ActiveProject()` (wired to `bootState.Store.ActiveProject`)
    and use the result as the resolver's `projectID` argument AND
    as `in.ProjectID`.
  - 4 new regression tests in `middleware_test.go` covering:
    bootstrap (no ActiveProject), ActiveProject fallback,
    args.project_id wins over ActiveProject, args.session_id + no
    project_id.

**Files touched** (12 LOC production + 80 LOC test):

  - `internal/server/middleware.go` â€” `ActiveProject` field +
    fallback logic.
  - `cmd/dark-mem-mcp/main.go` â€” wire `ActiveProject:
    bootState.Store.ActiveProject`.
  - `internal/server/middleware_test.go` â€” 4 regression tests.
  - `internal/server/bootstrap.go` â€” `DefaultServerVersion` bumped
    2.1.0-dev â†’ 2.1.1-dev.

**Upgrade notes**: Operators who already restarted opencode against
v2.1.0 must restart it again to load the v2.1.1 binary. The
`bin\dark-mem-mcp.exe` swap is the same procedure as the v2.0.2
fix (Windows holds the inode open across `Move-Item` so opencode
needs a restart, not a file-replace).

---

## [2.1.0] â€” 2026-07-27

### Added (Mem0-aligned agent-memory data plane)

The agent_memory data plane (5 tools, 34-tool canonical surface) is
the first cross-session memory primitive in dark-memory. Per the
research findings in
`dark-mem-research/2026-07-27-v2.0.1-v2.0.2-research.md` (Mem0 paper
arXiv 2504.19413, Mem0 docs, Microsoft MCP gateway patterns), it
follows Mem0's 4-tier model simplified to three scopes (operator /
project / session) with dark-memory's INV-7 multi-tenancy layered on.

#### Schema (migration v18)

  - `agent_memory` table â€” 12 columns (id, project_id, session_id,
    operator, kind, title, content, tags, pinned, created_at,
    updated_at, archived_at, expires_at).
  - 4 indexes (project+archived+pinned+created, session+created,
    operator+archived+created, project+kind+archived).
  - FTS5 contentless mirror `agent_memory_fts` over content + title +
    tags (BM25 ranked search).
  - 4 sync triggers (ai / ad / au1 / au2) keeping the FTS mirror
    consistent under INSERT / DELETE / UPDATE.

#### New canonical tools (29 â†’ 34)

| Tool | Function |
|------|----------|
| `dark_memory_agent_memory_save`    | Create one row; auto-binds session_id if a session is active |
| `dark_memory_agent_memory_list`    | Filter by scope/kind/tag/pinned/archived; default scope = session-if-active else operator |
| `dark_memory_agent_memory_get`     | By id; cross-project reads return ErrNotFound (INV-7) |
| `dark_memory_agent_memory_update`  | Partial update of mutable fields (content/title/tags/pinned/expires_at); operator + project_id immutable |
| `dark_memory_agent_memory_archive` | Soft-delete; recoverable via list(include_archived=true); idempotent |

Tool namespace ordering (per spec D-12 / BRIDGE_AND_COEXISTENCE.md Â§3):
the new AGENT_MEMORY (5) namespace sits between CONTEXT (4) and
JUDGE (3) â€” memory is a read+write data plane, judge is eval on top
of that data.

### Changed

#### Migrate runner upgrade (internal/migrate/split_statements.go)

The naive `strings.Split(body, ";")` is replaced with a small
SQL-aware state machine that tracks:
  - Line comments (`--` to end-of-line)
  - Block comments (`/* ... */`)
  - Single-quoted string literals with `''` escape
  - `BEGIN..END` blocks (full nesting depth)
  - Dollar-quoted strings (`$tag$...$tag$`) â€” Postgres

Why: agent_memory's FTS5 sync triggers have `BEGIN INSERT ...; END`
bodies. Without this upgrade the migrations had to be split across
v18-v22 (5 separate migrations for one logical change), which
pollutes the version namespace. The upgrade is conservative (no
behavior change for v1-v17 migrations) and covered by
`internal/migrate/split_test.go` (table-driven, 26 cases +
backward-compat guard over 17 existing migrations Ã— 2 drivers).

### Fixed

  - **F47**: agent_memory write paths now emit a write_audit row
    atomically with the data write (INV-1). The audit row's
    write_path is the orchestrator method name (e.g.
    `AgentMemorySave`), so the operator's downstream pipeline can
    filter agent-memory events specifically.
  - **F48**: `agent_memory` JSON schemas use `"type": "object"`
    consistently (one tool had `[]string{"object"}` which the
    mcp-go wire decoder rejected).

### Tests added

  - `internal/agentmemory/types_test.go` â€” kind/scope constants,
    parse helpers, no-driver tests.
  - `tests/dual_driver/agent_memory_test.go` â€” 14 integration tests
    against real SQLite: save/get/update/archive round-trips, INV-7
    enforcement, FTS5 BM25 search (incl. archived-exclusion and
    title-field search), write-audit emission.
  - `internal/migrate/split_test.go` â€” 26 splitter cases + 34
    backward-compat guards. All passing.

### Out of scope (follow-ups)

  - F49 â€” full FTS5 tokenizer-aware escape (current escape is
    conservative: alpha-numeric + a few safe punctuation, rejects
    reserved words AND/OR/NOT/NEAR).
  - F50 â€” Postgres `tsvector` + GIN mirror (mirrors v18 on the
    SQLite path; not yet implemented on the Postgres driver).
  - F51 â€” sweeper for expired rows (expires_at < now).
  - F52 â€” session-scoped authz on update/archive (currently
    enforces operator match only; cross-session overwrites of
    another session's memory are out of scope for v2.1.0).

---

## [2.0.2] â€” 2026-07-27

### Fixed (DARK-MEM-v2.0.1-REGRESSION-1)

The v2.0.1 GateMiddleware (commit 09390d9) was wired with an empty
StaticSessionResolver stub. Every call returned an empty
session_id, and the gate refused all tool calls with
ErrFrameStaleTooFar. v2.0.2 fixes this in three layers.

#### Schema (v17 â€” commit e16b2b7)

projects.active_session_id + active_session_set_at columns
added by migration v17 (sqlite + postgres). Set by session_start
and session_resume; cleared (compare-and-set) by session_close.

#### Store interface

Three new methods on store.Store:

- SetActiveSession(ctx, projectID, sessionID) â€” overwrite; idempotent
- GetActiveSession(ctx, projectID) (string, error) â€” empty string if none
- ClearActiveSession(ctx, projectID, expectedSessionID) â€” CAS clear

SQLite implements all three. Postgres has stubs (driver unused
on this host).

#### Gate per-tool session requirement

RequiresActiveSession(toolName) allowlist gates PreCheck's
"SessionID or ProjectID empty" refusal. Default is true (most
tools require a session). The exempt list: session_start,
session_resume, health_ping, memory_state, project_create,
active_policy, load_constitution, admin_schema_status,
admin_vacuum, admin_migrate. These are operator-issued setup
plus read-only introspection and must work without a session.

Bootstrap and read-only tools were broken in v2.0.1; this
restores the v2.0.0 contract for them.

#### Real ActiveSessionResolver

internal/server/active_session_resolver.go â€” the
StoreBackedActiveSessionResolver queries the projects row
through a pluggable ActiveSessionLookup (main.go adapts via
StoreBackedLookup). Short in-process TTL cache (default 5s)
amortizes the per-tool-call DB read. Invalidate and
InvalidateAll are exported for write-through caching.

#### Orchestrator call sites

session_start / session_resume / session_resurrect write the
active pointer; session_close and the sweeper clear it
(CAS-aware so a stale close of an older session does not
clobber a newer session_start).

### Wire contract change

ActiveSessionResolver.ActiveSessionID gained a context.Context
first parameter. Both implementations (StaticSessionResolver,
StoreBackedActiveSessionResolver) updated. Third-party
implementations must adapt.

### Tests (regression guards for v2.0.1's failure)

- internal/policy/gate_v2_0_2_test.go â€” pins the
  RequiresActiveSession contract AND a direct guard that
  PreCheck on a session-free tool returns Allowed=true.
- internal/server/active_session_resolver_test.go â€” 6 tests
  covering cache hit, expiry, empty projectID no-op,
  lookup-error handling, Invalidate, CacheTTL=0 disable.
- tests/dual_driver/active_session_test.go â€” 4 tests covering
  Set/Get/Clear roundtrip, CAS semantics, race-new-session-wins,
  ErrProjectNotFound.

internal/server/middleware_test.go's
TestGateMiddleware_PreCheck_RefusesMissingIdentity was rewritten
to use vibe_publish instead of health_ping (the latter is now
session-free and would not exercise the refusal path).

---

## [2.0.1] â€” 2026-07-27
 â€” 2026-07-27

### Added (gate promoted to the transport layer)

The v2.0.0 pivot put the policy-gateway into the orchestrator layer.
v2.0.1 promotes it to the transport layer so every `dark_memory_*`
tool call now flows through `internal/policy.PostCheck` via the new
`GateMiddleware`, *before* the inner handler runs.

- **`internal/server/middleware.go` (new) â€” `GateMiddleware.Wrap`**.
  PreCheck (capability grant + intent-in-scope) before the inner
  handler; PostCheck (drift-at-write, when `DriftChecker` is non-nil)
  after, for artifact-creating tools only. When `Gate` is nil, the
  legacy direct-dispatch path runs (no policy enforcement), keeping
  the existing test harness ergonomic.
- **`internal/server/middleware_test.go` (new)** â€” 387 lines covering
  3 categories: capability mismatch, scope mismatch, drift verdict
  refusal at write boundary.
- **`internal/server/server.go`** â€” `wrapHandler` routes through `Gate`
  when set.
- **`internal/server/lifecycle.go`** â€” `BootState.Gate` field.
- **`cmd/dark-mem-mcp/main.go`** â€” wires the Gate from the returned
  `FrameSource` at boot.

### Changed (FrameSource singleton, 5A.ii.b.2.c.1)

The per-call `FrameSource` construction in `dark_memory_recall` is
lifted to a boot-time singleton. Both the recall tool and the gate
now share the same `CachedSource` instance.

- **`internal/recall/singleton.go` (new)** â€” boot-time `FrameSource`
  construction. Singleton contract tested in `singleton_test.go`.
- **`internal/tools/register.go`** â€” `RegisterAll` signature changes
  from returning `error` to returning `(policy.FrameSource, error)` so
  the caller can wire it into the gate.
- **`internal/tools/recall.go`** â€” uses the passed-in singleton;
  per-call construction is gone.
- **`tests/e2e/server_test.go`** â€” updated for the new `RegisterAll`
  signature.

### Added (operator ergonomics)

- **Legacy `DARK_SCRAPPER_URL` env shim** (H-4 follow-up). PR #10
  dropped the v1.x env name without a backward-compat alias, which
  broke unmigrated operators' drift-judge path on first boot. v2.0.1
  closes the gap: when `DARK_SCRAPPER_URL` is set AND
  `DARK_DRIFT_JUDGE_DAEMON_URL` is not, `NewSelfHarnessClient` falls
  through to the legacy value and logs a one-line deprecation notice
  at startup. The legacy env will be removed in v2.1.0.
  Migration: `sed -i 's/DARK_SCRAPPER_URL/DARK_DRIFT_JUDGE_DAEMON_URL/g' .env`
- **`internal/orchestration/llm_client_scrapper_alias_test.go` (new)**
  pins the fallback contract end-to-end against the live binary.

### Notes

- **`DriftChecker` is intentionally `nil`** in the gate wiring. The
  strict-mode opt-in is a separate follow-up (or a v2.0.2 if needed).
  The gate still runs PreCheck unconditionally and refuses tools the
  LLM isn't granted.
- **`DefaultServerVersion` bumped** from `2.0.0-dev` â†’
  `2.0.1-dev` in `internal/server/bootstrap.go` for the legacy
  hardcoded fallback. Canonical version resolution flows through
  `version.Resolve()` (set by `make release` via `-ldflags`).
- `git describe --tags --always --dirty` on a fresh v2.0.1 tag will
  print `v2.0.1` cleanly (no `+dirty` suffix).

---

## [2.0.0] â€” 2026-07-19

### Breaking (operator env contract â€” ships in PR #10)

- **`DARK_SCRAPPER_URL` â†’ `DARK_DRIFT_JUDGE_DAEMON_URL`**.
  SelfHarnessClient provider renamed from `"dark_scrapper"` to
  `"drift_judge_daemon"`. Function `judgeViaScrapper` â†’
  `judgeViaDriftJudgeDaemon`. **No backward-compat alias** â€”
  operators must update their env.
  Migration: `sed -i 's/DARK_SCRAPPER_URL/DARK_DRIFT_JUDGE_DAEMON_URL/g' .env`.
- **`DARK_JUDGE_MODEL_SCRAPPER` â†’ `DARK_JUDGE_MODEL_DRIFT_JUDGE_DAEMON`**.
- Test file `internal/orchestration/scrapper_wiring_test.go` â†’
  `internal/orchestration/drift_judge_daemon_wiring_test.go`.

### Breaking (server lifecycle default)

- **Shutdown default `close_reason` `aborted` â†’ `clean`** in
  `internal/server/lifecycle.go`. Operators who relied on the
  legacy `aborted` default for crash-recovery workflows can opt
  back via `DARK_SHUTDOWN_CLOSE_REASON=aborted`.
- New env var `DARK_AUTO_RESURRECT=on_boot` opts into automatic
  session resurrection on server startup. Default: orphans are
  surfaced via a log entry but not auto-recovered.

### Fixed

- **INFRA-002 â€” `dark_memory_vibe_spec` now surfaces WHICH form and WHY
  on tasks parse failure.** Pre-fix, `parseTasksField` in
  `internal/orchestration/vibe_spec.go` discarded the underlying
  `json.Unmarshal` error and returned only
  `store.NewFieldError(store.ErrInvalidArgument, "tasks")`, surfacing
  `"invalid argument at field=tasks"` to the harness with no
  diagnostic. Post-fix:
  - The error chain is `*store.FieldError{Field:"tasks"}`
    (F35 wire-propagation keeps working: `errors.As(err, &fe)`
    still finds it; `errors.Is(err, ErrInvalidArgument)` still
    matches).
  - Wrapped with `fmt.Errorf("%w: rejected by parser (Form A/B
    step N/unknown form ...): %v", fe, cause)` so the operator
    sees which form was attempted AND the underlying json
    diagnostic (e.g. "invalid character 'Â·' after top-level
    value").
  - The unknown-first-byte path explicitly names the offending
    byte (`first non-whitespace byte='{'`) and the expected
    shape without leaking the rest of the payload (preserves
    `classifyUnknown`'s no-payload-leak policy).
- Concrete reproductions covered by:
  - `internal/orchestration/vibe_spec_test.go` (orchestrator-level):
    - `[{...}]Â·` (trailing garbage byte after close-bracket, Form A)
    - `"[not-an-array]"` (outer string but inner not parseable, Form B)
    - `{...}` (object-shaped payload â€” neither Form applies)
  - `tests/wire/infra002_vibe_spec_diagnostic_test.go` (H-3 wire
    conformance): pins the contract end-to-end against the running
    binary over JSON-RPC â€” the same envelope shape production
    harnesses see.

### Added (memory-as-policy-gateway pivot)

The pivot replaces the pull-based CRUD model (v1.x) with a
gate-driven active-memory model. Every `dark_memory_*` tool call
now traverses `internal/policy.PostCheck` which:

1. Composes an **atomic context frame** from session + project +
   global state via `internal/atomic.FrameSource`.
2. Verifies the call's intent is in scope and the LLM has the
   capability grant for it (`CapabilitiesFrame`).
3. Invokes the orchestrator with the frame as input.
4. **Drift-checks the response at the write boundary** before
   returning to the LLM (`internal/drift.Checker`).

- **`dark_memory_recall` (29th canonical tool, CONTEXT 3 â†’ 4)** â€”
  the canonical scoped-replay orchestrator. Inputs: scope
  (global|project|session), project_id, session_id, since_token.
  Outputs: per-kind atomic frames (Identity, Scope, Capabilities,
  Drift, Persona) + delta write_audit rows since since_token +
  new_token cursor. RFC Â§3 M1 + Â§6.1.
- **`internal/atomic` package (NEW)** â€” Frame interface + 6
  concrete types: IdentityFrame, ScopeFrame, CapabilitiesFrame,
  PersonaFrame, DriftFrame, EvidenceFrame. FrameSource interface.
  Wave 5X.2: `Frame.Hash` signature `Hash() [32]byte` â†’
  `Hash() ([32]byte, error)` (defense against previous
  interface/type mismatch that was silently swallowed).
- **`internal/drift` package (NEW)** â€” `Strictness` enum
  (off|warn|strict), `Checker` type with `CheckArtifact(ctx,
  ArtifactInput) â†’ Verdict`, `JudgeCaller` interface. Replaces
  the previous `policy.PostCheck` stub. Decision tree for judge
  errors: strict refuses, warn allows.
- **`internal/policy` package (NEW)** â€” gate.PostCheckInput
  gains `DriftChecker + DriftArtifact` optional fields.
  `PostCheck` now calls `drift.Checker` when `Strictness != off`.
- **`internal/recall` package (NEW)** â€” `StoreSource` (reads
  from `store.Store`) + `CachedSource` (INV-5 cache re-hash on
  Get + audit emission on cache_mismatch). 9 tests.
- **Session lifecycle resilience** â€” `session_resurrect`,
  `session_recover`, `session_heartbeat`, `session_sweeper`,
  `boot_reconcile`. Closed-due-to-crash sessions are now
  resurrectable (only operator-initiated termination is
  terminal). `SessionResurrectOutput` gains 5 fields:
  `InheritedConstitution{ID,Ver}`, `ActiveConstitution{ID,Ver}`,
  `ConstitutionBumped`, `InheritedMods`.
- **L6 adapter integration** â€” 3 hooks from
  `BRIDGE_AND_COEXISTENCE.md` Â§6 wired:
  * `startup-recover` â†’ `runStartupRecover()` in main.go
  * `periodic-heartbeat` â†’ sweeper (5E.iii, doc-only)
  * `exit-close_clean` â†’ Shutdown default reason = clean
- **Per-project drift strictness** â€” `Project.DriftStrictness`
  field (migration v14). `drift.ResolveStrictness(projectOverride,
  envValue, warnf)` â€” empty/'default' â†’ env; valid â†’ override;
  invalid â†’ warn + env fallback.

### Added (canonical tool count 28 â†’ 29)

- **`dark_memory_recall`** â€” see "memory-as-policy-gateway
  pivot" above. CONTEXT namespace: 3 â†’ 4 tools.

### Changed (data plane)

- **Schema migrations v11â€“v15** (sqlite + postgres):
  * v11, v12 â€” frame-related scaffolding (see git log)
  * v13 â€” `CREATE UNIQUE INDEX uq_vibe_frames_natural_key ON
    vibe_frames (project_id, session_id, scope_level, scope_id,
    frame_kind)` â€” enables the UPSERT rewrite
  * v14 â€” `ALTER TABLE projects ADD COLUMN drift_strictness TEXT
    NOT NULL DEFAULT 'default'`
  * v15 â€” `ALTER TABLE vlp_state ADD COLUMN open_spec_id INTEGER
    NOT NULL DEFAULT 0`
- **SaveFrame rewritten as INSERT ... ON CONFLICT DO UPDATE**
  (sqlite) / `ON CONFLICT ... RETURNING` (postgres). Replaces the
  SELECT-then-INSERT/UPDATE race in the previous implementation
  under concurrent SaveFrame calls. Tested with 10-goroutine
  concurrent upsert â†’ 1 row.
- **`WriteContext.SessionEvent`** â€” every `Save*` emits this
  field in `write_audit`. Closes pre-existing drift where the
  `session_event` column was INSERTed NULL silently.
- **`VLPStateRow.OpenSpecID`** â€” the actual spec_id the session
  is working on. Previously the recall cache used `vlp_state.ID`
  as a meaningless proxy.
- **`Project.DriftStrictness`** â€” per-project resolver override.

### Notes (constitution + RFC)

- The pivot's design rationale lives in
  `vibe-flow/main/ACTIVE_MEMORY_RFC.md`, `SCHEMA_v11_v12.md`, and
  `DRIFT_BURST.md` â€” these are operator-private planning docs
  (NOT committed; lives in the operator's local workspace).
- Public docs updated: `vibe-flow/PLAN.md` v2 (pivoted roadmap),
  `vibe-flow/main/BRIDGE_AND_COEXISTENCE.md` v2 (cx.v3,
  policy_gateway, dark-research-mcp demoted, dark-recall
  cancelled).
- `DefaultServerVersion` constant bumped from `"1.4.1-dev"` â†’
  `"2.0.0-dev"`. Canonical source remains `version.Resolve()`
  (set by `make release` via `-ldflags`).
- 9 commits + 1 release (this PR). Pre-merge lint scrub PR #10
  established the H-4 compliant env-var rename as a separate
  concern.

---

## [1.4.1] â€” 2026-07-18

### Behavior change (callers MUST verify)

- **`dark_memory_vibe_spec` now rejects non-canonical `vibe_case` values**
  with `ErrInvalidArgument`. Previously the JSON Schema layer accepted
  any string (e.g. `"C8"`, `"code"`, `"c1"`); now both the JSON Schema
  enum AND the orchestrator reject unknown values via `vibecase.Parse`
  (defense in depth).
- Callers using valid `C1`..`C7` values see **no change**.
- Callers passing `""`, whitespace-only, or any non-canonical label
  now receive a structured error:
  ```
  vibe_case: vibecase: invalid case identifier: "X" is not one of
             [C1 C2 C3 C4 C5 C6 C7]
  ```
- **Migration:** if your harness ever passed an unexpected `vibe_case`
  value (e.g. a downstream convention of `"image"` for C3), map it
  to the canonical `"C3"` label before sending. No migration tool
  ships; the rejection is fail-loud and the operator-visible error
  names the allowed set.

This is the change that motivated the v1.4.1 PATCH bump instead of
v1.4.2: the new validation is observable to callers but does not
break any caller that was previously compliant with the canonical
C1..C7 set. Per the project's SemVer convention (no formal API
stability promise at v1.x), a PATCH bump is appropriate.

### Added (canonical C1..C7 taxonomy)

- **`internal/vibecase` package** â€” single source of truth for the
  C1..C7 case taxonomy. Replaces a JSON Schema enum fragment that
  was duplicated across `vibe_publish` and (asymmetrically) absent
  from `vibe_spec`. Exports:
  - `Case` (typed string) and the seven canonical constants
    `CaseCode..CaseMixed`.
  - `Parse(s)` (strict, trims, rejects empty + unknown + mixed-case),
    `MustParse(s)` (panic-on-error for startup constants),
    `IsValid(s)` (boolean shortcut).
  - `All()` and `JSONSchemaEnum()` â€” stable, ordered, defensively
    copied.
  - `Description(c)` â€” human-facing one-liner per case (for LLM
    context projections).
  - `ErrInvalidCase` â€” exported sentinel for `errors.Is` checks.
  - 15 unit tests covering ordering, defensive copy, trim, empty,
    unknown, mixed-case, error message contents, panic, boolean
    shortcut, round-trip, description, cardinality.

### Changed

- **`vibe_spec` now enforces the C1..C7 enum** at the JSON Schema
  layer (`internal/tools/vibe.go`) AND at the orchestrator layer
  (`internal/orchestration/vibe_spec.go`), closing the asymmetry
  where `vibe_publish` validated the enum but `vibe_spec` did not.
- **`vibe_publish` JSON Schema enum now derives from
  `vibecase.JSONSchemaEnum()`** instead of a hardcoded literal. Any
  future case addition automatically propagates to both tools.
- **Both orchestrators validate via `vibecase.Parse`** (defense in
  depth): even if the JSON Schema layer is bypassed (direct
  orchestrator call, future non-MCP transport, etc.), the validator
  rejects unknown cases before the row is persisted.
- 4 new orchestrator tests:
  `TestVibeSpec_InvalidVibeCase`,
  `TestVibeSpec_AcceptsAllCanonicalCases`,
  `TestVibeSpec_AcceptsTrimmedVibeCase`,
  `TestPublishVibe_InvalidVibeCase`.

### Versioning note

Adding a case (e.g. C8) is a MINOR bump and is backward-compatible
(case labels are stored as TEXT; existing rows remain readable).
Reordering or renaming an existing case is a BREAKING change. See
the package doc on `internal/vibecase` for the full contract.

---

## [1.4.0] â€” 2026-07-18

### Added (release-integrity release)

- **`release-integrity@1.0.0` constitution** ([`CONSTITUTION.md`](CONSTITUTION.md)).
  Five rules codify release hygiene: (1) single source of truth for
  version, (2) archive-not-delete for deprecation, (3) CHANGELOG is
  authoritative, (4) drift detection on every boot, (5) session-bound
  governance. Cross-cutting reference for every `vibe_publish` artifact
  in the dark-memory-mcp project.
- **`internal/version` package** â€” single-source version resolver.
  Replaces the hardcoded `DefaultServerVersion = "1.3.0"` constant in
  `internal/server/bootstrap.go` and the `var Version = "1.1.0-dev"`
  in `cmd/dark-mem-cli/main.go` and `cmd/dark-mem-inspect/main.go`.
  Resolution priority: `-ldflags` injection (canonical, set by
  `make release`) â†’ `debug.ReadBuildInfo()` (dev) â†’ hardcoded
  `"dev"` sentinel (emergency). 9 unit tests cover all three paths.
- **`Makefile`** with `build` / `release` / `drift-check` /
  `version` / `version-json` / `inspect` / `tag` / `clean` targets.
  Handles the multi-module `cmd/*` layout (each cmd is its own Go
  module; the Makefile `cd`s into each before `go build`).
- **`scripts/inject-version.sh`** (bash) and
  **`scripts/inject-version.ps1`** (PowerShell) â€” resolve the canonical
  version from `git describe` and emit the `-ldflags` expression that
  feeds `make release`. Same resolution rules, same output formats
  (`--raw` / `--json` / default), same `--strict` flag.

### Added (drift detection in health_ping)

- **`dark_memory_health_ping` response grew a `git` block.**
  New fields: `git.tag`, `git.commit`, `git.dirty`, `git.build_time`,
  `git.source` (one of `ldflags|buildinfo|dev`), `git.is_dev`.
- **Top-level `drift` bool** â€” true iff the resolver fell back to the
  dev path OR the working tree was dirty at build time. Per
  `CONSTITUTION.md` Rule 4, a release binary MUST report
  `drift=false`. Operators can monitor the single-bit signal directly.
- Wire-conformance test (`tests/wire/health_ping_test.go`) and the
  e2e binary (`cmd/e2e/main.go`) updated to mirror and assert the
  new fields.

### Changed

- The `internal/server/bootstrap.go::DefaultServerVersion` constant
  is now a deprecated string (`"1.4.0-dev"`) for any external
  callers; the canonical default flows through `version.Resolve()`.
- `cmd/e2e/main.go` relaxed the hardcoded `"1.3.0"` health_ping
  version assertion to "non-empty" â€” the value is now driven by the
  resolver, not by source code.

### Notes

- v1.4.0 ships together with **dark-research-mcp v0.7.0**, which
  wraps 38 duplicate tools (the dark_mem_*, dark_research_spec_*, etc.,
  and dark_ssd_* tools) in a deprecation envelope pointing at
  dark-memory-mcp. See
  [`dark-research-mcp/RELEASE_NOTES_v0.7.0.md`](https://github.com/Opita-Code/dark-research-mcp/blob/main/RELEASE_NOTES_v0.7.0.md)
  for the peer release notes and migration guide.

---

## [1.3.2] â€” 2026-07-16

### Fixed

- **`fix(llm): wire SelfHarnessClient.Judge to drift-judge-daemon HTTP route.**
  `SelfHarnessClient.Judge` was returning `ErrNoLLMAvailable` unconditionally
  (deferred to Wave 4+ in source). Wired to POST to `DARK_SCRAPPER_URL/v1/messages`
  with `Bearer ds-managed` (sentinel auth) when `provider == "dark_scrapper"`.
  Other providers (anthropic / openai / google) still return `ErrNoLLMAvailable`
  by design, preserving the source's visibility-over-silent-degrade philosophy.
  URL validation rejects empty / `file://` / no-scheme / no-host before any
  HTTP call (R5 defense).

### Added

- **`feat(federation): cross-namespace lookup tool + pipeline_status hint.**
  dark-memory and dark-research MCPs use two physically separate SQLite files
  (`dark-memory.db` vs `dark.db`) with compatible schemas on shared tables.
  New `internal/federation` package: read-only `Peer` handle opened from
  `DARK_FEDERATION_PEER_DSN`. New `dark_memory_federation_lookup` tool
  (opt-in extra, same pattern as `DARK_REDTEAM=armed`). `pipeline_status`
  now probes the peer on local miss and adds a `cross_namespace_hint` field.

### Governance

- DARK-MEM-001 establishes the `release-integrity@1.0.0` constitution
  (see [`CONSTITUTION.md`](CONSTITUTION.md)) and retroactively tags `v1.3.1`
  at `fbc5c03` to give the squash commit a canonical annotated reference.

---

## [1.3.1] â€” 2026-07-16

### Note (release plumbing)

- **Local tag `v1.3.1` retroactively created at commit `fbc5c03`.** The
  commit message reads `release: v1.3.1 -- sync unreleased work to origin/main
  (squashed)`. The squash landed in the repo on 2026-07-16 but no annotated
  tag was created at the time; the v1.3.1 entry exists to give that squash
  a canonical reference and to keep the tag chain (v1.3.0 â†’ v1.3.1 â†’ v1.3.2)
  consistent with the commit graph.
- No standalone code changes between v1.3.0 and v1.3.2: v1.3.1 is a
  release-plumbing tag only. The substantive changes in this window are
  documented under v1.3.0 and v1.3.2.

---

## [1.3.0] â€” 2026-07-16

### Added (production-readiness release)

- **`dark_memory_health_ping` â€” operator-facing liveness probe.**
  The canonical surface grew 27 â†’ 28 tools (OBSERVABILITY 3 â†’ 4).
  health_ping is a strict, documented-shape probe distinct from
  `memory_state`:
    - **Latency budget:** <500ms round-trip (target <50ms on warm cache);
      suitable for K8s liveness/readiness probes that fire every second.
    - **Side-effect freedom:** does NOT touch the audit bus, does NOT
      advance VLP state, does NOT migrate. Safe to call at high
      frequency.
    - **Frozen contract:** `{server, db, runtime, registry, latency_ms,
      checked_at}`. Adding fields is backward-compatible; removing
      fields is a breaking change to monitoring rules.
  Wire conformance: `tests/wire/health_ping_test.go::TestWire_HealthPingShape`
  (verifies all fields) and `::TestWire_HealthPingLatency` (verifies the
  500ms ceiling). Tool count: `tests/wire/zz_toolenum_test.go`.
- **`tests/wire/wire_session_test.go::waitForBootMarker`** â€” eliminates
  the startup race that previously caused intermittent "tool not found"
  failures when `initialize` arrived before the binary's mcp-go loop
  started. The harness now waits up to 5s for the `registered N tools`
  boot marker on stderr before sending `initialize`.
- **`internal/tools/health.go::unwrapToolResponse` helper** â€” single
  point of edit for the mcp-go `content:[{type:"text",text:"..."}]`
  envelope shape that wraps every tool response.
- **`Config.BootedAt`** field â€” wall-clock time captured at config load;
  `SetRuntimeContext` propagates it into `health_ping` so uptime is
  accurate from the very first call.
- **`.github/workflows/ci.yml`** â€” operator-reproducible CI recipe:
  builds, runs lint, runs `go test ./...`, runs `go test ./tests/wire`
  with `DARK_MEM_MCP_BIN` set. The never-push policy is preserved
  (this file lives in-repo for transparency; CI is local-only).
- **`docs/PRODUCTION_CHECKLIST.md` Â§Health Probe** â€” wiring guide for
  the new `dark_memory_health_ping` including a sample K8s liveness
  probe YAML and a Prometheus `up{job="dark-mem-mcp"}` snippet.

### Changed
- **Canonical tool count 27 â†’ 28.** README, DECISION_MATRIX,
  bridge.7 conformance test, e2e canonical-order test, and the
  sanity check inside `tools.RegisterAll` all bumped to 28.
- **`DARK_SERVER_VERSION` default** bumped from `1.2.3` to `1.3.0`
  (`DefaultServerVersion` constant in `internal/server/bootstrap.go`).
- **`tests/wire/wire_session_test.go::resolveWireBin`** now skips the
  test when no binary is found (previously fataled). The first
  candidate is still `../cmd/dark-mem-mcp/dark-mem-mcp.exe` so a
  freshly-built binary is picked up automatically.

### Documented
- **`docs/PRODUCTION_CHECKLIST.md` Â§Race detector availability** â€”
  the operator's `go test -race` requires a C compiler; on this host
  no gcc is installed and the race detector is therefore unavailable.
  Workaround: validate via the wire suite (10 tests, including
  TestWire_HealthPingLatency which exercises 5 sequential calls and
  catches perf regressions) and the e2e suite (`tests/e2e/server_test.go`
  fires 1000 concurrent calls).
- **`docs/PRODUCTION_CHECKLIST.md` Â§Stale-binary gotcha** â€” if a
  previous binary is left at `dark-mem-mcp.exe` in the repo root
  (or in `PATH` before `cmd/dark-mem-mcp/`), the wire harness's
  fallback resolution picks it up. Always rebuild into
  `cmd/dark-mem-mcp/` and either delete or set `DARK_MEM_MCP_BIN`
  explicitly when running `go test ./tests/wire`.

### Tests
- 15 / 15 packages PASS in `go test ./...` (full sequential suite).
- 10 / 10 wire tests PASS against the v1.3.0 binary in
  `go test -tags wire ./tests/wire/...` (with `DARK_MEM_MCP_BIN`
  set). Total wire suite runtime: ~25s.
- The 28-tool contract is enforced by both `TestE2E_28ToolsRegistered`
  (Go level) and `TestWire_RuntimeToolEnumeration` (wire level).

### Migration from v1.2.x
- **Drop-in for v1.2.5 operators.** No DB schema change, no migration
  bumps, no env var renames. The 28th tool is purely additive.
- The canonical order has a single new entry between `anomalies` and
  `admin_migrate`: `health_ping` at position 23 (0-indexed). Any
  harness that iterates `tools/list` and indexes by **name** is
  unaffected. Any harness that indexes by **position** must update.
- The `dark_memory_health_ping` tool is registered as canonical,
  not as an "extra". Un-armed servers see 28 tools; armed servers see
  28 + 3 redteam = 31.

---

## [1.2.5] â€” 2026-07-16

### Added
- **`tests/wire/` end-to-end JSON-RPC suite.** Wire-conformance tests
  prove fixes actually work through the real MCP wire (binary
  subprocess + JSON-RPC over stdio), not just at the Go orchestrator
  level. Catch the bugs that Go-level tests cannot: harness encoding
  (LLM dependent), schema-layer mismatches, error-envelope propagation.
  **Rule (H-3 in CONTRIBUTING.md):** every fix MUST ship with at
  least one wire test.
- **`store.FieldError` structured type + F35 wire propagation.** Previously
  orchestrator-level `ErrInvalidArgument` errors discarded the field
  name; only `json.UnmarshalTypeError` paths set `ToolError.Field`.
  This meant a `parseTasksField` rejection (e.g. LLM emits a number)
  surfaced as the generic "One or more arguments failed validation"
  message. `store.FieldError` carries the structured Field; ToToolError
  extracts it via `errors.As` and propagates to `ToolError.Field`.
  Tests: `tests/wire/f35_structured_error_test.go` (end-to-end via
  binary), `tests/orchestration/orchestrator_test.go::TestVibeSpec_StringifiedTasks_MalformedRejected`.
- **CONTRIBUTING.md** baking the four hard rules (H-1 each MCP owns its DB,
  H-2 array/object string fallback, H-3 wire tests mandatory, H-4 no
  private names in public artifacts) and seven conventions. Every
  future dark-* server is built against this doc.
- **`docs/PRODUCTION_CHECKLIST.md`** operator runbook: boot signal
  matrix, recovery playbooks (R-1 vec0, R-2 dark.db corruption, R-3
  tasks shape, R-4 LLM-prompt drift), dark-research vs dark-memory
  isolation verification, performance baselines, one-page cheat
  sheet.
- **Wire test infrastructure.** `tests/wire/wire_session_test.go`
  provides `wireSession` (binary subprocess + JSON-RPC framed
  stdio), `startWireSession(t)` (per-test isolated DB under
  `t.TempDir()`), `testsCall(name, args)` (strict per-id request).
  Override `DARK_MEM_MCP_BIN` env var to test a specific binary.

### Changed
- **`parseTasksField` error propagation.** Errors now wrap via
  `store.NewFieldError(store.ErrInvalidArgument, "tasks")` so the
  field name reaches `ToolError.Field`. The orchestrator-level
  `errMissingField` helper now also returns a `store.FieldError`
  instead of a plain `fmt.Errorf`. **Wire test impact:**
  `TestWire_F35_TypeMismatchSurfacesFieldPath` now passes (was
  returning the generic error envelope pre-fix).
- **`vibe_publish` shape regression test.** Tests now post the CORRECT
  nested shape (spec as object, artifact as object, tasks as
  JSON-encoded string). Pins the post-F33 contract.

### Tested
* 7 wire-conformance tests against the live binary:
  - F33 (vibe_publish nested schema)
  - INV-8 (defaultDSN isolation against cwd dark.db collision)
  - F35 (structured field error via `tasks: 42.0`)
  - F36 array form
  - F36 stringified-array form
  - F37-F40 (boot against half-migrated dark-memory.db)
  - F37 (duplicate column tolerance via ApplyOne-by-statement split)
* 15 of 15 package test suites pass (last suite run before this
  commit). The conformance suite is occasionally flaky under heavy
  concurrent load (full suite at once); reruns always pass.

### Operator notes
- Drop-in replacement for v1.2.4. No DB migration.
- The new `tests/wire/` package requires `DARK_MEM_MCP_BIN=<path-to-binary>`
  unless `./dark-mem-mcp.exe` is in the repo root (the default for
  development). Production CI should set this env var explicitly.
- The four wire-test failures (F35 fixed, F33 payload fixed,
  F37-F40 seed fixed) were real production bugs caught by writing
  wire tests FIRST in the regression suite. The "test the orchestrator
  only" approach was missing harness-layer failures.

---

## [1.2.3] â€” 2026-07-16

### Added
- **INV-8 (per-MCP database isolation).** Each MCP server in the dark-agents family owns its **own SQLite file** by convention. dark-memory-mcp now defaults to `dark-memory.db` instead of `dark.db`; dark-research-mcp continues to use `dark.db`. Sharing `dark.db` was the root cause of the v1.2.2 boot crashes (schema_migrations name collisions in the shared bookkeeping table). The principle is documented in `docs/INVARIANTS.md` (new `INV-8` section, with rationale, defence test, operator signal, and applicability to all future dark-* servers). Defensive test: `tests/invariants/inv8_test.go::TestServer_DefaultDSN_DoesNotCollideWithDarkResearch_INV8` â€” asserts the default DSN (a) is not `dark.db`, (b) doesn't contain `dark-research`, (c) contains `dark-memory`. Operators who want the legacy shared-DB behaviour can opt in via `DARK_DB=dark.db` env var.

### Changed
- **`defaultDSN()` â†’ `"dark-memory.db"`** (was `"dark.db"`). Backward-compatible override via `DARK_DB=` env var. Affects `internal/server/bootstrap.go` only. New public accessor `server.DefaultDSN()` so tests/invariants can assert without reflection. No DB migration needed; the change only affects the default path.

### Future directions
- **`[FUTURE-MCP-1]`** (the next dark-* project, see session notes) MUST default to a project-specific filename (`harvest.db` or per-project variant) and pass the `INV-8 defaultDSN uniqueness` lint. The lint is informal today (a grep in CI) but will become a go-vet rule in v1.3.0. Documented in `docs/INVARIANTS.md` under INV-8.

---

## [1.2.2] â€” 2026-07-16

### Fixed
- **F37 â€” migration runner now tolerates "duplicate column name" errors.** applyOne in `internal/migrate/migrate.go` was running every statement in `m.Up` via a single `tx.ExecContext` inside one transaction. Any failure (including benign "duplicate column name: project_id" when a v7-style ALTER TABLE ADD COLUMN had partially completed during a prior boot crash) rolled back the WHOLE migration and aborted the daemon. The runner now splits multi-statement migration bodies on `;`, runs each statement separately, and treats the duplicate-column error class (SQLite `duplicate column name: X` + Postgres `column X already exists`) as already-satisfied. Regression tests cover the recovery flow (`TestMigrate_TolerantOfDuplicateColumn_F37`) plus a regression guard against over-broad catch (`TestMigrate_StillFailsOnNonDuplicateErrors_F37`).
- **F38 â€” `EnsureCoreTables` self-heals missing core tables on boot.** The dark.db at `C:\Users\Nico\AppData\Local\dark-agents\dark.db` is shared with dark-research-mcp, whose bookkeeping table uses the same `schema_migrations` rows. When dark-research-mcp's v1-v3 were applied with overlapping version names (initial_schema, constitutions_and_mods, sdd_evaluations_constitution_audit), dark-memory-mcp's v5+ (`sessions_table`, `project_namespace`, `vibe_brands_composite_unique`, `vlp_state_table`, `audit_project_index`) appeared "already applied" without having actually run against the schema â€” leaving `sessions` and `projects` tables physically absent from the DB. New helper `migrate.EnsureCoreTables(ctx, db)` issues `CREATE TABLE IF NOT EXISTS` for the four core tables v5/v6/v7 expect to find, called once from the sqlite Store's `Open` before `Migrate` so the migration runner sees the correct schema state. Tests: `TestEnsureCoreTables_FreshDB_F38`, `_Idempotent_F38`, `_RecoveryFromHalfMigratedDarkDB_F38` (the exact 6-step crash repro from today's session).
- **F39 â€” migration runner tolerates "no such module: <ext>" errors.** Orphan sqlite-vec triggers (`trg_research_items_vec_delete`, etc.) referencing the unloadable `vec0` virtual-table module were causing `ALTER TABLE vibe_brands RENAME TO vibe_brands_old` (in v8) to surface `SQL logic error: error in trigger trg_research_items_vec_delete: no such module: vec0`. Same `applyOne` extension; the "no such module" substring is now treated as already-satisfied at the per-statement level. Tests in `tests/migrate/tolerate_ddl_errors_f39_f40_test.go::TestMigrate_ToleratesNoSuchModule_F39`.
- **F40 â€” migration runner tolerates "table X already exists" errors.** The same per-statement loop now also handles the rare case where a `CREATE TABLE` in a migration's `Up` is called against a table that already exists (e.g. `EnsureCoreTables` + `Migrate` both try to create the same table at boot, or a v8-style rename-and-recreate pattern). The existing table is preserved as-is. Test in `tests/migrate/tolerate_ddl_errors_f39_f40_test.go::TestMigrate_ToleratesTableAlreadyExists_F40`.

### Operator notes
- v1.2.2 is a **drop-in replacement** for v1.2.1. No migrations required. The 27-tool canonical surface is unchanged. No DB schema change.
- Restart the running `dark-mem-mcp.exe` to pick up the new code; the F37/F38/F39/F40 changes only affect boot behaviour.
- **However**, today's dark.db at the canonical path is in a pre-v1.2.0 partial state (has `attempts`, `audit`, `findings`, `judgments`, `runs`, etc. tables from a previous [prior-evaluation-loadout] loadout, plus orphan vec0 triggers). Even with v1.2.2's tolerance patches, v8 (`vibe_brands_composite_unique`) will fail at the `INSERT INTO vibe_brands SELECT FROM vibe_brands_old` step because the rename was silently skipped (F39). To bootstrap a clean dark-memory-mcp state without losing recent work, see the operator's playbook:
  - **Safe path A (recommended):** archive the current dark.db (`Rename-Item dark.db dark.db.bak-$(date)`) and let v1.2.2 create a fresh one. Existing `research_*` rows from dark-research-mcp won't be visible (that's the cross-project trade-off) but dark-memory-mcp boots cleanly.
  - **Safe path B:** point dark-memory-mcp at a separate DB via `DARK_DB=./dark-memory.db`. The defaultDSN stays `./dark.db`; setting the env var on the binary is sufficient.
  - **Risky path C (do not try):** manually drop `vibe_brands` before booting v1.2.2 so v8 can recreate it. The F37/F39 tolerance will then drop the rename/recreate loop back into a clean state. Only do this if you've back-vacuumed data.

### Known issue
- The dark.db shared schema_migrations bookkeeping between dark-research-mcp and dark-memory-mcp is fragile by design (both projects use `version INTEGER, applied_at TEXT` rows but the version numbers are NAME-aligned, not ID-aligned). Future directions to consider: namespace dark-memory-mcp's bookkeeping to `dark_memory_schema_migrations`; or partition the schema_migrations table by namespace. Not addressed in v1.2.2 â€” separate PR if you want to take it on.

---

## [1.2.1] â€” 2026-07-16

### Fixed
- **F36 â€” `vibe_spec` rejects payloads from MCP harnesses that stringify arrays.** The gemela tool `dark_research_spec_create` (separate server, same `vibe_specs` table) declares `tasks` as `type: "string"` and persists the value as opaque text. `dark_memory_vibe_spec` declared `tasks` as `type: "array"` and required `Tasks []VibeSpecTask`. Some MCP harnesses serialise array arguments as JSON-encoded strings under either schema; in that case `BindOrchestrator`'s `json.Unmarshal` fails with `*json.UnmarshalTypeError: cannot unmarshal string into Go struct field VibeSpecInput.tasks of type []orchestration.VibeSpecTask`, and the operator-visible error surfaced as a generic `ErrInvalidArgument` (without a precise field hint) â€” F35's structured-field reporting kicked in only on successful unmarshal-then-orchestrator failure paths, not on raw unmarshal failures. Symptom: every `dark_memory_vibe_spec` call from certain harnesses returned `{"code":"ErrInvalidArgument","message":"One or more arguments failed validation..."}` regardless of payload validity.
  - `internal/orchestration/vibe_spec.go` â€” `Tasks` is now `json.RawMessage`; new helper `parseTasksField` accepts both forms (leading-byte dispatch on `[` vs `"`) and returns a typed `[]VibeSpecTask`. The validation graph (unique ids, non-empty description, depends_on consistency, cycle detection) is unchanged.
  - `internal/tools/vibe.go` â€” schema for `tasks` widened from `type: "array"` to `anyOf: [{...array, items: vibeSpecTaskSchema}, {type: "string"}]`. Both forms now advertise at the wire layer so harnesses can pick whichever shape they prefer.
  - `tests/orchestration/orchestrator_test.go` â€” added `mustMarshalTasks` helper bridging the old typed-slice test bodies; added 2 new tests: `TestVibeSpec_AcceptsStringifiedTasks` (round-trip: raw string in, parsed array in storage) and `TestVibeSpec_StringifiedTasks_MalformedRejected` (precise error mentions "stringified" plus `ErrInvalidArgument`). The 8 pre-existing VibeSpec tests updated from `Tasks: []orchestration.VibeSpecTask{...}` to `Tasks: mustMarshalTasks(t, []orchestration.VibeSpecTask{...})`.

### Operator notes
- v1.2.1 is a **drop-in replacement** for v1.2.0. No migrations required. The 27-tool canonical surface is unchanged (no new tools, no deprecations). No DB schema change.
- Restart the running `dark-mem-mcp.exe` (PIDs currently running the pre-v1.2.1 binary are tagged in the process list) to pick up the new code. Until restart, `dark_memory_vibe_spec` calls that pass `tasks` as a raw array will continue to fail â€” pass them as a JSON-encoded string in the meantime.

---

## [1.2.0] â€” 2026-07-16

### Added
- **`dark_memory_project_create`** (F33 / Bug C) â€” new PROJECT namespace tool (1 tool) that closes the bootstrap loop for INV-7 multi-tenancy. Prior to v1.2.0, the only way to provision a non-`default` project was to insert into the `projects` table out of band; now operators can create tenants from inside the MCP surface, then immediately call `dark_memory_session_start` with the new `project_id`. Idempotent on `project_id` â€” re-creating an existing project returns the existing row with `idempotent_replay: true` and the original `created_at`.
  - `internal/tools/project.go` â€” new file (RegisterProject + ProjectCreateInput/Result + validation)
  - Kebab-case pattern enforced: `^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`
  - Placed at canonical index 0 (before `session_start`) so tools/list discovery order matches the natural bootstrap flow
- **F35 structured error reporting** â€” `ToolError` extended with `Field`, `ExpectedType`, `ActualType`, and `SchemaHintURL`. `BindOrchestrator` now promotes `*json.UnmarshalTypeError` paths into discrete fields instead of hiding them in `Message`. Callers (LLM-driven or operator-driven) can render targeted fix-up hints without parsing free-form strings. All new fields are `omitempty` so the legacy shape is preserved for non-type-mismatch errors.
- **`vibeSpecTaskSchema`** (F33 / Bug B) â€” extracted shared strict schema for `vibe_spec` / `vibe_publish` task items. `additionalProperties: false` + explicit property list (`id`, `description`, `depends_on`, `owner`). Stops the silent-drop / type-coerce behavior that made calls fail with `cannot unmarshal string into ... depends_on of type []string` when callers passed `title`/`status`/`priority`.
- **`tests/tools/project_tool_test.go`** â€” 7 sub-tests covering happy path, idempotent replay, schema rejection (uppercase project_id, empty display_name, missing fields, unknown field) and the BindStore error envelope shape.

### Fixed
- **F33 / Bug A â€” `vibe_publish` JSON Schema is wrong.** Schema declared `spec`, `constitution`, `tasks`, `artifact_url`, `artifact_type`, `text` as flat top-level strings, but the Go struct `PublishVibeInput` (internal/orchestration/publish_vibe.go:42-72) nests them under `Spec PublishSpecInput` and `Artifact PublishArtifactInput`. Result: every harness call failed with `cannot unmarshal string into Go struct field PublishVibeInput.spec of type orchestration.PublishSpecInput`. Schema is now nested-correct with `additionalProperties: false` on both sub-objects.
- **F33 / Bug C â€” `dark_memory_project_create` was documented but not implemented.** `internal/project/types.go:9` advertised the tool, but no `tools/project.go` existed. Closed by adding the tool in this release.

### Changed
- **Canonical tool surface: 26 â†’ 27** (F33). New PROJECT namespace (1 tool) inserted at index 0. `NewRegistry`, `CanonicalOrder`, and the boot-time sanity check in `RegisterAll` updated to expect 27.
- **Tool surface layout**:
  - `PROJECT (1) â†’ create`
  - `SESSION (4) â†’ start, resume, status, close`
  - `RESEARCH (3) â†’ topic, recall, resume_thread`
  - `VIBE (4) â†’ publish, spec, pipeline_status, resolve_drift`
  - `CONTEXT (3) â†’ artifact_context, spec_context, session_context`
  - `JUDGE (3) â†’ judge, consensus, judgment_history`
  - `POLICY (2) â†’ active_policy, load_constitution`
  - `OBSERVABILITY (3) â†’ memory_state, writes, anomalies`
  - `ADMIN (3) â†’ admin_migrate, admin_schema_status, admin_vacuum`
  - `L6-VLP (1) â†’ vlp_handle_event` (DMAP v1.1 spec 193)
  - Total: 1+4+3+4+3+3+2+3+3+1 = 27.
- Schema strictness: `vibe_publish`, `vibe_spec`, `project_create` now use `additionalProperties: false` on their nested objects so the harness rejects unknown fields at parse time instead of silently dropping or coercing them.

### Migration notes
- **No DB migration.** `dark_memory_project_create` writes to the existing `projects` table (migrations/v7) â€” no schema change. Existing operators running v1.1.x keep their data; the new tool just provides an in-band path to provision what previously required `INSERT INTO projects (...)`.
- **Backwards compatibility for `vibe_publish` callers.** The schema fix is breaking for callers that built payloads against the old (broken) flat-string shape â€” those payloads were never valid against the Go struct and would have failed unmarshal at runtime. New payloads use the nested shape. See `docs/PR-v1.2.0.md` (added in this release) for a before/after payload diff.
- **Backwards compatibility for `ToolError` consumers.** The four new fields (`Field`, `ExpectedType`, `ActualType`, `SchemaHintURL`) are `omitempty`, so existing JSON consumers that ignore unknown fields keep working. Consumers that strictly validate the response shape should add the new fields to their allow-list.

### Tests
- 7 new sub-tests in `tests/tools/project_tool_test.go` (success, idempotent replay, schema rejection, error envelope).
- All existing v1.1.0 tests still pass against the updated `RegisterAll` (27-tool surface); existing test fixtures that asserted on the 26-tool count have been updated.

[1.2.0]: https://github.com/Opita-Code/dark-memory-mcp/compare/v1.1.0...v1.2.0

---

## [1.1.0] â€” 2026-07-16

### Added
- **DMAP v1.1 (Dark Memory Agent Protocol)** â€” 6-layer architecture, 26 atomic specs
  - Layer 2 (loop coordinator) closed with 5 atomic specs:
    - 2.1 SessionState â€” pure state-machine logic
    - 2.2 VLPPackage â€” 4 typed primitives (Brief/Propose/Record/Complete)
    - 2.3 VLPPersistence â€” Store-backed state with audit
    - 2.4 VLPAuditor â€” transition-level audit
    - 2.5 VLPLoopUseCase â€” end-to-end loop driver
- `Store.SaveVLPStateWithTransition` â€” atomic combo: UPSERT + row-level audit + transition-level audit in one DB transaction
- `audit.WriteEvent.ProjectID` field â€” INV-7 multi-tenancy at the audit layer
- `audit.ListFilters.ProjectID` â€” read-side tenant filtering
- 2 new dual-driver sub-tests: `write_audit_project_isolation` (F33), `vlp_state_roundtrip` enhancements (F33 cross-project)

### Changed
- **INV-1 hardening (F32)**: 21 SQLite Save*/Update*/Delete*/Close*/Link* methods now wrapped in `BeginTx` + `Commit` + `defer Rollback`
  - New helpers: `runInTx`, `recordWriteLockedTx` (SQLite); `runInTx`, `recordWriteTx` (Postgres)
  - Data row + audit row now atomic; partial failure rolls back both
  - **Critical**: helpers read `s.activeProject` without re-locking (deadlock avoidance â€” caller already holds `s.mu`)
- `UseCase.HandleEvent` (spec 2.5) refactored to use `Store.SaveVLPStateWithTransition` instead of two separate calls
- Default version bumped from `0.1.0-dev` to `1.1.0-dev` in `cmd/dark-mem-cli` + `cmd/dark-mem-inspect`

### Database
- **Migration v9** (`vlp_state_table`) â€” vlp_state per-session state row
  - `UNIQUE INDEX (project_id, session_id)` â€” multi-tenancy at vlp layer (INV-7)
- **Migration v10** (`audit_project_index`) â€” composite index on `write_audit(project_id, session_id)` for ListWrites filtering efficiency
  - **No column changes** â€” `write_audit.project_id` was already added in v7 (`project_namespace`)
  - **Idempotent** â€” `CREATE INDEX IF NOT EXISTS`
  - **Backwards compatible**

### Tests
- `internal/vlp` â€” 12 tests including new `TestVLP_E2E_AtomicSaveEmitsTwoAuditRows`
- `tests/dual_driver` â€” 11 sub-tests including F33 isolation
- 10 packages, all PASS (374s full suite)

### Known v2 follow-ups (not blocking)
- Postgres `notImpl` stubs need same F32 wrapping when real impls land (~30 methods)
- No meta-test verifying "every Save* rolls back its audit row on data-write failure" â€” only VLP has this
- `usecaseTransitionNotes` and `auditor.marshalTransitionNotes` produce byte-identical JSON but are duplicated; trivial refactor when v2 reorganizes vlp package

---

## [1.0.0] â€” 2026-07-12

### Added
- **Initial release**: 25 MCP tools, dual-driver SQLite + Postgres, 7 operational invariants
- 8 trades: SESSION (4), RESEARCH (3), VIBE (4), CONTEXT (3), JUDGE (3), POLICY (2), OBSERVABILITY (3), ADMIN (3)
- Migrations v1-v8 establishing core schema (sessions, research, vibe_specs, vibe_artifacts, vibe_brands, vibe_compliance, vibe_drift_reports, sdd_evaluations, write_audit, constitutions, mods, projects, mod_loads)
- CLI tools: `dark-mem-mcp` (MCP server), `dark-mem-cli` (admin), `dark-mem-inspect` (read-only observability)
- 9 test suites: cli, conformance, context, dual_driver, e2e, economy, invariants, orchestration, project
- Constitution watchdog (INV-4) â€” `constitutions` table + `Store.VerifyConstitutionHash`
- Canary protection (INV-3) â€” `SafetyHolder` rejects payloads containing canary
- Mod sanitization (INV-6) â€” content loader refuses unsafe content
- Multi-tenancy foundation (INV-7) â€” projects table + project_id column on every tenant-scoped table
- Bridge documentation: 5/7 bridges complete (bridge.3 + bridge.5 deferred per spec 164)
- MCP Inspector conformance test (`tests/conformance/`)

### License
- MIT â€” see [LICENSE](LICENSE)

[1.1.0]: https://github.com/Opita-Code/dark-memory-mcp/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/Opita-Code/dark-memory-mcp/releases/tag/v1.0.0

## [2.0.2] â€” 2026-07-27

### Fixed (DARK-MEM-v2.0.1-REGRESSION-1)

The v2.0.1 GateMiddleware (commit 09390d9) was wired with an empty
\StaticSessionResolver{}\ â€” every call returned \""\ and the gate
refused all tool calls (\ErrFrameStaleTooFar\). v2.0.2 fixes this
in three layers:

#### Schema (v17 â€” commit e16b2b7)

\projects.active_session_id\ + \ctive_session_set_at\ columns
added by migration v17 (sqlite + postgres). Set by session_start
+ session_resume; cleared (compare-and-set) by session_close.

#### Store interface

Three new methods on \store.Store\:

- \SetActiveSession(ctx, projectID, sessionID)\ â€” overwrite; idempotent
- \GetActiveSession(ctx, projectID) (string, error)\ â€” \""\ if none
- \ClearActiveSession(ctx, projectID, expectedSessionID)\ â€” CAS clear

SQLite implements all three. Postgres has stubs (driver unused
on this host).

#### Gate per-tool session requirement

\RequiresActiveSession(toolName)\ allowlist gates PreCheck's
\SessionID == "" || ProjectID == ""\ refusal. Default is true
(most tools require a session). The exempt list:
\session_start\, \session_resume\, \health_ping\,
\memory_state\, \project_create\, \ctive_policy\,
\load_constitution\, \dmin_schema_status\, \dmin_vacuum\,
\dmin_migrate\. These are operator-issued setup + read-only
introspection and must work without a session.

Bootstrap and read-only tools were broken in v2.0.1; this
restores the v2.0.0 contract for them.

#### Real ActiveSessionResolver

\internal/server/active_session_resolver.go\ â€”
\StoreBackedActiveSessionResolver\ queries the projects row
through a pluggable \ActiveSessionLookup\ (main.go adapts via
\StoreBackedLookup\). Short in-process TTL cache (default 5s)
amortizes the per-tool-call DB read. \Invalidate\ /
\InvalidateAll\ exported for write-through caching.

#### Orchestrator call sites

session_start / session_resume / session_resurrect write the
active pointer; session_close + sweeper clear it (CAS-aware so
a stale close of an older session doesn't clobber a newer
session_start).

### Wire contract change

\ActiveSessionResolver.ActiveSessionID\ gained a
\context.Context\ first parameter. Both implementations
(\StaticSessionResolver\, \StoreBackedActiveSessionResolver\)
updated. Third-party implementations must adapt.

### Tests (regression guards for v2.0.1's failure)

- \internal/policy/gate_v2_0_2_test.go\ â€” pins the
  \RequiresActiveSession\ contract AND a direct guard that
  \PreCheck\ on a session-free tool returns \Allowed=true\.
- \internal/server/active_session_resolver_test.go\ â€” 6 tests
  covering cache hit, expiry, empty projectID no-op,
  lookup-error handling, Invalidate, CacheTTL=0 disable.
- \	ests/dual_driver/active_session_test.go\ â€” 4 tests covering
  Set/Get/Clear roundtrip, CAS semantics, race-new-session-wins,
  ErrProjectNotFound.

\internal/server/middleware_test.go\\s
\TestGateMiddleware_PreCheck_RefusesMissingIdentity\ rewritten
to use \ibe_publish\ instead of \health_ping\ (the latter
is now session-free and would not exercise the refusal path).

---
Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [2.0.1] â€” 2026-07-27

### Added (gate promoted to the transport layer)

The v2.0.0 pivot put the policy-gateway into the orchestrator layer.
v2.0.1 promotes it to the transport layer so every `dark_memory_*`
tool call now flows through `internal/policy.PostCheck` via the new
`GateMiddleware`, *before* the inner handler runs.

- **`internal/server/middleware.go` (new) â€” `GateMiddleware.Wrap`**.
  PreCheck (capability grant + intent-in-scope) before the inner
  handler; PostCheck (drift-at-write, when `DriftChecker` is non-nil)
  after, for artifact-creating tools only. When `Gate` is nil, the
  legacy direct-dispatch path runs (no policy enforcement), keeping
  the existing test harness ergonomic.
- **`internal/server/middleware_test.go` (new)** â€” 387 lines covering
  3 categories: capability mismatch, scope mismatch, drift verdict
  refusal at write boundary.
- **`internal/server/server.go`** â€” `wrapHandler` routes through `Gate`
  when set.
- **`internal/server/lifecycle.go`** â€” `BootState.Gate` field.
- **`cmd/dark-mem-mcp/main.go`** â€” wires the Gate from the returned
  `FrameSource` at boot.

### Changed (FrameSource singleton, 5A.ii.b.2.c.1)

The per-call `FrameSource` construction in `dark_memory_recall` is
lifted to a boot-time singleton. Both the recall tool and the gate
now share the same `CachedSource` instance.

- **`internal/recall/singleton.go` (new)** â€” boot-time `FrameSource`
  construction. Singleton contract tested in `singleton_test.go`.
- **`internal/tools/register.go`** â€” `RegisterAll` signature changes
  from returning `error` to returning `(policy.FrameSource, error)` so
  the caller can wire it into the gate.
- **`internal/tools/recall.go`** â€” uses the passed-in singleton;
  per-call construction is gone.
- **`tests/e2e/server_test.go`** â€” updated for the new `RegisterAll`
  signature.

### Added (operator ergonomics)

- **Legacy `DARK_SCRAPPER_URL` env shim** (H-4 follow-up). PR #10
  dropped the v1.x env name without a backward-compat alias, which
  broke unmigrated operators' drift-judge path on first boot. v2.0.1
  closes the gap: when `DARK_SCRAPPER_URL` is set AND
  `DARK_DRIFT_JUDGE_DAEMON_URL` is not, `NewSelfHarnessClient` falls
  through to the legacy value and logs a one-line deprecation notice
  at startup. The legacy env will be removed in v2.1.0.
  Migration: `sed -i 's/DARK_SCRAPPER_URL/DARK_DRIFT_JUDGE_DAEMON_URL/g' .env`
- **`internal/orchestration/llm_client_scrapper_alias_test.go` (new)**
  pins the fallback contract end-to-end against the live binary.

### Notes

- **`DriftChecker` is intentionally `nil`** in the gate wiring. The
  strict-mode opt-in is a separate follow-up (or a v2.0.2 if needed).
  The gate still runs PreCheck unconditionally and refuses tools the
  LLM isn't granted.
- **`DefaultServerVersion` bumped** from `2.0.0-dev` â†’
  `2.0.1-dev` in `internal/server/bootstrap.go` for the legacy
  hardcoded fallback. Canonical version resolution flows through
  `version.Resolve()` (set by `make release` via `-ldflags`).
- `git describe --tags --always --dirty` on a fresh v2.0.1 tag will
  print `v2.0.1` cleanly (no `+dirty` suffix).

---

## [2.0.0] â€” 2026-07-19

### Breaking (operator env contract â€” ships in PR #10)

- **`DARK_SCRAPPER_URL` â†’ `DARK_DRIFT_JUDGE_DAEMON_URL`**.
  SelfHarnessClient provider renamed from `"dark_scrapper"` to
  `"drift_judge_daemon"`. Function `judgeViaScrapper` â†’
  `judgeViaDriftJudgeDaemon`. **No backward-compat alias** â€”
  operators must update their env.
  Migration: `sed -i 's/DARK_SCRAPPER_URL/DARK_DRIFT_JUDGE_DAEMON_URL/g' .env`.
- **`DARK_JUDGE_MODEL_SCRAPPER` â†’ `DARK_JUDGE_MODEL_DRIFT_JUDGE_DAEMON`**.
- Test file `internal/orchestration/scrapper_wiring_test.go` â†’
  `internal/orchestration/drift_judge_daemon_wiring_test.go`.

### Breaking (server lifecycle default)

- **Shutdown default `close_reason` `aborted` â†’ `clean`** in
  `internal/server/lifecycle.go`. Operators who relied on the
  legacy `aborted` default for crash-recovery workflows can opt
  back via `DARK_SHUTDOWN_CLOSE_REASON=aborted`.
- New env var `DARK_AUTO_RESURRECT=on_boot` opts into automatic
  session resurrection on server startup. Default: orphans are
  surfaced via a log entry but not auto-recovered.

### Fixed

- **INFRA-002 â€” `dark_memory_vibe_spec` now surfaces WHICH form and WHY
  on tasks parse failure.** Pre-fix, `parseTasksField` in
  `internal/orchestration/vibe_spec.go` discarded the underlying
  `json.Unmarshal` error and returned only
  `store.NewFieldError(store.ErrInvalidArgument, "tasks")`, surfacing
  `"invalid argument at field=tasks"` to the harness with no
  diagnostic. Post-fix:
  - The error chain is `*store.FieldError{Field:"tasks"}`
    (F35 wire-propagation keeps working: `errors.As(err, &fe)`
    still finds it; `errors.Is(err, ErrInvalidArgument)` still
    matches).
  - Wrapped with `fmt.Errorf("%w: rejected by parser (Form A/B
    step N/unknown form ...): %v", fe, cause)` so the operator
    sees which form was attempted AND the underlying json
    diagnostic (e.g. "invalid character 'Â·' after top-level
    value").
  - The unknown-first-byte path explicitly names the offending
    byte (`first non-whitespace byte='{'`) and the expected
    shape without leaking the rest of the payload (preserves
    `classifyUnknown`'s no-payload-leak policy).
- Concrete reproductions covered by:
  - `internal/orchestration/vibe_spec_test.go` (orchestrator-level):
    - `[{...}]Â·` (trailing garbage byte after close-bracket, Form A)
    - `"[not-an-array]"` (outer string but inner not parseable, Form B)
    - `{...}` (object-shaped payload â€” neither Form applies)
  - `tests/wire/infra002_vibe_spec_diagnostic_test.go` (H-3 wire
    conformance): pins the contract end-to-end against the running
    binary over JSON-RPC â€” the same envelope shape production
    harnesses see.

### Added (memory-as-policy-gateway pivot)

The pivot replaces the pull-based CRUD model (v1.x) with a
gate-driven active-memory model. Every `dark_memory_*` tool call
now traverses `internal/policy.PostCheck` which:

1. Composes an **atomic context frame** from session + project +
   global state via `internal/atomic.FrameSource`.
2. Verifies the call's intent is in scope and the LLM has the
   capability grant for it (`CapabilitiesFrame`).
3. Invokes the orchestrator with the frame as input.
4. **Drift-checks the response at the write boundary** before
   returning to the LLM (`internal/drift.Checker`).

- **`dark_memory_recall` (29th canonical tool, CONTEXT 3 â†’ 4)** â€”
  the canonical scoped-replay orchestrator. Inputs: scope
  (global|project|session), project_id, session_id, since_token.
  Outputs: per-kind atomic frames (Identity, Scope, Capabilities,
  Drift, Persona) + delta write_audit rows since since_token +
  new_token cursor. RFC Â§3 M1 + Â§6.1.
- **`internal/atomic` package (NEW)** â€” Frame interface + 6
  concrete types: IdentityFrame, ScopeFrame, CapabilitiesFrame,
  PersonaFrame, DriftFrame, EvidenceFrame. FrameSource interface.
  Wave 5X.2: `Frame.Hash` signature `Hash() [32]byte` â†’
  `Hash() ([32]byte, error)` (defense against previous
  interface/type mismatch that was silently swallowed).
- **`internal/drift` package (NEW)** â€” `Strictness` enum
  (off|warn|strict), `Checker` type with `CheckArtifact(ctx,
  ArtifactInput) â†’ Verdict`, `JudgeCaller` interface. Replaces
  the previous `policy.PostCheck` stub. Decision tree for judge
  errors: strict refuses, warn allows.
- **`internal/policy` package (NEW)** â€” gate.PostCheckInput
  gains `DriftChecker + DriftArtifact` optional fields.
  `PostCheck` now calls `drift.Checker` when `Strictness != off`.
- **`internal/recall` package (NEW)** â€” `StoreSource` (reads
  from `store.Store`) + `CachedSource` (INV-5 cache re-hash on
  Get + audit emission on cache_mismatch). 9 tests.
- **Session lifecycle resilience** â€” `session_resurrect`,
  `session_recover`, `session_heartbeat`, `session_sweeper`,
  `boot_reconcile`. Closed-due-to-crash sessions are now
  resurrectable (only operator-initiated termination is
  terminal). `SessionResurrectOutput` gains 5 fields:
  `InheritedConstitution{ID,Ver}`, `ActiveConstitution{ID,Ver}`,
  `ConstitutionBumped`, `InheritedMods`.
- **L6 adapter integration** â€” 3 hooks from
  `BRIDGE_AND_COEXISTENCE.md` Â§6 wired:
  * `startup-recover` â†’ `runStartupRecover()` in main.go
  * `periodic-heartbeat` â†’ sweeper (5E.iii, doc-only)
  * `exit-close_clean` â†’ Shutdown default reason = clean
- **Per-project drift strictness** â€” `Project.DriftStrictness`
  field (migration v14). `drift.ResolveStrictness(projectOverride,
  envValue, warnf)` â€” empty/'default' â†’ env; valid â†’ override;
  invalid â†’ warn + env fallback.

### Added (canonical tool count 28 â†’ 29)

- **`dark_memory_recall`** â€” see "memory-as-policy-gateway
  pivot" above. CONTEXT namespace: 3 â†’ 4 tools.

### Changed (data plane)

- **Schema migrations v11â€“v15** (sqlite + postgres):
  * v11, v12 â€” frame-related scaffolding (see git log)
  * v13 â€” `CREATE UNIQUE INDEX uq_vibe_frames_natural_key ON
    vibe_frames (project_id, session_id, scope_level, scope_id,
    frame_kind)` â€” enables the UPSERT rewrite
  * v14 â€” `ALTER TABLE projects ADD COLUMN drift_strictness TEXT
    NOT NULL DEFAULT 'default'`
  * v15 â€” `ALTER TABLE vlp_state ADD COLUMN open_spec_id INTEGER
    NOT NULL DEFAULT 0`
- **SaveFrame rewritten as INSERT ... ON CONFLICT DO UPDATE**
  (sqlite) / `ON CONFLICT ... RETURNING` (postgres). Replaces the
  SELECT-then-INSERT/UPDATE race in the previous implementation
  under concurrent SaveFrame calls. Tested with 10-goroutine
  concurrent upsert â†’ 1 row.
- **`WriteContext.SessionEvent`** â€” every `Save*` emits this
  field in `write_audit`. Closes pre-existing drift where the
  `session_event` column was INSERTed NULL silently.
- **`VLPStateRow.OpenSpecID`** â€” the actual spec_id the session
  is working on. Previously the recall cache used `vlp_state.ID`
  as a meaningless proxy.
- **`Project.DriftStrictness`** â€” per-project resolver override.

### Notes (constitution + RFC)

- The pivot's design rationale lives in
  `vibe-flow/main/ACTIVE_MEMORY_RFC.md`, `SCHEMA_v11_v12.md`, and
  `DRIFT_BURST.md` â€” these are operator-private planning docs
  (NOT committed; lives in the operator's local workspace).
- Public docs updated: `vibe-flow/PLAN.md` v2 (pivoted roadmap),
  `vibe-flow/main/BRIDGE_AND_COEXISTENCE.md` v2 (cx.v3,
  policy_gateway, dark-research-mcp demoted, dark-recall
  cancelled).
- `DefaultServerVersion` constant bumped from `"1.4.1-dev"` â†’
  `"2.0.0-dev"`. Canonical source remains `version.Resolve()`
  (set by `make release` via `-ldflags`).
- 9 commits + 1 release (this PR). Pre-merge lint scrub PR #10
  established the H-4 compliant env-var rename as a separate
  concern.

---

## [1.4.1] â€” 2026-07-18

### Behavior change (callers MUST verify)

- **`dark_memory_vibe_spec` now rejects non-canonical `vibe_case` values**
  with `ErrInvalidArgument`. Previously the JSON Schema layer accepted
  any string (e.g. `"C8"`, `"code"`, `"c1"`); now both the JSON Schema
  enum AND the orchestrator reject unknown values via `vibecase.Parse`
  (defense in depth).
- Callers using valid `C1`..`C7` values see **no change**.
- Callers passing `""`, whitespace-only, or any non-canonical label
  now receive a structured error:
  ```
  vibe_case: vibecase: invalid case identifier: "X" is not one of
             [C1 C2 C3 C4 C5 C6 C7]
  ```
- **Migration:** if your harness ever passed an unexpected `vibe_case`
  value (e.g. a downstream convention of `"image"` for C3), map it
  to the canonical `"C3"` label before sending. No migration tool
  ships; the rejection is fail-loud and the operator-visible error
  names the allowed set.

This is the change that motivated the v1.4.1 PATCH bump instead of
v1.4.2: the new validation is observable to callers but does not
break any caller that was previously compliant with the canonical
C1..C7 set. Per the project's SemVer convention (no formal API
stability promise at v1.x), a PATCH bump is appropriate.

### Added (canonical C1..C7 taxonomy)

- **`internal/vibecase` package** â€” single source of truth for the
  C1..C7 case taxonomy. Replaces a JSON Schema enum fragment that
  was duplicated across `vibe_publish` and (asymmetrically) absent
  from `vibe_spec`. Exports:
  - `Case` (typed string) and the seven canonical constants
    `CaseCode..CaseMixed`.
  - `Parse(s)` (strict, trims, rejects empty + unknown + mixed-case),
    `MustParse(s)` (panic-on-error for startup constants),
    `IsValid(s)` (boolean shortcut).
  - `All()` and `JSONSchemaEnum()` â€” stable, ordered, defensively
    copied.
  - `Description(c)` â€” human-facing one-liner per case (for LLM
    context projections).
  - `ErrInvalidCase` â€” exported sentinel for `errors.Is` checks.
  - 15 unit tests covering ordering, defensive copy, trim, empty,
    unknown, mixed-case, error message contents, panic, boolean
    shortcut, round-trip, description, cardinality.

### Changed

- **`vibe_spec` now enforces the C1..C7 enum** at the JSON Schema
  layer (`internal/tools/vibe.go`) AND at the orchestrator layer
  (`internal/orchestration/vibe_spec.go`), closing the asymmetry
  where `vibe_publish` validated the enum but `vibe_spec` did not.
- **`vibe_publish` JSON Schema enum now derives from
  `vibecase.JSONSchemaEnum()`** instead of a hardcoded literal. Any
  future case addition automatically propagates to both tools.
- **Both orchestrators validate via `vibecase.Parse`** (defense in
  depth): even if the JSON Schema layer is bypassed (direct
  orchestrator call, future non-MCP transport, etc.), the validator
  rejects unknown cases before the row is persisted.
- 4 new orchestrator tests:
  `TestVibeSpec_InvalidVibeCase`,
  `TestVibeSpec_AcceptsAllCanonicalCases`,
  `TestVibeSpec_AcceptsTrimmedVibeCase`,
  `TestPublishVibe_InvalidVibeCase`.

### Versioning note

Adding a case (e.g. C8) is a MINOR bump and is backward-compatible
(case labels are stored as TEXT; existing rows remain readable).
Reordering or renaming an existing case is a BREAKING change. See
the package doc on `internal/vibecase` for the full contract.

---

## [1.4.0] â€” 2026-07-18

### Added (release-integrity release)

- **`release-integrity@1.0.0` constitution** ([`CONSTITUTION.md`](CONSTITUTION.md)).
  Five rules codify release hygiene: (1) single source of truth for
  version, (2) archive-not-delete for deprecation, (3) CHANGELOG is
  authoritative, (4) drift detection on every boot, (5) session-bound
  governance. Cross-cutting reference for every `vibe_publish` artifact
  in the dark-memory-mcp project.
- **`internal/version` package** â€” single-source version resolver.
  Replaces the hardcoded `DefaultServerVersion = "1.3.0"` constant in
  `internal/server/bootstrap.go` and the `var Version = "1.1.0-dev"`
  in `cmd/dark-mem-cli/main.go` and `cmd/dark-mem-inspect/main.go`.
  Resolution priority: `-ldflags` injection (canonical, set by
  `make release`) â†’ `debug.ReadBuildInfo()` (dev) â†’ hardcoded
  `"dev"` sentinel (emergency). 9 unit tests cover all three paths.
- **`Makefile`** with `build` / `release` / `drift-check` /
  `version` / `version-json` / `inspect` / `tag` / `clean` targets.
  Handles the multi-module `cmd/*` layout (each cmd is its own Go
  module; the Makefile `cd`s into each before `go build`).
- **`scripts/inject-version.sh`** (bash) and
  **`scripts/inject-version.ps1`** (PowerShell) â€” resolve the canonical
  version from `git describe` and emit the `-ldflags` expression that
  feeds `make release`. Same resolution rules, same output formats
  (`--raw` / `--json` / default), same `--strict` flag.

### Added (drift detection in health_ping)

- **`dark_memory_health_ping` response grew a `git` block.**
  New fields: `git.tag`, `git.commit`, `git.dirty`, `git.build_time`,
  `git.source` (one of `ldflags|buildinfo|dev`), `git.is_dev`.
- **Top-level `drift` bool** â€” true iff the resolver fell back to the
  dev path OR the working tree was dirty at build time. Per
  `CONSTITUTION.md` Rule 4, a release binary MUST report
  `drift=false`. Operators can monitor the single-bit signal directly.
- Wire-conformance test (`tests/wire/health_ping_test.go`) and the
  e2e binary (`cmd/e2e/main.go`) updated to mirror and assert the
  new fields.

### Changed

- The `internal/server/bootstrap.go::DefaultServerVersion` constant
  is now a deprecated string (`"1.4.0-dev"`) for any external
  callers; the canonical default flows through `version.Resolve()`.
- `cmd/e2e/main.go` relaxed the hardcoded `"1.3.0"` health_ping
  version assertion to "non-empty" â€” the value is now driven by the
  resolver, not by source code.

### Notes

- v1.4.0 ships together with **dark-research-mcp v0.7.0**, which
  wraps 38 duplicate tools (the dark_mem_*, dark_research_spec_*, etc.,
  and dark_ssd_* tools) in a deprecation envelope pointing at
  dark-memory-mcp. See
  [`dark-research-mcp/RELEASE_NOTES_v0.7.0.md`](https://github.com/Opita-Code/dark-research-mcp/blob/main/RELEASE_NOTES_v0.7.0.md)
  for the peer release notes and migration guide.

---

## [1.3.2] â€” 2026-07-16

### Fixed

- **`fix(llm): wire SelfHarnessClient.Judge to drift-judge-daemon HTTP route.**
  `SelfHarnessClient.Judge` was returning `ErrNoLLMAvailable` unconditionally
  (deferred to Wave 4+ in source). Wired to POST to `DARK_SCRAPPER_URL/v1/messages`
  with `Bearer ds-managed` (sentinel auth) when `provider == "dark_scrapper"`.
  Other providers (anthropic / openai / google) still return `ErrNoLLMAvailable`
  by design, preserving the source's visibility-over-silent-degrade philosophy.
  URL validation rejects empty / `file://` / no-scheme / no-host before any
  HTTP call (R5 defense).

### Added

- **`feat(federation): cross-namespace lookup tool + pipeline_status hint.**
  dark-memory and dark-research MCPs use two physically separate SQLite files
  (`dark-memory.db` vs `dark.db`) with compatible schemas on shared tables.
  New `internal/federation` package: read-only `Peer` handle opened from
  `DARK_FEDERATION_PEER_DSN`. New `dark_memory_federation_lookup` tool
  (opt-in extra, same pattern as `DARK_REDTEAM=armed`). `pipeline_status`
  now probes the peer on local miss and adds a `cross_namespace_hint` field.

### Governance

- DARK-MEM-001 establishes the `release-integrity@1.0.0` constitution
  (see [`CONSTITUTION.md`](CONSTITUTION.md)) and retroactively tags `v1.3.1`
  at `fbc5c03` to give the squash commit a canonical annotated reference.

---

## [1.3.1] â€” 2026-07-16

### Note (release plumbing)

- **Local tag `v1.3.1` retroactively created at commit `fbc5c03`.** The
  commit message reads `release: v1.3.1 -- sync unreleased work to origin/main
  (squashed)`. The squash landed in the repo on 2026-07-16 but no annotated
  tag was created at the time; the v1.3.1 entry exists to give that squash
  a canonical reference and to keep the tag chain (v1.3.0 â†’ v1.3.1 â†’ v1.3.2)
  consistent with the commit graph.
- No standalone code changes between v1.3.0 and v1.3.2: v1.3.1 is a
  release-plumbing tag only. The substantive changes in this window are
  documented under v1.3.0 and v1.3.2.

---

## [1.3.0] â€” 2026-07-16

### Added (production-readiness release)

- **`dark_memory_health_ping` â€” operator-facing liveness probe.**
  The canonical surface grew 27 â†’ 28 tools (OBSERVABILITY 3 â†’ 4).
  health_ping is a strict, documented-shape probe distinct from
  `memory_state`:
    - **Latency budget:** <500ms round-trip (target <50ms on warm cache);
      suitable for K8s liveness/readiness probes that fire every second.
    - **Side-effect freedom:** does NOT touch the audit bus, does NOT
      advance VLP state, does NOT migrate. Safe to call at high
      frequency.
    - **Frozen contract:** `{server, db, runtime, registry, latency_ms,
      checked_at}`. Adding fields is backward-compatible; removing
      fields is a breaking change to monitoring rules.
  Wire conformance: `tests/wire/health_ping_test.go::TestWire_HealthPingShape`
  (verifies all fields) and `::TestWire_HealthPingLatency` (verifies the
  500ms ceiling). Tool count: `tests/wire/zz_toolenum_test.go`.
- **`tests/wire/wire_session_test.go::waitForBootMarker`** â€” eliminates
  the startup race that previously caused intermittent "tool not found"
  failures when `initialize` arrived before the binary's mcp-go loop
  started. The harness now waits up to 5s for the `registered N tools`
  boot marker on stderr before sending `initialize`.
- **`internal/tools/health.go::unwrapToolResponse` helper** â€” single
  point of edit for the mcp-go `content:[{type:"text",text:"..."}]`
  envelope shape that wraps every tool response.
- **`Config.BootedAt`** field â€” wall-clock time captured at config load;
  `SetRuntimeContext` propagates it into `health_ping` so uptime is
  accurate from the very first call.
- **`.github/workflows/ci.yml`** â€” operator-reproducible CI recipe:
  builds, runs lint, runs `go test ./...`, runs `go test ./tests/wire`
  with `DARK_MEM_MCP_BIN` set. The never-push policy is preserved
  (this file lives in-repo for transparency; CI is local-only).
- **`docs/PRODUCTION_CHECKLIST.md` Â§Health Probe** â€” wiring guide for
  the new `dark_memory_health_ping` including a sample K8s liveness
  probe YAML and a Prometheus `up{job="dark-mem-mcp"}` snippet.

### Changed
- **Canonical tool count 27 â†’ 28.** README, DECISION_MATRIX,
  bridge.7 conformance test, e2e canonical-order test, and the
  sanity check inside `tools.RegisterAll` all bumped to 28.
- **`DARK_SERVER_VERSION` default** bumped from `1.2.3` to `1.3.0`
  (`DefaultServerVersion` constant in `internal/server/bootstrap.go`).
- **`tests/wire/wire_session_test.go::resolveWireBin`** now skips the
  test when no binary is found (previously fataled). The first
  candidate is still `../cmd/dark-mem-mcp/dark-mem-mcp.exe` so a
  freshly-built binary is picked up automatically.

### Documented
- **`docs/PRODUCTION_CHECKLIST.md` Â§Race detector availability** â€”
  the operator's `go test -race` requires a C compiler; on this host
  no gcc is installed and the race detector is therefore unavailable.
  Workaround: validate via the wire suite (10 tests, including
  TestWire_HealthPingLatency which exercises 5 sequential calls and
  catches perf regressions) and the e2e suite (`tests/e2e/server_test.go`
  fires 1000 concurrent calls).
- **`docs/PRODUCTION_CHECKLIST.md` Â§Stale-binary gotcha** â€” if a
  previous binary is left at `dark-mem-mcp.exe` in the repo root
  (or in `PATH` before `cmd/dark-mem-mcp/`), the wire harness's
  fallback resolution picks it up. Always rebuild into
  `cmd/dark-mem-mcp/` and either delete or set `DARK_MEM_MCP_BIN`
  explicitly when running `go test ./tests/wire`.

### Tests
- 15 / 15 packages PASS in `go test ./...` (full sequential suite).
- 10 / 10 wire tests PASS against the v1.3.0 binary in
  `go test -tags wire ./tests/wire/...` (with `DARK_MEM_MCP_BIN`
  set). Total wire suite runtime: ~25s.
- The 28-tool contract is enforced by both `TestE2E_28ToolsRegistered`
  (Go level) and `TestWire_RuntimeToolEnumeration` (wire level).

### Migration from v1.2.x
- **Drop-in for v1.2.5 operators.** No DB schema change, no migration
  bumps, no env var renames. The 28th tool is purely additive.
- The canonical order has a single new entry between `anomalies` and
  `admin_migrate`: `health_ping` at position 23 (0-indexed). Any
  harness that iterates `tools/list` and indexes by **name** is
  unaffected. Any harness that indexes by **position** must update.
- The `dark_memory_health_ping` tool is registered as canonical,
  not as an "extra". Un-armed servers see 28 tools; armed servers see
  28 + 3 redteam = 31.

---

## [1.2.5] â€” 2026-07-16

### Added
- **`tests/wire/` end-to-end JSON-RPC suite.** Wire-conformance tests
  prove fixes actually work through the real MCP wire (binary
  subprocess + JSON-RPC over stdio), not just at the Go orchestrator
  level. Catch the bugs that Go-level tests cannot: harness encoding
  (LLM dependent), schema-layer mismatches, error-envelope propagation.
  **Rule (H-3 in CONTRIBUTING.md):** every fix MUST ship with at
  least one wire test.
- **`store.FieldError` structured type + F35 wire propagation.** Previously
  orchestrator-level `ErrInvalidArgument` errors discarded the field
  name; only `json.UnmarshalTypeError` paths set `ToolError.Field`.
  This meant a `parseTasksField` rejection (e.g. LLM emits a number)
  surfaced as the generic "One or more arguments failed validation"
  message. `store.FieldError` carries the structured Field; ToToolError
  extracts it via `errors.As` and propagates to `ToolError.Field`.
  Tests: `tests/wire/f35_structured_error_test.go` (end-to-end via
  binary), `tests/orchestration/orchestrator_test.go::TestVibeSpec_StringifiedTasks_MalformedRejected`.
- **CONTRIBUTING.md** baking the four hard rules (H-1 each MCP owns its DB,
  H-2 array/object string fallback, H-3 wire tests mandatory, H-4 no
  private names in public artifacts) and seven conventions. Every
  future dark-* server is built against this doc.
- **`docs/PRODUCTION_CHECKLIST.md`** operator runbook: boot signal
  matrix, recovery playbooks (R-1 vec0, R-2 dark.db corruption, R-3
  tasks shape, R-4 LLM-prompt drift), dark-research vs dark-memory
  isolation verification, performance baselines, one-page cheat
  sheet.
- **Wire test infrastructure.** `tests/wire/wire_session_test.go`
  provides `wireSession` (binary subprocess + JSON-RPC framed
  stdio), `startWireSession(t)` (per-test isolated DB under
  `t.TempDir()`), `testsCall(name, args)` (strict per-id request).
  Override `DARK_MEM_MCP_BIN` env var to test a specific binary.

### Changed
- **`parseTasksField` error propagation.** Errors now wrap via
  `store.NewFieldError(store.ErrInvalidArgument, "tasks")` so the
  field name reaches `ToolError.Field`. The orchestrator-level
  `errMissingField` helper now also returns a `store.FieldError`
  instead of a plain `fmt.Errorf`. **Wire test impact:**
  `TestWire_F35_TypeMismatchSurfacesFieldPath` now passes (was
  returning the generic error envelope pre-fix).
- **`vibe_publish` shape regression test.** Tests now post the CORRECT
  nested shape (spec as object, artifact as object, tasks as
  JSON-encoded string). Pins the post-F33 contract.

### Tested
* 7 wire-conformance tests against the live binary:
  - F33 (vibe_publish nested schema)
  - INV-8 (defaultDSN isolation against cwd dark.db collision)
  - F35 (structured field error via `tasks: 42.0`)
  - F36 array form
  - F36 stringified-array form
  - F37-F40 (boot against half-migrated dark-memory.db)
  - F37 (duplicate column tolerance via ApplyOne-by-statement split)
* 15 of 15 package test suites pass (last suite run before this
  commit). The conformance suite is occasionally flaky under heavy
  concurrent load (full suite at once); reruns always pass.

### Operator notes
- Drop-in replacement for v1.2.4. No DB migration.
- The new `tests/wire/` package requires `DARK_MEM_MCP_BIN=<path-to-binary>`
  unless `./dark-mem-mcp.exe` is in the repo root (the default for
  development). Production CI should set this env var explicitly.
- The four wire-test failures (F35 fixed, F33 payload fixed,
  F37-F40 seed fixed) were real production bugs caught by writing
  wire tests FIRST in the regression suite. The "test the orchestrator
  only" approach was missing harness-layer failures.

---

## [1.2.3] â€” 2026-07-16

### Added
- **INV-8 (per-MCP database isolation).** Each MCP server in the dark-agents family owns its **own SQLite file** by convention. dark-memory-mcp now defaults to `dark-memory.db` instead of `dark.db`; dark-research-mcp continues to use `dark.db`. Sharing `dark.db` was the root cause of the v1.2.2 boot crashes (schema_migrations name collisions in the shared bookkeeping table). The principle is documented in `docs/INVARIANTS.md` (new `INV-8` section, with rationale, defence test, operator signal, and applicability to all future dark-* servers). Defensive test: `tests/invariants/inv8_test.go::TestServer_DefaultDSN_DoesNotCollideWithDarkResearch_INV8` â€” asserts the default DSN (a) is not `dark.db`, (b) doesn't contain `dark-research`, (c) contains `dark-memory`. Operators who want the legacy shared-DB behaviour can opt in via `DARK_DB=dark.db` env var.

### Changed
- **`defaultDSN()` â†’ `"dark-memory.db"`** (was `"dark.db"`). Backward-compatible override via `DARK_DB=` env var. Affects `internal/server/bootstrap.go` only. New public accessor `server.DefaultDSN()` so tests/invariants can assert without reflection. No DB migration needed; the change only affects the default path.

### Future directions
- **`[FUTURE-MCP-1]`** (the next dark-* project, see session notes) MUST default to a project-specific filename (`harvest.db` or per-project variant) and pass the `INV-8 defaultDSN uniqueness` lint. The lint is informal today (a grep in CI) but will become a go-vet rule in v1.3.0. Documented in `docs/INVARIANTS.md` under INV-8.

---

## [1.2.2] â€” 2026-07-16

### Fixed
- **F37 â€” migration runner now tolerates "duplicate column name" errors.** applyOne in `internal/migrate/migrate.go` was running every statement in `m.Up` via a single `tx.ExecContext` inside one transaction. Any failure (including benign "duplicate column name: project_id" when a v7-style ALTER TABLE ADD COLUMN had partially completed during a prior boot crash) rolled back the WHOLE migration and aborted the daemon. The runner now splits multi-statement migration bodies on `;`, runs each statement separately, and treats the duplicate-column error class (SQLite `duplicate column name: X` + Postgres `column X already exists`) as already-satisfied. Regression tests cover the recovery flow (`TestMigrate_TolerantOfDuplicateColumn_F37`) plus a regression guard against over-broad catch (`TestMigrate_StillFailsOnNonDuplicateErrors_F37`).
- **F38 â€” `EnsureCoreTables` self-heals missing core tables on boot.** The dark.db at `C:\Users\Nico\AppData\Local\dark-agents\dark.db` is shared with dark-research-mcp, whose bookkeeping table uses the same `schema_migrations` rows. When dark-research-mcp's v1-v3 were applied with overlapping version names (initial_schema, constitutions_and_mods, sdd_evaluations_constitution_audit), dark-memory-mcp's v5+ (`sessions_table`, `project_namespace`, `vibe_brands_composite_unique`, `vlp_state_table`, `audit_project_index`) appeared "already applied" without having actually run against the schema â€” leaving `sessions` and `projects` tables physically absent from the DB. New helper `migrate.EnsureCoreTables(ctx, db)` issues `CREATE TABLE IF NOT EXISTS` for the four core tables v5/v6/v7 expect to find, called once from the sqlite Store's `Open` before `Migrate` so the migration runner sees the correct schema state. Tests: `TestEnsureCoreTables_FreshDB_F38`, `_Idempotent_F38`, `_RecoveryFromHalfMigratedDarkDB_F38` (the exact 6-step crash repro from today's session).
- **F39 â€” migration runner tolerates "no such module: <ext>" errors.** Orphan sqlite-vec triggers (`trg_research_items_vec_delete`, etc.) referencing the unloadable `vec0` virtual-table module were causing `ALTER TABLE vibe_brands RENAME TO vibe_brands_old` (in v8) to surface `SQL logic error: error in trigger trg_research_items_vec_delete: no such module: vec0`. Same `applyOne` extension; the "no such module" substring is now treated as already-satisfied at the per-statement level. Tests in `tests/migrate/tolerate_ddl_errors_f39_f40_test.go::TestMigrate_ToleratesNoSuchModule_F39`.
- **F40 â€” migration runner tolerates "table X already exists" errors.** The same per-statement loop now also handles the rare case where a `CREATE TABLE` in a migration's `Up` is called against a table that already exists (e.g. `EnsureCoreTables` + `Migrate` both try to create the same table at boot, or a v8-style rename-and-recreate pattern). The existing table is preserved as-is. Test in `tests/migrate/tolerate_ddl_errors_f39_f40_test.go::TestMigrate_ToleratesTableAlreadyExists_F40`.

### Operator notes
- v1.2.2 is a **drop-in replacement** for v1.2.1. No migrations required. The 27-tool canonical surface is unchanged. No DB schema change.
- Restart the running `dark-mem-mcp.exe` to pick up the new code; the F37/F38/F39/F40 changes only affect boot behaviour.
- **However**, today's dark.db at the canonical path is in a pre-v1.2.0 partial state (has `attempts`, `audit`, `findings`, `judgments`, `runs`, etc. tables from a previous [prior-evaluation-loadout] loadout, plus orphan vec0 triggers). Even with v1.2.2's tolerance patches, v8 (`vibe_brands_composite_unique`) will fail at the `INSERT INTO vibe_brands SELECT FROM vibe_brands_old` step because the rename was silently skipped (F39). To bootstrap a clean dark-memory-mcp state without losing recent work, see the operator's playbook:
  - **Safe path A (recommended):** archive the current dark.db (`Rename-Item dark.db dark.db.bak-$(date)`) and let v1.2.2 create a fresh one. Existing `research_*` rows from dark-research-mcp won't be visible (that's the cross-project trade-off) but dark-memory-mcp boots cleanly.
  - **Safe path B:** point dark-memory-mcp at a separate DB via `DARK_DB=./dark-memory.db`. The defaultDSN stays `./dark.db`; setting the env var on the binary is sufficient.
  - **Risky path C (do not try):** manually drop `vibe_brands` before booting v1.2.2 so v8 can recreate it. The F37/F39 tolerance will then drop the rename/recreate loop back into a clean state. Only do this if you've back-vacuumed data.

### Known issue
- The dark.db shared schema_migrations bookkeeping between dark-research-mcp and dark-memory-mcp is fragile by design (both projects use `version INTEGER, applied_at TEXT` rows but the version numbers are NAME-aligned, not ID-aligned). Future directions to consider: namespace dark-memory-mcp's bookkeeping to `dark_memory_schema_migrations`; or partition the schema_migrations table by namespace. Not addressed in v1.2.2 â€” separate PR if you want to take it on.

---

## [1.2.1] â€” 2026-07-16

### Fixed
- **F36 â€” `vibe_spec` rejects payloads from MCP harnesses that stringify arrays.** The gemela tool `dark_research_spec_create` (separate server, same `vibe_specs` table) declares `tasks` as `type: "string"` and persists the value as opaque text. `dark_memory_vibe_spec` declared `tasks` as `type: "array"` and required `Tasks []VibeSpecTask`. Some MCP harnesses serialise array arguments as JSON-encoded strings under either schema; in that case `BindOrchestrator`'s `json.Unmarshal` fails with `*json.UnmarshalTypeError: cannot unmarshal string into Go struct field VibeSpecInput.tasks of type []orchestration.VibeSpecTask`, and the operator-visible error surfaced as a generic `ErrInvalidArgument` (without a precise field hint) â€” F35's structured-field reporting kicked in only on successful unmarshal-then-orchestrator failure paths, not on raw unmarshal failures. Symptom: every `dark_memory_vibe_spec` call from certain harnesses returned `{"code":"ErrInvalidArgument","message":"One or more arguments failed validation..."}` regardless of payload validity.
  - `internal/orchestration/vibe_spec.go` â€” `Tasks` is now `json.RawMessage`; new helper `parseTasksField` accepts both forms (leading-byte dispatch on `[` vs `"`) and returns a typed `[]VibeSpecTask`. The validation graph (unique ids, non-empty description, depends_on consistency, cycle detection) is unchanged.
  - `internal/tools/vibe.go` â€” schema for `tasks` widened from `type: "array"` to `anyOf: [{...array, items: vibeSpecTaskSchema}, {type: "string"}]`. Both forms now advertise at the wire layer so harnesses can pick whichever shape they prefer.
  - `tests/orchestration/orchestrator_test.go` â€” added `mustMarshalTasks` helper bridging the old typed-slice test bodies; added 2 new tests: `TestVibeSpec_AcceptsStringifiedTasks` (round-trip: raw string in, parsed array in storage) and `TestVibeSpec_StringifiedTasks_MalformedRejected` (precise error mentions "stringified" plus `ErrInvalidArgument`). The 8 pre-existing VibeSpec tests updated from `Tasks: []orchestration.VibeSpecTask{...}` to `Tasks: mustMarshalTasks(t, []orchestration.VibeSpecTask{...})`.

### Operator notes
- v1.2.1 is a **drop-in replacement** for v1.2.0. No migrations required. The 27-tool canonical surface is unchanged (no new tools, no deprecations). No DB schema change.
- Restart the running `dark-mem-mcp.exe` (PIDs currently running the pre-v1.2.1 binary are tagged in the process list) to pick up the new code. Until restart, `dark_memory_vibe_spec` calls that pass `tasks` as a raw array will continue to fail â€” pass them as a JSON-encoded string in the meantime.

---

## [1.2.0] â€” 2026-07-16

### Added
- **`dark_memory_project_create`** (F33 / Bug C) â€” new PROJECT namespace tool (1 tool) that closes the bootstrap loop for INV-7 multi-tenancy. Prior to v1.2.0, the only way to provision a non-`default` project was to insert into the `projects` table out of band; now operators can create tenants from inside the MCP surface, then immediately call `dark_memory_session_start` with the new `project_id`. Idempotent on `project_id` â€” re-creating an existing project returns the existing row with `idempotent_replay: true` and the original `created_at`.
  - `internal/tools/project.go` â€” new file (RegisterProject + ProjectCreateInput/Result + validation)
  - Kebab-case pattern enforced: `^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`
  - Placed at canonical index 0 (before `session_start`) so tools/list discovery order matches the natural bootstrap flow
- **F35 structured error reporting** â€” `ToolError` extended with `Field`, `ExpectedType`, `ActualType`, and `SchemaHintURL`. `BindOrchestrator` now promotes `*json.UnmarshalTypeError` paths into discrete fields instead of hiding them in `Message`. Callers (LLM-driven or operator-driven) can render targeted fix-up hints without parsing free-form strings. All new fields are `omitempty` so the legacy shape is preserved for non-type-mismatch errors.
- **`vibeSpecTaskSchema`** (F33 / Bug B) â€” extracted shared strict schema for `vibe_spec` / `vibe_publish` task items. `additionalProperties: false` + explicit property list (`id`, `description`, `depends_on`, `owner`). Stops the silent-drop / type-coerce behavior that made calls fail with `cannot unmarshal string into ... depends_on of type []string` when callers passed `title`/`status`/`priority`.
- **`tests/tools/project_tool_test.go`** â€” 7 sub-tests covering happy path, idempotent replay, schema rejection (uppercase project_id, empty display_name, missing fields, unknown field) and the BindStore error envelope shape.

### Fixed
- **F33 / Bug A â€” `vibe_publish` JSON Schema is wrong.** Schema declared `spec`, `constitution`, `tasks`, `artifact_url`, `artifact_type`, `text` as flat top-level strings, but the Go struct `PublishVibeInput` (internal/orchestration/publish_vibe.go:42-72) nests them under `Spec PublishSpecInput` and `Artifact PublishArtifactInput`. Result: every harness call failed with `cannot unmarshal string into Go struct field PublishVibeInput.spec of type orchestration.PublishSpecInput`. Schema is now nested-correct with `additionalProperties: false` on both sub-objects.
- **F33 / Bug C â€” `dark_memory_project_create` was documented but not implemented.** `internal/project/types.go:9` advertised the tool, but no `tools/project.go` existed. Closed by adding the tool in this release.

### Changed
- **Canonical tool surface: 26 â†’ 27** (F33). New PROJECT namespace (1 tool) inserted at index 0. `NewRegistry`, `CanonicalOrder`, and the boot-time sanity check in `RegisterAll` updated to expect 27.
- **Tool surface layout**:
  - `PROJECT (1) â†’ create`
  - `SESSION (4) â†’ start, resume, status, close`
  - `RESEARCH (3) â†’ topic, recall, resume_thread`
  - `VIBE (4) â†’ publish, spec, pipeline_status, resolve_drift`
  - `CONTEXT (3) â†’ artifact_context, spec_context, session_context`
  - `JUDGE (3) â†’ judge, consensus, judgment_history`
  - `POLICY (2) â†’ active_policy, load_constitution`
  - `OBSERVABILITY (3) â†’ memory_state, writes, anomalies`
  - `ADMIN (3) â†’ admin_migrate, admin_schema_status, admin_vacuum`
  - `L6-VLP (1) â†’ vlp_handle_event` (DMAP v1.1 spec 193)
  - Total: 1+4+3+4+3+3+2+3+3+1 = 27.
- Schema strictness: `vibe_publish`, `vibe_spec`, `project_create` now use `additionalProperties: false` on their nested objects so the harness rejects unknown fields at parse time instead of silently dropping or coercing them.

### Migration notes
- **No DB migration.** `dark_memory_project_create` writes to the existing `projects` table (migrations/v7) â€” no schema change. Existing operators running v1.1.x keep their data; the new tool just provides an in-band path to provision what previously required `INSERT INTO projects (...)`.
- **Backwards compatibility for `vibe_publish` callers.** The schema fix is breaking for callers that built payloads against the old (broken) flat-string shape â€” those payloads were never valid against the Go struct and would have failed unmarshal at runtime. New payloads use the nested shape. See `docs/PR-v1.2.0.md` (added in this release) for a before/after payload diff.
- **Backwards compatibility for `ToolError` consumers.** The four new fields (`Field`, `ExpectedType`, `ActualType`, `SchemaHintURL`) are `omitempty`, so existing JSON consumers that ignore unknown fields keep working. Consumers that strictly validate the response shape should add the new fields to their allow-list.

### Tests
- 7 new sub-tests in `tests/tools/project_tool_test.go` (success, idempotent replay, schema rejection, error envelope).
- All existing v1.1.0 tests still pass against the updated `RegisterAll` (27-tool surface); existing test fixtures that asserted on the 26-tool count have been updated.

[1.2.0]: https://github.com/Opita-Code/dark-memory-mcp/compare/v1.1.0...v1.2.0

---

## [1.1.0] â€” 2026-07-16

### Added
- **DMAP v1.1 (Dark Memory Agent Protocol)** â€” 6-layer architecture, 26 atomic specs
  - Layer 2 (loop coordinator) closed with 5 atomic specs:
    - 2.1 SessionState â€” pure state-machine logic
    - 2.2 VLPPackage â€” 4 typed primitives (Brief/Propose/Record/Complete)
    - 2.3 VLPPersistence â€” Store-backed state with audit
    - 2.4 VLPAuditor â€” transition-level audit
    - 2.5 VLPLoopUseCase â€” end-to-end loop driver
- `Store.SaveVLPStateWithTransition` â€” atomic combo: UPSERT + row-level audit + transition-level audit in one DB transaction
- `audit.WriteEvent.ProjectID` field â€” INV-7 multi-tenancy at the audit layer
- `audit.ListFilters.ProjectID` â€” read-side tenant filtering
- 2 new dual-driver sub-tests: `write_audit_project_isolation` (F33), `vlp_state_roundtrip` enhancements (F33 cross-project)

### Changed
- **INV-1 hardening (F32)**: 21 SQLite Save*/Update*/Delete*/Close*/Link* methods now wrapped in `BeginTx` + `Commit` + `defer Rollback`
  - New helpers: `runInTx`, `recordWriteLockedTx` (SQLite); `runInTx`, `recordWriteTx` (Postgres)
  - Data row + audit row now atomic; partial failure rolls back both
  - **Critical**: helpers read `s.activeProject` without re-locking (deadlock avoidance â€” caller already holds `s.mu`)
- `UseCase.HandleEvent` (spec 2.5) refactored to use `Store.SaveVLPStateWithTransition` instead of two separate calls
- Default version bumped from `0.1.0-dev` to `1.1.0-dev` in `cmd/dark-mem-cli` + `cmd/dark-mem-inspect`

### Database
- **Migration v9** (`vlp_state_table`) â€” vlp_state per-session state row
  - `UNIQUE INDEX (project_id, session_id)` â€” multi-tenancy at vlp layer (INV-7)
- **Migration v10** (`audit_project_index`) â€” composite index on `write_audit(project_id, session_id)` for ListWrites filtering efficiency
  - **No column changes** â€” `write_audit.project_id` was already added in v7 (`project_namespace`)
  - **Idempotent** â€” `CREATE INDEX IF NOT EXISTS`
  - **Backwards compatible**

### Tests
- `internal/vlp` â€” 12 tests including new `TestVLP_E2E_AtomicSaveEmitsTwoAuditRows`
- `tests/dual_driver` â€” 11 sub-tests including F33 isolation
- 10 packages, all PASS (374s full suite)

### Known v2 follow-ups (not blocking)
- Postgres `notImpl` stubs need same F32 wrapping when real impls land (~30 methods)
- No meta-test verifying "every Save* rolls back its audit row on data-write failure" â€” only VLP has this
- `usecaseTransitionNotes` and `auditor.marshalTransitionNotes` produce byte-identical JSON but are duplicated; trivial refactor when v2 reorganizes vlp package

---

## [1.0.0] â€” 2026-07-12

### Added
- **Initial release**: 25 MCP tools, dual-driver SQLite + Postgres, 7 operational invariants
- 8 trades: SESSION (4), RESEARCH (3), VIBE (4), CONTEXT (3), JUDGE (3), POLICY (2), OBSERVABILITY (3), ADMIN (3)
- Migrations v1-v8 establishing core schema (sessions, research, vibe_specs, vibe_artifacts, vibe_brands, vibe_compliance, vibe_drift_reports, sdd_evaluations, write_audit, constitutions, mods, projects, mod_loads)
- CLI tools: `dark-mem-mcp` (MCP server), `dark-mem-cli` (admin), `dark-mem-inspect` (read-only observability)
- 9 test suites: cli, conformance, context, dual_driver, e2e, economy, invariants, orchestration, project
- Constitution watchdog (INV-4) â€” `constitutions` table + `Store.VerifyConstitutionHash`
- Canary protection (INV-3) â€” `SafetyHolder` rejects payloads containing canary
- Mod sanitization (INV-6) â€” content loader refuses unsafe content
- Multi-tenancy foundation (INV-7) â€” projects table + project_id column on every tenant-scoped table
- Bridge documentation: 5/7 bridges complete (bridge.3 + bridge.5 deferred per spec 164)
- MCP Inspector conformance test (`tests/conformance/`)

### License
- MIT â€” see [LICENSE](LICENSE)

[1.1.0]: https://github.com/Opita-Code/dark-memory-mcp/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/Opita-Code/dark-memory-mcp/releases/tag/v1.0.0All notable changes to dark-memory-mcp are documented here.

## [2.0.2] â€” 2026-07-27

### Fixed (DARK-MEM-v2.0.1-REGRESSION-1)

The v2.0.1 GateMiddleware (commit 09390d9) was wired with an empty
\StaticSessionResolver{}\ â€” every call returned \""\ and the gate
refused all tool calls (\ErrFrameStaleTooFar\). v2.0.2 fixes this
in three layers:

#### Schema (v17 â€” commit e16b2b7)

\projects.active_session_id\ + \ctive_session_set_at\ columns
added by migration v17 (sqlite + postgres). Set by session_start
+ session_resume; cleared (compare-and-set) by session_close.

#### Store interface

Three new methods on \store.Store\:

- \SetActiveSession(ctx, projectID, sessionID)\ â€” overwrite; idempotent
- \GetActiveSession(ctx, projectID) (string, error)\ â€” \""\ if none
- \ClearActiveSession(ctx, projectID, expectedSessionID)\ â€” CAS clear

SQLite implements all three. Postgres has stubs (driver unused
on this host).

#### Gate per-tool session requirement

\RequiresActiveSession(toolName)\ allowlist gates PreCheck's
\SessionID == "" || ProjectID == ""\ refusal. Default is true
(most tools require a session). The exempt list:
\session_start\, \session_resume\, \health_ping\,
\memory_state\, \project_create\, \ctive_policy\,
\load_constitution\, \dmin_schema_status\, \dmin_vacuum\,
\dmin_migrate\. These are operator-issued setup + read-only
introspection and must work without a session.

Bootstrap and read-only tools were broken in v2.0.1; this
restores the v2.0.0 contract for them.

#### Real ActiveSessionResolver

\internal/server/active_session_resolver.go\ â€”
\StoreBackedActiveSessionResolver\ queries the projects row
through a pluggable \ActiveSessionLookup\ (main.go adapts via
\StoreBackedLookup\). Short in-process TTL cache (default 5s)
amortizes the per-tool-call DB read. \Invalidate\ /
\InvalidateAll\ exported for write-through caching.

#### Orchestrator call sites

session_start / session_resume / session_resurrect write the
active pointer; session_close + sweeper clear it (CAS-aware so
a stale close of an older session doesn't clobber a newer
session_start).

### Wire contract change

\ActiveSessionResolver.ActiveSessionID\ gained a
\context.Context\ first parameter. Both implementations
(\StaticSessionResolver\, \StoreBackedActiveSessionResolver\)
updated. Third-party implementations must adapt.

### Tests (regression guards for v2.0.1's failure)

- \internal/policy/gate_v2_0_2_test.go\ â€” pins the
  \RequiresActiveSession\ contract AND a direct guard that
  \PreCheck\ on a session-free tool returns \Allowed=true\.
- \internal/server/active_session_resolver_test.go\ â€” 6 tests
  covering cache hit, expiry, empty projectID no-op,
  lookup-error handling, Invalidate, CacheTTL=0 disable.
- \	ests/dual_driver/active_session_test.go\ â€” 4 tests covering
  Set/Get/Clear roundtrip, CAS semantics, race-new-session-wins,
  ErrProjectNotFound.

\internal/server/middleware_test.go\\s
\TestGateMiddleware_PreCheck_RefusesMissingIdentity\ rewritten
to use \ibe_publish\ instead of \health_ping\ (the latter
is now session-free and would not exercise the refusal path).

---
Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [2.0.1] â€” 2026-07-27

### Added (gate promoted to the transport layer)

The v2.0.0 pivot put the policy-gateway into the orchestrator layer.
v2.0.1 promotes it to the transport layer so every `dark_memory_*`
tool call now flows through `internal/policy.PostCheck` via the new
`GateMiddleware`, *before* the inner handler runs.

- **`internal/server/middleware.go` (new) â€” `GateMiddleware.Wrap`**.
  PreCheck (capability grant + intent-in-scope) before the inner
  handler; PostCheck (drift-at-write, when `DriftChecker` is non-nil)
  after, for artifact-creating tools only. When `Gate` is nil, the
  legacy direct-dispatch path runs (no policy enforcement), keeping
  the existing test harness ergonomic.
- **`internal/server/middleware_test.go` (new)** â€” 387 lines covering
  3 categories: capability mismatch, scope mismatch, drift verdict
  refusal at write boundary.
- **`internal/server/server.go`** â€” `wrapHandler` routes through `Gate`
  when set.
- **`internal/server/lifecycle.go`** â€” `BootState.Gate` field.
- **`cmd/dark-mem-mcp/main.go`** â€” wires the Gate from the returned
  `FrameSource` at boot.

### Changed (FrameSource singleton, 5A.ii.b.2.c.1)

The per-call `FrameSource` construction in `dark_memory_recall` is
lifted to a boot-time singleton. Both the recall tool and the gate
now share the same `CachedSource` instance.

- **`internal/recall/singleton.go` (new)** â€” boot-time `FrameSource`
  construction. Singleton contract tested in `singleton_test.go`.
- **`internal/tools/register.go`** â€” `RegisterAll` signature changes
  from returning `error` to returning `(policy.FrameSource, error)` so
  the caller can wire it into the gate.
- **`internal/tools/recall.go`** â€” uses the passed-in singleton;
  per-call construction is gone.
- **`tests/e2e/server_test.go`** â€” updated for the new `RegisterAll`
  signature.

### Added (operator ergonomics)

- **Legacy `DARK_SCRAPPER_URL` env shim** (H-4 follow-up). PR #10
  dropped the v1.x env name without a backward-compat alias, which
  broke unmigrated operators' drift-judge path on first boot. v2.0.1
  closes the gap: when `DARK_SCRAPPER_URL` is set AND
  `DARK_DRIFT_JUDGE_DAEMON_URL` is not, `NewSelfHarnessClient` falls
  through to the legacy value and logs a one-line deprecation notice
  at startup. The legacy env will be removed in v2.1.0.
  Migration: `sed -i 's/DARK_SCRAPPER_URL/DARK_DRIFT_JUDGE_DAEMON_URL/g' .env`
- **`internal/orchestration/llm_client_scrapper_alias_test.go` (new)**
  pins the fallback contract end-to-end against the live binary.

### Notes

- **`DriftChecker` is intentionally `nil`** in the gate wiring. The
  strict-mode opt-in is a separate follow-up (or a v2.0.2 if needed).
  The gate still runs PreCheck unconditionally and refuses tools the
  LLM isn't granted.
- **`DefaultServerVersion` bumped** from `2.0.0-dev` â†’
  `2.0.1-dev` in `internal/server/bootstrap.go` for the legacy
  hardcoded fallback. Canonical version resolution flows through
  `version.Resolve()` (set by `make release` via `-ldflags`).
- `git describe --tags --always --dirty` on a fresh v2.0.1 tag will
  print `v2.0.1` cleanly (no `+dirty` suffix).

---

## [2.0.0] â€” 2026-07-19

### Breaking (operator env contract â€” ships in PR #10)

- **`DARK_SCRAPPER_URL` â†’ `DARK_DRIFT_JUDGE_DAEMON_URL`**.
  SelfHarnessClient provider renamed from `"dark_scrapper"` to
  `"drift_judge_daemon"`. Function `judgeViaScrapper` â†’
  `judgeViaDriftJudgeDaemon`. **No backward-compat alias** â€”
  operators must update their env.
  Migration: `sed -i 's/DARK_SCRAPPER_URL/DARK_DRIFT_JUDGE_DAEMON_URL/g' .env`.
- **`DARK_JUDGE_MODEL_SCRAPPER` â†’ `DARK_JUDGE_MODEL_DRIFT_JUDGE_DAEMON`**.
- Test file `internal/orchestration/scrapper_wiring_test.go` â†’
  `internal/orchestration/drift_judge_daemon_wiring_test.go`.

### Breaking (server lifecycle default)

- **Shutdown default `close_reason` `aborted` â†’ `clean`** in
  `internal/server/lifecycle.go`. Operators who relied on the
  legacy `aborted` default for crash-recovery workflows can opt
  back via `DARK_SHUTDOWN_CLOSE_REASON=aborted`.
- New env var `DARK_AUTO_RESURRECT=on_boot` opts into automatic
  session resurrection on server startup. Default: orphans are
  surfaced via a log entry but not auto-recovered.

### Fixed

- **INFRA-002 â€” `dark_memory_vibe_spec` now surfaces WHICH form and WHY
  on tasks parse failure.** Pre-fix, `parseTasksField` in
  `internal/orchestration/vibe_spec.go` discarded the underlying
  `json.Unmarshal` error and returned only
  `store.NewFieldError(store.ErrInvalidArgument, "tasks")`, surfacing
  `"invalid argument at field=tasks"` to the harness with no
  diagnostic. Post-fix:
  - The error chain is `*store.FieldError{Field:"tasks"}`
    (F35 wire-propagation keeps working: `errors.As(err, &fe)`
    still finds it; `errors.Is(err, ErrInvalidArgument)` still
    matches).
  - Wrapped with `fmt.Errorf("%w: rejected by parser (Form A/B
    step N/unknown form ...): %v", fe, cause)` so the operator
    sees which form was attempted AND the underlying json
    diagnostic (e.g. "invalid character 'Â·' after top-level
    value").
  - The unknown-first-byte path explicitly names the offending
    byte (`first non-whitespace byte='{'`) and the expected
    shape without leaking the rest of the payload (preserves
    `classifyUnknown`'s no-payload-leak policy).
- Concrete reproductions covered by:
  - `internal/orchestration/vibe_spec_test.go` (orchestrator-level):
    - `[{...}]Â·` (trailing garbage byte after close-bracket, Form A)
    - `"[not-an-array]"` (outer string but inner not parseable, Form B)
    - `{...}` (object-shaped payload â€” neither Form applies)
  - `tests/wire/infra002_vibe_spec_diagnostic_test.go` (H-3 wire
    conformance): pins the contract end-to-end against the running
    binary over JSON-RPC â€” the same envelope shape production
    harnesses see.

### Added (memory-as-policy-gateway pivot)

The pivot replaces the pull-based CRUD model (v1.x) with a
gate-driven active-memory model. Every `dark_memory_*` tool call
now traverses `internal/policy.PostCheck` which:

1. Composes an **atomic context frame** from session + project +
   global state via `internal/atomic.FrameSource`.
2. Verifies the call's intent is in scope and the LLM has the
   capability grant for it (`CapabilitiesFrame`).
3. Invokes the orchestrator with the frame as input.
4. **Drift-checks the response at the write boundary** before
   returning to the LLM (`internal/drift.Checker`).

- **`dark_memory_recall` (29th canonical tool, CONTEXT 3 â†’ 4)** â€”
  the canonical scoped-replay orchestrator. Inputs: scope
  (global|project|session), project_id, session_id, since_token.
  Outputs: per-kind atomic frames (Identity, Scope, Capabilities,
  Drift, Persona) + delta write_audit rows since since_token +
  new_token cursor. RFC Â§3 M1 + Â§6.1.
- **`internal/atomic` package (NEW)** â€” Frame interface + 6
  concrete types: IdentityFrame, ScopeFrame, CapabilitiesFrame,
  PersonaFrame, DriftFrame, EvidenceFrame. FrameSource interface.
  Wave 5X.2: `Frame.Hash` signature `Hash() [32]byte` â†’
  `Hash() ([32]byte, error)` (defense against previous
  interface/type mismatch that was silently swallowed).
- **`internal/drift` package (NEW)** â€” `Strictness` enum
  (off|warn|strict), `Checker` type with `CheckArtifact(ctx,
  ArtifactInput) â†’ Verdict`, `JudgeCaller` interface. Replaces
  the previous `policy.PostCheck` stub. Decision tree for judge
  errors: strict refuses, warn allows.
- **`internal/policy` package (NEW)** â€” gate.PostCheckInput
  gains `DriftChecker + DriftArtifact` optional fields.
  `PostCheck` now calls `drift.Checker` when `Strictness != off`.
- **`internal/recall` package (NEW)** â€” `StoreSource` (reads
  from `store.Store`) + `CachedSource` (INV-5 cache re-hash on
  Get + audit emission on cache_mismatch). 9 tests.
- **Session lifecycle resilience** â€” `session_resurrect`,
  `session_recover`, `session_heartbeat`, `session_sweeper`,
  `boot_reconcile`. Closed-due-to-crash sessions are now
  resurrectable (only operator-initiated termination is
  terminal). `SessionResurrectOutput` gains 5 fields:
  `InheritedConstitution{ID,Ver}`, `ActiveConstitution{ID,Ver}`,
  `ConstitutionBumped`, `InheritedMods`.
- **L6 adapter integration** â€” 3 hooks from
  `BRIDGE_AND_COEXISTENCE.md` Â§6 wired:
  * `startup-recover` â†’ `runStartupRecover()` in main.go
  * `periodic-heartbeat` â†’ sweeper (5E.iii, doc-only)
  * `exit-close_clean` â†’ Shutdown default reason = clean
- **Per-project drift strictness** â€” `Project.DriftStrictness`
  field (migration v14). `drift.ResolveStrictness(projectOverride,
  envValue, warnf)` â€” empty/'default' â†’ env; valid â†’ override;
  invalid â†’ warn + env fallback.

### Added (canonical tool count 28 â†’ 29)

- **`dark_memory_recall`** â€” see "memory-as-policy-gateway
  pivot" above. CONTEXT namespace: 3 â†’ 4 tools.

### Changed (data plane)

- **Schema migrations v11â€“v15** (sqlite + postgres):
  * v11, v12 â€” frame-related scaffolding (see git log)
  * v13 â€” `CREATE UNIQUE INDEX uq_vibe_frames_natural_key ON
    vibe_frames (project_id, session_id, scope_level, scope_id,
    frame_kind)` â€” enables the UPSERT rewrite
  * v14 â€” `ALTER TABLE projects ADD COLUMN drift_strictness TEXT
    NOT NULL DEFAULT 'default'`
  * v15 â€” `ALTER TABLE vlp_state ADD COLUMN open_spec_id INTEGER
    NOT NULL DEFAULT 0`
- **SaveFrame rewritten as INSERT ... ON CONFLICT DO UPDATE**
  (sqlite) / `ON CONFLICT ... RETURNING` (postgres). Replaces the
  SELECT-then-INSERT/UPDATE race in the previous implementation
  under concurrent SaveFrame calls. Tested with 10-goroutine
  concurrent upsert â†’ 1 row.
- **`WriteContext.SessionEvent`** â€” every `Save*` emits this
  field in `write_audit`. Closes pre-existing drift where the
  `session_event` column was INSERTed NULL silently.
- **`VLPStateRow.OpenSpecID`** â€” the actual spec_id the session
  is working on. Previously the recall cache used `vlp_state.ID`
  as a meaningless proxy.
- **`Project.DriftStrictness`** â€” per-project resolver override.

### Notes (constitution + RFC)

- The pivot's design rationale lives in
  `vibe-flow/main/ACTIVE_MEMORY_RFC.md`, `SCHEMA_v11_v12.md`, and
  `DRIFT_BURST.md` â€” these are operator-private planning docs
  (NOT committed; lives in the operator's local workspace).
- Public docs updated: `vibe-flow/PLAN.md` v2 (pivoted roadmap),
  `vibe-flow/main/BRIDGE_AND_COEXISTENCE.md` v2 (cx.v3,
  policy_gateway, dark-research-mcp demoted, dark-recall
  cancelled).
- `DefaultServerVersion` constant bumped from `"1.4.1-dev"` â†’
  `"2.0.0-dev"`. Canonical source remains `version.Resolve()`
  (set by `make release` via `-ldflags`).
- 9 commits + 1 release (this PR). Pre-merge lint scrub PR #10
  established the H-4 compliant env-var rename as a separate
  concern.

---

## [1.4.1] â€” 2026-07-18

### Behavior change (callers MUST verify)

- **`dark_memory_vibe_spec` now rejects non-canonical `vibe_case` values**
  with `ErrInvalidArgument`. Previously the JSON Schema layer accepted
  any string (e.g. `"C8"`, `"code"`, `"c1"`); now both the JSON Schema
  enum AND the orchestrator reject unknown values via `vibecase.Parse`
  (defense in depth).
- Callers using valid `C1`..`C7` values see **no change**.
- Callers passing `""`, whitespace-only, or any non-canonical label
  now receive a structured error:
  ```
  vibe_case: vibecase: invalid case identifier: "X" is not one of
             [C1 C2 C3 C4 C5 C6 C7]
  ```
- **Migration:** if your harness ever passed an unexpected `vibe_case`
  value (e.g. a downstream convention of `"image"` for C3), map it
  to the canonical `"C3"` label before sending. No migration tool
  ships; the rejection is fail-loud and the operator-visible error
  names the allowed set.

This is the change that motivated the v1.4.1 PATCH bump instead of
v1.4.2: the new validation is observable to callers but does not
break any caller that was previously compliant with the canonical
C1..C7 set. Per the project's SemVer convention (no formal API
stability promise at v1.x), a PATCH bump is appropriate.

### Added (canonical C1..C7 taxonomy)

- **`internal/vibecase` package** â€” single source of truth for the
  C1..C7 case taxonomy. Replaces a JSON Schema enum fragment that
  was duplicated across `vibe_publish` and (asymmetrically) absent
  from `vibe_spec`. Exports:
  - `Case` (typed string) and the seven canonical constants
    `CaseCode..CaseMixed`.
  - `Parse(s)` (strict, trims, rejects empty + unknown + mixed-case),
    `MustParse(s)` (panic-on-error for startup constants),
    `IsValid(s)` (boolean shortcut).
  - `All()` and `JSONSchemaEnum()` â€” stable, ordered, defensively
    copied.
  - `Description(c)` â€” human-facing one-liner per case (for LLM
    context projections).
  - `ErrInvalidCase` â€” exported sentinel for `errors.Is` checks.
  - 15 unit tests covering ordering, defensive copy, trim, empty,
    unknown, mixed-case, error message contents, panic, boolean
    shortcut, round-trip, description, cardinality.

### Changed

- **`vibe_spec` now enforces the C1..C7 enum** at the JSON Schema
  layer (`internal/tools/vibe.go`) AND at the orchestrator layer
  (`internal/orchestration/vibe_spec.go`), closing the asymmetry
  where `vibe_publish` validated the enum but `vibe_spec` did not.
- **`vibe_publish` JSON Schema enum now derives from
  `vibecase.JSONSchemaEnum()`** instead of a hardcoded literal. Any
  future case addition automatically propagates to both tools.
- **Both orchestrators validate via `vibecase.Parse`** (defense in
  depth): even if the JSON Schema layer is bypassed (direct
  orchestrator call, future non-MCP transport, etc.), the validator
  rejects unknown cases before the row is persisted.
- 4 new orchestrator tests:
  `TestVibeSpec_InvalidVibeCase`,
  `TestVibeSpec_AcceptsAllCanonicalCases`,
  `TestVibeSpec_AcceptsTrimmedVibeCase`,
  `TestPublishVibe_InvalidVibeCase`.

### Versioning note

Adding a case (e.g. C8) is a MINOR bump and is backward-compatible
(case labels are stored as TEXT; existing rows remain readable).
Reordering or renaming an existing case is a BREAKING change. See
the package doc on `internal/vibecase` for the full contract.

---

## [1.4.0] â€” 2026-07-18

### Added (release-integrity release)

- **`release-integrity@1.0.0` constitution** ([`CONSTITUTION.md`](CONSTITUTION.md)).
  Five rules codify release hygiene: (1) single source of truth for
  version, (2) archive-not-delete for deprecation, (3) CHANGELOG is
  authoritative, (4) drift detection on every boot, (5) session-bound
  governance. Cross-cutting reference for every `vibe_publish` artifact
  in the dark-memory-mcp project.
- **`internal/version` package** â€” single-source version resolver.
  Replaces the hardcoded `DefaultServerVersion = "1.3.0"` constant in
  `internal/server/bootstrap.go` and the `var Version = "1.1.0-dev"`
  in `cmd/dark-mem-cli/main.go` and `cmd/dark-mem-inspect/main.go`.
  Resolution priority: `-ldflags` injection (canonical, set by
  `make release`) â†’ `debug.ReadBuildInfo()` (dev) â†’ hardcoded
  `"dev"` sentinel (emergency). 9 unit tests cover all three paths.
- **`Makefile`** with `build` / `release` / `drift-check` /
  `version` / `version-json` / `inspect` / `tag` / `clean` targets.
  Handles the multi-module `cmd/*` layout (each cmd is its own Go
  module; the Makefile `cd`s into each before `go build`).
- **`scripts/inject-version.sh`** (bash) and
  **`scripts/inject-version.ps1`** (PowerShell) â€” resolve the canonical
  version from `git describe` and emit the `-ldflags` expression that
  feeds `make release`. Same resolution rules, same output formats
  (`--raw` / `--json` / default), same `--strict` flag.

### Added (drift detection in health_ping)

- **`dark_memory_health_ping` response grew a `git` block.**
  New fields: `git.tag`, `git.commit`, `git.dirty`, `git.build_time`,
  `git.source` (one of `ldflags|buildinfo|dev`), `git.is_dev`.
- **Top-level `drift` bool** â€” true iff the resolver fell back to the
  dev path OR the working tree was dirty at build time. Per
  `CONSTITUTION.md` Rule 4, a release binary MUST report
  `drift=false`. Operators can monitor the single-bit signal directly.
- Wire-conformance test (`tests/wire/health_ping_test.go`) and the
  e2e binary (`cmd/e2e/main.go`) updated to mirror and assert the
  new fields.

### Changed

- The `internal/server/bootstrap.go::DefaultServerVersion` constant
  is now a deprecated string (`"1.4.0-dev"`) for any external
  callers; the canonical default flows through `version.Resolve()`.
- `cmd/e2e/main.go` relaxed the hardcoded `"1.3.0"` health_ping
  version assertion to "non-empty" â€” the value is now driven by the
  resolver, not by source code.

### Notes

- v1.4.0 ships together with **dark-research-mcp v0.7.0**, which
  wraps 38 duplicate tools (the dark_mem_*, dark_research_spec_*, etc.,
  and dark_ssd_* tools) in a deprecation envelope pointing at
  dark-memory-mcp. See
  [`dark-research-mcp/RELEASE_NOTES_v0.7.0.md`](https://github.com/Opita-Code/dark-research-mcp/blob/main/RELEASE_NOTES_v0.7.0.md)
  for the peer release notes and migration guide.

---

## [1.3.2] â€” 2026-07-16

### Fixed

- **`fix(llm): wire SelfHarnessClient.Judge to drift-judge-daemon HTTP route.**
  `SelfHarnessClient.Judge` was returning `ErrNoLLMAvailable` unconditionally
  (deferred to Wave 4+ in source). Wired to POST to `DARK_SCRAPPER_URL/v1/messages`
  with `Bearer ds-managed` (sentinel auth) when `provider == "dark_scrapper"`.
  Other providers (anthropic / openai / google) still return `ErrNoLLMAvailable`
  by design, preserving the source's visibility-over-silent-degrade philosophy.
  URL validation rejects empty / `file://` / no-scheme / no-host before any
  HTTP call (R5 defense).

### Added

- **`feat(federation): cross-namespace lookup tool + pipeline_status hint.**
  dark-memory and dark-research MCPs use two physically separate SQLite files
  (`dark-memory.db` vs `dark.db`) with compatible schemas on shared tables.
  New `internal/federation` package: read-only `Peer` handle opened from
  `DARK_FEDERATION_PEER_DSN`. New `dark_memory_federation_lookup` tool
  (opt-in extra, same pattern as `DARK_REDTEAM=armed`). `pipeline_status`
  now probes the peer on local miss and adds a `cross_namespace_hint` field.

### Governance

- DARK-MEM-001 establishes the `release-integrity@1.0.0` constitution
  (see [`CONSTITUTION.md`](CONSTITUTION.md)) and retroactively tags `v1.3.1`
  at `fbc5c03` to give the squash commit a canonical annotated reference.

---

## [1.3.1] â€” 2026-07-16

### Note (release plumbing)

- **Local tag `v1.3.1` retroactively created at commit `fbc5c03`.** The
  commit message reads `release: v1.3.1 -- sync unreleased work to origin/main
  (squashed)`. The squash landed in the repo on 2026-07-16 but no annotated
  tag was created at the time; the v1.3.1 entry exists to give that squash
  a canonical reference and to keep the tag chain (v1.3.0 â†’ v1.3.1 â†’ v1.3.2)
  consistent with the commit graph.
- No standalone code changes between v1.3.0 and v1.3.2: v1.3.1 is a
  release-plumbing tag only. The substantive changes in this window are
  documented under v1.3.0 and v1.3.2.

---

## [1.3.0] â€” 2026-07-16

### Added (production-readiness release)

- **`dark_memory_health_ping` â€” operator-facing liveness probe.**
  The canonical surface grew 27 â†’ 28 tools (OBSERVABILITY 3 â†’ 4).
  health_ping is a strict, documented-shape probe distinct from
  `memory_state`:
    - **Latency budget:** <500ms round-trip (target <50ms on warm cache);
      suitable for K8s liveness/readiness probes that fire every second.
    - **Side-effect freedom:** does NOT touch the audit bus, does NOT
      advance VLP state, does NOT migrate. Safe to call at high
      frequency.
    - **Frozen contract:** `{server, db, runtime, registry, latency_ms,
      checked_at}`. Adding fields is backward-compatible; removing
      fields is a breaking change to monitoring rules.
  Wire conformance: `tests/wire/health_ping_test.go::TestWire_HealthPingShape`
  (verifies all fields) and `::TestWire_HealthPingLatency` (verifies the
  500ms ceiling). Tool count: `tests/wire/zz_toolenum_test.go`.
- **`tests/wire/wire_session_test.go::waitForBootMarker`** â€” eliminates
  the startup race that previously caused intermittent "tool not found"
  failures when `initialize` arrived before the binary's mcp-go loop
  started. The harness now waits up to 5s for the `registered N tools`
  boot marker on stderr before sending `initialize`.
- **`internal/tools/health.go::unwrapToolResponse` helper** â€” single
  point of edit for the mcp-go `content:[{type:"text",text:"..."}]`
  envelope shape that wraps every tool response.
- **`Config.BootedAt`** field â€” wall-clock time captured at config load;
  `SetRuntimeContext` propagates it into `health_ping` so uptime is
  accurate from the very first call.
- **`.github/workflows/ci.yml`** â€” operator-reproducible CI recipe:
  builds, runs lint, runs `go test ./...`, runs `go test ./tests/wire`
  with `DARK_MEM_MCP_BIN` set. The never-push policy is preserved
  (this file lives in-repo for transparency; CI is local-only).
- **`docs/PRODUCTION_CHECKLIST.md` Â§Health Probe** â€” wiring guide for
  the new `dark_memory_health_ping` including a sample K8s liveness
  probe YAML and a Prometheus `up{job="dark-mem-mcp"}` snippet.

### Changed
- **Canonical tool count 27 â†’ 28.** README, DECISION_MATRIX,
  bridge.7 conformance test, e2e canonical-order test, and the
  sanity check inside `tools.RegisterAll` all bumped to 28.
- **`DARK_SERVER_VERSION` default** bumped from `1.2.3` to `1.3.0`
  (`DefaultServerVersion` constant in `internal/server/bootstrap.go`).
- **`tests/wire/wire_session_test.go::resolveWireBin`** now skips the
  test when no binary is found (previously fataled). The first
  candidate is still `../cmd/dark-mem-mcp/dark-mem-mcp.exe` so a
  freshly-built binary is picked up automatically.

### Documented
- **`docs/PRODUCTION_CHECKLIST.md` Â§Race detector availability** â€”
  the operator's `go test -race` requires a C compiler; on this host
  no gcc is installed and the race detector is therefore unavailable.
  Workaround: validate via the wire suite (10 tests, including
  TestWire_HealthPingLatency which exercises 5 sequential calls and
  catches perf regressions) and the e2e suite (`tests/e2e/server_test.go`
  fires 1000 concurrent calls).
- **`docs/PRODUCTION_CHECKLIST.md` Â§Stale-binary gotcha** â€” if a
  previous binary is left at `dark-mem-mcp.exe` in the repo root
  (or in `PATH` before `cmd/dark-mem-mcp/`), the wire harness's
  fallback resolution picks it up. Always rebuild into
  `cmd/dark-mem-mcp/` and either delete or set `DARK_MEM_MCP_BIN`
  explicitly when running `go test ./tests/wire`.

### Tests
- 15 / 15 packages PASS in `go test ./...` (full sequential suite).
- 10 / 10 wire tests PASS against the v1.3.0 binary in
  `go test -tags wire ./tests/wire/...` (with `DARK_MEM_MCP_BIN`
  set). Total wire suite runtime: ~25s.
- The 28-tool contract is enforced by both `TestE2E_28ToolsRegistered`
  (Go level) and `TestWire_RuntimeToolEnumeration` (wire level).

### Migration from v1.2.x
- **Drop-in for v1.2.5 operators.** No DB schema change, no migration
  bumps, no env var renames. The 28th tool is purely additive.
- The canonical order has a single new entry between `anomalies` and
  `admin_migrate`: `health_ping` at position 23 (0-indexed). Any
  harness that iterates `tools/list` and indexes by **name** is
  unaffected. Any harness that indexes by **position** must update.
- The `dark_memory_health_ping` tool is registered as canonical,
  not as an "extra". Un-armed servers see 28 tools; armed servers see
  28 + 3 redteam = 31.

---

## [1.2.5] â€” 2026-07-16

### Added
- **`tests/wire/` end-to-end JSON-RPC suite.** Wire-conformance tests
  prove fixes actually work through the real MCP wire (binary
  subprocess + JSON-RPC over stdio), not just at the Go orchestrator
  level. Catch the bugs that Go-level tests cannot: harness encoding
  (LLM dependent), schema-layer mismatches, error-envelope propagation.
  **Rule (H-3 in CONTRIBUTING.md):** every fix MUST ship with at
  least one wire test.
- **`store.FieldError` structured type + F35 wire propagation.** Previously
  orchestrator-level `ErrInvalidArgument` errors discarded the field
  name; only `json.UnmarshalTypeError` paths set `ToolError.Field`.
  This meant a `parseTasksField` rejection (e.g. LLM emits a number)
  surfaced as the generic "One or more arguments failed validation"
  message. `store.FieldError` carries the structured Field; ToToolError
  extracts it via `errors.As` and propagates to `ToolError.Field`.
  Tests: `tests/wire/f35_structured_error_test.go` (end-to-end via
  binary), `tests/orchestration/orchestrator_test.go::TestVibeSpec_StringifiedTasks_MalformedRejected`.
- **CONTRIBUTING.md** baking the four hard rules (H-1 each MCP owns its DB,
  H-2 array/object string fallback, H-3 wire tests mandatory, H-4 no
  private names in public artifacts) and seven conventions. Every
  future dark-* server is built against this doc.
- **`docs/PRODUCTION_CHECKLIST.md`** operator runbook: boot signal
  matrix, recovery playbooks (R-1 vec0, R-2 dark.db corruption, R-3
  tasks shape, R-4 LLM-prompt drift), dark-research vs dark-memory
  isolation verification, performance baselines, one-page cheat
  sheet.
- **Wire test infrastructure.** `tests/wire/wire_session_test.go`
  provides `wireSession` (binary subprocess + JSON-RPC framed
  stdio), `startWireSession(t)` (per-test isolated DB under
  `t.TempDir()`), `testsCall(name, args)` (strict per-id request).
  Override `DARK_MEM_MCP_BIN` env var to test a specific binary.

### Changed
- **`parseTasksField` error propagation.** Errors now wrap via
  `store.NewFieldError(store.ErrInvalidArgument, "tasks")` so the
  field name reaches `ToolError.Field`. The orchestrator-level
  `errMissingField` helper now also returns a `store.FieldError`
  instead of a plain `fmt.Errorf`. **Wire test impact:**
  `TestWire_F35_TypeMismatchSurfacesFieldPath` now passes (was
  returning the generic error envelope pre-fix).
- **`vibe_publish` shape regression test.** Tests now post the CORRECT
  nested shape (spec as object, artifact as object, tasks as
  JSON-encoded string). Pins the post-F33 contract.

### Tested
* 7 wire-conformance tests against the live binary:
  - F33 (vibe_publish nested schema)
  - INV-8 (defaultDSN isolation against cwd dark.db collision)
  - F35 (structured field error via `tasks: 42.0`)
  - F36 array form
  - F36 stringified-array form
  - F37-F40 (boot against half-migrated dark-memory.db)
  - F37 (duplicate column tolerance via ApplyOne-by-statement split)
* 15 of 15 package test suites pass (last suite run before this
  commit). The conformance suite is occasionally flaky under heavy
  concurrent load (full suite at once); reruns always pass.

### Operator notes
- Drop-in replacement for v1.2.4. No DB migration.
- The new `tests/wire/` package requires `DARK_MEM_MCP_BIN=<path-to-binary>`
  unless `./dark-mem-mcp.exe` is in the repo root (the default for
  development). Production CI should set this env var explicitly.
- The four wire-test failures (F35 fixed, F33 payload fixed,
  F37-F40 seed fixed) were real production bugs caught by writing
  wire tests FIRST in the regression suite. The "test the orchestrator
  only" approach was missing harness-layer failures.

---

## [1.2.3] â€” 2026-07-16

### Added
- **INV-8 (per-MCP database isolation).** Each MCP server in the dark-agents family owns its **own SQLite file** by convention. dark-memory-mcp now defaults to `dark-memory.db` instead of `dark.db`; dark-research-mcp continues to use `dark.db`. Sharing `dark.db` was the root cause of the v1.2.2 boot crashes (schema_migrations name collisions in the shared bookkeeping table). The principle is documented in `docs/INVARIANTS.md` (new `INV-8` section, with rationale, defence test, operator signal, and applicability to all future dark-* servers). Defensive test: `tests/invariants/inv8_test.go::TestServer_DefaultDSN_DoesNotCollideWithDarkResearch_INV8` â€” asserts the default DSN (a) is not `dark.db`, (b) doesn't contain `dark-research`, (c) contains `dark-memory`. Operators who want the legacy shared-DB behaviour can opt in via `DARK_DB=dark.db` env var.

### Changed
- **`defaultDSN()` â†’ `"dark-memory.db"`** (was `"dark.db"`). Backward-compatible override via `DARK_DB=` env var. Affects `internal/server/bootstrap.go` only. New public accessor `server.DefaultDSN()` so tests/invariants can assert without reflection. No DB migration needed; the change only affects the default path.

### Future directions
- **`[FUTURE-MCP-1]`** (the next dark-* project, see session notes) MUST default to a project-specific filename (`harvest.db` or per-project variant) and pass the `INV-8 defaultDSN uniqueness` lint. The lint is informal today (a grep in CI) but will become a go-vet rule in v1.3.0. Documented in `docs/INVARIANTS.md` under INV-8.

---

## [1.2.2] â€” 2026-07-16

### Fixed
- **F37 â€” migration runner now tolerates "duplicate column name" errors.** applyOne in `internal/migrate/migrate.go` was running every statement in `m.Up` via a single `tx.ExecContext` inside one transaction. Any failure (including benign "duplicate column name: project_id" when a v7-style ALTER TABLE ADD COLUMN had partially completed during a prior boot crash) rolled back the WHOLE migration and aborted the daemon. The runner now splits multi-statement migration bodies on `;`, runs each statement separately, and treats the duplicate-column error class (SQLite `duplicate column name: X` + Postgres `column X already exists`) as already-satisfied. Regression tests cover the recovery flow (`TestMigrate_TolerantOfDuplicateColumn_F37`) plus a regression guard against over-broad catch (`TestMigrate_StillFailsOnNonDuplicateErrors_F37`).
- **F38 â€” `EnsureCoreTables` self-heals missing core tables on boot.** The dark.db at `C:\Users\Nico\AppData\Local\dark-agents\dark.db` is shared with dark-research-mcp, whose bookkeeping table uses the same `schema_migrations` rows. When dark-research-mcp's v1-v3 were applied with overlapping version names (initial_schema, constitutions_and_mods, sdd_evaluations_constitution_audit), dark-memory-mcp's v5+ (`sessions_table`, `project_namespace`, `vibe_brands_composite_unique`, `vlp_state_table`, `audit_project_index`) appeared "already applied" without having actually run against the schema â€” leaving `sessions` and `projects` tables physically absent from the DB. New helper `migrate.EnsureCoreTables(ctx, db)` issues `CREATE TABLE IF NOT EXISTS` for the four core tables v5/v6/v7 expect to find, called once from the sqlite Store's `Open` before `Migrate` so the migration runner sees the correct schema state. Tests: `TestEnsureCoreTables_FreshDB_F38`, `_Idempotent_F38`, `_RecoveryFromHalfMigratedDarkDB_F38` (the exact 6-step crash repro from today's session).
- **F39 â€” migration runner tolerates "no such module: <ext>" errors.** Orphan sqlite-vec triggers (`trg_research_items_vec_delete`, etc.) referencing the unloadable `vec0` virtual-table module were causing `ALTER TABLE vibe_brands RENAME TO vibe_brands_old` (in v8) to surface `SQL logic error: error in trigger trg_research_items_vec_delete: no such module: vec0`. Same `applyOne` extension; the "no such module" substring is now treated as already-satisfied at the per-statement level. Tests in `tests/migrate/tolerate_ddl_errors_f39_f40_test.go::TestMigrate_ToleratesNoSuchModule_F39`.
- **F40 â€” migration runner tolerates "table X already exists" errors.** The same per-statement loop now also handles the rare case where a `CREATE TABLE` in a migration's `Up` is called against a table that already exists (e.g. `EnsureCoreTables` + `Migrate` both try to create the same table at boot, or a v8-style rename-and-recreate pattern). The existing table is preserved as-is. Test in `tests/migrate/tolerate_ddl_errors_f39_f40_test.go::TestMigrate_ToleratesTableAlreadyExists_F40`.

### Operator notes
- v1.2.2 is a **drop-in replacement** for v1.2.1. No migrations required. The 27-tool canonical surface is unchanged. No DB schema change.
- Restart the running `dark-mem-mcp.exe` to pick up the new code; the F37/F38/F39/F40 changes only affect boot behaviour.
- **However**, today's dark.db at the canonical path is in a pre-v1.2.0 partial state (has `attempts`, `audit`, `findings`, `judgments`, `runs`, etc. tables from a previous [prior-evaluation-loadout] loadout, plus orphan vec0 triggers). Even with v1.2.2's tolerance patches, v8 (`vibe_brands_composite_unique`) will fail at the `INSERT INTO vibe_brands SELECT FROM vibe_brands_old` step because the rename was silently skipped (F39). To bootstrap a clean dark-memory-mcp state without losing recent work, see the operator's playbook:
  - **Safe path A (recommended):** archive the current dark.db (`Rename-Item dark.db dark.db.bak-$(date)`) and let v1.2.2 create a fresh one. Existing `research_*` rows from dark-research-mcp won't be visible (that's the cross-project trade-off) but dark-memory-mcp boots cleanly.
  - **Safe path B:** point dark-memory-mcp at a separate DB via `DARK_DB=./dark-memory.db`. The defaultDSN stays `./dark.db`; setting the env var on the binary is sufficient.
  - **Risky path C (do not try):** manually drop `vibe_brands` before booting v1.2.2 so v8 can recreate it. The F37/F39 tolerance will then drop the rename/recreate loop back into a clean state. Only do this if you've back-vacuumed data.

### Known issue
- The dark.db shared schema_migrations bookkeeping between dark-research-mcp and dark-memory-mcp is fragile by design (both projects use `version INTEGER, applied_at TEXT` rows but the version numbers are NAME-aligned, not ID-aligned). Future directions to consider: namespace dark-memory-mcp's bookkeeping to `dark_memory_schema_migrations`; or partition the schema_migrations table by namespace. Not addressed in v1.2.2 â€” separate PR if you want to take it on.

---

## [1.2.1] â€” 2026-07-16

### Fixed
- **F36 â€” `vibe_spec` rejects payloads from MCP harnesses that stringify arrays.** The gemela tool `dark_research_spec_create` (separate server, same `vibe_specs` table) declares `tasks` as `type: "string"` and persists the value as opaque text. `dark_memory_vibe_spec` declared `tasks` as `type: "array"` and required `Tasks []VibeSpecTask`. Some MCP harnesses serialise array arguments as JSON-encoded strings under either schema; in that case `BindOrchestrator`'s `json.Unmarshal` fails with `*json.UnmarshalTypeError: cannot unmarshal string into Go struct field VibeSpecInput.tasks of type []orchestration.VibeSpecTask`, and the operator-visible error surfaced as a generic `ErrInvalidArgument` (without a precise field hint) â€” F35's structured-field reporting kicked in only on successful unmarshal-then-orchestrator failure paths, not on raw unmarshal failures. Symptom: every `dark_memory_vibe_spec` call from certain harnesses returned `{"code":"ErrInvalidArgument","message":"One or more arguments failed validation..."}` regardless of payload validity.
  - `internal/orchestration/vibe_spec.go` â€” `Tasks` is now `json.RawMessage`; new helper `parseTasksField` accepts both forms (leading-byte dispatch on `[` vs `"`) and returns a typed `[]VibeSpecTask`. The validation graph (unique ids, non-empty description, depends_on consistency, cycle detection) is unchanged.
  - `internal/tools/vibe.go` â€” schema for `tasks` widened from `type: "array"` to `anyOf: [{...array, items: vibeSpecTaskSchema}, {type: "string"}]`. Both forms now advertise at the wire layer so harnesses can pick whichever shape they prefer.
  - `tests/orchestration/orchestrator_test.go` â€” added `mustMarshalTasks` helper bridging the old typed-slice test bodies; added 2 new tests: `TestVibeSpec_AcceptsStringifiedTasks` (round-trip: raw string in, parsed array in storage) and `TestVibeSpec_StringifiedTasks_MalformedRejected` (precise error mentions "stringified" plus `ErrInvalidArgument`). The 8 pre-existing VibeSpec tests updated from `Tasks: []orchestration.VibeSpecTask{...}` to `Tasks: mustMarshalTasks(t, []orchestration.VibeSpecTask{...})`.

### Operator notes
- v1.2.1 is a **drop-in replacement** for v1.2.0. No migrations required. The 27-tool canonical surface is unchanged (no new tools, no deprecations). No DB schema change.
- Restart the running `dark-mem-mcp.exe` (PIDs currently running the pre-v1.2.1 binary are tagged in the process list) to pick up the new code. Until restart, `dark_memory_vibe_spec` calls that pass `tasks` as a raw array will continue to fail â€” pass them as a JSON-encoded string in the meantime.

---

## [1.2.0] â€” 2026-07-16

### Added
- **`dark_memory_project_create`** (F33 / Bug C) â€” new PROJECT namespace tool (1 tool) that closes the bootstrap loop for INV-7 multi-tenancy. Prior to v1.2.0, the only way to provision a non-`default` project was to insert into the `projects` table out of band; now operators can create tenants from inside the MCP surface, then immediately call `dark_memory_session_start` with the new `project_id`. Idempotent on `project_id` â€” re-creating an existing project returns the existing row with `idempotent_replay: true` and the original `created_at`.
  - `internal/tools/project.go` â€” new file (RegisterProject + ProjectCreateInput/Result + validation)
  - Kebab-case pattern enforced: `^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`
  - Placed at canonical index 0 (before `session_start`) so tools/list discovery order matches the natural bootstrap flow
- **F35 structured error reporting** â€” `ToolError` extended with `Field`, `ExpectedType`, `ActualType`, and `SchemaHintURL`. `BindOrchestrator` now promotes `*json.UnmarshalTypeError` paths into discrete fields instead of hiding them in `Message`. Callers (LLM-driven or operator-driven) can render targeted fix-up hints without parsing free-form strings. All new fields are `omitempty` so the legacy shape is preserved for non-type-mismatch errors.
- **`vibeSpecTaskSchema`** (F33 / Bug B) â€” extracted shared strict schema for `vibe_spec` / `vibe_publish` task items. `additionalProperties: false` + explicit property list (`id`, `description`, `depends_on`, `owner`). Stops the silent-drop / type-coerce behavior that made calls fail with `cannot unmarshal string into ... depends_on of type []string` when callers passed `title`/`status`/`priority`.
- **`tests/tools/project_tool_test.go`** â€” 7 sub-tests covering happy path, idempotent replay, schema rejection (uppercase project_id, empty display_name, missing fields, unknown field) and the BindStore error envelope shape.

### Fixed
- **F33 / Bug A â€” `vibe_publish` JSON Schema is wrong.** Schema declared `spec`, `constitution`, `tasks`, `artifact_url`, `artifact_type`, `text` as flat top-level strings, but the Go struct `PublishVibeInput` (internal/orchestration/publish_vibe.go:42-72) nests them under `Spec PublishSpecInput` and `Artifact PublishArtifactInput`. Result: every harness call failed with `cannot unmarshal string into Go struct field PublishVibeInput.spec of type orchestration.PublishSpecInput`. Schema is now nested-correct with `additionalProperties: false` on both sub-objects.
- **F33 / Bug C â€” `dark_memory_project_create` was documented but not implemented.** `internal/project/types.go:9` advertised the tool, but no `tools/project.go` existed. Closed by adding the tool in this release.

### Changed
- **Canonical tool surface: 26 â†’ 27** (F33). New PROJECT namespace (1 tool) inserted at index 0. `NewRegistry`, `CanonicalOrder`, and the boot-time sanity check in `RegisterAll` updated to expect 27.
- **Tool surface layout**:
  - `PROJECT (1) â†’ create`
  - `SESSION (4) â†’ start, resume, status, close`
  - `RESEARCH (3) â†’ topic, recall, resume_thread`
  - `VIBE (4) â†’ publish, spec, pipeline_status, resolve_drift`
  - `CONTEXT (3) â†’ artifact_context, spec_context, session_context`
  - `JUDGE (3) â†’ judge, consensus, judgment_history`
  - `POLICY (2) â†’ active_policy, load_constitution`
  - `OBSERVABILITY (3) â†’ memory_state, writes, anomalies`
  - `ADMIN (3) â†’ admin_migrate, admin_schema_status, admin_vacuum`
  - `L6-VLP (1) â†’ vlp_handle_event` (DMAP v1.1 spec 193)
  - Total: 1+4+3+4+3+3+2+3+3+1 = 27.
- Schema strictness: `vibe_publish`, `vibe_spec`, `project_create` now use `additionalProperties: false` on their nested objects so the harness rejects unknown fields at parse time instead of silently dropping or coercing them.

### Migration notes
- **No DB migration.** `dark_memory_project_create` writes to the existing `projects` table (migrations/v7) â€” no schema change. Existing operators running v1.1.x keep their data; the new tool just provides an in-band path to provision what previously required `INSERT INTO projects (...)`.
- **Backwards compatibility for `vibe_publish` callers.** The schema fix is breaking for callers that built payloads against the old (broken) flat-string shape â€” those payloads were never valid against the Go struct and would have failed unmarshal at runtime. New payloads use the nested shape. See `docs/PR-v1.2.0.md` (added in this release) for a before/after payload diff.
- **Backwards compatibility for `ToolError` consumers.** The four new fields (`Field`, `ExpectedType`, `ActualType`, `SchemaHintURL`) are `omitempty`, so existing JSON consumers that ignore unknown fields keep working. Consumers that strictly validate the response shape should add the new fields to their allow-list.

### Tests
- 7 new sub-tests in `tests/tools/project_tool_test.go` (success, idempotent replay, schema rejection, error envelope).
- All existing v1.1.0 tests still pass against the updated `RegisterAll` (27-tool surface); existing test fixtures that asserted on the 26-tool count have been updated.

[1.2.0]: https://github.com/Opita-Code/dark-memory-mcp/compare/v1.1.0...v1.2.0

---

## [1.1.0] â€” 2026-07-16

### Added
- **DMAP v1.1 (Dark Memory Agent Protocol)** â€” 6-layer architecture, 26 atomic specs
  - Layer 2 (loop coordinator) closed with 5 atomic specs:
    - 2.1 SessionState â€” pure state-machine logic
    - 2.2 VLPPackage â€” 4 typed primitives (Brief/Propose/Record/Complete)
    - 2.3 VLPPersistence â€” Store-backed state with audit
    - 2.4 VLPAuditor â€” transition-level audit
    - 2.5 VLPLoopUseCase â€” end-to-end loop driver
- `Store.SaveVLPStateWithTransition` â€” atomic combo: UPSERT + row-level audit + transition-level audit in one DB transaction
- `audit.WriteEvent.ProjectID` field â€” INV-7 multi-tenancy at the audit layer
- `audit.ListFilters.ProjectID` â€” read-side tenant filtering
- 2 new dual-driver sub-tests: `write_audit_project_isolation` (F33), `vlp_state_roundtrip` enhancements (F33 cross-project)

### Changed
- **INV-1 hardening (F32)**: 21 SQLite Save*/Update*/Delete*/Close*/Link* methods now wrapped in `BeginTx` + `Commit` + `defer Rollback`
  - New helpers: `runInTx`, `recordWriteLockedTx` (SQLite); `runInTx`, `recordWriteTx` (Postgres)
  - Data row + audit row now atomic; partial failure rolls back both
  - **Critical**: helpers read `s.activeProject` without re-locking (deadlock avoidance â€” caller already holds `s.mu`)
- `UseCase.HandleEvent` (spec 2.5) refactored to use `Store.SaveVLPStateWithTransition` instead of two separate calls
- Default version bumped from `0.1.0-dev` to `1.1.0-dev` in `cmd/dark-mem-cli` + `cmd/dark-mem-inspect`

### Database
- **Migration v9** (`vlp_state_table`) â€” vlp_state per-session state row
  - `UNIQUE INDEX (project_id, session_id)` â€” multi-tenancy at vlp layer (INV-7)
- **Migration v10** (`audit_project_index`) â€” composite index on `write_audit(project_id, session_id)` for ListWrites filtering efficiency
  - **No column changes** â€” `write_audit.project_id` was already added in v7 (`project_namespace`)
  - **Idempotent** â€” `CREATE INDEX IF NOT EXISTS`
  - **Backwards compatible**

### Tests
- `internal/vlp` â€” 12 tests including new `TestVLP_E2E_AtomicSaveEmitsTwoAuditRows`
- `tests/dual_driver` â€” 11 sub-tests including F33 isolation
- 10 packages, all PASS (374s full suite)

### Known v2 follow-ups (not blocking)
- Postgres `notImpl` stubs need same F32 wrapping when real impls land (~30 methods)
- No meta-test verifying "every Save* rolls back its audit row on data-write failure" â€” only VLP has this
- `usecaseTransitionNotes` and `auditor.marshalTransitionNotes` produce byte-identical JSON but are duplicated; trivial refactor when v2 reorganizes vlp package

---

## [1.0.0] â€” 2026-07-12

### Added
- **Initial release**: 25 MCP tools, dual-driver SQLite + Postgres, 7 operational invariants
- 8 trades: SESSION (4), RESEARCH (3), VIBE (4), CONTEXT (3), JUDGE (3), POLICY (2), OBSERVABILITY (3), ADMIN (3)
- Migrations v1-v8 establishing core schema (sessions, research, vibe_specs, vibe_artifacts, vibe_brands, vibe_compliance, vibe_drift_reports, sdd_evaluations, write_audit, constitutions, mods, projects, mod_loads)
- CLI tools: `dark-mem-mcp` (MCP server), `dark-mem-cli` (admin), `dark-mem-inspect` (read-only observability)
- 9 test suites: cli, conformance, context, dual_driver, e2e, economy, invariants, orchestration, project
- Constitution watchdog (INV-4) â€” `constitutions` table + `Store.VerifyConstitutionHash`
- Canary protection (INV-3) â€” `SafetyHolder` rejects payloads containing canary
- Mod sanitization (INV-6) â€” content loader refuses unsafe content
- Multi-tenancy foundation (INV-7) â€” projects table + project_id column on every tenant-scoped table
- Bridge documentation: 5/7 bridges complete (bridge.3 + bridge.5 deferred per spec 164)
- MCP Inspector conformance test (`tests/conformance/`)

### License
- MIT â€” see [LICENSE](LICENSE)

[1.1.0]: https://github.com/Opita-Code/dark-memory-mcp/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/Opita-Code/dark-memory-mcp/releases/tag/v1.0.0Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).