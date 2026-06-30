package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/config"
	"github.com/yourorg/multi-agent/internal/contract"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/planner"
	"github.com/yourorg/multi-agent/internal/store"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
	claudebe "github.com/yourorg/multi-agent/pkg/agentbackend/claude"
)

type fakeSDK struct {
	agents       []agentsdk.AgentCard
	delegateResp *agentsdk.DelegateTaskResponse
	delegateErr  error
	waitInfo     *agentsdk.TaskInfo
	waitErr      error

	delegatedReqs []agentsdk.DelegateTaskRequest
}

type fakeObserver struct {
	mu     sync.Mutex
	events []observer.Event
}

func (f *fakeObserver) Emit(ev observer.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
}

func (f *fakeSDK) DiscoverAgents(ctx context.Context) ([]agentsdk.AgentCard, error) {
	return f.agents, nil
}
func (f *fakeSDK) DelegateTask(ctx context.Context, req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
	f.delegatedReqs = append(f.delegatedReqs, req)
	return f.delegateResp, f.delegateErr
}
func (f *fakeSDK) WaitForTask(ctx context.Context, id string, _ time.Duration) (*agentsdk.TaskInfo, error) {
	return f.waitInfo, f.waitErr
}

func newOrch(t *testing.T, sdk SDKDelegator, mode string) *Orchestrator {
	return newOrchWithObserver(t, sdk, mode, nil)
}

func newOrchWithObserver(t *testing.T, sdk SDKDelegator, mode string, obs ObserverSink) *Orchestrator {
	t.Helper()
	t.Setenv("FAKE_PLANNER_MODE", mode)
	p := planner.New(config.Planner{TimeoutSec: 5}, claudebe.New(agentbackend.ClaudeConfig{Bin: fakePlannerForOrch(t)}, nil).LLM())
	s, err := store.Open(filepath.Join(t.TempDir(), "x.db"))
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return New(s, p, sdk, config.Fanout{MaxConcurrency: 4, DefaultPolicy: "best_effort"}, "self-id", obs)
}

func fakePlannerForOrch(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../../testdata/fake-planner.sh")
	require.NoError(t, err)
	return p
}

func TestRoute_HappyPath(t *testing.T) {
	sdk := &fakeSDK{
		agents:       []agentsdk.AgentCard{{AgentID: "agent-a", Status: "available"}},
		delegateResp: &agentsdk.DelegateTaskResponse{TaskID: "child-1"},
		waitInfo:     &agentsdk.TaskInfo{TaskID: "child-1", Status: "completed", Output: "child output"},
	}
	o := newOrch(t, sdk, "route_a")

	res, err := o.Run(context.Background(), executor.Task{ID: "p1", Skill: "route", Prompt: "do thing"})
	require.NoError(t, err)
	require.Equal(t, "child output", res.Summary)
	require.Len(t, sdk.delegatedReqs, 1)
	require.Equal(t, "agent-a", sdk.delegatedReqs[0].TargetID)
	require.Equal(t, "do thing", sdk.delegatedReqs[0].Prompt)
}

func TestRoute_NoCandidate(t *testing.T) {
	sdk := &fakeSDK{
		agents: []agentsdk.AgentCard{{AgentID: "agent-a", Status: "available"}},
	}
	o := newOrch(t, sdk, "route_empty")

	_, err := o.Run(context.Background(), executor.Task{ID: "p2", Skill: "route", Prompt: "x"})
	require.ErrorContains(t, err, "no candidate")
	require.Empty(t, sdk.delegatedReqs)
}

func TestRoute_ContractUniqueTargetBypassesPlanner(t *testing.T) {
	sdk := &fakeSDK{
		agents: []agentsdk.AgentCard{
			{AgentID: "self-id", Status: "available", Card: json.RawMessage(`{"skills":["route"]}`)},
			{AgentID: "agent-a", Status: "available", Card: json.RawMessage(`{"skills":["chat"]}`)},
		},
		delegateResp: &agentsdk.DelegateTaskResponse{TaskID: "child-1"},
		waitInfo:     &agentsdk.TaskInfo{TaskID: "child-1", Status: "completed", Output: "child output"},
	}
	o := newOrch(t, sdk, "route_empty")
	prompt := routeContractPrompt(t, []string{"self-id", "agent-a"}, []string{"chat"}, "do thing")

	res, err := o.Run(context.Background(), executor.Task{ID: "p-contract", Skill: "route", Prompt: prompt})

	require.NoError(t, err)
	require.Equal(t, "child output", res.Summary)
	require.Len(t, sdk.delegatedReqs, 1)
	require.Equal(t, "agent-a", sdk.delegatedReqs[0].TargetID)
	require.Equal(t, "do thing", sdk.delegatedReqs[0].Prompt)
}

