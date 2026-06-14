package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// fakeSDKQueue lets each child task return a queued (status, output) pair keyed by request order.
type fakeSDKQueue struct {
	mu         sync.Mutex
	agents     []agentsdk.AgentCard
	nextID     int
	queue      []agentsdk.TaskInfo
	dispatched []agentsdk.DelegateTaskRequest
}

func (f *fakeSDKQueue) DiscoverAgents(_ context.Context) ([]agentsdk.AgentCard, error) {
	return f.agents, nil
}
func (f *fakeSDKQueue) DelegateTask(_ context.Context, req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dispatched = append(f.dispatched, req)
	f.nextID++
	return &agentsdk.DelegateTaskResponse{TaskID: fmt.Sprintf("c%d", f.nextID)}, nil
}
func (f *fakeSDKQueue) WaitForTask(_ context.Context, id string, _ time.Duration) (*agentsdk.TaskInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queue) == 0 {
		return &agentsdk.TaskInfo{TaskID: id, Status: "failed"}, nil
	}
	info := f.queue[0]
	f.queue = f.queue[1:]
	info.TaskID = id
	return &info, nil
}

type cancelAwareSDK struct {
	mu         sync.Mutex
	agents     []agentsdk.AgentCard
	dispatched []agentsdk.DelegateTaskRequest
}

func (f *cancelAwareSDK) DiscoverAgents(_ context.Context) ([]agentsdk.AgentCard, error) {
	return f.agents, nil
}

func (f *cancelAwareSDK) DelegateTask(_ context.Context, req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dispatched = append(f.dispatched, req)
	return &agentsdk.DelegateTaskResponse{TaskID: req.Prompt}, nil
}

func (f *cancelAwareSDK) WaitForTask(ctx context.Context, id string, _ time.Duration) (*agentsdk.TaskInfo, error) {
	if id == "fail" {
		return &agentsdk.TaskInfo{TaskID: id, Status: "failed", FailureReason: "boom"}, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

type nonCooperativeSDK struct {
	mu           sync.Mutex
	agents       []agentsdk.AgentCard
	dispatched   []agentsdk.DelegateTaskRequest
	slowStarted  chan struct{}
	failReturned chan struct{}
	slowOnce     sync.Once
	failOnce     sync.Once
}

func (f *nonCooperativeSDK) DiscoverAgents(_ context.Context) ([]agentsdk.AgentCard, error) {
	return f.agents, nil
}

func (f *nonCooperativeSDK) DelegateTask(_ context.Context, req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dispatched = append(f.dispatched, req)
	return &agentsdk.DelegateTaskResponse{TaskID: req.Prompt}, nil
}

func (f *nonCooperativeSDK) WaitForTask(_ context.Context, id string, _ time.Duration) (*agentsdk.TaskInfo, error) {
	if id == "fail" {
		if f.failReturned != nil {
			f.failOnce.Do(func() { close(f.failReturned) })
		}
		return &agentsdk.TaskInfo{TaskID: id, Status: "failed", FailureReason: "boom"}, nil
	}
	if id == "slow" && f.slowStarted != nil {
		f.slowOnce.Do(func() { close(f.slowStarted) })
	}
	select {}
}

type fakeArtifactResolver struct {
	contentByURL map[string]string
	gets         []string
	puts         []fakeArtifactPut
}

type fakeArtifactPut struct {
	URL     string
	Content string
	MIME    string
}

func (f *fakeArtifactResolver) GetArtifact(ctx context.Context, rawURL string) ([]byte, string, error) {
	f.gets = append(f.gets, rawURL)
	content, ok := f.contentByURL[rawURL]
	if !ok {
		return nil, "", fmt.Errorf("artifact not found: %s", rawURL)
	}
	return []byte(content), "text/plain", nil
}

func (f *fakeArtifactResolver) PutWrite(ctx context.Context, rawURL string, content []byte, mime string) error {
	f.puts = append(f.puts, fakeArtifactPut{URL: rawURL, Content: string(content), MIME: mime})
	return nil
}

func (f *fakeArtifactResolver) AuthorizeArtifactURL(rawURL string) (string, bool) {
	if strings.Contains(rawURL, "/api/artifacts/") {
		return rawURL + "?token=test-token", true
	}
	return rawURL, false
}

func TestFanout_HappyDiamond(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{
			{AgentID: "agent-a", Status: "available"},
			{AgentID: "agent-b", Status: "available"},
			{AgentID: "agent-c", Status: "available"},
			{AgentID: "agent-d", Status: "available"},
		},
		queue: []agentsdk.TaskInfo{
			{Status: "completed", Output: "out-1"},
			{Status: "completed", Output: "out-2"},
			{Status: "completed", Output: "out-3"},
			{Status: "completed", Output: "out-4"},
		},
	}
	o := newOrch(t, sdk, "plan_diamond")
	res, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})
	_ = res
	require.NoError(t, err)
	require.Len(t, sdk.dispatched, 4)
}

