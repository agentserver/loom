package observerweb

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/commandiface"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/observerstore"
)

// buildValidSnapshot returns a canonical Snapshot suitable for happy-path tests.
func buildValidSnapshot(t *testing.T) capability.Snapshot {
	t.Helper()
	snap, err := capability.NewSnapshot(capability.Snapshot{
		OS:                "linux",
		Arch:              "amd64",
		Platform:          commandiface.Platform{OS: "linux", Arch: "amd64"},
		CommandInterfaces: []commandiface.CommandInterface{{Skill: "bash", Kind: "bash", Command: "/bin/bash", Default: true}},
		Network:           capability.NetworkLoopbackOnly,
	})
	if err != nil {
		t.Fatalf("NewSnapshot: %v", err)
	}
	return snap
}

func postCapabilitySnapshot(t *testing.T, srv *httptest.Server, token string, snapBody json.RawMessage) *http.Response {
	t.Helper()
	payload, _ := json.Marshal(struct {
		Snapshot json.RawMessage `json:"snapshot"`
	}{Snapshot: snapBody})
	req, _ := http.NewRequest("POST", srv.URL+"/api/capability-snapshots", bytes.NewReader(payload))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	return resp
}

// newHandlerServer wraps the existing newTestHandler(t) in an httptest.Server
// so tests can use full-URL client calls.
func newHandlerServer(t *testing.T) (*httptest.Server, *observerstore.SQLiteStore) {
	t.Helper()
	h, store := newTestHandler(t)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, store
}

// registerDriver seeds an observerstore workspace + Agent with role=driver.
// Mirrors server_test.go:104-108 3-step (UpsertWorkspaceLazy + UpsertAPIKey + UpsertAgent).
func registerDriver(t *testing.T, store *observerstore.SQLiteStore, agentID, wsID string) (observerstore.Agent, string) {
	t.Helper()
	require.NoError(t, store.UpsertWorkspaceLazy(wsID, "Test-"+wsID, "ak-"+wsID))
	require.NoError(t, store.UpsertAPIKey(observerstore.APIKeySpec{ID: "ak-" + wsID, Key: "seed-" + wsID}))
	token := "test-bearer-" + agentID
	ag := observerstore.Agent{
		ID:          agentID,
		WorkspaceID: wsID,
		Role:        observer.RoleDriver,
		DisplayName: agentID,
	}
	require.NoError(t, store.UpsertAgent(ag, token, "ak-"+wsID))
	return ag, token
}

