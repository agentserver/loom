# Distributed Driver/Master Contract Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the first executable slice of the distributed driver/master contract design: typed contracts, observer persistence, driver contract submission, master contract validation, and container E2E scaffolding.

**Architecture:** Introduce `internal/contract` as the shared schema and validation boundary. Observer persists contracts and resource snapshots. Driver exposes a new contract-aware submit path that can route direct-first or send a constrained payload to master. Master recognizes contract payloads and rejects invalid plans before dispatch.

**Tech Stack:** Go, SQLite via `modernc.org/sqlite`, existing observer HTTP server, existing driver MCP tool framework, existing master orchestrator and planner packages, Docker Compose for distributed development tests.

---

## Scope

This plan implements Phase 1 of `docs/superpowers/specs/2026-05-14-distributed-driver-master-contract-design.md`.

Implemented in this phase:

- Shared `TaskContract`, `ResourceSnapshot`, `ExecutionPolicy`, `ArtifactRecord` types.
- Validation/defaulting for contract policies.
- Observer persistence and HTTP APIs for contracts and snapshots.
- Driver resource snapshot builder and `submit_contract_task` MCP tool.
- Master contract envelope parsing and policy validation.
- Container runtime and compose scaffolding for long-lived agent containers.

Excluded from this phase:

- Full natural-language driver clarification loop.
- Plan approval UI.
- Repo commit mode.
- Automatic MCP upgrade suggestions.
- Full no-shared-business-volume E2E with real agentserver authorization.

## File Map

- Create `multi-agent/internal/contract/types.go`: shared JSON data model.
- Create `multi-agent/internal/contract/validate.go`: defaulting, validation, helper methods.
- Create `multi-agent/internal/contract/envelope.go`: prompt envelope encode/decode for master compatibility.
- Create `multi-agent/internal/contract/snapshot.go`: normalize `agentsdk.AgentCard` into `ResourceSnapshot`.
- Create `multi-agent/internal/contract/contract_test.go`: unit tests for defaults, validation, envelope, snapshot normalization.
- Modify `multi-agent/internal/observerstore/schema.sql`: add contract and resource snapshot tables.
- Modify `multi-agent/internal/observerstore/store.go`: add store models and CRUD methods.
- Modify `multi-agent/internal/observerstore/store_test.go`: test persistence.
- Modify `multi-agent/internal/observerweb/server.go`: add contract/snapshot API routes.
- Modify `multi-agent/internal/observerweb/server_test.go`: test API authentication and payloads.
- Create `multi-agent/internal/driver/contract_tools.go`: new `submit_contract_task` tool and routing helper.
- Modify `multi-agent/internal/driver/observer_relay.go`: save task contracts and resource snapshots through observer HTTP APIs.
- Modify `multi-agent/internal/driver/tools.go`: register the new tool in `Tools.All()`.
- Modify `multi-agent/internal/driver/tools_test.go`: test driver contract submit behavior.
- Create `multi-agent/internal/orchestrator/contract_policy.go`: parse contract envelope and validate master policy.
- Modify `multi-agent/internal/orchestrator/dag.go`: add policy-aware DAG validation.
- Modify `multi-agent/internal/orchestrator/route.go`: reject contract route when master is not allowed.
- Modify `multi-agent/internal/orchestrator/fanout.go`: validate contract policy after planning and before dispatch.
- Create `multi-agent/internal/orchestrator/contract_policy_test.go`: test master rejection cases.
- Modify `multi-agent/internal/config/config.go`: persist `credentials.workspace_id` for master and slave configs.
- Modify `multi-agent/internal/tunnel/tunnel.go`: save `WorkspaceID` returned by agentserver registration.
- Modify `multi-agent/internal/config/config_test.go`: verify workspace ID YAML round-trip.
- Modify `multi-agent/internal/tunnel/tunnel_test.go`: verify registration persists workspace ID.
- Create `multi-agent/dev/agent-runtime/Dockerfile`: long-lived agent runtime with Claude Code installed.
- Create `multi-agent/dev/compose.distributed.yaml`: sample multi-container development topology.
- Create `multi-agent/dev/configs/master.example.yaml`: persisted master config example.
- Create `multi-agent/dev/configs/driver.example.yaml`: persisted driver config example.
- Create `multi-agent/dev/configs/slave-a.example.yaml`: persisted first slave config example.
- Create `multi-agent/dev/configs/slave-b.example.yaml`: persisted second slave config example.
- Create `multi-agent/tests/scripts/distributed_compose_test.go`: static test that compose/config scaffolding has required mounts and environment.

---

### Task 1: Shared Contract Package

**Files:**
- Create: `multi-agent/internal/contract/types.go`
- Create: `multi-agent/internal/contract/validate.go`
- Create: `multi-agent/internal/contract/envelope.go`
- Create: `multi-agent/internal/contract/snapshot.go`
- Create: `multi-agent/internal/contract/contract_test.go`

- [ ] **Step 1: Write failing tests for defaults and validation**

Create `multi-agent/internal/contract/contract_test.go`:

```go
package contract

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"
)

func TestTaskContractApplyDefaults(t *testing.T) {
	tc := TaskContract{
		Version:        1,
		ConversationID: "conv-1",
		Intent: IntentSpec{
			Goal:            "generate a helper",
			SuccessCriteria: []string{"helper is saved as an artifact"},
		},
		DataContract: DataContract{
			WriteTargets: []WriteTarget{{Type: WriteTargetArtifact, Kind: "code", Name: "helper.go"}},
		},
	}

	tc.ApplyDefaults()

	if tc.ExecutionPolicy.Routing != RoutingDirectFirst {
		t.Fatalf("routing = %q", tc.ExecutionPolicy.Routing)
	}
	if tc.ExecutionPolicy.AllowBuildMCP {
		t.Fatalf("allow_build_mcp default = true")
	}
	if !tc.ExecutionPolicy.AllowCodeArtifacts {
		t.Fatalf("allow_code_artifacts default = false")
	}
	if tc.ExecutionPolicy.CodePersistence != CodePersistenceObserverArtifactStore {
		t.Fatalf("code_persistence = %q", tc.ExecutionPolicy.CodePersistence)
	}
	if tc.ExecutionPolicy.WriteMode != WriteModeArtifactOnly {
		t.Fatalf("write_mode = %q", tc.ExecutionPolicy.WriteMode)
	}
	if tc.ExecutionPolicy.MaxDAGNodes != 6 {
		t.Fatalf("max_dag_nodes = %d", tc.ExecutionPolicy.MaxDAGNodes)
	}
}

func TestTaskContractValidateRejectsMissingIntent(t *testing.T) {
	tc := TaskContract{Version: 1}
	tc.ApplyDefaults()

	err := tc.Validate()
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if !strings.Contains(err.Error(), "intent.goal is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestTaskContractValidateRejectsBuildMCPWhenDisabled(t *testing.T) {
	tc := TaskContract{
		Version:        1,
		ConversationID: "conv-1",
		Intent: IntentSpec{
			Goal:            "create a durable tool",
			SuccessCriteria: []string{"tool is callable"},
		},
		DataContract: DataContract{
			WriteTargets: []WriteTarget{{Type: WriteTargetArtifact, Kind: "code", Name: "server.go"}},
		},
		CapabilityRequirements: CapabilityRequirements{
			Skills: []string{"build_mcp"},
		},
	}
	tc.ApplyDefaults()

	err := tc.Validate()
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if !strings.Contains(err.Error(), "build_mcp requested but execution_policy.allow_build_mcp is false") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnvelopeRoundTrip(t *testing.T) {
	tc := TaskContract{
		Version:        1,
		ConversationID: "conv-1",
		Intent: IntentSpec{
			Goal:            "summarize artifacts",
			SuccessCriteria: []string{"summary references artifacts"},
		},
		DataContract: DataContract{
			WriteTargets: []WriteTarget{{Type: WriteTargetArtifact, Kind: "document", Name: "summary.md"}},
		},
	}
	tc.ApplyDefaults()

	prompt, err := EncodeEnvelope(tc, "Use this contract.")
	if err != nil {
		t.Fatalf("EncodeEnvelope: %v", err)
	}
	got, body, ok, err := DecodeEnvelope(prompt)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	if !ok {
		t.Fatalf("expected envelope")
	}
	if body != "Use this contract." {
		t.Fatalf("body = %q", body)
	}
	if got.ConversationID != "conv-1" {
		t.Fatalf("conversation_id = %q", got.ConversationID)
	}
}

func TestResourceSnapshotFromAgentCards(t *testing.T) {
	cardBody := json.RawMessage(`{
		"skills":["chat","mcp"],
		"tools":["echo"],
		"short_id":"short-a",
		"resources":{"memory_gb":16,"tags":["go"]}
	}`)
	snap := NewResourceSnapshot([]agentsdk.AgentCard{
		{
			AgentID:     "agent-a",
			DisplayName: "slave-a",
			Description: "worker",
			Status:      "available",
			Card:        cardBody,
		},
	}, "driver-id")

	if len(snap.Agents) != 1 {
		t.Fatalf("agents len = %d", len(snap.Agents))
	}
	a := snap.Agents[0]
	if a.AgentID != "agent-a" || a.ShortID != "short-a" {
		t.Fatalf("agent = %+v", a)
	}
	if len(a.Skills) != 2 || a.Skills[1] != "mcp" {
		t.Fatalf("skills = %#v", a.Skills)
	}
	if string(a.Resources) == "" {
		t.Fatalf("resources not preserved")
	}
}
```

