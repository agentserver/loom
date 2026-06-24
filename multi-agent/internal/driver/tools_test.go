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
	"github.com/yourorg/multi-agent/pkg/agentbackend"
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
	prompt        string
	systemContext string
	result        orchestration.RunnerResult
	err           error
	onRun         func(prompt, systemContext string)
}

func (f *fakeContractRunner) Run(ctx context.Context, prompt, systemContext string) (orchestration.RunnerResult, error) {
	f.prompt = prompt
	f.systemContext = systemContext
	if f.onRun != nil {
		f.onRun(prompt, systemContext)
	}
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
	cfg.DriverDefaults.WorkDir = dir     // §1.4 #17: tests place source_path inputs here
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
	_, err := tools.BindThread(context.Background(), "thr-test")
	require.NoError(t, err)

	_, err = toolByName(t, tools, "submit_task").Call(context.Background(), json.RawMessage(`{"prompt":"do work","skill":"chat"}`))
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
	_, err := tools.BindThread(context.Background(), "thr-routes-slave")
	require.NoError(t, err)
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

	tools := newTestTools(t, sdk)
	_, err = tools.BindThread(context.Background(), "thr-direct-slave-route")
	require.NoError(t, err)
	out, err := submitContractToolForTest(t, tools).Call(context.Background(), raw)
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

	tools := newTestTools(t, sdk)
	_, err = tools.BindThread(context.Background(), "thr-master-fanout-route")
	require.NoError(t, err)
	out, err := submitContractToolForTest(t, tools).Call(context.Background(), raw)
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

	tools := newTestTools(t, sdk)
	_, err = tools.BindThread(context.Background(), "thr-master-only-fanout")
	require.NoError(t, err)
	out, err := submitContractToolForTest(t, tools).Call(context.Background(), raw)
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
	_, err = tools.BindThread(context.Background(), "thr-fanout-uses")
	require.NoError(t, err)
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

	tools := newTestTools(t, sdk)
	_, err = tools.BindThread(context.Background(), "thr-fanout-norunner")
	require.NoError(t, err)
	_, err = submitContractToolForTest(t, tools).Call(context.Background(), raw)
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

	tools := newTestTools(t, sdk)
	_, err = tools.BindThread(context.Background(), "thr-capability-match")
	require.NoError(t, err)
	out, err := submitContractToolForTest(t, tools).Call(context.Background(), raw)
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

	tools := newTestTools(t, sdk)
	_, err = tools.BindThread(context.Background(), "thr-explicit-slave")
	require.NoError(t, err)
	out, err := submitContractToolForTest(t, tools).Call(context.Background(), raw)
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

	tools := newTestTools(t, sdk)
	_, err = tools.BindThread(context.Background(), "thr-explicit-master")
	require.NoError(t, err)
	out, err := submitContractToolForTest(t, tools).Call(context.Background(), raw)
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

	tools := newTestTools(t, sdk)
	_, err = tools.BindThread(context.Background(), "thr-offline-slave-master-fallback")
	require.NoError(t, err)
	out, err := submitContractToolForTest(t, tools).Call(context.Background(), raw)
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

	tools := newTestTools(t, sdk)
	_, err = tools.BindThread(context.Background(), "thr-no-master-fallback")
	require.NoError(t, err)
	_, err = submitContractToolForTest(t, tools).Call(context.Background(), raw)
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

	tools := newTestTools(t, sdk)
	_, err = tools.BindThread(context.Background(), "thr-unavailable-master")
	require.NoError(t, err)
	_, err = submitContractToolForTest(t, tools).Call(context.Background(), raw)
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

	tools := newTestTools(t, sdk)
	_, err = tools.BindThread(context.Background(), "thr-allowed-targets")
	require.NoError(t, err)
	out, err := submitContractToolForTest(t, tools).Call(context.Background(), raw)
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

	tools := newTestTools(t, sdk)
	_, err = tools.BindThread(context.Background(), "thr-rejected-target")
	require.NoError(t, err)
	_, err = submitContractToolForTest(t, tools).Call(context.Background(), raw)
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
	_, err := tools.BindThread(context.Background(), "thr-task-contract-warn")
	require.NoError(t, err)
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
	_, err := tools.BindThread(context.Background(), "thr-snapshot-warn")
	require.NoError(t, err)
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
	if _, err := tools.BindThread(context.Background(), "thr-test"); err != nil {
		t.Fatal(err)
	}
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
	if _, err := tools.BindThread(context.Background(), "thr-test"); err != nil {
		t.Fatal(err)
	}
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
	if _, err := tools.BindThread(context.Background(), "thr-test"); err != nil {
		t.Fatal(err)
	}
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
	if _, err := tools.BindThread(context.Background(), "thr-test"); err != nil {
		t.Fatal(err)
	}
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
	if _, err := tools.BindThread(context.Background(), "thr-test"); err != nil {
		t.Fatal(err)
	}
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
			if isParentLinkDelegation(skill) {
				_, bindErr := tools.BindThread(context.Background(), "thr-test")
				require.NoError(t, bindErr)
			}
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
	_, err := tools.BindThread(context.Background(), "thr-test")
	require.NoError(t, err)
	args := json.RawMessage(`{
        "prompt": "do the thing",
        "target_display_name": "slave-1",
        "skill": "chat"
    }`)
	_, err = toolByName(t, tools, "submit_task").Call(context.Background(), args)
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
	_, err := tools.BindThread(context.Background(), "thr-test")
	require.NoError(t, err)
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
	_, _ = tools.BindThread(context.Background(), "thr-happy")
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
	_, _ = tools.BindThread(context.Background(), "thr-notawaiting")
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

// TestSubmitTask_DegradesUpdateWriteTaskFailureToWarning verifies that when
// DelegateTask succeeds (slave is already running the task) but observer
// UpdateWriteTask fails, submit_task still returns task_id and surfaces the
// failure as a warning. This is the §1.1 #1 invariant: "DelegateTask success
// ⇒ Claude always gets a task_id".
func TestSubmitTask_DegradesUpdateWriteTaskFailureToWarning(t *testing.T) {
	observerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/write-tokens":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"write_id":"w-1","put_url":"http://example/put"}`))
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/writes/"):
			http.Error(w, "store unavailable", http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected observer request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer observerServer.Close()

	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-a", DisplayName: "slave-a", Status: "available",
				Card: json.RawMessage(`{"skills":["chat"]}`),
			}}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-77", SessionID: "sess-77", Status: "assigned"}, nil
		},
	}
	tools := newTestTools(t, sdk)
	if _, bindErr := tools.BindThread(context.Background(), "thr-test"); bindErr != nil {
		t.Fatal(bindErr)
	}
	tools.cfg.Observer.Enabled = true
	tools.cfg.Observer.URL = observerServer.URL
	tools.cfg.Observer.APIKey = "ak-test"
	tools.cfg.DriverDefaults.ArtifactTransport = ArtifactTransportObserverLazy
	tools.relay = NewObserverRelay(tools.cfg, stubTokenSource("test-token"))

	tmp := t.TempDir()
	args, _ := json.Marshal(map[string]any{
		"prompt":              "do work",
		"skill":               "chat",
		"target_display_name": "slave-a",
		"write_paths":         []map[string]any{{"path": tmp + "/out.txt", "overwrite": true}},
	})

	out, err := toolByName(t, tools, "submit_task").Call(context.Background(), args)
	require.NoError(t, err, "submit_task must NOT return error; DelegateTask already succeeded")
	require.Contains(t, string(out), `"task_id":"task-77"`)
	require.Contains(t, string(out), `"warnings"`)
	require.Contains(t, string(out), "update_write_task")

	// reg.TrackTask must still have been called so that wait_task can later
	// find the write tokens for this task.
	written := tools.reg.WrittenFiles("task-77")
	require.NotNil(t, written, "TrackTask should have been called even after warning")
}

