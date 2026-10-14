# LUCIDEZ-OP-RISK v1.1 — índice (tope siempre-activo; NO expandir en contexto)
#
# Este fragmento es lo ÚNICO del framework que vive permanentemente en
# contexto (~350 tokens). El texto completo vive fuera (SPEC + cuaderno)
# y se trae SOLO cuando un trigger dispara (diseño índice + fetch-on-trigger).
# Pegar el framework completo en contexto viola esta regla.

Orden:
1. El servidor manda — lo validado en código (R12 pre-flight, R13 mensajes)
   no se re-pregunta ni se duplica en prompt.
2. Ante un trigger, fetch la regla ANTES de actuar.
3. Si el fetch falla o la regla no existe: declarar riesgo abierto y no
   actuar a ciegas.

Reglas (una línea; texto completo en docs/specs/SPEC-lucidez-rrules.md §4):
- R11 inexpressible-by-default [trigger: diseñar inputs] — el camino correcto
  no requiere precisión manual; todo input copiado a mano tiene modo auto.
- R12 no garbage rows [trigger: pre-write] — ningún input inválido crea
  estado persistente; validar antes del primer write.
- R13 actionable refusal [trigger: escribir un error] — todo rechazo nombra
  qué falló, qué se esperaba, comando copiable, hasta 5 sugerencias.
- R14 noisy degradation [trigger: diseñar fallback] — ninguna degradación
  silenciosa; todo fallback emite caveat con siguiente paso.
- R15 recurring-error-to-runbook [trigger: cerrar sesión con count>=2] —
  firma recurrente exige runbook + fix + test; el evento se cierra citando
  el commit del fix.
- R16 negative test per gap [trigger: nuevo riesgo] — cada riesgo tiene su
  test adversarial TestOperatorRisk_<ID>_*; sin test no hay "mitigated".
- R17 audit never weakens [trigger: tocar pins/auditoría] — ninguna mejora
  de UX reduce fijación; auto-derivar registra QUÉ fijó + exige precondición
  verificable o caveat explícito.

Fetch:
- Reglas → docs/specs/SPEC-lucidez-rrules.md §4 (leer solo la sección R<N>).
- Instancias → agent_memory_recall tag risk:R<N> (solo la fila relevante).