- [ ] **Step 2: Run tests and verify they fail**

Run:

```bash
cd /root/multi-agent/.worktrees/driver-master-contract-spec/multi-agent
/root/.local/go-toolchain/go/bin/go test ./internal/contract
```

Expected: FAIL because package `internal/contract` and its types do not exist.

- [ ] **Step 3: Add contract data types**

Create `multi-agent/internal/contract/types.go`:

```go
package contract

import "encoding/json"

const Version = 1

const (
	RoutingDirectFirst = "direct_first"
	RoutingMasterOnly  = "master_only"
)

const (
	CodePersistenceObserverArtifactStore = "observer_artifact_store"
	ExposeCodeOnRequest                  = "on_request"
	WriteModeArtifactOnly                = "artifact_only"
	WriteModePatch                       = "patch"
	WriteModeRepoCommit                  = "repo_commit"
	WriteTargetArtifact                  = "artifact"
)

type TaskContract struct {
	Version                  int                    `json:"version"`
	ConversationID           string                 `json:"conversation_id"`
	Intent                   IntentSpec             `json:"intent"`
	DataContract             DataContract           `json:"data_contract"`
	ExecutionPolicy          ExecutionPolicy        `json:"execution_policy"`
	CapabilityRequirements   CapabilityRequirements `json:"capability_requirements"`
}

type IntentSpec struct {
	Goal            string   `json:"goal"`
	BusinessContext string   `json:"business_context,omitempty"`
	SuccessCriteria []string `json:"success_criteria"`
	NonGoals        []string `json:"non_goals,omitempty"`
}

type DataContract struct {
	ReadArtifacts []ArtifactRef  `json:"read_artifacts"`
	WriteTargets  []WriteTarget  `json:"write_targets"`
}

type ArtifactRef struct {
	ArtifactID string `json:"artifact_id"`
	Kind       string `json:"kind,omitempty"`
	Name       string `json:"name,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
}

type WriteTarget struct {
	Type string `json:"type"`
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type ExecutionPolicy struct {
	Routing                      string   `json:"routing"`
	AllowMaster                  *bool    `json:"allow_master,omitempty"`
	AllowBuildMCP                bool     `json:"allow_build_mcp"`
	AllowCodeArtifacts           bool     `json:"allow_code_artifacts"`
	CodePersistence              string   `json:"code_persistence"`
	ExposeCodeToUser             string   `json:"expose_code_to_user"`
	WriteMode                    string   `json:"write_mode"`
	MaxDAGNodes                  int      `json:"max_dag_nodes"`
	MaxDepth                     int      `json:"max_depth"`
	MaxConcurrency               int      `json:"max_concurrency"`
	RequirePlanApproval          bool     `json:"require_plan_approval"`
	RequireUserApprovalForRepoWrites bool `json:"require_user_approval_for_repo_writes"`
	AllowedTargets               []string `json:"allowed_targets,omitempty"`
}

type CapabilityRequirements struct {
	Skills    []string        `json:"skills"`
	Tools     []string        `json:"tools"`
	Resources json.RawMessage `json:"resources,omitempty"`
}

type ResourceSnapshot struct {
	GeneratedAt string          `json:"generated_at"`
	Agents      []ResourceAgent `json:"agents"`
}

type ResourceAgent struct {
	AgentID     string          `json:"agent_id"`
	ShortID     string          `json:"short_id,omitempty"`
	DisplayName string          `json:"display_name"`
	Description string          `json:"description,omitempty"`
	Status      string          `json:"status"`
	Skills      []string        `json:"skills,omitempty"`
	Tools       []string        `json:"tools,omitempty"`
	MCPTools    json.RawMessage `json:"mcp_tools,omitempty"`
	Resources   json.RawMessage `json:"resources,omitempty"`
}

type ArtifactRecord struct {
	ArtifactID      string `json:"artifact_id"`
	ConversationID  string `json:"conversation_id"`
	TaskID          string `json:"task_id"`
	ProducerAgentID string `json:"producer_agent_id"`
	Kind            string `json:"kind"`
	Language        string `json:"language,omitempty"`
	Name            string `json:"name"`
	SHA256          string `json:"sha256"`
	Bytes           int64  `json:"bytes"`
	ContentRef      string `json:"content_ref"`
	VisibleToUser   bool   `json:"visible_to_user"`
	Purpose         string `json:"purpose"`
}
```

- [ ] **Step 4: Add defaults and validation**

Create `multi-agent/internal/contract/validate.go`:

```go
package contract

import (
	"fmt"
	"strings"
)

func (tc *TaskContract) ApplyDefaults() {
	if tc.Version == 0 {
		tc.Version = Version
	}
	if tc.ExecutionPolicy.Routing == "" {
		tc.ExecutionPolicy.Routing = RoutingDirectFirst
	}
	if tc.ExecutionPolicy.AllowMaster == nil {
		tc.ExecutionPolicy.AllowMaster = Bool(true)
	}
	if !tc.ExecutionPolicy.AllowCodeArtifacts {
		tc.ExecutionPolicy.AllowCodeArtifacts = true
	}
	if tc.ExecutionPolicy.CodePersistence == "" {
		tc.ExecutionPolicy.CodePersistence = CodePersistenceObserverArtifactStore
	}
	if tc.ExecutionPolicy.ExposeCodeToUser == "" {
		tc.ExecutionPolicy.ExposeCodeToUser = ExposeCodeOnRequest
	}
	if tc.ExecutionPolicy.WriteMode == "" {
		tc.ExecutionPolicy.WriteMode = WriteModeArtifactOnly
	}
	if tc.ExecutionPolicy.MaxDAGNodes == 0 {
		tc.ExecutionPolicy.MaxDAGNodes = 6
	}
	if tc.ExecutionPolicy.MaxDepth == 0 {
		tc.ExecutionPolicy.MaxDepth = 3
	}
	if tc.ExecutionPolicy.MaxConcurrency == 0 {
		tc.ExecutionPolicy.MaxConcurrency = 3
	}
}

func (tc TaskContract) Validate() error {
	if tc.Version != Version {
		return fmt.Errorf("unsupported contract version: %d", tc.Version)
	}
	if strings.TrimSpace(tc.Intent.Goal) == "" {
		return fmt.Errorf("intent.goal is required")
	}
	if len(tc.Intent.SuccessCriteria) == 0 {
		return fmt.Errorf("intent.success_criteria is required")
	}
	for i, wt := range tc.DataContract.WriteTargets {
		if wt.Type == "" {
			return fmt.Errorf("data_contract.write_targets[%d].type is required", i)
		}
		if wt.Type != WriteTargetArtifact {
			return fmt.Errorf("data_contract.write_targets[%d].type %q is not supported", i, wt.Type)
		}
		if wt.Kind == "" {
			return fmt.Errorf("data_contract.write_targets[%d].kind is required", i)
		}
		if wt.Name == "" {
			return fmt.Errorf("data_contract.write_targets[%d].name is required", i)
		}
	}
	if err := validatePolicy(tc.ExecutionPolicy); err != nil {
		return err
	}
	if !tc.ExecutionPolicy.AllowBuildMCP {
		for _, s := range tc.CapabilityRequirements.Skills {
			if s == "build_mcp" {
				return fmt.Errorf("build_mcp requested but execution_policy.allow_build_mcp is false")
			}
		}
	}
	return nil
}

func Bool(v bool) *bool {
	return &v
}

func (p ExecutionPolicy) AllowsMaster() bool {
	return p.AllowMaster == nil || *p.AllowMaster
}

func validatePolicy(p ExecutionPolicy) error {
	switch p.Routing {
	case RoutingDirectFirst, RoutingMasterOnly:
	default:
		return fmt.Errorf("execution_policy.routing %q is not supported", p.Routing)
	}
	if p.CodePersistence != CodePersistenceObserverArtifactStore {
		return fmt.Errorf("execution_policy.code_persistence %q is not supported", p.CodePersistence)
	}
	switch p.WriteMode {
	case WriteModeArtifactOnly, WriteModePatch, WriteModeRepoCommit:
	default:
		return fmt.Errorf("execution_policy.write_mode %q is not supported", p.WriteMode)
	}
	if p.WriteMode == WriteModeRepoCommit && !p.RequireUserApprovalForRepoWrites {
		return fmt.Errorf("repo_commit requires execution_policy.require_user_approval_for_repo_writes")
	}
	if p.MaxDAGNodes < 1 {
		return fmt.Errorf("execution_policy.max_dag_nodes must be >= 1")
	}
	if p.MaxDepth < 1 {
		return fmt.Errorf("execution_policy.max_depth must be >= 1")
	}
	if p.MaxConcurrency < 1 {
		return fmt.Errorf("execution_policy.max_concurrency must be >= 1")
	}
	return nil
}
```

- [ ] **Step 5: Add envelope helpers**

Create `multi-agent/internal/contract/envelope.go`:

```go
package contract

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	EnvelopeStart = "<TASK_CONTRACT version=1>"
	EnvelopeEnd   = "</TASK_CONTRACT>"
)

