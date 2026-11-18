// Package server — server.go: the Server type, registry wiring, and
// stdio transport. This is the entry point the cmd/dark-mem-mcp
// binary uses (see ../../cmd/dark-mem-mcp/main.go).
//
// Per RFC §6 step 5, after Boot returns the caller is expected to
// (a) register all canonical tools (this is where per-namespace tool
// files come in — see internal/tools/*.go), then (b) call ServeStdio
// to block on the stdio MCP transport.
//
// Tool registration is explicit (Register* functions below) rather
// than init-time magic. This keeps the boot sequence greppable and
// test-friendly: tests can construct a Server with only a subset of
// tools to exercise specific code paths.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/agentbootstrap"
	"github.com/dark-agents/dark-memory-mcp/internal/tools"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Server is the MCP server. It wraps the BootState (Store + Safety +
// Orchestrator + Registry) and the mcp-go server instance.
type Server struct {
	boot   *BootState
	mcpSrv *server.MCPServer
}

// New constructs the Server. Does NOT start the stdio transport; call
// ServeStdio for that. Tool registration must happen between New and
// ServeStdio (or via RegisterAll called from New — see flag below).
//
// mcp-go options wired here:
//   - WithToolCapabilities(true): advertises listChanged so harnesses
//     can hot-reload the tool list.
//   - WithRecovery(): catches panics in any tool handler so one bad
//     call can't bring down the server.
//   - WithToolFilter(canonicalOrderFilter): mcp-go's handleListTools
//     sorts tools alphabetically by name (verified by reading the
//     upstream source at v0.40.0). Our canonical order per RFC D-9
//     is namespace-grouped (PROJECT → SESSION → RESEARCH →
//     AGENT_BOOTSTRAP → VIBE → CONTEXT → AGENT_MEMORY → RECALL →
//     JUDGE → POLICY → OBSERVABILITY → ADMIN → VLP); the filter
//     re-sorts to that order so tools/list emits the contract
//     promised by spec 164 bridge.4.
//   - WithInstructions(...): bake coexistence_group + cross-feature
//     hints into the initialize response (per spec 164 bridge.2 + the
//     v2.6.0 self-bootstrap cross-references).
//   - WithResourceCapabilities(true, true): advertises subscribe +
//     listChanged so harnesses can detect resource additions.
//   - WithHooks: capture the initialize.clientInfo (legacy 2025-06-18).
//   - WithMetaPropagator: capture _meta.clientInfo per-request (new
//     2026-07-28 spec). The two paths converge in the agentbootstrap
//     clientInfo store; recommend_companions + detect_environment
//     read from there.
func New(ctx context.Context) (*Server, error) {
	boot, err := Boot(ctx)
	if err != nil {
		return nil, err
	}

	// Canonical-order position map (built once at boot; reused on
	// every tools/list request via the filter closure).
	canonicalPos := make(map[string]int, 32)
	for i, n := range tools.CanonicalOrder() {
		canonicalPos[tools.WireName(n)] = i
	}
	canonicalOrderFilter := func(_ context.Context, listed []mcplib.Tool) []mcplib.Tool {
		// Stable sort by canonical position; tools not in canonical
		// (shouldn't happen given RegisterAll sanity-check) sink to
		// the end in alphabetical order.
		sort.SliceStable(listed, func(i, j int) bool {
			pi, oki := canonicalPos[listed[i].Name]
			pj, okj := canonicalPos[listed[j].Name]
			switch {
			case oki && okj:
				return pi < pj
			case oki:
				return true
			case okj:
				return false
			default:
				return listed[i].Name < listed[j].Name
			}
		})
		return listed
	}

	// Hooks + meta propagator wire the dual-spec clientInfo capture
	// (legacy handshake + new per-request _meta). See
	// internal/agentbootstrap/clientinfo.go for the full design.
	bootstrapHooks, bootstrapMeta := agentbootstrap.RegisterHooks()

	mcpSrv := server.NewMCPServer(
		boot.Config.ServerName,
		boot.Config.ServerVersion,
		server.WithToolCapabilities(true),
		server.WithRecovery(),
		server.WithToolFilter(canonicalOrderFilter),
		server.WithResourceCapabilities(true, true),
		server.WithHooks(bootstrapHooks),
		server.WithMetaPropagator(bootstrapMeta),
		// coexistence_group + policy_gateway baked into the standard
		// MCP instructions field. mcp-go v0.56.0's Implementation
		// struct doesn't carry custom fields, so we use the
		// instructions channel (visible to all MCP-native harnesses
		// that read the initialize response). The exact string is
		// documented in BRIDGE_AND_COEXISTENCE.md v2.0.0 §2.1 + §3.
		//
		// policy_gateway=true declares dark-memory as the policy
		// authority for the dark-agents coexistence group. Under
		// cx.v3, harnesses route dark-* tool calls through this
		// server for persona shaping, capability checks, and
		// drift-at-write. dark-research-mcp v0.8.0+ is demoted to
		// a backing (policy_gateway=false); see its RELEASE_NOTES
		// and BRIDGE §3.2.
		//
		// v2.6.0+: cross-feature hints appended (resources,
		// dark_memory_agent_bootstrap/recommend_companions/detect_environment,
		// companion MCPs). See agentbootstrap.CrossFeatureHints.
		server.WithInstructions(BuildInstructions(boot.Config.CoexistenceGroup, boot.Config.ServerVersion)),
	)

	// Register the dark-agent self-bootstrap resources (system prompt,
	// compatibility matrix, 6 install guides, 2 companion docs). This
	// must happen AFTER mcpSrv construction (AddResource needs the
	// server instance). Idempotent for the lifetime of the process.
	//
	// The template context is built from the live sources of truth
	// (tools.BuildBootstrapData) so the served documents always
	// reflect the current tool surface + schema version.
	if err := agentbootstrap.RegisterAll(mcpSrv, tools.BuildBootstrapData()); err != nil {
		return nil, fmt.Errorf("server.New: register agentbootstrap resources: %w", err)
	}

	return &Server{
		boot:   boot,
		mcpSrv: mcpSrv,
	}, nil
}

// BuildInstructions returns the MCP `instructions` string baked into
// the initialize response. Exported so server_test.go can assert the
// cx.v3 metadata without reflection. mcp-go v0.56.0's MCPServer struct
// holds `instructions` as an unexported field; the only public surface
// for the wire format is this helper.
//
// The string is the conformance contract per
// BRIDGE_AND_COEXISTENCE.md v2.0.0 §2.1:
//
//	coexistence_group=dark-agents/memory
//	policy_gateway=true
//
// Tests in server_test.go assert these substrings. dark-research-mcp
// v0.8.0+ reads this string to detect that dark-memory is the gateway
// and route dark-* calls through it (or fall back to direct calls in
// legacy mode).
//
// v2.6.0+ also appends cross-feature hints pointing harnesses at the
// self-bootstrap resources + the 3 self-bootstrap tools. This is
// best-effort: per the MCP blog (2025-11-03), the instructions field
// is "up to the implementer" — opencode (#32856) discards it; Claude
// Desktop + Claude Code honor it. The canonical bootstrap path is the
// dark-memory://docs/* resources.
func BuildInstructions(coexistenceGroup, version string) string {
	// The tool count is derived from the live registry — never
	// hardcoded — so the instructions field can't drift from the
	// actual surface when tools are added.
	n := len(tools.CanonicalOrder())
	base := fmt.Sprintf(
		"dark-memory-mcp server. coexistence_group=%s policy_gateway=true (spec 164 bridge.2 cx.v3). Canonical %d-tool order preserved per spec 164 bridge.4 + DMAP v1.1 spec 193 Layer 6. This server is the policy gateway for the dark-agents/memory coexistence group; harnesses detecting another dark-agents/* peer (dark-research-mcp with coexistence_group=dark-agents/research, policy_gateway=false) should route dark_* calls through this gateway for persona shaping, capability checks, and drift-at-write. Version=%s.",
		coexistenceGroup, n, version,
	)
	crossFeature := agentbootstrap.CrossFeatureHints(version)
	return base + " " + crossFeature
}

// RegisterAll iterates the canonical tool list and registers each
// tool present in the Registry with the mcp-go server. Tools not yet
// added are silently skipped (the canonical order is the contract;
// the actual set is the intersection of canonical ∩ registered).
//
// It also registers any "extra" tools — registered tools that are
// NOT in the canonical order. These are armed-mode namespaces (e.g.
// L7-REDTEAM) that appear only when the operator has flipped the
// relevant env flag. The public surface in the un-armed server stays
// at 26; the armed server gets 26 + extras.
//
// The handler adapter converts tools.HandlerFunc (our internal
// shape) into the mcp-go handler signature: it pulls Arguments from
// the CallToolRequest, calls our handler, and packages the
// ToolResponse back into a CallToolResult with structured content.
func (s *Server) RegisterAll() error {
	for _, t := range s.boot.Registry.ListCanonical() {
		if err := s.registerOne(t); err != nil {
			return fmt.Errorf("server.RegisterAll: tool %q: %w", t.Name, err)
		}
	}
	canonicalCount := len(s.boot.Registry.ListCanonical())
	extras := s.boot.Registry.ListExtras()
	for _, t := range extras {
		if err := s.registerOne(t); err != nil {
			return fmt.Errorf("server.RegisterAll: extra tool %q: %w", t.Name, err)
		}
	}
	if len(extras) > 0 {
		log.Printf("dark-mem-mcp: registered %d canonical + %d extras (armed-mode namespaces)",
			canonicalCount, len(extras))
	} else {
		log.Printf("dark-mem-mcp: registered %d tools (canonical order)", canonicalCount)
	}
	return nil
}

// registerOne adds a single Tool to the mcp-go server.
//
// We use NewToolWithRawSchema (not NewTool + WithRawInputSchema)
// because mcp-go's Tool.MarshalJSON (verified in v0.56.0 mcp/tools.go
// lines 678-687) still has an intentional conflict check that errors
// when both InputSchema.Type and RawInputSchema are set:
//
//	if t.RawInputSchema != nil {
//	    if t.InputSchema.Type != "" {
//	        return nil, fmt.Errorf("tool %s has both InputSchema
//	            and RawInputSchema set: %w", t.Name, errToolSchemaConflict)
//	    }
//	    m["inputSchema"] = t.RawInputSchema
//	}
//
// NewTool initializes InputSchema.Type="object" by default, so
// using NewTool + WithRawInputSchema trips that check. NewToolWithRawSchema
// starts with a clean Tool (InputSchema.Type="") and sets
// RawInputSchema directly — bypassing the conflict. The upstream
// ToolInputSchema MarshalJSON was fixed in v0.53.0 (PR #858), but
// that fix is independent of the Tool-level conflict check which
// is intentional.
//
// Discovered via the bridge.7 conformance test: tools/list returned
// a serialization error and the client never received a response
// (the server logged "failed to write response: ... tool
// dark_memory_active_policy has both InputSchema and RawInputSchema set").
func (s *Server) registerOne(t *tools.Tool) error {
	wireName := tools.WireName(t.Name)
	tool := mcplib.NewToolWithRawSchema(wireName, t.Description, t.InputSchema)
	handler := s.wrapHandler(t)
	s.mcpSrv.AddTool(tool, handler)
	return nil
}

// wrapHandler converts our tools.HandlerFunc to the mcp-go handler
// signature. It is the single point where the two shapes meet:
//
//	our ToolResponse {Data, Audit, Next, Error}
//	    ↔
//	mcp-go CallToolResult {Content, IsError}
//
// On error: we emit a single TextContent block with the structured
// ToolError JSON; the mcp-go IsError marker is set so the harness
// can branch on it.
//
// Gate integration (v2.0.1, follow-up 1): when s.boot.Gate is set,
// the handler is wrapped with GateMiddleware.Wrap which runs
// PreCheck before the inner handler and PostCheck (drift-at-write)
// after, for artifact-creating tools only. When Gate is nil, the
// legacy direct-dispatch path runs (no policy enforcement).
func (s *Server) wrapHandler(t *tools.Tool) func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		// mcp-go returns Arguments as map[string]any; we re-marshal
		// to raw JSON so our handler signature (json.RawMessage) is
		// uniform regardless of whether the harness pre-decoded.
		rawMap := req.GetArguments()
		var rawJSON json.RawMessage
		if rawMap != nil {
			b, err := json.Marshal(rawMap)
			if err != nil {
				return mcplib.NewToolResultError(fmt.Sprintf("internal: cannot re-marshal args: %v", err)), nil
			}
			rawJSON = b
		}

		// Inner handler — runs after PreCheck passes.
		inner := func(ctx context.Context, raw json.RawMessage) (*tools.ToolResponse, error) {
			return t.Handler(ctx, raw)
		}

		// Gate-aware path (v2.0.1+): PreCheck → inner → PostCheck.
		// Direct path (legacy / Gate == nil): inner only.
		var resp *tools.ToolResponse
		if s.boot.Gate != nil {
			var gErr error
			resp, gErr = s.boot.Gate.Wrap(ctx, t.Name, rawJSON, inner)
			if gErr != nil {
				// Gate.Wrap returns nil error on refusal paths; this
				// branch only fires on a non-policy error (e.g. ctx
				// cancelled). Map to ErrInternal for consistency
				// with the legacy path.
				resp = &tools.ToolResponse{
					Error: &tools.ToolError{
						Code:    tools.ErrInternal,
						Message: gErr.Error(),
					},
				}
			}
		} else {
			var err error
			resp, err = inner(ctx, rawJSON)
			if err != nil {
				// Handler returned a Go error (shouldn't happen for
				// BindOrchestrator adapters, which surface errors as
				// ToolError). Map to a generic internal error.
				resp = &tools.ToolResponse{
					Error: &tools.ToolError{
						Code:    tools.ErrInternal,
						Message: err.Error(),
					},
				}
			}
		}

		// Marshal the ToolResponse. On error: emit as IsError text.
		if resp.Error != nil {
			body, _ := json.Marshal(resp)
			return mcplib.NewToolResultError(string(body)), nil
		}
		body, err := json.Marshal(resp)
		if err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("internal: marshal ToolResponse: %v", err)), nil
		}
		return mcplib.NewToolResultText(string(body)), nil
	}
}

