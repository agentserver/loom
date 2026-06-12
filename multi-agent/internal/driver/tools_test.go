package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/contract"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/orchestration"
)

// fakeSDK satisfies SDKClient for tests.
type fakeSDK struct {
	discoverFunc  func() ([]agentsdk.AgentCard, error)
	delegateFunc  func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error)
	getTaskFunc   func(id string, includeOutput bool) (*agentsdk.TaskInfo, error)
	peerProxyFunc func(method, target, path string, body io.Reader) (*http.Response, error)
}

func (f *fakeSDK) DiscoverAgents(ctx context.Context) ([]agentsdk.AgentCard, error) {
	return f.discoverFunc()
}
func (f *fakeSDK) DelegateTask(ctx context.Context, req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
	return f.delegateFunc(req)
}
func (f *fakeSDK) GetTask(ctx context.Context, id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
	return f.getTaskFunc(id, includeOutput)
}
func (f *fakeSDK) PeerProxy(ctx context.Context, method, target, path string, body io.Reader) (*http.Response, error) {
	return f.peerProxyFunc(method, target, path, body)
}

type fakeObserver struct {
	events []observer.Event
}

func (f *fakeObserver) Emit(ev observer.Event) {
	f.events = append(f.events, ev)
}

// Token satisfies the driver.TokenSource interface so the tools' observer
// progress helper authenticates against the fake observer HTTP server. The
// fake server itself doesn't validate the value, only that one is present.
func (f *fakeObserver) Token() string { return "fake-token" }

type fakeContractRunner struct {
	prompt string
	result orchestration.RunnerResult
	err    error
}

func (f *fakeContractRunner) Run(ctx context.Context, prompt string) (orchestration.RunnerResult, error) {
	f.prompt = prompt
	return f.result, f.err
}

type stubTokenSource string

func (s stubTokenSource) Token() string { return string(s) }

func newTestTools(t *testing.T, sdk SDKClient) *Tools {
	return newTestToolsWithObserver(t, sdk, nil)
}

func newTestToolsWithObserver(t *testing.T, sdk SDKClient, obs ObserverSink) *Tools {
	t.Helper()
	dir := t.TempDir()
	a, err := NewAuditLog(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	j, err := NewTaskJournal(filepath.Join(dir, "driver-tasks.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	cfg := &Config{}
	cfg.Server.URL = "https://srv.example.com"
	cfg.Credentials.ShortID = "drv-001"
	cfg.Credentials.SandboxID = "sbx-driver"
	cfg.DriverDefaults.TaskTimeoutSec = 600
	cfg.DriverDefaults.AuditLogDir = dir // expose so cache root and audit log path are predictable
	tools := NewTools(NewFileRegistry(50000), a, sdk, cfg, obs)
	tools.SetTaskJournal(j)
	return tools
}

func submitContractToolForTest(t *testing.T, tools *Tools) Tool {
	t.Helper()
	for _, candidate := range tools.All() {
		if candidate.Name() == "submit_contract_task" {
			return candidate
		}
	}
	t.Fatal("submit_contract_task tool not registered")
	return nil
}

func toolByName(t *testing.T, tools *Tools, name string) Tool {
	t.Helper()
	for _, candidate := range tools.All() {
		if candidate.Name() == name {
			return candidate
		}
	}
	t.Fatalf("%s tool not registered", name)
	return nil
}

func TestSubmitTaskRecordsDelegatedTask(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{
					AgentID:     "agent-1",
					DisplayName: "master-1",
					Status:      "available",
					Card:        json.RawMessage(`{"skills":["fanout"]}`),
				},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-submit", SessionID: "session-1", Status: "submitted"}, nil
		},
	}
	tools := newTestTools(t, sdk)

	_, err := toolByName(t, tools, "submit_task").Call(context.Background(), json.RawMessage(`{"prompt":"do work","skill":"chat"}`))
	require.NoError(t, err)

	records, err := tools.taskJournal.Recent(1, "task-submit")
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "submit_task", records[0].Tool)
	require.Equal(t, "agent-1", records[0].TargetID)
	require.Equal(t, "master-1", records[0].TargetDisplayName)
	require.Equal(t, "chat", records[0].Skill)
	require.Equal(t, "session-1", records[0].SessionID)
	require.False(t, records[0].Wait)
}

func testTaskContract() contract.TaskContract {
	return contract.TaskContract{
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
}

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
	tc := testTaskContract()
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	tool := submitContractToolForTest(t, tools)

	out, err := tool.Call(context.Background(), raw)
	require.NoError(t, err)
	require.Contains(t, string(out), `"task_id":"task-1"`)
	require.Equal(t, "slave-a", lastDelegate.TargetID)
	require.Equal(t, "chat", lastDelegate.Skill)
	require.Contains(t, lastDelegate.Prompt, contract.EnvelopeStart)
}

func TestSubmitContractTaskReturnsDirectSlaveRoute(t *testing.T) {
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
	tc := testTaskContract()
	tc.ExecutionPolicy.Routing = contract.RoutingDirectFirst
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	out, err := submitContractToolForTest(t, newTestTools(t, sdk)).Call(context.Background(), raw)
	require.NoError(t, err)
	require.Contains(t, string(out), `"route":"direct_slave"`)
	require.Equal(t, "slave-a", lastDelegate.TargetID)
	require.Equal(t, "chat", lastDelegate.Skill)
}

func TestSubmitContractTaskReturnsMasterFanoutRoute(t *testing.T) {
	var lastDelegate agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "m1", DisplayName: "master", Status: "available", Card: json.RawMessage(`{"skills":["fanout"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			lastDelegate = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
	}
	tc := testTaskContract()
	tc.ExecutionPolicy.Routing = contract.RoutingMasterOnly
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	out, err := submitContractToolForTest(t, newTestTools(t, sdk)).Call(context.Background(), raw)
	require.NoError(t, err)
	require.Contains(t, string(out), `"route":"master_fanout"`)
	require.Equal(t, "m1", lastDelegate.TargetID)
	require.Equal(t, "fanout", lastDelegate.Skill)
}

func TestSubmitContractTaskMasterOnlyStillDelegatesToMasterFanout(t *testing.T) {
	var lastDelegate agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "m1", DisplayName: "master", Status: "available", Card: json.RawMessage(`{"skills":["fanout"]}`)},
				{AgentID: "s1", DisplayName: "slave", Status: "available", Card: json.RawMessage(`{"skills":["chat"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			lastDelegate = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
	}
	tc := testTaskContract()
	tc.ExecutionPolicy.Routing = contract.RoutingMasterOnly
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	out, err := submitContractToolForTest(t, newTestTools(t, sdk)).Call(context.Background(), raw)
	require.NoError(t, err)
	require.Contains(t, string(out), `"route":"master_fanout"`)
	require.Equal(t, "m1", lastDelegate.TargetID)
	require.Equal(t, "fanout", lastDelegate.Skill)
	require.Contains(t, lastDelegate.Prompt, contract.EnvelopeStart)
}

func TestSubmitContractTaskUsesDriverFanoutWhenRecommended(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			// Two slaves both satisfy the tool, so directContractCapabilityMatches returns >1
			// and the route becomes driver_fanout (direct_first routing, no missing tools).
			return []agentsdk.AgentCard{
				{AgentID: "sbx-driver", DisplayName: "driver", Status: "available"},
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["chat"],"mcp_tools":[{"server":"csv_profiler","name":"profile_orders_csv"}]}`)},
				{AgentID: "slave-b", DisplayName: "slave-b", Status: "available", Card: json.RawMessage(`{"skills":["chat"],"mcp_tools":[{"server":"csv_profiler","name":"profile_orders_csv"}]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			t.Fatalf("driver_fanout route must not delegate")
			return nil, nil
		},
	}
	tc := testTaskContract()
	tc.ExecutionPolicy.Routing = contract.RoutingDirectFirst
	tc.CapabilityRequirements.Skills = nil
	tc.CapabilityRequirements.Tools = []string{"csv_profiler/profile_orders_csv"}
	raw, err := json.Marshal(map[string]interface{}{
		"contract": tc,
		"prompt":   "analyze refunds",
	})
	require.NoError(t, err)

	tools := newTestTools(t, sdk)
	runner := &fakeContractRunner{result: orchestration.RunnerResult{Summary: "driver summary"}}
	tools.SetContractRunner(runner)

	out, err := submitContractToolForTest(t, tools).Call(context.Background(), raw)
	require.NoError(t, err)
	require.Contains(t, string(out), `"route":"driver_fanout"`)
	require.Contains(t, string(out), `"summary":"driver summary"`)
	require.Contains(t, runner.prompt, contract.EnvelopeStart)
}

func TestSubmitContractTaskDriverFanoutRequiresConfiguredRunner(t *testing.T) {
	var delegated bool
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			// Two slaves satisfy the tool → driver_fanout route, but no runner is configured.
			return []agentsdk.AgentCard{
				{AgentID: "sbx-driver", DisplayName: "driver", Status: "available"},
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["chat"],"mcp_tools":[{"server":"csv_profiler","name":"profile_orders_csv"}]}`)},
				{AgentID: "slave-b", DisplayName: "slave-b", Status: "available", Card: json.RawMessage(`{"skills":["chat"],"mcp_tools":[{"server":"csv_profiler","name":"profile_orders_csv"}]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = true
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
	}
	tc := testTaskContract()
	tc.ExecutionPolicy.Routing = contract.RoutingDirectFirst
	tc.CapabilityRequirements.Skills = nil
	tc.CapabilityRequirements.Tools = []string{"csv_profiler/profile_orders_csv"}
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	_, err = submitContractToolForTest(t, newTestTools(t, sdk)).Call(context.Background(), raw)
	require.Error(t, err)
	require.Contains(t, err.Error(), "driver_fanout route is recommended but no driver contract runner is configured")
	require.False(t, delegated)
}

func TestSubmitContractTaskDirectRouteUsesCapabilityAwareMatch(t *testing.T) {
	var lastDelegate agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["chat"]}`)},
				{AgentID: "slave-b", DisplayName: "slave-b", Status: "available", Card: json.RawMessage(`{"skills":["chat"],"mcp_tools":[{"server":"refund_policy_checker","name":"evaluate_rows"}]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			lastDelegate = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
	}
	tc := testTaskContract()
	tc.CapabilityRequirements.Tools = []string{"refund_policy_checker/evaluate_rows"}
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	out, err := submitContractToolForTest(t, newTestTools(t, sdk)).Call(context.Background(), raw)
	require.NoError(t, err)
	require.Contains(t, string(out), `"route":"direct_slave"`)
	require.Equal(t, "slave-b", lastDelegate.TargetID)
	require.Equal(t, "chat", lastDelegate.Skill)
}