func EncodeEnvelope(tc TaskContract, body string) (string, error) {
	tc.ApplyDefaults()
	if err := tc.Validate(); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(tc, "", "  ")
	if err != nil {
		return "", err
	}
	return EnvelopeStart + "\n" + string(b) + "\n" + EnvelopeEnd + "\n\n" + strings.TrimSpace(body), nil
}

func DecodeEnvelope(prompt string) (TaskContract, string, bool, error) {
	trimmed := strings.TrimSpace(prompt)
	if !strings.HasPrefix(trimmed, EnvelopeStart) {
		return TaskContract{}, prompt, false, nil
	}
	rest := strings.TrimPrefix(trimmed, EnvelopeStart)
	idx := strings.Index(rest, EnvelopeEnd)
	if idx < 0 {
		return TaskContract{}, "", true, fmt.Errorf("task contract envelope missing end marker")
	}
	raw := strings.TrimSpace(rest[:idx])
	body := strings.TrimSpace(rest[idx+len(EnvelopeEnd):])
	var tc TaskContract
	if err := json.Unmarshal([]byte(raw), &tc); err != nil {
		return TaskContract{}, "", true, fmt.Errorf("decode task contract: %w", err)
	}
	tc.ApplyDefaults()
	if err := tc.Validate(); err != nil {
		return TaskContract{}, "", true, err
	}
	return tc, body, true, nil
}
```

- [ ] **Step 6: Add resource snapshot normalization**

Create `multi-agent/internal/contract/snapshot.go`:

```go
package contract

import (
	"encoding/json"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
)

func NewResourceSnapshot(cards []agentsdk.AgentCard, selfID string) ResourceSnapshot {
	out := ResourceSnapshot{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Agents:      make([]ResourceAgent, 0, len(cards)),
	}
	for _, c := range cards {
		if c.AgentID == selfID {
			continue
		}
		var inner struct {
			Skills    []string        `json:"skills"`
			Tools     []string        `json:"tools"`
			MCPTools  json.RawMessage `json:"mcp_tools"`
			Resources json.RawMessage `json:"resources"`
			ShortID   string          `json:"short_id"`
		}
		_ = json.Unmarshal(c.Card, &inner)
		out.Agents = append(out.Agents, ResourceAgent{
			AgentID:     c.AgentID,
			ShortID:     inner.ShortID,
			DisplayName: c.DisplayName,
			Description: c.Description,
			Status:      c.Status,
			Skills:      inner.Skills,
			Tools:       inner.Tools,
			MCPTools:    inner.MCPTools,
			Resources:   inner.Resources,
		})
	}
	return out
}
```

- [ ] **Step 7: Run contract package tests**

Run:

```bash
cd /root/multi-agent/.worktrees/driver-master-contract-spec/multi-agent
/root/.local/go-toolchain/go/bin/go test ./internal/contract
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/contract
git commit -m "Add distributed task contract types"
```

---

### Task 2: Observer Contract Persistence

**Files:**
- Modify: `multi-agent/internal/observerstore/schema.sql`
- Modify: `multi-agent/internal/observerstore/store.go`
- Modify: `multi-agent/internal/observerstore/store_test.go`
- Modify: `multi-agent/internal/observerweb/server.go`
- Modify: `multi-agent/internal/observerweb/server_test.go`

- [ ] **Step 1: Write failing observer store tests**

Append to `multi-agent/internal/observerstore/store_test.go`:

```go
func TestTaskContractPersistence(t *testing.T) {
	s := testStore(t)

	body := json.RawMessage(`{"version":1,"conversation_id":"conv-1","intent":{"goal":"g","success_criteria":["s"]}}`)
	require.NoError(t, s.SaveTaskContract(TaskContractRecord{
		WorkspaceID:    "ws1",
		TaskID:         "task-1",
		ConversationID: "conv-1",
		OwnerAgentID:   "driver",
		Body:           body,
	}))

	got, err := s.GetTaskContract("ws1", "task-1")
	require.NoError(t, err)
	require.Equal(t, "conv-1", got.ConversationID)
	require.JSONEq(t, string(body), string(got.Body))
}