func TestFanout_BestEffortPartialFailure(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{
			{AgentID: "agent-a", Status: "available"},
			{AgentID: "agent-b", Status: "available"},
		},
		queue: []agentsdk.TaskInfo{
			{Status: "failed"},
		},
	}
	o := newOrch(t, sdk, "plan_chain")
	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "x"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "required node a failed")
	// Only "a" was dispatched; "b" was skipped.
	require.Len(t, sdk.dispatched, 1)
}

func TestFanout_EmitsPlanningCompleted(t *testing.T) {
	// §1.5 added a planner-level target_id whitelist with 3-attempt retry.
	// plan_chain returns a 2-node plan referencing agent-a + agent-b; both
	// must be registered so the planner accepts the plan and the orchestrator
	// emits EventMasterPlanningCompleted. (Pre-§1.5 only agent-a was
	// registered and dispatch failed later, but the event still fired; post-
	// §1.5 the planner rejects the unknown target_id up front, so the event
	// never fires.) The test's intent is to verify the event is emitted on
	// successful planning — not to exercise dispatch-of-unknown-agent.
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{
			{AgentID: "agent-a", Status: "available"},
			{AgentID: "agent-b", Status: "available"},
		},
		queue: []agentsdk.TaskInfo{
			{Status: "completed", Output: "ok"},
			{Status: "completed", Output: "ok"},
		},
	}
	obs := &fakeObserver{}
	o := newOrchWithObserver(t, sdk, "plan_chain", obs)

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})

	require.NoError(t, err)
	require.NotEmpty(t, eventsOfType(obs.events, observer.EventMasterPlanningCompleted))
}

func TestFanout_DoesNotEmitPlanningCompletedForInvalidInitialPlan(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{
			{AgentID: "agent-a", Status: "available"},
			{AgentID: "agent-b", Status: "available"},
		},
	}
	obs := &fakeObserver{}
	o := newOrchWithObserver(t, sdk, "plan_invalid_cycle", obs)

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})

	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid plan")
	require.Empty(t, eventsOfType(obs.events, observer.EventMasterPlanningCompleted))
}

func TestFanout_DoesNotEmitPlanningCompletedForInvalidReplan(t *testing.T) {
	dir := t.TempDir()
	roundFile := filepath.Join(dir, "round")
	bin := filepath.Join(dir, "planner.sh")
	err := os.WriteFile(bin, []byte(`#!/usr/bin/env bash
round=$(cat "$ROUND_FILE" 2>/dev/null || echo 0)
case "$round" in
  0)
    cat <<'EOF'
[{"id":"n0","target_id":"agent-a","skill":"mcp","prompt":"{\"server\":\"srv\",\"tool\":\"render\",\"args\":{\"bad\":true}}"}]
EOF
    ;;
  1)
    cat <<'EOF'
[
  {"id":"x","target_id":"agent-a","prompt":"x","depends_on":["y"]},
  {"id":"y","target_id":"agent-a","prompt":"y","depends_on":["x"]}
]
EOF
    ;;
  *) echo "REDUCED";;
esac
echo $((round+1)) > "$ROUND_FILE"
`), 0o755)
	require.NoError(t, err)
	t.Setenv("ROUND_FILE", roundFile)

	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{agentWithRenderTool(t)},
	}
	obs := &fakeObserver{}
	p := planner.New(config.Planner{TimeoutSec: 5}, claudebe.New(agentbackend.ClaudeConfig{Bin: bin}, nil).LLM())
	s, err := store.Open(filepath.Join(t.TempDir(), "x.db"))
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	o := New(s, p, sdk, config.Fanout{MaxConcurrency: 4, DefaultPolicy: "best_effort"}, "self-id", obs)

	_, err = o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})

	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid mcp validation replan")
	require.Len(t, eventsOfType(obs.events, observer.EventMasterPlanningCompleted), 1)
}

func TestFanout_AllOrNothingFailsImmediately(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{
			{AgentID: "agent-a", Status: "available"},
			{AgentID: "agent-b", Status: "available"},
		},
		queue: []agentsdk.TaskInfo{
			{Status: "failed"},
		},
	}
	o := newOrch(t, sdk, "plan_chain")
	o.cfg.PolicyBySkill = map[string]string{"fanout": "all_or_nothing"}

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "x"})
	require.Error(t, err)
}

func TestFanout_AllOrNothingEmitsTerminalEventsForInFlightSiblings(t *testing.T) {
	sdk := &cancelAwareSDK{
		agents: []agentsdk.AgentCard{
			{AgentID: "agent-a", Status: "available"},
			{AgentID: "agent-b", Status: "available"},
		},
	}
	obs := &fakeObserver{}
	o := newOrchWithObserver(t, sdk, "plan_parallel", obs)
	o.cfg.PolicyBySkill = map[string]string{"fanout": "all_or_nothing"}

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "x"})
	require.Error(t, err)
	require.Len(t, sdk.dispatched, 2)

	done := eventsOfType(obs.events, observer.EventMasterSubtaskDone)
	require.Len(t, done, 2)
	seen := map[string]string{}
	for _, ev := range done {
		seen[ev.SubtaskID] = ev.Status
	}
	require.Equal(t, "failed", seen["fail"])
	require.Equal(t, "failed", seen["slow"])
}

