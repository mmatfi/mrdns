# mrdns — design

A single Go binary that manages DNS zones in a **SQLite database** through a
**web UI** and deploys them as generated BIND zone files over **SSH**, reloading
`named`. UI-only (server-rendered HTML + htmx) — no raw-file editing, no JSON API.

## Decisions

| Area | Choice |
|---|---|
| Source of truth | **SQLite** (zones + records). BIND zone files are generated at deploy time. |
| Interface | Web UI only (`html/template` + htmx) |
| Auth | Single access token → signed session cookie; CSRF on mutations |
| Install | `/opt/mrdns/{bin,etc,var}`; database at `var/mrdns.db` |
| DNS | `github.com/miekg/dns` (build / parse / render / verify) |
| DB driver | `modernc.org/sqlite` (pure Go, so the static binary still builds) |
| Transport | `golang.org/x/crypto/ssh` + `github.com/pkg/sftp`, host-key pinning |
| Validation | `named-checkzone`, locally and on every target |

## Data model

SQLite is the single source of truth — there are no zone files to edit:

- **zones** — name + SOA settings (primary NS, admin mailbox, refresh/retry/
  expire/minimum/TTL), current serial, and target server names.
- **records** — `id, zone, name, ttl, type, data`. The SOA is *not* a record
  row; it is zone settings.
- **snapshots** — the rendered zone text actually deployed at a point in time,
  for diff and rollback, pruned to `backup_keep`.

Editing mutates the database directly. A zone shows **"unpublished changes"**
when its rendered form differs from the latest snapshot. **Deploy** renders the
DB → bumps the SOA serial → ships it → records a snapshot. **Rollback** restores
a snapshot's content back into the DB.

Creating a zone is a form (name + SOA settings + target servers). No file exists
until deploy; the generated file is `<zone>.zone` in each server's
`remote_zone_dir` (point `named.conf` there).

## Install layout

```
/opt/mrdns/
├── bin/mrdns
├── etc/   mrdns.yaml · mrdns.env · id_ed25519 · known_hosts
└── var/   mrdns.db · log/audit.jsonl
```

`mrdns.yaml` declares **servers + settings only** — zones live in the database.

## Deploy pipeline (two-phase)

1. Render the zone from the DB; **bump the SOA serial**.
2. Local `named-checkzone` (warn-and-continue if the binary is absent).
3. **Phase 1 — stage + validate** on every target (SFTP temp + remote
   `named-checkzone`); abort and clean up if any fails — nothing goes live.
4. **Phase 2 — commit + reload** on every target (atomic `mv` + `rndc reload`).
5. **Verify** the SOA serial via DNS (best-effort).
6. If content reached all targets, record a snapshot and persist the new serial.

## HTTP routes (UI only, behind session auth except where noted)

```
/login  /logout
/                              dashboard (zone cards)
/zones/new   POST /zones       create a zone
/zones/{zone}                  editor (inline records)
/zones/{zone}/settings         SOA + targets (GET/POST)    POST /zones/{zone}/delete
/zones/{zone}/records          GET fragment · POST add
        …/records/edit?id  …/records/update  …/records/delete
/zones/{zone}/preview          read-only generated zone file
/zones/{zone}/diff   POST …/validate   POST …/deploy
/zones/{zone}/history   POST …/rollback
/servers
/healthz   /metrics            (public)
```

## Security

Token constant-time compared from env; SSH **host-key pinning** (never
`InsecureIgnoreHostKey`); scoped `rndc` sudoers; record values built via
`miekg/dns` (blocks zone-file injection); `named-checkzone` before every reload;
per-IP login rate limiting; append-only JSONL audit log.

## Layout

```
cmd/mrdns
internal/config   servers + settings, DB path, env secrets
internal/store    SQLite: zones/records/snapshots, render, dirty, publish/restore
internal/zone     parse/render/build, SOA, serial policies, named-checkzone
internal/deploy   ssh transport + two-phase pipeline (storage-agnostic Request)
internal/web      handlers, auth + CSRF, html/template + htmx, dark/light themes
internal/audit    internal/metrics
configs/  deploy/ (systemd unit, sudoers, install.sh, manual e2e)
```

## Status

Feature-complete: DB-backed structured editing (create zone, inline
add/edit/delete records, zone settings), generated-file preview, diff, the
two-phase SSH deploy with a per-server timeline, history/rollback, audit log,
Prometheus metrics, login rate limiting, and a dark/light htmx UI. Tests across
all packages (incl. an in-process SSH server); race-clean.
