// Package metrics exposes a small set of Prometheus-format counters without an
// external client library.
package metrics

import (
	"fmt"
	"net/http"
	"sync/atomic"
)

// Metrics holds the service counters.
type Metrics struct {
	HTTPRequests  atomic.Int64
	LoginFailures atomic.Int64
	DeploySuccess atomic.Int64
	DeployPartial atomic.Int64
	DeployFailed  atomic.Int64
}

// New returns a zeroed Metrics.
func New() *Metrics { return &Metrics{} }

// ObserveDeploy increments the deploy counter for the given status string.
func (m *Metrics) ObserveDeploy(status string) {
	switch status {
	case "success":
		m.DeploySuccess.Add(1)
	case "partial":
		m.DeployPartial.Add(1)
	default:
		m.DeployFailed.Add(1)
	}
}

// Handler writes the metrics in Prometheus text exposition format.
func (m *Metrics) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprintf(w, "# HELP mrdns_http_requests_total Total HTTP requests served.\n")
		fmt.Fprintf(w, "# TYPE mrdns_http_requests_total counter\n")
		fmt.Fprintf(w, "mrdns_http_requests_total %d\n", m.HTTPRequests.Load())
		fmt.Fprintf(w, "# HELP mrdns_login_failures_total Failed login attempts.\n")
		fmt.Fprintf(w, "# TYPE mrdns_login_failures_total counter\n")
		fmt.Fprintf(w, "mrdns_login_failures_total %d\n", m.LoginFailures.Load())
		fmt.Fprintf(w, "# HELP mrdns_deploys_total Deploys by outcome.\n")
		fmt.Fprintf(w, "# TYPE mrdns_deploys_total counter\n")
		fmt.Fprintf(w, "mrdns_deploys_total{status=\"success\"} %d\n", m.DeploySuccess.Load())
		fmt.Fprintf(w, "mrdns_deploys_total{status=\"partial\"} %d\n", m.DeployPartial.Load())
		fmt.Fprintf(w, "mrdns_deploys_total{status=\"failed\"} %d\n", m.DeployFailed.Load())
	}
}
