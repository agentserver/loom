# WT-1-fault-injection — Implementation Plan

> Plan for the Stage-3 TDD work that satisfies
> `docs/specs/wt1-fault-injection.spec.md`.
> Branch: `paper/v3/p1-fault-injection`.
> Base: `origin/paper/v3-integration @ 17f2c3c`
> (post-WT-1-ablation-registry merge).

This plan turns each spec deliverable into a file and each spec security
mitigation into a Go test, then sequences them strictly RED → GREEN →
REFACTOR.

---

## 1. File breakdown

All paths are relative to the Go module root `multi-agent/`.

| File | Purpose | Public symbols (in this file) | Imports | Build tag |
| --- | --- | --- | --- | --- |
| `tools/eval/faultinject/doc.go` | Package godoc; describes role, build-tag boundary, security caveats. | — | (none) | `//go:build evaltool` |
| `tools/eval/faultinject/kinds.go` | The 8 `FaultKind` constants and `AllFaultKinds`, plus `FaultDirective`. | `FaultKind`, the 8 `Fault*` constants, `AllFaultKinds`, `FaultDirective`, `IsKnownKind` | (none) | `//go:build evaltool` |
| `tools/eval/faultinject/errors.go` | Sentinel error values. | `ErrControlPlaneMustBeLoopback`, `ErrInjectionRunIDInvalid`, `ErrInjectionKindUnknown`, `ErrInjectionRateLimited`, `ErrInjectionTargetTooLong`, `ErrInjectionParamsTooLarge`, `ErrSentinelCred` | `errors` | `//go:build evaltool` |
| `tools/eval/faultinject/state.go` | `faultStore` with `Add` / `Clear` / `List` / `Lookup`; `FAKE_CRED_FOR_INJECTION_DO_NOT_USE` constant; per-`(run_id, kind)` counter; `MaxInjectionsPerRun`. | `Store`, `NewStore`, `MaxInjectionsPerRun`, `SentinelFakeCred`, `RunIDRegexp`, `ValidateRunID` | `regexp`, `sync`, `time` | `//go:build evaltool` |
| `tools/eval/faultinject/server.go` | `http.Server` with loopback enforcement, JSON handlers for `/inject` `/clear` `/list`, audit logger plumbed through. | `Server`, `Config`, `NewServer`, `(*Server).Serve` (or `ListenAndServe`), `(*Server).Shutdown` | `context`, `encoding/json`, `errors`, `fmt`, `io`, `net`, `net/http`, `strconv`, `time` | `//go:build evaltool` |
| `tools/eval/faultinject/audit.go` | Audit writer (defaults to `os.Stderr`, override for tests). | `AuditWriter`, `SetAuditWriter`, `emitAudit` | `encoding/json`, `io`, `os`, `sync`, `time` | `//go:build evaltool` |
| `tools/eval/faultinject/hookbridge.go` | The `evaltool`-only glue that installs `driver.SetHook` and `executor.SetHook` against this store, plus the per-kind hook implementation. | `Install`, `Uninstall` | `context`, `errors`, `io/fs`, `os`, `…driver`, `…executor` | `//go:build evaltool` |
| `tools/eval/faultinject/kinds_test.go` | Enum coverage: `AllFaultKinds` matches the declared 8; round-trip with `IsKnownKind`. | (tests) | `testing`, `reflect` | `//go:build evaltool` |
| `tools/eval/faultinject/state_test.go` | `faultStore` correctness; rate-limit boundary; `ValidateRunID` matrix; sentinel-cred byte-equality. | (tests) | `errors`, `testing`, `time` | `//go:build evaltool` |
| `tools/eval/faultinject/server_test.go` | All HTTP behaviour: loopback bind, endpoint contracts, timeouts, audit log. | (tests) | `bufio`, `bytes`, `encoding/json`, `errors`, `net`, `net/http`, `net/http/httptest`, `strings`, `sync`, `testing`, `time` | `//go:build evaltool` |
| `tools/eval/faultinject/hookbridge_test.go` | End-to-end: install bridge → fire each hook → expected error path. | (tests) | `context`, `errors`, `os`, `testing` | `//go:build evaltool` |
| `tools/eval/faultinject/buildtag_test.go` | Asserts every `.go` file in the package starts with `//go:build evaltool`. | (test) | `os`, `path/filepath`, `strings`, `testing` | `//go:build evaltool` |
| `internal/driver/fault_hook.go` | Production-build (no tag) shim: `HookPoint`, `Hook`, `SetHook`, `InjectIfActive`. Default noop. | `HookPoint`, the 5 driver `HookPoint*` constants, `Hook`, `SetHook`, `InjectIfActive` | `context`, `sync/atomic` | (none) |
| `internal/driver/fault_hook_test.go` | Noop fast-path benchmark; setter restores previous hook; nil hook restores noop. | (tests / bench) | `context`, `errors`, `testing` | (none) |
| `internal/executor/fault_hook.go` | Production-build shim for executor; same shape as driver. | `HookPoint`, the 2 executor `HookPoint*` constants, `Hook`, `SetHook`, `InjectIfActive` | `context`, `sync/atomic` | (none) |
| `internal/executor/fault_hook_test.go` | Same matrix as driver. | (tests) | `context`, `errors`, `testing` | (none) |
| `scripts/eval/faultinject_integration_test.sh` | Spawns driver+slave stubs under `-tags=evaltool`, walks the 8 kinds end-to-end, asserts observer buckets. | — | bash | (n/a) |