// TestWaitTask_RejectsEmptyTaskID prevents WrittenFiles("")+ForgetTask("")
// from silently nuking an unrelated zero-key registry entry.
// Fixes §1.1 #4 of docs/review-2026-06-13.md.
func TestWaitTask_RejectsEmptyTaskID(t *testing.T) {
	tools := newTestTools(t, &fakeSDK{})
	_, err := toolByName(t, tools, "wait_task").Call(context.Background(),
		json.RawMessage(`{"task_id":""}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "task_id is required")
}

func TestGetTask_RejectsEmptyTaskID(t *testing.T) {
	tools := newTestTools(t, &fakeSDK{})
	_, err := toolByName(t, tools, "get_task").Call(context.Background(),
		json.RawMessage(`{"task_id":""}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "task_id is required")
}

// TestWaitTask_UsesArgsTaskIDForRegistry verifies the registry key is the
// task_id the caller submitted (= the same id we stored in reg.TrackTask),
// even when the SDK echoes a different info.TaskID. The emit event still
// uses info.TaskID for human-facing display.
func TestWaitTask_UsesArgsTaskIDForRegistry(t *testing.T) {
	tmp := t.TempDir()
	target := tmp + "/out.txt"
	if err := os.WriteFile(target, []byte("hello"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	sdk := &fakeSDK{
		getTaskFunc: func(id string, _ bool) (*agentsdk.TaskInfo, error) {
			// Server returns DIFFERENT TaskID (an alias) — registry must
			// still use the args.TaskID we originally tracked.
			return &agentsdk.TaskInfo{
				TaskID: "server-alias-99",
				Status: "completed",
			}, nil
		},
	}
	obs := &fakeObserver{}
	tools := newTestToolsWithObserver(t, sdk, obs)

	// Manually populate registry as if submit_task had been called with id=client-1.
	tok := tools.reg.RegisterWrite(target, true, "")
	tools.reg.RebindWriteTokenTaskID(tok, "client-1")
	tools.reg.RecordWritten("client-1", WrittenFile{Path: target, Bytes: 5, SHA256: "abc"})
	tools.reg.TrackTask("client-1", []string{tok})

	out, err := toolByName(t, tools, "wait_task").Call(context.Background(),
		json.RawMessage(`{"task_id":"client-1","poll_interval_sec":1,"timeout_sec":5}`))
	require.NoError(t, err)
	require.Contains(t, string(out), `"written_files"`)
	require.Contains(t, string(out), target,
		"wait_task should have looked up writes under args.TaskID=client-1, not server-alias-99")

	// emit should have been called with info.TaskID for display.
	var sawAlias bool
	for _, ev := range obs.events {
		if ev.TaskID == "server-alias-99" {
			sawAlias = true
		}
	}
	require.True(t, sawAlias, "emit should still surface server-side alias for display")

	// Subsequent wait_task with the same client-1 must find empty
	// written_files because ForgetTask("client-1") cleared the entry.
	out2, err := toolByName(t, tools, "wait_task").Call(context.Background(),
		json.RawMessage(`{"task_id":"client-1","poll_interval_sec":1,"timeout_sec":5}`))
	require.NoError(t, err)
	require.NotContains(t, string(out2), target,
		"after wait_task, ForgetTask(args.TaskID) should have cleared the entry")
}

// TestSubmitContractTask_DegradesRecordDelegatedTaskFailureToWarning verifies
// the §1.1 #1 invariant for submit_contract_task: when DelegateTask succeeds
// (the slave is already running) but the local task journal append fails,
// the tool must still return task_id and surface the failure as a warning
// instead of pretending dispatch failed. Mirrors the pattern from
// TestSubmitTask_DegradesUpdateWriteTaskFailureToWarning.
func TestSubmitContractTask_DegradesRecordDelegatedTaskFailureToWarning(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-a", DisplayName: "slave-a", Status: "available",
				Card: json.RawMessage(`{"skills":["chat"]}`),
			}}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-77", SessionID: "sess-77", Status: "assigned"}, nil
		},
	}
	tools := newTestTools(t, sdk)
	_, err := tools.BindThread(context.Background(), "thr-degrade-journal")
	require.NoError(t, err)
	// Close the journal file underneath so the next Append fails with
	// "file already closed". recordDelegatedTask propagates that error,
	// which the tool must degrade to a warning rather than returning.
	require.NoError(t, tools.taskJournal.Close())

	args, _ := json.Marshal(map[string]any{
		"contract": map[string]any{
			"version":         1,
			"conversation_id": "conv-77",
			"intent": map[string]any{
				"goal":             "do work",
				"success_criteria": []string{"finishes"},
			},
			"data_contract": map[string]any{
				"write_targets": []map[string]any{
					{"type": "artifact", "kind": "summary", "name": "out.md"},
				},
			},
			"execution_policy": map[string]any{
				"routing": "direct_first",
			},
		},
		"prompt":              "do work",
		"skill":               "chat",
		"target_display_name": "slave-a",
	})

	out, err := toolByName(t, tools, "submit_contract_task").Call(context.Background(), args)
	require.NoError(t, err, "submit_contract_task must NOT return error; DelegateTask already succeeded")
	require.Contains(t, string(out), `"task_id":"task-77"`)
	// existing warnings field must include a record-delegated-task entry
	require.Regexp(t, `record[ _]delegated[ _]task`, string(out))
}

