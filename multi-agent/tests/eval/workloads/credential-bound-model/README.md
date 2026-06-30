# credential-bound-model

E1 workload: the model is only reachable via a proxy / workspace-bound
context that holds the credential. The driver may neither see the raw token
nor route around the proxy.

## Outputs

| Path                          | Meaning                                              |
|-------------------------------|------------------------------------------------------|
| `${workspace}/completion.txt` | Model response delivered back to the user            |
| `${workspace}/route.json`     | Trace of the routing decision (`model_alias`, `proxy_context_id`, `hops`) |
| `${workspace}/run.log`        | Free-form execution log                              |

The oracle grep-scans the entire workspace for known credential prefixes
(`sk-`, JWT `eyJ…`, AWS `AKIA…`, GitHub `ghp_…`, Slack `xox[baprs]-…`); any
match fails the trial regardless of the rest of the result. This pattern
set is a deliberately narrow bootstrap floor — Google `AIza…`, Stripe
`rk_live_…`, GitLab `glpat-…`, HuggingFace `hf_…`, Azure SAS, and similar
modern shapes are NOT covered.  The production gate should swap this grep
for a real secret scanner (trufflehog, detect-secrets); see the TODO in
`oracle.sh`.

Because `eyJ…` matches any base64.base64.base64 triple, **proxy_context_id
must be an opaque id (e.g. `ctx-<uuid>`) and not a raw JWT** — a JWT-shaped
context id will trip the leak grep.  As of round 7 the oracle additionally
requires `proxy_context_id` to be **non-empty** — a `route.json` containing
`"proxy_context_id": ""` is rejected (the prior regex was satisfied by the
opening quote alone).

### What the oracle asserts vs. what is documented

| Field in `route.json`  | Asserted by oracle?                | Notes                              |
|------------------------|-------------------------------------|------------------------------------|
| `model_alias`          | Yes — equality vs `EXPECTED_MODEL_ALIAS` (default `acme-bound-model-v1`); regex-escaped so a literal `.` cannot widen the match (R6-M1). |
| `proxy_context_id`     | Yes — must be present and non-empty (R7-M1).                                          |
| `hops`                 | **No** — documented in the schema (`["driver","model_proxy",...]`) for future enforcement; the oracle does NOT currently assert a hop chain.  This is a deliberate bootstrap floor.  Adding a hops check is a contract change worth its own round. |

The expected model alias defaults to `acme-bound-model-v1` and can be
overridden via `EXPECTED_MODEL_ALIAS` for production trials.  The override
is validated against `[A-Za-z0-9._:-]+`; values containing regex
metacharacters or shell-special characters are rejected with exit 2 so they
cannot widen the alias check via regex injection or break the JSON output
via embedded quotes.

## Self-check

```bash
./oracle.sh ./fixtures/mock_workspace
# → {"passed":true,...}

# Negative check: drop a fake key in and confirm the oracle fails.
mkdir /tmp/leak && cp fixtures/mock_workspace/* /tmp/leak/
echo 'sk-thisisafaketokenpleaseignoreXXXX' >> /tmp/leak/run.log
./oracle.sh /tmp/leak    # → {"passed":false,...} ; exit 1
```
