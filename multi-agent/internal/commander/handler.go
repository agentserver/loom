package commander

import (
	"context"
	"errors"
	"sync"
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
	workerCache *sessionWorkerCache

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
	if res, ok, err := h.trySessionWorker(ctx, id, prompt, sink); ok {
		if err == nil {
			return res, nil
		}
		if errors.Is(err, agentbackend.ErrSessionWorkerUnavailable) {
			return h.Backend.RunResume(ctx, id, prompt, sink)
		}
		if ctx.Err() != nil {
			return res, err
		}
		sink.Write("status", "hot worker failed; falling back to resume")
	}
	return h.Backend.RunResume(ctx, id, prompt, sink)
}

// Close releases daemon-owned hot workers. Daemon.Run calls it during shutdown;
// tests may call it directly when using Handler without Daemon.
func (h *Handler) Close() error {
	if h.workerCache == nil {
		return nil
	}
	return h.workerCache.closeAll()
}

func (h *Handler) trySessionWorker(ctx context.Context, id, prompt string, sink executor.Sink) (executor.Result, bool, error) {
	workerBackend, ok := h.Backend.(agentbackend.SessionWorkerBackend)
	if !ok {
		return executor.Result{}, false, nil
	}
	sess, _, err := h.Backend.GetSession(ctx, id)
	if err != nil {
		return executor.Result{}, true, agentbackend.ErrSessionWorkerUnavailable
	}
	if sess.ID == "" {
		sess.ID = id
	}
	if sess.Kind == "" {
		sess.Kind = h.Backend.Kind()
	}
	key := h.workerKey(sess)
	cache := h.ensureWorkerCache()
	entry, err := cache.acquire(ctx, key, func(ctx context.Context) (agentbackend.SessionWorker, error) {
		return workerBackend.NewSessionWorker(ctx, sess)
	})
	if err != nil {
		return executor.Result{}, true, err
	}
	defer cache.release(entry)

	entry.mu.Lock()
	defer entry.mu.Unlock()
	if !cache.isCurrent(entry) {
		return executor.Result{}, true, agentbackend.ErrSessionWorkerUnavailable
	}
	if health, ok := entry.worker.(agentbackend.HealthySessionWorker); ok && !health.Healthy() {
		cache.remove(entry)
		return executor.Result{}, true, agentbackend.ErrSessionWorkerUnavailable
	}
	res, err := entry.worker.Run(ctx, prompt, sink)
	if err != nil {
		cache.remove(entry)
	}
	return res, true, err
}

func (h *Handler) ensureWorkerCache() *sessionWorkerCache {
	h.workerOnce.Do(func() {
		h.workerCache = newSessionWorkerCache(h.WorkerMax, h.WorkerIdleTimeout)
	})
	return h.workerCache
}

func (h *Handler) markActiveWorkers(sessions []agentbackend.Session) {
	if h.workerCache == nil {
		return
	}
	active := h.workerCache.activeKeys()
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
