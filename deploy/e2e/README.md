# Manual end-to-end test target

A throwaway BIND server reachable over SSH, to exercise the full mrdns deploy
path (SFTP stage → `named-checkzone` → `rndc reload` → SOA verify) against a
real `named`.

> **Not run in CI**, and not for production. It is a best-effort scaffold — the
> `rndc` control-channel setup in particular can vary by BIND version, so you may
> need to tweak `named.conf.local`/`entrypoint.sh` for your environment.

## With Docker

```sh
cd deploy/e2e

# 1. Generate an SSH key for mrdns and expose its public half to the container.
ssh-keygen -t ed25519 -N '' -f ./mrdns_key
cp mrdns_key.pub authorized_keys

# 2. Build and start the target (SSH on :2222, DNS on :5353).
docker compose up --build -d

# 3. Pin the target's host key.
ssh-keyscan -p 2222 127.0.0.1 > known_hosts

# 4. Smoke the moving parts directly.
ssh -i mrdns_key -p 2222 -o UserKnownHostsFile=known_hosts mrdns-deploy@127.0.0.1 \
    'sudo /usr/sbin/rndc status'
dig @127.0.0.1 -p 5353 SOA example.com +short
```

Then point an mrdns config at it and deploy from the UI:

```yaml
servers:
  e2e:
    host: 127.0.0.1
    port: 2222
    user: mrdns-deploy
    ssh_key: deploy/e2e/mrdns_key
    known_hosts: deploy/e2e/known_hosts
    remote_zone_dir: /etc/bind/zones
    checkzone_cmd: /usr/sbin/named-checkzone
    reload_cmd: "sudo /usr/sbin/rndc reload {zone}"
```

In the UI, create the `example.com` zone targeting `e2e`, add a record, deploy,
and confirm the serial advanced:

```sh
dig @127.0.0.1 -p 5353 SOA example.com +short
```

Tear down with `docker compose down`.

## Without Docker (any BIND host)

The same steps as the production target, applied to a scratch host:

1. Create the deploy user: `useradd -m mrdns-deploy`.
2. Add the mrdns **public** key to `~mrdns-deploy/.ssh/authorized_keys`.
3. Make the zone dir writable by the deploy user (e.g. `chown :bind /etc/bind/zones && chmod 2775 /etc/bind/zones`).
4. Install the scoped sudoers rule from `../sudoers.example`.
5. Declare the zone in `named.conf` pointing at a file under the zone dir.
6. On the mrdns host, `ssh-keyscan` the target into the configured `known_hosts`.
