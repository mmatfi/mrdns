# mrdns

A small Go service to edit BIND zone files through a web UI and deploy them
over SSH to predefined nameservers, reloading `named` afterward.

- **UI-only** — server-rendered HTML + htmx, no JSON API.
- **Flat files** — raw BIND zone files on disk are the source of truth
  (live / draft / backups).
- **Single access token** auth → signed session cookie, CSRF-protected.
- **Two-phase deploy** — validate on every target before committing on any.

See [DESIGN.md](DESIGN.md) for the full design and roadmap.

> Status: **feature-complete (P0–P4).** Sign in, browse zones, edit records
> (add/delete) or the raw zone file into a draft, review the draft-vs-live diff,
> validate, and deploy over SSH to all targets with per-server results, plus
> history/rollback. Hardening is in: append-only audit log, Prometheus
> `/metrics`, and per-IP login rate limiting. See [DESIGN.md](DESIGN.md) for
> the architecture and the (still-open) niceties.

## Build & run (local dev)

```sh
make run        # builds and runs against configs/mrdns.dev.yaml (state under ./.dev)
# open http://127.0.0.1:8080/ → sign in with the token (defaults to "dev-token")
```

Override the secrets explicitly, or use other targets:

```sh
export MRDNS_TOKEN=$(openssl rand -hex 32)
export MRDNS_COOKIE_KEY=$(openssl rand -hex 32)
make run

make check      # gofmt + vet + test
make race       # tests under the race detector
make secrets    # print strong MRDNS_TOKEN / MRDNS_COOKIE_KEY values
```

Metrics are exposed at `/metrics`; the audit trail is JSONL under
`<zones_dir>/log/`. `secure_cookies: false` in the dev config lets sessions work
over plain HTTP on localhost — set it to `true` in production and serve behind
TLS.

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