func TestSubmitContractTaskExplicitSlaveTargetReturnsDirectRoute(t *testing.T) {
	var lastDelegate agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["chat"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			lastDelegate = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
	}
	raw, err := json.Marshal(map[string]interface{}{
		"contract":            testTaskContract(),
		"target_display_name": "slave-a",
	})
	require.NoError(t, err)

	out, err := submitContractToolForTest(t, newTestTools(t, sdk)).Call(context.Background(), raw)
	require.NoError(t, err)
	require.Contains(t, string(out), `"route":"direct_slave"`)
	require.Equal(t, "slave-a", lastDelegate.TargetID)
	require.Equal(t, "chat", lastDelegate.Skill)
}

func TestSubmitContractTaskExplicitMasterTargetBypassesDriverFanoutBlock(t *testing.T) {
	var lastDelegate agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "m1", DisplayName: "master", Status: "available", Card: json.RawMessage(`{"skills":["fanout"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			lastDelegate = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
	}
	tc := testTaskContract()
	tc.ExecutionPolicy.Routing = contract.RoutingDirectFirst
	tc.CapabilityRequirements.Skills = nil
	tc.CapabilityRequirements.Tools = []string{"csv_profiler/profile_orders_csv"}
	raw, err := json.Marshal(map[string]interface{}{
		"contract":            tc,
		"target_display_name": "master",
	})
	require.NoError(t, err)

	out, err := submitContractToolForTest(t, newTestTools(t, sdk)).Call(context.Background(), raw)
	require.NoError(t, err)
	require.Contains(t, string(out), `"route":"master_fanout"`)
	require.Equal(t, "m1", lastDelegate.TargetID)
	require.Equal(t, "fanout", lastDelegate.Skill)
}

func TestSubmitContractTaskIgnoresOfflineSlaveAndFallsBackToAvailableMaster(t *testing.T) {
	var lastDelegate agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-offline", DisplayName: "slave-offline", Status: "offline", Card: json.RawMessage(`{"skills":["chat"]}`)},
				{AgentID: "sbx-master", DisplayName: "master", Status: "available", Card: json.RawMessage(`{"skills":["fanout"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			lastDelegate = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
	}
	raw, err := json.Marshal(map[string]interface{}{"contract": testTaskContract()})
	require.NoError(t, err)

	out, err := submitContractToolForTest(t, newTestTools(t, sdk)).Call(context.Background(), raw)
	require.NoError(t, err)
	require.Contains(t, string(out), `"task_id":"task-1"`)
	require.Equal(t, "sbx-master", lastDelegate.TargetID)
	require.Equal(t, "fanout", lastDelegate.Skill)
}

func TestSubmitContractTaskDisallowsMasterFallbackWhenPolicyForbidsMaster(t *testing.T) {
	var delegated bool
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-master", DisplayName: "master", Status: "available", Card: json.RawMessage(`{"skills":["fanout"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = true
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
	}
	tc := testTaskContract()
	tc.ExecutionPolicy.AllowMaster = contract.Bool(false)
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	_, err = submitContractToolForTest(t, newTestTools(t, sdk)).Call(context.Background(), raw)
	require.Error(t, err)
	require.Contains(t, err.Error(), "master fallback is not allowed")
	require.False(t, delegated)
}

func TestSubmitContractTaskDoesNotFallbackToUnavailableMaster(t *testing.T) {
	var delegated bool
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-master", DisplayName: "master", Status: "offline", Card: json.RawMessage(`{"skills":["fanout"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = true
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
	}
	raw, err := json.Marshal(map[string]interface{}{"contract": testTaskContract()})
	require.NoError(t, err)

	_, err = submitContractToolForTest(t, newTestTools(t, sdk)).Call(context.Background(), raw)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no fanout-skilled agent available")
	require.False(t, delegated)
}

func TestSubmitContractTaskAllowedTargetsRestrictsDirectRoute(t *testing.T) {
	var lastDelegate agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["chat"]}`)},
				{AgentID: "slave-b", DisplayName: "slave-b", Status: "available", Card: json.RawMessage(`{"skills":["chat"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			lastDelegate = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
	}
	tc := testTaskContract()
	tc.ExecutionPolicy.AllowedTargets = []string{"slave-b"}
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	out, err := submitContractToolForTest(t, newTestTools(t, sdk)).Call(context.Background(), raw)
	require.NoError(t, err)
	require.Contains(t, string(out), `"task_id":"task-1"`)
	require.Equal(t, "slave-b", lastDelegate.TargetID)
	require.Equal(t, "chat", lastDelegate.Skill)
}

func TestSubmitContractTaskAllowedTargetsRejectsFallbackTarget(t *testing.T) {
	var delegated bool
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-master", DisplayName: "master", Status: "available", Card: json.RawMessage(`{"skills":["fanout"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = true
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
	}
	tc := testTaskContract()
	tc.ExecutionPolicy.AllowedTargets = []string{"slave-a"}
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	_, err = submitContractToolForTest(t, newTestTools(t, sdk)).Call(context.Background(), raw)
	require.Error(t, err)
	require.Contains(t, err.Error(), "target is not allowed by contract")
	require.False(t, delegated)
}

func TestSubmitContractTaskReturnsWarningWhenTaskContractSaveFailsAfterDelegate(t *testing.T) {
	observerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/resource-snapshots":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		case "/api/task-contracts":
			http.Error(w, "store unavailable", http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected observer path: %s", r.URL.Path)
		}
	}))
	defer observerServer.Close()

	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["chat"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
	}
	tools := newTestTools(t, sdk)
	tools.cfg.Observer.Enabled = true
	tools.cfg.Observer.URL = observerServer.URL
	tools.cfg.Observer.APIKey = "ak-test"
	tools.cfg.Observer.TokenStatePath = "/tmp/test-observer-token"
	tools.relay = NewObserverRelay(tools.cfg, stubTokenSource("test-token"))
	raw, err := json.Marshal(map[string]interface{}{"contract": testTaskContract()})
	require.NoError(t, err)

	out, err := submitContractToolForTest(t, tools).Call(context.Background(), raw)
	require.NoError(t, err)
	require.Contains(t, string(out), `"task_id":"task-1"`)
	require.Contains(t, string(out), `"warnings"`)
	require.Contains(t, string(out), "observer save task contract")
}

