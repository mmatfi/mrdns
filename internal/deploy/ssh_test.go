package deploy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/mmatfi/mrdns/internal/config"
)

// TestSSHConnTransport exercises the real SSH transport against an in-process
// server: strict host-key pinning, public-key auth, command execution, and an
// SFTP upload. No external server or Docker required.
func TestSSHConnTransport(t *testing.T) {
	// Host key for the fake server.
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}

	// Client key, written to a file for dialSSH to read.
	_, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientSigner, err := ssh.NewSignerFromKey(clientPriv)
	if err != nil {
		t.Fatal(err)
	}
	pemBlock, err := ssh.MarshalPrivateKey(clientPriv, "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(pemBlock), 0o600); err != nil {
		t.Fatal(err)
	}

	srvCfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) == string(clientSigner.PublicKey().Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, errMsg("unauthorized key")
		},
	}
	srvCfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()
	go serveOneSSH(t, ln, srvCfg)

	// Pin the host key.
	khPath := filepath.Join(dir, "known_hosts")
	line := knownhosts.Line([]string{addr}, hostSigner.PublicKey())
	if err := os.WriteFile(khPath, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	srv := config.Server{
		Host:          host,
		Port:          port,
		User:          "tester",
		SSHKey:        keyPath,
		KnownHosts:    khPath,
		RemoteZoneDir: dir,
	}

	ctx := context.Background()
	conn, err := dialSSH(ctx, srv)
	if err != nil {
		t.Fatalf("dialSSH: %v", err)
	}
	defer conn.Close()

	// Exec.
	out, err := conn.Run(ctx, "named-checkzone example.com /tmp/x")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "ok\n" {
		t.Errorf("Run output = %q, want %q", out, "ok\n")
	}

	// SFTP upload, then read it back from the server's filesystem.
	target := filepath.Join(dir, "uploaded.zone")
	if err := conn.Upload(ctx, target, []byte("zone-data"), 0o644); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read uploaded file: %v", err)
	}
	if string(got) != "zone-data" {
		t.Errorf("uploaded content = %q, want %q", got, "zone-data")
	}
}

func TestDialSSHRequiresKnownHosts(t *testing.T) {
	_, err := dialSSH(context.Background(), config.Server{Host: "h", User: "u"})
	if err == nil {
		t.Fatal("expected error when known_hosts is unset")
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"example.com": `'example.com'`,
		"a b":         `'a b'`,
		"it's":        `'it'\''s'`,
		"$(rm -rf /)": `'$(rm -rf /)'`,
		"x; reboot":   `'x; reboot'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

type errMsg string

func (e errMsg) Error() string { return string(e) }

// serveOneSSH accepts a single connection and serves session channels: "exec"
// returns canned output, and the "sftp" subsystem is served against the real
// filesystem.
func serveOneSSH(t *testing.T, ln net.Listener, cfg *ssh.ServerConfig) {
	nConn, err := ln.Accept()
	if err != nil {
		return
	}
	sconn, chans, reqs, err := ssh.NewServerConn(nConn, cfg)
	if err != nil {
		t.Logf("server handshake: %v", err)
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)

	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		ch, requests, err := newCh.Accept()
		if err != nil {
			return
		}
		go handleSession(ch, requests)
	}
}

func handleSession(ch ssh.Channel, requests <-chan *ssh.Request) {
	for req := range requests {
		switch req.Type {
		case "exec":
			_ = req.Reply(true, nil)
			_, _ = io.WriteString(ch, "ok\n")
			_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
			_ = ch.Close()
			return
		case "subsystem":
			var payload struct{ Name string }
			_ = ssh.Unmarshal(req.Payload, &payload)
			if payload.Name != "sftp" {
				_ = req.Reply(false, nil)
				continue
			}
			_ = req.Reply(true, nil)
			server, err := sftp.NewServer(ch)
			if err == nil {
				_ = server.Serve()
			}
			_ = ch.Close()
			return
		default:
			_ = req.Reply(false, nil)
		}
	}
}