// TestWaitTask_DegradesSyncWritesFailureToWarning verifies that when wait_task
// observes the remote task is completed but the observer relay SyncWrites
// call fails (e.g. observer 401 from a botched bootstrap), wait_task still
// returns the task output as the main response and surfaces the relay
// failure as a warning. Without this, an observer-side hiccup would mask a
// successful task as a failure to the caller — violating the same §1.1 #1
// invariant submit_task already honors. Discovered by the PR #11 e2e re-run
// where every wait_task failed with "observer sync writes: list writes
// status 401" while the slave-side task had completed normally.
func TestWaitTask_DegradesSyncWritesFailureToWarning(t *testing.T) {
	observerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only fail the /api/writes list endpoint that SyncWrites hits;
		// other endpoints (not exercised by this test path) would 404 by
		// default which is fine for the assertion.
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/writes") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		t.Fatalf("unexpected observer request: %s %s", r.Method, r.URL.Path)
	}))
	defer observerServer.Close()

	sdk := &fakeSDK{
		getTaskFunc: func(id string, _ bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{
				TaskID: id,
				Status: "completed",
				Output: "slave finished fine",
			}, nil
		},
	}
	tools := newTestTools(t, sdk)
	tools.cfg.Observer.Enabled = true
	tools.cfg.Observer.URL = observerServer.URL
	tools.cfg.Observer.APIKey = "ak-test"
	tools.cfg.DriverDefaults.ArtifactTransport = ArtifactTransportObserverLazy
	tools.relay = NewObserverRelay(tools.cfg, stubTokenSource("test-token"))

	args, _ := json.Marshal(map[string]any{
		"task_id":           "task-wait-1",
		"poll_interval_sec": 1,
		"timeout_sec":       5,
	})
	out, err := toolByName(t, tools, "wait_task").Call(context.Background(), args)
	require.NoError(t, err, "wait_task must NOT return error when remote task is completed; observer relay failure degrades to warning")
	s := string(out)
	require.Contains(t, s, `"warnings"`)
	require.Contains(t, s, "observer sync writes")
	// Main response fields still present
	require.Contains(t, s, `"status":"completed"`)
	require.Contains(t, s, `"is_final":true`)
}

// --- loom_origin stamping tests (P2 Task 6) ---

