package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/yourorg/multi-agent/internal/humanloop"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestCodexAppServerSmoke(t *testing.T) {
	if os.Getenv("LOOM_CODEX_APPSERVER_SMOKE") != "1" {
		t.Skip("set LOOM_CODEX_APPSERVER_SMOKE=1 to run against local codex app-server")
	}
	t.Setenv(appServerUnsafeHumanloopRoutingEnv, "1")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	b, err := agentbackend.New(agentbackend.Config{
		Kind:       agentbackend.KindCodex,
		Bin:        "codex",
		WorkDir:    t.TempDir(),
		WorkerMode: "app_server",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := b.(interface{ Close() error }); ok {
		defer func() {
			if err := closer.Close(); err != nil {
				t.Logf("closing codex app-server backend: %v", err)
			}
		}()
	}

	wb, err := ensureCodexAppServerSmokeBackend(ctx, b)
	if err != nil {
		if ctx.Err() != nil {
			t.Fatalf("codex app-server handshake timed out with error %v", err)
		}
		t.Fatalf("codex app-server handshake failed: %v", err)
	}

	worker, err := wb.NewSessionWorker(ctx, agentbackend.Session{
		ID:         "ffffffff-ffff-4fff-bfff-ffffffffffff",
		Kind:       agentbackend.KindCodex,
		WorkingDir: t.TempDir(),
	})
	if worker != nil {
		_ = worker.Close()
		t.Fatalf("NewSessionWorker returned worker %#v, want nil for synthetic thread", worker)
	}
	if err == nil {
		t.Fatal("NewSessionWorker error = nil, want unavailable or resume failure for synthetic thread")
	}
	if ctx.Err() != nil {
		t.Fatalf("NewSessionWorker timed out with error %v", err)
	}
	if !errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
		t.Fatalf("NewSessionWorker error = %v, want ErrSessionWorkerUnavailable", err)
	}
}

func ensureCodexAppServerSmokeBackend(ctx context.Context, b agentbackend.Backend) (*workerBackend, error) {
	wb, ok := b.(*workerBackend)
	if !ok {
		return nil, fmt.Errorf("app_server codex backend has type %T, want *workerBackend", b)
	}
	if wb.manager == nil {
		return nil, agentbackend.ErrSessionWorkerUnavailable
	}
	if err := wb.manager.ensure(ctx); err != nil {
		return nil, err
	}
	return wb, nil
}

func TestCodexAppServerSmokeBackendEnsureFailsWhenBinaryUnavailable(t *testing.T) {
	t.Setenv(appServerUnsafeHumanloopRoutingEnv, "1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	b, err := agentbackend.New(agentbackend.Config{
		Kind:       agentbackend.KindCodex,
		Bin:        filepath.Join(t.TempDir(), "missing-codex"),
		WorkDir:    t.TempDir(),
		WorkerMode: "app_server",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := b.(interface{ Close() error }); ok {
		defer func() {
			if err := closer.Close(); err != nil {
				t.Logf("closing codex app-server backend: %v", err)
			}
		}()
	}

	wb, err := ensureCodexAppServerSmokeBackend(ctx, b)
	if err == nil {
		if wb != nil {
			_ = wb.Close()
		}
		t.Fatal("ensureCodexAppServerSmokeBackend error = nil, want unavailable for missing binary")
	}
	if !errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
		t.Fatalf("ensureCodexAppServerSmokeBackend error = %v, want ErrSessionWorkerUnavailable", err)
	}
}