func TestSubmitContractTaskReturnsWarningWhenResourceSnapshotSaveFailsAfterDelegate(t *testing.T) {
	observerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/resource-snapshots":
			http.Error(w, "store unavailable", http.StatusInternalServerError)
		case "/api/task-contracts":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected observer path: %s", r.URL.Path)
		}
	}))
	defer observerServer.Close()

	var delegated bool
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["chat"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = true
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
	}
	tools := newTestTools(t, sdk)
	tools.cfg.Observer.Enabled = true
	tools.cfg.Observer.URL = observerServer.URL
	tools.cfg.Observer.APIKey = "ak-test"
	tools.cfg.Observer.TokenStatePath = "/tmp/test-observer-token"
	tools.relay = NewObserverRelay(tools.cfg, stubTokenSource("test-token"))
	raw, err := json.Marshal(map[string]interface{}{"contract": testTaskContract()})
	require.NoError(t, err)

	out, err := submitContractToolForTest(t, tools).Call(context.Background(), raw)
	require.NoError(t, err)
	require.True(t, delegated)
	require.Contains(t, string(out), `"task_id":"task-1"`)
	require.Contains(t, string(out), "observer save resource snapshot")
}

func TestTool_ListAgents_FiltersSelf(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-driver", DisplayName: "driver-yuzishu"},
				{AgentID: "sbx-master", DisplayName: "master-prod", Status: "available",
					Card: json.RawMessage(`{"skills":["fanout"],"tools":[],"mcp_tools":[{"server":"refund_policy_checker","name":"evaluate_rows","input_schema":{"type":"object"}}]}`)},
			}, nil
		},
	}
	tools := newTestTools(t, sdk)
	for _, tt := range tools.All() {
		if tt.Name() == "list_agents" {
			out, err := tt.Call(context.Background(), json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(out), "driver-yuzishu") {
				t.Errorf("self not filtered: %s", out)
			}
			if !strings.Contains(string(out), "master-prod") {
				t.Errorf("master missing: %s", out)
			}
			if !strings.Contains(string(out), `"mcp_tools"`) || !strings.Contains(string(out), "evaluate_rows") {
				t.Errorf("mcp_tools missing: %s", out)
			}
			return
		}
	}
	t.Fatal("list_agents tool not registered")
}

func TestTool_ListAgents_ReturnsOnlyAvailableAgentsWithRoleAndStatus(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-driver", DisplayName: "current-driver", AgentType: "driver", Status: "available"},
				{AgentID: "other-driver", DisplayName: "other-driver", AgentType: "driver", Status: "available"},
				{AgentID: "master-a", DisplayName: "master-a", Status: "available", Card: json.RawMessage(`{"skills":["fanout"]}`)},
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["chat"]}`)},
				{AgentID: "slave-offline", DisplayName: "slave-offline", Status: "offline", Card: json.RawMessage(`{"skills":["chat"]}`)},
				{AgentID: "slave-busy", DisplayName: "slave-busy", Status: "busy", Card: json.RawMessage(`{"skills":["chat"]}`)},
			}, nil
		},
	}
	out, err := toolByName(t, newTestTools(t, sdk), "list_agents").Call(context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)

	var got struct {
		Agents []struct {
			AgentID string `json:"agent_id"`
			Status  string `json:"status"`
			Role    string `json:"role"`
		} `json:"agents"`
	}
	require.NoError(t, json.Unmarshal(out, &got))
	require.Equal(t, []struct {
		AgentID string `json:"agent_id"`
		Status  string `json:"status"`
		Role    string `json:"role"`
	}{
		{AgentID: "other-driver", Status: "available", Role: "driver"},
		{AgentID: "master-a", Status: "available", Role: "master"},
		{AgentID: "slave-a", Status: "available", Role: "slave"},
	}, got.Agents)
}

func TestTool_ListAgents_CanIncludeUnavailableAgents(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-driver", DisplayName: "current-driver", AgentType: "driver", Status: "available"},
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["chat"]}`)},
				{AgentID: "slave-offline", DisplayName: "slave-offline", Status: "offline", Card: json.RawMessage(`{"skills":["chat"]}`)},
				{AgentID: "slave-busy", DisplayName: "slave-busy", Status: "busy", Card: json.RawMessage(`{"skills":["chat"]}`)},
			}, nil
		},
	}
	out, err := toolByName(t, newTestTools(t, sdk), "list_agents").Call(context.Background(), json.RawMessage(`{"include_unavailable":true}`))
	require.NoError(t, err)

	var got struct {
		Agents []struct {
			AgentID string `json:"agent_id"`
			Status  string `json:"status"`
		} `json:"agents"`
	}
	require.NoError(t, json.Unmarshal(out, &got))
	require.Equal(t, []struct {
		AgentID string `json:"agent_id"`
		Status  string `json:"status"`
	}{
		{AgentID: "slave-a", Status: "available"},
		{AgentID: "slave-offline", Status: "offline"},
		{AgentID: "slave-busy", Status: "busy"},
	}, got.Agents)
}

func TestTool_InspectCapabilitiesReturnsSnapshotAndSavesIt(t *testing.T) {
	var snapshotSaved bool
	observerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/resource-snapshots" {
			t.Fatalf("unexpected observer path: %s", r.URL.Path)
		}
		snapshotSaved = true
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer observerServer.Close()

	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-driver", DisplayName: "driver", Status: "available"},
				{AgentID: "m1", DisplayName: "master", Status: "available", Card: json.RawMessage(`{"skills":["fanout"],"mcp_tools":[{"server":"policy","name":"evaluate_rows"}]}`)},
				{AgentID: "s1", DisplayName: "slave", Status: "available", Card: json.RawMessage(`{"skills":["register_mcp"],"resources":{"tags":["python3"]}}`)},
			}, nil
		},
	}
	tools := newTestTools(t, sdk)
	tools.cfg.Observer.Enabled = true
	tools.cfg.Observer.URL = observerServer.URL
	tools.cfg.Observer.WorkspaceID = "dev"
	tools.cfg.Observer.AgentID = "driver"
	tools.cfg.Observer.APIKey = "ak-test"
	tools.cfg.Observer.TokenStatePath = "/tmp/test-observer-token"
	tools.relay = NewObserverRelay(tools.cfg, stubTokenSource("test-token"))

	out, err := toolByName(t, tools, "inspect_capabilities").Call(context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)
	require.True(t, snapshotSaved)
	require.Contains(t, string(out), `"resource_snapshot"`)
	require.Contains(t, string(out), `"masters"`)
	require.Contains(t, string(out), `"slaves"`)
	require.Contains(t, string(out), "evaluate_rows")
	require.Contains(t, string(out), "register_mcp")
}

func TestTool_DraftTaskContractBuildsContractAndClarificationQuestions(t *testing.T) {
	tools := newTestTools(t, &fakeSDK{})
	out, err := toolByName(t, tools, "draft_task_contract").Call(context.Background(), json.RawMessage(`{
		"goal":"Analyze refunds and write a report",
		"write_targets":[{"kind":"markdown","name":"refund-risk-report.md"}],
		"required_tools":["csv_profiler/profile_orders_csv","refund_policy_checker/evaluate_rows"]
	}`))
	require.NoError(t, err)
	require.Contains(t, string(out), `"contract"`)
	require.Contains(t, string(out), `"conversation_id"`)
	require.Contains(t, string(out), `"Analyze refunds and write a report"`)
	require.Contains(t, string(out), `"csv_profiler/profile_orders_csv"`)
	require.Contains(t, string(out), `"clarification_questions"`)
}

