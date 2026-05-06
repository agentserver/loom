# master_agent

Pure-orchestration agent for agentserver. See
`docs/superpowers/specs/2026-04-28-master-agent-design.md`.

Built from the same Go module as salve_agent.

## Build

    cd salve_agent
    go build -o ../master_agent/master-agent ./cmd/master-agent

## Configure

    cp master_agent/config.example.yaml master_agent/config.yaml
    # edit server.url

## Run

    cd master_agent && ./master-agent config.yaml

## End-to-end

    AGENTSERVER_URL=https://agent.example.com ./master_agent/scripts/e2e.sh
