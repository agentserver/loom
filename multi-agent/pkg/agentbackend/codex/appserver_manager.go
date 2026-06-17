package codex

import (
	"context"
	"errors"
	"os/exec"
	"sync"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

var (
	_ agentbackend.Backend              = (*workerBackend)(nil)
	_ agentbackend.SessionWorkerBackend = (*workerBackend)(nil)
)

type workerBackend struct {
	*Backend
	manager *appServerManager
}

func (b *workerBackend) NewSessionWorker(ctx context.Context, session agentbackend.Session) (agentbackend.SessionWorker, error) {
	if session.ID == "" || b.manager == nil {
		return nil, agentbackend.ErrSessionWorkerUnavailable
	}
	if err := b.manager.ensure(ctx); err != nil {
		if errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
			return nil, err
		}
		return nil, agentbackend.ErrSessionWorkerUnavailable
	}
	return nil, agentbackend.ErrSessionWorkerUnavailable
}

type appServerManager struct {
	cfg agentbackend.Config
	env []string

	mu      sync.Mutex
	started bool
	rpc     *appServerRPC
	cmd     *exec.Cmd
	err     error
}

func newAppServerManager(cfg agentbackend.Config, env []string) *appServerManager {
	return &appServerManager{
		cfg: cfg,
		env: append([]string(nil), env...),
	}
}

func (m *appServerManager) ensure(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if m.started {
		return nil
	}
	if err := ctx.Err(); err != nil {
		m.err = agentbackend.ErrSessionWorkerUnavailable
		return m.err
	}
	m.err = agentbackend.ErrSessionWorkerUnavailable
	return m.err
}

func (m *appServerManager) close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd != nil && m.cmd.Process != nil {
		return m.cmd.Process.Kill()
	}
	return nil
}