// ServeStdio starts the stdio transport and blocks until ctx is
// cancelled or the transport fails. Defers the boot Shutdown so
// resources are released on exit.
func (s *Server) ServeStdio(ctx context.Context) error {
	log.Printf("dark-mem-mcp: serving stdio (server=%s v%s coexistence_group=%s)",
		s.boot.Config.ServerName, s.boot.Config.ServerVersion, s.boot.Config.CoexistenceGroup)
	defer s.boot.Shutdown(ctx)
	return server.ServeStdio(s.mcpSrv)
}

// ServeStream serves the full MCP surface over an existing net.Conn
// (spec 1176 §4.10 — MCP-over-socket for the daemon). It is the
// daemon-side equivalent of ServeStdio: the same MCPServer instance
// (canonical-order filter, hooks, meta propagator, resources, gate
// middleware) is driven over the connection's read/write endpoints,
// so the wire is the native MCP JSON-RPC that opencode expects.
//
// The bridge is a transparent byte proxy: it forwards opencode's
// stdio bytes to the daemon socket and the daemon's bytes back to
// opencode. This function makes the daemon speak MCP natively on that
// socket, which is what makes the bridge mode actually usable (the
// legacy Frame protocol only understood "ping" + "tools/list").
//
// Blocks until ctx is cancelled or the connection is closed.
func (s *Server) ServeStream(ctx context.Context, conn net.Conn) error {
	stdioSrv := server.NewStdioServer(s.mcpSrv)
	// The stdio server writes to conn and reads from conn. mcp-go
	// treats these as stdin/stdout, so a bidirectional net.Conn is
	// the right adapter. Read+write are both the same conn.
	stdioSrv.SetErrorLogger(log.New(os.Stderr, "dark-mem-mcp-daemon: ", log.LstdFlags))
	return stdioSrv.Listen(ctx, conn, conn)
}

