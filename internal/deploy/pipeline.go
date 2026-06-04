package deploy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"

	"github.com/mmatfi/mrdns/internal/config"
	"github.com/mmatfi/mrdns/internal/zone"
)

// DialFunc opens a connection to a target server. Injectable for testing.
type DialFunc func(ctx context.Context, srv config.Server) (Conn, error)

// VerifyFunc queries a server's live SOA serial for a zone. Injectable.
type VerifyFunc func(ctx context.Context, host, zoneName string) (uint32, error)

// Request is a fully-prepared deploy: rendered zone content (with the new
// serial already applied) plus the target server names.
type Request struct {
	Zone      string
	Content   []byte
	OldSerial uint32
	NewSerial uint32
	Targets   []string
}

// Pipeline rolls rendered zone content out to target servers using a two-phase
// approach: stage + validate everywhere, then commit + reload everywhere. It is
// storage-agnostic — the caller renders the content and persists results.
type Pipeline struct {
	cfg     *config.Config
	checker zone.Checker
	dial    DialFunc
	verify  VerifyFunc
	log     *slog.Logger
}

// New builds a Pipeline with real SSH dialing and DNS verification.
func New(cfg *config.Config, log *slog.Logger) *Pipeline {
	return &Pipeline{
		cfg:     cfg,
		checker: zone.Checker{},
		dial:    dialSSH,
		verify:  dnsVerifySerial,
		log:     log,
	}
}

func (p *Pipeline) remoteFile(zoneName string) string { return zoneName + ".zone" }

// Deploy validates the content locally, then stages + validates on every
// target before committing + reloading on any. Verification is best-effort.
func (p *Pipeline) Deploy(ctx context.Context, req Request) (*Result, error) {
	res := &Result{Zone: req.Zone, OldSerial: req.OldSerial, NewSerial: req.NewSerial, Status: StatusFailed, StartedAt: time.Now()}
	defer func() { res.EndedAt = time.Now() }()

	if len(req.Targets) == 0 {
		return res, errors.New("zone has no target servers")
	}

	// Local validation. A genuine failure aborts; inability to run
	// named-checkzone locally is a warning since every target re-validates.
	if out, err := p.checker.Check(ctx, req.Zone, req.Content); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return res, fmt.Errorf("local named-checkzone rejected zone: %s", strings.TrimSpace(out))
		}
		p.log.Warn("local named-checkzone unavailable; relying on remote validation", "err", err)
	}

	zoneFqdn := dns.Fqdn(req.Zone)
	file := p.remoteFile(req.Zone)
	res.Servers = make([]ServerResult, len(req.Targets))
	conns := make([]Conn, len(req.Targets))
	tmpPaths := make([]string, len(req.Targets))

	// Phase 1: dial, stage (upload temp), validate on every target.
	var wg sync.WaitGroup
	for i, name := range req.Targets {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			sr := &res.Servers[i]
			sr.Name = name
			srv, ok := p.cfg.Servers[name]
			if !ok {
				sr.Err = "unknown server in config"
				return
			}
			conn, err := p.dial(ctx, srv)
			if err != nil {
				sr.Err = "connect: " + err.Error()
				return
			}
			conns[i] = conn

			tmp := remoteTmpPath(srv, file)
			tmpPaths[i] = tmp
			if err := conn.Upload(ctx, tmp, req.Content, 0o644); err != nil {
				sr.Err = "upload: " + err.Error()
				return
			}
			sr.Staged = true

			checkCmd := fmt.Sprintf("%s %s %s", srv.CheckzoneCmd, shellQuote(zoneFqdn), shellQuote(tmp))
			out, err := conn.Run(ctx, checkCmd)
			sr.Output = strings.TrimSpace(out)
			if err != nil {
				sr.Err = "checkzone: " + err.Error()
				return
			}
			sr.Checked = true
		}(i, name)
	}
	wg.Wait()

	for i := range res.Servers {
		if !res.Servers[i].Checked {
			p.cleanup(ctx, conns, tmpPaths)
			closeAll(conns)
			res.Status = StatusFailed
			return res, nil
		}
	}

	// Phase 2: commit (move into place) and reload on every target.
	for i, name := range req.Targets {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			sr := &res.Servers[i]
			srv := p.cfg.Servers[name]
			conn := conns[i]

			dst := remoteFinalPath(srv, file)
			moveCmd := fmt.Sprintf("mv -f -- %s %s", shellQuote(tmpPaths[i]), shellQuote(dst))
			if out, err := conn.Run(ctx, moveCmd); err != nil {
				sr.Output = strings.TrimSpace(out)
				sr.Err = "move: " + err.Error()
				return
			}
			sr.Moved = true

			reloadCmd := strings.ReplaceAll(srv.ReloadCmd, "{zone}", shellQuote(req.Zone))
			out, err := conn.Run(ctx, reloadCmd)
			sr.Output = strings.TrimSpace(out)
			if err != nil {
				sr.Err = "reload: " + err.Error()
				return
			}
			sr.Reloaded = true
		}(i, name)
	}
	wg.Wait()

	// Phase 3: verify the new serial is live (best-effort).
	for i, name := range req.Targets {
		if !res.Servers[i].Reloaded {
			continue
		}
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			srv := p.cfg.Servers[name]
			got, err := p.verify(ctx, srv.Host, req.Zone)
			switch {
			case err != nil:
				p.log.Warn("verify query failed", "zone", req.Zone, "server", name, "err", err)
			case got != req.NewSerial:
				p.log.Warn("verify serial mismatch", "zone", req.Zone, "server", name, "want", req.NewSerial, "got", got)
			default:
				res.Servers[i].Verified = true
			}
		}(i, name)
	}
	wg.Wait()

	closeAll(conns)

	res.Status = StatusSuccess
	for i := range res.Servers {
		if !res.Servers[i].Moved || !res.Servers[i].Reloaded {
			res.Status = StatusPartial
		}
	}
	return res, nil
}

func (p *Pipeline) cleanup(ctx context.Context, conns []Conn, tmpPaths []string) {
	for i, conn := range conns {
		if conn == nil || tmpPaths[i] == "" {
			continue
		}
		rmCmd := fmt.Sprintf("rm -f -- %s", shellQuote(tmpPaths[i]))
		if _, err := conn.Run(ctx, rmCmd); err != nil {
			p.log.Warn("cleanup staged temp failed", "path", tmpPaths[i], "err", err)
		}
	}
}

func closeAll(conns []Conn) {
	for _, c := range conns {
		if c != nil {
			_ = c.Close()
		}
	}
}
