package commander

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// Handler is the transport-agnostic command dispatcher used by both the
// WebSocket client and the local HTTP server.
type Handler struct {
	Backend agentbackend.Backend

	// WorkerMax caps hot session workers kept by this daemon. Zero uses the
	// default. Negative values disable worker caching.
	WorkerMax int
	// WorkerIdleTimeout controls how long an idle worker remains hot. Zero uses
	// the default.
	WorkerIdleTimeout time.Duration

	workerOnce  sync.Once
	workerCache atomic.Pointer[sessionWorkerCache]
	closed      atomic.Bool

	turnMu    sync.Mutex
	turnLocks map[string]*turnLock
}

// ListSessions returns every session this backend has persisted.
func (h *Handler) ListSessions(ctx context.Context) ([]agentbackend.Session, error) {
	sessions, err := h.Backend.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	h.markActiveWorkers(sessions)
	return sessions, nil
}

// GetSession returns descriptor and message history for one session ID.
func (h *Handler) GetSession(ctx context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
	return h.Backend.GetSession(ctx, id)
}

// SessionTurn runs one user turn against an existing session.
func (h *Handler) SessionTurn(ctx context.Context, id, prompt string, sink executor.Sink) (executor.Result, error) {
	unlock := h.lockTurn(id)
	defer unlock()
	if res, ok, canFallback, err := h.trySessionWorker(ctx, id, prompt, sink); ok {
		if err == nil {
			return res, nil
		}
		if canFallback {
			if !errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
				agentbackend.WriteStatus(sink, agentbackend.StatusStarting, "hot worker unavailable; falling back to resume")
			}
			return h.Backend.RunResume(ctx, id, prompt, sink)
		}
		return res, err
	}
	return h.Backend.RunResume(ctx, id, prompt, sink)
}

// Close releases daemon-owned hot workers. Daemon.Run calls it during shutdown;
// tests may call it directly when using Handler without Daemon.
func (h *Handler) Close() error {
	h.closed.Store(true)
	h.workerOnce.Do(func() {})
	cache := h.workerCache.Load()
	if cache == nil {
		return nil
	}
	return cache.closeAll()
}

func (h *Handler) trySessionWorker(ctx context.Context, id, prompt string, sink executor.Sink) (executor.Result, bool, bool, error) {
	workerBackend, ok := h.Backend.(agentbackend.SessionWorkerBackend)
	if !ok {
		return executor.Result{}, false, false, nil
	}
	sess, _, err := h.Backend.GetSession(ctx, id)
	if err != nil {
		return executor.Result{}, true, true, agentbackend.ErrSessionWorkerUnavailable
	}
	if sess.ID == "" {
		sess.ID = id
	}
	if sess.Kind == "" {
		sess.Kind = h.Backend.Kind()
	}
	key := h.workerKey(sess)
	cache := h.ensureWorkerCache()
	if cache == nil {
		return executor.Result{}, true, true, agentbackend.ErrSessionWorkerUnavailable
	}
	entry, err := cache.acquire(ctx, key, func(ctx context.Context) (agentbackend.SessionWorker, error) {
		return workerBackend.NewSessionWorker(ctx, sess)
	})
	if err != nil {
		return executor.Result{}, true, true, err
	}
	defer cache.release(entry)

	if !cache.isCurrent(entry) {
		return executor.Result{}, true, true, agentbackend.ErrSessionWorkerUnavailable
	}
	if health, ok := entry.worker.(agentbackend.HealthySessionWorker); ok && !health.Healthy() {
		cache.remove(entry)
		return executor.Result{}, true, true, agentbackend.ErrSessionWorkerUnavailable
	}
	res, err := entry.worker.Run(ctx, prompt, sink)
	if err != nil {
		cache.remove(entry)
	}
	return res, true, false, err
}

func (h *Handler) ensureWorkerCache() *sessionWorkerCache {
	h.workerOnce.Do(func() {
		if h.closed.Load() || h.WorkerMax < 0 {
			return
		}
		h.workerCache.Store(newSessionWorkerCache(h.WorkerMax, h.WorkerIdleTimeout))
	})
	return h.workerCache.Load()
}

func (h *Handler) markActiveWorkers(sessions []agentbackend.Session) {
	if _, ok := h.Backend.(agentbackend.SessionWorkerBackend); !ok {
		return
	}
	cache := h.workerCache.Load()
	if cache == nil {
		return
	}
	active := cache.activeKeys()
	for i := range sessions {
		sessions[i].ActiveWorker = active[h.workerKey(sessions[i])]
	}
}

func (h *Handler) workerKey(sess agentbackend.Session) sessionWorkerKey {
	kind := sess.Kind
	if kind == "" {
		kind = h.Backend.Kind()
	}
	return sessionWorkerKey{kind: kind, sessionID: sess.ID, workDir: sess.WorkingDir}
}

func (h *Handler) lockTurn(sessionID string) func() {
	h.turnMu.Lock()
	if h.turnLocks == nil {
		h.turnLocks = make(map[string]*turnLock)
	}
	lock := h.turnLocks[sessionID]
	if lock == nil {
		lock = &turnLock{}
		h.turnLocks[sessionID] = lock
	}
	lock.refs++
	h.turnMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		h.turnMu.Lock()
		lock.refs--
		if lock.refs == 0 && h.turnLocks[sessionID] == lock {
			delete(h.turnLocks, sessionID)
		}
		h.turnMu.Unlock()
	}
}