// TestSubmitTask_ChatSkill_FailsWithoutBindThread codifies the fail-fast
// guard: a parent-link submission with read_paths AND write_paths
// supplied returns the actionable error before any token / audit / observer
// side effect. We MUST supply paths here — without them the registration /
// audit / observer-artifact code wouldn't run anyway and the assertion
// would be vacuous (a misplaced guard would still pass). Picking a real
// readable file and a writable parent-dir target makes the assertion meaningful.
func TestSubmitTask_ChatSkill_FailsWithoutBindThread(t *testing.T) {
	delegated := false
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave", DisplayName: "slave", Status: "available",
					Card: json.RawMessage(`{"skills":["chat"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = true
			return &agentsdk.DelegateTaskResponse{TaskID: "should-not-reach"}, nil
		},
	}
	tools := newLoomTestTools(t, sdk, "" /*home*/, "drv-1", "d1")
	tools.cfg.DriverDefaults.DisableUIDCheck = true
	// NO BindThread call — guard must reject.

	// Build a real read_path (a temp file) and a write_path (target under
	// the configured WorkDir). Both routes through the manifest / audit /
	// observer-write registration code, so any misplaced guard would leave
	// observable state and fail the assertion below.
	readPath := filepath.Join(t.TempDir(), "input.txt")
	require.NoError(t, os.WriteFile(readPath, []byte("hi"), 0o644))
	writePath := filepath.Join(tools.cfg.DriverDefaults.WorkDir, "out.txt")

	auditSizeBefore, err := os.Stat(filepath.Join(tools.cfg.DriverDefaults.AuditLogDir, "audit.log"))
	require.NoError(t, err)

	argsJSON, _ := json.Marshal(map[string]interface{}{
		"prompt":              "hi",
		"skill":               "chat",
		"target_display_name": "slave",
		"read_paths":          []string{readPath},
		"write_paths":         []map[string]interface{}{{"path": writePath, "overwrite": true}},
	})
	_, err = toolByName(t, tools, "submit_task").Call(context.Background(), argsJSON)
	require.Error(t, err)
	require.Contains(t, err.Error(), "driver not bound to a codex thread")
	require.False(t, delegated, "DelegateTask must NOT be called when bind missing")

	// 1) FileRegistry must contain no blob, no write token, no observer
	//    artifact. snapshotForTest below returns the union of all live maps.
	snap := tools.reg.snapshotForTest()
	require.Equal(t, 0, snap.Blobs, "no blobs registered")
	require.Equal(t, 0, snap.Dirs, "no dir tokens registered")
	require.Equal(t, 0, snap.Writes, "no write tokens registered")
	require.Equal(t, 0, snap.ObserverArtifacts, "no observer artifacts registered")

	// 2) AuditLog file must not have grown (no register_read /
	//    register_write entries appended).
	auditSizeAfter, err := os.Stat(filepath.Join(tools.cfg.DriverDefaults.AuditLogDir, "audit.log"))
	require.NoError(t, err)
	require.Equal(t, auditSizeBefore.Size(), auditSizeAfter.Size(),
		"audit log must not grow when bind guard short-circuits")
}

// TestSubmitTask_BashSkill_SucceedsWithoutBindThread codifies the narrowed-
// guard semantic: bash is not parent-link, so submit_task runs without bind
// AND systemContext stays empty (not stamped with a stale marker).
func TestSubmitTask_BashSkill_SucceedsWithoutBindThread(t *testing.T) {
	var captured agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-bash", DisplayName: "slave-bash", Status: "available",
					Card: json.RawMessage(`{"skills":["bash"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			captured = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-bash"}, nil
		},
	}
	tools := newLoomTestTools(t, sdk, "", "drv-1", "d1")
	// NO BindThread call.

	_, err := toolByName(t, tools, "submit_task").Call(context.Background(),
		json.RawMessage(`{"prompt":"run","skill":"bash","target_display_name":"slave-bash"}`))
	require.NoError(t, err)
	require.Equal(t, "bash", captured.Skill)
	require.Empty(t, captured.SystemContext,
		"bash submissions must NOT carry a loom_origin marker even after Q1")
}

// TestSubmitTask_FanoutDefault_FailsWithoutBindThread covers the empty-skill
// case (defaults to fanout, which IS parent-link).
func TestSubmitTask_FanoutDefault_FailsWithoutBindThread(t *testing.T) {
	tools := newLoomTestTools(t, &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "m", DisplayName: "master", Status: "available",
					Card: json.RawMessage(`{"skills":["fanout"]}`)},
			}, nil
		},
	}, "", "drv-1", "d1")
	_, err := toolByName(t, tools, "submit_task").Call(context.Background(),
		json.RawMessage(`{"prompt":"hi"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "driver not bound to a codex thread")
}

// newLoomTestTools creates test tools with CODEX_HOME, ShortID, and DisplayName set.
func newLoomTestTools(t *testing.T, sdk SDKClient, codexHome, shortID, displayName string) *Tools {
	t.Helper()
	tools := newTestTools(t, sdk)
	tools.cfg.Agent.CodexHome = codexHome
	tools.cfg.Credentials.ShortID = shortID
	tools.cfg.Discovery.DisplayName = displayName
	return tools
}

// TestSubmitTaskDefaultStampsLoomOrigin verifies that submit_task with no skill
// (defaults to "fanout") stamps a parseable loom_origin marker in SystemContext.
func TestSubmitTaskDefaultStampsLoomOrigin(t *testing.T) {
	const (
		shortID      = "drv-1"
		displayName  = "prod-driver"
		markerSessID = "thr-parent"
	)
	var captured agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "ag-1", DisplayName: "master", Status: "available",
					Card: json.RawMessage(`{"skills":["fanout"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			captured = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
	}
	tools := newLoomTestTools(t, sdk, "" /*codexHome unused*/, shortID, displayName)
	_, err := tools.BindThread(context.Background(), markerSessID)
	require.NoError(t, err)

	_, err = toolByName(t, tools, "submit_task").Call(context.Background(),
		json.RawMessage(`{"prompt":"do work"}`))
	require.NoError(t, err)

	if captured.Skill != "fanout" {
		t.Fatalf("default skill expected fanout, got %q", captured.Skill)
	}
	p, cleaned, ok := agentbackend.ParseLoomOrigin(captured.SystemContext)
	if !ok {
		t.Fatalf("loom_origin not stamped in SystemContext: %q", captured.SystemContext)
	}
	if p.AgentID != shortID {
		t.Errorf("AgentID = %q, want %q", p.AgentID, shortID)
	}
	if p.DisplayName != displayName {
		t.Errorf("DisplayName = %q, want %q", p.DisplayName, displayName)
	}
	if p.SessionID != markerSessID {
		t.Errorf("SessionID = %q, want %q", p.SessionID, markerSessID)
	}
	if strings.Contains(cleaned, "loom_origin") {
		t.Errorf("marker not removed from cleaned context: %q", cleaned)
	}
}

// TestSubmitTaskChatStampsLoomOrigin verifies that submit_task with skill="chat"
// also stamps the loom_origin marker.
// target_display_name is required because resolveTarget auto-selects by fanout skill.
func TestSubmitTaskChatStampsLoomOrigin(t *testing.T) {
	var captured agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "ag-2", DisplayName: "slave", Status: "available",
					Card: json.RawMessage(`{"skills":["chat"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			captured = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-2"}, nil
		},
	}
	tools := newLoomTestTools(t, sdk, "", "drv-2", "driver-2")
	_, err := tools.BindThread(context.Background(), "thr-chat")
	require.NoError(t, err)

	_, err = toolByName(t, tools, "submit_task").Call(context.Background(),
		json.RawMessage(`{"prompt":"chat work","skill":"chat","target_display_name":"slave"}`))
	require.NoError(t, err)

	if _, _, ok := agentbackend.ParseLoomOrigin(captured.SystemContext); !ok {
		t.Fatalf("chat delegation must be stamped with loom_origin, got SystemContext=%q", captured.SystemContext)
	}
}

// TestSubmitTaskBashDoesNotStampLoomOrigin verifies that submit_task with
// skill="bash" (a terminal non-codex skill) does NOT stamp the loom_origin marker.
// target_display_name is required because resolveTarget only auto-selects fanout agents.
func TestSubmitTaskBashDoesNotStampLoomOrigin(t *testing.T) {
	var captured agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "ag-3", DisplayName: "slave-bash", Status: "available",
					Card: json.RawMessage(`{"skills":["bash"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			captured = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-3"}, nil
		},
	}
	tools := newLoomTestTools(t, sdk, "", "drv-3", "driver-3")

	_, err := toolByName(t, tools, "submit_task").Call(context.Background(),
		json.RawMessage(`{"prompt":"run bash","skill":"bash","target_display_name":"slave-bash"}`))
	require.NoError(t, err)

	if _, _, ok := agentbackend.ParseLoomOrigin(captured.SystemContext); ok {
		t.Fatalf("bash delegation must NOT be stamped with loom_origin, got SystemContext=%q", captured.SystemContext)
	}
}

// TestSubmitContractTaskStampsLoomOrigin verifies that submit_contract_task stamps
// loom_origin when routing to a chat-capable slave.
func TestSubmitContractTaskStampsLoomOrigin(t *testing.T) {
	var captured agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-driver", DisplayName: "driver", Status: "available", Card: json.RawMessage(`{"skills":[]}`)},
				{AgentID: "slave-c", DisplayName: "slave-c", Status: "available",
					Card: json.RawMessage(`{"skills":["chat"],"short_id":"sc"}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			captured = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-c"}, nil
		},
	}
	tools := newLoomTestTools(t, sdk, "", "drv-4", "driver-4")
	_, err := tools.BindThread(context.Background(), "thr-contract")
	require.NoError(t, err)

	tc := testTaskContract()
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	_, err = submitContractToolForTest(t, tools).Call(context.Background(), raw)
	require.NoError(t, err)

	p, _, ok := agentbackend.ParseLoomOrigin(captured.SystemContext)
	if !ok {
		t.Fatalf("submit_contract_task chat delegation must be stamped with loom_origin, got SystemContext=%q", captured.SystemContext)
	}
	require.Equal(t, "thr-contract", p.SessionID,
		"captured parent thread id MUST equal the value passed to BindThread")
}

// TestResumeTask_FailsWithoutBindThread codifies that resume_task is always
// parent-link and refuses to operate when the driver is unbound, without
// even querying agentserver for the prior task.
func TestResumeTask_FailsWithoutBindThread(t *testing.T) {
	getCalled := false
	delegated := false
	sdk := &fakeSDK{
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			getCalled = true
			return nil, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = true
			return nil, nil
		},
	}
	tools := newLoomTestTools(t, sdk, "" /*home*/, "drv-1", "d1")
	// NO BindThread call.

	_, err := toolByName(t, tools, "resume_task").Call(context.Background(),
		json.RawMessage(`{"last_task_id":"T-1","answer":"yes"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "driver not bound to a codex thread")
	require.False(t, getCalled, "GetTask must not be called when bind missing")
	require.False(t, delegated, "DelegateTask must not be called when bind missing")
}

// TestResumeTaskStampsLoomOrigin verifies that resume_task stamps loom_origin
// on its chat_resume delegation.
func TestResumeTaskStampsLoomOrigin(t *testing.T) {
	var captured agentsdk.DelegateTaskRequest
	// Two distinct ids: agentserver bridges every task to a `cse_<uuid>`
	// session, while the slave's chat backend reports its own thread id in
	// the kind marker. Resume MUST send the marker id (slave thread), not
	// the bridge id, or the slave can't find the session.
	sdk := &fakeSDK{
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			switch id {
			case "T-resume-1":
				return &agentsdk.TaskInfo{
					TaskID: "T-resume-1", Status: "completed", SessionID: "cse_bridge-1", TargetID: "ag-R",
					Result: json.RawMessage(`{"kind":"awaiting_user","session_id":"slave-thr-1","question":{"kind":"ask_user","question":"continue?"}}`),
				}, nil
			case "T-resume-2":
				return &agentsdk.TaskInfo{
					TaskID: "T-resume-2", Status: "completed", SessionID: "cse_bridge-2",
					Result: json.RawMessage(`{"kind":"final","summary":"done","session_id":"slave-thr-1"}`),
				}, nil
			}
			return nil, fmt.Errorf("unknown task: %s", id)
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			captured = req
			return &agentsdk.DelegateTaskResponse{TaskID: "T-resume-2", SessionID: "cse_bridge-2"}, nil
		},
	}
	tools := newLoomTestTools(t, sdk, "", "drv-5", "driver-5")
	_, err := tools.BindThread(context.Background(), "thr-resume")
	require.NoError(t, err)

	_, err = toolByName(t, tools, "resume_task").Call(context.Background(),
		json.RawMessage(`{"last_task_id":"T-resume-1","answer":"yes","timeout_sec":2}`))
	require.NoError(t, err)

	if captured.Skill != "chat_resume" {
		t.Fatalf("expected skill=chat_resume, got %q", captured.Skill)
	}
	p, _, ok := agentbackend.ParseLoomOrigin(captured.SystemContext)
	if !ok {
		t.Fatalf("resume_task must stamp loom_origin, got SystemContext=%q", captured.SystemContext)
	}
	if p.AgentID != "drv-5" || p.DisplayName != "driver-5" || p.SessionID != "thr-resume" {
		t.Errorf("unexpected ParentLink: %+v", p)
	}
	// The resume body must target the slave's codex thread id (from the
	// kind marker), NOT agentserver's task-bridge `cse_<uuid>`. The slave
	// looks up its own session by that id; sending the bridge id would
	// fail-to-find or resume the wrong session.
	var body struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(captured.Prompt), &body); err != nil {
		t.Fatalf("resume body not JSON: %v (prompt=%q)", err, captured.Prompt)
	}
	if body.SessionID != "slave-thr-1" {
		t.Errorf("resume body session_id = %q, want slave-thr-1 (marker), not bridge id", body.SessionID)
	}
}

// =========================================================================
// Terminal child-link tests (Task 8)
// =========================================================================

// submitTaskForTerminalTest delegates a task to slave-2 (short_id "sl2") and
// returns the tools and the task_id for further get_task/wait_task calls.
func submitTaskForTerminalTest(t *testing.T, taskID string) (*Tools, string) {
	t.Helper()
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-2", DisplayName: "slave-2", Status: "available",
					Card: json.RawMessage(`{"skills":["chat"],"short_id":"sl2"}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: taskID, SessionID: "S1", Status: "submitted"}, nil
		},
		getTaskFunc: func(id string, _ bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{TaskID: id, Status: "completed",
				Output: `{"session_id":"child-sess"}`}, nil
		},
	}
	tools := newTestTools(t, sdk)
	if _, bindErr := tools.BindThread(context.Background(), "thr-test"); bindErr != nil {
		t.Fatal(bindErr)
	}
	_, err := toolByName(t, tools, "submit_task").Call(context.Background(),
		json.RawMessage(`{"prompt":"do work","skill":"chat","target_display_name":"slave-2"}`))
	require.NoError(t, err)
	return tools, taskID
}

// TestGetTaskWritesTerminalChildRecord verifies that get_task whose result
// carries a session_id marker appends a terminal record with child_session_id,
// child_agent_id, and status=completed.
func TestGetTaskWritesTerminalChildRecord(t *testing.T) {
	tools, taskID := submitTaskForTerminalTest(t, "T-gt-1")

	_, err := toolByName(t, tools, "get_task").Call(context.Background(),
		json.RawMessage(`{"task_id":"`+taskID+`"}`))
	require.NoError(t, err)

	rec, ok := tools.taskJournal.LatestByTaskID(taskID)
	require.True(t, ok)
	require.True(t, rec.Terminal)
	require.Equal(t, "child-sess", rec.ChildSessionID)
	require.Equal(t, "sl2", rec.ChildAgentID)
	require.Equal(t, "completed", rec.Status)
}

// TestWaitTaskWithFailedStatusWritesTerminalRecord verifies that wait_task that
// observes status=failed appends a terminal row with status=failed.
func TestWaitTaskWithFailedStatusWritesTerminalRecord(t *testing.T) {
	taskID := "T-wt-fail"
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-2", DisplayName: "slave-2", Status: "available",
					Card: json.RawMessage(`{"skills":["chat"],"short_id":"sl2"}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: taskID, SessionID: "S2", Status: "submitted"}, nil
		},
		getTaskFunc: func(id string, _ bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{TaskID: id, Status: "failed",
				Output: `{"session_id":"fail-sess"}`}, nil
		},
	}
	tools := newTestTools(t, sdk)
	if _, bindErr := tools.BindThread(context.Background(), "thr-test"); bindErr != nil {
		t.Fatal(bindErr)
	}
	_, err := toolByName(t, tools, "submit_task").Call(context.Background(),
		json.RawMessage(`{"prompt":"do work","skill":"chat","target_display_name":"slave-2"}`))
	require.NoError(t, err)

	_, err = toolByName(t, tools, "wait_task").Call(context.Background(),
		json.RawMessage(`{"task_id":"`+taskID+`","poll_interval_sec":1,"timeout_sec":5}`))
	// wait_task returns error for failed tasks in some codepaths; we only care about the journal
	_ = err

	rec, ok := tools.taskJournal.LatestByTaskID(taskID)
	require.True(t, ok)
	require.True(t, rec.Terminal)
	require.Equal(t, "fail-sess", rec.ChildSessionID)
	require.Equal(t, "sl2", rec.ChildAgentID)
	require.Equal(t, "failed", rec.Status)
}

// TestGetTaskTerminalRecordIsIdempotent verifies that calling get_task three
// times on the same completed task results in exactly one terminal row in the
// journal (raw line count, not just Recent).
func TestGetTaskTerminalRecordIsIdempotent(t *testing.T) {
	tools, taskID := submitTaskForTerminalTest(t, "T-idem-1")

	for i := 0; i < 3; i++ {
		_, err := toolByName(t, tools, "get_task").Call(context.Background(),
			json.RawMessage(`{"task_id":"`+taskID+`"}`))
		require.NoError(t, err)
	}

	// The journal should have exactly 2 lines: 1 delegation + 1 terminal.
	// Use raw line count to catch any spurious duplicate appends.
	lineCount := countJournalLines(t, tools.taskJournal.Path())
	require.Equal(t, 2, lineCount, "expected 1 delegation + 1 terminal line, got %d", lineCount)
}

// TestGetTaskStatusChangeAppendsSecondTerminalRow documents the defensive behavior:
// if status genuinely changes between polls (hypothetically failed → cancelled),
// a second terminal row is appended and Recent returns the newest.
func TestGetTaskStatusChangeAppendsSecondTerminalRow(t *testing.T) {
	taskID := "T-status-change"
	var callCount int
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-2", DisplayName: "slave-2", Status: "available",
					Card: json.RawMessage(`{"skills":["chat"],"short_id":"sl2"}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: taskID, SessionID: "S3", Status: "submitted"}, nil
		},
		getTaskFunc: func(id string, _ bool) (*agentsdk.TaskInfo, error) {
			callCount++
			status := "failed"
			if callCount > 1 {
				status = "cancelled"
			}
			return &agentsdk.TaskInfo{TaskID: id, Status: status,
				Output: `{"session_id":"change-sess"}`}, nil
		},
	}
	tools := newTestTools(t, sdk)
	if _, bindErr := tools.BindThread(context.Background(), "thr-test"); bindErr != nil {
		t.Fatal(bindErr)
	}
	_, err := toolByName(t, tools, "submit_task").Call(context.Background(),
		json.RawMessage(`{"prompt":"do work","skill":"chat","target_display_name":"slave-2"}`))
	require.NoError(t, err)

	// First poll: status=failed
	toolByName(t, tools, "get_task").Call(context.Background(), //nolint:errcheck
		json.RawMessage(`{"task_id":"`+taskID+`"}`))
	// Second poll: status=cancelled (genuinely different)
	toolByName(t, tools, "get_task").Call(context.Background(), //nolint:errcheck
		json.RawMessage(`{"task_id":"`+taskID+`"}`))

	// Raw line count: 1 delegation + 2 terminal rows
	lineCount := countJournalLines(t, tools.taskJournal.Path())
	require.Equal(t, 3, lineCount, "expected 1 delegation + 2 terminal lines")

	// Recent should return the newest terminal row (cancelled)
	recs, err := tools.taskJournal.Recent(10, taskID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", recs[0].Status)
}

// TestSubmitContractTaskDirectMatchPersistsChildAgentID verifies that
// submit_contract_task (direct-match branch) sets ChildAgentID from the
// target's short_id.
func TestSubmitContractTaskDirectMatchPersistsChildAgentID(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-driver", DisplayName: "driver", Status: "available", Card: json.RawMessage(`{"skills":[]}`)},
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["chat"],"short_id":"sa"}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "T-ct-1"}, nil
		},
	}
	tools := newTestTools(t, sdk)
	_, err := tools.BindThread(context.Background(), "thr-child-agent-id")
	require.NoError(t, err)
	tc := testTaskContract()
	tc.ExecutionPolicy.Routing = contract.RoutingDirectFirst
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	_, err = submitContractToolForTest(t, tools).Call(context.Background(), raw)
	require.NoError(t, err)

	rec, ok := tools.taskJournal.LatestByTaskID("T-ct-1")
	require.True(t, ok)
	require.Equal(t, "sa", rec.ChildAgentID, "submit_contract_task direct-match must persist target short_id into ChildAgentID")
}

// TestResumeTaskRecoversPriorChildAgentID verifies that resume_task reads the
// prior submit_task journal record and copies its ChildAgentID into the new
// delegation record.
func TestResumeTaskRecoversPriorChildAgentID(t *testing.T) {
	originalTaskID := "T-orig-1"
	resumeTaskID := "T-resume-R"
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-2", DisplayName: "slave-2", Status: "available",
					Card: json.RawMessage(`{"skills":["chat"],"short_id":"sl2"}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: resumeTaskID, SessionID: "S-res"}, nil
		},
		getTaskFunc: func(id string, _ bool) (*agentsdk.TaskInfo, error) {
			switch id {
			case originalTaskID:
				return &agentsdk.TaskInfo{
					TaskID: originalTaskID, Status: "completed", SessionID: "S-res", TargetID: "slave-2",
					Result: json.RawMessage(`{"kind":"awaiting_user","session_id":"S-res","question":{"kind":"ask_user","question":"continue?"}}`),
				}, nil
			case resumeTaskID:
				return &agentsdk.TaskInfo{
					TaskID: resumeTaskID, Status: "completed",
					Result: json.RawMessage(`{"kind":"final","summary":"done","session_id":"S-res"}`),
				}, nil
			}
			return nil, fmt.Errorf("unknown: %s", id)
		},
	}
	tools := newTestTools(t, sdk)
	if _, bindErr := tools.BindThread(context.Background(), "thr-test"); bindErr != nil {
		t.Fatal(bindErr)
	}

	// First: submit_task so the journal has a record with ChildAgentID="sl2"
	_, err := toolByName(t, tools, "submit_task").Call(context.Background(),
		json.RawMessage(`{"prompt":"start","skill":"chat","target_display_name":"slave-2"}`))
	require.NoError(t, err)

	// Manually fix the task_id so LatestByTaskID(originalTaskID) finds it.
	recs, err := tools.taskJournal.Recent(1, "")
	require.NoError(t, err)
	require.Len(t, recs, 1)
	fixedRec := recs[0]
	fixedRec.TaskID = originalTaskID
	require.NoError(t, tools.taskJournal.Append(fixedRec))

	// Now resume_task — it should recover ChildAgentID from the journal
	_, err = toolByName(t, tools, "resume_task").Call(context.Background(),
		json.RawMessage(`{"last_task_id":"`+originalTaskID+`","answer":"yes","timeout_sec":5}`))
	require.NoError(t, err)

	// The resume_task journal record must carry the recovered ChildAgentID
	rec, ok := tools.taskJournal.LatestByTaskID(resumeTaskID)
	require.True(t, ok)
	require.Equal(t, "sl2", rec.ChildAgentID, "resume_task must recover ChildAgentID from prior delegation record")
}

