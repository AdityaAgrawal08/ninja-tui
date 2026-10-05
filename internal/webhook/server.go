package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// maxBodyBytes bounds how much of a webhook request is read. ninja results for
// large mail can be sizeable; 24 MiB leaves headroom over ninja's own 25 MiB
// message ceiling while protecting the listener.
const maxBodyBytes = 24 << 20

// Handler receives a decoded Result. Implementations must be fast and must not
// block the HTTP handler for long; the TUI wires this to a non-blocking send
// into the Bubble Tea event loop.
type Handler func(Result)

// Server is a small HTTP listener for ninja's SMTP webhook. It only accepts
// POST /hook and answers 202 Accepted as soon as the body decodes. ninja does
// not retry and does not sign its webhook, so speed matters more than policy.
type Server struct {
	listenAddr string

	mu       sync.RWMutex
	onResult Handler
	ln       net.Listener
	srv      *http.Server
}

// New returns an unstarted Server that will bind to addr. Use ":0" to let the
// OS choose a port, then read Addr after Start.
func New(addr string) *Server {
	return &Server{listenAddr: addr}
}

// SetHandler installs the result callback. It may be called before or after
// Start and is safe for concurrent use.
func (s *Server) SetHandler(h Handler) {
	s.mu.Lock()
	s.onResult = h
	s.mu.Unlock()
}

// Start binds the listener and begins serving in a background goroutine. The
// error is returned before any request can arrive, so a failure here is
// actionable at startup.
func (s *Server) Start() error {
	if s.listenAddr == "" {
		return errors.New("webhook: empty listen address")
	}
	ln, err := net.Listen("tcp", s.listenAddr)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/hook", s.handleHook)
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	s.mu.Lock()
	s.ln = ln
	s.srv = srv
	s.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()
	return nil
}

// Addr reports the address the listener is actually bound to. Before Start it
// reports the configured address.
func (s *Server) Addr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.ln != nil {
		return s.ln.Addr().String()
	}
	return s.listenAddr
}

// Shutdown stops accepting requests and waits for in-flight ones until ctx is
// done.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.RLock()
	srv := s.srv
	s.mu.RUnlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

func (s *Server) handleHook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(body) > maxBodyBytes {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	var res Result
	if err := json.Unmarshal(body, &res); err != nil {
		http.Error(w, "decode result: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Acknowledge first: the decoded result is complete at this point and
	// ninja only logs non-2xx responses.
	w.WriteHeader(http.StatusAccepted)
	_, _ = io.WriteString(w, "accepted")

	s.mu.RLock()
	h := s.onResult
	s.mu.RUnlock()
	if h != nil {
		h(res)
	}
}
