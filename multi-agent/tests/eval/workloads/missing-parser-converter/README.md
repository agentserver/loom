# missing-parser-converter

E1 workload: no context starts with a tool that knows the input format. A
builder context must synthesize an MCP, the slave runs it, and downstream
tasks in the same trial reuse the descriptor.

## Outputs

| Path                                  | Meaning                                              |
|---------------------------------------|------------------------------------------------------|
| `${workspace}/synthesized.mcp.json`   | MCP descriptor exposing at least a `convert` tool    |
| `${workspace}/converted.out`          | Result of running `convert` on `fixtures/sample.in`  |
| `${workspace}/acceptance.log`         | One `PASS`/`FAIL` line per golden case               |

The single golden case lives at `fixtures/golden/expected.out`; the oracle
does a byte-for-byte `cmp` against `${workspace}/converted.out` and refuses
to pass on any divergence.

The `acceptance.log` contract is strict: every non-empty line must begin
with `PASS` or `FAIL`.  Any other line (including framework summary lines
like jest's `Tests: 1 failed, 4 passed, 5 total`, mocha's `  N failing`,
or a python `Traceback (most recent call last):`) is rejected as drift —
the framework's own conclusion does not get to override the canonical
per-case verdicts.  Workloads that emit framework output should funnel it
to a separate file (e.g. `framework.log`) rather than mixing it into the
canonical line-per-case log.  Multi-case support is intentionally out of
scope for this bootstrap; the natural extension is paired
`expected_<case>.out` / `converted_<case>.out` files iterated explicitly.

## Self-check

```bash
./oracle.sh ./fixtures/mock_workspace
# → {"passed":true,...}
```

To hand-build a passing mock workspace:

```bash
mkdir /tmp/mpc && \
  cp fixtures/mock_workspace/synthesized.mcp.json /tmp/mpc/ && \
  cp fixtures/golden/expected.out /tmp/mpc/converted.out && \
  echo "PASS expected.out" > /tmp/mpc/acceptance.log && \
  ./oracle.sh /tmp/mpc
```
