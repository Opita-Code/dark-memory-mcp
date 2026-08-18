# Diagramas — dark-memory-mcp v2.19.0

> **TL;DR**: Este documento agrupa todos los diagramas del proyecto
> en un solo lugar. Cada diagrama está en formato **Mermaid** (que
> renderiza en GitHub, GitLab, VS Code y la mayoría de los
> visualizadores Markdown). Los diagramas se referencian desde
> otros documentos (no se duplican).

---

## 0. Convención de colores

Todos los diagramas usan la misma convención:

| Color | Significado |
|---|---|
| 🟢 Verde | Estado saludable, ruta correcta |
| 🟡 Amarillo | Advertencia, decisión pendiente |
| 🔴 Rojo | Error, estado terminal |
| 🔵 Azul | Flujo normal, datos en tránsito |
| ⚪ Gris | Estado neutro, componente pasivo |

En Mermaid, esto se aplica con `classDef` y `class`.

---

## 1. Vista de capas (deployment)

```mermaid
graph TD
    subgraph "Cliente IA"
        A["opencode / Claude Desktop / Cursor"]
    end

    subgraph "Capa 2: Wrapper"
        B["dark-mem-mcp.exe<br/>30 MB · dispatcher"]
    end

    subgraph "Capa 3: Bridge"
        C["dark-mem-mcp-bridge.exe<br/>3.9 MB · forwarding"]
    end

    subgraph "Capa 4: Daemon"
        D["dark-mem-mcp-daemon.exe<br/>27 MB · long-lived"]
    end

    subgraph "Capa 5: Persistencia"
        E["SQLite<br/>~/.local/share/dark-memory-mcp/dark.db"]
        F["Postgres opcional<br/>DARK_DB=postgres://…"]
    end

    A -- JSON-RPC por stdio --> B
    B -- execve --> C
    C -- socket Unix / named pipe --> D
    D -- driver sqlite --> E
    D -- driver pgx --> F

    classDef normal fill:#cce5ff,stroke:#0066cc
    classDef healthy fill:#d4edda,stroke:#28a745
    classDef warning fill:#fff3cd,stroke:#ffc107
    classDef error fill:#f8d7da,stroke:#dc3545
    classDef passive fill:#e2e3e5,stroke:#6c757d

    class A,C normal
    class B,D healthy
    class E,F passive
```

