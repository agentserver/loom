# WT-1 fault injection — spec

Status: draft (Stage 1 of WT-1-fault-injection workflow)
Branch: `paper/v3/p1-fault-injection`
Base: `origin/paper/v3-integration` @ `1332327`
Owner: WT-1-fault-injection worktree

## 1. Purpose

Phase 1 row D5 in `/root/paper_writing/docs/final/todo_list.md` calls for a
controlled fault-injection harness that lets the eval rig deliberately steer
driver / executor code through specific failure paths so the WT-0 failure
taxonomy buckets can be exercised end-to-end. The harness ships as a
**control-plane HTTP server + injection registry** under
`multi-agent/tools/eval/faultinject/`, plus a thin, default-noop
`fault_hook.go` file in each of `internal/driver` and `internal/executor`
that lets production code probe the registry without importing it.

This spec covers only the harness. The set of hook *call sites* added to
existing driver/executor code paths is intentionally minimal (one or two
`InjectIfActive(...)` calls per hook point); we do not rewrite control flow
in those packages. A different worktree (D4) owns the actual failure-path
logic.

## 2. Non-goals

- Not a fuzzer. Faults are explicit, named, and triggered by an external
  request, never random.
- Not a replacement for unit tests of failure handling — those still own
  their own scenarios. This is for the E2E integration matrix only.
- Not part of any production binary. See §7 (g) for the build-tag boundary.
- Not a load tester. The control plane is rate-limited per (run_id, kind).

## 3. Fault kinds (8)

Declared in `tools/eval/faultinject/kinds.go`:

```go
type FaultKind string

const (
    FaultMissingFile       FaultKind = "missing_file"
    FaultStaleCapability   FaultKind = "stale_capability"
    FaultWrongOSVersion    FaultKind = "wrong_os_version"
    FaultForbiddenCred     FaultKind = "forbidden_cred"
    FaultSlaveDisconnect   FaultKind = "slave_disconnect"
    FaultDriverRestart     FaultKind = "driver_restart"
    FaultModelRouteFailure FaultKind = "model_route_failure"
    FaultDuplicatePickup   FaultKind = "duplicate_pickup"
)

// AllFaultKinds enumerates every kind in declaration order. The /list
// endpoint and tests iterate this slice; adding a new kind requires
// appending here in lockstep.
var AllFaultKinds = []FaultKind{
    FaultMissingFile,
    FaultStaleCapability,
    FaultWrongOSVersion,
    FaultForbiddenCred,
    FaultSlaveDisconnect,
    FaultDriverRestart,
    FaultModelRouteFailure,
    FaultDuplicatePickup,
}
```

Mapping to WT-0 `observerstore.FailureCategory` (informational; categories
live in the observer, not in this package):

| FaultKind                | Expected `FailureCategory`              |
| ------------------------ | --------------------------------------- |
| `missing_file`           | `FailMissingFile`                       |
| `stale_capability`       | `FailStaleCapability`                   |
| `wrong_os_version`       | `FailWrongVersion`                      |
| `forbidden_cred`         | `FailForbiddenCred`                     |
| `slave_disconnect`       | `FailSlaveDisconnect`                   |
| `driver_restart`         | `FailDriverRestart`                     |
| `model_route_failure`    | `FailContractViolation` (503 from gateway) |
| `duplicate_pickup`       | `FailDuplicateWrite`                    |

### 3.1 Per-kind semantics

| Kind                  | Hook point                          | Injected behaviour                                                  | Safety constraint                                            |
| --------------------- | ----------------------------------- | ------------------------------------------------------------------- | ------------------------------------------------------------ |
| `missing_file`        | executor file-read entry            | return `*os.PathError{Op:"open", Path: target, Err: fs.ErrNotExist}` | never deletes a real file on disk                            |
| `stale_capability`    | driver capability snapshot read     | return a deep copy with the snapshot hash flipped to the prior hash | only the in-memory view is mutated; persistent store untouched |
| `wrong_os_version`    | driver capability OS field read     | swap `os` field e.g. `linux→darwin`                                  | in-memory mutation only                                      |
| `forbidden_cred`      | credential resolve in driver/slave  | return literal `FAKE_CRED_FOR_INJECTION_DO_NOT_USE` as the cred     | never inject a string shaped like a real cred (see §7 c)     |
| `slave_disconnect`    | slave heartbeat handler             | close the slave TCP conn associated with the injected `run_id`       | only the conn for that test run is touched                   |
| `driver_restart`      | driver main loop                    | `panic(errFaultDriverRestart)`; runner recovers and restarts        | runner is test-only; never reaches production main           |
| `model_route_failure` | model gateway client                | return synthetic HTTP 503 to the run's gateway calls only           | only requests originating from the injected `run_id`         |
| `duplicate_pickup`    | driver task pickup                  | call the dedup branch using the *same* idempotency key as the pickup | never replays the raw user command (see §7 d)                |

