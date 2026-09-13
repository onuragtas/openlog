# Demo "servers" for the local stack demo (docs/operations/local-stack-demo.md): plain Debian 12
# without the agent. The agent is installed later with scripts/install.sh from the local release
# server, exactly like on a real machine.
#
#   SERVICES=0: ca-certificates, curl, openssl, procps
#   SERVICES=1: plus nginx, redis-server and postgresql (started by the entrypoint)
FROM debian:12
ARG SERVICES=0
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates curl openssl procps \
    && if [ "$SERVICES" = 1 ]; then \
         DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends nginx redis-server postgresql \
         # Without try_files a missing file logs "[error] open() ... failed" to error.log (log filter demo).
         && sed -i '/try_files/d' /etc/nginx/sites-available/default; \
       fi \
    && rm -rf /var/lib/apt/lists/*
COPY entrypoint.sh /usr/local/bin/stackdemo-host
RUN chmod 0755 /usr/local/bin/stackdemo-host
ENTRYPOINT ["/usr/local/bin/stackdemo-host"]