func TestTool_DryRunContractReportsExistingMCPToolsSatisfyRequirements(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "m1", DisplayName: "master", Status: "available", Card: json.RawMessage(`{"skills":["fanout"]}`)},
				{AgentID: "s1", DisplayName: "slave", Status: "available", Card: json.RawMessage(`{"skills":["chat"],"mcp_tools":[{"server":"refund_policy_checker","name":"evaluate_rows"}]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			t.Fatalf("dry_run_contract must not delegate")
			return nil, nil
		},
	}
	tc := testTaskContract()
	tc.CapabilityRequirements.Tools = []string{"refund_policy_checker/evaluate_rows"}
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	out, err := toolByName(t, newTestTools(t, sdk), "dry_run_contract").Call(context.Background(), raw)
	require.NoError(t, err)
	require.Contains(t, string(out), `"runnable":true`)
	require.Contains(t, string(out), `"satisfied_tools"`)
	require.Contains(t, string(out), "refund_policy_checker/evaluate_rows")
}

func TestTool_DryRunContractRecommendsDirectSlaveRoute(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-driver", DisplayName: "driver", Status: "available"},
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["chat"],"mcp_tools":[{"server":"refund_policy_checker","name":"evaluate_rows"}]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			t.Fatalf("dry_run_contract must not delegate")
			return nil, nil
		},
	}
	tc := testTaskContract()
	tc.CapabilityRequirements.Tools = []string{"refund_policy_checker/evaluate_rows"}

	report := callDryRunContractForTest(t, sdk, tc)

	require.True(t, report.Runnable)
	require.Equal(t, "direct_slave", report.RecommendedRoute)
	require.Equal(t, "slave-a", report.RecommendedTargetID)
	require.Equal(t, "chat", report.RecommendedSkill)
}

func TestTool_DryRunContractRecommendsDirectSlaveAfterFilteringTools(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["chat"]}`)},
				{AgentID: "slave-b", DisplayName: "slave-b", Status: "available", Card: json.RawMessage(`{"skills":["chat"],"mcp_tools":[{"server":"refund_policy_checker","name":"evaluate_rows"}]}`)},
			}, nil
		},
	}
	tc := testTaskContract()
	tc.CapabilityRequirements.Tools = []string{"refund_policy_checker/evaluate_rows"}

	report := callDryRunContractForTest(t, sdk, tc)

	require.True(t, report.Runnable)
	require.Equal(t, "direct_slave", report.RecommendedRoute)
	require.Equal(t, "slave-b", report.RecommendedTargetID)
	require.Equal(t, "chat", report.RecommendedSkill)
}

func TestTool_DryRunContractReportsBlockedRouteWhenResourcesMismatch(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-driver", DisplayName: "driver", Status: "available"},
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{
					"skills":["chat"],
					"mcp_tools":[{"server":"refund_policy_checker","name":"evaluate_rows"}],
					"resources":{"tags":["python3"],"region":"us"}
				}`)},
			}, nil
		},
	}
	tc := testTaskContract()
	tc.CapabilityRequirements.Tools = []string{"refund_policy_checker/evaluate_rows"}
	tc.CapabilityRequirements.Resources = json.RawMessage(`{"tags":["gpu"],"region":"us"}`)

	report := callDryRunContractForTest(t, sdk, tc)

	require.False(t, report.Runnable)
	require.Equal(t, "blocked", report.RecommendedRoute)
	require.JSONEq(t, `{"tags":["gpu"],"region":"us"}`, string(report.MissingResources))
	require.Contains(t, report.Reasons, "required resources are missing or unavailable")
}

func TestTool_DryRunContractReportsBlockedRouteWhenOnlyDisallowedAgentHasTool(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "allowed-slave", DisplayName: "allowed-slave", Status: "available", Card: json.RawMessage(`{"skills":["chat"]}`)},
				{AgentID: "disallowed-slave", DisplayName: "disallowed-slave", Status: "available", Card: json.RawMessage(`{"skills":["chat"],"mcp_tools":[{"server":"refund_policy_checker","name":"evaluate_rows"}]}`)},
			}, nil
		},
	}
	tc := testTaskContract()
	tc.ExecutionPolicy.AllowedTargets = []string{"allowed-slave"}
	tc.CapabilityRequirements.Tools = []string{"refund_policy_checker/evaluate_rows"}

	report := callDryRunContractForTest(t, sdk, tc)

	require.False(t, report.Runnable)
	require.Equal(t, "blocked", report.RecommendedRoute)
	require.Contains(t, report.MissingTools, "refund_policy_checker/evaluate_rows")
}

func TestTool_DryRunContractMatchesEquivalentNumericResources(t *testing.T) {
	require.True(t, resourceJSONContains(json.RawMessage(`{"workers":1.0}`), json.RawMessage(`{"workers":1}`)))
	require.True(t, resourceJSONContains(json.RawMessage(`{"limits":{"cpu":1}}`), json.RawMessage(`{"limits":{"cpu":1.0}}`)))
	require.True(t, resourceJSONContains(json.RawMessage(`{"versions":[0.9,1.0,2]}`), json.RawMessage(`{"versions":[1]}`)))
}

func TestTool_DryRunContractRecommendsDriverFanoutRoute(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			// Two slaves both have the tool; no single-direct-slave match → driver_fanout.
			return []agentsdk.AgentCard{
				{AgentID: "sbx-driver", DisplayName: "driver", Status: "available"},
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["chat"],"mcp_tools":[{"server":"csv_profiler","name":"profile_orders_csv"}]}`)},
				{AgentID: "slave-b", DisplayName: "slave-b", Status: "available", Card: json.RawMessage(`{"skills":["chat"],"mcp_tools":[{"server":"csv_profiler","name":"profile_orders_csv"}]}`)},
			}, nil
		},
	}
	tc := testTaskContract()
	tc.ExecutionPolicy.Routing = contract.RoutingDirectFirst
	tc.CapabilityRequirements.Skills = nil
	tc.CapabilityRequirements.Tools = []string{"csv_profiler/profile_orders_csv"}

	report := callDryRunContractForTest(t, sdk, tc)

	require.True(t, report.Runnable)
	require.Equal(t, "driver_fanout", report.RecommendedRoute)
	require.Empty(t, report.RecommendedTargetID)
	require.Equal(t, "fanout", report.RecommendedSkill)
}

func TestTool_DryRunContractBlocksWhenToolsMissingAndNoSatisfyingAgent(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "resource-slave", DisplayName: "resource-slave", Status: "available", Card: json.RawMessage(`{"skills":["chat"],"resources":{"tags":["gpu"]}}`)},
			}, nil
		},
	}
	tc := testTaskContract()
	tc.ExecutionPolicy.Routing = contract.RoutingDirectFirst
	tc.CapabilityRequirements.Skills = nil
	tc.CapabilityRequirements.Tools = []string{"csv_profiler/profile_orders_csv"}

	report := callDryRunContractForTest(t, sdk, tc)

	require.False(t, report.Runnable)
	require.Equal(t, "blocked", report.RecommendedRoute)
	require.Contains(t, report.MissingTools, "csv_profiler/profile_orders_csv")
}

func TestTool_DryRunContractRecommendsMasterFanoutRoute(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "m1", DisplayName: "master", Status: "available", Card: json.RawMessage(`{"skills":["fanout"],"mcp_tools":[{"server":"refund_policy_checker","name":"evaluate_rows"}]}`)},
			}, nil
		},
	}
	tc := testTaskContract()
	tc.ExecutionPolicy.Routing = contract.RoutingMasterOnly
	tc.CapabilityRequirements.Skills = nil
	tc.CapabilityRequirements.Tools = []string{"refund_policy_checker/evaluate_rows"}

	report := callDryRunContractForTest(t, sdk, tc)

	require.True(t, report.Runnable)
	require.Equal(t, "master_fanout", report.RecommendedRoute)
	require.Equal(t, "m1", report.RecommendedTargetID)
	require.Equal(t, "fanout", report.RecommendedSkill)
}

func TestTool_DryRunContractReportsBlockedWhenToolsMissingNoCandidate(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "m1", DisplayName: "master", Status: "available", Card: json.RawMessage(`{"skills":["fanout"]}`)},
			}, nil
		},
	}
	tc := testTaskContract()
	tc.ExecutionPolicy.Routing = contract.RoutingMasterOnly
	tc.CapabilityRequirements.Skills = nil
	tc.CapabilityRequirements.Tools = []string{"csv_profiler/profile_orders_csv"}
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	out, err := toolByName(t, newTestTools(t, sdk), "dry_run_contract").Call(context.Background(), raw)
	require.NoError(t, err)
	require.Contains(t, string(out), `"runnable":false`)
	require.Contains(t, string(out), `"missing_tools":["csv_profiler/profile_orders_csv"]`)
}

func TestTool_DryRunContractReportsBlockedRoute(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-driver", DisplayName: "driver", Status: "available"},
			}, nil
		},
	}
	tc := testTaskContract()
	tc.CapabilityRequirements.Skills = nil
	tc.CapabilityRequirements.Tools = []string{"missing/tool"}

	report := callDryRunContractForTest(t, sdk, tc)

	require.False(t, report.Runnable)
	require.Equal(t, "blocked", report.RecommendedRoute)
	require.Contains(t, report.MissingTools, "missing/tool")
}

