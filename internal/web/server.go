package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
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
}

// New creates a new web dashboard server.
func New(cfg *config.Config, store *storage.Storage, port int) *Server {
	s := &Server{
		cfg:     cfg,
		store:   store,
		clients: make(map[chan []byte]struct{}),
		done:    make(chan struct{}),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleDashboard)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/events", s.handleSSE)

	s.httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	return s
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

// handleDashboard serves the embedded HTML page.
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

// handleState returns the current dashboard data as JSON.
func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	data := BuildDashboardData(s.cfg, s.store)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
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
