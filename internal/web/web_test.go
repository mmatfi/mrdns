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
	"strconv"
	"strings"
	"testing"

	"github.com/mmatfi/mrdns/internal/audit"
	"github.com/mmatfi/mrdns/internal/config"
	"github.com/mmatfi/mrdns/internal/deploy"
	"github.com/mmatfi/mrdns/internal/metrics"
	"github.com/mmatfi/mrdns/internal/store"
	"github.com/mmatfi/mrdns/internal/zone"
)

type fakeDeployer struct {
	called bool
	req    deploy.Request
}

func (f *fakeDeployer) Deploy(_ context.Context, req deploy.Request) (*deploy.Result, error) {
	f.called = true
	f.req = req
	return &deploy.Result{
		Zone: req.Zone, OldSerial: req.OldSerial, NewSerial: req.NewSerial,
		Status:  deploy.StatusSuccess,
		Servers: []deploy.ServerResult{{Name: "ns1", Staged: true, Checked: true, Moved: true, Reloaded: true, Verified: true}},
	}, nil
}

func buildServer(t *testing.T, auditW io.Writer) (*Server, *store.Store, *fakeDeployer) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"), 5)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateZone(store.Zone{Name: "example.com", PrimaryNS: "ns1.example.com", Mbox: "admin@example.com", Targets: []string{"ns1"}}); err != nil {
		t.Fatal(err)
	}
	r, err := zone.NormalizeRecord("example.com", "www", 3600, "A", "192.0.2.2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddRecord("example.com", r); err != nil {
		t.Fatal(err)
	}

	secure := false
	cfg := &config.Config{
		AuthToken: "tok", CookieKey: bytes.Repeat([]byte{7}, 32), SecureCookies: &secure,
		SerialPolicy: "increment",
		Servers: map[string]config.Server{
			"ns1": {Host: "ns1", User: "u", Port: 22, RemoteZoneDir: "/z", CheckzoneCmd: "named-checkzone", ReloadCmd: "rndc reload {zone}"},
		},
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

func wwwID(t *testing.T, st *store.Store) int64 {
	t.Helper()
	recs, _ := st.Records("example.com")
	for _, r := range recs {
		if r.Name == "www.example.com." {
			return r.ID
		}
	}
	t.Fatal("www record not found")
	return 0
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
	for _, want := range []string{"www.example.com.", "192.0.2.2", "Deploy", "Add"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("editor missing %q", want)
		}
	}
}

func TestCreateZone(t *testing.T) {
	srv, st, _ := newTestServer(t)
	form := url.Values{
		"name": {"new.example.com"}, "primary_ns": {"ns1.new.example.com"}, "mbox": {"admin@new.example.com"},
		"targets": {"ns1"},
	}
	rec := do(srv, authed(t, srv, "POST", "/zones", form))
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/zones/new.example.com") {
		t.Fatalf("create zone: %d -> %q", rec.Code, rec.Header().Get("Location"))
	}
	if ok, _ := st.ZoneExists("new.example.com"); !ok {
		t.Error("zone was not created in the store")
	}
}

func TestCreateZoneRejectsBad(t *testing.T) {
	srv, _, _ := newTestServer(t)
	form := url.Values{"name": {"bad.example.com"}, "primary_ns": {""}, "mbox": {"admin@bad.example.com"}}
	rec := do(srv, authed(t, srv, "POST", "/zones", form))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "required") {
		t.Fatalf("expected re-rendered form with error; got %d", rec.Code)
	}
}

func TestAddRecordPersists(t *testing.T) {
	srv, st, _ := newTestServer(t)
	form := url.Values{"name": {"ftp"}, "ttl": {"3600"}, "type": {"A"}, "data": {"192.0.2.9"}}
	rec := do(srv, authed(t, srv, "POST", "/zones/example.com/records", form))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "192.0.2.9") {
		t.Fatalf("add record: %d body %s", rec.Code, rec.Body)
	}
	recs, _ := st.Records("example.com")
	if len(recs) != 2 {
		t.Fatalf("records = %d, want 2", len(recs))
	}
}

func TestAddInvalidRecord(t *testing.T) {
	srv, st, _ := newTestServer(t)
	form := url.Values{"name": {"x"}, "type": {"A"}, "data": {"not-an-ip"}}
	rec := do(srv, authed(t, srv, "POST", "/zones/example.com/records", form))
	if rec.Code != http.StatusOK || !strings.Contains(strings.ToLower(rec.Body.String()), "invalid") {
		t.Fatalf("expected invalid-record error; got %d body %s", rec.Code, rec.Body)
	}
	if recs, _ := st.Records("example.com"); len(recs) != 1 {
		t.Errorf("invalid add should not persist; records = %d", len(recs))
	}
}