func TestTool_DryRunContractReportsMissingToolsAsBlocked(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "m1", DisplayName: "master", Status: "available", Card: json.RawMessage(`{"skills":["fanout"]}`)},
			}, nil
		},
	}
	tc := testTaskContract()
	tc.CapabilityRequirements.Tools = []string{"missing/tool"}
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	out, err := toolByName(t, newTestTools(t, sdk), "dry_run_contract").Call(context.Background(), raw)
	require.NoError(t, err)
	require.Contains(t, string(out), `"runnable":false`)
	require.Contains(t, string(out), "missing/tool")
}

func callDryRunContractForTest(t *testing.T, sdk *fakeSDK, tc contract.TaskContract) dryRunReport {
	t.Helper()
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	out, err := toolByName(t, newTestTools(t, sdk), "dry_run_contract").Call(context.Background(), raw)
	require.NoError(t, err)

	var report dryRunReport
	require.NoError(t, json.Unmarshal(out, &report))
	return report
}

func TestTool_SubmitTask_RegistersFilesAndDelegates(t *testing.T) {
	dir := t.TempDir()
	in1 := filepath.Join(dir, "a.txt")
	in2 := filepath.Join(dir, "b.txt")
	out := filepath.Join(dir, "out.txt")
	os.WriteFile(in1, []byte("hello"), 0o644)
	os.WriteFile(in2, []byte("world"), 0o644)

	var gotPrompt string
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-master", DisplayName: "master-prod", Status: "available",
					Card: json.RawMessage(`{"skills":["fanout"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			gotPrompt = req.Prompt
			if req.TargetID != "sbx-master" {
				t.Errorf("target_id: %s", req.TargetID)
			}
			if req.Skill != "fanout" {
				t.Errorf("skill: %s", req.Skill)
			}
			if req.TimeoutSeconds != 600 {
				t.Errorf("timeout: %d", req.TimeoutSeconds)
			}
			return &agentsdk.DelegateTaskResponse{TaskID: "t-1"}, nil
		},
	}
	obs := &fakeObserver{}
	tools := newTestToolsWithObserver(t, sdk, obs)
	args := json.RawMessage(`{
        "prompt": "merge these",
        "read_paths": ["` + in1 + `", "` + in2 + `"],
        "write_paths": [{"path": "` + out + `", "overwrite": true}]
    }`)
	for _, tt := range tools.All() {
		if tt.Name() == "submit_task" {
			res, err := tt.Call(context.Background(), args)
			if err != nil {
				t.Fatalf("submit_task: %v", err)
			}
			var parsed struct {
				TaskID   string `json:"task_id"`
				Manifest struct {
					Files  []FileEntry         `json:"files"`
					Writes []WriteRequestEntry `json:"writes"`
				} `json:"manifest"`
			}
			json.Unmarshal(res, &parsed)
			if parsed.TaskID != "t-1" {
				t.Errorf("task_id: %s", parsed.TaskID)
			}
			if len(parsed.Manifest.Files) != 2 {
				t.Errorf("files: %+v", parsed.Manifest.Files)
			}
			if len(parsed.Manifest.Writes) != 1 || !parsed.Manifest.Writes[0].Overwrite {
				t.Errorf("writes: %+v", parsed.Manifest.Writes)
			}
			if !strings.Contains(gotPrompt, "<USER_FILES_MANIFEST") {
				t.Errorf("manifest not in prompt: %s", gotPrompt)
			}
			if !strings.Contains(gotPrompt, "merge these") {
				t.Errorf("user prompt not preserved: %s", gotPrompt)
			}
			if len(obs.events) != 1 {
				t.Fatalf("observer events: %+v", obs.events)
			}
			ev := obs.events[0]
			if ev.Type != observer.EventDriverTaskSubmitted {
				t.Errorf("event type: %s", ev.Type)
			}
			if ev.TaskID != "t-1" {
				t.Errorf("event task_id: %s", ev.TaskID)
			}
			if ev.Summary != "merge these" {
				t.Errorf("event summary: %q", ev.Summary)
			}
			if ev.Status != "assigned" {
				t.Errorf("event status: %s", ev.Status)
			}
			if ev.TargetAgentID != "sbx-master" {
				t.Errorf("event target_agent_id: %s", ev.TargetAgentID)
			}
			if ev.TargetRole != observer.RoleMaster {
				t.Errorf("event target_role: %s", ev.TargetRole)
			}
			return
		}
	}
	t.Fatal("submit_task tool not registered")
}

func TestTool_SubmitTask_ObserverLazyManifest(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "a.txt")
	out := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(in, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	var artifacts int
	var writes int
	observerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer observer-token" {
			t.Fatalf("observer auth = %q", got)
		}
		switch r.URL.Path {
		case "/api/artifacts":
			artifacts++
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"artifact_id":"art_1","url":"` + r.Host + `/api/artifacts/art_1","state":"registered"}`))
		case "/api/write-tokens":
			writes++
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"write_id":"wr_1","put_url":"` + r.Host + `/api/writes/wr_1"}`))
		case "/api/writes/wr_1":
			if r.Method != http.MethodPatch {
				t.Fatalf("write rebind method = %s", r.Method)
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected observer path: %s", r.URL.Path)
		}
	}))
	defer observerServer.Close()

	var gotPrompt string
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{AgentID: "sbx-master", DisplayName: "master-prod", Status: "available", Card: json.RawMessage(`{"skills":["fanout"]}`)}}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			gotPrompt = req.Prompt
			return &agentsdk.DelegateTaskResponse{TaskID: "t-1"}, nil
		},
	}
	tools := newTestTools(t, sdk)
	tools.cfg.Observer.Enabled = true
	tools.cfg.Observer.URL = observerServer.URL
	tools.cfg.Observer.APIKey = "ak-test"
	tools.cfg.Observer.TokenStatePath = "/tmp/test-observer-token"
	tools.cfg.DriverDefaults.ArtifactTransport = ArtifactTransportObserverLazy
	tools.relay = NewObserverRelay(tools.cfg, stubTokenSource("observer-token"))

	for _, tt := range tools.All() {
		if tt.Name() == "submit_task" {
			res, err := tt.Call(context.Background(), json.RawMessage(`{
				"prompt":"merge",
				"read_paths":["`+in+`"],
				"write_paths":[{"path":"`+out+`","overwrite":true}]
			}`))
			if err != nil {
				t.Fatalf("submit_task: %v", err)
			}
			if artifacts != 1 || writes != 1 {
				t.Fatalf("observer calls: artifacts=%d writes=%d", artifacts, writes)
			}
			if strings.Contains(gotPrompt, "/api/agent/peer/") {
				t.Fatalf("prompt still uses peer proxy: %s", gotPrompt)
			}
			for _, want := range []string{"/api/artifacts/art_1", "/api/writes/wr_1"} {
				if !strings.Contains(gotPrompt, want) {
					t.Fatalf("prompt missing %s: %s", want, gotPrompt)
				}
			}
			if !strings.Contains(string(res), "/api/artifacts/art_1") {
				t.Fatalf("response missing artifact URL: %s", res)
			}
			return
		}
	}
	t.Fatal("submit_task tool not registered")
}

func TestTool_SubmitTask_ObserverLazyRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{AgentID: "sbx-master", DisplayName: "master-prod", Status: "available", Card: json.RawMessage(`{"skills":["fanout"]}`)}}, nil
		},
	}
	tools := newTestTools(t, sdk)
	tools.cfg.Observer.Enabled = true
	tools.cfg.Observer.URL = "http://observer.example"
	tools.cfg.Observer.APIKey = "ak-test"
	tools.cfg.Observer.TokenStatePath = "/tmp/test-observer-token"
	tools.cfg.DriverDefaults.ArtifactTransport = ArtifactTransportObserverLazy

	for _, tt := range tools.All() {
		if tt.Name() == "submit_task" {
			_, err := tt.Call(context.Background(), json.RawMessage(`{"prompt":"merge","read_paths":["`+dir+`"]}`))
			if err == nil || !strings.Contains(err.Error(), "directory read_paths are not implemented") {
				t.Fatalf("err = %v", err)
			}
			return
		}
	}
	t.Fatal("submit_task tool not registered")
}

func TestObserverRelayServesPendingFileRequest(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(in, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewFileRegistry(100)
	reg.RegisterObserverArtifact("art_1", in, "file")

	var uploaded string
	observerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer observer-token" {
			t.Fatalf("observer auth = %q", got)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/artifact-requests":
			_, _ = w.Write([]byte(`{"requests":[{"request_id":"fetch_1","artifact_id":"art_1","kind":"file","path":"` + in + `","state":"pending"}]}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/artifacts/art_1/content":
			body, _ := io.ReadAll(r.Body)
			uploaded = string(body)
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected observer request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer observerServer.Close()

	relay := &ObserverRelay{baseURL: observerServer.URL, src: stubTokenSource("observer-token"), http: observerServer.Client()}
	err := relay.ServePendingOnce(context.Background(), reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if uploaded != "hello" {
		t.Fatalf("uploaded = %q", uploaded)
	}
}

