package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/registry"
)

//go:embed dashboard.html
var dashboardHTML embed.FS

const maxClients = 50

// Server is a lightweight HTTP server for the Cortex dashboard.
type Server struct {
	httpServer *http.Server
	cfg        *config.Config
	store      *storage.Storage
	clients    map[chan []byte]struct{}
	mu         sync.RWMutex
	done       chan struct{}

	// Token gates all /api/* requests. Even though we bind loopback,
	// other processes on the same machine are still in the threat
	// model (a malicious browser tab, a compromised dev tool, an
	// unprivileged user on a multi-user box). The token is generated
	// on construction and persisted to `<ContextDir>/dashboard.token`
	// with 0600 perms so authorized callers (CLI, scripts) can read
	// it without having to run their own derivation.
	Token string
}

// New creates a new web dashboard server.
func New(cfg *config.Config, store *storage.Storage, port int) *Server {
	s := &Server{
		cfg:     cfg,
		store:   store,
		clients: make(map[chan []byte]struct{}),
		done:    make(chan struct{}),
		Token:   generateToken(),
	}
	// Best-effort persist of the token. If this fails (read-only FS,
	// missing context dir, etc.) the server still works — the token
	// is in memory — but operators won't be able to retrieve it
	// out of band.
	_ = persistToken(cfg.ContextDir, s.Token)

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleDashboard)
	mux.Handle("/api/state", s.requireToken(http.HandlerFunc(s.handleState)))
	mux.Handle("/api/projects", s.requireToken(http.HandlerFunc(s.handleProjects)))
	mux.Handle("/api/events", s.requireToken(http.HandlerFunc(s.handleSSE)))

	// Bind to loopback only. The dashboard exposes an SSE event stream
	// of captured tool calls — content that often contains partially-
	// redacted secrets and decision history. A `:%d` bind would expose
	// it to every device on the user's LAN.
	s.httpServer = &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", port),
		Handler: mux,
	}

	return s
}

// generateToken returns 32 random bytes hex-encoded. Cryptographically
// random — never use math/rand here. Length covers a 64-char hex string,
// plenty of entropy against guessing.
func generateToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// Falling back to a less-random token would be silently weaker.
		// Better to abort: caller can retry or run without a server.
		panic(fmt.Sprintf("web: failed to generate token: %v", err))
	}
	return hex.EncodeToString(b)
}

// persistToken writes the token to <contextDir>/dashboard.token with
// 0600 perms so only the running user can read it. The contextDir is
// expected to already exist (created by config setup) — if not, the
// write fails and the in-memory token still works.
func persistToken(contextDir, token string) error {
	path := filepath.Join(contextDir, "dashboard.token")
	return os.WriteFile(path, []byte(token+"\n"), 0600)
}

// requireToken wraps an HTTP handler with a bearer-token check.
// The token may be presented either via the Authorization header
// ("Bearer <token>") or the ?token=<token> query parameter — the
// latter form is needed by EventSource clients, which cannot set
// custom headers.
//
// Comparison uses subtle.ConstantTimeCompare so a timing attacker
// cannot probe the token byte-by-byte over many requests.
func (s *Server) requireToken(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := ""
		if auth := r.Header.Get("Authorization"); auth != "" {
			if strings.HasPrefix(auth, "Bearer ") {
				got = strings.TrimPrefix(auth, "Bearer ")
			}
		}
		if got == "" {
			got = r.URL.Query().Get("token")
		}
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// Start starts the HTTP server and the SSE broadcast loop. Blocks until shutdown.
func (s *Server) Start() error {
	go s.broadcastLoop()
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	close(s.done)
	return s.httpServer.Shutdown(ctx)
}

// handleDashboard serves the embedded HTML page with the per-server
// token injected as `window.__CORTEX_TOKEN__`. The in-page JS uses
// that global to authenticate its EventSource connection.
//
// The token is hex (no quote/HTML-special chars) so direct splice
// into a JS string literal is safe; we still use %q to be defensive
// against future token-format changes.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := dashboardHTML.ReadFile("dashboard.html")
	if err != nil {
		http.Error(w, "dashboard not found", http.StatusInternalServerError)
		return
	}
	tokenScript := fmt.Sprintf(`<script>window.__CORTEX_TOKEN__ = %q;</script>`, s.Token)
	html := strings.Replace(string(data), "<head>", "<head>\n"+tokenScript, 1)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(html))
}

// handleState returns the current dashboard data as JSON.
func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	data := BuildDashboardData(s.cfg, s.store)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

// handleProjects returns the list of registered projects.
func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	type projectInfo struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Path       string `json:"path"`
		LastActive string `json:"last_active"`
		HasPending bool   `json:"has_pending"`
	}

	var projects []projectInfo
	if reg, err := registry.Open(); err == nil {
		for _, p := range reg.List() {
			info := projectInfo{
				ID:         p.ID,
				Name:       p.Name,
				Path:       p.Path,
				LastActive: p.LastActive.Format(time.RFC3339),
			}
			// Check for pending events
			pendingDir := filepath.Join(p.Path, ".cortex", "queue", "pending")
			if entries, err := os.ReadDir(pendingDir); err == nil && len(entries) > 0 {
				info.HasPending = true
			}
			projects = append(projects, info)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(projects)
}

// handleSSE streams dashboard state to the client via Server-Sent Events.
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	s.mu.RLock()
	count := len(s.clients)
	s.mu.RUnlock()
	if count >= maxClients {
		http.Error(w, "too many clients", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ch := make(chan []byte, 4)

	s.mu.Lock()
	s.clients[ch] = struct{}{}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.clients, ch)
		s.mu.Unlock()
	}()

	// Send initial state immediately
	if data := s.buildJSON(); data != nil {
		fmt.Fprintf(w, "event: state\ndata: %s\n\n", data)
		flusher.Flush()
	}

	// Stream updates
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: state\ndata: %s\n\n", msg)
			flusher.Flush()
		case <-r.Context().Done():
			return
		case <-s.done:
			return
		}
	}
}

// broadcastLoop polls dashboard data every 500ms and sends to all SSE clients.
func (s *Server) broadcastLoop() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			data := s.buildJSON()
			if data == nil {
				continue
			}

			s.mu.RLock()
			for ch := range s.clients {
				select {
				case ch <- data:
				default:
					// Client is slow, drop this update
				}
			}
			s.mu.RUnlock()
		case <-s.done:
			return
		}
	}
}

// buildJSON produces the JSON payload for SSE.
func (s *Server) buildJSON() []byte {
	d := BuildDashboardData(s.cfg, s.store)
	data, err := json.Marshal(d)
	if err != nil {
		return nil
	}
	return data
}
