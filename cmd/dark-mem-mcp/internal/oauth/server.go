// Package oauth — server.go: localhost HTTP server that captures OAuth
// callback redirects from providers. Listens on port 9335 (configurable
// via DARK_OAUTH_CALLBACK_PORT). Single-shot: serves one callback
// request then shuts down.
//
// Flow:
//
//   1. Router.Connect() calls Server.Start(Port)
//   2. Server starts a net/http listener on 127.0.0.1:Port
//   3. Server registers /oauth/callback handler
//   4. User opens auth_url in browser, approves on provider
//   5. Provider redirects to http://127.0.0.1:Port/oauth/callback?code=X&state=Y
//   6. Handler writes success HTML to the response, signals code+state
//      to the in-flight flow via channel, and initiates server.Shutdown
//   7. Router receives code+state, exchanges for tokens, persists
//
// Browser shows "connection refused" if localhost:9335 was closed
// before the browser hit it. This is harmless: the callback already
// fired; only the browser's display is affected.
package oauth

import (
	"context"
	"fmt"
	"html"
	"net"
	"net/http"
	"sync"
	"time"
)

// DefaultCallbackPath is the URL path providers redirect to.
const DefaultCallbackPath = "/oauth/callback"

// Server is the localhost callback server. One server per flow; it
// shuts down after serving the single callback request.
type Server struct {
	port   int
	path   string
	mu     sync.Mutex
	listener net.Listener
	server  *http.Server
	flow   *PendingFlow // set by Register()
}

// NewServer constructs a Server bound to the given port. The server
// is not started until Start is called.
func NewServer(port int) *Server {
	if port <= 0 {
		port = DefaultCallbackPort
	}
	return &Server{
		port: port,
		path: DefaultCallbackPath,
	}
}

// Port returns the configured port (for surfacing in ConnectResponse).
func (s *Server) Port() int {
	return s.port
}

// CallbackURL returns the full URL providers redirect to. Surfaced to
// the OAuth provider implementations and to the user.
func (s *Server) CallbackURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", s.port, s.path)
}

// Start binds the listener, starts the HTTP server in a goroutine, and
// returns immediately. The caller waits on flow.Callback for the
// provider's redirect to be received.
func (s *Server) Start(flow *PendingFlow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
	if err != nil {
		return fmt.Errorf("oauth: bind 127.0.0.1:%d: %w", s.port, err)
	}
	s.listener = ln
	s.flow = flow
	s.server = &http.Server{
		Handler:      http.HandlerFunc(s.handleCallback),
		ReadTimeout:  DefaultFlowTimeout,
		WriteTimeout: 5 * time.Second,
	}
	go func() {
		_ = s.server.Serve(ln)
	}()
	return nil
}

// handleCallback processes the single redirect the provider sends.
// On success: writes a friendly HTML page, signals the flow, and
// schedules server shutdown.
func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	errParam := r.URL.Query().Get("error")

	s.mu.Lock()
	flow := s.flow
	s.mu.Unlock()

	if flow == nil {
		http.Error(w, "no pending flow", http.StatusBadRequest)
		return
	}

	if errParam != "" {
		// User denied or provider errored.
		flow.Callback <- callbackResult{Err: fmt.Errorf("oauth: provider returned error: %s", errParam)}
		s.respondHTML(w, "Authorization denied",
			fmt.Sprintf("The OAuth provider returned an error: %s. You can close this tab.", html.EscapeString(errParam)))
		go s.shutdown()
		return
	}

	if state != flow.State {
		// CSRF check failed — the callback's state doesn't match
		// the flow's state. Could be attacker trying to inject a
		// different code. Don't signal; let the flow time out.
		flow.Callback <- callbackResult{Err: fmt.Errorf("oauth: state mismatch (got %q, want %q)", state, flow.State)}
		s.respondHTML(w, "State mismatch", "The OAuth state did not match. You can close this tab and retry.")
		go s.shutdown()
		return
	}

	flow.Callback <- callbackResult{Code: code, State: state}
	s.respondHTML(w, "Authorization received",
		"dark-memory has received your authorization. You can close this tab and return to your harness.")
	go s.shutdown()
}

// respondHTML writes a small friendly HTML page so the user's browser
// shows something more useful than "connection refused" when the
// server shuts down immediately after this response.
func (s *Server) respondHTML(w http.ResponseWriter, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>%s — dark-memory</title>
  <style>
    body { font-family: -apple-system, system-ui, sans-serif; max-width: 480px; margin: 80px auto; padding: 0 24px; color: #1a1a1a; }
    h1 { font-size: 22px; font-weight: 600; }
    p { font-size: 15px; line-height: 1.5; color: #555; }
    .ok { color: #1a7f37; font-weight: 500; }
  </style>
</head>
<body>
  <h1 class="ok">✓ %s</h1>
  <p>%s</p>
  <p style="font-size: 13px; color: #888;">dark-memory oauth/callback</p>
</body>
</html>`, html.EscapeString(title), html.EscapeString(title), html.EscapeString(body))
}

// shutdown gracefully closes the HTTP server. Idempotent.
func (s *Server) shutdown() {
	s.mu.Lock()
	server := s.server
	s.mu.Unlock()
	if server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}