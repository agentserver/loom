package codex

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestCodexAppServerSmoke(t *testing.T) {
	if os.Getenv("LOOM_CODEX_APPSERVER_SMOKE") != "1" {
		t.Skip("set LOOM_CODEX_APPSERVER_SMOKE=1 to run against local codex app-server")
	}

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

	wb, ok := b.(agentbackend.SessionWorkerBackend)
	if !ok {
		t.Fatalf("app_server codex backend does not implement SessionWorkerBackend")
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
