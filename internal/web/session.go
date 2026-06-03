package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const (
	sessionCookieName = "mrdns_session"
	sessionTTL        = 12 * time.Hour
)

// session is the payload stored, signed, inside the session cookie. It carries
// the authentication state and a per-session CSRF token.
type session struct {
	Authed bool   `json:"a"`
	Expiry int64  `json:"e"`
	CSRF   string `json:"c"`
}

func (s *session) valid() bool { return time.Now().Unix() < s.Expiry }

func newSession(authed bool) *session {
	return &session{
		Authed: authed,
		Expiry: time.Now().Add(sessionTTL).Unix(),
		CSRF:   newCSRF(),
	}
}

func newCSRF() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// signSession serializes and HMAC-signs a session into a cookie value of the
// form "<payload>.<mac>".
func (srv *Server) signSession(s *session) (string, error) {
	payload, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding.EncodeToString(payload)
	return enc + "." + srv.mac(enc), nil
}

// parseSession verifies the signature and expiry of a cookie value.
func (srv *Server) parseSession(value string) (*session, bool) {
	enc, mac, ok := strings.Cut(value, ".")
	if !ok {
		return nil, false
	}
	if subtle.ConstantTimeCompare([]byte(mac), []byte(srv.mac(enc))) != 1 {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return nil, false
	}
	var s session
	if err := json.Unmarshal(payload, &s); err != nil {
		return nil, false
	}
	if !s.valid() {
		return nil, false
	}
	return &s, true
}

func (srv *Server) mac(data string) string {
	h := hmac.New(sha256.New, srv.cookieKey)
	h.Write([]byte(data))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

func (srv *Server) currentSession(r *http.Request) (*session, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil, false
	}
	return srv.parseSession(c.Value)
}

func (srv *Server) setSession(w http.ResponseWriter, s *session) {
	value, err := srv.signSession(s)
	if err != nil {
		srv.log.Error("sign session", "err", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   srv.secureCookies,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(s.Expiry, 0),
	})
}

func (srv *Server) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   srv.secureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