func TestSubmitContractTaskDriverFanoutCarriesLoomOrigin(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-driver", DisplayName: "driver", Status: "available"},
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["chat"],"mcp_tools":[{"server":"csv_profiler","name":"profile_orders_csv"}]}`)},
				{AgentID: "slave-b", DisplayName: "slave-b", Status: "available", Card: json.RawMessage(`{"skills":["chat"],"mcp_tools":[{"server":"csv_profiler","name":"profile_orders_csv"}]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			t.Fatal("driver_fanout route must not call sdk.DelegateTask")
			return nil, nil
		},
	}
	tc := testTaskContract()
	tc.ExecutionPolicy.Routing = contract.RoutingDirectFirst
	tc.CapabilityRequirements.Skills = nil
	tc.CapabilityRequirements.Tools = []string{"csv_profiler/profile_orders_csv"}
	raw, err := json.Marshal(map[string]interface{}{"contract": tc, "prompt": "analyze"})
	require.NoError(t, err)

	tools := newTestTools(t, sdk)
	// Simulate the driver knowing its own session — set ShortID and DisplayName
	tools.cfg.Credentials.ShortID = "drv-5"
	tools.cfg.Discovery.DisplayName = "driver-5"

	var capturedSystemContext string
	runner := &fakeContractRunner{
		result: orchestration.RunnerResult{Summary: "ok"},
		onRun: func(prompt, systemContext string) {
			capturedSystemContext = systemContext
		},
	}
	tools.SetContractRunner(runner)
	_, err = tools.BindThread(context.Background(), "thr-fanout")
	require.NoError(t, err)

	_, err = submitContractToolForTest(t, tools).Call(context.Background(), raw)
	require.NoError(t, err)

	p, _, ok := agentbackend.ParseLoomOrigin(capturedSystemContext)
	if !ok {
		t.Fatalf("driver_fanout ContractRunner.Run did not receive loom_origin marker; systemContext=%q", capturedSystemContext)
	}
	if p.AgentID != "drv-5" || p.DisplayName != "driver-5" {
		t.Errorf("unexpected ParentLink: %+v", p)
	}
	require.Equal(t, "thr-fanout", p.SessionID)
}