func TestFanoutNoProgressTimeoutFollowsSubTaskTimeout(t *testing.T) {
	got := fanoutNoProgressTimeout(config.Fanout{SubTaskDefaults: config.SubTaskDefaults{TimeoutSec: 600}})
	require.Equal(t, 630*time.Second, got)
}

func TestFanout_RequiredFailureDrainBoundedForNonCooperativeSibling(t *testing.T) {
	sdk := &nonCooperativeSDK{
		agents: []agentsdk.AgentCard{
			{AgentID: "agent-a", Status: "available"},
			{AgentID: "agent-b", Status: "available"},
		},
		slowStarted:  make(chan struct{}),
		failReturned: make(chan struct{}),
	}
	o := newOrch(t, sdk, "plan_parallel")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := o.Run(ctx, executor.Task{ID: "p", Skill: "fanout", Prompt: "x"})
		done <- err
	}()

	for slowSeen, failSeen := false, false; !slowSeen || !failSeen; {
		select {
		case <-sdk.slowStarted:
			slowSeen = true
		case <-sdk.failReturned:
			failSeen = true
		case err := <-done:
			require.Error(t, err)
			require.Contains(t, err.Error(), "required node fail failed")
			return
		case <-time.After(5 * time.Second):
			t.Fatalf("test setup did not reach required state: slow_seen=%v fail_seen=%v", slowSeen, failSeen)
		}
	}
	cancel()

	select {
	case err := <-done:
		require.Error(t, err)
		require.Contains(t, err.Error(), "required node fail failed")
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after required failure and context cancellation")
	}
}

func TestFanout_PassesNodeSkillToDelegateTask(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{agentWithTool(t, "x", "y")},
		queue:  []agentsdk.TaskInfo{{Status: "completed", Output: "ok"}},
	}
	o := newOrch(t, sdk, "plan_with_skill")
	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})
	require.NoError(t, err)
	require.Len(t, sdk.dispatched, 1)
	require.Equal(t, "mcp", sdk.dispatched[0].Skill,
		"orchestrator must thread Node.Skill into DelegateTask")
}

func TestFanout_FetchesObserverArtifactBeforeMCPJSONRender(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{agentWithCSVProfileTool(t)},
		queue:  []agentsdk.TaskInfo{{Status: "completed", Output: "profile ok"}},
	}
	resolver := &fakeArtifactResolver{
		contentByURL: map[string]string{
			"http://observer.local/api/artifacts/art_csv": "order_id,amount\n1,12.50\n",
		},
	}
	o := newOrch(t, sdk, "plan_fetch_artifact_then_mcp")
	o.artifacts = resolver

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})

	require.NoError(t, err)
	require.Equal(t, []string{"http://observer.local/api/artifacts/art_csv"}, resolver.gets)
	require.Len(t, sdk.dispatched, 1)
	require.Equal(t, "mcp", sdk.dispatched[0].Skill)
	require.JSONEq(t,
		`{"server":"csv_profile","tool":"profile_csv","args":{"csv_text":"order_id,amount\n1,12.50\n"}}`,
		sdk.dispatched[0].Prompt)
}

func TestFanout_PutsFinalSummaryToObserverWriteURLs(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "planner.sh")
	err := os.WriteFile(bin, []byte(`#!/usr/bin/env bash
stdin=$(cat)
if [[ "$stdin" == *"task reducer"* ]]; then
  echo "FINAL REPORT"
else
  echo '[{"id":"n1","target_id":"agent-a","prompt":"do work"}]'
fi
`), 0o755)
	require.NoError(t, err)

	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{{AgentID: "agent-a", Status: "available"}},
		queue:  []agentsdk.TaskInfo{{Status: "completed", Output: "work output"}},
	}
	resolver := &fakeArtifactResolver{}
	p := planner.New(config.Planner{TimeoutSec: 5}, claudebe.New(agentbackend.ClaudeConfig{Bin: bin}, nil).LLM())
	s, err := store.Open(filepath.Join(t.TempDir(), "x.db"))
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	o := New(s, p, sdk, config.Fanout{MaxConcurrency: 4, DefaultPolicy: "best_effort"}, "self-id", nil)
	o.artifacts = resolver

	prompt := `<USER_FILES_MANIFEST version=1>
{"files":[],"writes":[{"path":"/tmp/out.md","kind":"file","overwrite":true,"put_url":"http://observer.local/api/writes/wr_1"}]}
</USER_FILES_MANIFEST>

write a report`
	_, err = o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: prompt})

	require.NoError(t, err)
	require.Len(t, resolver.puts, 1)
	require.Equal(t, "http://observer.local/api/writes/wr_1", resolver.puts[0].URL)
	require.Equal(t, "FINAL REPORT", resolver.puts[0].Content)
	require.Equal(t, "text/markdown; charset=utf-8", resolver.puts[0].MIME)
}

