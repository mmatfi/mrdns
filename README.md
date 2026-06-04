# mrdns

A small Go service to manage DNS zones in a database through a web UI and
deploy them as BIND zone files over SSH, reloading `named`.

- **Database-backed** — zones and records live in **SQLite**; you never edit raw
  files. The BIND zone file is generated at deploy time.
- **UI-only** — server-rendered HTML + htmx (dark/light themes), no JSON API.
- **Structured editing** — create a zone from a form, add/edit/delete records
  inline; deploy renders, validates, ships over SSH, and reloads `named`.
- **Single access token** auth → signed session cookie, CSRF-protected.
- **Two-phase deploy** — validate on every target before committing on any —
  with diff, history/rollback, an audit log, and Prometheus `/metrics`.

See [DESIGN.md](DESIGN.md) for the architecture.

## Build & run (local dev)

```sh
make run        # builds and runs against configs/mrdns.dev.yaml (state under ./.dev)
# open http://127.0.0.1:8080/ → sign in with the token (defaults to "dev-token")
# then click "New zone" to create one — no files to edit
```

Override secrets or use other targets:

```sh
export MRDNS_TOKEN=$(openssl rand -hex 32)
export MRDNS_COOKIE_KEY=$(openssl rand -hex 32)
make run

make check      # gofmt + vet + test
make race       # tests under the race detector
make secrets    # print strong MRDNS_TOKEN / MRDNS_COOKIE_KEY values
```

`secure_cookies: false` (dev) lets sessions work over plain HTTP on localhost —
set it `true` in production and serve behind TLS.

## Install (/opt/mrdns)

```sh
sudo ./deploy/install.sh         # builds, creates the mrdns user, lays out /opt/mrdns
                                 # (re-running it restarts the service so upgrades take effect)
# then edit /opt/mrdns/etc/mrdns.env and mrdns.yaml, and on first install:
sudo systemctl enable --now mrdns
```

## Configuration

`/opt/mrdns/etc/mrdns.yaml` declares the **target servers and settings** only —
**zones and records are created in the web UI** and stored in
`<data_dir>/mrdns.db`. Secrets come from the environment
(`/opt/mrdns/etc/mrdns.env`). See [configs/mrdns.example.yaml](configs/mrdns.example.yaml).

`/metrics` exposes Prometheus counters; the audit trail is JSONL at `audit_log`.

## Target server prerequisites

Each nameserver needs the `mrdns-deploy` user, the service's SSH public key in
its `authorized_keys`, write access to `remote_zone_dir`, a `named.conf` zone
pointing at `<remote_zone_dir>/<zone>.zone`, and the scoped sudo rule in
[deploy/sudoers.example](deploy/sudoers.example). See
[deploy/e2e/README.md](deploy/e2e/README.md) for a throwaway test target.