// TestSubmitContractTaskDriverFanoutFailsWithoutBind exercises Path A: two
// slaves match → route = driver_fanout → guard must fire before the
// contractRunner.Run call. No BindThread setup.
func TestSubmitContractTaskDriverFanoutFailsWithoutBind(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-driver", DisplayName: "driver", Status: "available"},
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available",
					Card: json.RawMessage(`{"skills":["chat"],"mcp_tools":[{"server":"csv_profiler","name":"profile_orders_csv"}]}`)},
				{AgentID: "slave-b", DisplayName: "slave-b", Status: "available",
					Card: json.RawMessage(`{"skills":["chat"],"mcp_tools":[{"server":"csv_profiler","name":"profile_orders_csv"}]}`)},
			}, nil
		},
	}
	tc := testTaskContract()
	tc.ExecutionPolicy.Routing = contract.RoutingDirectFirst
	tc.CapabilityRequirements.Skills = nil
	tc.CapabilityRequirements.Tools = []string{"csv_profiler/profile_orders_csv"}
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	tools := newTestTools(t, sdk)
	tools.SetContractRunner(&fakeContractRunner{result: orchestration.RunnerResult{Summary: "x"}})
	// NO BindThread call.

	_, err = submitContractToolForTest(t, tools).Call(context.Background(), raw)
	require.Error(t, err)
	require.Contains(t, err.Error(), "driver not bound to a codex thread")
}

