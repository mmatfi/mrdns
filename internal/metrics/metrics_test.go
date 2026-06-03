package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExposition(t *testing.T) {
	m := New()
	m.HTTPRequests.Add(3)
	m.LoginFailures.Add(1)
	m.ObserveDeploy("success")
	m.ObserveDeploy("partial")
	m.ObserveDeploy("failed")
	m.ObserveDeploy("weird") // counts as failed

	rec := httptest.NewRecorder()
	m.Handler()(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()

	for _, want := range []string{
		"mrdns_http_requests_total 3",
		"mrdns_login_failures_total 1",
		`mrdns_deploys_total{status="success"} 1`,
		`mrdns_deploys_total{status="partial"} 1`,
		`mrdns_deploys_total{status="failed"} 2`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}
