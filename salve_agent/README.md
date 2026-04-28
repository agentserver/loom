# salve_agent

Custom agent for agentserver. See `docs/superpowers/specs/2026-04-27-salve-agent-design.md`.

## Build

    go build -o salve-agent ./cmd/salve-agent

## Configure

    cp config.example.yaml config.yaml
    # edit server.url

## Run

    ./salve-agent