## 4. Control plane

HTTP server lives in `tools/eval/faultinject/server.go`, package
`faultinject`, file-level build tag `//go:build evaltool`.

### 4.1 Listen address

- Default `127.0.0.1:18189`.
- The bind address is configurable via constructor argument, but the
  server **rejects** any address that does not resolve exclusively to
  loopback (`127.0.0.0/8` or `::1/128`). The check resolves the host
  portion and walks every result; if *any* result is non-loopback, the
  constructor returns `ErrControlPlaneMustBeLoopback` and the server
  never starts. A literal `0.0.0.0` / `::` is rejected outright (those
  bind on every interface and are non-loopback by definition).
- `http.Server` is constructed with:
  - `ReadHeaderTimeout: 5 * time.Second`
  - `ReadTimeout:       30 * time.Second`
  - `WriteTimeout:      30 * time.Second`
  - `IdleTimeout:       60 * time.Second`

### 4.2 Endpoints

#### POST `/inject`

Request body (JSON):

```json
{
  "run_id": "string, required, ^[A-Za-z0-9_-]{8,128}$",
  "kind":   "string, required, one of AllFaultKinds",
  "target": "string, optional, ≤512 bytes",
  "params": { "...": "string→string, optional, ≤16 entries" }
}
```

Responses:

- `200 {"ok": true, "active": <n>}` — fault registered for that run.
- `400 {"error": "..."}` — validation failure. Specific error codes:
  - `ErrInjectionRunIDInvalid`     bad / missing `run_id`
  - `ErrInjectionKindUnknown`      kind not in AllFaultKinds
  - `ErrInjectionRateLimited`      run/kind exceeds `MaxInjectionsPerRun`
  - `ErrInjectionTargetTooLong`    target > 512 bytes
  - `ErrInjectionParamsTooLarge`   params > 16 entries or any value > 1024 bytes

#### POST `/clear`

Request body `{"run_id": "..."}`. Drops every active fault for that
`run_id`. Responds `200 {"ok": true, "cleared": <n>}`. Bad / missing
`run_id` → `400`.

#### GET `/list?run_id=...`

Returns a JSON array of the run's active fault directives in injection
order:

```json
[{"kind":"missing_file","target":"foo.txt","params":{},"seq":1}]
```

Bad / missing `run_id` → `400`. An unknown but well-formed `run_id` → `200 []`.

### 4.3 Quotas

```go
const MaxInjectionsPerRun = 100 // per (run_id, kind)
```

Counted on `/inject` calls; cleared by `/clear`. Exceeding it returns
`400 ErrInjectionRateLimited` and the fault is *not* registered.

## 5. Shared state

```go
// In state.go, package faultinject (//go:build evaltool).
//
// activeFaults maps run_id → ordered slice of directives. Reads are
// taken via a snapshot under mutex; writers append under the same
// mutex. We do not expose a sync.Map because per-run rate limiting
// needs an atomic read-modify-write over the slice length.
type faultStore struct {
    mu        sync.Mutex
    perRun    map[string][]FaultDirective
    perRunKind map[string]map[FaultKind]int // injection count
}

type FaultDirective struct {
    Kind   FaultKind
    Target string
    Params map[string]string
    Seq    int       // monotonic per run_id
    At     time.Time // server-side timestamp (UTC), informational
}
```

The store exposes:

- `Add(runID string, d FaultDirective) error` — used by `/inject`.
- `Clear(runID string) int` — used by `/clear`.
- `List(runID string) []FaultDirective` — used by `/list` and by the
  hook lookup.
- `Lookup(runID string, kind FaultKind) (FaultDirective, bool)` —
  returns the *first* matching directive in injection order; consumers
  can choose to mark-consumed (out of scope for this spec — current
  consumers re-fire on every hook hit, which is the desired semantics
  for "stay broken until cleared").