func TestObserverRelaySyncWrites(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.txt")
	reg := NewFileRegistry(100)

	observerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/writes" || r.URL.Query().Get("task_id") != "t-1" {
			t.Fatalf("unexpected observer request: %s %s", r.Method, r.URL.String())
		}
		_, _ = w.Write([]byte(`{"writes":[{"write_id":"wr_1","path":"` + out + `","overwrite":true,"bytes":4,"sha256":"s","content":"ZG9uZQ=="}]}`))
	}))
	defer observerServer.Close()

	relay := &ObserverRelay{baseURL: observerServer.URL, src: stubTokenSource("observer-token"), http: observerServer.Client()}
	written, err := relay.SyncWrites(context.Background(), "t-1", false, reg)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 1 || written[0].Path != out {
		t.Fatalf("written: %+v", written)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "done" {
		t.Fatalf("body = %q", body)
	}
}

func TestObserverRelayUpdateWriteTaskRetriesSQLiteBusy(t *testing.T) {
	calls := 0
	observerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPatch || r.URL.Path != "/api/writes/wr_1" {
			t.Fatalf("unexpected observer request: %s %s", r.Method, r.URL.Path)
		}
		if calls == 1 {
			http.Error(w, "database is locked (5) (SQLITE_BUSY)", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer observerServer.Close()

	relay := &ObserverRelay{baseURL: observerServer.URL, src: stubTokenSource("observer-token"), http: observerServer.Client()}
	err := relay.UpdateWriteTask(context.Background(), "wr_1", "task_1")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestTool_SubmitTask_EmitsSlaveTargetRoleForNonMasterTarget(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-slave", DisplayName: "slave-prod", Status: "available",
					Card: json.RawMessage(`{"skills":["chat","mcp"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "t-1"}, nil
		},
	}
	obs := &fakeObserver{}
	tools := newTestToolsWithObserver(t, sdk, obs)
	for _, tt := range tools.All() {
		if tt.Name() == "submit_task" {
			_, err := tt.Call(context.Background(), json.RawMessage(`{"prompt":"run it","target_display_name":"slave-prod"}`))
			if err != nil {
				t.Fatalf("submit_task: %v", err)
			}
			if len(obs.events) != 1 {
				t.Fatalf("observer events: %+v", obs.events)
			}
			if obs.events[0].TargetRole != observer.RoleSlave {
				t.Fatalf("target_role: %s", obs.events[0].TargetRole)
			}
			return
		}
	}
	t.Fatal("submit_task tool not registered")
}

func TestTool_SubmitTask_RejectsAmbiguousTarget(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "m1", DisplayName: "master-a", Status: "available", Card: json.RawMessage(`{"skills":["fanout"]}`)},
				{AgentID: "m2", DisplayName: "master-b", Status: "available", Card: json.RawMessage(`{"skills":["fanout"]}`)},
			}, nil
		},
	}
	tools := newTestTools(t, sdk)
	for _, tt := range tools.All() {
		if tt.Name() == "submit_task" {
			_, err := tt.Call(context.Background(), json.RawMessage(`{"prompt":"x"}`))
			if err == nil {
				t.Fatal("expected ambiguous-target error")
			}
			if !strings.Contains(err.Error(), "ambiguous") && !strings.Contains(err.Error(), "candidates") {
				t.Errorf("error message: %v", err)
			}
			return
		}
	}
}

func TestTool_GetTask_ReturnsStatus(t *testing.T) {
	sdk := &fakeSDK{
		getTaskFunc: func(id string, _ bool) (*agentsdk.TaskInfo, error) {
			if id != "t-1" {
				return nil, errors.New("nope")
			}
			return &agentsdk.TaskInfo{TaskID: "t-1", Status: "completed", Output: "done"}, nil
		},
	}
	tools := newTestTools(t, sdk)
	for _, tt := range tools.All() {
		if tt.Name() == "get_task" {
			res, err := tt.Call(context.Background(), json.RawMessage(`{"task_id":"t-1"}`))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(res), `"status":"completed"`) {
				t.Errorf("res: %s", res)
			}
			return
		}
	}
}

func TestGetTaskIncludesObserverProgress(t *testing.T) {
	observerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tasks/t1/progress" {
			t.Fatalf("path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer fake-token" {
			t.Fatalf("authorization: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"latest_progress":"working","latest_progress_phase":"build","latest_progress_at":"2026-05-13T01:02:03Z","final_output":"not done","is_final":false}`))
	}))
	defer observerServer.Close()

	sdk := &fakeSDK{
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			if id != "t1" {
				return nil, errors.New("nope")
			}
			if !includeOutput {
				t.Fatal("includeOutput = false")
			}
			return &agentsdk.TaskInfo{TaskID: "t1", Status: "running", Output: "sdk output"}, nil
		},
	}
	tools := newTestToolsWithObserver(t, sdk, &fakeObserver{})
	tools.cfg.Observer.Enabled = true
	tools.cfg.Observer.URL = observerServer.URL

	for _, tt := range tools.All() {
		if tt.Name() == "get_task" {
			res, err := tt.Call(context.Background(), json.RawMessage(`{"task_id":"t1"}`))
			if err != nil {
				t.Fatal(err)
			}
			want := `{"status":"running","output":"sdk output","failure_reason":"","latest_progress":"working","latest_progress_phase":"build","latest_progress_at":"2026-05-13T01:02:03Z","final_output":"not done","is_final":false}`
			if string(res) != want {
				t.Fatalf("response mismatch\nwant: %s\n got: %s", want, res)
			}
			return
		}
	}
	t.Fatal("get_task tool not registered")
}

func TestGetTaskSkipsObserverProgressWhenObserverDisabled(t *testing.T) {
	var calls int32
	observerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"task_id":"t1","latest_progress":"unexpected","latest_progress_phase":"build","latest_progress_at":"2026-05-13T01:02:03Z","final_output":"unexpected","is_final":true}]`))
	}))
	defer observerServer.Close()

	sdk := &fakeSDK{
		getTaskFunc: func(id string, _ bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{TaskID: id, Status: "running", Output: "sdk output"}, nil
		},
	}
	tools := newTestTools(t, sdk)
	tools.cfg.Observer.Enabled = false
	tools.cfg.Observer.URL = observerServer.URL

	for _, tt := range tools.All() {
		if tt.Name() == "get_task" {
			res, err := tt.Call(context.Background(), json.RawMessage(`{"task_id":"t1"}`))
			if err != nil {
				t.Fatal(err)
			}
			if got := atomic.LoadInt32(&calls); got != 0 {
				t.Fatalf("observer requests: got %d, want 0", got)
			}
			want := `{"status":"running","output":"sdk output","failure_reason":"","latest_progress":"","latest_progress_phase":"","latest_progress_at":"","final_output":"","is_final":false}`
			if string(res) != want {
				t.Fatalf("response mismatch\nwant: %s\n got: %s", want, res)
			}
			return
		}
	}
	t.Fatal("get_task tool not registered")
}

func TestTool_WaitTask_ReturnsWrittenFiles(t *testing.T) {
	sdk := &fakeSDK{
		getTaskFunc: func(id string, _ bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{TaskID: id, Status: "completed", Output: "hi"}, nil
		},
	}
	tools := newTestTools(t, sdk)
	tools.reg.RecordWritten("t-2", WrittenFile{Path: "/p", Bytes: 5, SHA256: "s"})
	for _, tt := range tools.All() {
		if tt.Name() == "wait_task" {
			res, err := tt.Call(context.Background(),
				json.RawMessage(`{"task_id":"t-2","poll_interval_sec":1,"timeout_sec":5}`))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(res), `"written_files"`) || !strings.Contains(string(res), `"/p"`) {
				t.Errorf("res: %s", res)
			}
			return
		}
	}
}

func TestWaitTaskTerminalFinalOutputFallsBackToSDKOutput(t *testing.T) {
	observerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tasks/t2/progress" {
			t.Fatalf("path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"latest_progress":"done","latest_progress_phase":"final","latest_progress_at":"2026-05-13T04:05:06Z","final_output":"","is_final":false}`))
	}))
	defer observerServer.Close()

	sdk := &fakeSDK{
		getTaskFunc: func(id string, _ bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{TaskID: id, Status: "completed", Output: "sdk final"}, nil
		},
	}
	tools := newTestToolsWithObserver(t, sdk, &fakeObserver{})
	tools.cfg.Observer.Enabled = true
	tools.cfg.Observer.URL = observerServer.URL
	tools.reg.RecordWritten("t2", WrittenFile{Path: "/p", Bytes: 5, SHA256: "s"})

	for _, tt := range tools.All() {
		if tt.Name() == "wait_task" {
			res, err := tt.Call(context.Background(),
				json.RawMessage(`{"task_id":"t2","poll_interval_sec":1,"timeout_sec":5}`))
			if err != nil {
				t.Fatal(err)
			}
			want := `{"status":"completed","output":"sdk final","failure_reason":"","latest_progress":"done","latest_progress_phase":"final","latest_progress_at":"2026-05-13T04:05:06Z","final_output":"sdk final","is_final":true,"written_files":[{"path":"/p","bytes":5,"sha256":"s","written_at":""}]}`
			if string(res) != want {
				t.Fatalf("response mismatch\nwant: %s\n got: %s", want, res)
			}
			return
		}
	}
	t.Fatal("wait_task tool not registered")
}

