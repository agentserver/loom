FROM debian:bookworm-slim

# install.sh uses sudo (no-op as root, but the binary must exist), xxd for
# random keygen, curl for healthchecks, ca-certs for HTTPS to agent.cs.ac.cn.
RUN apt-get update && apt-get install -y --no-install-recommends \
        bash sudo ca-certificates curl python3 xxd \
    && rm -rf /var/lib/apt/lists/*

# Compose bind-mounts deploy/linux/ at /opt/loom/deploy at runtime.
# Each container's per-instance dir lives under /var/lib/loom/.
WORKDIR /var/lib/loom

ENV API_KEY="COMPOSE_TESTKEY_dont_use_in_prod"
