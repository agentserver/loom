FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
COPY mock-agentserver /usr/local/bin/mock-agentserver
EXPOSE 18080
ENTRYPOINT ["/usr/local/bin/mock-agentserver"]