func TestRoute_ChildFails(t *testing.T) {
	sdk := &fakeSDK{
		agents:       []agentsdk.AgentCard{{AgentID: "agent-a", Status: "available"}},
		delegateResp: &agentsdk.DelegateTaskResponse{TaskID: "child-1"},
		waitInfo:     &agentsdk.TaskInfo{TaskID: "child-1", Status: "failed"},
	}
	o := newOrch(t, sdk, "route_a")
	_, err := o.Run(context.Background(), executor.Task{ID: "p3", Skill: "route", Prompt: "x"})
	require.ErrorContains(t, err, "child")
}

func TestRoute_DelegateError(t *testing.T) {
	sdk := &fakeSDK{
		agents:      []agentsdk.AgentCard{{AgentID: "agent-a", Status: "available"}},
		delegateErr: errors.New("boom"),
	}
	o := newOrch(t, sdk, "route_a")
	_, err := o.Run(context.Background(), executor.Task{ID: "p4", Skill: "route", Prompt: "x"})
	require.ErrorContains(t, err, "boom")
}

func TestRoute_FiltersSelf(t *testing.T) {
	sdk := &fakeSDK{
		agents: []agentsdk.AgentCard{
			{AgentID: "self-id", Status: "available"},
			{AgentID: "agent-a", Status: "available"},
		},
		delegateResp: &agentsdk.DelegateTaskResponse{TaskID: "c"},
		waitInfo:     &agentsdk.TaskInfo{TaskID: "c", Status: "completed", Output: "ok"},
	}
	o := newOrch(t, sdk, "route_a")
	_, err := o.Run(context.Background(), executor.Task{ID: "p5", Skill: "route", Prompt: "x"})
	require.NoError(t, err)
	for _, r := range sdk.delegatedReqs {
		require.NotEqual(t, "self-id", r.TargetID)
	}
}

func TestRoute_UsesResultOutputWhenSDKOutputIsEmpty(t *testing.T) {
	sdk := &fakeSDK{
		agents:       []agentsdk.AgentCard{{AgentID: "agent-a", Status: "available"}},
		delegateResp: &agentsdk.DelegateTaskResponse{TaskID: "child-1"},
		waitInfo:     &agentsdk.TaskInfo{TaskID: "child-1", Status: "completed", Result: json.RawMessage(`{"output":"child result"}`)},
	}
	o := newOrch(t, sdk, "route_a")

	res, err := o.Run(context.Background(), executor.Task{ID: "p-result", Skill: "route", Prompt: "do thing"})

	require.NoError(t, err)
	require.Equal(t, "child result", res.Summary)
}

func routeContractPrompt(t *testing.T, allowedTargets, requiredSkills []string, body string) string {
	t.Helper()
	allowMaster := true
	tc := contract.TaskContract{
		Version:        contract.Version,
		ConversationID: "route-contract-test",
		Intent: contract.IntentSpec{
			Goal:            body,
			SuccessCriteria: []string{"done"},
		},
		DataContract: contract.DataContract{
			ReadArtifacts: []contract.ArtifactRef{},
			WriteTargets:  []contract.WriteTarget{{Type: contract.WriteTargetArtifact, Kind: "text", Name: "out.txt"}},
		},
		ExecutionPolicy: contract.ExecutionPolicy{
			Routing:        contract.RoutingMasterOnly,
			AllowMaster:    &allowMaster,
			AllowedTargets: allowedTargets,
		},
		CapabilityRequirements: contract.CapabilityRequirements{Skills: requiredSkills},
		RecoveryHint:           "test recovery hint",
	}
	tc.ApplyDefaults()
	prompt, err := contract.EncodeEnvelope(tc, body)
	require.NoError(t, err)
	return prompt
}