func TestWaitTaskTerminalFinalOutputFallsBackToSDKResultOutput(t *testing.T) {
	observerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tasks/t3/progress" {
			t.Fatalf("path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"final_output":"","is_final":false}`))
	}))
	defer observerServer.Close()

	sdk := &fakeSDK{
		getTaskFunc: func(id string, _ bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{TaskID: id, Status: "completed", Result: json.RawMessage(`{"output":"result final"}`)}, nil
		},
	}
	tools := newTestToolsWithObserver(t, sdk, &fakeObserver{})
	tools.cfg.Observer.Enabled = true
	tools.cfg.Observer.URL = observerServer.URL

	for _, tt := range tools.All() {
		if tt.Name() == "wait_task" {
			res, err := tt.Call(context.Background(),
				json.RawMessage(`{"task_id":"t3","poll_interval_sec":1,"timeout_sec":5}`))
			if err != nil {
				t.Fatal(err)
			}
			want := `{"status":"completed","output":"result final","failure_reason":"","latest_progress":"","latest_progress_phase":"","latest_progress_at":"","final_output":"result final","is_final":true,"written_files":null}`
			if string(res) != want {
				t.Fatalf("response mismatch\nwant: %s\n got: %s", want, res)
			}
			return
		}
	}
	t.Fatal("wait_task tool not registered")
}

func TestTool_TailSubtasks_PeerProxiesMaster(t *testing.T) {
	called := false
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-master", DisplayName: "master-prod", Status: "available",
					Card: json.RawMessage(`{"skills":["fanout"],"short_id":"m-short"}`)},
			}, nil
		},
		peerProxyFunc: func(method, target, path string, body io.Reader) (*http.Response, error) {
			called = true
			if target != "m-short" {
				t.Errorf("target: %s", target)
			}
			if !strings.Contains(path, "/tasks/t-9/children") {
				t.Errorf("path: %s", path)
			}
			body2 := `[{"node_id":"n1","status":"completed","target_id":"slv","created_at":"2026-01-01T00:00:00Z"}]`
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(body2)),
				Header:     http.Header{},
			}, nil
		},
	}
	tools := newTestTools(t, sdk)
	for _, tt := range tools.All() {
		if tt.Name() == "tail_subtasks" {
			res, err := tt.Call(context.Background(),
				json.RawMessage(`{"task_id":"t-9","since_seq":0,"max_wait_sec":1}`))
			if err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("PeerProxy not called")
			}
			if !strings.Contains(string(res), `"events"`) {
				t.Errorf("res: %s", res)
			}
			return
		}
	}
}

func TestTool_CancelTask_StubReturnsNotSupported(t *testing.T) {
	sdk := &fakeSDK{
		getTaskFunc: func(id string, _ bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{TaskID: id, Status: "running"}, nil
		},
	}
	tools := newTestTools(t, sdk)
	for _, tt := range tools.All() {
		if tt.Name() == "cancel_task" {
			res, err := tt.Call(context.Background(), json.RawMessage(`{"task_id":"t-3"}`))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(res), `"ok":false`) {
				t.Errorf("res: %s", res)
			}
			if !strings.Contains(string(res), "running") {
				t.Errorf("res must include current status: %s", res)
			}
			return
		}
	}
}

// TestSubmitTask_JSONSkill_NoManifestPrefix verifies that submit_task with a
// JSON-prompt skill sends the caller's prompt downstream verbatim. Slave
// executors for these skills json.Unmarshal the prompt; the
// USER_FILES_MANIFEST prefix would break that with `invalid character '<'`.
func TestSubmitTask_JSONSkill_NoManifestPrefix(t *testing.T) {
	for _, skill := range []string{"mcp", "bash", "powershell", "register_mcp", "unregister_mcp", "claude_permissions", "permissions", "file", "chat_resume"} {
		t.Run(skill, func(t *testing.T) {
			var gotPrompt string
			sdk := &fakeSDK{
				discoverFunc: func() ([]agentsdk.AgentCard, error) {
					return []agentsdk.AgentCard{
						{AgentID: "slave-1", DisplayName: "slave-1", Status: "available",
							Card: json.RawMessage(`{"skills":["` + skill + `"]}`)},
					}, nil
				},
				delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
					gotPrompt = req.Prompt
					return &agentsdk.DelegateTaskResponse{TaskID: "t"}, nil
				},
			}
			tools := newTestTools(t, sdk)
			args := json.RawMessage(`{
                "prompt": "{\"server\":\"x\",\"tool\":\"y\",\"args\":{}}",
                "target_display_name": "slave-1",
                "skill": "` + skill + `"
            }`)
			_, err := toolByName(t, tools, "submit_task").Call(context.Background(), args)
			require.NoError(t, err)
			require.NotContains(t, gotPrompt, "<USER_FILES_MANIFEST",
				"skill %s must not receive a USER_FILES_MANIFEST prefix", skill)
			require.Equal(t, `{"server":"x","tool":"y","args":{}}`, gotPrompt)
		})
	}
}

// TestSubmitTask_JSONSkill_RejectsReadPaths verifies that read_paths or
// write_paths with a JSON-prompt skill returns a clear error: the manifest
// cannot be conveyed without breaking the slave's json.Unmarshal.
func TestSubmitTask_JSONSkill_RejectsReadPaths(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-1", DisplayName: "slave-1", Status: "available",
					Card: json.RawMessage(`{"skills":["mcp"]}`)},
			}, nil
		},
	}
	tools := newTestTools(t, sdk)
	dir := t.TempDir()
	file := filepath.Join(dir, "x.txt")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))
	args := json.RawMessage(`{
        "prompt": "{}",
        "read_paths": ["` + file + `"],
        "target_display_name": "slave-1",
        "skill": "mcp"
    }`)
	_, err := toolByName(t, tools, "submit_task").Call(context.Background(), args)
	require.Error(t, err)
	require.Contains(t, err.Error(), "JSON-only")
}

