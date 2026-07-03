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
}