**Lectura**: deployment físico de los 3 binarios + 2 posibles DBs.
**Usado en**: [ARCHITECTURE.md §2.1](./ARCHITECTURE.md#21-vista-de-capas-la-fuente-de-verdad).

---

## 2. Vista lógica (namespaces)

```mermaid
graph LR
    subgraph "PROJECT (1)"
        N1[project_create]
    end
    subgraph "SESSION (7)"
        N2[session_start<br/>session_resume<br/>session_heartbeat<br/>session_status<br/>session_close<br/>session_recover<br/>session_resurrect]
    end
    subgraph "RESEARCH (3)"
        N3[research_topic<br/>research_recall<br/>research_resume_thread]
    end
    subgraph "BOOTSTRAP (3)"
        N4[agent_bootstrap<br/>agent_recommend_companions<br/>agent_detect_environment]
    end
    subgraph "VIBE (4)"
        N5[vibe_publish<br/>vibe_spec<br/>pipeline_status<br/>resolve_drift]
    end
    subgraph "CONTEXT (4)"
        N6[recall<br/>artifact_context<br/>spec_context<br/>session_context]
    end
    subgraph "AGENT_MEMORY (10)"
        N7[agent_memory_save<br/>agent_memory_list<br/>agent_memory_recall<br/>agent_memory_get<br/>agent_memory_update<br/>agent_memory_archive<br/>agent_memory_delegate<br/>agent_memory_entities<br/>subagent_register<br/>subagent_unregister]
    end
    subgraph "MINDSET (1)"
        N8[mindset_apply]
    end
    subgraph "DELEGATION (1)"
        N9[delegate_intent]
    end
    subgraph "JUDGE (3)"
        N10[judge<br/>consensus<br/>judgment_history]
    end
    subgraph "POLICY (2)"
        N11[active_policy<br/>load_constitution]
    end
    subgraph "OBSERVABILITY (4)"
        N12[health_ping<br/>memory_state<br/>writes<br/>anomalies]
    end
    subgraph "ERROR_OBS (4)"
        N13[error_summary<br/>error_list<br/>error_get<br/>error_resolve]
    end
    subgraph "ADMIN (3)"
        N14[admin_migrate<br/>admin_schema_status<br/>admin_vacuum]
    end
    subgraph "L6-VLP (1)"
        N15[vlp_handle_event]
    end
    subgraph "EMBEDDER (1)"
        N16[embedder_setup_prompt]
    end

    N1 --> T["Total: 57 tools"]
    N2 --> T
    N3 --> T
    N4 --> T
    N5 --> T
    N6 --> T
    N7 --> T
    N8 --> T
    N9 --> T
    N10 --> T
    N11 --> T
    N12 --> T
    N13 --> T
    N14 --> T
    N15 --> T
    N16 --> T

    classDef totalNode fill:#d4edda,stroke:#28a745,stroke-width:3px
    class T totalNode
```

**Lectura**: 17 namespaces → 57 tools.

**Usado en**: [ARCHITECTURE.md §2.2](./ARCHITECTURE.md#22-vista-lógica-los-57-nombres).

---

## 3. Vista de datos (las 18 tablas)

```mermaid
graph TD
    subgraph "Auditoría"
        T1[write_audit<br/>~100k filas/mes]
        T2[error_clusters]
        T3[judgment_history]
    end

    subgraph "Dominio"
        T4[sessions]
        T5[projects]
        T6[agent_memory]
        T7[constitutions]
        T8[mindsets]
        T9[specs]
        T10[artifacts]
        T11[drift_reports]
        T12[vlp_events]
    end

    subgraph "Coexistencia"
        T13[research_runs]
        T14[research_items]
        T15[research_links]
    end

    subgraph "Sistema"
        T16[sqlite_master]
        T17[schema_migrations]
        T18[gate_refusals]
    end

    T1 --> T4
    T1 --> T6
    T4 --> T5
    T9 --> T10
    T10 --> T11
    T11 --> T3
    T12 --> T4

    classDef audit fill:#fff3cd,stroke:#ffc107
    classDef domain fill:#cce5ff,stroke:#0066cc
    classDef coexist fill:#f8d7da,stroke:#dc3545
    classDef system fill:#e2e3e5,stroke:#6c757d

    class T1,T2,T3 audit
    class T4,T5,T6,T7,T8,T9,T10,T11,T12 domain
    class T13,T14,T15 coexist
    class T16,T17,T18 system
```

**Lectura**: 4 categorías de tablas, `write_audit` como nodo
central.

**Usado en**: [ARCHITECTURE.md §2.3](./ARCHITECTURE.md#23-vista-de-datos-las-18-tablas).

---

## 4. Flujo del vibe-loop (la verificación)

```mermaid
sequenceDiagram
    participant U as Usuario
    participant A as Agente
    participant S as Spec
    participant K as Código
    participant J as Judge
    participant D as DB

    U->>A: Quiero feature X
    A->>S: vibe_spec(vibe_case, tasks)
    S->>D: INSERT specs row
    D-->>S: spec_id
    A->>K: implementar X
    K->>D: Save* métodos
    D-->>K: write_audit rows
    A->>S: vibe_publish(artifact, spec)
    S->>J: drift_judge(artifact)
    J-->>S: aligned | drift_detected | needs_human
    S->>D: INSERT drift_reports
    alt aligned
        S->>D: ship_lane
        S-->>A: artifact shipped
    else drift_detected
        S-->>A: fix + re-publish
    else needs_human
        S-->>A: STOP, surface to operator
    end
```

**Lectura**: el ciclo completo desde intención del usuario hasta
ship.

**Usado en**: [README.md](../README.md#el-vibe-loop-un-paradigma-nuevo).

---

## 5. Lifecycle del daemon (FSM)

```mermaid
stateDiagram-v2
    [*] --> not_running
    not_running --> starting: dispatcher execve bridge
    starting --> ready: ready-file created
    ready --> ready: accept loop
    ready --> shutting_down: idle timeout 30m
    ready --> shutting_down: SIGTERM
    starting --> not_running: error
    ready --> not_running: fatal
    shutting_down --> [*]: clean
```

**Lectura**: 4 estados, transiciones del daemon.

**Usado en**: [RUNBOOK.md §Daemon lifecycle](./RUNBOOK.md#daemon-lifecycle).

---

## 6. Lifecycle del bridge (FSM)

```mermaid
stateDiagram-v2
    [*] --> spawning
    spawning --> connecting: execve OK
    spawning --> [*]: execve FAIL
    connecting --> ready: dial OK
    connecting --> spawning: dial FAIL, 3 retries
    ready --> ready: forward frames
    ready --> [*]: stdin cerrado
    ready --> spawning: daemon crashed
```

**Lectura**: 4 estados, el bridge se re-spawn si el daemon se cae.

**Usado en**: [ARCHITECTURE.md §2.1](./ARCHITECTURE.md#21-vista-de-capas-la-fuente-de-verdad).

---

## 7. Auditoría de un Save (secuencia)

```mermaid
sequenceDiagram
    participant C as Cliente
    participant H as Handler
    participant S as Store
    participant D as DB

    C->>H: tools/call agent_memory_save
    H->>H: validate WriteContext
    H->>S: Save(row, *WriteContext)
    S->>D: BEGIN TRANSACTION
    S->>D: INSERT INTO agent_memory
    S->>D: INSERT INTO write_audit
    alt éxito
        S->>D: COMMIT
        D-->>S: OK
        S-->>H: row + audit_id
        H-->>C: {result: row}
    else failure
        S->>D: ROLLBACK
        D-->>S: error
        S-->>H: error
        H->>D: INSERT INTO error_clusters
        H-->>C: error envelope
    end
```

**Lectura**: cada Save tiene 2 INSERTs en la misma transacción.
Esto es **INV-1**.

**Usado en**: [INVARIANTS.md §INV-1](./INVARIANTS.md#inv-1--auditoría-en-el-camino-de-escritura).

---

## 8. Despliegue de un agente con subagente

```mermaid
sequenceDiagram
    participant P as Principal (operator)
    participant D as delegate_intent
    participant M as mindset_apply
    participant C as agent_memory_delegate
    participant R as subagent_register
    participant S as Sub-agente

    P->>D: ¿delegar a sub-agente?
    D->>D: DECIDE
    alt DELEGATE
        D->>M: compose system_prompt
        M-->>D: system_prompt
        D->>C: build delegation_context
        C-->>D: delegation_context
        D->>R: register C2 binding
        R-->>D: subagent_id
        D->>P: bundle {system_prompt, delegation_context, subagent_id}
        P->>S: spawn with bundle
        S->>S: trabajo
        S-->>P: output
        P->>R: unregister
    else HANDLE
        P->>P: handle inline
    else REFUSE
        P->>P: refuse
    end
```

**Lectura**: el patrón de delegación, paso a paso.

**Usado en**: [skills/dark-memory/SKILL.md §1.7](../.config/opencode/skills/dark-memory/SKILL.md).

---

## 9. Stack de decisiones (cómo se elige)

```mermaid
graph TD
    Start([Necesito hacer X]) --> Q1{X ya conocido?}
    Q1 -->|Sí| UseRecall[agent_memory_recall]
    Q1 -->|No| Q2{Sobre dark-memory?}
    Q2 -->|Sí| UseTools[57 tools MCP]
    Q2 -->|No| Q3{Necesito browser?}
    Q3 -->|Sí| UseCopilot[dark-copilot 35 tools]
    Q3 -->|No| Q4{Necesito OSINT?}
    Q4 -->|Sí| UseResearch[dark-research 13 intents]
    Q4 -->|No| Q5{Es código?}
    Q5 -->|Sí| Build[build skill]
    Q5 -->|No| Plan[plan skill]

    UseRecall --> End([Respuesta])
    UseTools --> End
    UseCopilot --> End
    UseResearch --> End
    Build --> End
    Plan --> End

    classDef decision fill:#fff3cd,stroke:#ffc107
    classDef action fill:#cce5ff,stroke:#0066cc
    classDef start fill:#d4edda,stroke:#28a745
    classDef endNode fill:#f8d7da,stroke:#dc3545

    class Start start
    class End endNode
    class Q1,Q2,Q3,Q4,Q5 decision
    class UseRecall,UseTools,UseCopilot,UseResearch,Build,Plan action
```

**Lectura**: el árbol de decisión para qué herramienta usar.

**Usado en**: el sistema-prompt de dark-agent.

---

## 10. Notas técnicas

### 10.1 Renderizado de Mermaid

Mermaid renderiza en:

- GitHub: nativo en el visor de PRs.
- GitLab: nativo desde 13.0.
- VS Code: con la extensión "Markdown Preview Mermaid Support".
- IntelliJ: con el plugin "Markdown Navigator".
- Vim: con `markdown-preview.nvim`.

### 10.2 Edición de los diagramas

Para modificar un diagrama:

1. Edita el bloque Mermaid en este archivo.
2. Verifica en GitHub después del push.
3. Si la edición es profunda (cambio de capas), actualiza el
   `## Change log` al final de este documento.

### 10.3 Limitaciones

- Mermaid no tiene iconos personalizados. Si necesitas íconos
  específicos, usa un PNG externo.
- Mermaid tiene un límite de ~1000 nodos. Para diagramas más
  grandes, divide en varios.

---

## 11. Change log

- **2026-08-14** (v2.19.0): diagrama 1 actualizado para incluir
  dispatcher/bridge/daemon. Diagrama 6 (lifecycle del bridge) es
  nuevo.
- **2026-08-13** (v2.18.0): sin cambios. Diagrama 2 actualizado
  para incluir el nuevo `judge_list_personas` (53 → 54 tools,
  JUDGE ganó 1 tool).
- **2026-08-11** (v2.15.0): diagramas 4, 7, 8 creados.
- **2026-08-08** (v2.13.0): diagramas 1, 2, 3, 5, 9 originales.
