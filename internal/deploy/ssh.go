package deploy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/mmatfi/mrdns/internal/config"
)

// Conn is the set of remote operations the pipeline performs over a single SSH
// connection to a target server. It is an interface so the pipeline can be
// tested without a real server.
type Conn interface {
	Upload(ctx context.Context, remotePath string, content []byte, perm os.FileMode) error
	Run(ctx context.Context, cmd string) (output string, err error)
	Close() error
}

const dialTimeout = 10 * time.Second

type sshConn struct {
	client *ssh.Client
	sftp   *sftp.Client
}

// dialSSH connects to srv with strict host-key pinning sourced from its
// known_hosts file. Encrypted private keys are not supported.
func dialSSH(ctx context.Context, srv config.Server) (Conn, error) {
	if srv.KnownHosts == "" {
		return nil, fmt.Errorf("server %s: known_hosts is required for host-key pinning", srv.Host)
	}
	hostKeyCB, err := knownhosts.New(srv.KnownHosts)
	if err != nil {
		return nil, fmt.Errorf("load known_hosts: %w", err)
	}
	keyBytes, err := os.ReadFile(srv.SSHKey)
	if err != nil {
		return nil, fmt.Errorf("read ssh key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("parse ssh key (encrypted keys unsupported): %w", err)
	}

	port := srv.Port
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(srv.Host, strconv.Itoa(port))
	clientCfg := &ssh.ClientConfig{
		User:            srv.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hostKeyCB,
		Timeout:         dialTimeout,
	}

	dialer := net.Dialer{Timeout: dialTimeout}
	netConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	c, chans, reqs, err := ssh.NewClientConn(netConn, addr, clientCfg)
	if err != nil {
		netConn.Close()
		return nil, fmt.Errorf("ssh handshake %s: %w", addr, err)
	}
	client := ssh.NewClient(c, chans, reqs)
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("start sftp: %w", err)
	}
	return &sshConn{client: client, sftp: sftpClient}, nil
}

func (s *sshConn) Upload(_ context.Context, remotePath string, content []byte, perm os.FileMode) error {
	f, err := s.sftp.OpenFile(remotePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return s.sftp.Chmod(remotePath, perm)
}

func (s *sshConn) Run(ctx context.Context, cmd string) (string, error) {
	session, err := s.client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()

	var buf syncBuffer
	session.Stdout = &buf
	session.Stderr = &buf
	if err := session.Start(cmd); err != nil {
		return "", err
	}

	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		<-done
		return buf.String(), ctx.Err()
	case err := <-done:
		return buf.String(), err
	}
}

func (s *sshConn) Close() error {
	if s.sftp != nil {
		_ = s.sftp.Close()
	}
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}

// dnsVerifySerial queries the SOA serial for zoneName at host:53.
func dnsVerifySerial(ctx context.Context, host, zoneName string) (uint32, error) {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(zoneName), dns.TypeSOA)
	client := &dns.Client{Timeout: 5 * time.Second}
	in, _, err := client.ExchangeContext(ctx, m, net.JoinHostPort(host, "53"))
	if err != nil {
		return 0, err
	}
	for _, rr := range in.Answer {
		if soa, ok := rr.(*dns.SOA); ok {
			return soa.Serial, nil
		}
	}
	return 0, errors.New("no SOA record in response")
}

// shellQuote single-quotes a string for safe inclusion in a remote command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func randomSuffix() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func remoteFinalPath(srv config.Server, file string) string {
	return path.Join(srv.RemoteZoneDir, path.Base(file))
}

func remoteTmpPath(srv config.Server, file string) string {
	return path.Join(srv.RemoteZoneDir, ".mrdns-"+path.Base(file)+"-"+randomSuffix()+".tmp")
}

// syncBuffer is a goroutine-safe buffer. ssh.Session copies stdout and stderr
// from separate goroutines, so a plain bytes.Buffer would race.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
