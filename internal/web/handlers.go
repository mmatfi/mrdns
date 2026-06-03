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
	s, _ := srv.currentSession(r)

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

	srv.render(w, "dashboard.html", map[string]any{
		"Title":   "Zones",
		"Authed":  true,
		"CSRF":    csrfOf(s),
		"Zones":   zones,
		"Servers": servers,
	})
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