func TestResourceSnapshotPersistence(t *testing.T) {
	s := testStore(t)
	body := json.RawMessage(`{"generated_at":"now","agents":[{"agent_id":"a","display_name":"slave"}]}`)

	require.NoError(t, s.SaveResourceSnapshot(ResourceSnapshotRecord{
		WorkspaceID:  "ws1",
		SnapshotID:   "snap-1",
		OwnerAgentID: "driver",
		Body:         body,
	}))

	got, err := s.GetLatestResourceSnapshot("ws1")
	require.NoError(t, err)
	require.Equal(t, "snap-1", got.SnapshotID)
	require.JSONEq(t, string(body), string(got.Body))
}
```

- [ ] **Step 2: Run store tests and verify they fail**

Run:

```bash
cd /root/multi-agent/.worktrees/driver-master-contract-spec/multi-agent
/root/.local/go-toolchain/go/bin/go test ./internal/observerstore -run 'Test(TaskContract|ResourceSnapshot)Persistence'
```

Expected: FAIL with undefined `TaskContractRecord`, `SaveTaskContract`, `GetTaskContract`, `ResourceSnapshotRecord`, `SaveResourceSnapshot`, or `GetLatestResourceSnapshot`.

- [ ] **Step 3: Add schema tables**

Append to `multi-agent/internal/observerstore/schema.sql`:

```sql
CREATE TABLE IF NOT EXISTS task_contracts (
  workspace_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  conversation_id TEXT NOT NULL,
  owner_agent_id TEXT NOT NULL,
  body TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (workspace_id, task_id)
);

CREATE INDEX IF NOT EXISTS idx_task_contracts_conversation
ON task_contracts(workspace_id, conversation_id, updated_at);

CREATE TABLE IF NOT EXISTS resource_snapshots (
  workspace_id TEXT NOT NULL,
  snapshot_id TEXT NOT NULL,
  owner_agent_id TEXT NOT NULL,
  body TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (workspace_id, snapshot_id)
);

CREATE INDEX IF NOT EXISTS idx_resource_snapshots_latest
ON resource_snapshots(workspace_id, created_at);
```

- [ ] **Step 4: Add store models and methods**

Append to `multi-agent/internal/observerstore/store.go` near existing artifact/write models and methods:

```go
type TaskContractRecord struct {
	WorkspaceID    string          `json:"workspace_id"`
	TaskID         string          `json:"task_id"`
	ConversationID string          `json:"conversation_id"`
	OwnerAgentID   string          `json:"owner_agent_id"`
	Body           json.RawMessage `json:"body"`
	CreatedAt      string          `json:"created_at,omitempty"`
	UpdatedAt      string          `json:"updated_at,omitempty"`
}

type ResourceSnapshotRecord struct {
	WorkspaceID  string          `json:"workspace_id"`
	SnapshotID   string          `json:"snapshot_id"`
	OwnerAgentID string          `json:"owner_agent_id"`
	Body         json.RawMessage `json:"body"`
	CreatedAt    string          `json:"created_at,omitempty"`
}