// Close releases boot resources. Idempotent.
func (s *Server) Close() {
	if s == nil || s.boot == nil {
		return
	}
	_ = s.boot.Shutdown(context.Background())
}

// ServeHTTPOptions configura el transporte HTTP local.
//
// QUE ES ESTO. El kernel se ha servido siempre por stdio, para que un
// harness (opencode) lo hable como hijo de proceso. Eso no lo puede
// hablar un NAVEGADOR: el navegador no puede lanzar procesos ni abrir
// pipes, solo hace fetch. Este transporte es lo que permite que una
// pagina web publicada (por ejemplo memory.opitacode.com) hable con el
// dark-memory que esta en el escritorio de QUIEN VISITA la pagina.
//
// EL MODELO DE SEGURIDAD, y por que cada piece viene de donde viene:
//
//  1. SOLO LOOPBACK POR DEFECTO. La direccion por defecto es 127.0.0.1,
//     no 0.0.0.0. Un servidor MCP local en todas las interfaces es un
//     servidor MCP en la red de la persona, con las 81 tools.
//
//  2. CORS RESTRINGIDO, no abierto. AllowedOrigins es una lista
//     explicita de origenes (por ejemplo
//     "https://memory.opitacode.com"). Sin origenes configurados, el
//     transporte NO emite ninguna cabecera Access-Control-*: una pagina
//     cualquiera no puede ni ver la respuesta.
//
//  3. ANTI-DNS-REBINDING. mcp-go lo implementa (server/http_localhost.go,
//     rejectDNSRebinding): si la conexion viene de loopback pero la
//     cabecera Host no es de loopback, responde 403. Sin esto, una
//     pagina maliciosa podria apuntar su dominio a 127.0.0.1 y hablar con
//     el MCP local de la victima desde su navegador. La especificacion
//     MCP de seguridad lo pide (2025-11-25,
//     basic/security_best_practices#local-mcp-server-compromise).
//
//  4. PRIVATE NETWORK ACCESS. MEDIDO el 2026-11-18: sin esto, el
//     navegador REAL bloquea la peticion. Una pagina servida por HTTPS
//     que pide a 127.0.0.1 es, para el navegador, una peticion a la red
//     privada, y Chrome exige (PNA, antes "Local Network Access") que el
//     preflight OPTIONS devuelva `Access-Control-Allow-Private-Network:
//     true`. mcp-go v0.56.0 NO lo implementa (medido: ni una mencion de
//     "Private-Network" en su server/). Con curl no se nota, porque curl
//     no aplica la politica del navegador: con curl el preflight pasaba y
//     con el navegador fallaba con "TypeError: Failed to fetch". Sin
//     esto, el producto no existe en el navegador.
//
//  5. NINGUNA CREDENCIAL EN EL SERVIDOR. El keyring es del sistema
//     operativo de la persona; este transporte no lo expone.
type ServeHTTPOptions struct {
	// Addr es la direccion de escucha. Por defecto "127.0.0.1:0" (un
	// puerto efimero, elegido por el sistema operativo).
	Addr string

	// AllowedOrigins son los origenes de navegador autorizados. Vacio
	// significa: sin CORS (nadie puede llamar desde una pagina).
	AllowedOrigins []string

	// AllowCredentials habilita Access-Control-Allow-Credentials.
	// Es incompatible con "*" en AllowedOrigins: la especificacion de
	// CORS lo prohibe, y mcp-go lo respeta (con "*" y credentials, el
	// servidor repite el Origin en vez de mandar "*").
	AllowCredentials bool

	// PathPrefix es la ruta base del endpoint MCP. Por defecto "/mcp".
	PathPrefix string

	// AllowPrivateNetwork habilita `Access-Control-Allow-Private-Network:
	// true` en las respuestas a origenes autorizados. Es lo que permite
	// que una pagina HTTPS (la landing) hable con un servidor en
	// 127.0.0.1. MEDIDO el 2026-11-18: sin esto, el navegador bloquea
	// la peticion entera.
	AllowPrivateNetwork bool
}

