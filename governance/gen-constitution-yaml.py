#!/usr/bin/env python3
"""Genera constitutions/dark-agents-workspace-policy.yaml a partir de la forma
canónica (governance/workspace-policy.constitution.example.json).

Por qué generarlo en vez de escribirlo a mano: el verificador
(governance/check-constitution.sh) compara la sección `persona` del YAML con
la del ejemplo COMO CADENAS LITERALES. Copiar ese texto a mano garantiza
que en algún momento difieran por un acento o un espacio. Aquí la copia es
del mismo fichero, así que no puede divergir.

El bloque de carga de runtime (meta, scope, operational_rules, workspace,
persona, footer) sale del ejemplo. Los comentarios y los bloques que solo
existen en el contrato (update, osint, tools, redaction, ssrf, identity,
authority, refusal) se escriben aquí, en español.
"""
import hashlib
import json
import pathlib
import re

ROOT = pathlib.Path(__file__).resolve().parents[1]
EXAMPLE = ROOT / "governance" / "workspace-policy.constitution.example.json"
YAML = ROOT / "constitutions" / "dark-agents-workspace-policy.yaml"

ex = json.loads(EXAMPLE.read_text(encoding="utf-8"))


def block_scalar(text: str, indent: str = "    ") -> str:
    """Texto multilínea como bloque literal YAML (|)."""
    lines = text.split("\n")
    return "\n".join(f"{indent}{ln}" if ln else "" for ln in lines)


def one_line(value: str) -> str:
    """Un valor de persona en una sola línea, entre comillas dobles."""
    assert '"' not in value, "el valor de persona no debe llevar comillas dobles"
    return '"' + value.replace("\\", "\\\\") + '"'


def yaml_str(value: str) -> str:
    """Un escalar de una línea."""
    if re.search(r"[:#\-\[\]{}&*!|>'\"%@`]|^\s|\s$", value):
        return '"' + value.replace("\\", "\\\\").replace('"', '\\"') + '"'
    return value


head = '''# =============================================================================
# Constitution de política de workspace — dark-agents/workspace-policy v{VERSION}
# =============================================================================
#
# TL;DR: la política del operador que mantiene cada artefacto de proyecto
# dentro del workspace de ese proyecto. Este fichero es el artefacto de
# CONTRATO que exige ARCHITECTURE-V4.md §7 ("Constitution contract": un
# documento YAML versionado y SHA-pineado bajo constitutions/<name>.yaml).
# La forma que el servidor carga hoy es la copia JSON de governance/ — ver
# governance/check-constitution.sh, que es el paso de verificación que §7
# pide y que `dark-mem constitution verify` no implementa en esta build.
#
# POR QUÉ HAY TRES FORMAS
#
#   1. ESTE YAML        el contrato. Portable: no ata la política a una
#                       máquina concreta.
#   2. EL EJEMPLO JSON  la forma canónica, con los marcadores
#                       <workspace_root> y <home_root> sin sustituir.
#   3. EL JSON ACTIVO   el ejemplo instanciado con las rutas reales del
#                       operador. No se versiona (está en .gitignore) y es lo
#                       que vigila el watchdog por SHA.
#
# El contrato fija el SHA del EJEMPLO, no el de una máquina: un SHA local
# haría que el contrato dejara de ser reutilizable en cuanto otro operador
# instantiara la política con sus propias rutas.
#
# POR QUÉ JSON Y NO TOML PARA LA FORMA DE RUNTIME
#
# El watchdog guarda los bytes CRUDOS del fichero de constitution en
# constitutions.parsed_json y el inyector de persona
# (internal/recall/assemble.go, parsePersonaFromConstitution) parsea ese
# blob con json.Unmarshal. Una constitution en YAML o TOML queda guardada en
# la DB como no parseable-para-persona y su sección persona NUNCA llega al
# agente — le pasa también a la constitution builtin del repo
# (vibe-flow/constitution/dark-mem.constitution.toml, cuya sección [tone]
# tiene el mismo destino).
#
# Las secciones replican la forma de la constitution builtin del repo
# (vibe-flow/constitution/dark-mem.constitution.toml: meta, identity,
# authority, refusal, scope, operational_rules, tone, layers, footer) más la
# sección `persona` que lee el inyector.
#
# IDIOMA: toda la documentación va en español, incluida la carga útil de
# runtime (persona, scope, operational_rules, footer), porque es lo que el
# servidor inyecta al agente y lo que el agente lee.
# =============================================================================

id: dark-agents/workspace-policy
version: {VERSION}
'''

sha = hashlib.sha256(EXAMPLE.read_bytes()).hexdigest()
head += f"sha256: {sha}\n"
head += ("invariants: [INV-1, INV-2, INV-3, INV-4, INV-5, INV-6, INV-7, INV-8, INV-9, "
         "INV-10, INV-11, INV-12, INV-13, INV-14, INV-15]\n")