// TestSubmitTask_ChatStillGetsManifest regression-guards that chat-style
// skills still receive the manifest prefix (so Claude can see read/write
// handles even when none are present).
func TestSubmitTask_ChatStillGetsManifest(t *testing.T) {
	var gotPrompt string
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-1", DisplayName: "slave-1", Status: "available",
					Card: json.RawMessage(`{"skills":["chat"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			gotPrompt = req.Prompt
			return &agentsdk.DelegateTaskResponse{TaskID: "t"}, nil
		},
	}
	tools := newTestTools(t, sdk)
	args := json.RawMessage(`{
        "prompt": "do the thing",
        "target_display_name": "slave-1",
        "skill": "chat"
    }`)
	_, err := toolByName(t, tools, "submit_task").Call(context.Background(), args)
	require.NoError(t, err)
	require.Contains(t, gotPrompt, "<USER_FILES_MANIFEST")
}

// TestSubmitTaskReturnsSessionID verifies that submit_task surfaces the
// SessionID returned by agentserver's DelegateTaskResponse.
func TestSubmitTaskReturnsSessionID(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{
					AgentID:     "ag-1",
					DisplayName: "slave-A",
					Status:      "available",
					Card:        json.RawMessage(`{"skills":["fanout","chat"]}`),
				},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{
				TaskID:    "T-1",
				SessionID: "S-abc",
				Status:    "assigned",
			}, nil
		},
	}
	tools := newTestTools(t, sdk)
	submit := toolByName(t, tools, "submit_task")
	raw, err := submit.Call(context.Background(),
		json.RawMessage(`{"prompt":"hi","target_display_name":"slave-A"}`))
	require.NoError(t, err)
	var got struct {
		TaskID    string `json:"task_id"`
		SessionID string `json:"session_id"`
	}
	require.NoError(t, json.Unmarshal(raw, &got))
	if got.TaskID != "T-1" {
		t.Errorf("task_id = %q, want T-1", got.TaskID)
	}
	if got.SessionID != "S-abc" {
		t.Errorf("session_id = %q, want S-abc", got.SessionID)
	}
}

func TestWaitTaskReturnsAwaitingUserWhenResultMarker(t *testing.T) {
	sdk := &fakeSDK{
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{
				TaskID: "T-1", Status: "completed", SessionID: "S-abc", TargetID: "ag-X",
				Result: json.RawMessage(`{"kind":"awaiting_user","session_id":"S-abc","question":{"kind":"ask_user","question":"pick?","options":["a","b"]}}`),
			}, nil
		},
	}
	tools := newTestTools(t, sdk)
	raw, err := toolByName(t, tools, "wait_task").Call(context.Background(),
		json.RawMessage(`{"task_id":"T-1","timeout_sec":2}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Status        string          `json:"status"`
		IsFinal       bool            `json:"is_final"`
		SessionID     string          `json:"session_id"`
		CurrentTaskID string          `json:"current_task_id"`
		TargetID      string          `json:"target_id"`
		Question      json.RawMessage `json:"question"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "awaiting_user" {
		t.Errorf("status = %q", got.Status)
	}
	if got.IsFinal {
		t.Errorf("is_final should be false")
	}
	if got.SessionID != "S-abc" {
		t.Errorf("session_id = %q", got.SessionID)
	}
	if got.CurrentTaskID != "T-1" {
		t.Errorf("current_task_id = %q", got.CurrentTaskID)
	}
	if got.TargetID != "ag-X" {
		t.Errorf("target_id = %q", got.TargetID)
	}
	if !strings.Contains(string(got.Question), `"question":"pick?"`) {
		t.Errorf("question payload not propagated: %s", got.Question)
	}
}

func TestWaitTaskUnwrapsKindFinal(t *testing.T) {
	sdk := &fakeSDK{
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{
				TaskID: "T-2", Status: "completed", SessionID: "S-z",
				Result: json.RawMessage(`{"kind":"final","summary":"all done","session_id":"S-z"}`),
			}, nil
		},
	}
	tools := newTestTools(t, sdk)
	raw, err := toolByName(t, tools, "wait_task").Call(context.Background(),
		json.RawMessage(`{"task_id":"T-2","timeout_sec":2}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Status, Output string }
	_ = json.Unmarshal(raw, &got)
	if got.Status != "completed" {
		t.Errorf("status = %q", got.Status)
	}
	if got.Output != "all done" {
		t.Errorf("output = %q, want 'all done'", got.Output)
	}
}

func TestWaitTaskPassesThroughLegacyResultUnwrapped(t *testing.T) {
	sdk := &fakeSDK{
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{
				TaskID: "T-3", Status: "completed", Output: "bash ran",
			}, nil
		},
	}
	tools := newTestTools(t, sdk)
	raw, err := toolByName(t, tools, "wait_task").Call(context.Background(),
		json.RawMessage(`{"task_id":"T-3","timeout_sec":2}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Status, Output string }
	_ = json.Unmarshal(raw, &got)
	if got.Status != "completed" {
		t.Errorf("status = %q", got.Status)
	}
	if got.Output != "bash ran" {
		t.Errorf("output = %q, want 'bash ran'", got.Output)
	}
}

func TestGetTaskReturnsAwaitingUserWhenResultMarker(t *testing.T) {
	sdk := &fakeSDK{
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{
				TaskID: "T-1", Status: "completed", SessionID: "S-abc", TargetID: "ag-X",
				Result: json.RawMessage(`{"kind":"awaiting_user","session_id":"S-abc","question":{"kind":"ask_user","question":"pick?"}}`),
			}, nil
		},
	}
	tools := newTestTools(t, sdk)
	raw, err := toolByName(t, tools, "get_task").Call(context.Background(),
		json.RawMessage(`{"task_id":"T-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Status string }
	_ = json.Unmarshal(raw, &got)
	if got.Status != "awaiting_user" {
		t.Errorf("get_task status = %q, want awaiting_user", got.Status)
	}
}

func TestResumeTaskHappy(t *testing.T) {
	var delegated agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			// Two states: T-1 = the paused task; T-2 = the new chat_resume task.
			switch id {
			case "T-1":
				return &agentsdk.TaskInfo{
					TaskID: "T-1", Status: "completed", SessionID: "S-abc", TargetID: "ag-X",
					Result: json.RawMessage(`{"kind":"awaiting_user","session_id":"S-abc","question":{"kind":"ask_user","question":"q?"}}`),
				}, nil
			case "T-2":
				return &agentsdk.TaskInfo{
					TaskID: "T-2", Status: "completed", SessionID: "S-abc",
					Result: json.RawMessage(`{"kind":"final","summary":"finalised","session_id":"S-abc"}`),
				}, nil
			}
			return nil, fmt.Errorf("unknown task: %s", id)
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = req
			return &agentsdk.DelegateTaskResponse{TaskID: "T-2", SessionID: "S-abc"}, nil
		},
	}
	tools := newTestTools(t, sdk)
	raw, err := toolByName(t, tools, "resume_task").Call(context.Background(),
		json.RawMessage(`{"last_task_id":"T-1","answer":"yes","timeout_sec":2}`))
	if err != nil {
		t.Fatal(err)
	}

	if delegated.Skill != "chat_resume" {
		t.Errorf("Skill = %q, want chat_resume", delegated.Skill)
	}
	if delegated.TargetID != "ag-X" {
		t.Errorf("TargetID = %q, want ag-X", delegated.TargetID)
	}
	var body struct {
		SessionID string `json:"session_id"`
		Answer    string `json:"answer"`
		Kind      string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(delegated.Prompt), &body); err != nil {
		t.Fatalf("Prompt is not JSON: %v (%q)", err, delegated.Prompt)
	}
	if body.SessionID != "S-abc" || body.Answer != "yes" || body.Kind != "ask_user" {
		t.Errorf("Prompt body = %+v", body)
	}
	var got struct{ Status, Output string }
	_ = json.Unmarshal(raw, &got)
	if got.Status != "completed" || got.Output != "finalised" {
		t.Errorf("expected completed/finalised, got %+v", got)
	}
}

func TestResumeTaskRejectsWhenNotAwaitingUser(t *testing.T) {
	sdk := &fakeSDK{
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{
				TaskID: "T-1", Status: "completed",
				Result: json.RawMessage(`{"kind":"final","summary":"x"}`),
			}, nil
		},
	}
	tools := newTestTools(t, sdk)
	_, err := toolByName(t, tools, "resume_task").Call(context.Background(),
		json.RawMessage(`{"last_task_id":"T-1","answer":"a"}`))
	if err == nil || !strings.Contains(err.Error(), "not awaiting_user") {
		t.Errorf("expected not-awaiting-user error, got %v", err)
	}
}

func TestResumeTaskRejectsMissingArgs(t *testing.T) {
	tools := newTestTools(t, &fakeSDK{})
	cases := []string{
		`{"answer":"x"}`,                   // missing last_task_id
		`{"last_task_id":"T"}`,             // missing answer
		`{"last_task_id":"T","answer":""}`, // empty answer
		`{"last_task_id":"","answer":"x"}`, // empty last_task_id
	}
	for _, body := range cases {
		_, err := toolByName(t, tools, "resume_task").Call(context.Background(), json.RawMessage(body))
		if err == nil || !strings.Contains(err.Error(), "required") {
			t.Errorf("body=%s expected 'required' error, got %v", body, err)
		}
	}
}
