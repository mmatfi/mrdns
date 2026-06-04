package web

import (
	"bytes"
	"crypto/subtle"
	"io"
	"net"
	"net/http"
	"sort"
	"time"

	"github.com/mmatfi/mrdns/internal/audit"
)

func (srv *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok\n")
}

func (srv *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	s, ok := srv.currentSession(r)
	if ok && s.Authed {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if !ok {
		s = newSession(false)
		srv.setSession(w, s)
	}
	srv.render(w, "login.html", map[string]any{
		"Title": "Sign in",
		"CSRF":  s.CSRF,
		"Error": r.URL.Query().Get("error"),
	})
}

func (srv *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !srv.loginLimiter.allow(ip, time.Now()) {
		srv.audit.Log(audit.Event{Action: "login_rate_limited", Actor: ip})
		http.Error(w, "too many attempts; try again later", http.StatusTooManyRequests)
		return
	}
	s, ok := srv.currentSession(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf_token")), []byte(s.CSRF)) != 1 {
		http.Error(w, "csrf token mismatch", http.StatusForbidden)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.PostFormValue("token")), []byte(srv.authToken)) != 1 {
		srv.metrics.LoginFailures.Add(1)
		srv.audit.Log(audit.Event{Action: "login_failed", Actor: ip})
		http.Redirect(w, r, "/login?error=Invalid+access+token", http.StatusSeeOther)
		return
	}
	srv.setSession(w, newSession(true))
	srv.audit.Log(audit.Event{Action: "login", Actor: ip})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (srv *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	srv.clearSession(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (srv *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	type zoneCard struct {
		Name    string
		Serial  uint32
		Records int
		Dirty   bool
		Targets []string
	}
	zones, err := srv.store.ListZones()
	if err != nil {
		srv.log.Error("list zones", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	cards := make([]zoneCard, 0, len(zones))
	dirtyCount := 0
	for _, z := range zones {
		recs, _ := srv.store.Records(z.Name)
		dirty, _ := srv.store.Dirty(z.Name)
		if dirty {
			dirtyCount++
		}
		cards = append(cards, zoneCard{Name: z.Name, Serial: z.Serial, Records: len(recs), Dirty: dirty, Targets: z.Targets})
	}

	type serverRow struct {
		Name, Host, User string
		Port, Zones      int
	}
	servers := make([]serverRow, 0, len(srv.cfg.Servers))
	for _, name := range srv.cfg.ServerNames() {
		sv := srv.cfg.Servers[name]
		used := 0
		for _, z := range zones {
			for _, t := range z.Targets {
				if t == name {
					used++
				}
			}
		}
		port := sv.Port
		if port == 0 {
			port = 22
		}
		servers = append(servers, serverRow{Name: name, Host: sv.Host, User: sv.User, Port: port, Zones: used})
	}

	srv.render(w, "dashboard.html", srv.pageData(r, "Zones", map[string]any{
		"Cards": cards, "Servers": servers,
		"NumZones": len(cards), "NumServers": len(servers), "NumDirty": dirtyCount,
	}))
}

func (srv *Server) handleServers(w http.ResponseWriter, r *http.Request) {
	zones, _ := srv.store.ListZones()
	type row struct {
		Name, Host, User, RemoteDir string
		Port                        int
		Zones                       []string
	}
	rows := make([]row, 0, len(srv.cfg.Servers))
	for _, name := range srv.cfg.ServerNames() {
		sv := srv.cfg.Servers[name]
		var zs []string
		for _, z := range zones {
			for _, t := range z.Targets {
				if t == name {
					zs = append(zs, z.Name)
				}
			}
		}
		sort.Strings(zs)
		port := sv.Port
		if port == 0 {
			port = 22
		}
		rows = append(rows, row{Name: name, Host: sv.Host, User: sv.User, RemoteDir: sv.RemoteZoneDir, Port: port, Zones: zs})
	}
	srv.render(w, "servers.html", srv.pageData(r, "Servers", map[string]any{"Servers": rows}))
}

// pageData builds the common template data (title, auth state, CSRF) merged
// with page-specific values.
func (srv *Server) pageData(r *http.Request, title string, extra map[string]any) map[string]any {
	s, _ := srv.currentSession(r)
	d := map[string]any{"Title": title, "Authed": true, "CSRF": csrfOf(s)}
	for k, v := range extra {
		d[k] = v
	}
	return d
}

// render executes a named template into a buffer first so a template error
// never produces a half-written response.
func (srv *Server) render(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := srv.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		srv.log.Error("render template", "name", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func csrfOf(s *session) string {
	if s == nil {
		return ""
	}
	return s.CSRF
}

// clientIP returns the source IP of a request, without the port.
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
