# WT-2-dry-run-validator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the §A3 four-class pre-execution validator
(`missing_file` / `wrong_version` / `forbidden_cred` /
`policy_violation`) in a new `internal/contract/validator/` subpackage,
extend `dryRunContractTool` to invoke it + emit three metric events,
add the `dry_run_blocks` table + writer, and register the `NoDryRun`
ablation flag with mandatory bypass logging.

**Architecture:** A pure validator package returns `[]Block` given
`(TaskContract, capability.Snapshot)`. The driver's existing
`dryRunContractTool` calls it after the current
`analyzeContractCapabilities` step, persists blocks via a new
observerstore writer, and emits three per-invocation events via the
already-wired `Tools.emit(observer.Event)`. Backward compat: when the
optional `capability_snapshot` arg is omitted, the four new classes
return no blocks and the existing `recommended_route` output is
unchanged.

**Tech Stack:** Go 1.22+, `golang.org/x/mod/semver`, SQLite (via
observerstore), existing `capability` / `contract` / `ablation` /
`observer` / `secretscrub` packages.

## Global Constraints

- Repo root: `/root/multi-agent`; Go module root `multi-agent/`; **cwd must be** `/root/multi-agent/.worktrees/p2-dry-run-validator`.
- Branch: `paper/v3/p2-dry-run-validator`; base `origin/paper/v3-integration` = `d053897`. **Do not `git push`**.
- Every commit message ends with the trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Semver comparisons use `golang.org/x/mod/semver`. **Never** hand-roll `strings.Split(v, ".")` + `strconv.Atoi`. CI guard grep: `strings.Split.*"\\."` under `internal/contract/validator/` must return empty.
- All SQL uses `?` placeholders. **No** string concatenation into query bodies.
- `Block.Detail` is `fmt.Sprintf("%s: expected %s, actual %s", field, expected, actual)` — three inputs only. **Never** `json.Marshal(tc)` / `json.Marshal(snap)` into `Detail`.
- `Block.Detail` truncated to `maxDetailBytes = 8192` at BOTH validator construction and writer entry (defense-in-depth).
- `forbidden_cred` uses Go `==` on the underlying string. **Never** `HasPrefix` / `Contains` / `HasSuffix` / `EqualFold`.
- Validator package imports allow-list: `context`, `errors`, `fmt`, `log`, `path`, `sort`, `strings`, `golang.org/x/mod/semver`, `github.com/yourorg/multi-agent/internal/ablation`, `.../internal/capability`, `.../internal/contract`. **No** `os` / `net` / `io/*` / `database/sql`.
- `NoDryRun` ablation gate: when true, tool short-circuits AFTER contract validation and BEFORE `validator.Check`. Must log exactly one line: `[ablation] NoDryRun: skipped conversation=<conversation_id>` (via stdlib `log`). Zero events emitted; zero `dry_run_blocks` rows.
- Every dry-run invocation emits EXACTLY 3 events (`PreExecutionFaultCatchRate`, `MissingArtifactDetectionRate`, `PolicyViolationPreventionRate`) with payloads shaped per spec §5:
  - `PreExecutionFaultCatchRate`: `{numerator, blocks_total, attempt_id, contract_hash, experiment_id}`
  - `MissingArtifactDetectionRate`: `{numerator, missing_files, attempt_id, contract_hash, experiment_id}`
  - `PolicyViolationPreventionRate`: `{numerator, policy_violations, attempt_id, contract_hash, experiment_id}`
  Denominator is `COUNT(events WHERE type='<Metric>')` and includes clean invocations.
- Any hard `< N ns` performance assertion in tests MUST be conditionalised: `if testing.Short() || os.Getenv("CI") != "" { t.Skip("perf assertion skipped in short/CI mode") }`.
- All tests run under `-race -shuffle=on`; test names use table-driven `t.Run` sub-tests when >1 case.

## File Structure

| File | Purpose | New? |
| --- | --- | --- |
| `multi-agent/internal/contract/types.go` | Extend `CapabilityRequirements` with `ForbiddenAliases`, `ToolRequirements`; extend `ExecutionPolicy` with `RequiredReach`. | Modify |
| `multi-agent/internal/contract/types_test.go` | Round-trip JSON tests for the three new fields (marshalled key stability, default-zero shape). | New |
| `multi-agent/internal/contract/validator/validator.go` | `Validator` interface, `Block`/`Kind`/`Severity` types, `New()` constructor, four private check funcs (`checkMissingFile`, `checkWrongVersion`, `checkForbiddenCred`, `checkPolicyViolation`), semver helper `ensureVPrefix`, `maxDetailBytes` constant + `truncateDetail` helper. | New |
| `multi-agent/internal/contract/validator/validator_test.go` | 4 positive/negative injects per class; semver-rc negative; forbidden_cred bypass matrix; `Block.Detail` size cap. | New |
| `multi-agent/internal/contract/validator/ablation.go` | `disableDryRun bool` + `IsDryRunDisabled()` + `SetDryRunDisabled(v bool)` (test-only helper) + `init()` registering `NoDryRun`. | New |
| `multi-agent/internal/contract/validator/ablation_test.go` | Register-succeeds + IsDryRunDisabled reflects Default.SetByName toggling. | New |
| `multi-agent/internal/contract/validator/imports_test.go` | `go/parser`-driven test asserting the package's import list matches the allow-list from §Global Constraints (purity contract §7(a)). | New |
| `multi-agent/internal/driver/capability_tools.go` | Extend `dryRunContractTool` schema/`Call`: optional `capability_snapshot` arg → `validator.Check` → persist blocks → emit 3 events. Add `experimentIDFromContract(tc)` helper. Add `attempt_id` to `dryRunReport`. | Modify |
| `multi-agent/internal/driver/capability_tools_test.go` | Per-class inject; 3-event count on mixed dry-run; ablation bypass log assertion; backward-compat (snapshot omitted); best-effort write-failure warning. | Modify |
| `multi-agent/internal/observerstore/schema.sql` | Append `CREATE TABLE dry_run_blocks (...)` with 3 indexes. | Modify |
| `multi-agent/internal/observerstore/dry_run_blocks_writer.go` | `DryRunBlockRow` struct, `DryRunBlockWriter` interface, `NewDryRunBlockWriter(*sql.DB)`, parameterized `INSERT ... ON CONFLICT(block_id) DO NOTHING`, `secretscrub.Sanitize` on 4 free-text columns, 8 KiB `detail` truncation. | New |
| `multi-agent/internal/observerstore/dry_run_blocks_writer_test.go` | INSERT round-trip; ON CONFLICT idempotency; detail truncation at 8 KiB; SQL-injection meta-char persists verbatim; consumer-view SELECT §6.1 numeric result. | New |
| `multi-agent/go.mod` / `go.sum` | Add `golang.org/x/mod` if not already present. | Modify (maybe) |

## Task Index

| # | Task | Depends on |
| --- | --- | --- |
| 1 | Contract type extensions (`ForbiddenAliases`, `ToolRequirements`, `RequiredReach`) | — |
| 2 | Validator package skeleton + `Block`/`Kind` types + import-purity test | 1 |
| 3 | `checkMissingFile` (TDD) | 2 |
| 4 | `checkWrongVersion` with `golang.org/x/mod/semver` (TDD) | 2 |
| 5 | `checkForbiddenCred` exact-match (TDD) | 2 |
| 6 | `checkPolicyViolation` (TDD) | 2 |
| 7 | `New()` composite validator + `Block.Detail` size cap | 3–6 |
| 8 | `NoDryRun` ablation registration | 2 |
| 9 | `dry_run_blocks` table DDL + writer skeleton | — |
| 10 | Writer: parameterized INSERT, secret scrub, detail truncation | 9 |
| 11 | Consumer-view SELECT §6.1 numeric-result test | 10 |
| 12 | Extend `dryRunContractTool` — schema, `capability_snapshot` parse, validator call | 7, 10 |
| 13 | Emit 3 metric events + persist blocks; wire ablation short-circuit | 8, 12 |
| 14 | Backward-compat test (snapshot omitted) + mixed dry-run 3-event count | 13 |
| 15 | Final full-suite gate + `go vet ./...` + commit polish | 14 |

Every task in §Task Details below is self-contained: file paths,
failing tests written first, exact commands to run, exact expected
output, and a commit step. Tasks 3–6 are independently reviewable
(each contributes one check function + its tests).

---

## Task Details

### Task 1: Extend `contract.TaskContract` with the three pre-exec fields

**Files:**
- Modify: `multi-agent/internal/contract/types.go` (add `ToolRequirement` type, extend `CapabilityRequirements`, extend `ExecutionPolicy`)
- Test: `multi-agent/internal/contract/types_test.go` (new file — round-trip JSON stability)

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `type ToolRequirement struct { Name string; MinVersion string }` — `json:"name"` / `json:"min_version,omitempty"`.
  - `CapabilityRequirements.ForbiddenAliases []string` — `json:"forbidden_aliases,omitempty"`.
  - `CapabilityRequirements.ToolRequirements []ToolRequirement` — `json:"tool_requirements,omitempty"`.
  - `ExecutionPolicy.RequiredReach capability.NetworkReach` — `json:"required_reach,omitempty"`.

- [ ] **Step 1: Write the failing test**

Create `multi-agent/internal/contract/types_test.go`:

```go
package contract

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/yourorg/multi-agent/internal/capability"
)

func TestCapabilityRequirements_ForbiddenAliases_RoundTrip(t *testing.T) {
	orig := CapabilityRequirements{
		Skills:           []string{"chat"},
		Tools:            []string{"grep"},
		ForbiddenAliases: []string{"openai_for_glm", "gh_pat_dev"},
	}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got CapabilityRequirements
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(orig.ForbiddenAliases, got.ForbiddenAliases) {
		t.Errorf("ForbiddenAliases round-trip: got %v want %v", got.ForbiddenAliases, orig.ForbiddenAliases)
	}
}

func TestCapabilityRequirements_ToolRequirements_RoundTrip(t *testing.T) {
	orig := CapabilityRequirements{
		ToolRequirements: []ToolRequirement{
			{Name: "go", MinVersion: "1.22.0"},
			{Name: "sqlite3", MinVersion: ""}, // presence-only
		},
	}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got CapabilityRequirements
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(orig.ToolRequirements, got.ToolRequirements) {
		t.Errorf("ToolRequirements round-trip: got %v want %v", got.ToolRequirements, orig.ToolRequirements)
	}
}

func TestExecutionPolicy_RequiredReach_RoundTrip(t *testing.T) {
	orig := ExecutionPolicy{
		Routing:       RoutingMasterOnly,
		RequiredReach: capability.NetworkIntranet,
	}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ExecutionPolicy
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.RequiredReach != capability.NetworkIntranet {
		t.Errorf("RequiredReach round-trip: got %q want %q", got.RequiredReach, capability.NetworkIntranet)
	}
}

// Zero-value emitting: `required_reach` and the two new
// capability_requirements fields all carry `omitempty` — a Marshal of a
// zero-value ExecutionPolicy / CapabilityRequirements MUST NOT include
// them (backward compat with existing task_contracts.body JSON blobs).
func TestNewFields_OmitEmpty(t *testing.T) {
	var cr CapabilityRequirements
	b, err := json.Marshal(cr)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bs := string(b); containsAny(bs, "forbidden_aliases", "tool_requirements") {
		t.Errorf("empty CapabilityRequirements leaked new keys: %s", bs)
	}
	var ep ExecutionPolicy
	b2, err := json.Marshal(ep)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bs := string(b2); containsAny(bs, "required_reach") {
		t.Errorf("empty ExecutionPolicy leaked new key: %s", bs)
	}
}

func containsAny(hay string, needles ...string) bool {
	for _, n := range needles {
		for i := 0; i+len(n) <= len(hay); i++ {
			if hay[i:i+len(n)] == n {
				return true
			}
		}
	}
	return false
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go test ./internal/contract/ -run 'TestCapabilityRequirements_ForbiddenAliases_RoundTrip|TestCapabilityRequirements_ToolRequirements_RoundTrip|TestExecutionPolicy_RequiredReach_RoundTrip|TestNewFields_OmitEmpty' -count=1
```

Expected: build errors — `ForbiddenAliases`, `ToolRequirements`, `RequiredReach`, `ToolRequirement` are undefined.

- [ ] **Step 3: Extend `types.go`**

Modify `multi-agent/internal/contract/types.go`. Add the import for `capability`:

```go
import (
	"encoding/json"

	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/commandiface"
)
```

Extend `ExecutionPolicy` — add the last field (keep every existing field verbatim):

```go
type ExecutionPolicy struct {
	Routing                          string   `json:"routing"`
	// ... unchanged fields ...
	AllowedTargets                   []string `json:"allowed_targets,omitempty"`
	// RequiredReach is the §A3 minimum outbound network reach the
	// executing host must have. Empty ⇒ no constraint. Semantic ladder:
	// none < loopback-only < intranet < internet. Enforced by
	// internal/contract/validator.checkPolicyViolation.
	RequiredReach capability.NetworkReach `json:"required_reach,omitempty"`
}
```

Extend `CapabilityRequirements` and add the `ToolRequirement` type:

```go
type CapabilityRequirements struct {
	Skills           []string          `json:"skills"`
	Tools            []string          `json:"tools"`
	Resources        json.RawMessage   `json:"resources,omitempty"`
	// ForbiddenAliases lists capability.CredentialAlias values that MUST
	// NOT be present on the executing host. Compared with exact
	// case-sensitive string equality against Snapshot.Credentials — see
	// wt2-dry-run-validator.spec.md §7(e).
	ForbiddenAliases []string          `json:"forbidden_aliases,omitempty"`
	// ToolRequirements carries version predicates. Presence-only
	// requirements can still use Tools []string; ToolRequirements
	// upgrades a tool to "must be present AND >= MinVersion" (semver).
	ToolRequirements []ToolRequirement `json:"tool_requirements,omitempty"`
}

// ToolRequirement pairs a tool name with a semver minimum version.
// Empty MinVersion ⇒ presence-only (same semantics as the legacy Tools
// []string entry).
type ToolRequirement struct {
	Name       string `json:"name"`
	MinVersion string `json:"min_version,omitempty"`
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go test ./internal/contract/ -count=1
```

Expected: PASS for the four new tests AND all existing `internal/contract/` tests (fields are additive, existing test contracts are unaffected).

- [ ] **Step 5: Commit**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator
git add multi-agent/internal/contract/types.go multi-agent/internal/contract/types_test.go
git commit -m "$(cat <<'EOF'
WT-2-dry-run-validator: extend TaskContract for §A3 pre-exec inputs

- CapabilityRequirements.ForbiddenAliases []string
- CapabilityRequirements.ToolRequirements []ToolRequirement
- ExecutionPolicy.RequiredReach capability.NetworkReach

All three are additive with omitempty; existing task_contracts.body
JSON is unaffected. Consumed by internal/contract/validator (Task 2+).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Validator package skeleton + import-purity test

**Files:**
- Create: `multi-agent/internal/contract/validator/validator.go`
- Create: `multi-agent/internal/contract/validator/validator_test.go`
- Create: `multi-agent/internal/contract/validator/imports_test.go`
- Modify: `multi-agent/go.mod` / `go.sum` (add `golang.org/x/mod`)

**Interfaces:**
- Consumes: `contract.TaskContract` (from Task 1), `capability.Snapshot`.
- Produces:
  - `type Validator interface { Check(ctx context.Context, tc contract.TaskContract, snap capability.Snapshot) []Block }`
  - `type Block struct { Kind Kind; Severity Severity; Field, Expected, Actual, Detail string }` (JSON tags per spec §2)
  - `type Kind string`; constants `KindMissingFile`, `KindWrongVersion`, `KindForbiddenCred`, `KindPolicyViolation`
  - `type Severity string`; constant `SeverityBlock`
  - `const maxDetailBytes = 8192`
  - `func New() Validator` (stub: returns a validator whose `Check` returns `nil` — real checks land in Tasks 3–6)
  - `func newBlock(kind Kind, field, expected, actual string) Block` — assembles `Detail` per §7(f) and truncates.

- [ ] **Step 1: Add the semver dependency**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go get golang.org/x/mod@latest
```

Expected: `go.mod` gains `golang.org/x/mod vX.Y.Z`; no errors.

- [ ] **Step 2: Write the failing test (`validator_test.go`)**

Create `multi-agent/internal/contract/validator/validator_test.go`:

```go
package validator

import (
	"context"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/contract"
)

func TestNew_ReturnsValidator(t *testing.T) {
	v := New()
	if v == nil {
		t.Fatal("New() returned nil")
	}
}

func TestValidator_EmptyContract_NoBlocks(t *testing.T) {
	v := New()
	got := v.Check(context.Background(), contract.TaskContract{}, capability.Snapshot{})
	if len(got) != 0 {
		t.Errorf("empty contract + empty snapshot must produce no blocks; got %d: %+v", len(got), got)
	}
}