// pnaMiddleware anade el soporte de Private Network Access.
//
// QUE ES PNA. Una pagina HTTPS que pide a una direccion de la red local
// (127.0.0.1, 192.168.x.x) le esta pidiendo al navegador que le de
// acceso a la red privada de la persona. El navegador no lo concede por
// las buenas: exige que el servidor de destino conteste al preflight con
// `Access-Control-Allow-Private-Network: true`. Chrome lo lleva
// haciendo desde 2024 (y lo pidio antes con el nombre de Local Network
// Access).
//
// POR QUE SOLO SI EL ORIGEN ESTA AUTORIZADO. La cabecera no se manda
// "a lo bruto": se responde true SOLO cuando el Origin de la peticion
// esta en la lista de autorizados. Si se mandara siempre, cualquier
// pagina podria usar la cabecera para leer la respuesta del servidor
// local, que es justo lo que el CORS restringido evita.
//
// MEDIDO el 2026-11-18: con esta cabecera, el preflight desde
// https://memory.opitacode.com pasa y el navegador llega al servidor.
func pnaMiddleware(next http.Handler, permitidos []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Solo tiene sentido en el preflight: la peticion de verdad no
		// lleva la cabecera, y por eso el navegador decide con la
		// respuesta al OPTIONS.
		if r.Method == http.MethodOptions &&
			r.Header.Get("Access-Control-Request-Private-Network") == "true" {
			origin := r.Header.Get("Origin")
			for _, p := range permitidos {
				if p == "*" || (origin != "" && p == origin) {
					w.Header().Set("Access-Control-Allow-Private-Network", "true")
					break
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// originMiddleware rechaza con 403 las peticiones cuyo Origin no este
// autorizado.
//
// QUE PIDE LA ESPECIFICACION. MEDIDO el 2026-11-18 en la spec de MCP
// (version 2025-06-18 y la revision draft 2026-07-28), seccion de
// seguridad del transporte Streamable HTTP:
//
//	"Servers MUST validate the Origin header on all incoming connections
//	 to prevent DNS rebinding attacks. If the Origin header is present and
//	 invalid, servers MUST respond with HTTP 403 Forbidden."
//
// Es un MUST, y lo que teniamos era otra cosa: CORS restringido (el
// navegador no deja LEER la respuesta de un origen no autorizado) y el
// chequeo anti-rebinding de mcp-go sobre la cabecera Host (403 si la
// peticion llega por loopback con un Host que no es de loopback).
//
// LO QUE AÑADE. El 403 explicito: la respuesta dice por que se rechaza, y
// un cliente que no es un navegador (un script, un curl) recibe la misma
// respuesta que el navegador. Antes un origen no autorizado recibia un 200
// sin cabecera CORS, que es correcto para un navegador pero opaco para
// cualquier otro cliente.
//
// Y QUE NO SE ROMPE. Una peticion SIN cabecera Origin no se toca: es el
// caso legitimo de una herramienta de linea de comandos en la maquina de la
// persona, que es lo que el transporte por HTTP tiene que permitir.
//
// FAIL CLOSED. Sin origenes configurados, cualquier peticion que traiga
// Origin recibe un 403: el servidor no esta listo para hablar con
// paginas, y lo dice.
func originMiddleware(next http.Handler, permitidos []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			permitido := false
			for _, p := range permitidos {
				if p == "*" || p == origin {
					permitido = true
					break
				}
			}
			if !permitido {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprintf(w, "dark-mem-mcp: origen no autorizado: %s\n", origin)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ServeHTTP sirve la superficie MCP completa sobre Streamable HTTP.
//
// QUE ES. El MISMO servidor MCP que sirve stdio, sobre el mismo
// http.Handler: el filtro de orden canonico, los hooks, el
// meta-propagador, los recursos y el middleware del gate se quedan igual.
// No hay una segunda implementacion de las tools: hay un segundo
// TRANSPORTE. Por eso el numero de tools y su orden son los mismos que
// por stdio.
//
// POR QUE NO ES "UN SERVIDOR MAS". El kernel es un proceso local de la
// persona: la base (DARK_DB) esta en su disco, el keyring es el suyo, y
// la pagina web que lo invoca no ve nada mas que lo que esa tool
// devuelve. Ese es el modelo local-first: los datos de la persona no
// salen del equipo de la persona.
//
// Bloquea hasta que ctx se cancela.
func (s *Server) ServeHTTP(ctx context.Context, opts ServeHTTPOptions) error {
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:0"
	}
	if opts.PathPrefix == "" {
		opts.PathPrefix = "/mcp"
	}

	log.Printf("dark-mem-mcp: serving http (server=%s v%s addr=%s prefix=%s origins=%v pna=%v)",
		s.boot.Config.ServerName, s.boot.Config.ServerVersion,
		opts.Addr, opts.PathPrefix, opts.AllowedOrigins, opts.AllowPrivateNetwork)
	defer s.boot.Shutdown(ctx)

	var cors []server.CORSOption
	if len(opts.AllowedOrigins) > 0 {
		cors = append(cors, server.WithCORSAllowedOrigins(opts.AllowedOrigins...))
		// MEDIDO el 2026-11-18: el navegador exige que la cabecera
		// `Mcp-Protocol-Version` este en `Access-Control-Allow-Headers`.
		// mcp-go anuncia por defecto Content-Type, Mcp-Session-Id,
		// Last-Event-ID y Authorization, pero NO esa: y como el cliente
		// de la pagina la manda en cada llamada, el preflight la rechaza
		// y el navegador da "TypeError: Failed to fetch" sin dar mas
		// pistas. El sintoma es el mismo que el de un origen no
		// autorizado, pero la causa es otra.
		cors = append(cors, server.WithCORSAllowedHeaders(
			"Content-Type", "Mcp-Session-Id", "Last-Event-ID", "Authorization",
			"Mcp-Protocol-Version"))
		if opts.AllowCredentials {
			cors = append(cors, server.WithCORSAllowCredentials())
		}
	}

	httpSrv := server.NewStreamableHTTPServer(s.mcpSrv,
		server.WithEndpointPath(opts.PathPrefix),
		server.WithStreamableHTTPCORS(cors...),
	)

	// El servidor HTTP de mcp-go ya aplica la proteccion anti-rebinding
	// (server/http_localhost.go): una peticion que llega por loopback
	// con una cabecera Host que no es de loopback recibe 403.
	//
	// El middleware de PNA se envuelve por fuera: tiene que responder al
	// preflight ANTES de que el handler de mcp-go lo ignore.
	//
	// Y el de Origin, por fuera de todo: la spec dice MUST validar el
	// Origin y responder 403 si es invalido. Se comprueba antes que nada,
	// para que un origen no autorizado no llegue ni al handler.
	mux := http.NewServeMux()
	mux.Handle(opts.PathPrefix,
		originMiddleware(pnaMiddleware(httpSrv, opts.AllowedOrigins), opts.AllowedOrigins))

	ln, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return err
	}
	log.Printf("dark-mem-mcp: http listening on %s (mcp endpoint %s)", ln.Addr(), opts.PathPrefix)

	srv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	return srv.Serve(ln)
}

// Registry exposes the tool registry so callers (e.g. tests, the
// RegisterAll helper) can register tools between New and ServeStdio.
func (s *Server) Registry() *tools.Registry {
	return s.boot.Registry
}

// BootState returns the boot state (for tests that need direct
// access to the Store / Orchestrator without going through MCP).
func (s *Server) BootState() *BootState {
	return s.boot
}
