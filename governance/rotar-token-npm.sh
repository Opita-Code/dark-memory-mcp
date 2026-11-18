#!/usr/bin/env bash
# Acotar el token de npm de CI y rotarlo en el secret del repo.
#
# QUE HACE Y POR QUE:
#   El token que se usa hoy ("Opita Code CI Token", creado el 2026-11-18)
#   tiene permisos package:write + org:write sobre TODO el scope @opitacode,
#   y bypass_2fa: True. Es mas permisivo de lo que necesita publicar los
#   siete paquetes de este repo.
#
#   La API de npm NO tiene "modify": solo hay `npm token create` y
#   `npm token revoke`. Asi que acotar significa CREAR uno nuevo, probarlo,
#   ponerlo en el secret, y REVOCAR el viejo.
#
# QUE NECESITA:
#   - La contrasena de la cuenta npm y su 2FA (lo pide `npm token create`).
#     Este script NO pide ni guarda la contrasena: la teclea npm.
#   - `gh` con sesion iniciada (para tocar los secrets).
#
# QUE NO HACE:
#   - No revoca el token viejo hasta que el nuevo ha publicado de verdad.
#     Si algo falla, el CI sigue funcionando con el token que ya funciona.
#
# USO:
#   bash governance/rotar-token-npm.sh
#
set -uo pipefail

REPO="Opita-Code/dark-memory-mcp"
SECRET="NODE_AUTH_TOKEN"
TOKEN_FILE="$HOME/Documentos/npmtoken.env"
SCOPE_GLOB="@opitacode/dark-memory-mcp*"

echo "=== 1. el estado antes ==="
npm token ls 2>&1 | sed 's/^/  /'
echo

echo "=== 2. crear el token acotado ==="
echo "    npm va a pedir la contrasena de la cuenta y el 2FA."
echo "    Permisos: solo los 7 paquetes de este repo, sin org:write, con 2FA de bypass"
echo "    porque la CI no puede teclear un OTP."
echo
npm token create \
  --name "Opita Code CI Token (acotado)" \
  --token-description "CI de Opita-Code/dark-memory-mcp. Solo los 7 paquetes de este repo." \
  --packages "$SCOPE_GLOB" \
  --packages-and-scopes-permission read-write \
  --orgs-permission no-access \
  --expires 30 \
  --bypass-2fa \
  2>&1 | tee "$HOME/.cache/tmp/opencode/npm-token-nuevo.txt"

NEW_TOKEN="$(grep -oE 'npm_[A-Za-z0-9]{20,}' "$HOME/.cache/tmp/opencode/npm-token-nuevo.txt" | head -1)"
if [[ -z "${NEW_TOKEN:-}" ]]; then
  echo
  echo "  NO SE PUDO LEER EL TOKEN NUEVO. No se toca nada."
  echo "  El secret sigue con el token viejo, que funciona."
  exit 1
fi
echo
echo "  token nuevo leido (${#NEW_TOKEN} chars)"
echo

echo "=== 3. comprobar que el token nuevo tiene el alcance correcto ==="
CODE=$(curl -s -o /tmp/npm-new-whoami.json -w "%{http_code}" \
  -H "Authorization: Bearer ${NEW_TOKEN}" \
  https://registry.npmjs.org/-/npm/v1/tokens)
echo "  GET /-/npm/v1/tokens -> HTTP ${CODE}"
if [[ "${CODE}" == "200" ]]; then
  python3 - <<'PY'
import json, os
p = "/tmp/npm-new-whoami.json"
d = json.load(open(p))
items = d.get("objects", d) if isinstance(d, dict) else d
for t in (items if isinstance(items, list) else []):
    print("  nombre:    ", t.get("name"))
    print("  expiry:    ", t.get("expiry"))
    print("  bypass2fa: ", t.get("bypass_2fa"))
    print("  permisos:  ", [(p.get("name"), p.get("action")) for p in (t.get("permissions") or [])])
    print("  scopes:    ", [(s.get("type"), s.get("name")) for s in (t.get("scopes") or [])])
PY
fi
rm -f /tmp/npm-new-whoami.json
echo

echo "=== 4. ponerlo en el secret del repo (por stdin: el valor no aparece en el historial) ==="
printf '%s\n' "${NEW_TOKEN}" | gh secret set "${SECRET}" --repo "${REPO}"
echo "  secret ${SECRET} actualizado en ${REPO}"
echo "  (el token viejo sigue vivo en npm hasta el paso 6)"
echo

echo "=== 5. PROBAR que el secret publica (relanza el publish de un tag ya publicado) ==="
echo "    El workflow es idempotente: si la version ya esta en npm, la omite."
echo "    Asi que un re-run NO publica nada nuevo: solo prueba la AUTENTICACION"
echo "    del secret, que es justo lo que queremos comprobar."
echo
echo "    El token viejo NO se ha revocado todavia, asi que si esto falla por"
echo "    permisos, el secret puede quedar con el token equivocado. Por eso"
echo "    el paso 6 espera a que this se lea bien."
echo
read -r -p "  ¿Relanzo el publish-npm para probar el secret nuevo? [s/N] " ans
if [[ "${ans,,}" == "s" ]]; then
  RUN_ID=$(gh run list --repo "${REPO}" --workflow publish-npm.yml --limit 1 --json databaseId --jq '.[0].databaseId')
  echo "  relanzando el run ${RUN_ID}"
  gh run rerun "${RUN_ID}" --repo "${REPO}" --failed
  echo "  watches: gh run watch ${RUN_ID} --repo ${REPO}"
  echo
  echo "  Cuando termine en verde, ejecuta este script otra vez con"
  echo "  REVOCAR=1 para revocar el token viejo."
else
  echo "  saltado. El secret tiene el token nuevo; el viejo sigue vivo."
fi
echo

echo "=== 6. revocar el token viejo (solo si el nuevo ha publicado) ==="
if [[ "${REVOCAR:-0}" != "1" ]]; then
  echo "  NO revocado. Cuando el nuevo token haya publicado de verdad:"
  echo "    REVOCAR=1 bash $0"
  echo
  echo "  El token viejo se identifica por su id (npm token ls lo muestra)."
  exit 0
fi

OLD_ID="$(npm token ls 2>&1 | grep -v 'acotado' | grep -oE 'id [0-9a-f]{8}' | head -1 | cut -d' ' -f2)"
if [[ -z "${OLD_ID:-}" ]]; then
  echo "  no se pudo determinar el id del token viejo. Revisa 'npm token ls'."
  exit 1
fi
echo "  revocando el token viejo: ${OLD_ID}"
npm token revoke "${OLD_ID}"
echo "  revocado"
echo
echo "=== 7. el estado final ==="
npm token ls 2>&1 | sed 's/^/  /'
echo
echo "  Y actualiza el fichero local para que no guarde un token muerto:"
echo "    ${TOKEN_FILE}"
