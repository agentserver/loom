//go:build evaltool

package faultinject

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// helper: build a Server bound to a free loopback port, return server + address.
func startTestServer(t *testing.T, audit io.Writer) (*Server, string) {
	t.Helper()
	cfg := Config{
		Listen: "127.0.0.1:0",
		Store:  NewStore(),
		Audit:  audit,
	}
	s, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = s.Shutdown(context.Background())
	})
	errCh := make(chan error, 1)
	go func() { errCh <- s.Serve(ctx) }()
	// Poll until Addr is published (Serve sets it once the listener is up).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if a := s.Addr(); a != "" {
			return s, a
		}
		select {
		case err := <-errCh:
			t.Fatalf("Serve returned early: %v", err)
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
	t.Fatalf("server did not publish Addr within 2s")
	return nil, ""
}

func post(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	buf, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

// F8: NewServer rejects any non-loopback bind address — literal 0.0.0.0,
// literal ::, an external IP, and a hostname that resolves to one.
func TestServer_RejectsNonLoopbackBind(t *testing.T) {
	cases := []string{
		"0.0.0.0:0",
		"[::]:0",
		"8.8.8.8:0",
		"1.1.1.1:18189",
		"0.0.0.0:18189",
	}
	for _, addr := range cases {
		cfg := Config{Listen: addr, Store: NewStore()}
		_, err := NewServer(cfg)
		if !errors.Is(err, ErrControlPlaneMustBeLoopback) {
			t.Errorf("NewServer(%q) err = %v, want ErrControlPlaneMustBeLoopback", addr, err)
		}
	}
}

// F9: NewServer accepts loopback literals and the localhost alias.
func TestServer_AcceptsLoopback(t *testing.T) {
	cases := []string{"127.0.0.1:0", "[::1]:0", "localhost:0"}
	for _, addr := range cases {
		cfg := Config{Listen: addr, Store: NewStore()}
		s, err := NewServer(cfg)
		if err != nil {
			t.Errorf("NewServer(%q) err = %v, want nil", addr, err)
			continue
		}
		// Spin up to confirm the listener actually binds, then tear down.
		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() { errCh <- s.Serve(ctx) }()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && s.Addr() == "" {
			time.Sleep(2 * time.Millisecond)
		}
		cancel()
		_ = s.Shutdown(context.Background())
	}
}

// Extra: empty listen address falls back to the documented default.
func TestServer_DefaultListenIsLoopback(t *testing.T) {
	cfg := Config{Listen: "", Store: NewStore()}
	s, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer empty Listen: %v", err)
	}
	if got := s.ListenAddr(); !strings.HasPrefix(got, "127.0.0.1:") {
		t.Errorf("default Listen = %q, want 127.0.0.1:*", got)
	}
}

// F10: /inject → /list → /clear → /list round-trip.
func TestServer_InjectClearListRoundTrip(t *testing.T) {
	var audit bytes.Buffer
	s, addr := startTestServer(t, &audit)
	_ = s
	const runID = "run-roundtrip"

	resp := post(t, "http://"+addr+"/inject", map[string]any{
		"run_id": runID, "kind": "missing_file", "target": "foo.txt",
	})
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("/inject status = %d, body=%s", resp.StatusCode, body)
	}
	resp.Body.Close()

	listResp, err := http.Get("http://" + addr + "/list?run_id=" + runID)
	if err != nil {
		t.Fatalf("/list: %v", err)
	}
	defer listResp.Body.Close()
	var got []FaultDirective
	if err := json.NewDecoder(listResp.Body).Decode(&got); err != nil {
		t.Fatalf("/list decode: %v", err)
	}
	if len(got) != 1 || got[0].Kind != FaultMissingFile || got[0].Target != "foo.txt" {
		t.Fatalf("/list = %+v; want one missing_file directive for foo.txt", got)
	}

	clrResp := post(t, "http://"+addr+"/clear", map[string]string{"run_id": runID})
	if clrResp.StatusCode != 200 {
		body, _ := io.ReadAll(clrResp.Body)
		t.Fatalf("/clear status = %d, body=%s", clrResp.StatusCode, body)
	}
	clrResp.Body.Close()

	list2, err := http.Get("http://" + addr + "/list?run_id=" + runID)
	if err != nil {
		t.Fatalf("/list-after: %v", err)
	}
	defer list2.Body.Close()
	var after []FaultDirective
	if err := json.NewDecoder(list2.Body).Decode(&after); err != nil {
		t.Fatalf("/list-after decode: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("/list after clear = %+v; want empty", after)
	}
}

