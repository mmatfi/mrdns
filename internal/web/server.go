// Package web serves the mrdns user interface. The HTTP surface is UI-only:
// server-rendered HTML pages plus htmx fragment endpoints. There is no JSON
// API. Authentication is a single access token exchanged at /login for a
// signed session cookie; state-changing requests are CSRF-protected.
package web

import (
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/mmatfi/mrdns/internal/config"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

// Server holds the dependencies for the web UI.
type Server struct {
	cfg           *config.Config
	authToken     string
	cookieKey     []byte
	secureCookies bool
	tmpl          *template.Template
	log           *slog.Logger
}

// New parses the embedded templates and constructs a Server.
func New(cfg *config.Config, log *slog.Logger) (*Server, error) {
	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg:           cfg,
		authToken:     cfg.AuthToken,
		cookieKey:     cfg.CookieKey,
		secureCookies: cfg.SecureCookiesEnabled(),
		tmpl:          tmpl,
		log:           log,
	}, nil
}

// Handler builds the routed HTTP handler with middleware applied.
func (srv *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public routes.
	mux.HandleFunc("GET /healthz", srv.handleHealthz)
	mux.HandleFunc("GET /login", srv.handleLoginForm)
	mux.HandleFunc("POST /login", srv.handleLogin)
	mux.HandleFunc("POST /logout", srv.handleLogout)

	staticSub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticSub)))

	// Authenticated routes.
	mux.Handle("GET /{$}", srv.requireAuth(http.HandlerFunc(srv.handleDashboard)))

	return srv.recoverer(srv.logRequests(mux))
}