func (s *Store) SaveTaskContract(rec TaskContractRecord) error {
	if rec.WorkspaceID == "" || rec.TaskID == "" || rec.OwnerAgentID == "" {
		return fmt.Errorf("workspace_id, task_id, and owner_agent_id are required")
	}
	if len(rec.Body) == 0 {
		return fmt.Errorf("body is required")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(`INSERT INTO task_contracts
		(workspace_id, task_id, conversation_id, owner_agent_id, body, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id, task_id) DO UPDATE SET
			conversation_id=excluded.conversation_id,
			owner_agent_id=excluded.owner_agent_id,
			body=excluded.body,
			updated_at=excluded.updated_at`,
		rec.WorkspaceID, rec.TaskID, rec.ConversationID, rec.OwnerAgentID, string(rec.Body), now, now)
	return err
}

func (s *Store) GetTaskContract(workspaceID, taskID string) (TaskContractRecord, error) {
	var rec TaskContractRecord
	var body string
	err := s.db.QueryRow(`SELECT workspace_id, task_id, conversation_id, owner_agent_id, body, created_at, updated_at
		FROM task_contracts WHERE workspace_id=? AND task_id=?`, workspaceID, taskID).
		Scan(&rec.WorkspaceID, &rec.TaskID, &rec.ConversationID, &rec.OwnerAgentID, &body, &rec.CreatedAt, &rec.UpdatedAt)
	if err != nil {
		return TaskContractRecord{}, err
	}
	rec.Body = json.RawMessage(body)
	return rec, nil
}

func (s *Store) SaveResourceSnapshot(rec ResourceSnapshotRecord) error {
	if rec.WorkspaceID == "" || rec.SnapshotID == "" || rec.OwnerAgentID == "" {
		return fmt.Errorf("workspace_id, snapshot_id, and owner_agent_id are required")
	}
	if len(rec.Body) == 0 {
		return fmt.Errorf("body is required")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(`INSERT INTO resource_snapshots
		(workspace_id, snapshot_id, owner_agent_id, body, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		rec.WorkspaceID, rec.SnapshotID, rec.OwnerAgentID, string(rec.Body), now)
	return err
}

func (s *Store) GetLatestResourceSnapshot(workspaceID string) (ResourceSnapshotRecord, error) {
	var rec ResourceSnapshotRecord
	var body string
	err := s.db.QueryRow(`SELECT workspace_id, snapshot_id, owner_agent_id, body, created_at
		FROM resource_snapshots WHERE workspace_id=? ORDER BY created_at DESC LIMIT 1`, workspaceID).
		Scan(&rec.WorkspaceID, &rec.SnapshotID, &rec.OwnerAgentID, &body, &rec.CreatedAt)
	if err != nil {
		return ResourceSnapshotRecord{}, err
	}
	rec.Body = json.RawMessage(body)
	return rec, nil
}
```

- [ ] **Step 5: Run store tests**

Run:

```bash
cd /root/multi-agent/.worktrees/driver-master-contract-spec/multi-agent
/root/.local/go-toolchain/go/bin/go test ./internal/observerstore
```

Expected: PASS.

- [ ] **Step 6: Write failing observer web tests**

Append to `multi-agent/internal/observerweb/server_test.go`:

```go
func TestTaskContractAPI(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "driver", Role: observer.RoleDriver, DisplayName: "Driver"}, "tok"))
	h := New(st)

	body := `{"task_id":"task-1","conversation_id":"conv-1","body":{"version":1,"intent":{"goal":"g","success_criteria":["s"]}}}`
	req := httptest.NewRequest(http.MethodPost, "/api/task-contracts", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusCreated, rr.Code)

	req = httptest.NewRequest(http.MethodGet, "/api/task-contracts/task-1", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var got observerstore.TaskContractRecord
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&got))
	require.Equal(t, "conv-1", got.ConversationID)
}
```

Add `strings` to the existing import block:

```go
import (
	"strings"
)
```

- [ ] **Step 7: Run web API test and verify it fails**

Run:

```bash
cd /root/multi-agent/.worktrees/driver-master-contract-spec/multi-agent
/root/.local/go-toolchain/go/bin/go test ./internal/observerweb -run TestTaskContractAPI
```

Expected: FAIL with 404 because `/api/task-contracts` is not registered.

- [ ] **Step 8: Add observer web routes**

Modify `multi-agent/internal/observerweb/server.go`.

Extend `Store`:

```go
SaveTaskContract(observerstore.TaskContractRecord) error
GetTaskContract(workspaceID, taskID string) (observerstore.TaskContractRecord, error)
SaveResourceSnapshot(observerstore.ResourceSnapshotRecord) error
GetLatestResourceSnapshot(workspaceID string) (observerstore.ResourceSnapshotRecord, error)
```

Register routes in `New`:

```go
mux.HandleFunc("/api/task-contracts", h.taskContracts)
mux.HandleFunc("/api/task-contracts/", h.taskContractByID)
mux.HandleFunc("/api/resource-snapshots", h.resourceSnapshots)
mux.HandleFunc("/api/resource-snapshots/latest", h.latestResourceSnapshot)
```

Add handlers:

```go
func (h *handler) taskContracts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	var req struct {
		TaskID         string          `json:"task_id"`
		ConversationID string          `json:"conversation_id"`
		Body           json.RawMessage `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	rec := observerstore.TaskContractRecord{
		WorkspaceID: agent.WorkspaceID, TaskID: req.TaskID,
		ConversationID: req.ConversationID, OwnerAgentID: agent.ID, Body: req.Body,
	}
	if err := h.s.SaveTaskContract(rec); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(rec)
}

func (h *handler) taskContractByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/task-contracts/")
	rec, err := h.s.GetTaskContract(agent.WorkspaceID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rec)
}
```

Add matching resource snapshot handlers:

```go
func (h *handler) resourceSnapshots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	var req struct {
		SnapshotID string          `json:"snapshot_id"`
		Body       json.RawMessage `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	rec := observerstore.ResourceSnapshotRecord{
		WorkspaceID: agent.WorkspaceID, SnapshotID: req.SnapshotID, OwnerAgentID: agent.ID, Body: req.Body,
	}
	if err := h.s.SaveResourceSnapshot(rec); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(rec)
}

func (h *handler) latestResourceSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	rec, err := h.s.GetLatestResourceSnapshot(agent.WorkspaceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rec)
}
```

- [ ] **Step 9: Run observer tests**

Run:

```bash
cd /root/multi-agent/.worktrees/driver-master-contract-spec/multi-agent
/root/.local/go-toolchain/go/bin/go test ./internal/observerstore ./internal/observerweb
```

Expected: PASS.

- [ ] **Step 10: Commit**

```bash
git add internal/observerstore internal/observerweb
git commit -m "Persist task contracts in observer"
```

---

### Task 3: Driver Contract Submission Tool

**Files:**
- Create: `multi-agent/internal/driver/contract_tools.go`
- Modify: `multi-agent/internal/driver/observer_relay.go`
- Modify: `multi-agent/internal/driver/tools.go`
- Modify: `multi-agent/internal/driver/tools_test.go`

- [ ] **Step 1: Write failing driver test**

Append to `multi-agent/internal/driver/tools_test.go`:

```go
func TestSubmitContractTaskRoutesToSingleMatchingSlave(t *testing.T) {
	var lastDelegate agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-driver", DisplayName: "driver", Status: "available", Card: json.RawMessage(`{"skills":[]}`)},
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["chat"],"short_id":"sa"}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			lastDelegate = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
	}
	tools := newTestTools(t, sdk)
	tc := contract.TaskContract{
		Version:        1,
		ConversationID: "conv-1",
		Intent: contract.IntentSpec{
			Goal:            "write a helper",
			SuccessCriteria: []string{"helper is saved"},
		},
		DataContract: contract.DataContract{
			WriteTargets: []contract.WriteTarget{{Type: contract.WriteTargetArtifact, Kind: "code", Name: "helper.go"}},
		},
		CapabilityRequirements: contract.CapabilityRequirements{Skills: []string{"chat"}},
	}
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	var tool Tool
	for _, candidate := range tools.All() {
		if candidate.Name() == "submit_contract_task" {
			tool = candidate
		}
	}
	require.NotNil(t, tool)

	out, err := tool.Call(context.Background(), raw)
	require.NoError(t, err)
	require.Contains(t, string(out), `"task_id":"task-1"`)
	require.Equal(t, "slave-a", lastDelegate.TargetID)
	require.Equal(t, "chat", lastDelegate.Skill)
	require.Contains(t, lastDelegate.Prompt, contract.EnvelopeStart)
}
```

Add imports if missing:

```go
import (
	"github.com/yourorg/multi-agent/internal/contract"
)
```

- [ ] **Step 2: Run driver test and verify it fails**

Run:

```bash
cd /root/multi-agent/.worktrees/driver-master-contract-spec/multi-agent
/root/.local/go-toolchain/go/bin/go test ./internal/driver -run TestSubmitContractTaskRoutesToSingleMatchingSlave
```

Expected: FAIL because `submit_contract_task` is not registered.

- [ ] **Step 3: Add observer relay helpers for contracts**

Append to `multi-agent/internal/driver/observer_relay.go`:

```go
type observerTaskContractSave struct {
	TaskID         string          `json:"task_id"`
	ConversationID string          `json:"conversation_id"`
	Body           json.RawMessage `json:"body"`
}

type observerResourceSnapshotSave struct {
	SnapshotID string          `json:"snapshot_id"`
	Body       json.RawMessage `json:"body"`
}

func (r *ObserverRelay) SaveTaskContract(ctx context.Context, taskID, conversationID string, body json.RawMessage) error {
	if r == nil {
		return nil
	}
	payload, _ := json.Marshal(observerTaskContractSave{
		TaskID: taskID, ConversationID: conversationID, Body: body,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/api/task-contracts", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("save task contract status %d", resp.StatusCode)
	}
	return nil
}

func (r *ObserverRelay) SaveResourceSnapshot(ctx context.Context, body json.RawMessage) error {
	if r == nil {
		return nil
	}
	snapshotID := "snap-" + strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
	payload, _ := json.Marshal(observerResourceSnapshotSave{SnapshotID: snapshotID, Body: body})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/api/resource-snapshots", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("save resource snapshot status %d", resp.StatusCode)
	}
	return nil
}
```

Add `strconv` to the existing import block:

```go
import (
	"strconv"
)
```

- [ ] **Step 4: Add submit contract tool**

Create `multi-agent/internal/driver/contract_tools.go`:

```go
package driver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/contract"
)

type submitContractTaskTool struct{ t *Tools }

func (s *submitContractTaskTool) Name() string { return "submit_contract_task" }

func (s *submitContractTaskTool) Description() string {
	return "Submit a validated TaskContract. Routes direct-first when exactly one slave matches, otherwise submits to master."
}

func (s *submitContractTaskTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"contract":{"type":"object"},
			"target_display_name":{"type":"string"},
			"skill":{"type":"string"},
			"timeout_sec":{"type":"integer"}
		},
		"required":["contract"],
		"additionalProperties":false
	}`)
}

func (s *submitContractTaskTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Contract          contract.TaskContract `json:"contract"`
		TargetDisplayName string                `json:"target_display_name"`
		Skill             string                `json:"skill"`
		TimeoutSec        int                   `json:"timeout_sec"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
	}
	args.Contract.ApplyDefaults()
	if err := args.Contract.Validate(); err != nil {
		return nil, &MCPToolError{Message: "invalid contract: " + err.Error()}
	}
	cards, err := s.t.sdk.DiscoverAgents(ctx)
	if err != nil {
		return nil, &MCPToolError{Message: "discover agents: " + err.Error()}
	}
	snapshot := contract.NewResourceSnapshot(cards, s.t.cfg.Credentials.SandboxID)
	snapshotBody, _ := json.Marshal(snapshot)
	if err := s.t.observerRelay().SaveResourceSnapshot(ctx, snapshotBody); err != nil {
		return nil, &MCPToolError{Message: "observer save resource snapshot: " + err.Error()}
	}
	targetID, targetName, skill, err := s.route(ctx, args.Contract, cards, args.TargetDisplayName, args.Skill)
	if err != nil {
		return nil, err
	}
	body := contractSummary(args.Contract, snapshot)
	prompt, err := contract.EncodeEnvelope(args.Contract, body)
	if err != nil {
		return nil, &MCPToolError{Message: "encode contract: " + err.Error()}
	}
	timeout := args.TimeoutSec
	if timeout == 0 {
		timeout = s.t.cfg.DriverDefaults.TaskTimeoutSec
	}
	resp, err := s.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       targetID,
		Skill:          skill,
		Prompt:         prompt,
		TimeoutSeconds: timeout,
	})
	if err != nil {
		return nil, &MCPToolError{Message: "delegate: " + err.Error()}
	}
	contractBody, _ := json.Marshal(args.Contract)
	if err := s.t.observerRelay().SaveTaskContract(ctx, resp.TaskID, args.Contract.ConversationID, contractBody); err != nil {
		return nil, &MCPToolError{Message: "observer save task contract: " + err.Error()}
	}
	return json.Marshal(map[string]interface{}{
		"task_id":             resp.TaskID,
		"target_id":           targetID,
		"target_display_name": targetName,
		"skill":               skill,
		"resource_snapshot":   snapshot,
	})
}

func (s *submitContractTaskTool) route(ctx context.Context, tc contract.TaskContract, cards []agentsdk.AgentCard, override, skillOverride string) (string, string, string, error) {
	if override != "" {
		id, name, _, _, err := s.t.resolveTarget(ctx, override)
		if err != nil {
			return "", "", "", err
		}
		return id, name, firstNonEmpty(skillOverride, "fanout"), nil
	}
	if tc.ExecutionPolicy.Routing == contract.RoutingMasterOnly {
		id, name, _, _, err := s.t.resolveTarget(ctx, "")
		if err != nil {
			return "", "", "", err
		}
		return id, name, firstNonEmpty(skillOverride, "fanout"), nil
	}
	matches := directMatches(cards, s.t.cfg.Credentials.SandboxID, tc.CapabilityRequirements.Skills)
	if len(matches) == 1 {
		return matches[0].AgentID, matches[0].DisplayName, firstNonEmpty(skillOverride, "chat"), nil
	}
	id, name, _, _, err := s.t.resolveTarget(ctx, "")
	if err != nil {
		return "", "", "", err
	}
	return id, name, firstNonEmpty(skillOverride, "fanout"), nil
}

func directMatches(cards []agentsdk.AgentCard, selfID string, required []string) []agentsdk.AgentCard {
	var out []agentsdk.AgentCard
	for _, c := range cards {
		if c.AgentID == selfID || hasSkill(c, "fanout") || hasSkill(c, "route") {
			continue
		}
		if hasAllSkills(c, required) {
			out = append(out, c)
		}
	}
	return out
}

func hasAllSkills(c agentsdk.AgentCard, required []string) bool {
	for _, want := range required {
		if !hasSkill(c, want) {
			return false
		}
	}
	return true
}

func contractSummary(tc contract.TaskContract, snapshot contract.ResourceSnapshot) string {
	return fmt.Sprintf("Execute TaskContract goal: %s\nAvailable agents in snapshot: %d", tc.Intent.Goal, len(snapshot.Agents))
}
```

- [ ] **Step 5: Register the new tool**

Modify `multi-agent/internal/driver/tools.go` in `All()`:

```go
func (t *Tools) All() []Tool {
	return []Tool{
		&listAgentsTool{t},
		&submitTaskTool{t},
		&submitContractTaskTool{t},
		&getTaskTool{t},
		&waitTaskTool{t},
		&tailSubtasksTool{t},
		&cancelTaskTool{t},
	}
}
```

- [ ] **Step 6: Run driver tests**

Run:

```bash
cd /root/multi-agent/.worktrees/driver-master-contract-spec/multi-agent
/root/.local/go-toolchain/go/bin/go test ./internal/driver
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/driver
git commit -m "Add driver contract submission tool"
```

---

### Task 4: Master Contract Policy Validation

**Files:**
- Create: `multi-agent/internal/orchestrator/contract_policy.go`
- Modify: `multi-agent/internal/orchestrator/dag.go`
- Modify: `multi-agent/internal/orchestrator/route.go`
- Modify: `multi-agent/internal/orchestrator/fanout.go`
- Create: `multi-agent/internal/orchestrator/contract_policy_test.go`

- [ ] **Step 1: Write failing policy tests**

Create `multi-agent/internal/orchestrator/contract_policy_test.go`:

```go
package orchestrator

import (
	"testing"

	"github.com/yourorg/multi-agent/internal/contract"
	"github.com/yourorg/multi-agent/internal/planner"
)

func TestContractFromPromptRejectsAllowMasterFalse(t *testing.T) {
	tc := contract.TaskContract{
		Version:        1,
		ConversationID: "conv-1",
		Intent: contract.IntentSpec{
			Goal:            "single-agent work",
			SuccessCriteria: []string{"done"},
		},
		DataContract: contract.DataContract{
			WriteTargets: []contract.WriteTarget{{Type: contract.WriteTargetArtifact, Kind: "document", Name: "out.md"}},
		},
	}
	tc.ApplyDefaults()
	tc.ExecutionPolicy.AllowMaster = contract.Bool(false)
	prompt, err := contract.EncodeEnvelope(tc, "body")
	if err != nil {
		t.Fatalf("EncodeEnvelope: %v", err)
	}

	_, _, err = contractPolicyFromPrompt(prompt)
	if err == nil {
		t.Fatalf("expected error")
	}
	if err.Error() != "contract execution_policy.allow_master is false" {
		t.Fatalf("error = %v", err)
	}
}

func TestValidatePlanWithPolicyRejectsBuildMCP(t *testing.T) {
	tc := contract.TaskContract{
		Version:        1,
		ConversationID: "conv-1",
		Intent: contract.IntentSpec{
			Goal:            "generate code",
			SuccessCriteria: []string{"artifact saved"},
		},
		DataContract: contract.DataContract{
			WriteTargets: []contract.WriteTarget{{Type: contract.WriteTargetArtifact, Kind: "code", Name: "helper.go"}},
		},
	}
	tc.ApplyDefaults()
	nodes := []planner.Node{{ID: "n1", TargetID: "slave", Skill: "build_mcp"}}

	err := ValidateWithContractPolicy(nodes, tc.ExecutionPolicy)
	if err == nil {
		t.Fatalf("expected policy error")
	}
	if err.Error() != "build_mcp node n1 rejected by contract policy" {
		t.Fatalf("error = %v", err)
	}
}

func TestValidatePlanWithPolicyRejectsTooManyNodes(t *testing.T) {
	p := contract.ExecutionPolicy{
		AllowMaster:        contract.Bool(true),
		AllowCodeArtifacts: true,
		CodePersistence:    contract.CodePersistenceObserverArtifactStore,
		WriteMode:          contract.WriteModeArtifactOnly,
		Routing:            contract.RoutingMasterOnly,
		MaxDAGNodes:        1,
		MaxDepth:           3,
		MaxConcurrency:     3,
	}
	nodes := []planner.Node{
		{ID: "n1", TargetID: "slave"},
		{ID: "n2", TargetID: "slave"},
	}

	err := ValidateWithContractPolicy(nodes, p)
	if err == nil {
		t.Fatalf("expected policy error")
	}
	if err.Error() != "plan too large for contract: 2 nodes (max 1)" {
		t.Fatalf("error = %v", err)
	}
}
```

- [ ] **Step 2: Run policy tests and verify they fail**

Run:

```bash
cd /root/multi-agent/.worktrees/driver-master-contract-spec/multi-agent
/root/.local/go-toolchain/go/bin/go test ./internal/orchestrator -run 'TestContract|TestValidatePlanWithPolicy'
```

Expected: FAIL with undefined `contractPolicyFromPrompt` and `ValidateWithContractPolicy`.

- [ ] **Step 3: Add contract policy helpers**

Create `multi-agent/internal/orchestrator/contract_policy.go`:

```go
package orchestrator

import (
	"fmt"

	"github.com/yourorg/multi-agent/internal/contract"
	"github.com/yourorg/multi-agent/internal/planner"
)

func contractPolicyFromPrompt(prompt string) (contract.TaskContract, string, error) {
	tc, body, ok, err := contract.DecodeEnvelope(prompt)
	if err != nil {
		return contract.TaskContract{}, "", err
	}
	if !ok {
		return contract.TaskContract{}, prompt, nil
	}
	if !tc.ExecutionPolicy.AllowsMaster() {
		return contract.TaskContract{}, "", fmt.Errorf("contract execution_policy.allow_master is false")
	}
	return tc, body, nil
}

func ValidateWithContractPolicy(nodes []planner.Node, policy contract.ExecutionPolicy) error {
	if err := Validate(nodes); err != nil {
		return err
	}
	if policy.MaxDAGNodes > 0 && len(nodes) > policy.MaxDAGNodes {
		return fmt.Errorf("plan too large for contract: %d nodes (max %d)", len(nodes), policy.MaxDAGNodes)
	}
	for _, n := range nodes {
		if n.Skill == "build_mcp" && !policy.AllowBuildMCP {
			return fmt.Errorf("build_mcp node %s rejected by contract policy", n.ID)
		}
	}
	return nil
}
```

- [ ] **Step 4: Apply contract policy in route**

Modify `multi-agent/internal/orchestrator/route.go` near the start of `runRoute`:

```go
func (o *Orchestrator) runRoute(ctx context.Context, t executor.Task) (executor.Result, error) {
	tc, prompt, err := contractPolicyFromPrompt(t.Prompt)
	if err != nil {
		return executor.Result{}, err
	}
	if tc.Version != 0 {
		t.Prompt = prompt
	}
	agents, err := o.discoverFiltered(ctx)
```

- [ ] **Step 5: Apply contract policy in fanout**

Modify `multi-agent/internal/orchestrator/fanout.go` near the start of `runFanout`:

```go
func (o *Orchestrator) runFanout(ctx context.Context, t executor.Task) (executor.Result, error) {
	tc, prompt, err := contractPolicyFromPrompt(t.Prompt)
	if err != nil {
		return executor.Result{}, err
	}
	if tc.Version != 0 {
		t.Prompt = prompt
	}
	agents, err := o.discoverFiltered(ctx)
```

Modify the validation after planning:

```go
if tc.Version != 0 {
	if err := ValidateWithContractPolicy(plan, tc.ExecutionPolicy); err != nil {
		return executor.Result{}, fmt.Errorf("invalid plan: %w", err)
	}
} else if err := Validate(plan); err != nil {
	return executor.Result{}, fmt.Errorf("invalid plan: %w", err)
}
```

- [ ] **Step 6: Run orchestrator tests**

Run:

```bash
cd /root/multi-agent/.worktrees/driver-master-contract-spec/multi-agent
/root/.local/go-toolchain/go/bin/go test ./internal/orchestrator
```

Expected: PASS.

- [ ] **Step 7: Run planner and orchestrator package checks together**

Run:

```bash
cd /root/multi-agent/.worktrees/driver-master-contract-spec/multi-agent
/root/.local/go-toolchain/go/bin/go test ./internal/planner ./internal/orchestrator
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/orchestrator
git commit -m "Validate master plans against task contracts"
```

---

### Task 5: Persist Workspace ID in Agent Credentials

**Files:**
- Modify: `multi-agent/internal/config/config.go`
- Modify: `multi-agent/internal/config/config_test.go`
- Modify: `multi-agent/internal/tunnel/tunnel.go`
- Modify: `multi-agent/internal/tunnel/tunnel_test.go`

- [ ] **Step 1: Write failing config round-trip test**

Append to `multi-agent/internal/config/config_test.go`:

```go
func TestCredentialsWorkspaceIDRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
server: { url: "https://agentserver.test" }
credentials:
  sandbox_id: sbx
  tunnel_token: tt
  proxy_token: pt
  workspace_id: ws-1
  short_id: short
discovery:
  display_name: agent
  skills: [chat]
fanout: { max_concurrency: 1 }
`), 0o600))

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "ws-1", cfg.Credentials.WorkspaceID)
	require.NoError(t, cfg.Save(path))

	reloaded, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "ws-1", reloaded.Credentials.WorkspaceID)
}
```

Add imports if missing:

```go
import (
	"os"
	"path/filepath"
)
```

- [ ] **Step 2: Run config test and verify it fails**

Run:

```bash
cd /root/multi-agent/.worktrees/driver-master-contract-spec/multi-agent
/root/.local/go-toolchain/go/bin/go test ./internal/config -run TestCredentialsWorkspaceIDRoundTrip
```

Expected: FAIL because `config.Credentials` does not expose `WorkspaceID`.

- [ ] **Step 3: Add workspace ID to shared credentials**

Modify `multi-agent/internal/config/config.go`:

```go
type Credentials struct {
	SandboxID   string `yaml:"sandbox_id"`
	TunnelToken string `yaml:"tunnel_token"`
	ProxyToken  string `yaml:"proxy_token"`
	WorkspaceID string `yaml:"workspace_id"`
	ShortID     string `yaml:"short_id"`
}
```

- [ ] **Step 4: Persist workspace ID during tunnel registration**

Modify `multi-agent/internal/tunnel/tunnel.go` in `EnsureRegistered` after `reg.ProxyToken`:

```go
t.cfg.Credentials.WorkspaceID = reg.WorkspaceID
```

- [ ] **Step 5: Update tunnel registration test**

Modify `multi-agent/internal/tunnel/tunnel_test.go` in `TestEnsureRegistered_PersistsCredentials` to assert:

```go
require.Equal(t, "ws-1", reloaded.Credentials.WorkspaceID)
```

Ensure the fake registration response in that test includes:

```json
"workspace_id": "ws-1"
```

- [ ] **Step 6: Run config and tunnel tests**

Run:

```bash
cd /root/multi-agent/.worktrees/driver-master-contract-spec/multi-agent
/root/.local/go-toolchain/go/bin/go test ./internal/config ./internal/tunnel
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/config internal/tunnel
git commit -m "Persist workspace id in agent credentials"
```

---

### Task 6: Container Development and E2E Scaffolding

**Files:**
- Create: `multi-agent/dev/agent-runtime/Dockerfile`
- Create: `multi-agent/dev/compose.distributed.yaml`
- Create: `multi-agent/dev/configs/master.example.yaml`
- Create: `multi-agent/dev/configs/driver.example.yaml`
- Create: `multi-agent/dev/configs/slave-a.example.yaml`
- Create: `multi-agent/dev/configs/slave-b.example.yaml`
- Create: `multi-agent/tests/scripts/distributed_compose_test.go`

- [ ] **Step 1: Write failing static compose test**

Create `multi-agent/tests/scripts/distributed_compose_test.go`:

```go
package scripts

