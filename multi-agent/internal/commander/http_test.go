package commander

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestHTTP_HealthzOK(t *testing.T) {
	srv := httptest.NewServer(NewHTTPHandler(&Handler{Backend: &fakeBackend{}}, LinkStatusFunc(func() bool { return true }), ""))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "linked: true") {
		t.Fatalf("body=%q", body)
	}
}

func TestHTTP_GetSessionsReturnsBackendResult(t *testing.T) {
	h := &Handler{Backend: &fakeBackend{
		listFn: func(_ context.Context) ([]agentbackend.Session, error) {
			return []agentbackend.Session{{ID: "s1"}, {ID: "s2"}}, nil
		},
	}}
	srv := httptest.NewServer(NewHTTPHandler(h, LinkStatusFunc(func() bool { return true }), ""))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var body struct {
		Sessions []agentbackend.Session `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Sessions) != 2 {
		t.Fatalf("got %d sessions", len(body.Sessions))
	}
}

func TestHTTP_GetSession404OnUnknown(t *testing.T) {
	h := &Handler{Backend: &fakeBackend{
		getFn: func(_ context.Context, _ string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{}, nil, agentbackend.ErrSessionNotFound
		},
	}}
	srv := httptest.NewServer(NewHTTPHandler(h, LinkStatusFunc(func() bool { return true }), ""))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/sessions/missing")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestHTTP_GetSessionOK(t *testing.T) {
	h := &Handler{Backend: &fakeBackend{
		getFn: func(_ context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{ID: id}, []agentbackend.SessionMessage{{Role: "user", Text: "hi"}}, nil
		},
	}}
	srv := httptest.NewServer(NewHTTPHandler(h, LinkStatusFunc(func() bool { return true }), ""))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/sessions/abc")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var body struct {
		Session  agentbackend.Session          `json:"session"`
		Messages []agentbackend.SessionMessage `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Session.ID != "abc" || len(body.Messages) != 1 {
		t.Fatalf("body=%+v", body)
	}
}

// TestHTTP_PostTurnStreamsSSE pins the SSE shape: one "event: chunk" per
// sink Write, then "event: done" with a result payload.
func TestHTTP_PostTurnStreamsSSE(t *testing.T) {
	h := &Handler{Backend: &fakeBackend{
		resumeFn: func(_ context.Context, id, prompt string, sink executor.Sink) (executor.Result, error) {
			if id != "abc" {
				t.Errorf("id=%q", id)
			}
			if prompt != "do thing" {
				t.Errorf("prompt=%q", prompt)
			}
			sink.Write("chunk", "alpha")
			sink.Write("chunk", "beta")
			sink.Close()
			return executor.Result{Summary: "done", SessionID: "abc"}, nil
		},
	}}
	srv := httptest.NewServer(NewHTTPHandler(h, LinkStatusFunc(func() bool { return true }), ""))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/sessions/abc/turn",
		strings.NewReader(`{"prompt":"do thing"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content-type=%q", got)
	}

	var lines []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n")
	if !strings.Contains(body, "event: chunk") {
		t.Errorf("missing chunk event:\n%s", body)
	}
	if !strings.Contains(body, "alpha") || !strings.Contains(body, "beta") {
		t.Errorf("missing data lines:\n%s", body)
	}
	if !strings.Contains(body, "event: done") {
		t.Errorf("missing done event:\n%s", body)
	}
	if !strings.Contains(body, `"result":`) || !strings.Contains(body, `"summary":"done"`) {
		t.Errorf("done event missing result body:\n%s", body)
	}
}

func TestHTTP_PostTurnErrSessionNotFound(t *testing.T) {
	h := &Handler{Backend: &fakeBackend{
		resumeFn: func(_ context.Context, _, _ string, _ executor.Sink) (executor.Result, error) {
			return executor.Result{}, agentbackend.ErrSessionNotFound
		},
	}}
	srv := httptest.NewServer(NewHTTPHandler(h, LinkStatusFunc(func() bool { return true }), ""))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/sessions/x/turn",
		strings.NewReader(`{"prompt":"p"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status=%d want 404", resp.StatusCode)
	}
}

func TestHTTP_RequiresBearerWhenConfigured(t *testing.T) {
	h := &Handler{Backend: &fakeBackend{
		listFn: func(_ context.Context) ([]agentbackend.Session, error) {
			return []agentbackend.Session{{ID: "s1"}}, nil
		},
	}}
	srv := httptest.NewServer(NewHTTPHandler(h, LinkStatusFunc(func() bool { return true }), "secret-token"))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/sessions")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without auth status=%d want 401", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/sessions", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("with auth status=%d want 200", resp.StatusCode)
	}
}
