# Production Test Binaries

This directory is for locally rebuilt agent binaries used by production-style
manual tests. Files under `bin/` are generated artifacts and are ignored.

## Layout

```text
tests/prod_test/
  README.md
  bin/
    driver-agent
    slave-agent
    driver-agent.windows-amd64.exe
    slave-agent.windows-amd64.exe
```

## Rebuild

Run these commands from the `multi-agent` module root:

```bash
mkdir -p tests/prod_test/bin

CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
  -o tests/prod_test/bin/driver-agent ./cmd/driver-agent
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
  -o tests/prod_test/bin/slave-agent ./cmd/slave-agent

CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags='-s -w' \
  -o tests/prod_test/bin/driver-agent.windows-amd64.exe ./cmd/driver-agent
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags='-s -w' \
  -o tests/prod_test/bin/slave-agent.windows-amd64.exe ./cmd/slave-agent
```
