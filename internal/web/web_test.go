package web

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mmatfi/mrdns/internal/audit"
	"github.com/mmatfi/mrdns/internal/config"
	"github.com/mmatfi/mrdns/internal/deploy"
	"github.com/mmatfi/mrdns/internal/metrics"
	"github.com/mmatfi/mrdns/internal/store"
)

const webZone = `$ORIGIN example.com.
$TTL 3600
@ IN SOA ns1.example.com. admin.example.com. 2026060301 7200 3600 1209600 3600
@ IN NS ns1.example.com.
@ IN A 192.0.2.1
www IN A 192.0.2.2
`

type fakeDeployer struct {
	called bool
	zone   string
}

func (f *fakeDeployer) Deploy(_ context.Context, zoneName string) (*deploy.Result, error) {
	f.called = true
	f.zone = zoneName
	return &deploy.Result{
		Zone: zoneName, OldSerial: 2026060301, NewSerial: 2026060302,
		Status: deploy.StatusSuccess, Promoted: true,
		Servers: []deploy.ServerResult{{Name: "ns1", Staged: true, Checked: true, Moved: true, Reloaded: true, Verified: true}},
	}, nil
}

func buildServer(t *testing.T, auditW io.Writer) (*Server, *store.Store, *fakeDeployer) {
	t.Helper()
	d := t.TempDir()
	st, err := store.New(filepath.Join(d, "live"), filepath.Join(d, "drafts"), filepath.Join(d, "backups"), filepath.Join(d, "locks"), 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Promote("example.com.zone", []byte(webZone)); err != nil {
		t.Fatal(err)
	}
	secure := false
	cfg := &config.Config{
		AuthToken: "tok", CookieKey: bytes.Repeat([]byte{7}, 32), SecureCookies: &secure,
		SerialPolicy: "increment",
		Servers: map[string]config.Server{
			"ns1": {Host: "ns1", User: "u", Port: 22, RemoteZoneDir: "/z", CheckzoneCmd: "named-checkzone", ReloadCmd: "rndc reload {zone}"},
		},
		Zones: map[string]config.Zone{"example.com": {File: "example.com.zone", Targets: []string{"ns1"}}},
	}
	dep := &fakeDeployer{}
	srv, err := New(Deps{
		Config: cfg, Store: st, Deployer: dep,
		Audit:   audit.NewWriter(auditW),
		Metrics: metrics.New(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, st, dep
}

func newTestServer(t *testing.T) (*Server, *store.Store, *fakeDeployer) {
	return buildServer(t, io.Discard)
}

// authed builds a request carrying a valid authenticated session and a matching
// CSRF token (form field + header).
func authed(t *testing.T, srv *Server, method, target string, form url.Values) *http.Request {
	t.Helper()
	s := newSession(true)
	val, err := srv.signSession(s)
	if err != nil {
		t.Fatal(err)
	}
	var body io.Reader
	if form != nil {
		form.Set("csrf_token", s.CSRF)
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: val})
	req.Header.Set("X-CSRF-Token", s.CSRF)
	return req
}

func do(srv *Server, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestEditorRequiresAuth(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := do(srv, httptest.NewRequest("GET", "/zones/example.com", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("got %d -> %q, want 303 -> /login", rec.Code, rec.Header().Get("Location"))
	}
}

func TestEditorShowsRecords(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := do(srv, authed(t, srv, "GET", "/zones/example.com", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"www.example.com.", "192.0.2.2", "Deploy", "Add record"} {
		if !strings.Contains(body, want) {
			t.Errorf("editor missing %q", want)
		}
	}
}

func TestAddRecordCreatesDraft(t *testing.T) {
	srv, st, _ := newTestServer(t)
	form := url.Values{"name": {"ftp"}, "ttl": {"3600"}, "type": {"A"}, "data": {"192.0.2.9"}}
	rec := do(srv, authed(t, srv, "POST", "/zones/example.com/records", form))
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "192.0.2.9") {
		t.Error("fragment missing the new record")
	}
	if !st.HasDraft("example.com.zone") {
		t.Fatal("draft was not created")
	}
	draft, _ := st.ReadDraft("example.com.zone")
	if !strings.Contains(string(draft), "ftp.example.com.") {
		t.Errorf("draft missing new record:\n%s", draft)
	}
}

func TestAddInvalidRecordReportsError(t *testing.T) {
	srv, st, _ := newTestServer(t)
	form := url.Values{"name": {"www"}, "ttl": {"3600"}, "type": {"A"}, "data": {"not-an-ip"}}
	rec := do(srv, authed(t, srv, "POST", "/zones/example.com/records", form))
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "invalid") {
		t.Errorf("expected an error message, got:\n%s", rec.Body)
	}
	if st.HasDraft("example.com.zone") {
		t.Error("invalid add should not create a draft")
	}
}

func TestRecordsFragment(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := do(srv, authed(t, srv, "GET", "/zones/example.com/records", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "www.example.com.") {
		t.Fatalf("records fragment: code %d body %q", rec.Code, rec.Body.String())
	}
}

func TestEditRecordFormShowsInputs(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := do(srv, authed(t, srv, "GET", "/zones/example.com/records/edit?name=www.example.com.&type=A&data=192.0.2.2", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `name="old_data"`) || !strings.Contains(body, `value="192.0.2.2"`) {
		t.Errorf("inline edit form not rendered:\n%s", body)
	}
}

func TestUpdateRecordEditsDraft(t *testing.T) {
	srv, st, _ := newTestServer(t)
	form := url.Values{
		"old_name": {"www.example.com."}, "old_type": {"A"}, "old_data": {"192.0.2.2"},
		"name": {"www"}, "ttl": {"3600"}, "type": {"A"}, "data": {"192.0.2.22"},
	}
	rec := do(srv, authed(t, srv, "POST", "/zones/example.com/records/update", form))
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body)
	}
	draft, _ := st.ReadDraft("example.com.zone")
	if !strings.Contains(string(draft), "192.0.2.22") {
		t.Errorf("draft missing the edited value:\n%s", draft)
	}
	if strings.Contains(string(draft), "192.0.2.2\n") {
		t.Errorf("draft still has the old value:\n%s", draft)
	}
}

func TestUpdateRecordInvalidKeepsEditing(t *testing.T) {
	srv, st, _ := newTestServer(t)
	form := url.Values{
		"old_name": {"www.example.com."}, "old_type": {"A"}, "old_data": {"192.0.2.2"},
		"name": {"www"}, "ttl": {"3600"}, "type": {"A"}, "data": {"not-an-ip"},
	}
	rec := do(srv, authed(t, srv, "POST", "/zones/example.com/records/update", form))
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "invalid") {
		t.Errorf("expected an error message, got:\n%s", rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `name="old_data"`) {
		t.Error("expected the row to stay in edit mode on error")
	}
	if st.HasDraft("example.com.zone") {
		t.Error("an invalid update should not write a draft")
	}
}

func TestDeleteRecord(t *testing.T) {
	srv, st, _ := newTestServer(t)
	form := url.Values{"name": {"www.example.com."}, "type": {"A"}, "data": {"192.0.2.2"}}
	rec := do(srv, authed(t, srv, "POST", "/zones/example.com/records/delete", form))
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "192.0.2.2") {
		t.Error("deleted record still present in fragment")
	}
	draft, _ := st.ReadDraft("example.com.zone")
	if strings.Contains(string(draft), "192.0.2.2") {
		t.Error("deleted record still present in draft")
	}
}

func TestRawSaveAndDiscard(t *testing.T) {
	srv, st, _ := newTestServer(t)
	newContent := webZone + "mail IN A 192.0.2.5\n"
	rec := do(srv, authed(t, srv, "POST", "/zones/example.com/raw", url.Values{"content": {newContent}}))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("raw save code %d, want 303", rec.Code)
	}
	if !st.HasDraft("example.com.zone") {
		t.Fatal("raw save did not create a draft")
	}

	rec = do(srv, authed(t, srv, "POST", "/zones/example.com/discard", url.Values{}))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("discard code %d, want 303", rec.Code)
	}
	if st.HasDraft("example.com.zone") {
		t.Error("draft should be gone after discard")
	}
}

func TestRawSaveRejectsInvalid(t *testing.T) {
	srv, st, _ := newTestServer(t)
	rec := do(srv, authed(t, srv, "POST", "/zones/example.com/raw", url.Values{"content": {"this is not a zone"}}))
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d, want 200 (re-render with error)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "does not parse") {
		t.Error("expected a parse error message")
	}
	if st.HasDraft("example.com.zone") {
		t.Error("invalid raw content should not be saved")
	}
}

