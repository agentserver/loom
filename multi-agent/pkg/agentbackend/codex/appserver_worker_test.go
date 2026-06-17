package codex

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestCodexFactoryDefaultDoesNotExposeSessionWorkerBackend(t *testing.T) {
	b, err := agentbackend.New(agentbackend.Config{
		Kind:    agentbackend.KindCodex,
		Bin:     "codex",
		WorkDir: t.TempDir(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b.(agentbackend.SessionWorkerBackend); ok {
		t.Fatalf("default codex backend implements SessionWorkerBackend; want plain backend")
	}
}

func TestCodexFactoryAppServerModeExposesSessionWorkerBackend(t *testing.T) {
	b, err := agentbackend.New(agentbackend.Config{
		Kind:       agentbackend.KindCodex,
		Bin:        "codex",
		WorkDir:    t.TempDir(),
		WorkerMode: "app_server",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b.(agentbackend.SessionWorkerBackend); !ok {
		t.Fatalf("app_server codex backend does not implement SessionWorkerBackend")
	}
}

func TestCodexWorkerBackendNewSessionWorkerUnavailableWhenManagerUnavailable(t *testing.T) {
	b, err := agentbackend.New(agentbackend.Config{
		Kind:       agentbackend.KindCodex,
		Bin:        filepath.Join(t.TempDir(), "missing-codex"),
		WorkDir:    t.TempDir(),
		WorkerMode: "app_server",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	workerBackend, ok := b.(agentbackend.SessionWorkerBackend)
	if !ok {
		t.Fatalf("app_server codex backend does not implement SessionWorkerBackend")
	}

	worker, err := workerBackend.NewSessionWorker(context.Background(), agentbackend.Session{
		ID:         "thread-1",
		Kind:       agentbackend.KindCodex,
		WorkingDir: t.TempDir(),
	})
	if !errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
		t.Fatalf("NewSessionWorker error = %v, want ErrSessionWorkerUnavailable", err)
	}
	if worker != nil {
		t.Fatalf("NewSessionWorker returned worker %#v, want nil", worker)
	}
}

func TestCodexWorkerBackendNewSessionWorkerUnavailableWhenSessionIDEmpty(t *testing.T) {
	b, err := agentbackend.New(agentbackend.Config{
		Kind:       agentbackend.KindCodex,
		Bin:        "codex",
		WorkDir:    t.TempDir(),
		WorkerMode: "app_server",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	workerBackend, ok := b.(agentbackend.SessionWorkerBackend)
	if !ok {
		t.Fatalf("app_server codex backend does not implement SessionWorkerBackend")
	}

	worker, err := workerBackend.NewSessionWorker(context.Background(), agentbackend.Session{
		Kind:       agentbackend.KindCodex,
		WorkingDir: t.TempDir(),
	})
	if !errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
		t.Fatalf("NewSessionWorker error = %v, want ErrSessionWorkerUnavailable", err)
	}
	if worker != nil {
		t.Fatalf("NewSessionWorker returned worker %#v, want nil", worker)
	}
}