func TestBlock_DetailShape(t *testing.T) {
	b := newBlock(KindMissingFile, "data_contract.read_artifacts[0].name", "config.yaml", "no snapshot file resource matches")
	if b.Kind != KindMissingFile {
		t.Errorf("Kind: got %q", b.Kind)
	}
	if b.Severity != SeverityBlock {
		t.Errorf("Severity: got %q", b.Severity)
	}
	if b.Field != "data_contract.read_artifacts[0].name" {
		t.Errorf("Field: got %q", b.Field)
	}
	want := "data_contract.read_artifacts[0].name: expected config.yaml, actual no snapshot file resource matches"
	if b.Detail != want {
		t.Errorf("Detail: got %q\nwant %q", b.Detail, want)
	}
}

// §7(c) + §7(f): Detail is truncated at maxDetailBytes on construction.
// A 20 KiB actual value must be cut down and end with a truncation marker.
func TestBlock_DetailTruncatedAt8KiB(t *testing.T) {
	huge := strings.Repeat("x", 20*1024)
	b := newBlock(KindWrongVersion, "capability_requirements.tools[0]", ">=1.22.0", huge)
	if len(b.Detail) > maxDetailBytes {
		t.Errorf("Detail exceeded cap: got %d bytes, cap %d", len(b.Detail), maxDetailBytes)
	}
	if !strings.HasSuffix(b.Detail, "<...truncated>") {
		t.Errorf("truncated Detail must carry sentinel; got trailing %q", b.Detail[len(b.Detail)-32:])
	}
}
```

- [ ] **Step 3: Write the failing import-purity test (`imports_test.go`)**

Create `multi-agent/internal/contract/validator/imports_test.go`:

```go
package validator_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// §7(a): validator MUST be pure. Enforce the import allow-list
// mechanically so a future refactor cannot silently pull in os/net/io
// and turn the dry-run tool into a side-effect vector.
var allowedImports = map[string]struct{}{
	`"context"`:                                       {},
	`"errors"`:                                        {},
	`"fmt"`:                                           {},
	`"log"`:                                           {},
	`"path"`:                                          {},
	`"sort"`:                                          {},
	`"strings"`:                                       {},
	`"golang.org/x/mod/semver"`:                       {},
	`"github.com/yourorg/multi-agent/internal/ablation"`:   {},
	`"github.com/yourorg/multi-agent/internal/capability"`: {},
	`"github.com/yourorg/multi-agent/internal/contract"`:   {},
}

