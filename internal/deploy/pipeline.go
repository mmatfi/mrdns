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
	"github.com/mmatfi/mrdns/internal/store"
	"github.com/mmatfi/mrdns/internal/zone"
)

// DialFunc opens a connection to a target server. Injectable for testing.
type DialFunc func(ctx context.Context, srv config.Server) (Conn, error)

// VerifyFunc queries a server's live SOA serial for a zone. Injectable.
type VerifyFunc func(ctx context.Context, host, zoneName string) (uint32, error)

// Pipeline orchestrates validate-and-deploy of zones to their target servers.
type Pipeline struct {
	cfg     *config.Config
	store   *store.Store
	checker zone.Checker
	dial    DialFunc
	verify  VerifyFunc
	now     func() time.Time
	log     *slog.Logger
}

// New builds a Pipeline with real SSH dialing and DNS verification.
func New(cfg *config.Config, st *store.Store, log *slog.Logger) *Pipeline {
	return &Pipeline{
		cfg:     cfg,
		store:   st,
		checker: zone.Checker{},
		dial:    dialSSH,
		verify:  dnsVerifySerial,
		now:     time.Now,
		log:     log,
	}
}

// Deploy validates the zone's pending content and rolls it out to every target
// server using a two-phase approach: stage + validate everywhere, then commit +
// reload everywhere. The local draft is promoted to live only if the content
// reached all targets.
func (p *Pipeline) Deploy(ctx context.Context, zoneName string) (*Result, error) {
	res := &Result{Zone: zoneName, Status: StatusFailed, StartedAt: p.now()}
	defer func() { res.EndedAt = p.now() }()

	zcfg, ok := p.cfg.Zones[zoneName]
	if !ok {
		return res, fmt.Errorf("unknown zone %q", zoneName)
	}

	// Source content: prefer the draft, fall back to the current live copy.
	content, err := p.store.ReadDraft(zcfg.File)
	if errors.Is(err, store.ErrNoDraft) {
		content, err = p.store.ReadLive(zcfg.File)
	}
	if err != nil {
		return res, fmt.Errorf("read zone source: %w", err)
	}

	// Parse, advance the SOA serial, and render the final content.
	z, err := zone.Parse(content, zoneName)
	if err != nil {
		return res, fmt.Errorf("parse zone: %w", err)
	}
	oldSerial, newSerial, err := z.BumpSerial(zone.SerialPolicy(p.cfg.SerialPolicy), p.now())
	if err != nil {
		return res, fmt.Errorf("bump serial: %w", err)
	}
	res.OldSerial, res.NewSerial = oldSerial, newSerial
	final := z.Render()

	// Local validation. A genuine validation failure aborts; an inability to
	// run named-checkzone locally is downgraded to a warning, since every
	// target re-validates during staging.
	if out, err := p.checker.Check(ctx, zoneName, final); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return res, fmt.Errorf("local named-checkzone rejected zone: %s", strings.TrimSpace(out))
		}
		p.log.Warn("local named-checkzone unavailable; relying on remote validation", "err", err)
	}

	zoneFqdn := dns.Fqdn(zoneName)
	targets := zcfg.Targets
	res.Servers = make([]ServerResult, len(targets))
	conns := make([]Conn, len(targets))
	tmpPaths := make([]string, len(targets))

	// Phase 1: dial, stage (upload temp), and validate on every target.
	var wg sync.WaitGroup
	for i, name := range targets {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			sr := &res.Servers[i]
			sr.Name = name
			srv := p.cfg.Servers[name]

			conn, err := p.dial(ctx, srv)
			if err != nil {
				sr.Err = "connect: " + err.Error()
				return
			}
			conns[i] = conn

			tmp := remoteTmpPath(srv, zcfg.File)
			tmpPaths[i] = tmp
			if err := conn.Upload(ctx, tmp, final, 0o644); err != nil {
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

	// Abort if staging or validation failed anywhere: nothing has gone live.
	for i := range res.Servers {
		if !res.Servers[i].Checked {
			p.cleanup(ctx, conns, tmpPaths)
			closeAll(conns)
			res.Status = StatusFailed
			return res, nil
		}
	}

	// Phase 2: commit (move into place) and reload on every target.
	for i, name := range targets {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			sr := &res.Servers[i]
			srv := p.cfg.Servers[name]
			conn := conns[i]

			dst := remoteFinalPath(srv, zcfg.File)
			moveCmd := fmt.Sprintf("mv -f -- %s %s", shellQuote(tmpPaths[i]), shellQuote(dst))
			if out, err := conn.Run(ctx, moveCmd); err != nil {
				sr.Output = strings.TrimSpace(out)
				sr.Err = "move: " + err.Error()
				return
			}
			sr.Moved = true

			reloadCmd := strings.ReplaceAll(srv.ReloadCmd, "{zone}", shellQuote(zoneName))
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
	for i, name := range targets {
		if !res.Servers[i].Reloaded {
			continue
		}
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			srv := p.cfg.Servers[name]
			got, err := p.verify(ctx, srv.Host, zoneName)
			switch {
			case err != nil:
				p.log.Warn("verify query failed", "zone", zoneName, "server", name, "err", err)
			case got != newSerial:
				p.log.Warn("verify serial mismatch", "zone", zoneName, "server", name, "want", newSerial, "got", got)
			default:
				res.Servers[i].Verified = true
			}
		}(i, name)
	}
	wg.Wait()

	closeAll(conns)

	// Status and local promotion.
	res.Status = StatusSuccess
	allMoved := true
	for i := range res.Servers {
		if !res.Servers[i].Moved {
			allMoved = false
		}
		if !res.Servers[i].Moved || !res.Servers[i].Reloaded {
			res.Status = StatusPartial
		}
	}
	if allMoved {
		if _, err := p.store.Promote(zcfg.File, final); err != nil {
			p.log.Error("promote draft to live failed", "zone", zoneName, "err", err)
			res.Status = StatusPartial
		} else {
			res.Promoted = true
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
