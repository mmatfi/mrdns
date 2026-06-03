# mrdns — design

A single Go binary that edits BIND zone files through a **web UI**, validates
them, and deploys them over **SSH** to predefined BIND servers, reloading
`named` afterward. UI-only: server-rendered HTML + htmx, **no JSON API**.

## Decisions

| Area | Choice |
|---|---|
| Source of truth | Raw BIND zone files on the service's local disk |
| Interface | Web UI only (server-rendered `html/template` + htmx) |
| Auth | Single access token → signed session cookie; CSRF on mutations |
| Install layout | `/opt/mrdns/{bin,etc,var}` |
| DNS library | `github.com/miekg/dns` (parse/render/verify) — *P1/P2* |
| Transport | `golang.org/x/crypto/ssh` + `github.com/pkg/sftp`, host-key pinning — *P2* |
| Validation | `named-checkzone`, run locally and remotely — *P1/P2* |

## Install layout

```
/opt/mrdns/
├── bin/mrdns                       # the single binary
├── etc/
│   ├── mrdns.yaml                  # config (no secrets)
│   ├── mrdns.env                   # MRDNS_TOKEN, MRDNS_COOKIE_KEY (0640, systemd EnvironmentFile)
│   ├── id_ed25519                  # SSH deploy key (0600)
│   └── known_hosts                 # pinned target host keys
└── var/
    ├── live/      example.com.zone         # last successfully deployed content
    ├── drafts/    example.com.zone         # work-in-progress edits
    ├── backups/   example.com.zone.<serial>.<ts>
    └── log/audit.jsonl
```

Runs as a dedicated `mrdns` user that owns `var/` and only reads `etc/`.

## Data model: live / draft / backup

- **Edit → draft.** UI edits write to `drafts/`, never to the live file.
- **Diff** = draft vs live (what is about to ship).
- **Deploy** validates the draft, pushes it to every target, reloads, and on
  success promotes draft → live (snapshotting the old live into `backups/`,
  keeping the last N).

This gives an edit → preview → deploy flow plus rollback, with no database.

## Auth & CSRF

- `/login` takes the access token (constant-time compared against
  `MRDNS_TOKEN`) and issues an HMAC-signed, `HttpOnly`/`SameSite=Lax` session
  cookie (`Secure` when `secure_cookies: true`).
- The signed cookie carries a per-session CSRF token. Every mutating request
  (POST/PUT/PATCH/DELETE) must echo it via the `X-CSRF-Token` header (htmx) or
  a `csrf_token` form field; verified constant-time.
- Successful login rotates the session (prevents fixation). `/healthz` is open.

## Deploy pipeline (two-phase, validate-everywhere-before-commit)

A bad zone never goes live, and multi-server deploys are near-atomic.

1. Acquire per-zone lock (in-process mutex + `flock` on the file).
2. Render draft → text; **bump the SOA serial** (default `YYYYMMDDnn`; BIND
   ignores changes if the serial does not increase — the #1 footgun).
3. **Local validation:** `named-checkzone <zone> <tmp>`. Abort on failure —
   nothing leaves the box.
4. **Phase 1 — stage + validate on every server** (concurrent): SFTP to a temp
   path, run `named-checkzone` remotely. If **any** server fails, clean up all
   temp files and abort — zero changes go live.
5. **Phase 2 — commit + reload on every server:** atomic `mv` temp → final
   path (preserving owner/perms), then `rndc reload <zone>`.
6. **Verify:** query each server's SOA (`dig`-equivalent via `miekg/dns`) and
   confirm the new serial is being served.
7. On full success: write the live file locally, snapshot the old live to
   `backups/`, delete the draft, append an audit entry. Report per-server
   status; surface partial failures with one-click redeploy.

## SOA serial policies

`date` (default): `YYYYMMDDnn` — bump `nn` if today's date already present,
else `YYYYMMDD00`; roll over to unixtime if `nn` overflows. Also `unixtime` and
`increment`.

## HTTP routes (UI only, behind session auth except where noted)

```
GET  /login        POST /login        POST /logout
GET  /             zone list / dashboard
GET  /zones/{zone} record-table editor (draft vs live) + raw-text toggle
POST /zones/{zone}/records[/{key}/edit|delete]   edit → draft, returns table fragment
POST /zones/{zone}/raw                           save raw text → draft
GET  /zones/{zone}/diff                          draft vs live
POST /zones/{zone}/validate                      named-checkzone the draft
POST /zones/{zone}/deploy                        run the two-phase pipeline
GET  /zones/{zone}/deploy/{id}                   progress (htmx polling; SSE later)
GET  /zones/{zone}/history    POST /zones/{zone}/rollback
GET  /servers      target reachability           GET /audit
GET  /healthz      (no auth)
GET  /static/...   (no auth; embedded assets)
```

## Security

- Token constant-time compared, read from env, never logged; serve behind TLS.
- SSH **host-key pinning** via `known_hosts` (never `InsecureIgnoreHostKey`).
- Dedicated low-priv `mrdns-deploy` user on targets + a tightly scoped sudoers
  rule for `rndc` only (`deploy/sudoers.example`).
- Record values constructed via `miekg/dns` RR types, not string concatenation,
  to block zone-file injection (e.g. a TXT value smuggling `$INCLUDE`/newlines).
- `named-checkzone` gate before every reload, local and remote.

## Layout

```
cmd/mrdns/main.go
internal/config/   YAML load + validate, /opt/mrdns paths, env secrets
internal/web/      handlers, auth + CSRF middleware, html/template + htmx (embed.FS)
internal/zone/     parse / render / serial / checkzone            (P1)
internal/store/    live / draft / backups, locking                (P1)
internal/deploy/   ssh, two-phase pipeline, reload, verify        (P2)
internal/audit/    append-only JSONL                              (P3)
configs/mrdns.example.yaml
deploy/            mrdns.service, mrdns.env.example, sudoers.example, install.sh
```

## Phases

- **P0 — scaffold (done):** module, config loader for `/opt/mrdns`, login +
  session/CSRF, dashboard, `/healthz`. Builds and runs.
- **P1 — zone core:** parse/render/serial/checkzone + flat-file store
  (live/draft/backups/locking). Unit tests (round-trip, serial policies).
- **P2 — deploy engine:** SSH/SFTP, two-phase stage→commit→reload→verify.
  Integration test against a BIND+sshd container.
- **P3 — UI flow:** record-table editor → diff → deploy progress →
  history/rollback → servers/audit.
- **P4 — hardening/ops:** metrics, rate-limit, install.sh polish, docker-compose
  e2e.

## Scope notes

- Targets are zones already declared in `named.conf`; managing `named.conf`
  entries / brand-new zones (`rndc reconfig`) is a later phase.
- One zone per file. `$INCLUDE`/`$GENERATE` and DNSSEC-signed zones are
  later-phase caveats (inline-signing interacts with reloads/serials).
- Reverse zones (`in-addr.arpa`/`ip6.arpa`) are just zones — supported.