func TestFanout_SkipsChatPutToObserverWriteURLAndLetsReducerWrite(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "planner.sh")
	err := os.WriteFile(bin, []byte(`#!/usr/bin/env bash
stdin=$(cat)
if [[ "$stdin" == *"task reducer"* ]]; then
  echo "FINAL REDUCED REPORT"
else
  cat <<'EOF'
[{"id":"n1","target_id":"agent-a","prompt":"Write a concise markdown report, then PUT the bytes to http://observer.local/api/writes/wr_1 (single PUT, expects 200)."}]
EOF
fi
`), 0o755)
	require.NoError(t, err)

	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{{AgentID: "agent-a", Status: "available"}},
	}
	resolver := &fakeArtifactResolver{}
	p := planner.New(config.Planner{TimeoutSec: 5}, claudebe.New(agentbackend.ClaudeConfig{Bin: bin}, nil).LLM())
	s, err := store.Open(filepath.Join(t.TempDir(), "x.db"))
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	o := New(s, p, sdk, config.Fanout{MaxConcurrency: 4, DefaultPolicy: "best_effort"}, "self-id", nil)
	o.artifacts = resolver

	prompt := `<USER_FILES_MANIFEST version=1>
{"files":[],"writes":[{"path":"/tmp/out.md","kind":"file","overwrite":true,"put_url":"http://observer.local/api/writes/wr_1"}]}
</USER_FILES_MANIFEST>

write a report`
	_, err = o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: prompt})

	require.NoError(t, err)
	require.Empty(t, sdk.dispatched)
	require.Len(t, resolver.puts, 1)
	require.Equal(t, "http://observer.local/api/writes/wr_1", resolver.puts[0].URL)
	require.Equal(t, "FINAL REDUCED REPORT", resolver.puts[0].Content)
}

func TestFanout_PutsFallbackSummaryWhenReducerFails(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "planner.sh")
	err := os.WriteFile(bin, []byte(`#!/usr/bin/env bash
stdin=$(cat)
if [[ "$stdin" == *"task reducer"* ]]; then
  echo "reducer unavailable" >&2
  exit 1
else
  echo '[{"id":"n1","target_id":"agent-a","prompt":"profile data"}]'
fi
`), 0o755)
	require.NoError(t, err)

	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{{AgentID: "agent-a", Status: "available"}},
		queue:  []agentsdk.TaskInfo{{Status: "completed", Output: `{"row_count":5,"risk":"high"}`}},
	}
	resolver := &fakeArtifactResolver{}
	p := planner.New(config.Planner{TimeoutSec: 5}, claudebe.New(agentbackend.ClaudeConfig{Bin: bin}, nil).LLM())
	s, err := store.Open(filepath.Join(t.TempDir(), "x.db"))
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	o := New(s, p, sdk, config.Fanout{MaxConcurrency: 4, DefaultPolicy: "best_effort"}, "self-id", nil)
	o.artifacts = resolver

	prompt := `<USER_FILES_MANIFEST version=1>
{"files":[],"writes":[{"path":"/tmp/out.md","kind":"file","overwrite":true,"put_url":"http://observer.local/api/writes/wr_1"}]}
</USER_FILES_MANIFEST>

write a report`
	_, err = o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: prompt})

	require.NoError(t, err)
	require.Len(t, resolver.puts, 1)
	require.Contains(t, resolver.puts[0].Content, "# Task Summary")
	require.Contains(t, resolver.puts[0].Content, "Reducer failed")
	require.Contains(t, resolver.puts[0].Content, `{"row_count":5,"risk":"high"}`)
}

func TestAuthorizeMCPArtifactURLsAddsObserverToken(t *testing.T) {
	resolver := &fakeArtifactResolver{contentByURL: map[string]string{"http://observer/api/artifacts/art_1": "ready"}}
	o := &Orchestrator{artifacts: resolver}
	got, err := o.authorizeMCPArtifactURLs(context.Background(), `{"server":"s","tool":"t","args":{"csv_url":"http://observer/api/artifacts/art_1"}}`)
	require.NoError(t, err)
	require.JSONEq(t, `{"server":"s","tool":"t","args":{"csv_url":"http://observer/api/artifacts/art_1?token=test-token"}}`, got)
	require.Equal(t, []string{"http://observer/api/artifacts/art_1"}, resolver.gets)
}

func TestFanout_InvalidMCPArgsRejectedBeforeDispatch(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{agentWithRenderTool(t)},
	}
	obs := &fakeObserver{}
	o := newOrchWithObserver(t, sdk, "plan_mcp_invalid_arg", obs)

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})

	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown argument put_url_128")
	require.Len(t, sdk.dispatched, 0)

	validationFailed := firstEventOfType(t, obs.events, observer.EventMasterMCPCallValidationFailed)
	require.Equal(t, "p", validationFailed.TaskID)
	require.Equal(t, "n0", validationFailed.SubtaskID)
	require.Equal(t, "agent-a", validationFailed.TargetAgentID)
	require.Equal(t, observer.RoleSlave, validationFailed.TargetRole)
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(validationFailed.Payload, &payload))
	require.Contains(t, payload["validation_error"], "unknown argument put_url_128")
	require.Equal(t, true, payload["required"])
	require.NotEmpty(t, payload["prompt"])
}

