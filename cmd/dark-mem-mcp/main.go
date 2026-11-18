// Command dark-mem-mcp is the dark-memory MCP server.
//
// As of t6 (spec 1242, 2026-08-18) this binary is the single boot
// path. The v2.19.0 split into dispatcher→bridge→daemon (spec 1176
// §4.10) is removed from active use: the daemon never implemented
// initialize/tools/call, and production runs the single-binary
// legacy path that every opencode restart loads.
//
// The heavy legacy server boot (orchestrator + 49 tools + drift gate
// + federation peer + sweeper + startup-recover) lives in
// legacy_main.go to keep this file minimal. The bridge and daemon
// packages in cmd/ remain in the source tree as a frozen archive
// for future §4.10 re-activation; see ARCHITECTURE.md §boot path.
package main

import (
	"fmt"
	"os"
	"strings"
)

// main arranca el servidor MCP.
//
// LOS FLAGS. MEDIDO el 2026-11-18: el arranque por stdio es el que usan
// los harnesses (opencode lanza el binario como hijo de proceso, sin
// argumentos), asi que el modo por defecto NO cambia. Los flags son
// opt-in, y solo se usan cuando alguien arranca el servidor a mano para
// que un NAVEGADOR le hable:
//
//	dark-mem-mcp --http 127.0.0.1:8899 --origen https://memory.opitacode.com
//
// Sin `--http`, el binario sirve stdio, igual que siempre.
//
// `--origen` es la lista de origenes de navegador autorizados (separados
// por comas). Sin ella, el transporte HTTP no emite ninguna cabecera
// CORS: una pagina cualquiera no puede ni ver la respuesta.
func main() {
	opts, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "dark-mem-mcp: %v\n", err)
		fmt.Fprintf(os.Stderr, "uso: dark-mem-mcp [--http DIRECCION] [--origen ORIGEN1,ORIGEN2]\n")
		os.Exit(64)
	}
	if opts.help {
		fmt.Fprintf(os.Stderr, "uso: dark-mem-mcp [--http DIRECCION] [--origen ORIGEN1,ORIGEN2]\n")
		fmt.Fprintf(os.Stderr, "  --http DIRECCION   sirve MCP sobre HTTP (por defecto 127.0.0.1:8899)\n")
		fmt.Fprintf(os.Stderr, "  --origen ORIGENES  origenes de navegador autorizados, separados por comas\n")
		os.Exit(0)
	}
	legacyMain(opts)
}

// opciones son los flags del arranque.
type opciones struct {
	http    string
	origen string
	help    bool
}

// parseFlags lee los flags. Los argumentos que no son flags se ignoran
// con una aviso: el harness puede pasar cosas que no son del nucleo.
func parseFlags(args []string) (opciones, error) {
	var o opciones
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help":
			o.help = true
		case a == "--http" && i+1 < len(args):
			o.http = args[i+1]
			i++
		case strings.HasPrefix(a, "--http="):
			o.http = strings.TrimPrefix(a, "--http=")
		case a == "--origen" && i+1 < len(args):
			o.origen = args[i+1]
			i++
		case strings.HasPrefix(a, "--origen="):
			o.origen = strings.TrimPrefix(a, "--origen=")
		default:
			// Un argumento desconocido no es un error: el boot path lo
			// historico acepta cualquier cosa.
		}
	}
	return o, nil
}