func TestCodexAppServerHumanloopRoutingSmoke(t *testing.T) {
	if os.Getenv("LOOM_CODEX_APPSERVER_HUMANLOOP_ROUTING_SMOKE") != "1" {
		t.Skip("set LOOM_CODEX_APPSERVER_HUMANLOOP_ROUTING_SMOKE=1 to verify real codex app-server per-thread MCP routing")
	}
	humanloopBin := os.Getenv("LOOM_CODEX_APPSERVER_HUMANLOOP_BIN")
	if humanloopBin == "" {
		t.Skip("set LOOM_CODEX_APPSERVER_HUMANLOOP_BIN to a driver-agent binary with humanloop-mcp support")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	cfg := agentbackend.Config{Kind: agentbackend.KindCodex, Bin: "codex", WorkDir: t.TempDir()}
	m := newAppServerManager(cfg, nil)
	if err := m.ensure(ctx); err != nil {
		t.Fatalf("codex app-server ensure failed: %v", err)
	}
	defer func() {
		if err := m.close(); err != nil {
			t.Logf("closing codex app-server manager: %v", err)
		}
	}()

	rpc := appServerSmokeRPC(t, m)
	workDirA := filepath.Join(cfg.WorkDir, "a")
	workDirB := filepath.Join(cfg.WorkDir, "b")
	threadA, threadB := appServerSmokeThreadIDs(t, ctx, cfg.Bin, workDirA, workDirB)

	srvA, epA := appServerSmokeHumanloopEndpoint(t, "a")
	defer srvA.Close()
	srvB, epB := appServerSmokeHumanloopEndpoint(t, "b")
	defer srvB.Close()

	payloads := make(chan appServerSmokeRoutedPayload, 2)
	appServerSmokeReceiveHumanloop(t, srvA, "a", payloads)
	appServerSmokeReceiveHumanloop(t, srvB, "b", payloads)

	appServerSmokeResumeWithHumanloop(t, ctx, m, threadA, workDirA, humanloopBin, epA)
	appServerSmokeResumeWithHumanloop(t, ctx, m, threadB, workDirB, humanloopBin, epB)

	const questionA = "loom-routing-thread-a"
	appServerSmokeCallAskUser(t, ctx, rpc, threadA, questionA)
	appServerSmokeAssertPayload(t, payloads, "a", questionA)

	const questionB = "loom-routing-thread-b"
	appServerSmokeCallAskUser(t, ctx, rpc, threadB, questionB)
	appServerSmokeAssertPayload(t, payloads, "b", questionB)
}

type appServerSmokeRoutedPayload struct {
	label   string
	payload humanloop.Payload
}

func appServerSmokeRPC(t *testing.T, m *appServerManager) *appServerRPC {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rpc == nil {
		t.Fatal("app-server rpc is nil after ensure")
	}
	return m.rpc
}

func appServerSmokeThreadIDs(t *testing.T, ctx context.Context, codexBin string, workDirA string, workDirB string) (string, string) {
	t.Helper()
	threadA := os.Getenv("LOOM_CODEX_APPSERVER_ROUTING_THREAD_A")
	threadB := os.Getenv("LOOM_CODEX_APPSERVER_ROUTING_THREAD_B")
	if threadA != "" || threadB != "" {
		if threadA == "" || threadB == "" {
			t.Fatal("set both LOOM_CODEX_APPSERVER_ROUTING_THREAD_A and LOOM_CODEX_APPSERVER_ROUTING_THREAD_B, or neither")
		}
		return threadA, threadB
	}
	return appServerSmokeSeedThread(t, ctx, codexBin, workDirA), appServerSmokeSeedThread(t, ctx, codexBin, workDirB)
}

func appServerSmokeSeedThread(t *testing.T, ctx context.Context, codexBin string, cwd string) string {
	t.Helper()
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, codexBin,
		"exec",
		"--json",
		"--cd", cwd,
		"--dangerously-bypass-approvals-and-sandbox",
		"--skip-git-repo-check",
		"Codex app-server routing smoke seed. Reply exactly READY.",
	)
	cmd.Stdin = bytes.NewReader(nil)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("seed codex exec cwd=%s: %v\nstderr:\n%s", cwd, err, stderr.String())
	}

	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		var event struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
		}
		if err := json.Unmarshal(sc.Bytes(), &event); err != nil {
			continue
		}
		if event.Type == "thread.started" && event.ThreadID != "" {
			return event.ThreadID
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan codex exec seed output: %v", err)
	}
	t.Fatalf("codex exec seed cwd=%s did not emit thread.started; stdout:\n%s\nstderr:\n%s", cwd, string(out), stderr.String())
	return ""
}

func appServerSmokeHumanloopEndpoint(t *testing.T, label string) (*humanloop.IPCServer, humanloop.Endpoint) {
	t.Helper()
	dir := t.TempDir()
	srv, ep, err := humanloop.ListenIPC(dir)
	if err != nil {
		t.Fatalf("listen humanloop endpoint %s: %v", label, err)
	}
	return srv, ep
}

func appServerSmokeReceiveHumanloop(t *testing.T, srv *humanloop.IPCServer, label string, out chan<- appServerSmokeRoutedPayload) {
	t.Helper()
	go func() {
		for {
			err := srv.ReceiveAndAck(func(p humanloop.Payload) error {
				out <- appServerSmokeRoutedPayload{label: label, payload: p}
				return nil
			})
			if err != nil {
				return
			}
		}
	}()
}

func appServerSmokeResumeWithHumanloop(
	t *testing.T,
	ctx context.Context,
	m *appServerManager,
	threadID string,
	cwd string,
	humanloopBin string,
	ep humanloop.Endpoint,
) {
	t.Helper()
	_, err := m.resumeThread(ctx, appServerThreadResumeParams{
		ThreadID: threadID,
		CWD:      cwd,
		Config: appServerConfig{
			MCPServers: map[string]appServerMCPServer{
				"loom_humanloop": {
					Command: humanloopBin,
					Args: []string{
						"humanloop-mcp",
						humanloop.EndpointArg(ep),
						"5",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("thread/resume %s with humanloop endpoint %s: %v", threadID, ep.Address, err)
	}
}

func appServerSmokeCallAskUser(t *testing.T, ctx context.Context, rpc *appServerRPC, threadID string, question string) {
	t.Helper()
	var raw json.RawMessage
	err := rpc.call(ctx, "mcpServer/tool/call", map[string]any{
		"threadId": threadID,
		"server":   "loom_humanloop",
		"tool":     "ask_user",
		"arguments": map[string]any{
			"question": question,
		},
	}, &raw)
	if err != nil {
		t.Fatalf("mcpServer/tool/call thread=%s question=%s: %v", threadID, question, err)
	}
}

func appServerSmokeAssertPayload(t *testing.T, payloads <-chan appServerSmokeRoutedPayload, wantLabel string, wantQuestion string) {
	t.Helper()
	select {
	case got := <-payloads:
		if got.label != wantLabel || got.payload.Question != wantQuestion {
			t.Fatalf("humanloop payload routed to %s question=%q, want %s question=%q", got.label, got.payload.Question, wantLabel, wantQuestion)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for humanloop payload %s on route %s", wantQuestion, wantLabel)
	}
	select {
	case extra := <-payloads:
		t.Fatalf("unexpected extra humanloop payload routed to %s question=%q", extra.label, extra.payload.Question)
	default:
	}
}