The store is owned by the server instance; tests construct a fresh
store per test. A package-level default store is provided so that
`fault_hook` shims (see §6) can find it via a `Hook(store)` setter
called from `cmd/eval-runner` (or equivalent test main).

## 6. `fault_hook.go` interface

Two new files, both with no build tag (they compile into production
builds, but their default behaviour is a zero-cost noop):

- `multi-agent/internal/driver/fault_hook.go`
- `multi-agent/internal/executor/fault_hook.go`

Each file defines:

```go
package driver // or executor

// HookPoint enumerates the call sites in this package that probe the
// fault injector. Adding a hook point requires (a) appending here and
// (b) adding the corresponding call in the package's call site code.
type HookPoint string

const (
    HookPointDriverPickup            HookPoint = "driver.pickup"
    HookPointDriverCapabilityRead    HookPoint = "driver.capability_read"
    HookPointDriverCredResolve       HookPoint = "driver.cred_resolve"
    HookPointDriverModelRoute        HookPoint = "driver.model_route"
    HookPointDriverMainLoop          HookPoint = "driver.main_loop"
    HookPointSlaveHeartbeat          HookPoint = "slave.heartbeat"
    HookPointExecutorFileOpen        HookPoint = "executor.file_open"
    HookPointExecutorCredResolve     HookPoint = "executor.cred_resolve"
)

// Hook is the global noop hook. faultinject (eval build only) replaces
// it via SetHook(...) at runner startup. Production binaries never
// import faultinject, so Hook stays at noop and the cost of every
// InjectIfActive call collapses to a single atomic.LoadPointer + nil
// check.
type Hook func(ctx context.Context, runID string, hp HookPoint, meta map[string]string) error

var hook atomic.Pointer[Hook]

// SetHook installs a hook implementation. nil restores the noop.
// Returns the previously installed hook (or nil) so tests can restore.
func SetHook(h Hook) Hook { ... }

// InjectIfActive is the fast-path probe called from every hook site.
// When no hook is installed (production), this is a single atomic load
// + nil check (target < 100ns, see plan §benchmark). When a hook is
// installed and matches, it returns the hook's error which the caller
// must propagate.
func InjectIfActive(ctx context.Context, runID string, hp HookPoint, meta map[string]string) error { ... }
```

The two files are independent (one per package) but identical in
shape. They never import `faultinject`. The dependency arrow goes:

```
cmd/eval-runner (//go:build evaltool)
    └── tools/eval/faultinject
            ├── driver.SetHook(...)
            └── executor.SetHook(...)
```

Production binaries (`cmd/driver-agent`, `cmd/slave-agent`, etc.) never
build with `evaltool`, so the linker drops the entire `faultinject`
package and `SetHook` is never called.

### 6.1 Fast path

`InjectIfActive` MUST be a single atomic pointer load and a nil check
when no hook is installed. Concretely:

```go
func InjectIfActive(...) error {
    h := hook.Load()
    if h == nil {
        return nil
    }
    return (*h)(ctx, runID, hp, meta)
}
```

Benchmark target: < 100ns/op on the dev workstation (see plan §6).

## 7. Security mitigations (mandatory)

These mitigations are non-negotiable. Stage 3 review fails P0 if any is
absent.

### (a) Loopback bind enforced

The control plane MUST bind to `127.0.0.0/8` or `::1/128` only. The
constructor resolves the host portion of the configured address and
returns `ErrControlPlaneMustBeLoopback` if *any* resolved address is
non-loopback. Literal `0.0.0.0` and `::` are rejected outright. The
binary refuses to start otherwise. Rationale: this is a fault injector;
exposing it on a routable interface lets any network-adjacent attacker
construct arbitrary faults against a real slave (e.g. `slave_disconnect`
DoS, `forbidden_cred` to trip downstream alert paths).

### (b) Per-(run_id, kind) rate limit

`MaxInjectionsPerRun = 100`. Each `/inject` increments a counter keyed by
`(run_id, kind)`; the 101st returns `400 ErrInjectionRateLimited`.
`/clear` resets both directive list and counter for the run.

### (c) `FaultForbiddenCred` sentinel-only

The injected cred string is the hardcoded literal
`FAKE_CRED_FOR_INJECTION_DO_NOT_USE`. It is:

- short enough that downstream secret scanners trivially recognise it
  as fake (no high-entropy prefix);
