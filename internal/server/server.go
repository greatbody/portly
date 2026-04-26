package server

import (
	"bufio"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/greatbody/portly/internal/auth"
	"github.com/greatbody/portly/internal/config"
	"github.com/greatbody/portly/internal/proxy"
	"github.com/greatbody/portly/internal/store"
)

//go:embed templates/*.html
var templatesFS embed.FS

type Server struct {
	Cfg   *config.Config
	Store *store.Store

	tmpl *template.Template

	mu       sync.RWMutex
	handlers map[string]*proxy.Handler // slug -> handler
}

func New(cfg *config.Config, st *store.Store) (*Server, error) {
	sub, err := fs.Sub(templatesFS, "templates")
	if err != nil {
		return nil, err
	}
	t, err := template.ParseFS(sub, "*.html")
	if err != nil {
		return nil, err
	}
	return &Server{
		Cfg:      cfg,
		Store:    st,
		tmpl:     t,
		handlers: map[string]*proxy.Handler{},
	}, nil
}

// Handler returns the root http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	// 1x1 transparent PNG so browser favicon requests don't litter logs with 404.
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		// 1x1 transparent PNG, base64 of 67 bytes
		const png = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\xff\xff?\x00\x05\xfe\x02\xfe\xa3\x35\x81\x84\x00\x00\x00\x00IEND\xaeB`\x82"
		_, _ = w.Write([]byte(png))
	})

	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/logout", s.handleLogout)

	// Reverse proxy entry — must come BEFORE root.
	mux.HandleFunc("/p/", s.handleProxy)

	// API
	mux.Handle("/api/targets", s.requireAuth(http.HandlerFunc(s.apiTargets), true))
	mux.Handle("/api/targets/", s.requireAuth(http.HandlerFunc(s.apiTargetByID), true))

	// Dashboard / admin pages
	mux.Handle("/", s.requireAuth(http.HandlerFunc(s.handleDashboard), false))

	return s.refererRescue(logMiddleware(mux))
}

// refererRescue: if a request comes in WITHOUT the /p/{slug}/ prefix but its Referer
// (or, as a last resort, a portly_last_slug cookie set by previous proxy responses)
// shows it originated from a /p/{slug}/ page, internally rewrite the URL so the
// proxy route can handle it. This catches SPA-generated absolute URLs (/assets/x.js,
// /api/foo) that even the JS shim might miss — for example ES module dynamic
// import(), which does not send a Referer header for module-typed scripts.
func (s *Server) refererRescue(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// only rescue if not already under /p/, /api/, /login, /logout, /healthz, /
		p := r.URL.Path
		if strings.HasPrefix(p, "/p/") || p == "/" || p == "/login" || p == "/logout" ||
			p == "/healthz" || p == "/favicon.ico" || strings.HasPrefix(p, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		slug := slugFromReferer(r.Header.Get("Referer"))
		if slug == "" {
			if c, err := r.Cookie("portly_last_slug"); err == nil {
				slug = c.Value
			}
		}
		if slug == "" {
			next.ServeHTTP(w, r)
			return
		}
		// Rewrite request path
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/p/" + slug + p
		if r2.URL.RawPath != "" {
			r2.URL.RawPath = "/p/" + slug + r2.URL.RawPath
		}
		next.ServeHTTP(w, r2)
	})
}

// slugFromReferer extracts the slug from a Referer URL like /p/{slug}/...
func slugFromReferer(ref string) string {
	if ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	rp := u.Path
	if !strings.HasPrefix(rp, "/p/") {
		return ""
	}
	rest := strings.TrimPrefix(rp, "/p/")
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[:i]
	}
	return rest
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		// Don't wrap proxy responses: doing so loses the http.Flusher / Hijacker
		// type assertion that ReverseProxy needs for SSE / WebSocket.
		if strings.HasPrefix(r.URL.Path, "/p/") {
			next.ServeHTTP(w, r)
			fmt.Printf("%s %s %s -> proxied %s\n",
				time.Now().Format("15:04:05"),
				r.Method, r.URL.RequestURI(), time.Since(start))
			return
		}
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		fmt.Printf("%s %s %s -> %d %s\n",
			time.Now().Format("15:04:05"),
			r.Method, r.URL.RequestURI(), sw.status, time.Since(start))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Flush forwards to the underlying writer when possible. ReverseProxy uses this
// for streaming (SSE, etc) when FlushInterval = -1.
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the underlying writer when possible (needed for WebSocket
// upgrades performed by httputil.ReverseProxy).
func (s *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := s.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("hijack not supported")
}

// ---------- handlers ----------

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		next := r.URL.Query().Get("next")
		_ = s.tmpl.ExecuteTemplate(w, "login.html", map[string]any{"Next": next, "Error": ""})
	case http.MethodPost:
		_ = r.ParseForm()
		username := r.FormValue("username")
		password := r.FormValue("password")
		next := r.FormValue("next")
		if next == "" {
			next = "/"
		}
		if err := s.login(w, r, username, password); err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			_ = s.tmpl.ExecuteTemplate(w, "login.html", map[string]any{"Next": next, "Error": "Invalid username or password"})
			return
		}
		http.Redirect(w, r, next, http.StatusFound)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.logout(w, r)
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	targets, err := s.Store.ListTargets()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	publicBase := s.Cfg.PublicBaseURL
	_ = s.tmpl.ExecuteTemplate(w, "dashboard.html", map[string]any{
		"Targets":    targets,
		"PublicBase": publicBase,
	})
}

// ---------- proxy ----------

func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	// /p/{slug}/...
	rest := strings.TrimPrefix(r.URL.Path, "/p/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}
	slug := rest
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		slug = rest[:i]
	}
	// Special case: "/p/slug" without trailing slash → redirect to "/p/slug/"
	if !strings.HasSuffix(r.URL.Path, "/") && !strings.Contains(rest, "/") {
		http.Redirect(w, r, "/p/"+slug+"/", http.StatusFound)
		return
	}

	// Auth check (proxy also requires login).
	u, _ := s.currentUser(r)
	if u == nil {
		http.Redirect(w, r, "/login?next="+r.URL.RequestURI(), http.StatusFound)
		return
	}

	t, err := s.Store.GetTargetBySlug(slug)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if t == nil {
		http.NotFound(w, r)
		return
	}
	if !t.Enabled {
		http.Error(w, "target disabled", http.StatusServiceUnavailable)
		return
	}

	// Stash the slug in a cookie so refererRescue can fall back to it when
	// browser-issued requests (e.g. ES module dynamic import, web workers,
	// some <link rel=preload>) arrive without a Referer header.
	http.SetCookie(w, &http.Cookie{
		Name:     "portly_last_slug",
		Value:    t.Slug,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   3600,
	})

	h := s.proxyHandlerFor(t)
	h.ServeHTTP(w, r)
}

func (s *Server) proxyHandlerFor(t *store.Target) *proxy.Handler {
	mount := "/p/" + t.Slug
	s.mu.RLock()
	if h, ok := s.handlers[t.Slug]; ok && h.Target.UpdatedAt == t.UpdatedAt {
		s.mu.RUnlock()
		return h
	}
	s.mu.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	h, _ := proxy.New(t, mount, time.Duration(s.Cfg.Security.UpstreamTimeoutSec)*time.Second)
	s.handlers[t.Slug] = h
	return h
}

func (s *Server) invalidateHandler(slug string) {
	s.mu.Lock()
	delete(s.handlers, slug)
	s.mu.Unlock()
}

// ---------- API ----------

type targetDTO struct {
	ID          int64  `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Scheme      string `json:"scheme"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
}

func toDTO(t *store.Target) targetDTO {
	return targetDTO{
		ID: t.ID, Slug: t.Slug, Name: t.Name, Scheme: t.Scheme,
		Host: t.Host, Port: t.Port, Description: t.Description, Enabled: t.Enabled,
	}
}

func (s *Server) apiTargets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		ts, err := s.Store.ListTargets()
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		out := make([]targetDTO, 0, len(ts))
		for _, t := range ts {
			out = append(out, toDTO(t))
		}
		writeJSON(w, 200, out)
	case http.MethodPost:
		var in targetDTO
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if err := validateInput(&in); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if err := validateTarget(s.Cfg, in.Host, in.Port); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		t := &store.Target{
			Slug: in.Slug, Name: in.Name, Scheme: in.Scheme, Host: in.Host,
			Port: in.Port, Description: in.Description, Enabled: in.Enabled,
		}
		out, err := s.Store.CreateTarget(t)
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, toDTO(out))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) apiTargetByID(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/targets/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad id"})
		return
	}
	t, err := s.Store.GetTarget(id)
	if err != nil || t == nil {
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return
	}
	switch r.Method {
	case http.MethodPut:
		var in targetDTO
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if err := validateInput(&in); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if err := validateTarget(s.Cfg, in.Host, in.Port); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		t.Slug = in.Slug
		t.Name = in.Name
		t.Scheme = in.Scheme
		t.Host = in.Host
		t.Port = in.Port
		t.Description = in.Description
		t.Enabled = in.Enabled
		if err := s.Store.UpdateTarget(t); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		s.invalidateHandler(t.Slug)
		writeJSON(w, 200, toDTO(t))
	case http.MethodDelete:
		if err := s.Store.DeleteTarget(id); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		s.invalidateHandler(t.Slug)
		writeJSON(w, 200, map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func validateInput(in *targetDTO) error {
	in.Slug = strings.TrimSpace(in.Slug)
	in.Name = strings.TrimSpace(in.Name)
	in.Scheme = strings.ToLower(strings.TrimSpace(in.Scheme))
	in.Host = strings.TrimSpace(in.Host)
	if in.Slug == "" {
		return errors.New("slug is required")
	}
	for _, r := range in.Slug {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return errors.New("slug must be [a-z0-9-_]")
		}
	}
	if in.Name == "" {
		in.Name = in.Slug
	}
	if in.Scheme != "http" && in.Scheme != "https" {
		return errors.New("scheme must be http or https")
	}
	if in.Host == "" {
		return errors.New("host is required")
	}
	if in.Port <= 0 || in.Port > 65535 {
		return errors.New("port must be 1..65535")
	}
	return nil
}

// EnsureAdmin creates an admin user if none exists. Returns the password (plaintext)
// only if it was just generated; otherwise empty string.
func (s *Server) EnsureAdmin() (username, generatedPassword string, err error) {
	n, err := s.Store.CountUsers()
	if err != nil {
		return "", "", err
	}
	if n > 0 {
		return s.Cfg.Admin.Username, "", nil
	}
	username = s.Cfg.Admin.Username
	if username == "" {
		username = "admin"
	}
	password := s.Cfg.Admin.Password
	if password == "" {
		password, err = auth.RandomPassword()
		if err != nil {
			return "", "", err
		}
		generatedPassword = password
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return "", "", err
	}
	if _, err := s.Store.CreateUser(username, hash); err != nil {
		return "", "", err
	}
	return username, generatedPassword, nil
}
