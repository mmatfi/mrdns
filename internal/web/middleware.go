package web

import (
	"crypto/subtle"
	"net/http"
	"runtime/debug"
	"time"
)

// requireAuth redirects unauthenticated requests to /login and enforces a CSRF
// token on state-changing methods (checked against the session's token, sent
// either as the X-CSRF-Token header or a csrf_token form field).
func (srv *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, ok := srv.currentSession(r)
		if !ok || !s.Authed {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if isMutating(r.Method) {
			token := r.Header.Get("X-CSRF-Token")
			if token == "" {
				_ = r.ParseForm()
				token = r.PostFormValue("csrf_token")
			}
			if subtle.ConstantTimeCompare([]byte(token), []byte(s.CSRF)) != 1 {
				http.Error(w, "csrf token mismatch", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func (srv *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.metrics.HTTPRequests.Add(1)
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		srv.log.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"dur", time.Since(start).String(),
			"remote", r.RemoteAddr,
		)
	})
}

func (srv *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				srv.log.Error("panic recovered", "err", rec, "stack", string(debug.Stack()))
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
