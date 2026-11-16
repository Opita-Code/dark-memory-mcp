#!/usr/bin/env bash
# governance/check-persona-text.sh — la persona es dato, no instruccion.
#
# POR QUE EXISTE
# La seccion persona de la constitution es el UNICO canal que el inyector
# (internal/recall/assemble.go:parsePersonaFromConstitution) mete en cada
# recall. La v1.2.0 metio reglas ahi a proposito, con el argumento de que
# "una regla que no se inyecta no es dura". Eso convierte un fichero de
# politica en un canal de voluntad: cualquier maquina que escriba en ese
# campo cambia lo que el agente obedece, sin auditoria y sin atribucion.
#
# Este guard mantiene el canal en su sitio: la persona DESCRIBE registro,
# idioma y tono. Si vuelve a ordenar, el check falla y la constitution no
# se activa. Las reglas se aplican en codigo (watchdog de SHA, write_audit,
# dm-flow.sh check), no en texto inyectado.
#
# USO
#   governance/check-persona-text.sh <texto|fichero> [...]
#   cat texto | governance/check-persona-text.sh
#
# CODIGOS: 0 limpio · 64 uso · 65 contiene instrucciones

set -uo pipefail

if [[ $# -eq 0 ]]; then
  text="$(cat)"
else
  text=""
  for arg in "$@"; do
    if [[ -f "$arg" ]]; then
      text+="$(cat "$arg")"$'\n'
    else
      text+="$arg"$'\n'
    fi
  done
fi

if [[ -z "${text//[[:space:]]/}" ]]; then
  echo "check-persona-text: no hay texto que revisar" >&2
  exit 64
fi

# UTF-8 para que \b y las clases con tilde funcionen como palabras.
export LC_ALL="${LC_ALL:-C.UTF-8}"

# nombre|regex. Se busca en una sola linea: los campos de persona son
# cadenas JSON de una linea, asi que el contexto de frase esta acotado.
RULES=(
  'reasignacion de rol|(act[[:space:]]+as|act[úu]a como|you are now|eres ahora|a partir de ahora|from now on)'
  'anulacion de instrucciones|(ignora|ignore|olvida|disregard|olvid[áa])[^.]{0,40}(instrucci|instruction|anterior|previous|arriba|above)'
  'canal de sistema|(system[ _-]?prompt|prompt del sistema|mensaje de sistema)'
  'imperativo en segunda persona|(^|[^[:alnum:]_])(debes|deber[áa]s|tienes que|ten[ée]s que|no escribas|no uses|no publiques|escrib[íi]|escribe|us[áa]|usa|razona|publica|verifica|opera|corrige|arregla)([^[:alnum:]_]|$)'
  'imperativo en ingles|(^|[^[:alnum:]_])(you must|you should|you shall|do not|do not|never|always)([^[:alnum:]_]|$)'
  'orden sobre una tool|(llama|call|invoca|invoke)[^.]{0,30}(tool|herramienta)'
)

rc=0
for rule in "${RULES[@]}"; do
  name="${rule%%|*}"
  re="${rule#*|}"
  if hit="$(printf '%s\n' "$text" | grep -E -i -o -m1 -e "$re")"; then
    printf 'check-persona-text: %s -> "%s"\n' "$name" "$hit" >&2
    rc=65
  fi
done

if [[ "$rc" -ne 0 ]]; then
  cat >&2 <<'MSG'
check-persona-text: la persona vuelve a mandar en vez de describir.
  Una regla de politica se aplica en codigo y deja rastro; no se inyecta
  como instruccion. La persona solo describe registro, idioma y tono.
MSG
  exit 65
fi

exit 0
