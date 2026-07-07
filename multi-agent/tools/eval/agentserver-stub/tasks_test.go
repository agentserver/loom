package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// Helpers named `Bearer` to avoid colliding with existing postJSON(t, url, body any)
// in stub_test.go (fix plan review r1 P0-1).
func postJSONBearer(t *testing.T, url, bearer string, payload map[string]any) *http.Response {
	t.Helper()
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	return resp
}

func putJSONBearer(t *testing.T, url, bearer string, payload map[string]any) *http.Response {
	t.Helper()
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("PUT", url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("put %s: %v", url, err)
	}
	return resp
}

func getJSONBearer(t *testing.T, url, bearer string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	return resp
}

func TestCreateTask_MissingBearer_401(t *testing.T) {
	srv := newTestServer(t)
	resp := postJSONBearer(t, srv.URL+"/api/agent/tasks", "", map[string]any{"target_id": "x", "prompt": "y"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", resp.StatusCode)
	}
}

func TestCreateTask_MissingFields_400(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	resp := postJSONBearer(t, srv.URL+"/api/agent/tasks", caller.ProxyToken, map[string]any{"prompt": "y"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 got %d", resp.StatusCode)
	}
}

func TestCreateTask_UnknownTarget_404(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	resp := postJSONBearer(t, srv.URL+"/api/agent/tasks", caller.ProxyToken, map[string]any{
		"target_id": "sbx-never-registered", "prompt": "y",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404 got %d", resp.StatusCode)
	}
}

func TestCreateTask_CrossWorkspace_403(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "ws-A")
	target := registerAgent(t, srv, "slave", "slv-b", "ws-B")
	postCard(t, srv, target.ProxyToken, map[string]any{"display_name": "b", "card": map[string]any{}}).Body.Close()
	resp := postJSONBearer(t, srv.URL+"/api/agent/tasks", caller.ProxyToken, map[string]any{
		"target_id": target.SandboxID, "prompt": "y",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 got %d", resp.StatusCode)
	}
}

func TestCreateTask_ReturnsTaskIDStatusPending(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	target := registerAgent(t, srv, "slave", "slv-001", "")
	postCard(t, srv, target.ProxyToken, map[string]any{"display_name": "s", "card": map[string]any{}}).Body.Close()
	resp := postJSONBearer(t, srv.URL+"/api/agent/tasks", caller.ProxyToken, map[string]any{
		"target_id": target.SandboxID, "prompt": "hello",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 201 got %d body %s", resp.StatusCode, b)
	}
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	if out["task_id"] == nil || out["task_id"] == "" {
		t.Fatalf("missing task_id: %+v", out)
	}
	if out["status"] != "pending" {
		t.Fatalf("want status=pending got %v", out["status"])
	}
}

func TestPollTasks_MissingBearer_401(t *testing.T) {
	srv := newTestServer(t)
	resp := getJSONBearer(t, srv.URL+"/api/agent/tasks/poll", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", resp.StatusCode)
	}
}

func TestPollTasks_SandboxMismatch_403(t *testing.T) {
	srv := newTestServer(t)
	poller := registerAgent(t, srv, "slave", "slv-001", "")
	resp := getJSONBearer(t, srv.URL+"/api/agent/tasks/poll?sandbox_id=sbx-other", poller.ProxyToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 got %d", resp.StatusCode)
	}
}

func TestPollTasks_NoTasks_204(t *testing.T) {
	srv := newTestServer(t)
	poller := registerAgent(t, srv, "slave", "slv-001", "")
	resp := getJSONBearer(t, srv.URL+"/api/agent/tasks/poll", poller.ProxyToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204 got %d", resp.StatusCode)
	}
}

func TestPollTasks_NoQueryString_UsesCallerSandbox(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	poller := registerAgent(t, srv, "slave", "slv-001", "")
	postCard(t, srv, poller.ProxyToken, map[string]any{"display_name": "s", "card": map[string]any{}}).Body.Close()
	postJSONBearer(t, srv.URL+"/api/agent/tasks", caller.ProxyToken, map[string]any{
		"target_id": poller.SandboxID, "prompt": "hello",
	}).Body.Close()
	resp := getJSONBearer(t, srv.URL+"/api/agent/tasks/poll", poller.ProxyToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 got %d", resp.StatusCode)
	}
	var arr []map[string]any
	json.NewDecoder(resp.Body).Decode(&arr)
	if len(arr) != 1 || arr[0]["prompt"] != "hello" {
		t.Fatalf("want 1 task with prompt=hello got %+v", arr)
	}
}

func TestPollTasks_AtomicAssignBatch5(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	poller := registerAgent(t, srv, "slave", "slv-001", "")
	postCard(t, srv, poller.ProxyToken, map[string]any{"display_name": "s", "card": map[string]any{}}).Body.Close()
	for i := 0; i < 7; i++ {
		postJSONBearer(t, srv.URL+"/api/agent/tasks", caller.ProxyToken, map[string]any{
			"target_id": poller.SandboxID, "prompt": "p",
		}).Body.Close()
	}
	resp1 := getJSONBearer(t, srv.URL+"/api/agent/tasks/poll", poller.ProxyToken)
	var arr1 []map[string]any
	json.NewDecoder(resp1.Body).Decode(&arr1)
	resp1.Body.Close()
	if len(arr1) != 5 {
		t.Fatalf("want batch 5 got %d", len(arr1))
	}
	resp2 := getJSONBearer(t, srv.URL+"/api/agent/tasks/poll", poller.ProxyToken)
	var arr2 []map[string]any
	json.NewDecoder(resp2.Body).Decode(&arr2)
	resp2.Body.Close()
	if len(arr2) != 2 {
		t.Fatalf("want batch 2 got %d", len(arr2))
	}
	resp3 := getJSONBearer(t, srv.URL+"/api/agent/tasks/poll", poller.ProxyToken)
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204 got %d", resp3.StatusCode)
	}
}

func TestUpdateStatus_MarksRunningToCompleted(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	poller := registerAgent(t, srv, "slave", "slv-001", "")
	postCard(t, srv, poller.ProxyToken, map[string]any{"display_name": "s", "card": map[string]any{}}).Body.Close()
	postResp := postJSONBearer(t, srv.URL+"/api/agent/tasks", caller.ProxyToken, map[string]any{
		"target_id": poller.SandboxID, "prompt": "p",
	})
	var created map[string]any
	json.NewDecoder(postResp.Body).Decode(&created)
	postResp.Body.Close()
	tid := created["task_id"].(string)

	rr := putJSONBearer(t, srv.URL+"/api/agent/tasks/"+tid+"/status", poller.ProxyToken, map[string]any{"status": "running"})
	rr.Body.Close()
	if rr.StatusCode != http.StatusOK {
		t.Fatalf("running: want 200 got %d", rr.StatusCode)
	}
	rc := putJSONBearer(t, srv.URL+"/api/agent/tasks/"+tid+"/status", poller.ProxyToken, map[string]any{
		"status": "completed", "result": json.RawMessage(`"hello output"`),
	})
	rc.Body.Close()
	if rc.StatusCode != http.StatusOK {
		t.Fatalf("completed: want 200 got %d", rc.StatusCode)
	}
	g := getJSONBearer(t, srv.URL+"/api/agent/tasks/"+tid, caller.ProxyToken)
	defer g.Body.Close()
	var info map[string]any
	json.NewDecoder(g.Body).Decode(&info)
	if info["status"] != "completed" {
		t.Fatalf("get status: want completed got %v", info["status"])
	}
}

func TestUpdateStatus_AcceptsResultField(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	poller := registerAgent(t, srv, "slave", "slv-001", "")
	postCard(t, srv, poller.ProxyToken, map[string]any{"display_name": "s", "card": map[string]any{}}).Body.Close()
	postResp := postJSONBearer(t, srv.URL+"/api/agent/tasks", caller.ProxyToken, map[string]any{"target_id": poller.SandboxID, "prompt": "p"})
	var created map[string]any
	json.NewDecoder(postResp.Body).Decode(&created)
	postResp.Body.Close()
	tid := created["task_id"].(string)
	putJSONBearer(t, srv.URL+"/api/agent/tasks/"+tid+"/status", poller.ProxyToken, map[string]any{
		"status": "completed", "result": json.RawMessage(`{"echo":"hello"}`),
	}).Body.Close()
	g := getJSONBearer(t, srv.URL+"/api/agent/tasks/"+tid+"?include_output=true", caller.ProxyToken)
	defer g.Body.Close()
	var info map[string]any
	json.NewDecoder(g.Body).Decode(&info)
	rawResult, _ := json.Marshal(info["result"])
	if string(rawResult) != `{"echo":"hello"}` {
		t.Fatalf("result: want {echo:hello} got %s", rawResult)
	}
}

func TestUpdateStatus_AcceptsOutputField(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	poller := registerAgent(t, srv, "slave", "slv-001", "")
	postCard(t, srv, poller.ProxyToken, map[string]any{"display_name": "s", "card": map[string]any{}}).Body.Close()
	postResp := postJSONBearer(t, srv.URL+"/api/agent/tasks", caller.ProxyToken, map[string]any{"target_id": poller.SandboxID, "prompt": "p"})
	var created map[string]any
	json.NewDecoder(postResp.Body).Decode(&created)
	postResp.Body.Close()
	tid := created["task_id"].(string)
	putJSONBearer(t, srv.URL+"/api/agent/tasks/"+tid+"/status", poller.ProxyToken, map[string]any{
		"status": "completed", "output": "hello output",
	}).Body.Close()
	g := getJSONBearer(t, srv.URL+"/api/agent/tasks/"+tid+"?include_output=true", caller.ProxyToken)
	defer g.Body.Close()
	var info map[string]any
	json.NewDecoder(g.Body).Decode(&info)
	if info["output"] != "hello output" {
		t.Fatalf("output: want 'hello output' got %v", info["output"])
	}
}

func TestUpdateStatus_WrongOwner_403(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	poller := registerAgent(t, srv, "slave", "slv-001", "")
	other := registerAgent(t, srv, "slave", "slv-other", "")
	postCard(t, srv, poller.ProxyToken, map[string]any{"display_name": "s", "card": map[string]any{}}).Body.Close()
	postResp := postJSONBearer(t, srv.URL+"/api/agent/tasks", caller.ProxyToken, map[string]any{"target_id": poller.SandboxID, "prompt": "p"})
	var created map[string]any
	json.NewDecoder(postResp.Body).Decode(&created)
	postResp.Body.Close()
	tid := created["task_id"].(string)
	rr := putJSONBearer(t, srv.URL+"/api/agent/tasks/"+tid+"/status", other.ProxyToken, map[string]any{"status": "running"})
	rr.Body.Close()
	if rr.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 got %d", rr.StatusCode)
	}
}

func TestGetTask_ReturnsResultAfterComplete(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	poller := registerAgent(t, srv, "slave", "slv-001", "")
	postCard(t, srv, poller.ProxyToken, map[string]any{"display_name": "s", "card": map[string]any{}}).Body.Close()
	postResp := postJSONBearer(t, srv.URL+"/api/agent/tasks", caller.ProxyToken, map[string]any{"target_id": poller.SandboxID, "prompt": "p"})
	var created map[string]any
	json.NewDecoder(postResp.Body).Decode(&created)
	postResp.Body.Close()
	tid := created["task_id"].(string)
	putJSONBearer(t, srv.URL+"/api/agent/tasks/"+tid+"/status", poller.ProxyToken, map[string]any{"status": "completed", "output": "done"}).Body.Close()
	g := getJSONBearer(t, srv.URL+"/api/agent/tasks/"+tid, caller.ProxyToken)
	defer g.Body.Close()
	if g.StatusCode != http.StatusOK {
		t.Fatalf("want 200 got %d", g.StatusCode)
	}
	var info map[string]any
	json.NewDecoder(g.Body).Decode(&info)
	if info["status"] != "completed" {
		t.Fatalf("status: want completed got %v", info["status"])
	}
}

func TestGetTask_WrongWorkspace_404(t *testing.T) {
	srv := newTestServer(t)
	callerA := registerAgent(t, srv, "driver", "drv-A", "ws-A")
	callerB := registerAgent(t, srv, "driver", "drv-B", "ws-B")
	pollerA := registerAgent(t, srv, "slave", "slv-A", "ws-A")
	postCard(t, srv, pollerA.ProxyToken, map[string]any{"display_name": "sA", "card": map[string]any{}}).Body.Close()
	postResp := postJSONBearer(t, srv.URL+"/api/agent/tasks", callerA.ProxyToken, map[string]any{"target_id": pollerA.SandboxID, "prompt": "p"})
	var created map[string]any
	json.NewDecoder(postResp.Body).Decode(&created)
	postResp.Body.Close()
	tid := created["task_id"].(string)
	g := getJSONBearer(t, srv.URL+"/api/agent/tasks/"+tid, callerB.ProxyToken)
	g.Body.Close()
	if g.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404 got %d", g.StatusCode)
	}
}
