# Production Test Binaries

This directory is for locally rebuilt agent binaries used by production-style
manual tests. Files under `bin/` are generated artifacts and are ignored.

> **Sibling test suite:** `tests/k8s_commander/` is a parallel e2e that
> stands up a 3-replica observer-server in minikube (with a mock
> agentserver + shared Postgres) specifically to regress-test the
> commander multi-pod state-persistence contracts (PR #37). It does NOT
> need any binaries from this directory or real agentserver credentials.
> See `E2E_RUNBOOK.md` § "Commander multi-pod e2e (k8s)" for when to use
> which suite.

## Layout

```text
tests/prod_test/
  README.md
  bin/
    driver-agent.linux-amd64
    slave-agent.linux-amd64
    driver-agent.windows-amd64.exe
    slave-agent.windows-amd64.exe
```

## Rebuild

Run these commands from the `multi-agent` module root:

```bash
mkdir -p tests/prod_test/bin

CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
  -o tests/prod_test/bin/driver-agent.linux-amd64 ./cmd/driver-agent
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
  -o tests/prod_test/bin/slave-agent.linux-amd64 ./cmd/slave-agent

CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags='-s -w' \
  -o tests/prod_test/bin/driver-agent.windows-amd64.exe ./cmd/driver-agent
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags='-s -w' \
  -o tests/prod_test/bin/slave-agent.windows-amd64.exe ./cmd/slave-agent
```