func TestUpdateAndDeleteRecord(t *testing.T) {
	srv, st, _ := newTestServer(t)
	id := wwwID(t, st)

	form := url.Values{"id": {strconv.FormatInt(id, 10)}, "name": {"www"}, "ttl": {"3600"}, "type": {"A"}, "data": {"192.0.2.42"}}
	if rec := do(srv, authed(t, srv, "POST", "/zones/example.com/records/update", form)); rec.Code != http.StatusOK {
		t.Fatalf("update: %d", rec.Code)
	}
	recs, _ := st.Records("example.com")
	if recs[0].Data != "192.0.2.42" {
		t.Errorf("record data = %q, want 192.0.2.42", recs[0].Data)
	}

	del := url.Values{"id": {strconv.FormatInt(id, 10)}}
	if rec := do(srv, authed(t, srv, "POST", "/zones/example.com/records/delete", del)); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}
	if recs, _ := st.Records("example.com"); len(recs) != 0 {
		t.Errorf("after delete records = %d, want 0", len(recs))
	}
}

func TestDeployInvokesDeployerAndPublishes(t *testing.T) {
	srv, st, dep := newTestServer(t)
	rec := do(srv, authed(t, srv, "POST", "/zones/example.com/deploy", url.Values{}))
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if !dep.called || dep.req.Zone != "example.com" || len(dep.req.Content) == 0 {
		t.Fatalf("deployer not invoked correctly: %+v", dep.req)
	}
	if !strings.Contains(rec.Body.String(), "success") {
		t.Error("deploy result page missing status")
	}
	if dirty, _ := st.Dirty("example.com"); dirty {
		t.Error("zone should be clean (published) after a successful deploy")
	}
}

func TestImportZoneUI(t *testing.T) {
	srv, st, _ := newTestServer(t)
	content := "$ORIGIN new.example.\n$TTL 3600\n" +
		"@ IN SOA ns1.new.example. host.new.example. 2026010101 7200 3600 1209600 3600\n" +
		"@ IN NS ns1.new.example.\nwww IN A 192.0.2.7\n"
	form := url.Values{"name": {"new.example"}, "content": {content}, "targets": {"ns1"}}
	rec := do(srv, authed(t, srv, "POST", "/zones/import", form))
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/zones/new.example") {
		t.Fatalf("import: %d -> %q", rec.Code, rec.Header().Get("Location"))
	}
	if ok, _ := st.ZoneExists("new.example"); !ok {
		t.Error("imported zone is missing from the store")
	}
	if recs, _ := st.Records("new.example"); len(recs) != 2 {
		t.Errorf("imported records = %d, want 2", len(recs))
	}
}

func TestImportInvalidUI(t *testing.T) {
	srv, _, _ := newTestServer(t)
	form := url.Values{"name": {"bad.example"}, "content": {"this is not a zone file"}, "targets": {"ns1"}}
	rec := do(srv, authed(t, srv, "POST", "/zones/import", form))
	if rec.Code != http.StatusOK || !strings.Contains(strings.ToLower(rec.Body.String()), "parse") {
		t.Fatalf("expected a re-rendered form with a parse error; got %d", rec.Code)
	}
}

func TestUnknownZone404(t *testing.T) {
	srv, _, _ := newTestServer(t)
	if rec := do(srv, authed(t, srv, "GET", "/zones/nope.example", nil)); rec.Code != http.StatusNotFound {
		t.Fatalf("code %d, want 404", rec.Code)
	}
}

func TestDeleteZone(t *testing.T) {
	srv, st, _ := newTestServer(t)
	rec := do(srv, authed(t, srv, "POST", "/zones/example.com/delete", url.Values{}))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete zone: %d", rec.Code)
	}
	if ok, _ := st.ZoneExists("example.com"); ok {
		t.Error("zone should be gone after delete")
	}
}

func TestCSRFRejectedOnMutation(t *testing.T) {
	srv, _, _ := newTestServer(t)
	s := newSession(true)
	val, _ := srv.signSession(s)
	form := url.Values{"name": {"x"}, "type": {"A"}, "data": {"192.0.2.7"}}
	req := httptest.NewRequest("POST", "/zones/example.com/records", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: val}) // no CSRF header/field
	if rec := do(srv, req); rec.Code != http.StatusForbidden {
		t.Fatalf("code %d, want 403 for missing CSRF", rec.Code)
	}
}

func TestDeployIsAudited(t *testing.T) {
	var buf bytes.Buffer
	srv, _, _ := buildServer(t, &buf)
	do(srv, authed(t, srv, "POST", "/zones/example.com/deploy", url.Values{}))
	if !strings.Contains(buf.String(), `"action":"deploy"`) {
		t.Errorf("deploy not audited:\n%s", buf.String())
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
		t.Fatalf("metrics: code %d", rec.Code)
	}
}