// F11: /inject rejects bad run_id values.
func TestServer_InjectRejectsBadRunID(t *testing.T) {
	s, addr := startTestServer(t, io.Discard)
	_ = s
	bads := []string{"", "abc", strings.Repeat("a", 129), "with/slash", "has space", "has.dot"}
	for _, r := range bads {
		resp := post(t, "http://"+addr+"/inject", map[string]any{
			"run_id": r, "kind": "missing_file",
		})
		if resp.StatusCode != 400 {
			t.Errorf("/inject run_id=%q status = %d, want 400", r, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// F12: /inject rejects unknown kinds.
func TestServer_InjectRejectsUnknownKind(t *testing.T) {
	s, addr := startTestServer(t, io.Discard)
	_ = s
	resp := post(t, "http://"+addr+"/inject", map[string]any{
		"run_id": "run-unknown", "kind": "made_up",
	})
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "kind unknown") {
		t.Errorf("body = %s; want substring 'kind unknown'", body)
	}
}

// F13: rate limit — 100 same-(run, kind) → 200; 101st → 400.
func TestServer_InjectRateLimit_101st_400(t *testing.T) {
	s, addr := startTestServer(t, io.Discard)
	_ = s
	const runID = "run-rl-100"
	for i := 0; i < MaxInjectionsPerRun; i++ {
		resp := post(t, "http://"+addr+"/inject", map[string]any{
			"run_id": runID, "kind": "missing_file",
		})
		if resp.StatusCode != 200 {
			t.Fatalf("inject %d: status = %d, want 200", i, resp.StatusCode)
		}
		resp.Body.Close()
	}
	resp := post(t, "http://"+addr+"/inject", map[string]any{
		"run_id": runID, "kind": "missing_file",
	})
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("101st inject status = %d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "rate limit") {
		t.Errorf("101st inject body = %s; want substring 'rate limit'", body)
	}
}

// F14: HTTP server timeouts are explicitly set.
func TestServer_HTTPTimeoutsSet(t *testing.T) {
	cfg := Config{Listen: "127.0.0.1:0", Store: NewStore()}
	s, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	got := s.HTTPServer()
	if got.ReadHeaderTimeout != 5*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 5s", got.ReadHeaderTimeout)
	}
	if got.ReadTimeout != 30*time.Second {
		t.Errorf("ReadTimeout = %v, want 30s", got.ReadTimeout)
	}
	if got.WriteTimeout != 30*time.Second {
		t.Errorf("WriteTimeout = %v, want 30s", got.WriteTimeout)
	}
	if got.IdleTimeout != 60*time.Second {
		t.Errorf("IdleTimeout = %v, want 60s", got.IdleTimeout)
	}
}

// F15: every /inject call emits exactly one audit line; 5 calls → 5 lines.
func TestServer_AuditLogEveryInjection(t *testing.T) {
	var audit bytes.Buffer
	s, addr := startTestServer(t, &audit)
	_ = s
	const runID = "run-audit5"
	for i := 0; i < 5; i++ {
		resp := post(t, "http://"+addr+"/inject", map[string]any{
			"run_id": runID, "kind": "missing_file", "target": fmt.Sprintf("f%d", i),
		})
		if resp.StatusCode != 200 {
			t.Fatalf("inject %d: %d", i, resp.StatusCode)
		}
		resp.Body.Close()
	}
	// Audit on inject is the registration audit (action="registered"
	// per server impl); the per-fire audit happens at hook bridge.
	// For this test we only need to verify that registrations are not
	// silent — every inject yields one audit line.
	lines := bytes.Split(bytes.TrimRight(audit.Bytes(), "\n"), []byte{'\n'})
	if len(lines) != 5 {
		t.Fatalf("audit line count = %d, want 5; raw=%q", len(lines), audit.String())
	}
	for i, line := range lines {
		var rec AuditRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("line %d unmarshal: %v; raw=%q", i, err, line)
		}
		if rec.RunID != runID || rec.Kind != FaultMissingFile {
			t.Errorf("line %d: rec = %+v; want run_id=%s kind=missing_file", i, rec, runID)
		}
		if rec.Action != "registered" {
			t.Errorf("line %d action = %q, want 'registered'", i, rec.Action)
		}
	}
}

// F17: target > 512 bytes → 400.
func TestServer_InjectRejectsOversizedTarget(t *testing.T) {
	s, addr := startTestServer(t, io.Discard)
	_ = s
	resp := post(t, "http://"+addr+"/inject", map[string]any{
		"run_id": "run-bigtarget", "kind": "missing_file", "target": strings.Repeat("x", 513),
	})
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "target too long") {
		t.Errorf("body = %s", body)
	}
}

