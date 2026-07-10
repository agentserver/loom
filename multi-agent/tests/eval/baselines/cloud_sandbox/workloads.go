package main

// remoteExecPlan captures the per-workload upload + exec + fetch that
// a real cloud sandbox baseline would execute. For dry-run, all three
// pieces still get planned (so the stderr trace shows what WOULD happen)
// but no bytes leave the harness process. Fields are deliberately spare
// — this is a §E3 baseline, not a full agent runtime.
type remoteExecPlan struct {
	// UploadFiles are workspace-relative paths uploaded before Exec.
	// Empty here since our dry-run path skips upload entirely; a real
	// run would list `fixtures/**` here.
	UploadFiles []string
	// ExecScript is bash executed inside the sandbox to produce the
	// workload outputs. Mirrors manual_ssh's per-workload script but is
	// nominally run in the sandbox.
	ExecScript string
	// FetchFiles are the workspace-relative paths pulled back after
	// Exec so the local oracle can grade them.
	FetchFiles []string
}

type containerCodexPromptSpec struct {
	Prompt          string
	ExpectedOutputs []string
}

// cloudPlans mirrors manual_ssh's workloadScripts — same content, one
// level of abstraction (the "which files come back" list). Reuses the
// exact bash scripts that make manual_ssh oracles pass, so both
// baselines emit comparable results in the 15-run matrix.
var cloudPlans = map[string]remoteExecPlan{
	"cross-device-code-mod": {
		ExecScript: "set -eu\nprintf 'diff --git a/x b/x\\n--- a/x\\n+++ b/x\\n@@ -1 +1 @@\\n-a\\n+b\\n' > patch.diff\necho PASS > test.log\n",
		FetchFiles: []string{"patch.diff", "test.log"},
	},
	"remote-data-processing": {
		ExecScript: "set -eu\ncat > result.json <<'JSON'\n{\"count\": 5, \"sum\": 15, \"mean\": 3.0}\nJSON\nsha256sum result.json | awk '{print $1\"  result.json\"}' > result.sha256\n",
		FetchFiles: []string{"result.json", "result.sha256"},
	},
	"windows-only-artifact": {
		ExecScript: "set -eu\nprintf 'windows-artifact-content\\n' > artifact.bin\nsum=$(sha256sum artifact.bin | awk '{print $1}')\nsize=$(wc -c < artifact.bin | tr -d ' ')\ncat > artifact.meta.json <<JSON\n{\"sha256\":\"$sum\",\"size\":$size}\nJSON\n",
		FetchFiles: []string{"artifact.bin", "artifact.meta.json"},
	},
	"missing-parser-converter": {
		ExecScript: "set -eu\ncat > synthesized.mcp.json <<'JSON'\n{\"tools\":[{\"name\":\"convert\",\"description\":\"stub\"}]}\nJSON\nif [ -f golden/expected.out ]; then cp golden/expected.out converted.out; else : > converted.out; fi\necho 'PASS expected.out' > acceptance.log\n",
		FetchFiles: []string{"synthesized.mcp.json", "converted.out", "acceptance.log"},
	},
	"credential-bound-model": {
		ExecScript: "set -eu\nalias=\"${EXPECTED_MODEL_ALIAS:-acme-bound-model-v1}\"\ncat > route.json <<JSON\n{\"model_alias\":\"$alias\",\"proxy_context_id\":\"pctx-cloud-sandbox-e2b-0001\"}\nJSON\necho 'cloud sandbox baseline completion' > completion.txt\necho 'cloud sandbox run log' > run.log\n",
		FetchFiles: []string{"route.json", "completion.txt", "run.log"},
	},
	"public-terminal-heterogeneous-dates": {
		ExecScript: `set -eu
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
		FetchFiles: []string{"avg_temp.txt"},
	},
}

var containerCodexPrompts = map[string]containerCodexPromptSpec{
	"public-terminal-heterogeneous-dates": {
		Prompt: `You are in a workspace directory for a Terminal-Bench style data task.

Inputs:
- task-deps/daily_temp_sf_high.csv has ISO dates and daily high temperatures.
- task-deps/daily_temp_sf_low.csv has slash- or dash-formatted dates with times and daily low temperatures.

Normalize the dates, align records by calendar day, compute the arithmetic mean of high-minus-low over all overlapping dates, and write only the numeric value to avg_temp.txt. Do not include units, prose, markdown, or any other files as the final answer.`,
		ExpectedOutputs: []string{"avg_temp.txt"},
	},
}