### 1.1 What this plan does NOT touch

- No edits to existing `internal/driver/*.go` or `internal/executor/*.go`
  files other than the new `fault_hook.go` / `fault_hook_test.go` files.
  In particular: the prompt forbids adding `InjectIfActive(...)` call
  sites that mutate existing business files in this worktree. Those
  call sites are owned by **D4 / D5 follow-up worktrees** that consume
  this harness. The `hookbridge.go` test in §3 wires hooks directly to
  contrived test fixtures, not to real driver/executor flow.

  > Rationale: WT-1-fault-injection delivers the *harness*. The prompt
  > under "工作约定" explicitly says "**不动** `internal/driver/*.go` /
  > `internal/executor/*.go` 的其它任何业务文件 (除非要在某个失败 return
  > 点调 `fault_hook.InjectIfActive(...)` —— 也只是新增一两行调用)".
  > To keep the diff conflict-free with concurrent D4 work, this
  > worktree adds ONLY the `fault_hook.go` shim files; call-site
  > insertions are deferred.

- No edits to `multi-agent/go.mod`. Everything used is in the standard
  library or already in `go.mod` (driver/executor live in the same
  module so `hookbridge.go`'s import is intra-module).
- No CLI binding. The control plane is started by a future
  `cmd/eval-runner` (or by the integration script directly via a tiny
  Go test main), not by `cmd/driver-agent`.

---

## 2. Implementation sketch (anchors for TDD; not the actual code)

```go
// kinds.go  (//go:build evaltool)
package faultinject

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

var AllFaultKinds = []FaultKind{
    FaultMissingFile, FaultStaleCapability, FaultWrongOSVersion,
    FaultForbiddenCred, FaultSlaveDisconnect, FaultDriverRestart,
    FaultModelRouteFailure, FaultDuplicatePickup,
}

type FaultDirective struct {
    Kind   FaultKind
    Target string
    Params map[string]string
    Seq    int
    At     time.Time
}

func IsKnownKind(k FaultKind) bool { /* linear scan AllFaultKinds */ }
```

```go
// state.go  (//go:build evaltool)
package faultinject

const (
    MaxInjectionsPerRun = 100
    SentinelFakeCred    = "FAKE_CRED_FOR_INJECTION_DO_NOT_USE"
)

var RunIDRegexp = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

func ValidateRunID(s string) error { /* RunIDRegexp.MatchString */ }

type Store struct {
    mu         sync.Mutex
    perRun     map[string][]FaultDirective
    perRunKind map[string]map[FaultKind]int
    seq        map[string]int
    now        func() time.Time // injectable for tests
}

func NewStore() *Store { ... }
func (s *Store) Add(runID string, kind FaultKind, target string, params map[string]string) (FaultDirective, error)
func (s *Store) Clear(runID string) (int, error)
func (s *Store) List(runID string) ([]FaultDirective, error)
func (s *Store) Lookup(runID string, kind FaultKind) (FaultDirective, bool)
```

```go
// server.go  (//go:build evaltool)
package faultinject

type Config struct {
    Listen string         // default "127.0.0.1:18189"
    Store  *Store
    Audit  io.Writer      // default os.Stderr
    Now    func() time.Time
}

type Server struct { /* embedded http.Server, listener, store, audit */ }

func NewServer(cfg Config) (*Server, error) {
    // 1. validate Listen host resolves to loopback only;
    //    return ErrControlPlaneMustBeLoopback otherwise.
    // 2. http.Server with ReadHeaderTimeout 5s,
    //    ReadTimeout 30s, WriteTimeout 30s, IdleTimeout 60s.
    // 3. mux: POST /inject, POST /clear, GET /list.
}

func (s *Server) Serve(ctx context.Context) error
func (s *Server) Shutdown(ctx context.Context) error
func (s *Server) Addr() string  // for tests
```

```go
// internal/driver/fault_hook.go  (no build tag)
package driver

type HookPoint string

const (
    HookPointDriverPickup         HookPoint = "driver.pickup"
    HookPointDriverCapabilityRead HookPoint = "driver.capability_read"
    HookPointDriverCredResolve    HookPoint = "driver.cred_resolve"
    HookPointDriverModelRoute     HookPoint = "driver.model_route"
    HookPointDriverMainLoop       HookPoint = "driver.main_loop"
    HookPointSlaveHeartbeat       HookPoint = "slave.heartbeat" // routed via driver bridge in tests
)

type Hook func(ctx context.Context, runID string, hp HookPoint, meta map[string]string) error

var hook atomic.Pointer[Hook]

func SetHook(h Hook) Hook { ... } // returns previous

func InjectIfActive(ctx context.Context, runID string, hp HookPoint, meta map[string]string) error {
    h := hook.Load()
    if h == nil {
        return nil
    }
    return (*h)(ctx, runID, hp, meta)
}
```

(Executor mirror is the same shape with `HookPointExecutorFileOpen` and
`HookPointExecutorCredResolve`. `HookPointSlaveHeartbeat` lives in the
driver package per spec §6; the slave-disconnect bridge in tests
spoofs a real heartbeat handler through that hook.)

---

## 3. Test matrix

Every Security mitigation (spec §7 a–h) and every fault kind has at
least one named test. Tests are listed by package; the integration
shell script is listed separately.

### 3.1 `tools/eval/faultinject` (`-tags=evaltool`)

| # | Test | Verifies | Spec ref |
| - | --- | --- | --- |
| F1 | `TestKinds_AllEightDeclared`                            | `len(AllFaultKinds) == 8` and each constant is present exactly once | §3 |
| F2 | `TestKinds_IsKnownRoundTrip`                            | every kind in `AllFaultKinds` is `IsKnownKind` true; unknown strings false | §3 |
| F3 | `TestStore_AddListLookup`                               | happy path: 3 injects → List returns 3 in injection order; Lookup returns first | §5 |
| F4 | `TestStore_ClearResetsCounter`                          | after `/clear`, the per-(run, kind) counter is 0; injection 101 succeeds | §4.3 |
| F5 | `TestStore_RateLimitPerRunPerKind`                      | 100 inserts of same (run, kind) succeed; 101st returns `ErrInjectionRateLimited`; other kinds for same run still accept | §7 b |
| F6 | `TestStore_ValidateRunID_Matrix`                        | `""`, `"abc"`, 129×"a", `"with/slash"`, `"good_id-12"` map to expected errors | §7 f |
| F7 | `TestStore_SentinelFakeCred_ByteEquality`               | `SentinelFakeCred == "FAKE_CRED_FOR_INJECTION_DO_NOT_USE"` (literal byte compare) | §7 c |
| F8 | `TestServer_RejectsNonLoopbackBind`                     | `NewServer(Config{Listen: "0.0.0.0:0"})` → `ErrControlPlaneMustBeLoopback`; same for `"::0"`, `"8.8.8.8:0"`, and a hostname that resolves to a non-loopback IP (`example.com`) | §7 a |
| F9 | `TestServer_AcceptsLoopback`                            | `127.0.0.1:0`, `[::1]:0`, and `localhost:0` (which resolves to loopback) all start cleanly | §7 a |
| F10 | `TestServer_InjectClearListRoundTrip`                  | POST /inject → GET /list returns 1 → POST /clear → GET /list returns 0 | §4 |
| F11 | `TestServer_InjectRejectsBadRunID`                     | run_id `""`, `"abc"`, 129×"a", `"with/slash"` each → 400 | §7 f |
| F12 | `TestServer_InjectRejectsUnknownKind`                  | `{"run_id":"abcdefgh","kind":"made_up"}` → 400 | §4.2 |
| F13 | `TestServer_InjectRateLimit_101st_400`                 | 100×inject same (run,kind) → 200; 101st → 400 with `ErrInjectionRateLimited` body | §7 b |
| F14 | `TestServer_HTTPTimeoutsSet`                           | reflect/inspect: ReadHeaderTimeout==5s, ReadTimeout==30s, WriteTimeout==30s, IdleTimeout==60s | §7 h |
| F15 | `TestServer_AuditLogEveryInjection`                    | 5 inject calls → audit writer received exactly 5 newline-terminated JSON lines, each with run_id/kind/hook/action="injected" | §7 e |
| F16 | `TestServer_AuditLogContainsHookOnHookFire`            | calling Store.Lookup-driven hook bridge: each fire writes one audit line whose `hook` matches the HookPoint string | §7 e |
| F17 | `TestServer_InjectRejectsOversizedTarget`              | target > 512 bytes → 400 `ErrInjectionTargetTooLong` | §4.2 |
| F18 | `TestServer_InjectRejectsOversizedParams`              | >16 params, or any value >1024 bytes → 400 `ErrInjectionParamsTooLarge` | §4.2 |
| F19 | `TestHookBridge_InjectAllEightKinds`                   | install bridge → for each of 8 kinds, fire the bridged hook → expected error type: `*os.PathError(ErrNotExist)`, stale-cap copy, OS swap, sentinel cred, slave-disconnect closed-conn err, driver-restart panic, model-route 503, duplicate-pickup dedup hit | §3.1, acceptance |
| F20 | `TestHookBridge_DuplicatePickup_NoCommandReplay`       | spawn fake "command runner" (counter-fixture) → inject duplicate_pickup → assert runner invocation count == 1 (idempotency-key path taken, not replay) | §7 d |
| F21 | `TestHookBridge_ForbiddenCred_SentinelOnly`            | install bridge → fire cred-resolve hook → returned string is byte-equal `FAKE_CRED_FOR_INJECTION_DO_NOT_USE` | §7 c |
| F22 | `TestPackage_AllFilesBuildTagged`                      | walk every `.go` file in the package; assert each starts with `//go:build evaltool` | §7 g |

### 3.2 `internal/driver` and `internal/executor` (no build tag)

| # | Test | Verifies | Spec ref |
| - | --- | --- | --- |
| D1 | `TestFaultHook_NoopByDefault`                                  | no `SetHook` call → `InjectIfActive(...)` returns nil for every HookPoint | §6 |
| D2 | `TestFaultHook_SetHookReturnsPrevious`                         | `SetHook(h1)` returns nil; `SetHook(h2)` returns h1; `SetHook(nil)` returns h2; after nil, `InjectIfActive` is noop again | §6 |
| D3 | `TestFaultHook_HookErrorPropagates`                            | installed hook returns sentinel error → `InjectIfActive` returns same error | §6 |
| D4 | `TestFaultHook_HookPointsEnumerated`                           | each declared `HookPoint*` constant is non-empty and unique within the package | §6 |
| D5 | `TestFaultHook_NoFaultInjectImport`                            | `go list -deps -test ./internal/driver/...` (and executor) does NOT list `tools/eval/faultinject` (asserted via `os/exec` inside the test) | §7 g |
| D6 | `BenchmarkFaultHook_NoopFastPath`                              | reports < 100 ns/op on the dev workstation; the test wrapping the bench (`TestBench_FastPathUnder100ns`) calls `testing.Benchmark` and asserts NsPerOp under threshold | §6.1, perf |

Mirror the same six tests in `internal/executor`.

### 3.3 Production-binary symbol leak guard

| # | Test | Verifies | Spec ref |
| - | --- | --- | --- |
| P1 | `scripts/eval/faultinject_no_symbols_in_prod.sh` | `go build -o /tmp/driver-prod ./cmd/driver-agent` (no tags) then `nm /tmp/driver-prod \| grep -c faultinject` == 0 | §7 g |

(Runs from CI / the integration script; covered by the Stage 3
acceptance checklist below.)

### 3.4 Integration smoke (8 kinds)

`scripts/eval/faultinject_integration_test.sh` does, for each kind:

1. Start `cmd/eval-runner` (or a tiny test main in the script's
   companion `.go` file) with `-tags=evaltool`. Listen 127.0.0.1:0,
   capture the chosen port.
2. POST `/inject` with the kind + run_id `it-<kind>-<rand8>`.
3. Drive a stub driver + stub slave + stub observer through one
   pickup/exec cycle for that run.
4. Assert the observer stub received a record with the expected
   `FailureCategory` (mapping from spec §3).
5. POST `/clear`; verify GET `/list` returns `[]`.
6. Tear down.

The script exits non-zero if any kind fails. CI runs it under
`run_in_background=true` so stage 3 acceptance can poll completion.

---

## 4. Sequencing — RED → GREEN → REFACTOR

Strict order. Each numbered step is one commit-sized unit; commit only
at §6 end-of-stage gates.

1. **kinds.go + kinds_test.go** — declare the 8 constants and
   `AllFaultKinds`; F1+F2 RED → declare → GREEN.
2. **errors.go** — declare the sentinels (no tests yet; they will be
   referenced by state/server tests).
3. **state.go + state_test.go** — `Store`, `ValidateRunID`,
   `SentinelFakeCred`, rate limit. F3–F7 RED → implement → GREEN.
4. **audit.go** — `AuditWriter` wrapper that JSON-encodes one line per
   call and synchronises writes with a mutex. No standalone test; its
   correctness is asserted via F15/F16.
5. **server.go + server_test.go** — handlers + loopback check + HTTP
   timeouts. F8–F18 RED → implement → GREEN. Add F22 last (build-tag
   walker).
6. **internal/driver/fault_hook.go + fault_hook_test.go** — noop +
   atomic pointer + bench. D1–D6 RED → implement → GREEN.
7. **internal/executor/fault_hook.go + fault_hook_test.go** — mirror
   of step 6.
8. **hookbridge.go + hookbridge_test.go** — install/uninstall bridge
   on top of `driver.SetHook` / `executor.SetHook`; per-kind hook
   implementations. F19–F21 RED → implement → GREEN.
9. **scripts/eval/faultinject_integration_test.sh** plus any
   `cmd/eval-runner`-shaped test main required to exercise the 8 kinds.
   Wire up; run; assert.
10. **scripts/eval/faultinject_no_symbols_in_prod.sh** — production
    `nm` check; run; assert zero matches.

---

## 5. Tooling commands (Stage 3 final-pass)

```bash
# Unit + race + shuffle
go test ./multi-agent/tools/eval/faultinject/... \
    -count=1 -shuffle=on -race -tags=evaltool

go test ./multi-agent/internal/driver/... ./multi-agent/internal/executor/... \
    -count=1 -shuffle=on -race

# Fast-path benchmark gate
go test -bench=BenchmarkFaultHook -benchtime=1s -run=^$ \
    ./multi-agent/internal/driver/... \
    ./multi-agent/internal/executor/...

# Vet + format
go vet ./multi-agent/...
gofmt -l multi-agent/tools/eval/faultinject \
        multi-agent/internal/driver \
        multi-agent/internal/executor

# Production-binary symbol-leak check (must print 0)
go build -o /tmp/driver-prod ./multi-agent/cmd/driver-agent
nm /tmp/driver-prod | grep -c faultinject || echo OK_ZERO

# Integration smoke (8 kinds) — runs in background, polled via Monitor
bash multi-agent/scripts/eval/faultinject_integration_test.sh
```

The wrapper inside `BenchmarkFaultHook_NoopFastPath` calls
`testing.Benchmark` and fails when NsPerOp ≥ 100 so the perf gate is a
test failure, not a soft regression.

---

## 6. Stage gates inside Stage 3

After each numbered step in §4, run `go test ./<changed-package>/... -race`
and only proceed when it is green. After step 8, run the *full* §5
command list. Commit only once, at the end of step 10, with the
trailer:

```
Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
```

Do NOT push (per prompt instruction).

---

## 7. Mapping back to Spec §7 (Security)

| Mitigation | Tests covering it |
| --- | --- |
| (a) Loopback bind enforced            | F8, F9 |
| (b) Per-(run, kind) rate limit        | F5, F13 |
| (c) `FaultForbiddenCred` sentinel     | F7, F21 |
| (d) `FaultDuplicatePickup` no replay  | F20 |
| (e) Audit log every injection         | F15, F16 |
| (f) `run_id` validation               | F6, F11 |
| (g) Build-tag isolation               | F22, D5, P1 |
| (h) HTTP server timeouts              | F14 |

No mitigation is uncovered.

## 8. Mapping back to Spec §3 (8 fault kinds)

| Kind                  | Covered by                       |
| --------------------- | -------------------------------- |
| `missing_file`        | F19 (slot 1), integration smoke  |
| `stale_capability`    | F19 (slot 2), integration smoke  |
| `wrong_os_version`    | F19 (slot 3), integration smoke  |
| `forbidden_cred`      | F19 (slot 4), F21, integration   |
| `slave_disconnect`    | F19 (slot 5), integration smoke  |
| `driver_restart`      | F19 (slot 6), integration smoke  |
| `model_route_failure` | F19 (slot 7), integration smoke  |
| `duplicate_pickup`    | F19 (slot 8), F20, integration   |

All 8 kinds covered by at least one Go test and one integration scenario.
