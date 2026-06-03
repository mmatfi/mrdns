package deploy

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mmatfi/mrdns/internal/config"
	"github.com/mmatfi/mrdns/internal/store"
)

const testZone = `$ORIGIN example.com.
$TTL 3600
@ IN SOA ns1.example.com. admin.example.com. 2026060301 7200 3600 1209600 3600
@ IN NS ns1.example.com.
@ IN A 192.0.2.1
`

// fakeConn is an in-memory Conn for exercising the pipeline orchestration.
type fakeConn struct {
	mu       sync.Mutex
	uploads  map[string][]byte
	commands []string
	closed   bool
	// runErr, if set, returns an error from Run for commands containing the
	// given substring.
	failOn  string
	failErr error
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

func newTestPipeline(t *testing.T, fakes map[string]*fakeConn) (*Pipeline, *store.Store) {
	t.Helper()
	d := t.TempDir()
	st, err := store.New(filepath.Join(d, "live"), filepath.Join(d, "drafts"), filepath.Join(d, "backups"), filepath.Join(d, "locks"), 5)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteDraft("example.com.zone", []byte(testZone)); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		SerialPolicy: "increment",
		Servers: map[string]config.Server{
			"ns1": {Host: "ns1", User: "u", RemoteZoneDir: "/etc/bind/zones", CheckzoneCmd: "named-checkzone", ReloadCmd: "rndc reload {zone}"},
			"ns2": {Host: "ns2", User: "u", RemoteZoneDir: "/etc/bind/zones", CheckzoneCmd: "named-checkzone", ReloadCmd: "rndc reload {zone}"},
		},
		Zones: map[string]config.Zone{
			"example.com": {File: "example.com.zone", Targets: []string{"ns1", "ns2"}},
		},
	}

	p := New(cfg, st, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	p.dial = func(_ context.Context, srv config.Server) (Conn, error) {
		c, ok := fakes[srv.Host]
		if !ok {
			return nil, errors.New("no fake for " + srv.Host)
		}
		return c, nil
	}
	p.verify = func(_ context.Context, host, _ string) (uint32, error) {
		return 2026060302, nil // the expected bumped serial
	}
	// Make local validation deterministically pass without bind-utils.
	if _, err := os.Stat("/bin/true"); err == nil {
		p.checker.Bin = "/bin/true"
	}
	p.now = func() time.Time { return time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC) }
	return p, st
}

func TestDeploySuccess(t *testing.T) {
	fakes := map[string]*fakeConn{"ns1": {}, "ns2": {}}
	p, st := newTestPipeline(t, fakes)

	res, err := p.Deploy(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if res.Status != StatusSuccess {
		t.Fatalf("status = %s, want success", res.Status)
	}
	if res.OldSerial != 2026060301 || res.NewSerial != 2026060302 {
		t.Fatalf("serial %d -> %d, want 2026060301 -> 2026060302", res.OldSerial, res.NewSerial)
	}
	if !res.Promoted {
		t.Error("expected draft to be promoted to live")
	}
	for _, sr := range res.Servers {
		if !(sr.Staged && sr.Checked && sr.Moved && sr.Reloaded && sr.Verified) {
			t.Errorf("server %s incomplete: %+v", sr.Name, sr)
		}
	}
	// Each target got an upload and ran checkzone, mv, and reload.
	for name, f := range fakes {
		if len(f.uploads) != 1 {
			t.Errorf("%s uploads = %d, want 1", name, len(f.uploads))
		}
		if !f.ranContaining("named-checkzone") || !f.ranContaining("mv -f --") || !f.ranContaining("rndc reload") {
			t.Errorf("%s missing expected commands: %v", name, f.commands)
		}
		if !f.closed {
			t.Errorf("%s connection not closed", name)
		}
	}
	// Live now holds the bumped content; the draft is gone.
	if st.HasDraft("example.com.zone") {
		t.Error("draft should be cleared after a successful deploy")
	}
	live, _ := st.ReadLive("example.com.zone")
	if !strings.Contains(string(live), "2026060302") {
		t.Errorf("live content missing new serial:\n%s", live)
	}
}

func TestDeployPhase1AbortLeavesNothingLive(t *testing.T) {
	fakes := map[string]*fakeConn{
		"ns1": {},
		"ns2": {failOn: "named-checkzone", failErr: errors.New("zone invalid")},
	}
	p, st := newTestPipeline(t, fakes)

	res, err := p.Deploy(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("deploy returned error: %v", err)
	}
	if res.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", res.Status)
	}
	if res.Promoted {
		t.Error("nothing should be promoted on a phase-1 abort")
	}
	// No target should have moved anything, and both staged temps are cleaned.
	for name, f := range fakes {
		if f.ranContaining("mv -f --") {
			t.Errorf("%s performed a move despite phase-1 abort", name)
		}
		if !f.ranContaining("rm -f --") {
			t.Errorf("%s staged temp was not cleaned up", name)
		}
	}
	if !st.HasDraft("example.com.zone") {
		t.Error("draft should be preserved after a failed deploy")
	}
}

func TestDeployReloadFailureIsPartial(t *testing.T) {
	fakes := map[string]*fakeConn{
		"ns1": {},
		"ns2": {failOn: "rndc reload", failErr: errors.New("rndc: connection refused")},
	}
	p, st := newTestPipeline(t, fakes)

	res, err := p.Deploy(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if res.Status != StatusPartial {
		t.Fatalf("status = %s, want partial", res.Status)
	}
	// Content reached every target, so it is promoted despite the reload issue.
	if !res.Promoted {
		t.Error("content moved everywhere; expected promotion")
	}
	if st.HasDraft("example.com.zone") {
		t.Error("draft should be cleared once content is live everywhere")
	}
	var ns2 ServerResult
	for _, sr := range res.Servers {
		if sr.Name == "ns2" {
			ns2 = sr
		}
	}
	if !ns2.Moved || ns2.Reloaded {
		t.Errorf("ns2 should be moved-but-not-reloaded: %+v", ns2)
	}
	if !strings.Contains(ns2.Err, "reload") {
		t.Errorf("ns2 error should mention reload: %q", ns2.Err)
	}
}

func TestDeployVerifyMismatchStillSucceeds(t *testing.T) {
	fakes := map[string]*fakeConn{"ns1": {}, "ns2": {}}
	p, _ := newTestPipeline(t, fakes)
	p.verify = func(_ context.Context, host, _ string) (uint32, error) {
		if host == "ns2" {
			return 1, nil // wrong serial
		}
		return 2026060302, nil
	}

	res, err := p.Deploy(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if res.Status != StatusSuccess {
		t.Fatalf("status = %s, want success (verify is best-effort)", res.Status)
	}
	for _, sr := range res.Servers {
		want := sr.Name == "ns1"
		if sr.Verified != want {
			t.Errorf("%s verified = %v, want %v", sr.Name, sr.Verified, want)
		}
	}
}

func TestDeployUnknownZone(t *testing.T) {
	p, _ := newTestPipeline(t, map[string]*fakeConn{"ns1": {}, "ns2": {}})
	if _, err := p.Deploy(context.Background(), "nope.example"); err == nil {
		t.Fatal("expected an error for an unknown zone")
	}
}
