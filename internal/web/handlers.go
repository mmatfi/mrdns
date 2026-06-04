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
	"github.com/mmatfi/mrdns/internal/zone"
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
	// Issue a fresh authenticated session (new CSRF) to prevent fixation.
	srv.setSession(w, newSession(true))
	srv.audit.Log(audit.Event{Action: "login", Actor: ip})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// clientIP returns the source IP of a request, without the port.
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func (srv *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	srv.clearSession(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (srv *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	type zoneCard struct {
		Name     string
		Serial   uint32
		Records  int
		HasDraft bool
		OK       bool
		Note     string
		Targets  []string
	}
	cards := make([]zoneCard, 0, len(srv.cfg.Zones))
	drafts := 0
	for name, zc := range srv.cfg.Zones {
		c := zoneCard{Name: name, Targets: zc.Targets, HasDraft: srv.store.HasDraft(zc.File)}
		if c.HasDraft {
			drafts++
		}
		content, _, err := srv.currentContent(zc)
		if err != nil {
			c.Note = "no zone file"
		} else if z, perr := zone.Parse(content, name); perr != nil {
			c.Note = "does not parse"
		} else {
			c.OK = true
			c.Serial, _ = z.Serial()
			c.Records = z.Len()
		}
		cards = append(cards, c)
	}
	sort.Slice(cards, func(i, j int) bool { return cards[i].Name < cards[j].Name })

	type serverCard struct {
		Name, Host, User string
		Port, Zones      int
	}
	servers := make([]serverCard, 0, len(srv.cfg.Servers))
	for name, sv := range srv.cfg.Servers {
		used := 0
		for _, z := range srv.cfg.Zones {
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
		servers = append(servers, serverCard{Name: name, Host: sv.Host, User: sv.User, Port: port, Zones: used})
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })

	srv.render(w, "dashboard.html", srv.pageData(r, "Zones", map[string]any{
		"Cards": cards, "Servers": servers,
		"NumZones": len(cards), "NumServers": len(servers), "NumDrafts": drafts,
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