func TestFanout_InvalidMCPArgsTriggersBoundedReplan(t *testing.T) {
	rf := filepath.Join(t.TempDir(), "round")
	t.Setenv("FAKE_PLANNER_ROUND_FILE", rf)

	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{agentWithRenderTool(t)},
		queue:  []agentsdk.TaskInfo{{Status: "completed", Output: "ok"}},
	}
	obs := &fakeObserver{}
	o := newOrchWithObserver(t, sdk, "plan_mcp_validation_replan", obs)

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})

	require.NoError(t, err)
	require.Len(t, sdk.dispatched, 1)
	require.Equal(t, "mcp", sdk.dispatched[0].Skill)
	require.JSONEq(t, `{"server":"srv","tool":"render","args":{"n":7}}`, sdk.dispatched[0].Prompt)

	validationFailed := eventsOfType(obs.events, observer.EventMasterMCPCallValidationFailed)
	require.Len(t, validationFailed, 1)
	require.Equal(t, "n0", validationFailed[0].SubtaskID)
	done := eventsOfType(obs.events, observer.EventMasterSubtaskDone)
	statusByNode := map[string]string{}
	for _, ev := range done {
		statusByNode[ev.SubtaskID] = ev.Status
	}
	require.Equal(t, "skipped", statusByNode["n0"])
	require.Equal(t, "completed", statusByNode["n0_n1"])
}

func TestRenamePlanIDs_RewritesJSONFieldPathTemplateReferences(t *testing.T) {
	nodes := []planner.Node{
		{ID: "n1", TargetID: "agent-a", Prompt: "profile"},
		{
			ID:        "n2",
			TargetID:  "agent-a",
			Skill:     "mcp",
			Prompt:    `{"server":"s","tool":"t","args":{"rows":{{n1.output.rows}},"policy":{{ n1.output.policy.rules }}}}`,
			DependsOn: []string{"n1"},
		},
	}

	got := renamePlanIDs(nodes, "retry")

	require.Equal(t, "retry_n2", got[1].ID)
	require.Equal(t, []string{"retry_n1"}, got[1].DependsOn)
	require.Equal(t, `{"server":"s","tool":"t","args":{"rows":{{retry_n1.output.rows}},"policy":{{retry_n1.output.policy.rules}}}}`, got[1].Prompt)
}

func TestMCPValidationReplanContext_IncludesJSONFieldPathGuidance(t *testing.T) {
	ctx := mcpValidationReplanContext(
		planner.Node{ID: "n3", TargetID: "agent-a", Prompt: `{"args":{"rows":{{n1.output}}}}`},
		[]agentsdk.AgentCard{agentWithRenderTool(t)},
		`{"server":"srv","tool":"render","args":{"n":"bad"}}`,
		fmt.Errorf("argument rows must be array"),
	)

	require.Contains(t, ctx, "failed_node_prompt_template")
	require.Contains(t, ctx, "{{n1.output.rows}}")
	require.Contains(t, ctx, "Do not replace a direct MCP call with an ordinary chat node")
}

// TestFanout_ReplanSupersedeBeforeAppend pins the ordering contract of the
// mcp validation replan path: MarkSuperseded must run BEFORE Append, AND
// any replan node whose dep is the just-superseded id must be surfaced as
// an explicit skipped done event (visible to reducer + observer) rather
// than silently dropped.
//
// Setup: round-0 plan = a single mcp node "n0" whose args fail validation;
// round-1 (replan) plan = a single node "v2" that depends_on "n0". After
// renamePlanIDs, the new node id is "n0_v2" with dep "n0".
//
// With NEW order (MarkSuperseded → Append → MarkOrphaned scan):
// MarkSuperseded marks ONLY n0 (s.rev[n0] is empty, n0_v2 not yet appended).
// Append then accepts n0_v2 (dep n0 is known) but leaves it out of pending
// (n0 is finished:skipped, not 'completed'). The post-Append orphan scan
// detects n0_v2 deps on a superseded id and marks it skipped explicitly —
// the reducer sees it via AllFinished() and the observer sees an explicit
// done event.
//
// With OLD order (Append → MarkSuperseded): Append registers n0_v2 in
// s.rev[n0], so MarkSuperseded(n0) transitively skipped n0_v2 too — same
// observable effect (skipped event) but for the wrong reason (transitive
// walk via rev edges instead of explicit orphan detection), and the
// outcome depended on call ordering rather than planner intent.
//
// With INTERMEDIATE (post-Task-4) order (MarkSuperseded → Append, no
// orphan scan): n0_v2 was orphaned and silently invisible — no done
// event, no reducer entry — a silent-orphan regression. This test pins
// the FIX for that regression.
//
// Fixes §1.2 #8 (part 2) of docs/review-2026-06-13.md.
//
// PR #11 review P2 update: when the orphaned new node is REQUIRED (the
// fake-planner's v2 has no "optional":true), the parent must fail rather
// than silently completing. Without this, a planner that emits a replan
// depending on a superseded id would produce a "successful" parent task
// where required work was discarded.
func TestFanout_ReplanSupersedeBeforeAppend(t *testing.T) {
	rf := filepath.Join(t.TempDir(), "round")
	t.Setenv("FAKE_PLANNER_ROUND_FILE", rf)

	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{agentWithRenderTool(t)},
	}
	obs := &fakeObserver{}
	o := newOrchWithObserver(t, sdk, "plan_mcp_validation_replan_dep_old", obs)

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})

	// n0_v2 is REQUIRED (no optional:true in fake-planner output) and gets
	// orphaned. Parent must fail with a "required" error — the silent
	// optional-downgrade was the PR #11 P2 regression.
	require.Error(t, err, "required orphan replan node must fail the parent task, not silently downgrade to optional")
	require.Contains(t, err.Error(), "n0_v2", "error must name the orphan node id")
	require.Contains(t, err.Error(), "required", "error must mark the orphan as required-failure")
	require.Empty(t, sdk.dispatched, "n0_v2 must not be dispatched (dep n0 is finished:skipped, not completed)")

	done := eventsOfType(obs.events, observer.EventMasterSubtaskDone)
	statusByNode := map[string]string{}
	errByNode := map[string]string{}
	for _, ev := range done {
		statusByNode[ev.SubtaskID] = ev.Status
		var payload map[string]string
		_ = json.Unmarshal(ev.Payload, &payload)
		errByNode[ev.SubtaskID] = payload["error"]
	}
	require.Equal(t, "skipped", statusByNode["n0"],
		"original node must be reported as skipped via MarkSuperseded")
	require.Equal(t, "skipped", statusByNode["n0_v2"],
		"orphan node n0_v2 must be explicitly reported as skipped via MarkOrphaned — "+
			"without this it would be invisible to AllFinished()/reducer/observer, "+
			"and the task would silently 'succeed' with the real work missing. got=%v", statusByNode)
	require.Contains(t, errByNode["n0_v2"], "orphaned",
		"orphan event must include a reason explaining the cause")
	require.Contains(t, errByNode["n0_v2"], "n0",
		"orphan event must name the superseded dep")
}