static = '''
# M4.1 — política de auto-actualización. Una política local de operador no se
# descarga de ningún canal, así que el mecanismo de actualización queda
# apagado; `pubkey` se deja vacío porque no hay un flujo de releases binarias
# contra el que verificar. Las constitutions que shippea el producto sí
# llevan aquí una clave ed25519 real.
update:
  enabled: false
  auto_apply: false
  notify: notify_once
  channel: mcp_resource
  check_on_start: false
  repo: opitacode/dark-memory-mcp
  pubkey: ""
  opt_out_field: "update.enabled=false in project config"

# M5 — disciplina de OSINT (R5 de dark-research).
osint:
  primary_source_required_for: [cve, version, supply-chain]
  pii_gate: mandatory
  redact_on_output: true
  rate_limits:
    osv.dev: "5/s"
    nvd: "5/30s"
    crt.sh: "1/2s"
    abuse.ch: "10/s"
    alienvault_otx: "1/s"
    htb: "5/30s"
    epss: "5/s"

# Tier-1 #5 — política de tools con deny por defecto.
tools:
  default_policy: deny
  exceptions: []
  artifact_creating: auto

# INV-13 — redacción.
redaction:
  patterns:
    - openai
    - anthropic
    - jwt
    - github_pat
    - aws
    - google
    - slack
    - bearer
    - basic_in_url
    - cookie
    - authorization
  shape_keys: ["*_token", "*_key", "*_secret", "*_password"]
  recursive: true
  depth_limit: 32

# INV-14 — política de SSRF.
ssrf:
  blocklist_cidrs:
    - 0.0.0.0/8
    - 10.0.0.0/8
    - 100.64.0.0/10
    - 127.0.0.0/8
    - 169.254.0.0/16
    - 172.16.0.0/12
    - 192.0.0.0/24
    - 192.168.0.0/16
  block_ipv4_mapped_ipv6: true
  dns_rebind_delay_ms: 50

'''

identity = '''
identity:
  agent_name: dark-agents
  agent_role: "agente operador ligado a un workspace de proyecto; todo artefacto que produce debe vivir bajo esa raíz"
  register: "técnico, preciso, defensivo por defecto; nunca performativo; nunca antropomorfizar los datos"

authority:
  body: |
    Modelo de autoridad:
      - El servidor MCP corre con la identidad del operador; la política se
        liga al proyecto con project_create(constitution_id, constitution_ver)
        y a las sesiones con session_start.
      - Cada fila de write_audit registra constitution_id@version, así que la
        política en vigor siempre es atribuible.
      - Editar esta política sin subir la versión se rechaza incondicionalmente
        (manipulación de misma versión) y el servidor no abrirá el store.
        Esa negativa es el comportamiento buscado.

refusal:
  body: |
    Rechaza, y saca el conflicto a la luz ante el operador en vez de hacerlo
    en silencio, cuando:
      - Una petición situaría un artefacto de proyecto fuera del workspace
        del proyecto (WS-2, WS-3).
      - No se puede establecer la raíz de workspaces de un proyecto (WS-1).
      - La política activa no es la que el proyecto tenía enlazada (WS-5).
      - Una regla de governance y la petición no coinciden. Nombra las dos;
        no elijas una ganadora.
'''

out = [head, static, identity]
out.append("\nmeta:\n")
for k, v in ex["meta"].items():
    out.append(f"  {k}: {yaml_str(v)}\n")

out.append("\nscope:\n  body: |\n")
out.append(block_scalar(ex["scope"]["body"]) + "\n")

out.append("\noperational_rules:\n  body: |\n")
out.append(block_scalar(ex["operational_rules"]["body"]) + "\n")

out.append("\nworkspace:\n")
ws = ex["workspace"]
out.append(f"  root_pattern: {yaml_str(ws['root_pattern'])}\n")
out.append("  projects:\n")
for k, v in ws["projects"].items():
    out.append(f"    {k}: {yaml_str(v)}\n")
out.append("  forbidden_roots:\n")
for r in ws["forbidden_roots"]:
    out.append(f"    - {yaml_str(r)}\n")
out.append(f"  forbidden_roots_note: {yaml_str(ws['forbidden_roots_note'])}\n")

out.append('''
# La sección persona que lee el inyector. Cada campo va en una sola línea:
# governance/check-constitution.sh compara estos tres valores contra la forma
# canónica con grep, y un bloque YAML plegado no compararía literal.
persona:
''')
for field in ("voice", "claims_policy", "tone"):
    out.append(f"  {field}: {one_line(ex['persona'][field])}\n")

out.append("\nfooter:\n  text: " + yaml_str(ex["footer"]["text"]) + "\n")

YAML.write_text("".join(out).replace("{VERSION}", ex["meta"]["version"]), encoding="utf-8")
print(f"escrito: {YAML} ({len(''.join(out).splitlines())} lineas)")
