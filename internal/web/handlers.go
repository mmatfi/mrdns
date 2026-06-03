package web

import (
	"bytes"
	"crypto/subtle"
	"io"
	"net/http"
	"sort"
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
	// Establish a pre-auth session so the login form carries a CSRF token.
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
		http.Redirect(w, r, "/login?error=Invalid+access+token", http.StatusSeeOther)
		return
	}
	// Issue a fresh authenticated session (new CSRF) to prevent fixation.
	srv.setSession(w, newSession(true))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (srv *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	srv.clearSession(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (srv *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	type zoneRow struct {
		Name    string
		File    string
		Targets []string
	}
	zones := make([]zoneRow, 0, len(srv.cfg.Zones))
	for name, z := range srv.cfg.Zones {
		zones = append(zones, zoneRow{Name: name, File: z.File, Targets: z.Targets})
	}
	sort.Slice(zones, func(i, j int) bool { return zones[i].Name < zones[j].Name })

	type serverRow struct {
		Name string
		Host string
		User string
	}
	servers := make([]serverRow, 0, len(srv.cfg.Servers))
	for name, sv := range srv.cfg.Servers {
		servers = append(servers, serverRow{Name: name, Host: sv.Host, User: sv.User})
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })

	srv.render(w, "dashboard.html", srv.pageData(r, "Zones", map[string]any{
		"Zones":   zones,
		"Servers": servers,
	}))
}

func (srv *Server) handleServers(w http.ResponseWriter, r *http.Request) {
	type row struct {
		Name, Host, User, RemoteDir string
		Port                        int
		Zones                       []string
	}
	rows := make([]row, 0, len(srv.cfg.Servers))
	for name, sv := range srv.cfg.Servers {
		var zones []string
		for zn, zc := range srv.cfg.Zones {
			for _, t := range zc.Targets {
				if t == name {
					zones = append(zones, zn)
				}
			}
		}
		sort.Strings(zones)
		rows = append(rows, row{Name: name, Host: sv.Host, User: sv.User, RemoteDir: sv.RemoteZoneDir, Port: sv.Port, Zones: zones})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

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
