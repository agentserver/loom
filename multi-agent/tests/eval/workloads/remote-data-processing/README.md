# remote-data-processing

E1 workload: dataset and pandas live on a headless Linux server; the user
sits at a laptop. The Loom agent must dispatch the aggregation job to the
slave context, return `result.json` and its SHA-256.

## Outputs

| Path                          | Meaning                                       |
|-------------------------------|-----------------------------------------------|
| `${workspace}/result.json`    | Aggregated stats `{count, sum, mean}`         |
| `${workspace}/result.sha256`  | SHA-256 of `result.json`, one line `<hash>  result.json` |

The golden checksum lives at `fixtures/golden/result.sha256` and is committed
beside the dataset; the oracle refuses to pass if the actual hash diverges.

## Self-check

```bash
./oracle.sh ./fixtures/mock_workspace
# → {"passed":true,...}
```

To regenerate the golden checksum after editing the canonical `result.json`:

```bash
sha256sum fixtures/mock_workspace/result.json | \
  awk '{print $1"  result.json"}' > fixtures/golden/result.sha256
cp fixtures/golden/result.sha256 fixtures/mock_workspace/result.sha256
```
