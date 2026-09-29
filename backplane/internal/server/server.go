// Package server connects the UI to the app over a loopback-only HTTP
// endpoint. Every API call needs the per-launch token, and Host/Origin checks
// stop other websites (DNS rebinding, cross-site requests) from reaching it.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"safisolutions.org/backplane/internal/app"
	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/engine"
)

// Server serves the UI and the RPC endpoint.
type Server struct {
	App   *app.App
	UI    fs.FS
	Token string
	// OnFocus is called when a second launch asks this window to come forward.
	OnFocus func()

	ln   net.Listener
	srv  *http.Server
	port string
	mu   sync.Mutex
}

// New prepares a server with a fresh random token.
func New(a *app.App, ui fs.FS) *Server {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return &Server{App: a, UI: ui, Token: hex.EncodeToString(b)}
}

// Start listens on a random loopback port.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.ln = ln
	_, s.port, _ = net.SplitHostPort(ln.Addr().String())
	mux := http.NewServeMux()
	mux.HandleFunc("/api/call/", s.guard(s.handleCall))
	mux.HandleFunc("/api/events", s.guard(s.handleEvents))
	mux.HandleFunc("/api/upload", s.guard(s.handleUpload))
	mux.HandleFunc("/api/focus", s.guard(s.handleFocus))
	mux.HandleFunc("/", s.handleStatic)
	s.srv = &http.Server{Handler: s.headers(mux), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	go s.srv.Serve(ln)
	return nil
}

// Port is the listening port.
func (s *Server) Port() string { return s.port }

// Origin is the UI's origin.
func (s *Server) Origin() string { return "http://127.0.0.1:" + s.port }

// URL is what the window loads. The token travels in the fragment, which
// browsers never send to a server or put in Referer headers.
func (s *Server) URL() string { return s.Origin() + "/#t=" + s.Token }

// Close stops the server.
func (s *Server) Close() {
	if s.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(ctx)
	}
}

func (s *Server) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		if !s.hostOK(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostOK blocks DNS-rebinding: only the loopback address and port we bound.
func (s *Server) hostOK(host string) bool {
	return host == "127.0.0.1:"+s.port || host == "localhost:"+s.port
}

// guard enforces the token and same-origin requests for the API.
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && o != s.Origin() && o != "http://localhost:"+s.port {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		tok := r.Header.Get("X-Backplane-Token")
		if tok == "" && (strings.HasSuffix(r.URL.Path, "/events")) {
			tok = r.URL.Query().Get("t") // EventSource cannot set headers
		}
		if subtle.ConstantTimeCompare([]byte(tok), []byte(s.Token)) != 1 {
			http.Error(w, "missing or wrong token", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// errorBody is the JSON shape of a failed call.
type errorBody struct {
	Message string        `json:"message"`
	Problem *core.Problem `json:"problem,omitempty"`
	Confirm bool          `json:"confirm,omitempty"` // the action needs the project name typed
}

func (s *Server) handleCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return
	}
	method := strings.TrimPrefix(r.URL.Path, "/api/call/")
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": errorBody{Message: "could not read the request"}})
		return
	}
	res, err := s.App.Call(r.Context(), method, body)
	if err != nil {
		eb := errorBody{Message: err.Error()}
		var p *core.Problem
		if errors.As(err, &p) {
			eb.Problem = p
			eb.Message = p.Title
			if p.Summary != "" {
				eb.Message += ": " + p.Summary
			}
		}
		if errors.Is(err, engine.ErrConfirm) {
			eb.Confirm = true
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": eb})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": res})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	ch, unsubscribe := s.App.Engine.Bus.Subscribe()
	defer unsubscribe()
	fmt.Fprintf(w, "retry: 2000\n\n")
	fl.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprintf(w, ": ping\n\n")
			fl.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			bs, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", bs)
			fl.Flush()
		}
	}
}

// handleUpload streams a product file straight to disk (large installers
// never pass through JSON).
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	r.Body = http.MaxBytesReader(w, r.Body, app.MaxUpload+1)
	res, err := s.App.SaveProductFile(q.Get("project"), q.Get("name"), r.Body)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": errorBody{Message: err.Error()}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": res})
}

func (s *Server) handleFocus(w http.ResponseWriter, r *http.Request) {
	if s.OnFocus != nil {
		s.OnFocus()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

var contentTypes = map[string]string{
	".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8",
	".svg": "image/svg+xml", ".png": "image/png", ".ico": "image/x-icon", ".woff2": "font/woff2", ".json": "application/json",
	".txt": "text/plain; charset=utf-8", ".webp": "image/webp",
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.UI == nil {
		http.Error(w, "UI not embedded", http.StatusNotFound)
		return
	}
	p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if p == "" {
		p = "index.html"
	}
	data, err := fs.ReadFile(s.UI, p)
	if err != nil {
		// Client-side routes fall back to the app shell.
		p = "index.html"
		if data, err = fs.ReadFile(s.UI, p); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	ext := path.Ext(p)
	if ct, ok := contentTypes[ext]; ok {
		w.Header().Set("Content-Type", ct)
	}
	if p == "index.html" {
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