import (
	"os"
	"strings"
	"testing"
)

func TestDistributedComposeScaffold(t *testing.T) {
	data, err := os.ReadFile("../../dev/compose.distributed.yaml")
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"master:",
		"driver:",
		"slave-a:",
		"slave-b:",
		"ANTHROPIC_BASE_URL: https://code.ai.cs.ac.cn",
		"ANTHROPIC_API_KEY: ${ANTHROPIC_API_KEY}",
		"./:/workspace/multi-agent",
		"./dev/configs/master.yaml:/config/config.yaml",
		"./dev/configs/driver.yaml:/config/config.yaml",
		"./dev/configs/slave-a.yaml:/config/config.yaml",
		"./dev/configs/slave-b.yaml:/config/config.yaml",
		"restart: unless-stopped",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("compose missing %q", want)
		}
	}
}

func TestAgentRuntimeInstallsClaudeCodeAndSkipsOnboarding(t *testing.T) {
	data, err := os.ReadFile("../../dev/agent-runtime/Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"npm install -g @anthropic-ai/claude-code",
		"ANTHROPIC_BASE_URL=https://code.ai.cs.ac.cn",
		`"hasCompletedOnboarding":true`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Dockerfile missing %q", want)
		}
	}
}
```

- [ ] **Step 2: Run static test and verify it fails**

Run:

```bash
cd /root/multi-agent/.worktrees/driver-master-contract-spec/multi-agent
/root/.local/go-toolchain/go/bin/go test ./tests/scripts -run 'TestDistributedCompose|TestAgentRuntime'
```

Expected: FAIL because `dev/compose.distributed.yaml` and `dev/agent-runtime/Dockerfile` do not exist.

- [ ] **Step 3: Add agent runtime Dockerfile**

Create `multi-agent/dev/agent-runtime/Dockerfile`:

```dockerfile
FROM golang:1.24-bookworm

