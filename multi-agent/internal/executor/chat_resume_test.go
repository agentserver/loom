package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeResumeBackend struct{ calls int }

func (f *fakeResumeBackend) Run(ctx context.Context, t Task, s Sink) (Result, error) {
	return Result{}, nil
}
func (f *fakeResumeBackend) RunResume(ctx context.Context, sid, ans string, s Sink) (Result, error) {
	f.calls++
	return Result{Summary: "ok: " + ans, SessionID: sid}, nil
}

func TestChatResumeHappyPath(t *testing.T) {
	be := &fakeResumeBackend{}
	ex := NewChatResume(ChatResumeConfig{Backend: be, FlockDir: t.TempDir()})
	res, err := ex.Run(context.Background(),
		Task{Prompt: `{"session_id":"S","answer":"yes","kind":"ask_user"}`},
		nullSink{})
	if err != nil {
		t.Fatal(err)
	}
	if be.calls != 1 || res.Summary != "ok: yes" {
		t.Errorf("unexpected: calls=%d res=%+v", be.calls, res)
	}
}

func TestChatResumeRejectsConcurrent(t *testing.T) {
	flockDir := t.TempDir()
	ex := NewChatResume(ChatResumeConfig{Backend: &slowBackend{}, FlockDir: flockDir})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	body := `{"session_id":"S-shared","answer":"x","kind":"ask_user"}`
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := ex.Run(context.Background(), Task{Prompt: body}, nullSink{})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	var busy int
	for err := range errs {
		if err != nil && strings.Contains(err.Error(), "session busy") {
			busy++
		}
	}
	if busy != 1 {
		t.Errorf("expected exactly 1 'session busy' error, got %d", busy)
	}
	if _, err := os.Stat(filepath.Join(flockDir, "S-shared.lock")); err != nil {
		t.Errorf("lock file should exist: %v", err)
	}
}

func TestChatResumeRejectsBadPrompt(t *testing.T) {
	ex := NewChatResume(ChatResumeConfig{Backend: &fakeResumeBackend{}, FlockDir: t.TempDir()})
	cases := []struct{ prompt, want string }{
		{`not json`, "bad prompt"},
		{`{"session_id":"","answer":"x"}`, "required"},
		{`{"session_id":"S","answer":""}`, "required"},
	}
	for _, c := range cases {
		_, err := ex.Run(context.Background(), Task{Prompt: c.prompt}, nullSink{})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("prompt=%q expected error containing %q, got %v", c.prompt, c.want, err)
		}
	}
}

type slowBackend struct{}

func (slowBackend) Run(ctx context.Context, t Task, _ Sink) (Result, error) { return Result{}, nil }
func (slowBackend) RunResume(ctx context.Context, _, _ string, _ Sink) (Result, error) {
	time.Sleep(200 * time.Millisecond)
	return Result{}, nil
}

type nullSink struct{}

func (nullSink) Write(string, string) {}
func (nullSink) Close()                {}