// TestFanout_ReplanOptionalOrphanContinues verifies that when the orphan
// replan node is explicitly marked optional, the parent still completes
// (only the optional work is silently skipped — that's the contract of
// optional nodes). This is the boundary case the P2 fix must NOT break.
// Mirror of TestFanout_ReplanSupersedeBeforeAppend with optional:true.
func TestFanout_ReplanOptionalOrphanContinues(t *testing.T) {
	rf := filepath.Join(t.TempDir(), "round")
	t.Setenv("FAKE_PLANNER_ROUND_FILE", rf)

	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{agentWithRenderTool(t)},
	}
	obs := &fakeObserver{}
	o := newOrchWithObserver(t, sdk, "plan_mcp_validation_replan_dep_old_optional", obs)

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})
	require.NoError(t, err, "optional orphan replan must NOT fail the parent")
	require.Empty(t, sdk.dispatched, "n0_v2 must not be dispatched")

	done := eventsOfType(obs.events, observer.EventMasterSubtaskDone)
	statusByNode := map[string]string{}
	for _, ev := range done {
		statusByNode[ev.SubtaskID] = ev.Status
	}
	require.Equal(t, "skipped", statusByNode["n0_v2"],
		"optional orphan still reported as skipped (just doesn't fail the parent)")
}

func TestFanout_MCPValidationReplanRejectsDisallowedContractTarget(t *testing.T) {
	dir := t.TempDir()
	roundFile := filepath.Join(dir, "round")
	bin := filepath.Join(dir, "planner.sh")
	err := os.WriteFile(bin, []byte(`#!/usr/bin/env bash
round=$(cat "$ROUND_FILE" 2>/dev/null || echo 0)
case "$round" in
  0)
    cat <<'EOF'
[{"id":"n0","target_id":"agent-a","skill":"mcp","prompt":"{\"server\":\"srv\",\"tool\":\"render\",\"args\":{\"n\":7,\"put_url_128\":\"http://x\"}}"}]
EOF
    ;;
  1)
    cat <<'EOF'
[{"id":"n1","target_id":"agent-b","skill":"chat","prompt":"bypass"}]
EOF
    ;;
  *) echo "REDUCED";;
esac
echo $((round+1)) > "$ROUND_FILE"
`), 0o755)
	require.NoError(t, err)
	t.Setenv("ROUND_FILE", roundFile)

	// §1.5 added a planner-level target_id whitelist with 3-attempt retry that
	// runs *before* contract-policy validation. To exercise the contract-policy
	// rejection path (the actual subject under test), agent-b must be a known
	// SDK agent (so the planner accepts the replan) but excluded from the
	// contract's AllowedTargets (so contract policy rejects it).
	// fanoutContractPrompt already sets AllowedTargets = []string{"agent-a"},
	// so we just need to register agent-b in the SDK snapshot.
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{
			agentWithRenderTool(t),
			{AgentID: "agent-b", Status: "available"},
		},
		queue: []agentsdk.TaskInfo{{Status: "completed", Output: "bypass"}},
	}
	p := planner.New(config.Planner{TimeoutSec: 5}, claudebe.New(agentbackend.ClaudeConfig{Bin: bin}, nil).LLM())
	s, err := store.Open(filepath.Join(t.TempDir(), "x.db"))
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	o := New(s, p, sdk, config.Fanout{MaxConcurrency: 4, DefaultPolicy: "best_effort"}, "self-id", nil)
	prompt := fanoutContractPrompt(t, contract.TaskContract{}, "do")

	_, err = o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: prompt})

	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid mcp validation replan")
	require.Contains(t, err.Error(), "target agent-b is not allowed")
	require.Empty(t, sdk.dispatched)
}

