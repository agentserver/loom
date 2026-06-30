# windows-only-artifact

E1 workload: an artifact (e.g. signed binary, `.pdb`, `.msi`) can only be
produced by a Windows-only toolchain. The driver lives on Linux/macOS; the
Loom router MUST be able to dispatch the build step to a Windows slave
context. **In production runs this workload needs a real Windows host.**

## Outputs

| Path                                | Meaning                                         |
|-------------------------------------|-------------------------------------------------|
| `${workspace}/artifact.bin`         | The build artifact                              |
| `${workspace}/artifact.meta.json`   | `{"sha256":"<hash>", ...}` describing artifact  |

The oracle re-hashes `artifact.bin` with `sha256sum` and compares it to the
value recorded inside `artifact.meta.json`; the production harness
additionally pins the expected hash per trial. If `artifact.bin` is present
but `sha256sum` cannot produce a hash, the oracle fails with
`details.sha256_tool="unavailable"` instead of reporting a skipped hash check
as a pass.

## Self-check (no Windows required for the oracle itself)

```bash
./oracle.sh ./fixtures/mock_workspace
# → {"passed":true,...}
```

To rebuild the mock fixture:

```bash
printf 'MZ\x90\x00\x03windows-mock-artifact\n' > fixtures/mock_workspace/artifact.bin
hash=$(sha256sum fixtures/mock_workspace/artifact.bin | awk '{print $1}')
printf '{"sha256":"%s","produced_on":"mock"}\n' "$hash" \
  > fixtures/mock_workspace/artifact.meta.json
```