func TestImportPurity(t *testing.T) {
	fset := token.NewFileSet()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		for _, imp := range f.Imports {
			if _, ok := allowedImports[imp.Path.Value]; !ok {
				t.Errorf("%s: forbidden import %s — validator must stay pure (see spec §7(a))", e.Name(), imp.Path.Value)
			}
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they fail**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go test ./internal/contract/validator/... -count=1
```

Expected: build errors — package/types not defined.

- [ ] **Step 5: Implement `validator.go`**

Create `multi-agent/internal/contract/validator/validator.go`:

```go
// Package validator runs the §A3 four-class pre-execution checks against
// a (TaskContract, capability.Snapshot) pair. Every check is a pure
// function; the package MUST NOT import os / net / io / database/sql —
// see the imports_test enforcement and spec §7(a).
package validator

import (
	"context"
	"fmt"
	"strings"

	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/contract"
)

// Severity is currently a single value; kept as a type so future
// advisory / info classes can extend without an API break.
type Severity string

const SeverityBlock Severity = "block"

type Kind string

const (
	KindMissingFile     Kind = "missing_file"
	KindWrongVersion    Kind = "wrong_version"
	KindForbiddenCred   Kind = "forbidden_cred"
	KindPolicyViolation Kind = "policy_violation"
)

// maxDetailBytes caps every Block.Detail. Enforced twice: here at
// construction and again at the observerstore writer boundary
// (dry_run_blocks_writer.go). See spec §7(c) + §7(f).
const maxDetailBytes = 8192

// detailTruncSentinel is appended when a Detail is cut. Callers grepping
// blocked_at rows can filter for this suffix to detect truncation.
const detailTruncSentinel = "<...truncated>"

// Block is one §A3 violation surfaced by a Validator.
type Block struct {
	Kind     Kind     `json:"kind"`
	Severity Severity `json:"severity"`
	Field    string   `json:"field"`
	Expected string   `json:"expected"`
	Actual   string   `json:"actual"`
	Detail   string   `json:"detail"`
}

// Validator runs every registered check against (tc, snap) and returns
// zero or more blocks. Implementations MUST be pure: no I/O, no
// filesystem access, no network, no goroutines. The context is
// threaded only so a future block-cancellation surface is compatible;
// the default staticValidator never observes it.
type Validator interface {
	Check(ctx context.Context, tc contract.TaskContract, snap capability.Snapshot) []Block
}

// New returns the default validator that runs all four §A3 checks in
// deterministic order (missing_file, wrong_version, forbidden_cred,
// policy_violation). Tasks 3–6 populate the check functions; the
// composite scaffold here returns nil so Task 2 can commit a
// self-consistent skeleton.
func New() Validator { return staticValidator{} }

type staticValidator struct{}

func (staticValidator) Check(_ context.Context, _ contract.TaskContract, _ capability.Snapshot) []Block {
	// Tasks 3–6 replace this body with the four ordered check calls.
	return nil
}

// newBlock assembles a Block with the required three-field Detail and
// enforces the maxDetailBytes cap. §7(f): callers pass only field name +
// expected + actual — NEVER raw contract / snapshot bodies. §7(c): the
// truncation caps a hand-crafted 40 KiB actual value.
func newBlock(kind Kind, field, expected, actual string) Block {
	detail := fmt.Sprintf("%s: expected %s, actual %s", field, expected, actual)
	if len(detail) > maxDetailBytes {
		// Reserve room for the sentinel so the cap is honoured even
		// after appending. Cut at byte boundary; Detail is not required
		// to be UTF-8-safe (structured fields carry the machine parts).
		cut := maxDetailBytes - len(detailTruncSentinel)
		if cut < 0 {
			cut = 0
		}
		detail = detail[:cut] + detailTruncSentinel
	}
	return Block{
		Kind:     kind,
		Severity: SeverityBlock,
		Field:    field,
		Expected: expected,
		Actual:   actual,
		Detail:   detail,
	}
}

// blockSliceGrow is a tiny helper: appends `b` to `dst` iff b is not
// the zero Block. Tasks 3–6 use it so a check that finds nothing can
// return Block{} without callers needing null-check boilerplate.
func blockSliceGrow(dst []Block, b Block) []Block {
	if b.Kind == "" {
		return dst
	}
	return append(dst, b)
}

// truncateForField is a helper for check functions that build a Field
// name from a slice index — keeps the printf format in one place.
func fieldAt(base string, i int) string {
	return fmt.Sprintf("%s[%d]", base, i)
}

// Suppress unused-import warnings on strings during Task 2 (used by
// Tasks 3–6). Leave it here; removing later is a no-op edit.
var _ = strings.Repeat
```

- [ ] **Step 6: Run tests to verify they pass**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go test ./internal/contract/validator/... -count=1 -race
```

Expected: PASS for `TestNew_ReturnsValidator`, `TestValidator_EmptyContract_NoBlocks`, `TestBlock_DetailShape`, `TestBlock_DetailTruncatedAt8KiB`, `TestImportPurity`.

- [ ] **Step 7: Commit**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator
git add multi-agent/go.mod multi-agent/go.sum multi-agent/internal/contract/validator/
git commit -m "$(cat <<'EOF'
WT-2-dry-run-validator: validator package skeleton (§A3 scaffold)

- Validator interface, Block/Kind/Severity types
- newBlock() with maxDetailBytes = 8192 truncation (§7(c) + §7(f))
- staticValidator{} placeholder Check (Tasks 3–6 populate)
- Import-purity test enforces §7(a): validator MUST NOT import
  os/net/io/database/sql
- go.mod: add golang.org/x/mod for semver comparison in Task 4

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: `checkMissingFile` (TDD)

**Files:**
- Modify: `multi-agent/internal/contract/validator/validator.go` (add `checkMissingFile`, wire into `staticValidator.Check`)
- Modify: `multi-agent/internal/contract/validator/validator_test.go` (add inject cases)

**Interfaces:**
- Consumes: `contract.DataContract.ReadArtifacts[].Name`, `capability.Snapshot.Files[].KindDetail`, `capability.Snapshot.Files[].PathPattern`.
- Produces: `func checkMissingFile(tc contract.TaskContract, snap capability.Snapshot) []Block` — private; called by `staticValidator.Check`.

**Semantic recap (spec §3.1):** for each `ReadArtifact` where `Kind == "file"` OR `Kind == "" && (name contains "/" or ".")`, emit a block unless SOME `snap.Files[i]` has `path.Match(patternPath, name)` returning true. `path.Match` errors → warn-log, no-match. Empty `Snapshot.Files` + any file-shaped artifact → one block per artifact.

- [ ] **Step 1: Write the failing tests**

Append to `multi-agent/internal/contract/validator/validator_test.go`:

```go
func TestCheckMissingFile_EmitsBlock_WhenNoFileResourceMatches(t *testing.T) {
	tc := contract.TaskContract{
		DataContract: contract.DataContract{
			ReadArtifacts: []contract.ArtifactRef{
				{Kind: "file", Name: "configs/prod.yaml"},
			},
		},
	}
	snap := capability.Snapshot{
		Files: []capability.FileResource{
			{KindDetail: "repo", PathPattern: "src/*.go"},
		},
	}
	got := New().Check(context.Background(), tc, snap)
	if len(got) != 1 || got[0].Kind != KindMissingFile {
		t.Fatalf("expected 1 missing_file block; got %d: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Field, "read_artifacts[0]") {
		t.Errorf("Field must anchor at the offending index: got %q", got[0].Field)
	}
	if got[0].Expected != "configs/prod.yaml" {
		t.Errorf("Expected: got %q", got[0].Expected)
	}
}

func TestCheckMissingFile_NoBlock_WhenPathMatches(t *testing.T) {
	tc := contract.TaskContract{
		DataContract: contract.DataContract{
			ReadArtifacts: []contract.ArtifactRef{
				{Kind: "file", Name: "configs/prod.yaml"},
			},
		},
	}
	snap := capability.Snapshot{
		Files: []capability.FileResource{
			{KindDetail: "config", PathPattern: "configs/*.yaml"},
		},
	}
	got := New().Check(context.Background(), tc, snap)
	for _, b := range got {
		if b.Kind == KindMissingFile {
			t.Errorf("expected no missing_file block; got %+v", b)
		}
	}
}

// §3.1 shape heuristic: kind empty + name looks path-shaped → still checked.
func TestCheckMissingFile_ImpliedFileArtifact(t *testing.T) {
	tc := contract.TaskContract{
		DataContract: contract.DataContract{
			ReadArtifacts: []contract.ArtifactRef{
				{Kind: "", Name: "spec.md"},                // has extension → treated as file
				{Kind: "", Name: "vendor/lib/mod.go"},     // has slash → treated as file
				{Kind: "", Name: "just-an-identifier"},   // no slash, no dot → NOT a file
			},
		},
	}
	got := New().Check(context.Background(), tc, capability.Snapshot{})
	var missing int
	for _, b := range got {
		if b.Kind == KindMissingFile {
			missing++
		}
	}
	if missing != 2 {
		t.Fatalf("expected 2 implied-file blocks; got %d (all blocks: %+v)", missing, got)
	}
}

// Malformed pattern: path.Match returns an error. checkMissingFile must
// treat this as no-match (still emit the block for the artifact) and
// NOT surface the pattern in Block.Detail (§7(f)).
func TestCheckMissingFile_MalformedPatternDoesNotLeak(t *testing.T) {
	tc := contract.TaskContract{
		DataContract: contract.DataContract{
			ReadArtifacts: []contract.ArtifactRef{{Kind: "file", Name: "x.txt"}},
		},
	}
	snap := capability.Snapshot{
		Files: []capability.FileResource{
			{KindDetail: "repo", PathPattern: "[malformed"},
		},
	}
	got := New().Check(context.Background(), tc, snap)
	if len(got) != 1 || got[0].Kind != KindMissingFile {
		t.Fatalf("expected 1 missing_file block; got %+v", got)
	}
	if strings.Contains(got[0].Detail, "[malformed") {
		t.Errorf("Detail must not leak the malformed pattern; got %q", got[0].Detail)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go test ./internal/contract/validator/ -run TestCheckMissingFile -count=1 -race
```

Expected: FAIL — `staticValidator.Check` still returns nil, so `len(got) == 0` for every "expected 1 block" case.

- [ ] **Step 3: Implement `checkMissingFile` + wire into `staticValidator.Check`**

Edit `multi-agent/internal/contract/validator/validator.go`:

1. Replace the top-level import block:

```go
import (
	"context"
	"fmt"
	"log"
	"path"
	"strings"

	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/contract"
)
```

2. Replace `staticValidator.Check` and add `checkMissingFile` + `looksLikePath`:

```go
func (staticValidator) Check(_ context.Context, tc contract.TaskContract, snap capability.Snapshot) []Block {
	var blocks []Block
	blocks = append(blocks, checkMissingFile(tc, snap)...)
	// Tasks 4–6 append their check outputs here.
	return blocks
}

// checkMissingFile — spec §3.1. For each ReadArtifact declared as a
// file (Kind=="file" or Kind empty with a path-shaped Name), require
// that at least one Snapshot.Files entry's PathPattern matches the
// Name via stdlib path.Match. Errors from path.Match are treated as
// no-match — the malformed pattern is logged at WARN and NEVER
// surfaced in Block.Detail (§7(f)).
func checkMissingFile(tc contract.TaskContract, snap capability.Snapshot) []Block {
	var out []Block
	for i, art := range tc.DataContract.ReadArtifacts {
		if !isFileArtifact(art) {
			continue
		}
		if matchAnyFile(snap.Files, art.Name) {
			continue
		}
		out = append(out, newBlock(
			KindMissingFile,
			fmt.Sprintf("data_contract.read_artifacts[%d].name", i),
			art.Name,
			"no snapshot file resource matches",
		))
	}
	return out
}

// isFileArtifact identifies file-shaped artifacts per §3.1: explicit
// Kind=="file", OR empty Kind with a Name that contains a slash or a
// dot (heuristic for "looks like a path"). This heuristic is
// deliberately conservative — a plain identifier like "workflow-id"
// slips through as NOT-a-file.
func isFileArtifact(art contract.ArtifactRef) bool {
	if art.Kind == "file" {
		return true
	}
	if art.Kind != "" {
		return false
	}
	return strings.ContainsAny(art.Name, "/.")
}

// matchAnyFile returns true iff any FileResource's PathPattern matches
// name via stdlib path.Match. A pattern-syntax error is logged and
// treated as no-match; the pattern itself never enters the returned
// blocks (§7(f)).
func matchAnyFile(files []capability.FileResource, name string) bool {
	for _, fr := range files {
		ok, err := path.Match(fr.PathPattern, name)
		if err != nil {
			// KindDetail is one of the 4 enum values, safe to log.
			// PathPattern is caller-controlled; NEVER include it here
			// or in Block.Detail — a malformed pattern is a snapshot
			// data-quality issue, not the artifact's fault.
			log.Printf("[validator] malformed path pattern in snapshot kind=%s (pattern hidden per §7(f))", fr.KindDetail)
			continue
		}
		if ok {
			return true
		}
	}
	return false
}
```

3. Remove the placeholder `var _ = strings.Repeat` line (now unused; `strings` is used by `isFileArtifact`).

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go test ./internal/contract/validator/ -count=1 -race
```

Expected: PASS for all `TestCheckMissingFile*`; existing skeleton tests still PASS; import-purity test still PASS (only added `log` + `path` — both on the allow-list).

- [ ] **Step 5: Commit**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator
git add multi-agent/internal/contract/validator/
git commit -m "$(cat <<'EOF'
WT-2-dry-run-validator: checkMissingFile (§3.1)

Path-shape heuristic for implicit file artifacts (Kind=="" + slash/dot);
stdlib path.Match against Snapshot.Files[].PathPattern; malformed
patterns log a warning that never names the pattern (§7(f)).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: `checkWrongVersion` with `golang.org/x/mod/semver` (TDD)

**Files:**
- Modify: `multi-agent/internal/contract/validator/validator.go` (add `checkWrongVersion`, `ensureVPrefix`, wire into `Check`)
- Modify: `multi-agent/internal/contract/validator/validator_test.go` (add inject cases incl. the §7(b) rc-prerelease negative)

**Interfaces:**
- Consumes: `contract.CapabilityRequirements.Tools` (presence-only), `contract.CapabilityRequirements.ToolRequirements[].{Name,MinVersion}`, `capability.Snapshot.Tools[].{Name,Version}`.
- Produces: `func checkWrongVersion(tc contract.TaskContract, snap capability.Snapshot) []Block`.

**Semantic recap (spec §3.2):** for each `ToolRequirement`: look up snapshot version by exact-case `Name`. Missing snapshot entry → block with `Actual = "<not installed>"`. Present but `semver.Compare(canonSnap, canonReq) < 0` → block. Unparseable operand → block with sentinel `Actual = "<unparseable: X>"` or `"<contract min_version unparseable: Y>"`. Legacy `Tools []string` entries are treated as presence-only requirements.

- [ ] **Step 1: Write the failing tests**

Append to `validator_test.go`:

```go
func TestCheckWrongVersion_EmitsBlock_WhenSnapshotBelowMin(t *testing.T) {
	tc := contract.TaskContract{
		CapabilityRequirements: contract.CapabilityRequirements{
			ToolRequirements: []contract.ToolRequirement{{Name: "go", MinVersion: "1.22.0"}},
		},
	}
	snap := capability.Snapshot{Tools: []capability.ToolVersion{{Name: "go", Version: "1.18.4"}}}
	got := New().Check(context.Background(), tc, snap)
	if len(got) != 1 || got[0].Kind != KindWrongVersion {
		t.Fatalf("expected 1 wrong_version block; got %+v", got)
	}
	if got[0].Actual != "1.18.4" {
		t.Errorf("Actual: got %q want %q", got[0].Actual, "1.18.4")
	}
}

func TestCheckWrongVersion_NoBlock_WhenSnapshotAtOrAboveMin(t *testing.T) {
	tc := contract.TaskContract{
		CapabilityRequirements: contract.CapabilityRequirements{
			ToolRequirements: []contract.ToolRequirement{{Name: "go", MinVersion: "1.22.0"}},
		},
	}
	for _, snapVer := range []string{"1.22.0", "1.22.5", "2.0.0"} {
		t.Run(snapVer, func(t *testing.T) {
			snap := capability.Snapshot{Tools: []capability.ToolVersion{{Name: "go", Version: snapVer}}}
			for _, b := range New().Check(context.Background(), tc, snap) {
				if b.Kind == KindWrongVersion {
					t.Errorf("unexpected wrong_version block: %+v", b)
				}
			}
		})
	}
}

// §7(b) THE CRITICAL NEGATIVE: 1.22.0-rc1 is a PRE-release; semver
// specifies it as LESS THAN 1.22.0. A hand-rolled "split by dot"
// comparator would incorrectly judge rc1 as ≥ 1.22.0 and let unstable
// pre-release binaries pass the dry-run.
func TestCheckWrongVersion_RCPrereleaseBelowRelease(t *testing.T) {
	tc := contract.TaskContract{
		CapabilityRequirements: contract.CapabilityRequirements{
			ToolRequirements: []contract.ToolRequirement{{Name: "go", MinVersion: "1.22.0"}},
		},
	}
	snap := capability.Snapshot{Tools: []capability.ToolVersion{{Name: "go", Version: "1.22.0-rc1"}}}
	got := New().Check(context.Background(), tc, snap)
	var wv int
	for _, b := range got {
		if b.Kind == KindWrongVersion {
			wv++
		}
	}
	if wv != 1 {
		t.Fatalf("§7(b) regression: rc1 must be < 1.22.0 (semver rule); got %d wrong_version blocks — did someone hand-roll semver? blocks=%+v", wv, got)
	}
}

func TestCheckWrongVersion_MissingSnapshotEntry(t *testing.T) {
	tc := contract.TaskContract{
		CapabilityRequirements: contract.CapabilityRequirements{
			ToolRequirements: []contract.ToolRequirement{{Name: "sqlite3", MinVersion: "3.40.0"}},
		},
	}
	got := New().Check(context.Background(), tc, capability.Snapshot{})
	if len(got) != 1 || got[0].Kind != KindWrongVersion {
		t.Fatalf("expected 1 wrong_version block; got %+v", got)
	}
	if got[0].Actual != "<not installed>" {
		t.Errorf("Actual sentinel: got %q", got[0].Actual)
	}
}

// Legacy Tools []string — presence-only. Absent snapshot ⇒ still a
// wrong_version block (§3.2 final paragraph).
func TestCheckWrongVersion_LegacyToolsPresenceOnly(t *testing.T) {
	tc := contract.TaskContract{
		CapabilityRequirements: contract.CapabilityRequirements{
			Tools: []string{"jq"},
		},
	}
	got := New().Check(context.Background(), tc, capability.Snapshot{})
	if len(got) != 1 || got[0].Kind != KindWrongVersion {
		t.Fatalf("expected 1 presence-only wrong_version block; got %+v", got)
	}
	if got[0].Expected != "<any version installed>" {
		t.Errorf("Expected sentinel: got %q", got[0].Expected)
	}
}

func TestCheckWrongVersion_UnparseableSnapshotVersion(t *testing.T) {
	tc := contract.TaskContract{
		CapabilityRequirements: contract.CapabilityRequirements{
			ToolRequirements: []contract.ToolRequirement{{Name: "python", MinVersion: "3.10.0"}},
		},
	}
	snap := capability.Snapshot{Tools: []capability.ToolVersion{{Name: "python", Version: "banana"}}}
	got := New().Check(context.Background(), tc, snap)
	if len(got) != 1 || got[0].Kind != KindWrongVersion {
		t.Fatalf("expected 1 wrong_version block; got %+v", got)
	}
	if !strings.HasPrefix(got[0].Actual, "<unparseable:") {
		t.Errorf("Actual sentinel: got %q", got[0].Actual)
	}
}

// CI GUARD: hand-rolled split-by-dot regression bait. Ensures NO source
// file in the validator package pattern-matches `strings.Split.*"."`.
// A ProductionCode file matching this regex is a spec §7(b) violation.
func TestNoHandRolledSemverSplit(t *testing.T) {
	// os is not on the validator import allow-list, but this test file
	// lives under package `validator_test` (external tests are exempt).
	// Move to a separate _test.go if needed.
	t.Skip("moved to imports_test.go if that file uses os; see plan §Task 4 step 3 for the final resting place")
}
```

Note: the last test `TestNoHandRolledSemverSplit` is a placeholder — we implement it properly in Step 3 by extending `imports_test.go` (external test package `validator_test` may use `os`).

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go test ./internal/contract/validator/ -run TestCheckWrongVersion -count=1 -race
```

Expected: FAIL — `checkWrongVersion` is not wired.

- [ ] **Step 3: Implement `checkWrongVersion` + `ensureVPrefix`**

Add to `multi-agent/internal/contract/validator/validator.go`:

1. Extend imports:

```go
import (
	"context"
	"fmt"
	"log"
	"path"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/contract"
)
```

2. Add functions:

```go
// checkWrongVersion — spec §3.2. Reads both the legacy Tools []string
// (presence-only) and the versioned ToolRequirements. Comparison uses
// golang.org/x/mod/semver, NEVER a hand-rolled split-by-dot — see
// spec §7(b) rc-prerelease rationale.
func checkWrongVersion(tc contract.TaskContract, snap capability.Snapshot) []Block {
	var out []Block

	// Presence-only pass over legacy Tools []string.
	for i, name := range tc.CapabilityRequirements.Tools {
		if _, ok := lookupTool(snap.Tools, name); ok {
			continue
		}
		out = append(out, newBlock(
			KindWrongVersion,
			fmt.Sprintf("capability_requirements.tools[%d]", i),
			"<any version installed>",
			"<not installed>",
		))
		_ = i
	}

	// Versioned pass over ToolRequirements.
	for i, req := range tc.CapabilityRequirements.ToolRequirements {
		field := fmt.Sprintf("capability_requirements.tool_requirements[%d]", i)

		got, ok := lookupTool(snap.Tools, req.Name)
		if !ok {
			expected := req.MinVersion
			if expected == "" {
				expected = "<any version installed>"
			} else {
				expected = ">=" + expected
			}
			out = append(out, newBlock(KindWrongVersion, field, expected, "<not installed>"))
			continue
		}
		if req.MinVersion == "" {
			continue // presence satisfied, no version constraint
		}

		canonReq := semver.Canonical(ensureVPrefix(req.MinVersion))
		if canonReq == "" {
			out = append(out, newBlock(
				KindWrongVersion, field, req.MinVersion,
				"<contract min_version unparseable: "+req.MinVersion+">",
			))
			continue
		}
		canonGot := semver.Canonical(ensureVPrefix(got.Version))
		if canonGot == "" {
			out = append(out, newBlock(
				KindWrongVersion, field, ">="+req.MinVersion,
				"<unparseable: "+got.Version+">",
			))
			continue
		}
		if semver.Compare(canonGot, canonReq) < 0 {
			out = append(out, newBlock(KindWrongVersion, field, ">="+req.MinVersion, got.Version))
		}
	}
	return out
}

// lookupTool returns the ToolVersion whose Name equals name (case-
// sensitive), and a boolean present flag.
func lookupTool(tools []capability.ToolVersion, name string) (capability.ToolVersion, bool) {
	for _, tv := range tools {
		if tv.Name == name {
			return tv, true
		}
	}
	return capability.ToolVersion{}, false
}

// ensureVPrefix prepends 'v' if missing. Required because
// semver.Canonical("1.22.0") returns "" — the mod/semver package only
// accepts the leading-v grammar from proxy.golang.org module paths.
// Callers pass user-supplied version strings which usually lack the v.
func ensureVPrefix(s string) string {
	if s == "" || s[0] == 'v' || s[0] == 'V' {
		return s
	}
	return "v" + s
}
```

3. Wire into `staticValidator.Check`:

```go
func (staticValidator) Check(_ context.Context, tc contract.TaskContract, snap capability.Snapshot) []Block {
	var blocks []Block
	blocks = append(blocks, checkMissingFile(tc, snap)...)
	blocks = append(blocks, checkWrongVersion(tc, snap)...)
	// Tasks 5–6 append here.
	return blocks
}
```

4. Replace the placeholder `TestNoHandRolledSemverSplit` in `validator_test.go` with a real one in `imports_test.go`:

Delete `TestNoHandRolledSemverSplit` from `validator_test.go` and append to `imports_test.go`:

```go
// §7(b) regression bait: any use of strings.Split with a literal "."
// under this package is a spec-forbidden hand-rolled semver split.
// Confirmed sightings in dependency stubs are OK — we grep source
// files only.
func TestNoHandRolledSemverSplit(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), `strings.Split(`) &&
			strings.Contains(string(body), `"."`) {
			// Coarse — a benign strings.Split(x, "/") + adjacent "." would
			// false-positive. If that happens, refine the guard to a
			// proper AST walk (not needed today).
			t.Errorf("%s: strings.Split + literal \".\" — hand-rolled semver split forbidden (§7(b))", e.Name())
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go test ./internal/contract/validator/ -count=1 -race
```

Expected: PASS for all `TestCheckWrongVersion*` + `TestNoHandRolledSemverSplit`. Existing tests (Task 3 + skeleton) still PASS.

- [ ] **Step 5: Commit**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator
git add multi-agent/internal/contract/validator/
git commit -m "$(cat <<'EOF'
WT-2-dry-run-validator: checkWrongVersion (§3.2) — semver via mod/semver

Uses golang.org/x/mod/semver.Compare with ensureVPrefix()
normalisation. Prerelease rule: v1.22.0-rc1 < v1.22.0 (§7(b)
canonical negative test guards against hand-rolled split-by-dot
regressions). Legacy Tools []string treated as presence-only.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: `checkForbiddenCred` exact-match (TDD)

**Files:**
- Modify: `multi-agent/internal/contract/validator/validator.go` (add `checkForbiddenCred`, wire into `Check`)
- Modify: `multi-agent/internal/contract/validator/validator_test.go` (bypass matrix)

**Interfaces:**
- Consumes: `contract.CapabilityRequirements.ForbiddenAliases`, `capability.Snapshot.Credentials`.
- Produces: `func checkForbiddenCred(tc contract.TaskContract, snap capability.Snapshot) []Block`.

**Semantic recap (spec §3.3 + §7(e)):** Go `==` on string. No prefix / substring / case-insensitive matching. Malformed alias in contract → block with `Actual = "<malformed alias>"` so the operator fixes their contract.

- [ ] **Step 1: Write the failing tests**

Append to `validator_test.go`:

```go
func TestCheckForbiddenCred_ExactMatch_Blocks(t *testing.T) {
	tc := contract.TaskContract{
		CapabilityRequirements: contract.CapabilityRequirements{
			ForbiddenAliases: []string{"openai_for_glm"},
		},
	}
	snap := capability.Snapshot{
		Credentials: []capability.CredentialAlias{"openai_for_glm", "internal_ci"},
	}
	got := New().Check(context.Background(), tc, snap)
	var fc int
	for _, b := range got {
		if b.Kind == KindForbiddenCred {
			fc++
			if b.Actual != "openai_for_glm" {
				t.Errorf("Actual: got %q want openai_for_glm", b.Actual)
			}
		}
	}
	if fc != 1 {
		t.Fatalf("expected 1 forbidden_cred block; got %d (all: %+v)", fc, got)
	}
}

// §7(e) THE CRITICAL BYPASS MATRIX: NONE of these variants may match
// `openai_for_glm`. If they do, attacker gets forbidden creds through
// by renaming.
func TestCheckForbiddenCred_ExactMatch_RejectsBypasses(t *testing.T) {
	tc := contract.TaskContract{
		CapabilityRequirements: contract.CapabilityRequirements{
			ForbiddenAliases: []string{"openai_for_glm"},
		},
	}
	cases := []capability.CredentialAlias{
		"openai_for_glm_backup",  // suffix bypass
		"real_openai_for_glm",    // prefix bypass
		"my_openai_for_glm_prod", // sandwich bypass
	}
	for _, c := range cases {
		t.Run(string(c), func(t *testing.T) {
			snap := capability.Snapshot{Credentials: []capability.CredentialAlias{c}}
			got := New().Check(context.Background(), tc, snap)
			for _, b := range got {
				if b.Kind == KindForbiddenCred {
					t.Errorf("§7(e) regression: exact-match forbidden_cred wrongly flagged %q against forbidden %q", c, "openai_for_glm")
				}
			}
		})
	}
}

// §7(e) CASE-SENSITIVITY regression bait: forbidden alias is
// well-formed lowercase (passes NewCredentialAlias), snapshot carries
// a case-different variant (CredentialAlias is a string type; the
// snapshot side can be hand-built past NewCredentialAlias's regex
// guard). An implementation using strings.EqualFold would flag this
// as forbidden; the spec §7(e) requires exact `==` and therefore NO
// block. If we ever get a KindForbiddenCred block here, someone
// swapped `==` for `EqualFold` in checkForbiddenCred.
func TestCheckForbiddenCred_CaseSensitiveAgainstEqualFoldRegression(t *testing.T) {
	tc := contract.TaskContract{
		CapabilityRequirements: contract.CapabilityRequirements{
			// Well-formed lowercase alias (matches ^[a-z][a-z0-9_]{2,63}$).
			ForbiddenAliases: []string{"openai_for_glm"},
		},
	}
	// CredentialAlias is a plain string type. A hand-crafted snapshot
	// (bypassing NewCredentialAlias, which the WT-1 spec permits for
	// direct construction) can carry ANY string; the validator must
	// still evaluate the CASE-SENSITIVE == against it, so an operator
	// who accidentally logged mixed-case credentials still sees them
	// as non-matches against a lowercase forbidden entry.
	cases := []capability.CredentialAlias{
		"Openai_For_Glm", // fully case-flipped
		"OPENAI_FOR_GLM", // uppercase
		"OpenAI_for_GLM", // mixed
	}
	for _, c := range cases {
		t.Run(string(c), func(t *testing.T) {
			snap := capability.Snapshot{Credentials: []capability.CredentialAlias{c}}
			got := New().Check(context.Background(), tc, snap)
			for _, b := range got {
				if b.Kind == KindForbiddenCred {
					t.Errorf("§7(e) regression: case-insensitive match — EqualFold in use? snapshot=%q forbidden=%q block=%+v",
						c, "openai_for_glm", b)
				}
			}
		})
	}
}

// Malformed alias in contract: NewCredentialAlias would reject it, but
// the contract's ForbiddenAliases is a plain []string that operators
// hand-author. checkForbiddenCred surfaces this as an operator-facing
// block, not a silent pass.
func TestCheckForbiddenCred_MalformedContractAlias(t *testing.T) {
	tc := contract.TaskContract{
		CapabilityRequirements: contract.CapabilityRequirements{
			ForbiddenAliases: []string{"NotAValidAlias!"}, // fails aliasShapeRe
		},
	}
	got := New().Check(context.Background(), tc, capability.Snapshot{})
	var fc int
	for _, b := range got {
		if b.Kind == KindForbiddenCred {
			fc++
			if b.Actual != "<malformed alias>" {
				t.Errorf("Actual sentinel: got %q", b.Actual)
			}
		}
	}
	if fc != 1 {
		t.Fatalf("expected 1 malformed-alias block; got %d", fc)
	}
}

func TestCheckForbiddenCred_NoBlock_WhenAbsent(t *testing.T) {
	tc := contract.TaskContract{
		CapabilityRequirements: contract.CapabilityRequirements{
			ForbiddenAliases: []string{"openai_for_glm"},
		},
	}
	snap := capability.Snapshot{Credentials: []capability.CredentialAlias{"internal_ci"}}
	for _, b := range New().Check(context.Background(), tc, snap) {
		if b.Kind == KindForbiddenCred {
			t.Errorf("unexpected forbidden_cred block: %+v", b)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go test ./internal/contract/validator/ -run TestCheckForbiddenCred -count=1 -race
```

Expected: FAIL — `checkForbiddenCred` not wired.

- [ ] **Step 3: Implement `checkForbiddenCred`**

Append to `validator.go`:

```go
// checkForbiddenCred — spec §3.3 + §7(e). Exact case-sensitive string
// equality. Any substring / prefix / case-insensitive comparison would
// let an attacker rename a forbidden alias with a suffix and bypass
// the check.
func checkForbiddenCred(tc contract.TaskContract, snap capability.Snapshot) []Block {
	var out []Block
	for i, want := range tc.CapabilityRequirements.ForbiddenAliases {
		field := fmt.Sprintf("capability_requirements.forbidden_aliases[%d]", i)

		// §7(e): validate the contract-side alias shape so operators
		// get an actionable block instead of a silent pass.
		if _, err := capability.NewCredentialAlias(want); err != nil {
			out = append(out, newBlock(KindForbiddenCred, field, want, "<malformed alias>"))
			continue
		}

		for _, have := range snap.Credentials {
			// EXACT MATCH ONLY. Do not touch: strings.HasPrefix,
			// strings.Contains, strings.EqualFold. See §7(e).
			if string(have) == want {
				out = append(out, newBlock(KindForbiddenCred, field, "<absent>", string(have)))
				break // one block per forbidden entry is enough
			}
		}
	}
	return out
}
```

Wire into `Check`:

```go
func (staticValidator) Check(_ context.Context, tc contract.TaskContract, snap capability.Snapshot) []Block {
	var blocks []Block
	blocks = append(blocks, checkMissingFile(tc, snap)...)
	blocks = append(blocks, checkWrongVersion(tc, snap)...)
	blocks = append(blocks, checkForbiddenCred(tc, snap)...)
	// Task 6 appends here.
	return blocks
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go test ./internal/contract/validator/ -count=1 -race
```

Expected: PASS for all `TestCheckForbiddenCred*`; earlier tests still PASS.

- [ ] **Step 5: Commit**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator
git add multi-agent/internal/contract/validator/
git commit -m "$(cat <<'EOF'
WT-2-dry-run-validator: checkForbiddenCred (§3.3, §7(e))

Exact case-sensitive string equality; bypass matrix
(openai_for_glm_backup / real_openai_for_glm / my_openai_for_glm_prod)
must NOT match. Malformed contract alias surfaces as a block with
Actual="<malformed alias>".

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: `checkPolicyViolation` (TDD)

**Files:**
- Modify: `multi-agent/internal/contract/validator/validator.go` (add `checkPolicyViolation`, `reachRank`, wire into `Check`)
- Modify: `multi-agent/internal/contract/validator/validator_test.go`

**Interfaces:**
- Consumes: `contract.ExecutionPolicy.RequiredReach`, `capability.Snapshot.Network`.
- Produces: `func checkPolicyViolation(tc contract.TaskContract, snap capability.Snapshot) []Block`; `func reachRank(r capability.NetworkReach) int` — private helper.

**Semantic recap (spec §3.4):** ladder `none(0) < loopback-only(1) < intranet(2) < internet(3)`. Empty `RequiredReach` ⇒ no constraint. Snapshot reach `< RequiredReach` ⇒ block. Snapshot reach OUTSIDE the enum ⇒ block (defensive).

- [ ] **Step 1: Write the failing tests**

Append to `validator_test.go`:

```go
func TestCheckPolicyViolation_EmitsBlock_WhenSnapshotReachTooLow(t *testing.T) {
	tc := contract.TaskContract{
		ExecutionPolicy: contract.ExecutionPolicy{RequiredReach: capability.NetworkInternet},
	}
	snap := capability.Snapshot{Network: capability.NetworkIntranet}
	got := New().Check(context.Background(), tc, snap)
	var pv int
	for _, b := range got {
		if b.Kind == KindPolicyViolation {
			pv++
			if b.Expected != string(capability.NetworkInternet) {
				t.Errorf("Expected: got %q", b.Expected)
			}
			if b.Actual != string(capability.NetworkIntranet) {
				t.Errorf("Actual: got %q", b.Actual)
			}
		}
	}
	if pv != 1 {
		t.Fatalf("expected 1 policy_violation block; got %d", pv)
	}
}

func TestCheckPolicyViolation_NoBlock_WhenEqualOrGreater(t *testing.T) {
	tc := contract.TaskContract{
		ExecutionPolicy: contract.ExecutionPolicy{RequiredReach: capability.NetworkLoopbackOnly},
	}
	for _, snapReach := range []capability.NetworkReach{
		capability.NetworkLoopbackOnly, capability.NetworkIntranet, capability.NetworkInternet,
	} {
		t.Run(string(snapReach), func(t *testing.T) {
			snap := capability.Snapshot{Network: snapReach}
			for _, b := range New().Check(context.Background(), tc, snap) {
				if b.Kind == KindPolicyViolation {
					t.Errorf("unexpected policy_violation block: %+v", b)
				}
			}
		})
	}
}

func TestCheckPolicyViolation_NoConstraint_WhenRequiredReachEmpty(t *testing.T) {
	tc := contract.TaskContract{ExecutionPolicy: contract.ExecutionPolicy{}}
	snap := capability.Snapshot{Network: capability.NetworkNone}
	for _, b := range New().Check(context.Background(), tc, snap) {
		if b.Kind == KindPolicyViolation {
			t.Errorf("empty RequiredReach must impose no constraint; got %+v", b)
		}
	}
}

// Defensive: hand-crafted snapshot bypasses NewSnapshot invariants
// (invalid NetworkReach). Validator must reject it as a policy
// violation instead of silently letting a garbage host through.
func TestCheckPolicyViolation_UnknownReach_Blocks(t *testing.T) {
	tc := contract.TaskContract{
		ExecutionPolicy: contract.ExecutionPolicy{RequiredReach: capability.NetworkIntranet},
	}
	snap := capability.Snapshot{Network: capability.NetworkReach("worldwide")}
	got := New().Check(context.Background(), tc, snap)
	var pv int
	for _, b := range got {
		if b.Kind == KindPolicyViolation {
			pv++
			if b.Actual != "worldwide" {
				t.Errorf("Actual: got %q want worldwide", b.Actual)
			}
		}
	}
	if pv != 1 {
		t.Fatalf("expected 1 policy_violation block; got %d", pv)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go test ./internal/contract/validator/ -run TestCheckPolicyViolation -count=1 -race
```

Expected: FAIL — `checkPolicyViolation` not wired.

- [ ] **Step 3: Implement `checkPolicyViolation` + `reachRank`**

Append to `validator.go`:

```go
// checkPolicyViolation — spec §3.4. Single check: snapshot network
// reach must be AT LEAST tc.ExecutionPolicy.RequiredReach on the
// semantic ladder none < loopback-only < intranet < internet.
func checkPolicyViolation(tc contract.TaskContract, snap capability.Snapshot) []Block {
	req := tc.ExecutionPolicy.RequiredReach
	if req == "" {
		return nil
	}
	reqRank := reachRank(req)
	if reqRank < 0 {
		// The contract itself carries an unknown reach — this is a
		// contract bug, but surface it as a policy_violation so the
		// operator fixes it instead of silently passing.
		return []Block{newBlock(
			KindPolicyViolation,
			"execution_policy.required_reach",
			string(req),
			"<contract required_reach unknown>",
		)}
	}
	gotRank := reachRank(snap.Network)
	if gotRank < 0 {
		return []Block{newBlock(
			KindPolicyViolation,
			"execution_policy.required_reach",
			string(req),
			string(snap.Network),
		)}
	}
	if gotRank < reqRank {
		return []Block{newBlock(
			KindPolicyViolation,
			"execution_policy.required_reach",
			string(req),
			string(snap.Network),
		)}
	}
	return nil
}

// reachRank returns the semantic-ladder position of a NetworkReach.
// Returns -1 for values outside the WT-1 enum (see spec §3.4 last
// bullet).
func reachRank(r capability.NetworkReach) int {
	switch r {
	case capability.NetworkNone:
		return 0
	case capability.NetworkLoopbackOnly:
		return 1
	case capability.NetworkIntranet:
		return 2
	case capability.NetworkInternet:
		return 3
	default:
		return -1
	}
}
```

Wire into `Check`:

```go
func (staticValidator) Check(_ context.Context, tc contract.TaskContract, snap capability.Snapshot) []Block {
	var blocks []Block
	blocks = append(blocks, checkMissingFile(tc, snap)...)
	blocks = append(blocks, checkWrongVersion(tc, snap)...)
	blocks = append(blocks, checkForbiddenCred(tc, snap)...)
	blocks = append(blocks, checkPolicyViolation(tc, snap)...)
	return blocks
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go test ./internal/contract/validator/ -count=1 -race
```

Expected: PASS for all `TestCheckPolicyViolation*`; earlier tests still PASS.

- [ ] **Step 5: Commit**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator
git add multi-agent/internal/contract/validator/
git commit -m "$(cat <<'EOF'
WT-2-dry-run-validator: checkPolicyViolation (§3.4)

Semantic-ladder rank (none<loopback-only<intranet<internet); unknown
reach (contract- or snapshot-side) surfaces as a block instead of a
silent pass. Empty RequiredReach = no constraint.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: Composite validator checkpoint (§4 acceptance)

**Files:**
- Test only: `multi-agent/internal/contract/validator/validator_test.go`

**Interfaces:**
- Consumes: `New()` (Tasks 2–6 populated the four checks).
- Produces: nothing — this is a checkpoint that asserts the four-in-one contract.

Task 2's `newBlock()` already enforced the `maxDetailBytes` cap; Tasks 3–6 wired the four checks into `staticValidator.Check` in the spec's deterministic order (missing_file, wrong_version, forbidden_cred, policy_violation). Task 7 is the checkpoint that asserts BOTH: the four checks are all reachable through `New().Check(...)` AND they emit blocks in the documented order when all four fire on one dry-run.

- [ ] **Step 1: Write the checkpoint test**

Append to `validator_test.go`:

```go
// Composite: one inject per class, all firing. Assert (a) count == 4,
// (b) deterministic order matches spec §2.
func TestNew_AllFourChecksFire_InOrder(t *testing.T) {
	tc := contract.TaskContract{
		DataContract: contract.DataContract{
			ReadArtifacts: []contract.ArtifactRef{{Kind: "file", Name: "missing.yaml"}},
		},
		CapabilityRequirements: contract.CapabilityRequirements{
			ToolRequirements: []contract.ToolRequirement{{Name: "go", MinVersion: "1.22.0"}},
			ForbiddenAliases: []string{"openai_for_glm"},
		},
		ExecutionPolicy: contract.ExecutionPolicy{RequiredReach: capability.NetworkInternet},
	}
	snap := capability.Snapshot{
		OS: "linux", Arch: "amd64",
		Network:     capability.NetworkIntranet,
		Tools:       []capability.ToolVersion{{Name: "go", Version: "1.18.4"}},
		Credentials: []capability.CredentialAlias{"openai_for_glm"},
		// No Files → missing_file fires.
	}
	got := New().Check(context.Background(), tc, snap)
	if len(got) != 4 {
		t.Fatalf("expected 4 blocks (one per class); got %d: %+v", len(got), got)
	}
	wantOrder := []Kind{KindMissingFile, KindWrongVersion, KindForbiddenCred, KindPolicyViolation}
	for i, w := range wantOrder {
		if got[i].Kind != w {
			t.Errorf("blocks[%d].Kind: got %q want %q", i, got[i].Kind, w)
		}
	}
	// All must be severity=block.
	for _, b := range got {
		if b.Severity != SeverityBlock {
			t.Errorf("Severity: got %q want %q", b.Severity, SeverityBlock)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they pass**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go test ./internal/contract/validator/ -count=1 -race -shuffle=on
```

Expected: PASS (no new production code needed — this asserts what Tasks 2–6 already wired).

- [ ] **Step 3: Commit**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator
git add multi-agent/internal/contract/validator/validator_test.go
git commit -m "$(cat <<'EOF'
WT-2-dry-run-validator: composite validator checkpoint (§4)

Asserts New().Check(...) fires all four classes with deterministic
ordering (missing_file, wrong_version, forbidden_cred,
policy_violation) on a fully-blocked contract.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: `NoDryRun` ablation registration

**Files:**
- Create: `multi-agent/internal/contract/validator/ablation.go`
- Create: `multi-agent/internal/contract/validator/ablation_test.go`

**Interfaces:**
- Consumes: `ablation.Default`, `ablation.NoDryRun` FlagName (already declared in `internal/ablation/registry.go`).
- Produces:
  - `func IsDryRunDisabled() bool` — accessor read by the driver's `dryRunContractTool`.
  - `func SetDryRunDisabled(v bool)` — test-only mutator (matches the WT-1 `capability.SetDisableUpload` pattern).

- [ ] **Step 1: Write the failing tests**

Create `multi-agent/internal/contract/validator/ablation_test.go`:

```go
package validator

import (
	"testing"

	"github.com/yourorg/multi-agent/internal/ablation"
)

// The init() in ablation.go must have registered NoDryRun against
// disableDryRun; List() from any test that imports validator must
// include NoDryRun.
func TestNoDryRun_RegisteredAtInit(t *testing.T) {
	found := false
	for _, name := range ablation.Default.List() {
		if name == ablation.NoDryRun {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("NoDryRun not registered in ablation.Default; init() failed")
	}
}

// SetByName("NoDryRun", true) must flip IsDryRunDisabled() to true; and
// SetByName back to false must flip it back. Serial-only test — no
// t.Parallel — the ablation contract requires pre-run-only mutation.
func TestNoDryRun_ToggleReflectsInAccessor(t *testing.T) {
	// Save and restore to keep other tests in this package clean.
	prev := IsDryRunDisabled()
	t.Cleanup(func() { SetDryRunDisabled(prev) })

	if err := ablation.Default.SetByName(string(ablation.NoDryRun), true); err != nil {
		t.Fatalf("SetByName(true): %v", err)
	}
	if !IsDryRunDisabled() {
		t.Errorf("IsDryRunDisabled() = false after SetByName(true)")
	}
	if err := ablation.Default.SetByName(string(ablation.NoDryRun), false); err != nil {
		t.Fatalf("SetByName(false): %v", err)
	}
	if IsDryRunDisabled() {
		t.Errorf("IsDryRunDisabled() = true after SetByName(false)")
	}
}

// SetDryRunDisabled is the test-only shortcut. Assert it matches
// SetByName's effect so tests that use the shortcut don't drift from
// production semantics.
func TestSetDryRunDisabled_MatchesRegistryToggle(t *testing.T) {
	prev := IsDryRunDisabled()
	t.Cleanup(func() { SetDryRunDisabled(prev) })

	SetDryRunDisabled(true)
	if !IsDryRunDisabled() {
		t.Error("SetDryRunDisabled(true) failed to set")
	}
	SetDryRunDisabled(false)
	if IsDryRunDisabled() {
		t.Error("SetDryRunDisabled(false) failed to clear")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go test ./internal/contract/validator/ -run TestNoDryRun -count=1 -race
```

Expected: build errors — `IsDryRunDisabled` / `SetDryRunDisabled` undefined.

- [ ] **Step 3: Implement `ablation.go`**

Create `multi-agent/internal/contract/validator/ablation.go`:

```go
package validator

import (
	"log"

	"github.com/yourorg/multi-agent/internal/ablation"
)

// disableDryRun is the ablation flag's *bool target. Registered in
// init() below against ablation.NoDryRun. Readers MUST use
// IsDryRunDisabled — the ablation contract makes no concurrency
// guarantee about the raw variable (spec §7(d) + registry.go).
var disableDryRun bool

// IsDryRunDisabled reports whether the NoDryRun ablation flag is on.
// Called by driver.dryRunContractTool.Call to decide whether to
// short-circuit BEFORE invoking validator.New().Check(...). When true,
// the tool logs "[ablation] NoDryRun: skipped conversation=<id>",
// emits ZERO metric events, and returns Blocks: [] (spec §7(d)).
func IsDryRunDisabled() bool { return disableDryRun }

// SetDryRunDisabled is the test-only mutator. Production code MUST
// use ablation.Default.SetByName(...) — the CLI binder does this
// once, before the driver starts.
func SetDryRunDisabled(v bool) { disableDryRun = v }

func init() {
	if err := ablation.Default.Register(ablation.NoDryRun, &disableDryRun); err != nil {
		// Init-time panic would DoS the whole process before main
		// runs; the ablation contract says Register never panics. All
		// error modes here are programmer bugs — log loudly.
		log.Printf("validator: ablation registration failed: %v", err)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go test ./internal/contract/validator/ -count=1 -race
```

Expected: PASS for all `TestNoDryRun*` + `TestSetDryRunDisabled_MatchesRegistryToggle`; import-purity test still PASS (only added `log` + `ablation`, both allow-listed).

- [ ] **Step 5: Commit**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator
git add multi-agent/internal/contract/validator/ablation.go multi-agent/internal/contract/validator/ablation_test.go
git commit -m "$(cat <<'EOF'
WT-2-dry-run-validator: NoDryRun ablation registration (§7(d))

init() registers NoDryRun → disableDryRun *bool via ablation.Default.
IsDryRunDisabled() accessor read by dryRunContractTool (Task 13) to
short-circuit BEFORE Check(...) while writing the mandatory log line.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: `dry_run_blocks` schema DDL + writer scaffold

**Files:**
- Modify: `multi-agent/internal/observerstore/schema.sql` (append the CREATE TABLE + indexes)
- Create: `multi-agent/internal/observerstore/dry_run_blocks_writer.go` (types + interface + `NewDryRunBlockWriter` stub — INSERT lands in Task 10)
- Create: `multi-agent/internal/observerstore/dry_run_blocks_writer_test.go` (schema-loaded assertion)

**Interfaces:**
- Consumes: `secretscrub.Sanitize`, existing `OpenSQLite` (loads `schema.sql`).
- Produces:
  - `type DryRunBlockRow struct { BlockID, AttemptID, ConversationID, ExperimentID, ContractHash, CapabilitySnapshotHash, BlockKind, Field, Expected, Actual, Detail string; BlockedAt time.Time }`
  - `type DryRunBlockWriter interface { WriteDryRunBlock(ctx context.Context, r DryRunBlockRow) error }`
  - `func NewDryRunBlockWriter(db *sql.DB) DryRunBlockWriter`
  - Package const `const dryRunBlockMaxDetailBytes = 8192`.

- [ ] **Step 1: Write the failing test**

Create `multi-agent/internal/observerstore/dry_run_blocks_writer_test.go`:

```go
package observerstore

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// Schema check: dry_run_blocks table + indexes must be applied by
// OpenSQLite via the embedded schema.sql.
func TestDryRunBlocks_SchemaLoaded(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='dry_run_blocks'`).Scan(&name)
	if err != nil {
		t.Fatalf("dry_run_blocks table not applied by schema.sql: %v", err)
	}

	// Assert all three indexes are present.
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='dry_run_blocks' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := map[string]bool{
		"idx_dry_run_blocks_conv":          false,
		"idx_dry_run_blocks_contract_hash": false,
		"idx_dry_run_blocks_attempt":       false,
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("index %s not created", name)
		}
	}
}