func TestDeployInvokesDeployer(t *testing.T) {
	srv, _, dep := newTestServer(t)
	rec := do(srv, authed(t, srv, "POST", "/zones/example.com/deploy", url.Values{}))
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if !dep.called || dep.zone != "example.com" {
		t.Fatalf("deployer not invoked correctly: called=%v zone=%q", dep.called, dep.zone)
	}
	if !strings.Contains(rec.Body.String(), "success") {
		t.Error("deploy result page missing status")
	}
}

func TestUnknownZone404(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := do(srv, authed(t, srv, "GET", "/zones/nope.example", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code %d, want 404", rec.Code)
	}
}

func TestCSRFRejectedOnMutation(t *testing.T) {
	srv, _, _ := newTestServer(t)
	// Authenticated session, but a wrong CSRF token.
	s := newSession(true)
	val, _ := srv.signSession(s)
	form := url.Values{"csrf_token": {"wrong"}, "name": {"x"}, "type": {"A"}, "data": {"192.0.2.7"}}
	req := httptest.NewRequest("POST", "/zones/example.com/records", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: val})
	rec := do(srv, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code %d, want 403 for bad CSRF", rec.Code)
	}
}

func TestDeployIsAudited(t *testing.T) {
	var buf bytes.Buffer
	srv, _, _ := buildServer(t, &buf)
	do(srv, authed(t, srv, "POST", "/zones/example.com/deploy", url.Values{}))
	out := buf.String()
	if !strings.Contains(out, `"action":"deploy"`) || !strings.Contains(out, "example.com") {
		t.Errorf("deploy was not audited:\n%s", out)
	}
}

func TestLoginRateLimited(t *testing.T) {
	srv, _, _ := newTestServer(t)
	var last int
	for i := 0; i < 7; i++ {
		req := httptest.NewRequest("POST", "/login", strings.NewReader(""))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		last = do(srv, req).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("after repeated attempts got %d, want 429", last)
	}
}

func TestMetricsEndpointPublic(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := do(srv, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "mrdns_http_requests_total") {
		t.Fatalf("metrics endpoint: code %d body %q", rec.Code, rec.Body.String())
	}
}
