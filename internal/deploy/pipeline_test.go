package deploy

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/mmatfi/mrdns/internal/config"
)

const zoneContent = "$ORIGIN example.com.\n$TTL 3600\n" +
	"@ 3600 IN SOA ns1.example.com. admin.example.com. 2 7200 3600 1209600 3600\n" +
	"@ 3600 IN NS ns1.example.com.\nwww 3600 IN A 192.0.2.2\n"

type fakeConn struct {
	mu       sync.Mutex
	uploads  map[string][]byte
	commands []string
	closed   bool
	failOn   string
	failErr  error
}

func (f *fakeConn) Upload(_ context.Context, remotePath string, content []byte, _ os.FileMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.uploads == nil {
		f.uploads = map[string][]byte{}
	}
	f.uploads[remotePath] = append([]byte(nil), content...)
	return nil
}

func (f *fakeConn) Run(_ context.Context, cmd string) (string, error) {
	f.mu.Lock()
	f.commands = append(f.commands, cmd)
	f.mu.Unlock()
	if f.failOn != "" && strings.Contains(cmd, f.failOn) {
		return "boom", f.failErr
	}
	return "OK", nil
}

func (f *fakeConn) Close() error { f.closed = true; return nil }

func (f *fakeConn) ranContaining(sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.commands {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func newTestPipeline(fakes map[string]*fakeConn) *Pipeline {
	cfg := &config.Config{
		Servers: map[string]config.Server{
			"ns1": {Host: "ns1", User: "u", RemoteZoneDir: "/etc/bind/zones", CheckzoneCmd: "named-checkzone", ReloadCmd: "rndc reload {zone}"},
			"ns2": {Host: "ns2", User: "u", RemoteZoneDir: "/etc/bind/zones", CheckzoneCmd: "named-checkzone", ReloadCmd: "rndc reload {zone}"},
		},
	}
	p := New(cfg, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	p.dial = func(_ context.Context, srv config.Server) (Conn, error) {
		c, ok := fakes[srv.Host]
		if !ok {
			return nil, errors.New("no fake for " + srv.Host)
		}
		return c, nil
	}
	p.verify = func(_ context.Context, _, _ string) (uint32, error) { return 2, nil }
	if _, err := os.Stat("/bin/true"); err == nil {
		p.checker.Bin = "/bin/true"
	}
	return p
}

func request() Request {
	return Request{Zone: "example.com", Content: []byte(zoneContent), OldSerial: 1, NewSerial: 2, Targets: []string{"ns1", "ns2"}}
}

func TestDeploySuccess(t *testing.T) {
	fakes := map[string]*fakeConn{"ns1": {}, "ns2": {}}
	res, err := newTestPipeline(fakes).Deploy(context.Background(), request())
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if res.Status != StatusSuccess || !res.AllMoved() {
		t.Fatalf("status=%s allMoved=%v, want success/true", res.Status, res.AllMoved())
	}
	for _, sr := range res.Servers {
		if !(sr.Staged && sr.Checked && sr.Moved && sr.Reloaded && sr.Verified) {
			t.Errorf("server %s incomplete: %+v", sr.Name, sr)
		}
	}
	for name, f := range fakes {
		if len(f.uploads) != 1 {
			t.Errorf("%s uploads=%d, want 1", name, len(f.uploads))
		}
		if !f.ranContaining("named-checkzone") || !f.ranContaining("mv -f --") || !f.ranContaining("rndc reload") {
			t.Errorf("%s missing expected commands: %v", name, f.commands)
		}
		if !f.closed {
			t.Errorf("%s not closed", name)
		}
	}
}

func TestDeployPhase1Abort(t *testing.T) {
	fakes := map[string]*fakeConn{
		"ns1": {},
		"ns2": {failOn: "named-checkzone", failErr: errors.New("zone invalid")},
	}
	res, err := newTestPipeline(fakes).Deploy(context.Background(), request())
	if err != nil {
		t.Fatalf("deploy returned error: %v", err)
	}
	if res.Status != StatusFailed || res.AllMoved() {
		t.Fatalf("status=%s allMoved=%v, want failed/false", res.Status, res.AllMoved())
	}
	for name, f := range fakes {
		if f.ranContaining("mv -f --") {
			t.Errorf("%s moved despite abort", name)
		}
		if !f.ranContaining("rm -f --") {
			t.Errorf("%s staged temp not cleaned up", name)
		}
	}
}

func TestDeployReloadFailureIsPartial(t *testing.T) {
	fakes := map[string]*fakeConn{
		"ns1": {},
		"ns2": {failOn: "rndc reload", failErr: errors.New("connection refused")},
	}
	res, err := newTestPipeline(fakes).Deploy(context.Background(), request())
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if res.Status != StatusPartial {
		t.Fatalf("status=%s, want partial", res.Status)
	}
	if !res.AllMoved() {
		t.Error("content moved everywhere; AllMoved should be true")
	}
}

func TestDeployVerifyMismatch(t *testing.T) {
	fakes := map[string]*fakeConn{"ns1": {}, "ns2": {}}
	p := newTestPipeline(fakes)
	p.verify = func(_ context.Context, host, _ string) (uint32, error) {
		if host == "ns2" {
			return 1, nil
		}
		return 2, nil
	}
	res, err := p.Deploy(context.Background(), request())
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if res.Status != StatusSuccess {
		t.Fatalf("status=%s, want success (verify is best-effort)", res.Status)
	}
	for _, sr := range res.Servers {
		want := sr.Name == "ns1"
		if sr.Verified != want {
			t.Errorf("%s verified=%v, want %v", sr.Name, sr.Verified, want)
		}
	}
}

func TestDeployNoTargets(t *testing.T) {
	req := request()
	req.Targets = nil
	if _, err := newTestPipeline(nil).Deploy(context.Background(), req); err == nil {
		t.Fatal("expected an error with no targets")
	}
}