func TestFanout_ValidMCPArgsDispatchesOnce(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{agentWithRenderTool(t)},
		queue:  []agentsdk.TaskInfo{{Status: "completed", Output: "ok"}},
	}
	o := newOrch(t, sdk, "plan_mcp_valid")

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})

	require.NoError(t, err)
	require.Len(t, sdk.dispatched, 1)
	require.Equal(t, "mcp", sdk.dispatched[0].Skill)
}

func TestFanout_RequiredFailureFailsParentUnderBestEffort(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{
			{AgentID: "agent-a", Status: "available"},
			{AgentID: "agent-b", Status: "available"},
		},
		queue: []agentsdk.TaskInfo{
			{Status: "failed", FailureReason: "boom"},
			{Status: "completed", Output: "ok"},
		},
	}
	o := newOrch(t, sdk, "plan_optional_failure")

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})

	require.Error(t, err)
	require.Contains(t, err.Error(), "required node")
}

func TestFanout_RequiredFailureMarksDownstreamSkipped(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{
			{AgentID: "agent-a", Status: "available"},
			{AgentID: "agent-b", Status: "available"},
		},
		queue: []agentsdk.TaskInfo{
			{Status: "failed", FailureReason: "boom"},
		},
	}
	obs := &fakeObserver{}
	o := newOrchWithObserver(t, sdk, "plan_chain", obs)

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})

	require.Error(t, err)
	done := eventsOfType(obs.events, observer.EventMasterSubtaskDone)
	statusByNode := map[string]string{}
	for _, ev := range done {
		statusByNode[ev.SubtaskID] = ev.Status
	}
	require.Equal(t, "failed", statusByNode["a"])
	require.Equal(t, "skipped", statusByNode["b"])

	requiredFailed := eventsOfType(obs.events, observer.EventMasterRequiredNodeFailed)
	require.Len(t, requiredFailed, 2)
	require.Equal(t, "a", requiredFailed[0].SubtaskID)
	require.Equal(t, "b", requiredFailed[1].SubtaskID)
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(requiredFailed[1].Payload, &payload))
	require.Equal(t, true, payload["required"])
	require.Equal(t, "b", payload["node_id"])
	require.Equal(t, "skipped", payload["status"])
}

func TestFanout_OptionalFailureReducedUnderBestEffort(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{
			{AgentID: "agent-a", Status: "available"},
			{AgentID: "agent-b", Status: "available"},
		},
		queue: []agentsdk.TaskInfo{
			{Status: "completed", Output: "ok"},
			{Status: "failed", FailureReason: "optional boom"},
		},
	}
	o := newOrch(t, sdk, "plan_optional_failure")

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})

	require.NoError(t, err)
	require.Len(t, sdk.dispatched, 2)
}

func TestFanout_EmitsPlanDispatchAndDoneEvents(t *testing.T) {
	sdk := &fakeSDKQueue{
		agents: []agentsdk.AgentCard{agentWithTool(t, "x", "y")},
		queue:  []agentsdk.TaskInfo{{Status: "completed", Output: "ok"}},
	}
	obs := &fakeObserver{}
	o := newOrchWithObserver(t, sdk, "plan_with_skill", obs)

	_, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "build something"})
	require.NoError(t, err)

	planCreated := firstEventOfType(t, obs.events, observer.EventMasterPlanCreated)
	require.Equal(t, "p", planCreated.TaskID)
	var planPayload map[string][]string
	require.NoError(t, json.Unmarshal(planCreated.Payload, &planPayload))
	require.Equal(t, []string{"n0"}, planPayload["node_ids"])

	dispatched := eventsOfType(obs.events, observer.EventMasterSubtaskDispatched)
	require.Len(t, dispatched, 1)
	require.Equal(t, "p", dispatched[0].TaskID)
	require.Equal(t, "n0", dispatched[0].SubtaskID)
	require.Equal(t, "c1", dispatched[0].ChildTaskID)
	require.Equal(t, "agent-a", dispatched[0].TargetAgentID)
	require.Equal(t, observer.RoleSlave, dispatched[0].TargetRole)
	require.Equal(t, "assigned", dispatched[0].Status)
	require.Equal(t, "build something", dispatched[0].Summary)
	require.Equal(t, "y", dispatched[0].SubtaskSummary)

	done := firstEventOfType(t, obs.events, observer.EventMasterSubtaskDone)
	require.Equal(t, "p", done.TaskID)
	require.Equal(t, "n0", done.SubtaskID)
	require.Equal(t, "c1", done.ChildTaskID)
	require.Equal(t, "completed", done.Status)
	var donePayload map[string]string
	require.NoError(t, json.Unmarshal(done.Payload, &donePayload))
	require.Equal(t, "ok", donePayload["output"])
}