// openTestDB opens an in-memory SQLite DB with the observer schema
// applied. If the test file infrastructure already provides one, this
// helper is a wrapper.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	store, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store.DB() // add DB() accessor if not present, or replace with existing test helper
}

// Writer scaffold present.
func TestDryRunBlockWriter_ConstructorReturnsNonNil(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewDryRunBlockWriter(db)
	if w == nil {
		t.Fatal("NewDryRunBlockWriter returned nil")
	}
	// Zero-row write must not panic; INSERT lands in Task 10 so an
	// empty row that returns an error is acceptable here.
	_ = w.WriteDryRunBlock(context.Background(), DryRunBlockRow{BlockedAt: time.Now()})
}
```

Note: if `openTestDB` conflicts with existing helpers or `SQLiteStore` lacks a `DB()` accessor, adapt to whatever the existing `store_test.go` uses (grep `openTestSQLite\|newTestStore` and reuse).

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go test ./internal/observerstore/ -run 'TestDryRunBlocks_SchemaLoaded|TestDryRunBlockWriter_ConstructorReturnsNonNil' -count=1
```

Expected: FAIL — table not created, writer type undefined.

- [ ] **Step 3: Append DDL to `schema.sql`**

Append to `multi-agent/internal/observerstore/schema.sql` (after the WT-1-capability-snapshot block, roughly line 260):

