# Manual end-to-end test target for mrdns: a BIND server reachable over SSH.
# NOT for production and NOT exercised by CI — verify in your own environment.
FROM debian:12-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends bind9 bind9-utils dnsutils openssh-server sudo \
 && rm -rf /var/lib/apt/lists/*

# Deploy user with a scoped sudo rule for rndc only.
RUN useradd -m -s /bin/bash mrdns-deploy \
 && install -d -m 700 -o mrdns-deploy -g mrdns-deploy /home/mrdns-deploy/.ssh \
 && printf 'mrdns-deploy ALL=(root) NOPASSWD: /usr/sbin/rndc reload, /usr/sbin/rndc reload *, /usr/sbin/rndc reconfig\n' \
      > /etc/sudoers.d/mrdns-deploy \
 && chmod 440 /etc/sudoers.d/mrdns-deploy

# Zone directory writable by the deploy user; named runs as user 'bind'.
RUN install -d -m 2775 -o mrdns-deploy -g bind /etc/bind/zones

COPY named.conf.local /etc/bind/named.conf.local
COPY example.com.zone /etc/bind/zones/example.com.zone
RUN chown mrdns-deploy:bind /etc/bind/zones/example.com.zone \
 && chmod 644 /etc/bind/zones/example.com.zone

RUN install -d -m 755 /run/sshd
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

EXPOSE 22 53/tcp 53/udp
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