func eventsOfType(events []observer.Event, typ string) []observer.Event {
	var out []observer.Event
	for _, ev := range events {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

func firstEventOfType(t *testing.T, events []observer.Event, typ string) observer.Event {
	t.Helper()
	matches := eventsOfType(events, typ)
	require.NotEmpty(t, matches, "event type %s not emitted", typ)
	return matches[0]
}

func fanoutContractPrompt(t *testing.T, tc contract.TaskContract, body string) string {
	t.Helper()
	tc.Version = contract.Version
	tc.ConversationID = "conv-1"
	tc.Intent = contract.IntentSpec{
		Goal:            body,
		SuccessCriteria: []string{"done"},
	}
	tc.DataContract = contract.DataContract{
		WriteTargets: []contract.WriteTarget{{Type: contract.WriteTargetArtifact, Kind: "document", Name: "out.md"}},
	}
	tc.ExecutionPolicy.AllowedTargets = []string{"agent-a"}
	tc.ApplyDefaults()
	prompt, err := contract.EncodeEnvelope(tc, body)
	require.NoError(t, err)
	return prompt
}

func agentWithRenderTool(t *testing.T) agentsdk.AgentCard {
	t.Helper()
	return agentWithTool(t, "srv", "render")
}

func agentWithTool(t *testing.T, server, tool string) agentsdk.AgentCard {
	t.Helper()
	card := json.RawMessage(`{
		"mcp_tools":[{
			"server":` + strconv.Quote(server) + `,
			"name":` + strconv.Quote(tool) + `,
			"input_schema":{
				"type":"object",
				"properties":{"n":{"type":"number"}},
				"required":[]
			}
		}]
	}`)
	return agentsdk.AgentCard{AgentID: "agent-a", Status: "available", Card: card}
}

func agentWithCSVProfileTool(t *testing.T) agentsdk.AgentCard {
	t.Helper()
	card := json.RawMessage(`{
		"mcp_tools":[{
			"server":"csv_profile",
			"name":"profile_csv",
			"input_schema":{
				"type":"object",
				"properties":{"csv_text":{"type":"string"}},
				"required":["csv_text"]
			}
		}]
	}`)
	return agentsdk.AgentCard{AgentID: "agent-a", Status: "available", Card: card}
}

// panickingSDK panics inside DelegateTask, simulating an SDK / artifact /
// render bug that surfaces inside a fanout worker goroutine.
type panickingSDK struct {
	agents      []agentsdk.AgentCard
	panicMsg    string
	delegateHit int32
}

func (p *panickingSDK) DiscoverAgents(_ context.Context) ([]agentsdk.AgentCard, error) {
	return p.agents, nil
}
func (p *panickingSDK) DelegateTask(_ context.Context, _ agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
	panic(p.panicMsg)
}
func (p *panickingSDK) WaitForTask(_ context.Context, _ string, _ time.Duration) (*agentsdk.TaskInfo, error) {
	panic("WaitForTask should not be reached")
}

// TestFanout_GoroutinePanicTurnsIntoFailedNode verifies that a panic inside
// a fanout worker goroutine (here: SDK.DelegateTask) is recovered by the
// protectedGo wrapper and reported as a failed FinishedNode, rather than
// crashing the driver process. Fixes §1.2 #7 of docs/review-2026-06-13.md.
func TestFanout_GoroutinePanicTurnsIntoFailedNode(t *testing.T) {
	sdk := &panickingSDK{
		agents: []agentsdk.AgentCard{
			{AgentID: "agent-a", Status: "available"},
			{AgentID: "agent-b", Status: "available"},
		},
		panicMsg: "boom from sdk",
	}
	obs := &fakeObserver{}
	o := newOrchWithObserver(t, sdk, "plan_chain", obs)

	// Before the protectedGo wrapper, this Run call would propagate the
	// panic out of the goroutine and crash the test binary. After the
	// fix, the panic is recovered, node "a" is reported failed, and Run
	// returns the required-node-failure error in an orderly fashion.
	_, err := o.Run(context.Background(), executor.Task{
		ID: "panic-task", Skill: "fanout", Prompt: "do work",
	})
	require.Error(t, err, "expected required-node failure (panic-recovered)")
	require.Contains(t, err.Error(), "required node a failed",
		"panic in worker must surface as failed node, not crash")

	// And the recovered panic message must be threaded into the node's
	// error so operators can see what blew up.
	obs.mu.Lock()
	defer obs.mu.Unlock()
	var sawPanic bool
	for _, ev := range obs.events {
		if ev.Type == observer.EventMasterSubtaskDone && ev.SubtaskID == "a" && ev.Status == "failed" {
			var payload map[string]string
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				continue
			}
			if strings.Contains(payload["error"], "panic:") && strings.Contains(payload["error"], "boom from sdk") {
				sawPanic = true
				break
			}
		}
	}
	require.True(t, sawPanic, "expected node 'a' done event with panic-recovered error message")
}
