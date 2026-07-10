// Package main is the manual_ssh baseline binary. See
// docs/specs/wt2-baselines.spec.md §7(c) — this baseline uses local
// shell scripts to *simulate* what a human doing the task manually
// through SSH would produce. It never invokes ssh/scp/rsync/sftp; §7(c)
// tests enforce that.
package main

// workloadScripts maps a workload ID to a bash snippet that, when run
// with `cwd = ${workspace}`, writes exactly the output files the
// workload's oracle looks for. Each snippet is the shortest artefact
// the maintainer would hand-produce to make the oracle happy — the
// same content the workload README documents for "build your own mock
// workspace from scratch".
//
// Discovery: to add a new workload here, read <workload>/oracle.sh, list
// the files it inspects, and produce them with only POSIX shell (printf,
// echo, sha256sum). Never call the workload's own oracle inside the
// script — the harness calls it after ExecuteAgent.
var workloadScripts = map[string]string{
	"cross-device-code-mod": `set -eu
printf 'diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n' > patch.diff
echo PASS > test.log
`,
	"remote-data-processing": `set -eu
# Oracle checks: result.json has count/mean/sum + sha256 in result.sha256
# matches result.json + sha256(result.json) matches fixtures/golden/result.sha256.
# The mock_workspace ships the exact golden-matching bytes; reproduce
# them verbatim so the golden axis passes. If the golden file itself
# ever changes, refresh both mock_workspace/result.json and this here-doc.
cat > result.json <<'JSON'
{"count": 5, "sum": 15, "mean": 3.0}
JSON
sha256sum result.json | awk '{print $1"  result.json"}' > result.sha256
`,
	"windows-only-artifact": `set -eu
# Oracle: non-empty artifact.bin whose sha256 matches meta JSON.
printf 'windows-artifact-content\n' > artifact.bin
sum=$(sha256sum artifact.bin | awk '{print $1}')
size=$(wc -c < artifact.bin | tr -d ' ')
cat > artifact.meta.json <<JSON
{"sha256":"$sum","size":$size}
JSON
`,
	"missing-parser-converter": `set -eu
# Oracle: synthesized.mcp.json holds a "convert" tool; converted.out
# equals fixtures/golden/expected.out byte-for-byte; acceptance.log has
# exactly cases_total=1 PASS line (the oracle hardcodes cases_total=1).
# We copy the golden from the fixtures the harness projected into the
# workspace.
cat > synthesized.mcp.json <<'JSON'
{"tools":[{"name":"convert","description":"stub"}]}
JSON
if [ -f golden/expected.out ]; then
  cp golden/expected.out converted.out
else
  # Golden absent — harness bug. Best-effort empty file so the oracle
  # can still report the mismatch explicitly.
  : > converted.out
fi
# The oracle hardcodes cases_total=1 and requires strict equality: one
# PASS line, no non-canonical lines. The mock_workspace ships
# "PASS expected.out" (the ^PASS\b anchor matches).
echo 'PASS expected.out' > acceptance.log
`,
	"credential-bound-model": `set -eu
# Oracle: route.json.model_alias == $EXPECTED_MODEL_ALIAS (default
# acme-bound-model-v1); non-empty proxy_context_id (not JWT-shaped);
# workspace grep -r has no secret substrings. Env is whitelisted so
# EXPECTED_MODEL_ALIAS is forwarded.
alias="${EXPECTED_MODEL_ALIAS:-acme-bound-model-v1}"
cat > route.json <<JSON
{"model_alias":"$alias","proxy_context_id":"pctx-manual-ssh-baseline-0001"}
JSON
echo "manual_ssh baseline completion" > completion.txt
echo "manual_ssh run log" > run.log
`,
	"public-terminal-heterogeneous-dates": `set -eu
python3 <<'PY'
import csv
from datetime import datetime
from pathlib import Path

root = Path(".")
high = {}
with (root / "task-deps" / "daily_temp_sf_high.csv").open(newline="", encoding="utf-8") as handle:
    for row in csv.DictReader(handle):
        high[row["date"]] = float(row["temperature"])

low = {}
with (root / "task-deps" / "daily_temp_sf_low.csv").open(newline="", encoding="utf-8") as handle:
    for row in csv.DictReader(handle):
        raw = row["date"].split()[0]
        parsed = None
        for fmt in ("%m/%d/%Y", "%m-%d-%Y"):
            try:
                parsed = datetime.strptime(raw, fmt).strftime("%Y-%m-%d")
                break
            except ValueError:
                pass
        if parsed is None:
            raise SystemExit(f"unsupported date format: {row['date']}")
        low[parsed] = float(row["temperature"])

dates = sorted(set(high) & set(low))
if not dates:
    raise SystemExit("no overlapping dates")
avg = sum(high[day] - low[day] for day in dates) / len(dates)
(root / "avg_temp.txt").write_text(f"{avg:.15f}\n", encoding="utf-8")
PY
`,
}