```sql

-- WT-2-dry-run-validator: §A3 four-class pre-execution block audit.
-- One row per Block returned by validator.Check(...) that the dry-run
-- tool persisted. Consumer view (spec §6.1) joins dry_run_blocks with
-- events (type='PreExecutionFaultCatchRate') for total-attempt
-- denominator; runs alone cannot supply it because blocked dispatches
-- never reach runs.
CREATE TABLE IF NOT EXISTS dry_run_blocks (
    block_id                 TEXT PRIMARY KEY,
    attempt_id               TEXT NOT NULL,
    conversation_id          TEXT NOT NULL,
    experiment_id            TEXT NOT NULL DEFAULT '',
    contract_hash            TEXT NOT NULL,
    capability_snapshot_hash TEXT NOT NULL,
    block_kind               TEXT NOT NULL
        CHECK (block_kind IN ('missing_file','wrong_version','forbidden_cred','policy_violation')),
    field                    TEXT NOT NULL DEFAULT '',
    expected                 TEXT NOT NULL DEFAULT '',
    actual                   TEXT NOT NULL DEFAULT '',
    detail                   TEXT NOT NULL DEFAULT '',
    blocked_at               TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_dry_run_blocks_conv
    ON dry_run_blocks(conversation_id, blocked_at);
CREATE INDEX IF NOT EXISTS idx_dry_run_blocks_contract_hash
    ON dry_run_blocks(contract_hash);
CREATE INDEX IF NOT EXISTS idx_dry_run_blocks_attempt
    ON dry_run_blocks(attempt_id);
```

- [ ] **Step 4: Create writer scaffold**

Create `multi-agent/internal/observerstore/dry_run_blocks_writer.go`:

```go
package observerstore

import (
	"context"
	"database/sql"
	"time"
)

// dryRunBlockMaxDetailBytes bounds the detail column at the writer
// boundary — defense-in-depth against a caller that skipped the
// validator's maxDetailBytes cap. See spec §7(c).
const dryRunBlockMaxDetailBytes = 8192

// DryRunBlockRow is the data shape persisted by NewDryRunBlockWriter.
// Mirrors validator.Block plus the identity + hash columns the
// consumer-view SELECT (spec §6.1) needs.
type DryRunBlockRow struct {
	BlockID                string
	AttemptID              string
	ConversationID         string
	ExperimentID           string
	ContractHash           string
	CapabilitySnapshotHash string
	BlockKind              string
	Field                  string
	Expected               string
	Actual                 string
	Detail                 string
	BlockedAt              time.Time
}

// DryRunBlockWriter writes one DryRunBlockRow per call. Implementations
// must be goroutine-safe.
type DryRunBlockWriter interface {
	WriteDryRunBlock(ctx context.Context, r DryRunBlockRow) error
}

type dryRunBlocksWriter struct{ db *sql.DB }

// NewDryRunBlockWriter returns a DryRunBlockWriter backed by db. The
// schema migration (CREATE TABLE IF NOT EXISTS dry_run_blocks) is
// applied by OpenSQLite via the embedded schema.sql.
//
// SQLite-only: uses `?` placeholders. See route_reasons_writer.go for
// the pg-follow-up-required rationale.
func NewDryRunBlockWriter(db *sql.DB) DryRunBlockWriter {
	return &dryRunBlocksWriter{db: db}
}

// WriteDryRunBlock — scaffold only. Task 10 implements the
// parameterized INSERT, secret scrub, and detail truncation.
func (w *dryRunBlocksWriter) WriteDryRunBlock(ctx context.Context, r DryRunBlockRow) error {
	return nil
}
```

If the test's `openTestDB` helper needs a `DB()` accessor and `SQLiteStore` lacks one, add a minimal accessor:

```go
// DB exposes the underlying *sql.DB for internal test / writer use.
func (s *SQLiteStore) DB() *sql.DB { return s.db }
```

(Only add if the existing store.go doesn't already expose one — grep first.)

- [ ] **Step 5: Run tests to verify they pass**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go test ./internal/observerstore/ -run 'TestDryRunBlocks_SchemaLoaded|TestDryRunBlockWriter_ConstructorReturnsNonNil' -count=1 -race
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator
git add multi-agent/internal/observerstore/schema.sql multi-agent/internal/observerstore/dry_run_blocks_writer.go multi-agent/internal/observerstore/dry_run_blocks_writer_test.go
# Also add store.go if a DB() accessor was needed:
# git add multi-agent/internal/observerstore/store.go
git commit -m "$(cat <<'EOF'
WT-2-dry-run-validator: dry_run_blocks schema + writer scaffold (§6)

- CREATE TABLE dry_run_blocks + 3 indexes (conv, contract_hash,
  attempt) in schema.sql
- DryRunBlockRow / DryRunBlockWriter / NewDryRunBlockWriter scaffold
- Placeholder INSERT lands in Task 10

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: Writer INSERT — parameterized, scrubbed, truncated

**Files:**
- Modify: `multi-agent/internal/observerstore/dry_run_blocks_writer.go` (replace placeholder `WriteDryRunBlock` body)
- Modify: `multi-agent/internal/observerstore/dry_run_blocks_writer_test.go` (add round-trip / ON CONFLICT / truncation / SQL-meta tests)

**Interfaces:** unchanged (Task 9 defined them).

- [ ] **Step 1: Write the failing tests**

Append to `dry_run_blocks_writer_test.go`:

```go
// Basic round-trip: insert one row, read it back with all columns intact.
func TestWriteDryRunBlock_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewDryRunBlockWriter(db)
	row := DryRunBlockRow{
		BlockID:                "blk-abc123",
		AttemptID:              "att-xyz789",
		ConversationID:         "conv-1",
		ExperimentID:           "exp-A",
		ContractHash:           "sha256:aaaa",
		CapabilitySnapshotHash: "sha256:bbbb",
		BlockKind:              "missing_file",
		Field:                  "data_contract.read_artifacts[0].name",
		Expected:               "config.yaml",
		Actual:                 "no snapshot file resource matches",
		Detail:                 "data_contract.read_artifacts[0].name: expected config.yaml, actual no snapshot file resource matches",
		BlockedAt:              time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC),
	}
	if err := w.WriteDryRunBlock(context.Background(), row); err != nil {
		t.Fatalf("write: %v", err)
	}
	var kind, field, expected, actual string
	err := db.QueryRow(`SELECT block_kind, field, expected, actual FROM dry_run_blocks WHERE block_id=?`, row.BlockID).
		Scan(&kind, &field, &expected, &actual)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if kind != row.BlockKind || field != row.Field || expected != row.Expected || actual != row.Actual {
		t.Errorf("round-trip mismatch:\n got  %+v\n want %+v", []string{kind, field, expected, actual}, []string{row.BlockKind, row.Field, row.Expected, row.Actual})
	}
}

// ON CONFLICT(block_id) DO NOTHING — repeated writes are idempotent.
func TestWriteDryRunBlock_IdempotentOnBlockID(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewDryRunBlockWriter(db)
	row := DryRunBlockRow{
		BlockID: "blk-dedup", AttemptID: "a1", ConversationID: "c1",
		ContractHash: "h1", CapabilitySnapshotHash: "s1",
		BlockKind: "policy_violation", BlockedAt: time.Now().UTC(),
	}
	if err := w.WriteDryRunBlock(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteDryRunBlock(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM dry_run_blocks WHERE block_id=?`, "blk-dedup").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected 1 row after two identical writes; got %d", n)
	}
}

// §7(c) writer truncates at 8 KiB regardless of upstream.
func TestWriteDryRunBlock_DetailTruncatedAt8KiB(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewDryRunBlockWriter(db)
	huge := make([]byte, 20*1024)
	for i := range huge {
		huge[i] = 'x'
	}
	row := DryRunBlockRow{
		BlockID: "blk-huge", AttemptID: "a1", ConversationID: "c1",
		ContractHash: "h1", CapabilitySnapshotHash: "s1",
		BlockKind: "wrong_version", Detail: string(huge),
		BlockedAt: time.Now().UTC(),
	}
	if err := w.WriteDryRunBlock(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := db.QueryRow(`SELECT detail FROM dry_run_blocks WHERE block_id=?`, "blk-huge").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) > dryRunBlockMaxDetailBytes {
		t.Errorf("writer failed to truncate detail: stored %d bytes > cap %d", len(stored), dryRunBlockMaxDetailBytes)
	}
}

// §7(c) SQL-meta chars in a caller-controlled column must round-trip
// verbatim through parameterized ? placeholders. A string-concat bug
// would either error or produce a corrupt row.
func TestWriteDryRunBlock_SQLMetaCharactersPersistVerbatim(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewDryRunBlockWriter(db)
	meta := `'; DROP TABLE dry_run_blocks; --`
	row := DryRunBlockRow{
		BlockID: "blk-meta", AttemptID: "a1", ConversationID: "c1",
		ContractHash: "h1", CapabilitySnapshotHash: "s1",
		BlockKind: "forbidden_cred",
		Field:     meta,
		Expected:  meta,
		Actual:    meta,
		Detail:    meta,
		BlockedAt: time.Now().UTC(),
	}
	if err := w.WriteDryRunBlock(context.Background(), row); err != nil {
		t.Fatalf("write: %v", err)
	}
	// If SQL-injection succeeded, the table would be gone.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM dry_run_blocks WHERE block_id=?`, "blk-meta").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("row not persisted (injection attempt broke schema?): n=%d", n)
	}
	// Optional: secretscrub might rewrite content; the row still exists
	// with the exact block_id we asked for.
}

// CI-conditional perf gate: WriteDryRunBlock should complete in < 5 ms
// on any modern SQLite backend. Skipped under -short or when CI is set
// (per §Global Constraints).
func TestWriteDryRunBlock_PerfBudget(t *testing.T) {
	if testing.Short() || os.Getenv("CI") != "" {
		t.Skip("perf assertion skipped in short/CI mode")
	}
	db := openTestDB(t)
	defer db.Close()
	w := NewDryRunBlockWriter(db)
	row := DryRunBlockRow{
		BlockID: "blk-perf", AttemptID: "a1", ConversationID: "c1",
		ContractHash: "h1", CapabilitySnapshotHash: "s1",
		BlockKind: "policy_violation", BlockedAt: time.Now().UTC(),
	}
	start := time.Now()
	if err := w.WriteDryRunBlock(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > 5*time.Millisecond {
		t.Errorf("write exceeded 5ms budget: %v", el)
	}
}
```

At the top of the test file, add:

```go
import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"
)
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go test ./internal/observerstore/ -run 'TestWriteDryRunBlock' -count=1 -race
```

Expected: FAIL — placeholder `WriteDryRunBlock` returns nil without inserting.

- [ ] **Step 3: Implement the writer body**

Replace the placeholder `WriteDryRunBlock` body in
`multi-agent/internal/observerstore/dry_run_blocks_writer.go`:

```go
import (
	"context"
	"database/sql"
	"time"

	"github.com/yourorg/multi-agent/internal/secretscrub"
)

// ... existing types unchanged ...

// insertDryRunBlockSQL is a constant, parameterized INSERT. ON
// CONFLICT(block_id) DO NOTHING supports idempotent retries by the
// driver-side best-effort writer.
const insertDryRunBlockSQL = `
INSERT INTO dry_run_blocks(
    block_id, attempt_id, conversation_id, experiment_id,
    contract_hash, capability_snapshot_hash, block_kind,
    field, expected, actual, detail, blocked_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(block_id) DO NOTHING;
`