func countCapabilitySnapshots(t *testing.T, store *observerstore.SQLiteStore) int {
	t.Helper()
	var n int
	if err := store.DB().QueryRow(`SELECT count(*) FROM capability_snapshots`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func countUsagesForAgent(t *testing.T, store *observerstore.SQLiteStore, agentID string) int {
	t.Helper()
	var n int
	if err := store.DB().QueryRow(`SELECT count(*) FROM capability_snapshot_usages WHERE agent_id=?`, agentID).Scan(&n); err != nil {
		t.Fatalf("count usages: %v", err)
	}
	return n
}

// countUsagesForWorkspace supports detecting workspace-attribution forgery
// (fix plan review r4 P1).
func countUsagesForWorkspace(t *testing.T, store *observerstore.SQLiteStore, wsID string) int {
	t.Helper()
	var n int
	if err := store.DB().QueryRow(`SELECT count(*) FROM capability_snapshot_usages WHERE workspace_id=?`, wsID).Scan(&n); err != nil {
		t.Fatalf("count ws usages: %v", err)
	}
	return n
}

// --- tests ---------------------------------------------------------

func TestCapabilitySnapshots_HappyPath(t *testing.T) {
	srv, store := newHandlerServer(t)
	_, token := registerDriver(t, store, "drv-1", "ws-A")
	snap := buildValidSnapshot(t)
	body, _ := json.Marshal(snap)
	resp := postCapabilitySnapshot(t, srv, token, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 204 got %d body=%s", resp.StatusCode, b)
	}
	if got := countCapabilitySnapshots(t, store); got != 1 {
		t.Fatalf("capability_snapshots: want 1 got %d", got)
	}
	if got := countUsagesForAgent(t, store, "drv-1"); got != 1 {
		t.Fatalf("capability_snapshot_usages for drv-1: want 1 got %d", got)
	}
}

func TestCapabilitySnapshots_SecretScanRejected_422(t *testing.T) {
	srv, store := newHandlerServer(t)
	_, token := registerDriver(t, store, "drv-1", "ws-A")
	// Shape-valid canonical snapshot with a raw-token-shaped string in a
	// free-form Tool.Version — NewSnapshot accepts it (Version is free-form
	// per spec §3.1); WriteSnapshot's secret-scan is what rejects it.
	snap, err := capability.NewSnapshot(capability.Snapshot{
		OS:       "linux",
		Arch:     "amd64",
		Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
		Network:  capability.NetworkLoopbackOnly,
		Tools: []capability.ToolVersion{
			{Name: "ghtok", Version: "ghp_ABCDEFGHIJKLMNOPQRST1234567890"},
		},
	})
	if err != nil {
		t.Fatalf("NewSnapshot: %v (Tool.Version was expected to be shape-valid)", err)
	}
	body, _ := json.Marshal(snap)
	resp := postCapabilitySnapshot(t, srv, token, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 422 got %d body=%s", resp.StatusCode, b)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "snapshot contains raw token; rejected by secret scan") {
		t.Fatalf("want fixed text; got %q", b)
	}
	if got := countCapabilitySnapshots(t, store); got != 0 {
		t.Fatalf("capability_snapshots must be empty on scan reject; got %d", got)
	}
}

func TestCapabilitySnapshots_TooLarge_413(t *testing.T) {
	srv, store := newHandlerServer(t)
	_, token := registerDriver(t, store, "drv-1", "ws-A")
	// > 256 KiB body — must be VALID JSON so MaxBytesReader trips before json.Decoder.
	// Build a Snapshot with a huge padding baked into a valid JSON string so the
	// wire body itself is > 256 KiB.
	padding := strings.Repeat("A", 300*1024)
	snapJSON := `{"os":"linux","arch":"amd64","platform":{"os":"linux","arch":"amd64"},"network":"loopback-only","tools":[{"name":"pad","version":"` + padding + `"}]}`
	payload := []byte(`{"snapshot":` + snapJSON + `}`)
	req, _ := http.NewRequest("POST", srv.URL+"/api/capability-snapshots", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 413 got %d body=%s", resp.StatusCode, b)
	}
}

func TestCapabilitySnapshots_AuthNotDriver_403(t *testing.T) {
	srv, store := newHandlerServer(t)
	require.NoError(t, store.UpsertWorkspaceLazy("ws-A", "Test-ws-A", "ak-ws-A"))
	require.NoError(t, store.UpsertAPIKey(observerstore.APIKeySpec{ID: "ak-ws-A", Key: "seed-ws-A"}))
	slaveToken := "test-bearer-slv-1"
	require.NoError(t, store.UpsertAgent(
		observerstore.Agent{ID: "slv-1", WorkspaceID: "ws-A", Role: observer.RoleSlave, DisplayName: "slv-1"},
		slaveToken, "ak-ws-A",
	))
	snap := buildValidSnapshot(t)
	body, _ := json.Marshal(snap)
	resp := postCapabilitySnapshot(t, srv, slaveToken, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 got %d", resp.StatusCode)
	}
}

func TestCapabilitySnapshots_MethodNotPost_405(t *testing.T) {
	srv, store := newHandlerServer(t)
	_, token := registerDriver(t, store, "drv-1", "ws-A")
	req, _ := http.NewRequest("GET", srv.URL+"/api/capability-snapshots", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("want 405 got %d", resp.StatusCode)
	}
}

func TestCapabilitySnapshots_AttributionFromAuth_NotBody(t *testing.T) {
	// The handler MUST derive attribution from agent.ID + agent.WorkspaceID
	// (h.authenticate) — NOT any body-level fields. This test EMBEDS a forged
	// agent_id and workspace_id in the body and asserts they are ignored.
	srv, store := newHandlerServer(t)
	_, tok1 := registerDriver(t, store, "drv-alpha", "ws-A")
	_, tok2 := registerDriver(t, store, "drv-beta", "ws-A")
	snap := buildValidSnapshot(t)
	snapBytes, _ := json.Marshal(snap)

	// Alpha with forged top-level agent_id + workspace_id.
	forgedAlpha := []byte(`{"agent_id":"drv-VICTIM","workspace_id":"ws-VICTIM","snapshot":` + string(snapBytes) + `}`)
	reqA, _ := http.NewRequest("POST", srv.URL+"/api/capability-snapshots", bytes.NewReader(forgedAlpha))
	reqA.Header.Set("Authorization", "Bearer "+tok1)
	reqA.Header.Set("Content-Type", "application/json")
	respA, err := http.DefaultClient.Do(reqA)
	if err != nil {
		t.Fatalf("do alpha: %v", err)
	}
	respA.Body.Close()

	// Beta writes same snapshot (no forgery) — verifies dedup.
	postCapabilitySnapshot(t, srv, tok2, snapBytes).Body.Close()

	// capability_snapshots dedup by hash → 1 row (same snap body)
	if got := countCapabilitySnapshots(t, store); got != 1 {
		t.Fatalf("capability_snapshots: want 1 (dedup) got %d", got)
	}
	// Real auth ids attributed
	if got := countUsagesForAgent(t, store, "drv-alpha"); got != 1 {
		t.Fatalf("usages for alpha (auth id): want 1 got %d", got)
	}
	if got := countUsagesForAgent(t, store, "drv-beta"); got != 1 {
		t.Fatalf("usages for beta (auth id): want 1 got %d", got)
	}
	// Forged agent_id MUST NOT persist
	if got := countUsagesForAgent(t, store, "drv-VICTIM"); got != 0 {
		t.Fatalf("attribution forgery (agent): want 0 usages for forged agent_id, got %d", got)
	}
	// Forged workspace_id MUST NOT persist either (fix plan review r4 P1)
	if got := countUsagesForWorkspace(t, store, "ws-VICTIM"); got != 0 {
		t.Fatalf("attribution forgery (workspace): want 0 usages for forged workspace_id, got %d", got)
	}
	// Real workspace has both rows
	if got := countUsagesForWorkspace(t, store, "ws-A"); got != 2 {
		t.Fatalf("expected 2 usages under real workspace ws-A; got %d", got)
	}
}

func TestCapabilitySnapshots_NoCapabilityDiscoveryAblation_ObserverSideDefenseInDepth(t *testing.T) {
	// Even if the driver side forgets its own IsUploadDisabled() check,
	// observer-side WriteSnapshot must still short-circuit under the ablation.
	srv, store := newHandlerServer(t)
	_, token := registerDriver(t, store, "drv-1", "ws-A")
	capability.SetDisableUpload(true)
	t.Cleanup(func() { capability.SetDisableUpload(false) })
	snap := buildValidSnapshot(t)
	body, _ := json.Marshal(snap)
	resp := postCapabilitySnapshot(t, srv, token, body)
	defer resp.Body.Close()
	// WriteSnapshot short-circuits silently → observer returns 204
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204 (silent short-circuit) got %d", resp.StatusCode)
	}
	if got := countCapabilitySnapshots(t, store); got != 0 {
		t.Fatalf("capability_snapshots under NoCapabilityDiscovery: want 0 got %d", got)
	}
}