- distinctive enough that an ops engineer who sees it in a log
  immediately knows it came from the injector, not a real leak;
- defined as a single package constant so there is one site to grep.

Tests assert byte-for-byte equality with this literal.

### (d) `FaultDuplicatePickup` uses idempotency key, never replays command

The duplicate-pickup fault does NOT re-execute the agent's original
command. It calls into the existing dispatch dedup branch using the
*same* idempotency key as the original pickup, exercising the
"already-dispatched" code path. Tests assert the subprocess spawn count
remains 1 even after the fault fires.

Rationale: a true replay would run the agent's side effects twice —
outbound API calls, file writes, billable LLM tokens. In a test
environment this would be slow and expensive; in a misconfigured prod
environment (see (a)) this would be a denial-of-service vector.

### (e) Audit log: every injection logged

Every `InjectIfActive` hit emits one JSON line to stderr before
returning the hook's error:

```json
{"ts":"2026-06-30T20:11:03Z","run_id":"abc12345","kind":"missing_file","hook":"executor.file_open","action":"injected"}
```

The audit writer is `os.Stderr` and is unbuffered. Silent injection is a
P0 review failure. Tests assert one stderr line per injection.

### (f) `run_id` validation

`run_id` is the primary map key for `activeFaults`. It MUST match
`^[A-Za-z0-9_-]{8,128}$`. Missing, empty, too-short, too-long, or
out-of-character-class strings return `400 ErrInjectionRunIDInvalid` and
the fault is not registered. Same validation applies to `/clear` and
`/list`. Rationale: prevents an attacker (who somehow reached the
control plane despite (a)) from spamming arbitrary keys and exhausting
memory, and prevents log-injection via funky `run_id` values reaching
the audit writer.

### (g) Build-tag isolation from production

- All files in `tools/eval/faultinject/` have first line
  `//go:build evaltool` (a `package faultinject` line follows on the
  next line, no blank-line-separated `// +build` comment needed; Go 1.17+).
- The `fault_hook.go` files in `internal/driver` and `internal/executor`
  have NO build tag. They are always compiled. Their default `hook`
  pointer is nil and stays nil unless an `evaltool`-tagged main
  (e.g. `cmd/eval-runner`) calls `SetHook(...)`.
- Production `go build ./cmd/driver-agent` (no `-tags=evaltool`)
  produces a binary with zero symbols matching `/faultinject/`. The
  Stage 3 acceptance script runs `nm` and asserts zero matches.

### (h) HTTP server timeouts

`http.Server.ReadHeaderTimeout = 5s`, `ReadTimeout = 30s`,
`WriteTimeout = 30s`, `IdleTimeout = 60s`. Prevents slowloris / partial
header DoS from a runaway local test client.

## 8. Audit log schema

One JSON object per line on `os.Stderr`:

| Field    | Type   | Note                                                       |
| -------- | ------ | ---------------------------------------------------------- |
| `ts`     | string | RFC 3339 UTC                                               |
| `run_id` | string | the validated run_id                                       |
| `kind`   | string | the FaultKind string literal                               |
| `hook`   | string | the HookPoint string literal                               |
| `action` | string | always `"injected"` in this version (reserved for future)  |
| `seq`    | int    | the directive's monotonic seq                              |

## 9. Acceptance

- All 8 fault kinds round-trip through `/inject` → hook hit → expected
  driver/executor failure path → observer-side `FailureCategory` bucket.
- Each kind has one integration test under
  `multi-agent/scripts/eval/faultinject_integration_test.sh` (one
  scenario per kind, run sequentially against a stub-backed driver +
  slave).
- All Security mitigations (a–h) are covered by named tests; see plan
  §test matrix.
- `nm` on a production-tagged build of `cmd/driver-agent` shows zero
  `faultinject` symbols.
- `BenchmarkFaultHook` reports < 100 ns/op on the dev workstation.

## 10. Out of scope / future work

- Persisting fault directives to disk (currently in-memory; harness
  death = lost state, which is desirable for tests).
- Authentication on the control plane (loopback bind is the sole
  authorization boundary; adding tokens just shifts the problem).
- Time-bounded faults ("auto-clear after 30s") — explicit `/clear` is
  the only retraction path today.
- Hook injection into agentsdk / agentbackend / observer code — D5 is
  driver+executor only; other surfaces are deferred to follow-up
  worktrees.