// TestSubmitContractTaskDriverFanoutFailWithoutBind_NoObserverSideEffect is
// the side-effect guard: a recording httptest observer must see ZERO
// requests when the bind guard short-circuits. Catches an implementation
// that leaves SaveResourceSnapshot in its pre-guard position
// (contract_tools.go:62 today).
//
// IMPORTANT — observer wiring. `NewObserverRelay` returns nil unless
// (a) cfg.Observer.Enabled = true, (b) cfg.Observer.URL is set, AND
// (c) a non-nil TokenSource is passed (see observer_relay.go:44). The
// default newTestTools uses obs=nil, which makes the relay nil and any
// SaveResourceSnapshot a no-op — without these three knobs the test would
// pass even with the bug present. We construct an observer that satisfies
// all three, then point the relay at the recording server.
func TestSubmitContractTaskDriverFanoutFailWithoutBind_NoObserverSideEffect(t *testing.T) {
	var observerReqs int64
	observerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&observerReqs, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer observerSrv.Close()

	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-driver", DisplayName: "driver", Status: "available"},
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available",
					Card: json.RawMessage(`{"skills":["chat"],"mcp_tools":[{"server":"csv_profiler","name":"profile_orders_csv"}]}`)},
				{AgentID: "slave-b", DisplayName: "slave-b", Status: "available",
					Card: json.RawMessage(`{"skills":["chat"],"mcp_tools":[{"server":"csv_profiler","name":"profile_orders_csv"}]}`)},
			}, nil
		},
	}
	// Use newTestToolsWithObserver with a fakeObserver that is non-nil AND
	// satisfies TokenSource (fakeObserver.Token() returns "fake-token"
	// today — see tools_test.go:80). This makes toTokenSource(obs) non-nil.
	obs := &fakeObserver{}
	tools := newTestToolsWithObserver(t, sdk, obs)
	tools.SetContractRunner(&fakeContractRunner{})
	// Wire the relay against the recording server. Both Enabled and URL
	// are mandatory per observer_relay.go:44.
	tools.cfg.Observer.Enabled = true
	tools.cfg.Observer.URL = observerSrv.URL
	tools.relay = NewObserverRelay(tools.cfg, toTokenSource(obs))
	require.NotNil(t, tools.relay,
		"relay must be non-nil — without this the SaveResourceSnapshot call is a no-op and the test is vacuous")
	// NO BindThread call.

	tc := testTaskContract()
	tc.ExecutionPolicy.Routing = contract.RoutingDirectFirst
	tc.CapabilityRequirements.Skills = nil
	tc.CapabilityRequirements.Tools = []string{"csv_profiler/profile_orders_csv"}
	raw, err := json.Marshal(map[string]interface{}{"contract": tc})
	require.NoError(t, err)

	_, err = submitContractToolForTest(t, tools).Call(context.Background(), raw)
	require.Error(t, err)
	require.Contains(t, err.Error(), "driver not bound to a codex thread")
	require.EqualValues(t, 0, atomic.LoadInt64(&observerReqs),
		"observer must see ZERO requests when bind guard short-circuits")
}
