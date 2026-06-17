package codex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

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
