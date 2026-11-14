# El uso del proveedor: lo que un veredicto costó

> [El pipeline del juez](judge-pipeline-v4.md) · [El gate](GATE.md) ·
> [Los invariantes](INVARIANTS.md)

## QUÉ ES

Hasta ahora, una llamada al proveedor era una caja negra: el veredicto
sabía si el artefacto estaba alineado, con cuánta confianza, contra qué
proveedor y en cuántos milisegundos. **No sabía qué costó.** El proveedor
sí lo dice —cada respuesta trae `usage`— y el servidor lo recibía y lo
tiraba.

Este documento explica lo que ahora se expone: cuántas peticiones al
proveedor hizo un veredicto y cuántos tokens consumieron.

## DÓNDE NACE EL DATO

El proveedor responde con la forma de OpenAI:

```json
{"usage":{"prompt_tokens":1071,"completion_tokens":295,"total_tokens":1366,
          "prompt_tokens_details":{"cached_tokens":128},
          "completion_tokens_details":{"reasoning_tokens":207}}}
```

Los dos bloques `details` son opcionales: no todos los proveedores los
llenan. Y un proveedor que no los llena dice **cero**, no «desconocido». La
diferencia importa para la caché: los tokens cacheados se cobran de otra
manera, así que un coste que los finge frescos se equivoca.

## LA FORMA

La cuenta viaja en el `verdict_json`, que es lo que lee el runtime:

```json
"usage":{"requests":3,"tokens":{"prompt":5723,"completion":1212,
                                 "total":6935,"cached":768,"reasoning":873}}
```

- `requests`: las peticiones HTTP que se hicieron al proveedor.
- `tokens`: la suma de lo que esas peticiones reportaron, o `null` cuando
  no todas lo hicieron.

El campo `usage` **no aparece** cuando no se llamó a ningún proveedor.

## LAS TRES REGLAS

**1. Un dato que falta no es un cero.** Un proveedor que no reporta `usage`
no ha hecho la petición gratis: la petición ocurrió y se factura. Por eso
las peticiones se cuentan siempre, y los tokens son `null` —no `0`— cuando
falta alguno. Publicar una suma parcial como si fuera el total sería un
número falso con nombre de medida.

**2. Las peticiones que fallaron también cuentan.** El juez reintenta una
vez cuando la respuesta del modelo no trae un JSON legible, y si el
reintento tampoco vale, la cadena NLI da el veredicto. En el peor caso son
**tres peticiones por parte juzgada**, y las tres se cobran. La cuenta solo
suma lo que se sabe: si alguna de ellas no reportó su uso, el total es
`null` y las peticiones siguen contadas.

**3. Lo que no ocurrió no se cobra.** Si el juez respondió a la primera,
la cadena NLI no se ejecuta: no hay nada que sumar ni nada que perder.

## LO MEDIDO

Con el proveedor MiniMax-M3, medido el 2026-11-09 sobre el camino real del
juez:

| Documento | Peticiones | Tokens | Lo que declaraba el runtime entonces |
|---|---|---|---|
| 1 parte | 3 | ~3700 | 1 llamada |
| 2 partes | 6 | 6935 | 2 llamadas |

El runtime cuenta **muestras** (partes × consenso); el proveedor factura
**peticiones**. Con esa diferencia, el «coste por publicación» que el
runtime publica no era el coste: era otra medida con el mismo nombre.

La columna dice «entonces» porque es una medición del 2026-11-09, y porque
desde entonces el runtime de `opita-code` **lee este campo**: publica
`peticiones` y `tokens` con las mismas tres reglas. Ese trabajo está en el
otro repo; lo que hace falta para que el número se vea en línea es que este
PR entre.

## LO QUE NO HACE

- **No calcula dinero.** El precio del proveedor es del operador. Aquí solo
  hay tokens; la tarifa la declara quien opera.
- **No estima.** Un divisor de «4 bytes por token» es una convención, no una
  medida. Si el proveedor no lo dice, aquí no está.
- **No cambia el veredicto.** La cuenta viaja al lado; no participa en la
  decisión.

## DÓNDE ESTÁ EL CÓDIGO

| Pieza | Qué hace |
|---|---|
| `internal/llm/chatwire/usage.go` | La forma del uso y la cuenta que se niega a publicar una suma parcial como total |
| `internal/v4alpha/judge/v4judge/llm_judge.go` | El juez cuenta sus peticiones, con el reintento incluido |
| `internal/nli/chat.go` | La cadena NLI cuenta las suyas, también cuando fallan |
| `internal/orchestration/drift_judge.go` | Las dos cuentas se suman y viajan en el `verdict_json` |