func (w *dryRunBlocksWriter) WriteDryRunBlock(ctx context.Context, r DryRunBlockRow) error {
	// Defense-in-depth (§7(c)): truncate detail at the writer boundary
	// regardless of upstream. maxDetailBytes in validator.newBlock is
	// the primary cap; this is the last line of defence.
	if len(r.Detail) > dryRunBlockMaxDetailBytes {
		const sentinel = "<...truncated>"
		cut := dryRunBlockMaxDetailBytes - len(sentinel)
		if cut < 0 {
			cut = 0
		}
		r.Detail = r.Detail[:cut] + sentinel
	}
	// Scrub caller-controlled free-text columns. Mirrors
	// route_reasons_writer's defense: even though validator.newBlock
	// constrains inputs, a caller bypassing the validator (e.g. a
	// direct writer call) must not land raw secrets in the table.
	r.Field = secretscrub.Sanitize(r.Field)
	r.Expected = secretscrub.Sanitize(r.Expected)
	r.Actual = secretscrub.Sanitize(r.Actual)
	r.Detail = secretscrub.Sanitize(r.Detail)
	r.ConversationID = secretscrub.Sanitize(r.ConversationID)

	_, err := w.db.ExecContext(ctx, insertDryRunBlockSQL,
		r.BlockID, r.AttemptID, r.ConversationID, r.ExperimentID,
		r.ContractHash, r.CapabilitySnapshotHash, r.BlockKind,
		r.Field, r.Expected, r.Actual, r.Detail,
		r.BlockedAt.UTC().Format(time.RFC3339Nano),
	)
	return err
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go test ./internal/observerstore/ -count=1 -race
```

Expected: PASS for all `TestWriteDryRunBlock_*` (perf test skips under CI). Existing observerstore tests still PASS.

- [ ] **Step 5: Commit**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator
git add multi-agent/internal/observerstore/dry_run_blocks_writer.go multi-agent/internal/observerstore/dry_run_blocks_writer_test.go
git commit -m "$(cat <<'EOF'
WT-2-dry-run-validator: dry_run_blocks writer INSERT (§6, §7(c))

Parameterized INSERT with ON CONFLICT(block_id) DO NOTHING; detail
truncated at 8 KiB (defense-in-depth over validator.newBlock);
secretscrub.Sanitize on 5 free-text columns; SQL-meta round-trip
test guards against string-concat regressions.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 11: Consumer-view SELECT §6.1 numeric-result test

**Files:**
- Modify: `multi-agent/internal/observerstore/dry_run_blocks_writer_test.go`

**Interfaces:** none new — this task locks in the §6.1 consumer view against real seeded data.

**Semantic recap (spec §6.1 + §7(g)):** given N dry-run attempts, some blocked with `policy_violation` and some clean, compute `policy_blocked_attempts / total_attempts`. Denominator comes from `events` (type=`PreExecutionFaultCatchRate`, fires on every attempt). Test seeds 5 attempts (2 with policy_violation blocks) and asserts the SELECT returns 0.4.

- [ ] **Step 1: Write the failing test**

Append to `dry_run_blocks_writer_test.go`:

```go
// §7(g) reverse audit: given only dry_run_blocks + events, the §6.1
// SELECT must return the correct PolicyViolationPreventionRate.
// Seeded shape: 5 dry-run attempts, 2 with policy_violation, 1 with
// wrong_version, 2 clean. Expected rate = 2/5 = 0.4.
func TestConsumerView_PolicyViolationPreventionRate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewDryRunBlockWriter(db)
	now := time.Now().UTC()

	seed := []struct {
		attemptID, blockKind string
	}{
		{"att-1", "policy_violation"},
		{"att-2", "policy_violation"},
		{"att-3", "wrong_version"},
		// att-4 and att-5: clean (only events, no blocks).
	}
	for i, s := range seed {
		if err := w.WriteDryRunBlock(context.Background(), DryRunBlockRow{
			BlockID: "blk-" + s.attemptID + "-" + s.blockKind, AttemptID: s.attemptID,
			ConversationID: "conv-" + s.attemptID, ExperimentID: "exp-1",
			ContractHash: "h1", CapabilitySnapshotHash: "s1",
			BlockKind: s.blockKind, BlockedAt: now,
		}); err != nil {
			t.Fatalf("seed block %d: %v", i, err)
		}
	}

	// Seed 5 PreExecutionFaultCatchRate events (denominator).
	for _, id := range []string{"att-1", "att-2", "att-3", "att-4", "att-5"} {
		payload := `{"attempt_id":"` + id + `","numerator":0,"blocks_total":0,"contract_hash":"h1","experiment_id":"exp-1"}`
		_, err := db.Exec(`INSERT INTO events(event_id, ts, workspace_id, agent_id, agent_role, type, task_id, payload)
			VALUES(?, ?, 'ws', 'a', 'driver', ?, '', ?)`,
			"ev-"+id, now.Format(time.RFC3339Nano), "PreExecutionFaultCatchRate", payload)
		if err != nil {
			t.Fatalf("seed event %s: %v", id, err)
		}
	}

	// The §6.1 SELECT (unified form): numerator = DISTINCT attempts
	// with a policy_violation block; denominator = DISTINCT attempts
	// observed as PreExecutionFaultCatchRate events.
	const q = `
WITH policy_blocked AS (
    SELECT DISTINCT attempt_id FROM dry_run_blocks WHERE block_kind='policy_violation'
),
all_attempts AS (
    SELECT DISTINCT json_extract(payload, '$.attempt_id') AS attempt_id
    FROM events WHERE type='PreExecutionFaultCatchRate'
)
SELECT (SELECT COUNT(*) FROM policy_blocked) * 1.0 /
       NULLIF((SELECT COUNT(*) FROM all_attempts), 0);
`
	var rate sql.NullFloat64
	if err := db.QueryRow(q).Scan(&rate); err != nil {
		t.Fatalf("consumer SELECT: %v", err)
	}
	if !rate.Valid {
		t.Fatal("SELECT returned NULL")
	}
	if got, want := rate.Float64, 0.4; got != want {
		t.Errorf("PolicyViolationPreventionRate: got %v want %v", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Only fails if the writer INSERT is broken; passes if Task 10 is correct AND the events table accepts direct INSERTs.

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go test ./internal/observerstore/ -run TestConsumerView_PolicyViolationPreventionRate -count=1 -race
```

Expected: this test may pass immediately if Task 10 landed clean; if the events-direct INSERT fails, adjust the column list to match `store.go:686` (`event_id, ts, workspace_id, agent_id, agent_role, type, task_id, parent_task_id, subtask_id, child_task_id, summary, subtask_summary, status, target_agent_id, target_role, mcp_server_name, mcp_tools, payload`) — the omitted columns are `NULL`-tolerant so a short INSERT with just the required NOT NULL cols works.

- [ ] **Step 3: Commit**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator
git add multi-agent/internal/observerstore/dry_run_blocks_writer_test.go
git commit -m "$(cat <<'EOF'
WT-2-dry-run-validator: consumer-view SELECT round-trip (§6.1, §7(g))

Seeded fixture (5 attempts, 2 policy_violation) → SELECT returns 0.4;
proves the schema (attempt_id in dry_run_blocks + events payload)
enables reverse-audit without touching runs.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 12: Extend `dryRunContractTool` — schema + snapshot parse + validator wire

**Files:**
- Modify: `multi-agent/internal/driver/capability_tools.go` (add `capability_snapshot` field to `dryRunContractTool.InputSchema`, parse in `Call`, thread through to `validator.Check`, add `AttemptID` + `Blocks` to `dryRunReport`)
- Modify: `multi-agent/internal/driver/capability_tools_test.go` (basic snapshot-in / blocks-out test)

**Interfaces:**
- Consumes: `validator.New()`, `validator.Block`, `capability.NewSnapshot`, `capability.Snapshot`, `capability.ComputeHash`.
- Produces:
  - `dryRunReport.Blocks []validator.Block` field (JSON tag `blocks`).
  - `dryRunReport.AttemptID string` field (JSON tag `attempt_id`).
  - `dryRunReport.Runnable` is AND-ed with `len(Blocks) == 0`.

- [ ] **Step 1: Write the failing test**

Add to `multi-agent/internal/driver/capability_tools_test.go` (or a new
`dry_run_validator_integration_test.go` if the existing file is
already large — `wc -l capability_tools_test.go` first). For this plan
we append; adjust if the file is > 800 lines:

```go
// dry-run tool: caller passes a capability_snapshot; a missing_file
// block flows through the Blocks slice.
func TestDryRunContractTool_SnapshotIn_BlocksOut(t *testing.T) {
	// tools_test.go:80 — real signature is newTestTools(t, sdk SDKClient) *Tools.
	// dryRunContractTool.Call invokes DiscoverAgents; supply a no-op
	// discoverFunc so the fakeSDK doesn't nil-panic.
	tools := newTestTools(t, &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) { return nil, nil },
	})
	tc := contract.TaskContract{
		Version:        contract.Version,
		ConversationID: "conv-test-1",
		Intent:         contract.IntentSpec{Goal: "read config", SuccessCriteria: []string{"ok"}},
		DataContract: contract.DataContract{
			ReadArtifacts: []contract.ArtifactRef{{Kind: "file", Name: "missing.yaml"}},
			WriteTargets:  []contract.WriteTarget{{Type: contract.WriteTargetArtifact, Kind: "document", Name: "out.md"}},
		},
		RecoveryHint:    "read missing.yaml; retry idempotent",
		ExecutionPolicy: contract.ExecutionPolicy{Routing: contract.RoutingMasterOnly},
		// contract.Validate() requires capability_requirements to be
		// "present" via the §2.2 #5 sub-field rule. Ship a non-nil
		// Skills slice so the check passes and the test can reach the
		// validator invocation.
		CapabilityRequirements: contract.CapabilityRequirements{
			Skills: []string{},
			Tools:  []string{},
		},
	}
	tc.ApplyDefaults()

	snap := capability.Snapshot{
		OS: "linux", Arch: "amd64", Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
		Network: capability.NetworkInternet,
	}
	// Full args: contract + snapshot.
	args, err := json.Marshal(map[string]interface{}{
		"contract":           tc,
		"capability_snapshot": snap,
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := (&dryRunContractTool{t: tools}).Call(context.Background(), args)
	if err != nil {
		t.Fatalf("dry_run_contract Call: %v", err)
	}
	var report dryRunReport
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if len(report.Blocks) == 0 {
		t.Fatalf("expected at least 1 block; got %+v", report)
	}
	found := false
	for _, b := range report.Blocks {
		if b.Kind == validator.KindMissingFile {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a missing_file block; got %+v", report.Blocks)
	}
	if report.AttemptID == "" {
		t.Errorf("AttemptID must be non-empty")
	}
	if report.Runnable {
		t.Errorf("Runnable must be false when Blocks is non-empty")
	}
}
```

Add imports at the top of the test file if missing: `"context"`, `"encoding/json"`, `"testing"`, `"github.com/yourorg/multi-agent/internal/capability"`, `"github.com/yourorg/multi-agent/internal/commandiface"`, `"github.com/yourorg/multi-agent/internal/contract"`, `"github.com/yourorg/multi-agent/internal/contract/validator"`.

If `newTestTools(t)` doesn't exist, use whatever helper the existing capability_tools_test.go uses (`grep -n "func newTest\|Tools{" capability_tools_test.go`).

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go test ./internal/driver/ -run TestDryRunContractTool_SnapshotIn_BlocksOut -count=1 -race
```

Expected: FAIL — `dryRunReport` has no `Blocks`/`AttemptID` field.

- [ ] **Step 3: Extend `dryRunContractTool`**

Modify `multi-agent/internal/driver/capability_tools.go`:

1. Extend imports:

```go
import (
	// existing imports ...
	"github.com/yourorg/multi-agent/internal/contract/validator"
)
```

2. Update the schema (§4.1):

```go
func (d *dryRunContractTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"contract":{"type":"object"},
			"capability_snapshot":{"type":"object"}
		},
		"required":["contract"]
	}`)
}
```

3. Extend `dryRunReport`:

```go
type dryRunReport struct {
	Runnable              bool              `json:"runnable"`
	RecommendedRoute      string            `json:"recommended_route"`
	RecommendedTargetID   string            `json:"recommended_target_id,omitempty"`
	RecommendedTargetName string            `json:"recommended_target_display_name,omitempty"`
	RecommendedSkill      string            `json:"recommended_skill,omitempty"`
	SatisfiedTools        []string          `json:"satisfied_tools"`
	MissingTools          []string          `json:"missing_tools"`
	MissingSkills         []string          `json:"missing_skills"`
	MissingResources      json.RawMessage   `json:"missing_resources,omitempty"`
	Reasons               []string          `json:"reasons"`
	Blocks                []validator.Block `json:"blocks"`
	AttemptID             string            `json:"attempt_id"`
}
```

4. Rewrite `Call` to (a) parse the optional snapshot, (b) invoke the validator, (c) attach blocks + attempt_id, (d) AND-in the runnable gate:

```go
func (d *dryRunContractTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Contract           contract.TaskContract `json:"contract"`
		CapabilitySnapshot json.RawMessage       `json:"capability_snapshot"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error(), Category: observerstore.FailContractViolation}
	}
	tc := args.Contract
	tc.ApplyDefaults()
	if err := tc.Validate(); err != nil {
		return nil, &MCPToolError{Message: "invalid contract: " + err.Error(), Category: observerstore.FailContractViolation}
	}
	cards, err := d.t.sdk.DiscoverAgents(ctx)
	if err != nil {
		return nil, &MCPToolError{Message: "discover agents: " + err.Error(), Category: observerstore.FailUnknown}
	}
	report := analyzeContractCapabilities(cards, d.t.cfg.Credentials.SandboxID, tc)
	report.AttemptID = randomHex(16)

	// Task 13 wires the ablation short-circuit + event emit + block
	// persistence around this call. For Task 12 we only run the pure
	// validator and attach results.
	if len(args.CapabilitySnapshot) > 0 {
		var snapSpec capability.Snapshot
		if err := json.Unmarshal(args.CapabilitySnapshot, &snapSpec); err != nil {
			return nil, &MCPToolError{Message: "invalid capability_snapshot: " + err.Error(), Category: observerstore.FailContractViolation}
		}
		snap, err := capability.NewSnapshot(snapSpec)
		if err != nil {
			return nil, &MCPToolError{Message: "capability_snapshot: " + err.Error(), Category: observerstore.FailContractViolation}
		}
		report.Blocks = validator.New().Check(ctx, tc, snap)
		if len(report.Blocks) > 0 {
			report.Runnable = false
		}
	}
	return json.Marshal(report)
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go test ./internal/driver/ -count=1 -race
```

Expected: PASS for `TestDryRunContractTool_SnapshotIn_BlocksOut`; existing tests unaffected (schema change is additive; snapshot omission preserves prior behaviour).

- [ ] **Step 5: Commit**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator
git add multi-agent/internal/driver/capability_tools.go multi-agent/internal/driver/capability_tools_test.go
git commit -m "$(cat <<'EOF'
WT-2-dry-run-validator: dryRunContractTool wires validator (§4.1, §4.2)

Adds optional capability_snapshot arg; on presence, capability.NewSnapshot
validates it and validator.New().Check runs; blocks + attempt_id flow
back in dryRunReport. Runnable AND-ed with len(Blocks)==0. Absent
snapshot preserves the pre-WT-2 recommended_route behaviour.

Task 13 wires the ablation short-circuit + event emit + persistence.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 13: Emit 3 metric events + persist blocks; wire ablation short-circuit

**Files:**
- Modify: `multi-agent/internal/driver/capability_tools.go` (add `emitDryRunMetrics`, `persistDryRunBlocks`, ablation short-circuit, `extractExperimentID`)
- Modify: `multi-agent/internal/driver/capability_tools_test.go` (assert event count, block persistence, ablation bypass log)

**Interfaces:**
- Consumes: `t.emit(observer.Event)` (already wired), `NewDryRunBlockWriter` (Task 10), `IsDryRunDisabled()` (Task 8), `capability.ComputeHash`, and a local `contractHash(tc contract.TaskContract) string` helper (see Step 3 — `sha256(json.Marshal(tc))`; TaskContract has stable field order in its struct declaration, so `json.Marshal` is deterministic across equivalent contracts).
- Produces:
  - `func (t *Tools) emitDryRunMetric(ctx context.Context, kind, countKey, attemptID, contractHash, experimentID string, numerator, count int)` — private helper. `countKey` names the metric's class-specific count field (`blocks_total` / `missing_files` / `policy_violations`) per spec §5 payload table.
  - `func persistDryRunBlocks(ctx, writer, row DryRunBlockRow, blocks []validator.Block)` — private helper.
  - `func extractExperimentID(tc contract.TaskContract) string` — private helper; today returns `""` (contract doesn't yet carry `experiment_id`; the eval runner passes it via env / caller extension in a later WT).

- [ ] **Step 1: Write the failing tests**

Append to `capability_tools_test.go`:

```go
// After a dry-run with any snapshot input, exactly 3 metric events
// land in the observer with the correct types + attempt_id.
func TestDryRunContractTool_EmitsThreeMetricEvents(t *testing.T) {
	tools, sink := newTestToolsWithSink(t)
	// A clean dry-run (no blocks). All three events must still fire
	// with numerator=0.
	tc := makeMinimalValidContract(t)
	snap := capability.Snapshot{OS: "linux", Arch: "amd64", Platform: commandiface.Platform{OS: "linux", Arch: "amd64"}, Network: capability.NetworkInternet}
	args, _ := json.Marshal(map[string]interface{}{"contract": tc, "capability_snapshot": snap})
	if _, err := (&dryRunContractTool{t: tools}).Call(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	// sink.Events() returns everything emit()'d. Filter by our three types.
	counts := map[string]int{}
	var attemptID string
	for _, ev := range sink.Events() {
		switch ev.Type {
		case "PreExecutionFaultCatchRate", "MissingArtifactDetectionRate", "PolicyViolationPreventionRate":
			counts[ev.Type]++
			// Payload attempt_id must be identical across the three events.
			var p struct {
				AttemptID string `json:"attempt_id"`
				Numerator int    `json:"numerator"`
			}
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if attemptID == "" {
				attemptID = p.AttemptID
			} else if p.AttemptID != attemptID {
				t.Errorf("attempt_id mismatch across metric events: %q vs %q", p.AttemptID, attemptID)
			}
			// Clean run: numerator must be 0.
			if p.Numerator != 0 {
				t.Errorf("clean run event %s numerator: got %d want 0", ev.Type, p.Numerator)
			}
		}
	}
	if got, want := counts["PreExecutionFaultCatchRate"], 1; got != want {
		t.Errorf("PreExecutionFaultCatchRate count: got %d want %d", got, want)
	}
	if got, want := counts["MissingArtifactDetectionRate"], 1; got != want {
		t.Errorf("MissingArtifactDetectionRate count: got %d want %d", got, want)
	}
	if got, want := counts["PolicyViolationPreventionRate"], 1; got != want {
		t.Errorf("PolicyViolationPreventionRate count: got %d want %d", got, want)
	}
}

// Mixed run: one block from EACH class → three numerator=1 events
// AND exactly 3 metric events total (no dupes, no drops).
func TestDryRunContractTool_MixedRun_NumeratorsAllOne(t *testing.T) {
	tools, sink := newTestToolsWithSink(t)
	tc := makeAllFourViolationsContract(t) // helper: missing.yaml + go 1.22 + forbidden_alias + network=internet
	snap := capability.Snapshot{
		OS: "linux", Arch: "amd64", Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
		Network:     capability.NetworkIntranet,
		Tools:       []capability.ToolVersion{{Name: "go", Version: "1.18.4"}},
		Credentials: []capability.CredentialAlias{"openai_for_glm"},
	}
	args, _ := json.Marshal(map[string]interface{}{"contract": tc, "capability_snapshot": snap})
	if _, err := (&dryRunContractTool{t: tools}).Call(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	numerators := map[string]int{}
	counts := map[string]int{}
	// Payload key check: each metric type carries its class-specific
	// count field (blocks_total / missing_files / policy_violations),
	// per spec §5. Reject a generic `count` key regression.
	payloadKeys := map[string]map[string]bool{
		"PreExecutionFaultCatchRate":    {"blocks_total": true},
		"MissingArtifactDetectionRate":  {"missing_files": true},
		"PolicyViolationPreventionRate": {"policy_violations": true},
	}
	for _, ev := range sink.Events() {
		switch ev.Type {
		case "PreExecutionFaultCatchRate", "MissingArtifactDetectionRate", "PolicyViolationPreventionRate":
			counts[ev.Type]++
			var p map[string]interface{}
			_ = json.Unmarshal(ev.Payload, &p)
			num, _ := p["numerator"].(float64)
			numerators[ev.Type] = int(num)
			for k := range payloadKeys[ev.Type] {
				if _, ok := p[k]; !ok {
					t.Errorf("event %s missing required payload key %q; got %v", ev.Type, k, p)
				}
			}
		}
	}
	// EXACTLY 3 events (no dupes, no drops).
	if got, want := counts["PreExecutionFaultCatchRate"], 1; got != want {
		t.Errorf("PreExecutionFaultCatchRate count: got %d want %d", got, want)
	}
	if got, want := counts["MissingArtifactDetectionRate"], 1; got != want {
		t.Errorf("MissingArtifactDetectionRate count: got %d want %d", got, want)
	}
	if got, want := counts["PolicyViolationPreventionRate"], 1; got != want {
		t.Errorf("PolicyViolationPreventionRate count: got %d want %d", got, want)
	}
	total := counts["PreExecutionFaultCatchRate"] + counts["MissingArtifactDetectionRate"] + counts["PolicyViolationPreventionRate"]
	if total != 3 {
		t.Errorf("total metric events: got %d want 3", total)
	}
	if numerators["PreExecutionFaultCatchRate"] != 1 {
		t.Errorf("PreExecutionFaultCatchRate numerator: got %d want 1", numerators["PreExecutionFaultCatchRate"])
	}
	if numerators["MissingArtifactDetectionRate"] != 1 {
		t.Errorf("MissingArtifactDetectionRate numerator: got %d want 1", numerators["MissingArtifactDetectionRate"])
	}
	if numerators["PolicyViolationPreventionRate"] != 1 {
		t.Errorf("PolicyViolationPreventionRate numerator: got %d want 1", numerators["PolicyViolationPreventionRate"])
	}
}

// §7(d) ablation bypass: NoDryRun on → zero events, zero blocks in
// dryRunReport, zero rows in dry_run_blocks, exactly one log line
// containing the conversation_id.
func TestDryRunContractTool_NoDryRunAblation_ShortCircuits(t *testing.T) {
	tools, sink := newTestToolsWithSink(t)

	// Inject a real SQLite-backed writer so we can assert row count.
	store, err := observerstore.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	writer := observerstore.NewDryRunBlockWriter(store.DB())
	tools.dryRunWriter = writer

	prev := validator.IsDryRunDisabled()
	t.Cleanup(func() { validator.SetDryRunDisabled(prev) })
	validator.SetDryRunDisabled(true)

	// Capture stderr / log output.
	var logBuf bytes.Buffer
	origOut := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(origOut) })

	// Even a contract that WOULD produce all four blocks must persist
	// zero rows when the ablation gate is on.
	tc := makeAllFourViolationsContract(t)
	tc.ConversationID = "conv-ablation-42"
	snap := capability.Snapshot{
		OS: "linux", Arch: "amd64", Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
		Network:     capability.NetworkIntranet,
		Tools:       []capability.ToolVersion{{Name: "go", Version: "1.18.4"}},
		Credentials: []capability.CredentialAlias{"openai_for_glm"},
	}
	args, _ := json.Marshal(map[string]interface{}{"contract": tc, "capability_snapshot": snap})
	out, err := (&dryRunContractTool{t: tools}).Call(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	var report dryRunReport
	_ = json.Unmarshal(out, &report)
	if len(report.Blocks) != 0 {
		t.Errorf("ablation-on: expected 0 blocks; got %d", len(report.Blocks))
	}
	// Zero of the three metric events.
	for _, ev := range sink.Events() {
		switch ev.Type {
		case "PreExecutionFaultCatchRate", "MissingArtifactDetectionRate", "PolicyViolationPreventionRate":
			t.Errorf("ablation-on: unexpected metric event %s", ev.Type)
		}
	}
	// Zero rows in dry_run_blocks (spec §7(d) "zero dry_run_blocks rows").
	var n int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM dry_run_blocks`).Scan(&n); err != nil {
		t.Fatalf("count dry_run_blocks: %v", err)
	}
	if n != 0 {
		t.Errorf("ablation-on: expected 0 dry_run_blocks rows; got %d", n)
	}
	// Exactly one log line containing the mandated shape.
	logText := logBuf.String()
	want := "[ablation] NoDryRun: skipped conversation=conv-ablation-42"
	if !strings.Contains(logText, want) {
		t.Errorf("missing ablation log line %q; got:\n%s", want, logText)
	}
	if strings.Count(logText, want) != 1 {
		t.Errorf("expected 1 log line matching %q; got %d in:\n%s", want, strings.Count(logText, want), logText)
	}
}

// §4.3 "persist one row per block" — with the ablation flag OFF, a
// 4-block mixed dry-run must land 4 rows in dry_run_blocks with the
// right kinds, attempt_id, contract_hash, and capability_snapshot_hash.
func TestDryRunContractTool_PersistsOneRowPerBlock(t *testing.T) {
	tools, _ := newTestToolsWithSink(t)
	store, err := observerstore.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tools.dryRunWriter = observerstore.NewDryRunBlockWriter(store.DB())

	tc := makeAllFourViolationsContract(t)
	tc.ConversationID = "conv-persist-1"
	snap := capability.Snapshot{
		OS: "linux", Arch: "amd64", Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
		Network:     capability.NetworkIntranet,
		Tools:       []capability.ToolVersion{{Name: "go", Version: "1.18.4"}},
		Credentials: []capability.CredentialAlias{"openai_for_glm"},
	}
	args, _ := json.Marshal(map[string]interface{}{"contract": tc, "capability_snapshot": snap})
	out, err := (&dryRunContractTool{t: tools}).Call(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	var report dryRunReport
	_ = json.Unmarshal(out, &report)

	var n int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM dry_run_blocks WHERE attempt_id=?`, report.AttemptID).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if n != 4 {
		t.Errorf("expected 4 dry_run_blocks rows for attempt %s; got %d", report.AttemptID, n)
	}

	// One row per class.
	rows, err := store.DB().Query(`SELECT block_kind FROM dry_run_blocks WHERE attempt_id=? ORDER BY block_kind`, report.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		got[k] = true
	}
	for _, want := range []string{"missing_file", "wrong_version", "forbidden_cred", "policy_violation"} {
		if !got[want] {
			t.Errorf("missing persisted block kind %q; got %v", want, got)
		}
	}
}
```

Helper functions (add near existing `newTestTools` in `tools_test.go`
which already provides `newTestToolsWithObserver(t, sdk SDKClient, obs
ObserverSink) *Tools`; `fakeSDK` is the existing SDK stub — see
`tools_test.go:26`):

```go
// newTestToolsWithSink returns Tools wired to a capturing sink, using
// the existing newTestToolsWithObserver helper (tools_test.go:84) and
// a fresh fakeSDK with a no-op DiscoverAgents (the tool calls it
// early in Call; nil discoverFunc panics).
func newTestToolsWithSink(t *testing.T) (*Tools, *testEventSink) {
	t.Helper()
	sink := &testEventSink{}
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) { return nil, nil },
	}
	tools := newTestToolsWithObserver(t, sdk, sink)
	return tools, sink
}

type testEventSink struct {
	mu     sync.Mutex
	events []observer.Event
}

func (s *testEventSink) Emit(ev observer.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *testEventSink) Events() []observer.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]observer.Event, len(s.events))
	copy(out, s.events)
	return out
}

// makeMinimalValidContract returns a contract that passes .Validate().
func makeMinimalValidContract(t *testing.T) contract.TaskContract {
	t.Helper()
	tc := contract.TaskContract{
		Version:        contract.Version,
		ConversationID: "conv-minimal-1",
		Intent:         contract.IntentSpec{Goal: "x", SuccessCriteria: []string{"ok"}},
		DataContract: contract.DataContract{
			ReadArtifacts: []contract.ArtifactRef{},
			WriteTargets:  []contract.WriteTarget{{Type: contract.WriteTargetArtifact, Kind: "document", Name: "out.md"}},
		},
		RecoveryHint:           "read nothing; retry idempotent",
		ExecutionPolicy:        contract.ExecutionPolicy{Routing: contract.RoutingMasterOnly},
		CapabilityRequirements: contract.CapabilityRequirements{Skills: []string{}, Tools: []string{}},
	}
	tc.ApplyDefaults()
	return tc
}

// makeAllFourViolationsContract fires one block per class against the
// snapshot in TestDryRunContractTool_MixedRun_NumeratorsAllOne.
func makeAllFourViolationsContract(t *testing.T) contract.TaskContract {
	t.Helper()
	tc := makeMinimalValidContract(t)
	tc.DataContract.ReadArtifacts = []contract.ArtifactRef{{Kind: "file", Name: "missing.yaml"}}
	tc.CapabilityRequirements.ToolRequirements = []contract.ToolRequirement{{Name: "go", MinVersion: "1.22.0"}}
	tc.CapabilityRequirements.ForbiddenAliases = []string{"openai_for_glm"}
	tc.ExecutionPolicy.RequiredReach = capability.NetworkInternet
	return tc
}
```

Extra imports: `"bytes"`, `"log"`, `"strings"`, `"sync"`.

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go test ./internal/driver/ -run 'TestDryRunContractTool_EmitsThreeMetricEvents|TestDryRunContractTool_MixedRun|TestDryRunContractTool_NoDryRunAblation' -count=1 -race
```

Expected: FAIL — Task 12's `Call` doesn't emit events, doesn't check ablation.

- [ ] **Step 3: Implement the emit + ablation + persistence path in `Call`**

Extend `multi-agent/internal/driver/capability_tools.go`:

1. Add helpers:

```go
// extractExperimentID pulls an experiment_id off the contract's
// business_context (convention: `experiment_id=<id>` marker). Empty
// string when not present — dry-runs outside an experiment run fine
// with an empty experiment_id column. A future WT can move this to
// a dedicated contract field.
func extractExperimentID(tc contract.TaskContract) string {
	const marker = "experiment_id="
	ctx := tc.Intent.BusinessContext
	i := strings.Index(ctx, marker)
	if i < 0 {
		return ""
	}
	rest := ctx[i+len(marker):]
	end := strings.IndexAny(rest, " \t\n,")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

// contractHash returns a stable identifier for the contract. Uses
// json.Marshal (contract has stable field order in its struct
// declaration; the two hashes will be byte-identical for two
// equivalent contracts).
func contractHash(tc contract.TaskContract) string {
	body, err := json.Marshal(tc)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// emitDryRunMetric emits one metric event of `kind` with the standard
// per-invocation payload shape. `countKey` names the per-class count
// field (`blocks_total` for PreExecutionFaultCatchRate,
// `missing_files` for MissingArtifactDetectionRate,
// `policy_violations` for PolicyViolationPreventionRate) — see spec §5
// payload table. Keeping distinct keys per metric type means the
// consumer view SELECTs on `type=...` can key on the field it expects
// without disambiguating by generic `count`.
func (t *Tools) emitDryRunMetric(ctx context.Context, kind, countKey string, attemptID, contractHash, experimentID string, numerator, count int) {
	payload := map[string]interface{}{
		"numerator":     numerator,
		countKey:        count,
		"attempt_id":    attemptID,
		"contract_hash": contractHash,
		"experiment_id": experimentID,
	}
	body, _ := json.Marshal(payload)
	t.emit(observer.Event{
		WorkspaceID: t.cfg.Observer.WorkspaceID,
		AgentID:     t.cfg.Credentials.ShortID,
		AgentRole:   observer.RoleDriver,
		Type:        kind,
		TaskID:      "",
		Payload:     json.RawMessage(body),
	})
	_ = ctx // reserved for future cancellation
}
```

Add `crypto/sha256` and `encoding/hex` to the imports if not already present, plus `"github.com/yourorg/multi-agent/internal/observer"` (already used elsewhere in driver, may be present).

2. Rewrite `Call` again to wire ablation + emit + persistence:

```go
func (d *dryRunContractTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Contract           contract.TaskContract `json:"contract"`
		CapabilitySnapshot json.RawMessage       `json:"capability_snapshot"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error(), Category: observerstore.FailContractViolation}
	}
	tc := args.Contract
	tc.ApplyDefaults()
	if err := tc.Validate(); err != nil {
		return nil, &MCPToolError{Message: "invalid contract: " + err.Error(), Category: observerstore.FailContractViolation}
	}
	cards, err := d.t.sdk.DiscoverAgents(ctx)
	if err != nil {
		return nil, &MCPToolError{Message: "discover agents: " + err.Error(), Category: observerstore.FailUnknown}
	}
	report := analyzeContractCapabilities(cards, d.t.cfg.Credentials.SandboxID, tc)
	report.AttemptID = randomHex(16)

	// §7(d) — ablation short-circuit. Route recommendation remains;
	// only the four §A3 pre-exec checks + their side-effects are gated.
	if validator.IsDryRunDisabled() {
		log.Printf("[ablation] NoDryRun: skipped conversation=%s", tc.ConversationID)
		return json.Marshal(report)
	}

	var blocks []validator.Block
	var snapHash string
	if len(args.CapabilitySnapshot) > 0 {
		var snapSpec capability.Snapshot
		if err := json.Unmarshal(args.CapabilitySnapshot, &snapSpec); err != nil {
			return nil, &MCPToolError{Message: "invalid capability_snapshot: " + err.Error(), Category: observerstore.FailContractViolation}
		}
		snap, err := capability.NewSnapshot(snapSpec)
		if err != nil {
			return nil, &MCPToolError{Message: "capability_snapshot: " + err.Error(), Category: observerstore.FailContractViolation}
		}
		blocks = validator.New().Check(ctx, tc, snap)
		snapHash = capability.ComputeHash(snap)
	}
	report.Blocks = blocks
	if len(blocks) > 0 {
		report.Runnable = false
	}

	ch := contractHash(tc)
	experimentID := extractExperimentID(tc)

	// Persist blocks (best-effort; failure warns but doesn't fail the
	// tool call — matches SaveResourceSnapshot pattern).
	if writer := d.t.dryRunBlockWriter(); writer != nil && len(blocks) > 0 {
		blockedAt := nowUTC()
		for i, b := range blocks {
			row := observerstore.DryRunBlockRow{
				BlockID:                report.AttemptID + "-" + strconv.Itoa(i),
				AttemptID:              report.AttemptID,
				ConversationID:         tc.ConversationID,
				ExperimentID:           experimentID,
				ContractHash:           ch,
				CapabilitySnapshotHash: snapHash,
				BlockKind:              string(b.Kind),
				Field:                  b.Field,
				Expected:               b.Expected,
				Actual:                 b.Actual,
				Detail:                 b.Detail,
				BlockedAt:              blockedAt,
			}
			if werr := writer.WriteDryRunBlock(ctx, row); werr != nil {
				d.t.logHelperErr("dry_run_blocks", "write_block", werr)
			}
		}
	}

	// Emit 3 metric events (§5) — always, even for clean runs, so the
	// denominator is well-defined.
	preExec := 0
	if len(blocks) > 0 {
		preExec = 1
	}
	missingFiles := countBlocksOfKind(blocks, validator.KindMissingFile)
	policyViolations := countBlocksOfKind(blocks, validator.KindPolicyViolation)

	missingNum, policyNum := 0, 0
	if missingFiles > 0 {
		missingNum = 1
	}
	if policyViolations > 0 {
		policyNum = 1
	}
	d.t.emitDryRunMetric(ctx, "PreExecutionFaultCatchRate", "blocks_total", report.AttemptID, ch, experimentID, preExec, len(blocks))
	d.t.emitDryRunMetric(ctx, "MissingArtifactDetectionRate", "missing_files", report.AttemptID, ch, experimentID, missingNum, missingFiles)
	d.t.emitDryRunMetric(ctx, "PolicyViolationPreventionRate", "policy_violations", report.AttemptID, ch, experimentID, policyNum, policyViolations)

	return json.Marshal(report)
}

func countBlocksOfKind(blocks []validator.Block, kind validator.Kind) int {
	n := 0
	for _, b := range blocks {
		if b.Kind == kind {
			n++
		}
	}
	return n
}
```

3. Add `dryRunBlockWriter()` accessor to `Tools` (in `tools.go`):

```go
// dryRunBlockWriter returns the observer-side writer for
// dry_run_blocks, or nil if the driver has no observer DB attached
// (tests may operate without one).
func (t *Tools) dryRunBlockWriter() observerstore.DryRunBlockWriter {
	if t.dryRunWriter == nil {
		return nil
	}
	return t.dryRunWriter
}
```

Add the field to the `Tools` struct: `dryRunWriter observerstore.DryRunBlockWriter`. Wire it in `NewTools` (or the driver's equivalent constructor) — this is required by spec §4.3 "persist one row per block".

**Production wiring is REQUIRED by this WT** — no fallback path. The driver process talks to observer-server via HTTP (see `ObserverRelay`), not to a local SQLite. Concrete work:

1. Add a POST endpoint `/api/dry-run-blocks` to observer-server (see
   `multi-agent/cmd/observer-server/`) whose handler wraps
   `NewDryRunBlockWriter(store.DB())` and consumes a JSON body matching
   `DryRunBlockRow`. Follow the shape of the existing
   `/api/resource-snapshots` handler (it already accepts a JSON body
   and forwards to a writer on the store's *sql.DB).
2. Define an `httpDryRunBlockWriter` in
   `internal/driver/observer_relay.go` that POSTs each row via the
   existing HTTP client + bearer-token flow (mirrors
   `SaveTaskContract` / `SaveResourceSnapshot`). Instance is created
   inside `NewObserverRelay`.
3. Expose the writer through `NewTools` by adding a
   `t.observer.DryRunBlockWriter()` accessor on `ObserverRelay` or by
   handing the writer into `NewTools` as a constructor arg.

Unit tests inject a real SQLite-backed writer directly (bypassing HTTP) via `tools.dryRunWriter = observerstore.NewDryRunBlockWriter(sqliteDB)` — see `TestDryRunContractTool_PersistsOneRowPerBlock` and `TestDryRunContractTool_NoDryRunAblation_ShortCircuits`.

**If a test on a wired driver finds `t.dryRunWriter == nil`, that is a bug.** The plan's `dryRunBlockWriter()` accessor is only tolerant of nil for narrow test cases. Production fail-loud mechanism (current `NewTools` returns `*Tools`, not `(*Tools, error)` — no refactor required by this WT):

- In the driver's `main.go` (or the equivalent process entrypoint that wires `ObserverRelay` → `NewTools`), add a post-construction assertion: `if tools.dryRunWriter == nil { log.Fatal("dry_run_blocks writer not configured — spec §4.3 requires production wire-up") }`. Placed in `main`, this aborts the process before any dry-run call could silently drop persistence — the "fail loudly at startup" semantics the WT-2 spec requires without touching `NewTools`'s signature.
- Tests that intentionally construct `Tools` without a writer (existing driver test suite pre-WT-2) are unaffected because they never traverse `main.go`; they operate at unit-test scope where `tools.dryRunWriter` may be nil and the accessor's nil-guard covers them.

4. Add `nowUTC()` if not present (mirror `observerstore.nowUTC`): declare `var nowUTC = func() time.Time { return time.Now().UTC() }` at package scope.

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go test ./internal/driver/ -count=1 -race
```

Expected: PASS for the four new tests (EmitsThreeMetricEvents, MixedRun_NumeratorsAllOne, NoDryRunAblation_ShortCircuits, PersistsOneRowPerBlock) + all existing driver tests.

- [ ] **Step 5: Commit**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator
git add multi-agent/internal/driver/capability_tools.go multi-agent/internal/driver/tools.go multi-agent/internal/driver/capability_tools_test.go
git commit -m "$(cat <<'EOF'
WT-2-dry-run-validator: emit 3 metrics + persist blocks + ablation gate (§5, §6, §7(d))

- Emits PreExecutionFaultCatchRate / MissingArtifactDetectionRate /
  PolicyViolationPreventionRate per invocation (numerator=1 iff class
  fires; denominator = COUNT(*) on the event type).
- Best-effort dry_run_blocks writer wires blocks into observerstore
  with attempt_id + contract_hash + snapshot_hash + experiment_id.
- IsDryRunDisabled() short-circuit logs
  "[ablation] NoDryRun: skipped conversation=<id>" and emits zero
  events / persists zero blocks. Route recommendation is not gated.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 14: Backward-compat + best-effort-failure regression tests

**Files:**
- Modify: `multi-agent/internal/driver/capability_tools_test.go`

**Interfaces:** none new.

- [ ] **Step 1: Write tests**

Append to `capability_tools_test.go`:

```go
// §4.1 backward compat: contract-only args (no capability_snapshot) →
// tool succeeds, Blocks empty, recommended_route present (existing
// pre-WT-2 output). All 3 events still fire with numerator=0.
func TestDryRunContractTool_BackwardCompat_NoSnapshot(t *testing.T) {
	tools, sink := newTestToolsWithSink(t)
	tc := makeMinimalValidContract(t)
	args, _ := json.Marshal(map[string]interface{}{"contract": tc})
	out, err := (&dryRunContractTool{t: tools}).Call(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	var report dryRunReport
	_ = json.Unmarshal(out, &report)
	if len(report.Blocks) != 0 {
		t.Errorf("no snapshot: expected 0 blocks; got %d", len(report.Blocks))
	}
	// EXACTLY 3 events, all numerator=0. This is the backward-compat
	// contract: callers who don't supply a snapshot must still see the
	// events land so the denominator is unbiased (spec §5.1 last para
	// + §5 semantic). Test explicitly counts each type to catch a
	// regression where "no snapshot" silently skips emission.
	counts := map[string]int{}
	for _, ev := range sink.Events() {
		switch ev.Type {
		case "PreExecutionFaultCatchRate", "MissingArtifactDetectionRate", "PolicyViolationPreventionRate":
			counts[ev.Type]++
			var p struct {
				Numerator int `json:"numerator"`
			}
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if p.Numerator != 0 {
				t.Errorf("no-snapshot %s numerator: got %d want 0", ev.Type, p.Numerator)
			}
		}
	}
	if counts["PreExecutionFaultCatchRate"] != 1 {
		t.Errorf("PreExecutionFaultCatchRate count: got %d want 1", counts["PreExecutionFaultCatchRate"])
	}
	if counts["MissingArtifactDetectionRate"] != 1 {
		t.Errorf("MissingArtifactDetectionRate count: got %d want 1", counts["MissingArtifactDetectionRate"])
	}
	if counts["PolicyViolationPreventionRate"] != 1 {
		t.Errorf("PolicyViolationPreventionRate count: got %d want 1", counts["PolicyViolationPreventionRate"])
	}
	// Backward compat: recommended_route MUST still be set.
	if report.RecommendedRoute == "" {
		t.Error("backward-compat: recommended_route missing")
	}
}

// A dry_run_blocks writer failure must NOT fail the tool call; it
// logs a warning via t.logHelperErr and the report is returned intact.
func TestDryRunContractTool_WriterFailure_DoesNotFailCall(t *testing.T) {
	tools, _ := newTestToolsWithSink(t)
	tools.dryRunWriter = failingDryRunWriter{}
	tc := makeAllFourViolationsContract(t)
	snap := capability.Snapshot{OS: "linux", Arch: "amd64", Platform: commandiface.Platform{OS: "linux", Arch: "amd64"}, Network: capability.NetworkIntranet, Tools: []capability.ToolVersion{{Name: "go", Version: "1.18.4"}}, Credentials: []capability.CredentialAlias{"openai_for_glm"}}
	args, _ := json.Marshal(map[string]interface{}{"contract": tc, "capability_snapshot": snap})
	out, err := (&dryRunContractTool{t: tools}).Call(context.Background(), args)
	if err != nil {
		t.Fatalf("call failed on writer error: %v", err)
	}
	var report dryRunReport
	_ = json.Unmarshal(out, &report)
	if len(report.Blocks) == 0 {
		t.Error("blocks should still be present in report even when writer failed")
	}
}

type failingDryRunWriter struct{}

func (failingDryRunWriter) WriteDryRunBlock(context.Context, observerstore.DryRunBlockRow) error {
	return errors.New("simulated writer failure")
}
```

Add `"errors"` import if not already present.

- [ ] **Step 2: Run tests**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go test ./internal/driver/ -count=1 -race
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator
git add multi-agent/internal/driver/capability_tools_test.go
git commit -m "$(cat <<'EOF'
WT-2-dry-run-validator: backward-compat + writer-failure regression tests

(1) Contract-only args (no capability_snapshot) → tool returns
recommended_route + zero-numerator events (backward compat).
(2) A failing dry_run_blocks writer must NOT fail the tool call —
best-effort persistence semantics matches SaveResourceSnapshot.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 15: Final full-suite gate + `go vet ./...` + spec-coverage sweep

**Files:** none new — this is the gate.

- [ ] **Step 1: Run the full acceptance suite**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go test ./internal/contract/validator/... ./internal/driver/... ./internal/observerstore/... -count=1 -shuffle=on -race
```

Expected: all PASS. If any test fails, DO NOT PROCEED — fix and re-run.

- [ ] **Step 2: `go vet`**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator/multi-agent
go vet ./...
```

Expected: no warnings.

- [ ] **Step 3: §7(b) CI guard grep**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator
if grep -rE 'strings\.Split\(.*"\\."' multi-agent/internal/contract/validator/*.go | grep -v _test.go ; then
  echo "FAIL: §7(b) — hand-rolled semver split detected"
  exit 1
fi
echo "§7(b) semver-purity guard PASS"
```

Expected: `§7(b) semver-purity guard PASS`.

- [ ] **Step 4: Spec-coverage checklist (self-review)**

Cross-reference the plan against the spec §Acceptance list:

- [ ] 4 inject tests (one per class) — Tasks 3–6 each write theirs.
- [ ] 3-metric counts on a mixed dry-run — Task 13 (`TestDryRunContractTool_MixedRun_NumeratorsAllOne`).
- [ ] `NoDryRun` bypass: log + zero events + zero rows — Task 13 (`TestDryRunContractTool_NoDryRunAblation_ShortCircuits`).
- [ ] Consumer-view SELECT returns 0.4 — Task 11.
- [ ] Backward-compat when snapshot omitted — Task 14 (`TestDryRunContractTool_BackwardCompat_NoSnapshot`).
- [ ] §7(a) validator purity — Task 2 (`TestImportPurity`).
- [ ] §7(b) rc-prerelease semver — Task 4 (`TestCheckWrongVersion_RCPrereleaseBelowRelease`) + Task 15 grep guard.
- [ ] §7(c) parameterized SQL + 8 KiB detail — Task 10 (`TestWriteDryRunBlock_DetailTruncatedAt8KiB`, `TestWriteDryRunBlock_SQLMetaCharactersPersistVerbatim`).
- [ ] §7(d) ablation log line — Task 13 (`TestDryRunContractTool_NoDryRunAblation_ShortCircuits`).
- [ ] §7(e) forbidden_cred bypass matrix — Task 5 (`TestCheckForbiddenCred_ExactMatch_RejectsBypasses`).
- [ ] §7(f) `Block.Detail` shape — Task 2 (`TestBlock_DetailShape`, `TestBlock_DetailTruncatedAt8KiB`) + Task 3 (`TestCheckMissingFile_MalformedPatternDoesNotLeak`).
- [ ] §7(g) consumer-view SELECT — Task 11.
- [ ] Perf assertions CI-conditionalized — Task 10 (`TestWriteDryRunBlock_PerfBudget`).

- [ ] **Step 5: Verify the git log**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator
git log --format='%h %s%n%b' origin/paper/v3-integration..HEAD | grep -E '^Co-Authored-By:' | sort -u
```

Expected: only one line — `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>` — present on every commit.

- [ ] **Step 6: Confirm no push**

```bash
cd /root/multi-agent/.worktrees/p2-dry-run-validator
git status  # working tree clean
git log --oneline origin/paper/v3-integration..HEAD  # local-only commits
# DO NOT git push.
```

Expected: local commits present; nothing pushed.

- [ ] **Step 7: (No commit needed — this task is a gate, not a code change.)**

If Steps 1–5 all pass, the plan is fully executed. Handoff to Codex Stage-3 code review.

---

## Self-Review

The plan covers every spec section:

| Spec § | Covered by task |
| --- | --- |
| §1 Files touched | Tasks 1–13 (file matrix at top of plan) |
| §2 Validator interface | Task 2 |
| §3.1 missing_file | Task 3 |
| §3.2 wrong_version | Task 4 |
| §3.3 forbidden_cred | Task 5 |
| §3.4 policy_violation | Task 6 |
| §4 dryRunContractTool extension | Tasks 12–13 |
| §5 Metric events | Task 13 |
| §5.1 Ablation-off event contract | Task 13 |
| §6 dry_run_blocks table | Tasks 9–10 |
| §6.1 Consumer view SELECT | Task 11 |
| §6.2 Column rationale | Task 9 (DDL) |
| §7(a) Validator purity | Task 2 (import-purity test) |
| §7(b) Semver library | Task 4 + Task 15 grep guard |
| §7(c) SQL + detail cap | Tasks 2, 10 |
| §7(d) Ablation no silent bypass | Tasks 8, 13 |
| §7(e) forbidden_cred exact match | Task 5 |
| §7(f) Block.Detail three-field | Tasks 2, 3 |
| §7(g) Consumer-view reverse audit | Tasks 9, 11 |
| §8 Acceptance | Task 15 |

No placeholders; no "TODO"/"TBD"; every code step has full code; every command has expected output. Type consistency: `DryRunBlockRow`, `Block`, `Kind`, `Severity`, `Validator`, `dryRunReport.Blocks`, `IsDryRunDisabled`/`SetDryRunDisabled`, `NewDryRunBlockWriter` — all used consistently across tasks.

Production writer wire-up is REQUIRED by this WT — see Task 13 step 3 "Production wiring is REQUIRED" for the concrete three-step scope (observer-server endpoint + httpDryRunBlockWriter + NewTools wire-up). `NewTools` fails loudly if the writer is absent; tests inject an in-memory SQLite writer directly.

## Execution Handoff

Plan complete and saved to `docs/specs/wt2-dry-run-validator.plan.md`. Per this repo's three-stage convention:

1. Stage-2 Codex CLEAN loop on the plan (next step).
2. On CLEAN, Stage-3 TDD implementation task-by-task.

Suggested implementation mode: **superpowers:subagent-driven-development** — fresh subagent per task, review between tasks. Alternate: inline execution with checkpoints per task.