// F18: >16 params, or a value > 1024 bytes, → 400.
func TestServer_InjectRejectsOversizedParams(t *testing.T) {
	s, addr := startTestServer(t, io.Discard)
	_ = s
	// 17 entries.
	tooMany := make(map[string]string, 17)
	for i := 0; i < 17; i++ {
		tooMany[fmt.Sprintf("k%d", i)] = "v"
	}
	resp := post(t, "http://"+addr+"/inject", map[string]any{
		"run_id": "run-bigparams1", "kind": "missing_file", "params": tooMany,
	})
	if resp.StatusCode != 400 {
		t.Errorf("17-entry params: status = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
	// One oversized value.
	resp = post(t, "http://"+addr+"/inject", map[string]any{
		"run_id": "run-bigparams2", "kind": "missing_file", "params": map[string]string{"k": strings.Repeat("x", 1025)},
	})
	if resp.StatusCode != 400 {
		t.Errorf("oversized value: status = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
}

// F22: every file in this package starts with the //go:build evaltool tag.
func TestPackage_AllFilesBuildTagged(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var checked int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		path := filepath.Join(".", name)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("ReadFile %s: %v", path, err)
			continue
		}
		// First non-empty line MUST be //go:build evaltool.
		firstLine := ""
		for _, line := range strings.SplitN(string(b), "\n", 3) {
			if strings.TrimSpace(line) != "" {
				firstLine = line
				break
			}
		}
		if strings.TrimSpace(firstLine) != "//go:build evaltool" {
			t.Errorf("%s: first non-empty line = %q; want '//go:build evaltool'", name, firstLine)
		}
		checked++
	}
	if checked == 0 {
		t.Fatalf("no .go files found in package directory")
	}
}

// Extra: /clear of unknown well-formed run_id is a no-op 200.
func TestServer_ClearUnknownRunID_NoOp(t *testing.T) {
	s, addr := startTestServer(t, io.Discard)
	_ = s
	resp := post(t, "http://"+addr+"/clear", map[string]string{"run_id": "run-unknownXX"})
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// Extra: malformed JSON body → 400, never crashes.
func TestServer_InjectMalformedJSON(t *testing.T) {
	s, addr := startTestServer(t, io.Discard)
	_ = s
	resp, err := http.Post("http://"+addr+"/inject", "application/json", strings.NewReader("{not json"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// Extra: /list with malformed run_id → 400.
func TestServer_ListRejectsBadRunID(t *testing.T) {
	s, addr := startTestServer(t, io.Discard)
	_ = s
	resp, err := http.Get("http://" + addr + "/list?run_id=bad/slash")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// Extra: GET on /inject (wrong method) → 405.
func TestServer_MethodNotAllowed(t *testing.T) {
	s, addr := startTestServer(t, io.Discard)
	_ = s
	resp, err := http.Get("http://" + addr + "/inject")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

// Extra: ensure the bound port is actually loopback at the socket level.
func TestServer_BoundSocketIsLoopback(t *testing.T) {
	s, addr := startTestServer(t, io.Discard)
	_ = s
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", addr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		t.Errorf("bound host = %q (ip=%v); want loopback", host, ip)
	}
}

// Extra: /clear rejects unknown JSON fields (parity with /inject).
func TestServer_ClearRejectsUnknownFields(t *testing.T) {
	s, addr := startTestServer(t, io.Discard)
	_ = s
	resp, err := http.Post("http://"+addr+"/clear", "application/json",
		strings.NewReader(`{"run_id":"run-clrunk01","stray":"key"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400 for unknown field", resp.StatusCode)
	}
}

// Extra: /list with multiple run_id query params → 400 (ambiguity, never
// silently pick the first).
func TestServer_ListRejectsMultipleRunIDParams(t *testing.T) {
	s, addr := startTestServer(t, io.Discard)
	_ = s
	resp, err := http.Get("http://" + addr + "/list?run_id=run-aaaa1111&run_id=run-bbbb2222")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400 for repeated run_id", resp.StatusCode)
	}
}

// Extra: concurrent /inject from many goroutines stays under rate limit if N<=100.
func TestServer_ConcurrentInject(t *testing.T) {
	s, addr := startTestServer(t, io.Discard)
	_ = s
	const runID = "run-concserver"
	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			resp := post(t, "http://"+addr+"/inject", map[string]any{
				"run_id": runID, "kind": "missing_file",
			})
			resp.Body.Close()
		}()
	}
	wg.Wait()
	resp, err := http.Get("http://" + addr + "/list?run_id=" + runID)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var got []FaultDirective
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if len(got) != N {
		t.Errorf("post-concurrent list len = %d, want %d", len(got), N)
	}
}
