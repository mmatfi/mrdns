// Package web serves the mrdns user interface. The HTTP surface is UI-only:
// server-rendered HTML pages plus htmx fragment endpoints. Zones and records
// are edited as structured data in the SQLite store; BIND zone files are
// generated only at deploy time. Authentication is a single access token
// exchanged at /login for a signed session cookie; mutations are CSRF-protected.
package web

import (
	"context"
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/mmatfi/mrdns/internal/audit"
	"github.com/mmatfi/mrdns/internal/config"
	"github.com/mmatfi/mrdns/internal/deploy"
	"github.com/mmatfi/mrdns/internal/metrics"
	"github.com/mmatfi/mrdns/internal/store"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

// Deployer rolls a prepared deploy request out to its targets. Implemented by
// *deploy.Pipeline; an interface so the web layer is testable without SSH.
type Deployer interface {
	Deploy(ctx context.Context, req deploy.Request) (*deploy.Result, error)
}

// Deps are the dependencies for the web server.
type Deps struct {
	Config   *config.Config
	Store    *store.Store
	Deployer Deployer
	Audit    *audit.Logger
	Metrics  *metrics.Metrics
	Logger   *slog.Logger
}

// Server holds the dependencies for the web UI.
type Server struct {
	cfg           *config.Config
	store         *store.Store
	deployer      Deployer
	audit         *audit.Logger
	metrics       *metrics.Metrics
	loginLimiter  *rateLimiter
	authToken     string
	cookieKey     []byte
	secureCookies bool
	tmpl          *template.Template
	log           *slog.Logger
}

// New parses the embedded templates and constructs a Server.
func New(d Deps) (*Server, error) {
	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	met := d.Metrics
	if met == nil {
		met = metrics.New()
	}
	return &Server{
		cfg:           d.Config,
		store:         d.Store,
		deployer:      d.Deployer,
		audit:         d.Audit,
		metrics:       met,
		loginLimiter:  newRateLimiter(5, time.Minute),
		authToken:     d.Config.AuthToken,
		cookieKey:     d.Config.CookieKey,
		secureCookies: d.Config.SecureCookiesEnabled(),
		tmpl:          tmpl,
		log:           d.Logger,
	}, nil
}

// Handler builds the routed HTTP handler with middleware applied.
func (srv *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public routes.
	mux.HandleFunc("GET /healthz", srv.handleHealthz)
	mux.HandleFunc("GET /metrics", srv.metrics.Handler())
	mux.HandleFunc("GET /login", srv.handleLoginForm)
	mux.HandleFunc("POST /login", srv.handleLogin)
	mux.HandleFunc("POST /logout", srv.handleLogout)
	staticSub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticSub)))

	// Authenticated routes.
	auth := func(h http.HandlerFunc) http.Handler { return srv.requireAuth(h) }
	mux.Handle("GET /{$}", auth(srv.handleDashboard))
	mux.Handle("GET /servers", auth(srv.handleServers))

	mux.Handle("GET /zones/new", auth(srv.handleNewZoneForm))
	mux.Handle("POST /zones", auth(srv.handleCreateZone))
	mux.Handle("GET /zones/import", auth(srv.handleImportForm))
	mux.Handle("POST /zones/import", auth(srv.handleImport))
	mux.Handle("GET /zones/{zone}", auth(srv.handleEditor))
	mux.Handle("GET /zones/{zone}/settings", auth(srv.handleSettingsForm))
	mux.Handle("POST /zones/{zone}/settings", auth(srv.handleUpdateSettings))
	mux.Handle("POST /zones/{zone}/delete", auth(srv.handleDeleteZone))

	mux.Handle("GET /zones/{zone}/records", auth(srv.handleRecordsFragment))
	mux.Handle("GET /zones/{zone}/records/edit", auth(srv.handleEditRecordForm))
	mux.Handle("POST /zones/{zone}/records", auth(srv.handleAddRecord))
	mux.Handle("POST /zones/{zone}/records/update", auth(srv.handleUpdateRecord))
	mux.Handle("POST /zones/{zone}/records/delete", auth(srv.handleDeleteRecord))

	mux.Handle("GET /zones/{zone}/preview", auth(srv.handlePreview))
	mux.Handle("GET /zones/{zone}/diff", auth(srv.handleDiff))
	mux.Handle("POST /zones/{zone}/validate", auth(srv.handleValidate))
	mux.Handle("POST /zones/{zone}/deploy", auth(srv.handleDeploy))
	mux.Handle("GET /zones/{zone}/history", auth(srv.handleHistory))
	mux.Handle("POST /zones/{zone}/rollback", auth(srv.handleRollback))

	return srv.recoverer(srv.logRequests(mux))
}
