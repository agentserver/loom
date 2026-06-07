package scriptstest

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestObserverAgentserverIdentityE2E(t *testing.T) {
	root := repoRoot(t)
	tmp := t.TempDir()
	stubAddr := freeAddr(t)
	observerAddr := freeAddr(t)
	t.Setenv("OBSERVER_E2E_TELEMETRY_KEY", "ops-e2e-secret")

	stubConfig := filepath.Join(tmp, "whoami.json")
	writeJSONFile(t, stubConfig, map[string]any{
		"tokens": map[string]any{
			"proxy-driver-a": map[string]string{
				"user_id": "user-a", "workspace_id": "ws-a", "workspace_name": "A",
				"sandbox_id": "sandbox-a", "short_id": "driver-a", "role": "driver",
			},
			"proxy-driver-b": map[string]string{
				"user_id": "user-b", "workspace_id": "ws-b", "workspace_name": "B",
				"sandbox_id": "sandbox-b", "short_id": "driver-b", "role": "driver",
			},
		},
		"revoked_tokens": []string{"revoked-token"},
	})

	stub := startCommand(t, root, "go", "run", "./cmd/whoami-stub", "--listen", stubAddr, "--config", stubConfig)
	t.Cleanup(func() { stopCommand(stub) })
	waitHTTPStatus(t, "http://"+stubAddr+"/api/agent/whoami", "missing", http.StatusUnauthorized)

	observerConfig := filepath.Join(tmp, "observer.yaml")
	requireWriteFile(t, observerConfig, []byte(`listen_addr: "`+observerAddr+`"
db_path: "`+filepath.Join(tmp, "observer.db")+`"
identity:
  legacy_api_keys:
    enabled: true
  agentserver:
    enabled: true
    url: "http://`+stubAddr+`"
    fresh_ttl: 1s
    stale_grace: 1s
    request_timeout: 200ms
    cache_capacity: 128
    startup_probe: false
api_keys:
  - id: ak-e2e
    key: legacy-api-key
telemetry:
  enabled: true
  api_keys:
    - id: ops-e2e
      key_env: OBSERVER_E2E_TELEMETRY_KEY
      workspace_id: "*"
`))

	observer := startCommand(t, root, "go", "run", "./cmd/observer-server", "--config", observerConfig)
	t.Cleanup(func() { stopCommand(observer) })
	waitHTTPStatus(t, "http://"+observerAddr+"/api/events", "", http.StatusMethodNotAllowed)

	requireStatus(t, http.MethodGet, "http://"+observerAddr+"/", "", nil, http.StatusNotFound)

	eventA := map[string]any{
		"workspace_id": "ws-a", "agent_id": "driver-a", "agent_role": "driver",
		"type": "driver_task_submitted", "task_id": "task-a", "summary": "hello", "status": "assigned",
	}
	requireEventStatus(t, http.MethodPost, "http://"+observerAddr+"/api/events", "proxy-driver-a", eventA, http.StatusAccepted)

	requireStatus(t, http.MethodGet, "http://"+observerAddr+"/api/tasks/task-a/progress", "proxy-driver-b", nil, http.StatusNotFound)

	requireEventStatus(t, http.MethodPost, "http://"+observerAddr+"/api/events", "revoked-token", eventA, http.StatusForbidden)

	stopCommand(stub)
	resp := requireEventStatus(t, http.MethodPost, "http://"+observerAddr+"/api/events", "uncached-token", eventA, http.StatusServiceUnavailable)
	requireEqual(t, "5", resp.Header.Get("Retry-After"))

	legacyToken := registerLegacyAgent(t, observerAddr)
	legacyEvent := map[string]any{
		"workspace_id": "ws-legacy", "agent_id": "legacy-driver", "agent_role": "driver",
		"type": "driver_task_submitted", "task_id": "legacy-task", "summary": "legacy", "status": "assigned",
	}
	requireEventStatus(t, http.MethodPost, "http://"+observerAddr+"/api/events", legacyToken, legacyEvent, http.StatusAccepted)
}

func registerLegacyAgent(t *testing.T, observerAddr string) string {
	t.Helper()
	body := map[string]any{
		"workspace_id": "ws-legacy", "workspace_name": "Legacy",
		"agent_id": "legacy-driver", "role": "driver", "display_name": "Legacy Driver",
	}
	resp := requireStatus(t, http.MethodPost, "http://"+observerAddr+"/api/agents/register", "legacy-api-key", body, http.StatusOK)
	defer resp.Body.Close()
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	if out.Token == "" {
		t.Fatal("register response token is empty")
	}
	return out.Token
}

func requireStatus(t *testing.T, method, url, bearer string, body any, want int) *http.Response {
	return requireStatusWithHeaders(t, method, url, bearer, body, nil, want)
}

func requireEventStatus(t *testing.T, method, url, bearer string, body any, want int) *http.Response {
	return requireStatusWithHeaders(t, method, url, bearer, body, map[string]string{
		"X-Loom-Telemetry-Key": "ops-e2e-secret",
	}, want)
}

func requireStatusWithHeaders(t *testing.T, method, url, bearer string, body any, headers map[string]string, want int) *http.Response {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	if resp.StatusCode != want {
		defer resp.Body.Close()
		t.Fatalf("%s %s got status %d want %d", method, url, resp.StatusCode, want)
	}
	return resp
}

func waitHTTPStatus(t *testing.T, url, bearer string, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == want {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s to return %d", url, want)
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func startCommand(t *testing.T, dir string, name string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s %v: %v", name, args, err)
	}
	return cmd
}

func stopCommand(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_, _ = cmd.Process.Wait()
}

func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	requireWriteFile(t, path, data)
}

func requireWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func requireEqual[T comparable](t *testing.T, want, got T) {
	t.Helper()
	if got != want {
		t.Fatalf("got %v want %v", got, want)
	}
}