func TestRun_EmitsMasterTaskLifecycleEvents(t *testing.T) {
	sdk := &fakeSDK{
		agents:       []agentsdk.AgentCard{{AgentID: "agent-a", Status: "available"}},
		delegateResp: &agentsdk.DelegateTaskResponse{TaskID: "child-1"},
		waitInfo:     &agentsdk.TaskInfo{TaskID: "child-1", Status: "completed", Output: "child output"},
	}
	obs := &fakeObserver{}
	o := newOrchWithObserver(t, sdk, "route_a", obs)

	_, err := o.Run(context.Background(), executor.Task{ID: "p6", Skill: "route", Prompt: "do a useful thing"})
	require.NoError(t, err)

	require.GreaterOrEqual(t, len(obs.events), 2)
	require.Equal(t, observer.EventMasterTaskReceived, obs.events[0].Type)
	require.Equal(t, "p6", obs.events[0].TaskID)
	require.Equal(t, "do a useful thing", obs.events[0].Summary)
	require.Equal(t, "running", obs.events[0].Status)

	last := obs.events[len(obs.events)-1]
	require.Equal(t, observer.EventMasterTaskCompleted, last.Type)
	require.Equal(t, "p6", last.TaskID)
	require.Equal(t, "completed", last.Status)
	require.Equal(t, "do a useful thing", last.Summary)

	var payload map[string]string
	require.NoError(t, json.Unmarshal(last.Payload, &payload))
	require.Equal(t, "child output", payload["output"])
}

func TestRun_EmitsMasterTaskFailedWithErrorPayload(t *testing.T) {
	sdk := &fakeSDK{
		agents:      []agentsdk.AgentCard{{AgentID: "agent-a", Status: "available"}},
		delegateErr: errors.New("boom"),
	}
	obs := &fakeObserver{}
	o := newOrchWithObserver(t, sdk, "route_a", obs)

	_, err := o.Run(context.Background(), executor.Task{ID: "p7", Skill: "route", Prompt: "do a failing thing"})
	require.ErrorContains(t, err, "boom")

	last := obs.events[len(obs.events)-1]
	require.Equal(t, observer.EventMasterTaskFailed, last.Type)
	require.Equal(t, "p7", last.TaskID)
	require.Equal(t, "failed", last.Status)
	require.Equal(t, "do a failing thing", last.Summary)

	var payload map[string]string
	require.NoError(t, json.Unmarshal(last.Payload, &payload))
	require.Contains(t, payload["error"], "boom")
}

func TestTaskOutputUnwrapsChatKindFinalEnvelope(t *testing.T) {
	// The slave poller now forwards chat-skill results as the wrapped
	// kind:final envelope (#24 P2). Without unwrap, every chat child in a
	// fanout/route plan reduces to "" because none of the existing JSON
	// shapes ({"output":...}, JSON string) match.
	info := &agentsdk.TaskInfo{
		Status: "completed",
		Result: json.RawMessage(`{"kind":"final","summary":"the child output","session_id":"thr-7"}`),
	}
	if got := taskOutput(info); got != "the child output" {
		t.Fatalf("taskOutput should unwrap kind:final.summary, got %q", got)
	}
}

func TestTaskOutputAwaitingUserDoesNotMasqueradeAsSummary(t *testing.T) {
	// awaiting_user has no inner summary; we must fall through to the
	// other parsers (or "") rather than synthesise a fake string.
	info := &agentsdk.TaskInfo{
		Status: "completed",
		Result: json.RawMessage(`{"kind":"awaiting_user","question":{"kind":"ask_user"},"session_id":"thr-7"}`),
	}
	if got := taskOutput(info); got != "" {
		t.Fatalf("awaiting_user envelope must not become a non-empty summary, got %q", got)
	}
}

func TestTaskOutputFallbackOutputFieldStillWorks(t *testing.T) {
	// Existing {"output":...} contract (non-chat skills, e.g. bash) MUST
	// still parse — the chat unwrap is an addition, not a replacement.
	info := &agentsdk.TaskInfo{
		Status: "completed",
		Result: json.RawMessage(`{"output":"bash stdout"}`),
	}
	if got := taskOutput(info); got != "bash stdout" {
		t.Fatalf("non-chat {output:...} path regressed, got %q", got)
	}
}
