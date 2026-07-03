# eval/ — credential-bound-model evaluation harness

WT-2-credential-workload's evaluation layer. Sits alongside the
Phase 0 workload (`spec.yaml`, `oracle.sh`, `fixtures/`) without
modifying it — this directory only ADDS the dual-path measurement
scripts.

See `docs/specs/wt2-credential-workload.spec.md` and `.plan.md` for
the full contract.

## Files

| Path | Purpose |
|------|---------|
| `eval-modelproxy-overhead.sh` | Same-prompt double-run harness. Emits `ModelProxyOverhead = latency_a - latency_b`. |
| `hops_loopback_check.sh` | Given `route.json` + mode, asserts hop `proxy_addr` matches loopback expectation with EXACT-string equality (§7.f). |
| `testdata/config_a.toml` | Dummy path (a) codex config for `--dry-run` when the operator's real `prod_test/driver-codex-local/` isn't populated. |
| `testdata/config_b.toml` | Dummy path (b) codex config for `--dry-run`. |
| `testdata/hop_*.json` | Route-trace fixtures for `hops_loopback_check.sh --self-test`. |

## Quick smoke

```
# Loopback checker's own test suite (fixtures under testdata/):
bash hops_loopback_check.sh --self-test

# Harness invariants (sleep 30 literal + sentinel key rejector):
bash eval-modelproxy-overhead.sh --self-test

# Harness dry-run — validates config presence + env, prints planned
# runner command lines, does NOT invoke the runner:
CONFIG_A_PATH=$PWD/testdata/config_a.toml \
CONFIG_B_PATH=$PWD/testdata/config_b.toml \
RUNNER_BIN=/path/to/eval-runner \
OPENAI_API_KEY=sk-realoperatortoken \
bash eval-modelproxy-overhead.sh --mode both --samples 3 --dry-run
```

## Real measurement (operator machine only)

Requires:

- built `eval-runner` (from `multi-agent/tools/eval/runner/`);
- populated `prod_test/driver-codex-local/{.codex/config.toml,codex-config.toml}`
  with real bearer / env_key blocks (the drop-in fixtures under
  `testdata/` are only for CI dry-run);
- real `OPENAI_API_KEY` env var (mode=b guard);
- reachable local proxy (`http://127.0.0.1:53452/v1`) for mode=a;
- reachable upstream endpoint for mode=b.

```
RUNNER_BIN=$(go env GOPATH)/bin/eval-runner \
OPENAI_API_KEY=sk-realoperatortoken \
bash eval-modelproxy-overhead.sh --mode both --samples 5
```

Prints one final line:

```
ModelProxyOverhead: latency_a=<int_ms> latency_b=<int_ms> delta=<int_ms> (n_a=5 n_b=5)
```

## CI

CI runs `--dry-run` and `--self-test` only — see spec §7(h). Asserting
a specific delta ceiling in CI would be measuring the internet on a
bad-network minute, not the paper's claim.
