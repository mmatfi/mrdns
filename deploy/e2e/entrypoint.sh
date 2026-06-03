#!/bin/sh
set -eu

# Install the mrdns public key (bind-mounted at /authorized_keys) for SSH login.
if [ -f /authorized_keys ]; then
  install -m 600 -o mrdns-deploy -g mrdns-deploy /authorized_keys \
    /home/mrdns-deploy/.ssh/authorized_keys
else
  echo "warning: /authorized_keys not mounted; SSH login will fail" >&2
fi

# Ensure an rndc control key exists (named.conf.local wires up the controls block).
if [ ! -f /etc/bind/rndc.key ]; then
  rndc-confgen -a -c /etc/bind/rndc.key
  chown root:bind /etc/bind/rndc.key
  chmod 640 /etc/bind/rndc.key
fi

# Validate config, start named (daemonized), then sshd in the foreground.
named-checkconf
named -u bind
exec /usr/sbin/sshd -D -e
