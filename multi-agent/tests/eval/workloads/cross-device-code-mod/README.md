# cross-device-code-mod

E1 workload: repo on a laptop, tests on a Linux server, model via gateway.
A correct Loom agent dispatches the code change to the laptop driver, ships
the patch to the Linux slave, runs the test suite there, and returns the log.

## Outputs the workload must produce

| Path                       | Meaning                                  |
|----------------------------|------------------------------------------|
| `${workspace}/patch.diff`  | Unified diff applied to the repo         |
| `${workspace}/test.log`    | Raw output of the slave's test command   |

## Self-check the oracle

```bash
# from this directory
./oracle.sh ./fixtures/mock_workspace
# → {"passed":true,"details":{...},"metrics":{...}}
# exit 0
```

To build your own mock workspace from scratch:

```bash
mkdir /tmp/cdcm && \
  printf 'diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n' > /tmp/cdcm/patch.diff && \
  echo PASS > /tmp/cdcm/test.log && \
  ./oracle.sh /tmp/cdcm
```