RUN apt-get update && apt-get install -y \
    ca-certificates \
    curl \
    git \
    nodejs \
    npm \
    sqlite3 \
    zsh \
  && rm -rf /var/lib/apt/lists/*

RUN npm install -g @anthropic-ai/claude-code

ENV ANTHROPIC_BASE_URL=https://code.ai.cs.ac.cn

RUN mkdir -p /root \
  && printf '{"hasCompletedOnboarding":true}\n' > /root/.claude.json

WORKDIR /workspace/multi-agent
```

- [ ] **Step 4: Add distributed compose file**

Create `multi-agent/dev/compose.distributed.yaml`:

```yaml
services:
  master:
    build:
      context: .
      dockerfile: dev/agent-runtime/Dockerfile
    working_dir: /workspace/multi-agent
    volumes:
      - ./:/workspace/multi-agent
      - ./dev/configs/master.yaml:/config/config.yaml
    environment:
      ANTHROPIC_BASE_URL: https://code.ai.cs.ac.cn
      ANTHROPIC_API_KEY: ${ANTHROPIC_API_KEY}
    command: zsh -lc "go run ./cmd/master-agent /config/config.yaml"
    restart: unless-stopped

  driver:
    build:
      context: .
      dockerfile: dev/agent-runtime/Dockerfile
    working_dir: /workspace/multi-agent
    volumes:
      - ./:/workspace/multi-agent
      - ./dev/configs/driver.yaml:/config/config.yaml
    environment:
      ANTHROPIC_BASE_URL: https://code.ai.cs.ac.cn
      ANTHROPIC_API_KEY: ${ANTHROPIC_API_KEY}
    command: zsh -lc "go run ./cmd/driver-agent /config/config.yaml"
    restart: unless-stopped

  slave-a:
    build:
      context: .
      dockerfile: dev/agent-runtime/Dockerfile
    working_dir: /workspace/multi-agent
    volumes:
      - ./:/workspace/multi-agent
      - ./dev/configs/slave-a.yaml:/config/config.yaml
    environment:
      ANTHROPIC_BASE_URL: https://code.ai.cs.ac.cn
      ANTHROPIC_API_KEY: ${ANTHROPIC_API_KEY}
    command: zsh -lc "go run ./cmd/slave-agent /config/config.yaml"
    restart: unless-stopped

  slave-b:
    build:
      context: .
      dockerfile: dev/agent-runtime/Dockerfile
    working_dir: /workspace/multi-agent
    volumes:
      - ./:/workspace/multi-agent
      - ./dev/configs/slave-b.yaml:/config/config.yaml
    environment:
      ANTHROPIC_BASE_URL: https://code.ai.cs.ac.cn
      ANTHROPIC_API_KEY: ${ANTHROPIC_API_KEY}
    command: zsh -lc "go run ./cmd/slave-agent /config/config.yaml"
    restart: unless-stopped
```

- [ ] **Step 5: Add config examples**

Create `multi-agent/dev/configs/master.example.yaml`:

```yaml
server:
  url: https://your-agentserver.example
credentials:
  sandbox_id: ""
  tunnel_token: ""
  proxy_token: ""
  workspace_id: ""
  short_id: ""
claude:
  bin: claude
planner:
  bin: claude
  timeout_sec: 60
discovery:
  display_name: master-dev
  description: Development master agent
  skills: [route, fanout]
fanout:
  max_concurrency: 3
  default_policy: best_effort
observer:
  enabled: true
  url: http://observer:8080
  workspace_id: dev
  agent_id: master-dev
  token: master-token
```

Create `multi-agent/dev/configs/driver.example.yaml`:

```yaml
server:
  url: https://your-agentserver.example
credentials:
  sandbox_id: ""
  tunnel_token: ""
  proxy_token: ""
  workspace_id: ""
  short_id: ""
driver_defaults:
  target_display_name: master-dev
  task_timeout_sec: 600
  artifact_transport: observer_lazy
observer:
  enabled: true
  url: http://observer:8080
  workspace_id: dev
  agent_id: driver-dev
  token: driver-token
discovery:
  display_name: driver-dev
  description: Development driver agent
  skills: []
```

Create `multi-agent/dev/configs/slave-a.example.yaml` and `multi-agent/dev/configs/slave-b.example.yaml` with display names `slave-a-dev` and `slave-b-dev`:

```yaml
server:
  url: https://your-agentserver.example
credentials:
  sandbox_id: ""
  tunnel_token: ""
  proxy_token: ""
  workspace_id: ""
  short_id: ""
claude:
  bin: claude
discovery:
  display_name: slave-a-dev
  description: Development slave agent
  skills: [chat, mcp]
fanout:
  max_concurrency: 1
observer:
  enabled: true
  url: http://observer:8080
  workspace_id: dev
  agent_id: slave-a-dev
  token: slave-a-token
resources:
  memory_gb: 16
  tags: [go, dev]
```

For `slave-b.example.yaml`, use `display_name: slave-b-dev`, `agent_id: slave-b-dev`, and `token: slave-b-token`.

- [ ] **Step 6: Run static scaffold tests**

Run:

```bash
cd /root/multi-agent/.worktrees/driver-master-contract-spec/multi-agent
/root/.local/go-toolchain/go/bin/go test ./tests/scripts -run 'TestDistributedCompose|TestAgentRuntime'
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add dev tests/scripts/distributed_compose_test.go
git commit -m "Add distributed agent container scaffold"
```

---

### Task 7: Full Verification

**Files:**
- No new files.

- [ ] **Step 1: Run package tests**

Run:

```bash
cd /root/multi-agent/.worktrees/driver-master-contract-spec/multi-agent
/root/.local/go-toolchain/go/bin/go test ./...
```

Expected: PASS.

- [ ] **Step 2: Run vet**

Run:

```bash
cd /root/multi-agent/.worktrees/driver-master-contract-spec/multi-agent
/root/.local/go-toolchain/go/bin/go vet ./...
```

Expected: exit code 0 with no output.

- [ ] **Step 3: Run contract tests**

Run:

```bash
cd /root/multi-agent/.worktrees/driver-master-contract-spec/multi-agent
/root/.local/go-toolchain/go/bin/go test -tags=contract ./tests/contract/...
```

Expected: PASS.

- [ ] **Step 4: Check git status**

Run:

```bash
git status --short
```

Expected: clean output.

---

## Review Checklist

- `allow_build_mcp=false` rejects build MCP plans but still permits code artifacts.
- Driver can submit a structured contract without relying on local business files.
- Master validates contract policy before dispatch.
- Observer stores contracts and snapshots under workspace scope.
- Container scaffold preserves agent credentials through mounted config files.
- Shared source mount exists only for development and binary iteration.
- No task data path depends on shared directories.
