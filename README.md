# mrdns

A small Go service to edit BIND zone files through a web UI and deploy them
over SSH to predefined nameservers, reloading `named` afterward.

- **UI-only** — server-rendered HTML + htmx, no JSON API.
- **Flat files** — raw BIND zone files on disk are the source of truth
  (live / draft / backups).
- **Single access token** auth → signed session cookie, CSRF-protected.
- **Two-phase deploy** — validate on every target before committing on any.

See [DESIGN.md](DESIGN.md) for the full design and roadmap.

> Status: **P2** — on top of P0 (scaffold/auth) and P1 (zone core + flat-file
> store), the SSH deploy engine is in: host-key-pinned SSH/SFTP and the
> two-phase pipeline (stage + `named-checkzone` on every target → commit +
> `rndc reload` → verify SOA), with per-server result reporting. The editing
> UI that drives it lands in P3.

## Build & run (local dev)

```sh
go build ./...

export MRDNS_TOKEN=$(openssl rand -hex 32)
export MRDNS_COOKIE_KEY=$(openssl rand -hex 32)

go run ./cmd/mrdns -config configs/mrdns.example.yaml
# open http://127.0.0.1:8080/  → redirected to /login; sign in with $MRDNS_TOKEN
```

`secure_cookies: false` in the example config lets sessions work over plain
HTTP on localhost. Set it to `true` in production and serve behind TLS.

## Install (/opt/mrdns)

```sh
sudo ./deploy/install.sh         # builds, creates the mrdns user, lays out /opt/mrdns
# then edit /opt/mrdns/etc/mrdns.env and mrdns.yaml, and:
sudo systemctl enable --now mrdns
```

## Configuration

All config lives in `/opt/mrdns/etc/mrdns.yaml`; secrets come from the
environment (`/opt/mrdns/etc/mrdns.env`). See
[configs/mrdns.example.yaml](configs/mrdns.example.yaml).

## Target server prerequisites

Each nameserver needs the `mrdns-deploy` user, the service's SSH public key in
its `authorized_keys`, write access to the remote zone directory, and the
scoped sudo rule in [deploy/sudoers.example](deploy/sudoers.example).
