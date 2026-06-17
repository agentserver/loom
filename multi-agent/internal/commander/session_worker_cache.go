package commander

import (
	"context"
	"sync"
	"time"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

const (
	defaultSessionWorkerMax         = 10
	defaultSessionWorkerIdleTimeout = 10 * time.Minute
)

type sessionWorkerKey struct {
	kind      agentbackend.Kind
	sessionID string
	workDir   string
}

type sessionWorkerEntry struct {
	key      sessionWorkerKey
	worker   agentbackend.SessionWorker
	lastUsed time.Time
	refs     int
	closed   bool
	mu       sync.Mutex
}

type sessionWorkerCache struct {
	mu          sync.Mutex
	entries     map[sessionWorkerKey]*sessionWorkerEntry
	max         int
	idleTimeout time.Duration
	now         func() time.Time
	closed      bool
	done        chan struct{}
	stopOnce    sync.Once
}

func newSessionWorkerCache(max int, idleTimeout time.Duration) *sessionWorkerCache {
	if max == 0 {
		max = defaultSessionWorkerMax
	}
	if idleTimeout == 0 {
		idleTimeout = defaultSessionWorkerIdleTimeout
	}
	c := &sessionWorkerCache{
		entries:     make(map[sessionWorkerKey]*sessionWorkerEntry),
		max:         max,
		idleTimeout: idleTimeout,
		now:         time.Now,
		done:        make(chan struct{}),
	}
	if idleTimeout >= 0 {
		go c.pruneLoop()
	}
	return c
}

func (c *sessionWorkerCache) acquire(ctx context.Context, key sessionWorkerKey, create func(context.Context) (agentbackend.SessionWorker, error)) (*sessionWorkerEntry, error) {
	if c.max < 0 {
		return nil, agentbackend.ErrSessionWorkerUnavailable
	}
	now := c.now()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, agentbackend.ErrSessionWorkerUnavailable
	}
	c.pruneIdleLocked(now)
	if entry := c.entries[key]; entry != nil {
		entry.refs++
		entry.lastUsed = now
		c.mu.Unlock()
		return entry, nil
	}
	c.ensureRoomLocked(now)
	if len(c.entries) >= c.max {
		c.mu.Unlock()
		return nil, agentbackend.ErrSessionWorkerUnavailable
	}
	c.mu.Unlock()

	worker, err := create(ctx)
	if err != nil {
		return nil, err
	}

	now = c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		_ = worker.Close()
		return nil, agentbackend.ErrSessionWorkerUnavailable
	}
	c.pruneIdleLocked(now)
	c.ensureRoomLocked(now)
	if len(c.entries) >= c.max {
		_ = worker.Close()
		return nil, agentbackend.ErrSessionWorkerUnavailable
	}
	entry := &sessionWorkerEntry{key: key, worker: worker, refs: 1, lastUsed: now}
	c.entries[key] = entry
	return entry, nil
}

func (c *sessionWorkerCache) release(entry *sessionWorkerEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if current := c.entries[entry.key]; current != entry {
		return
	}
	if entry.refs > 0 {
		entry.refs--
	}
	entry.lastUsed = c.now()
}

func (c *sessionWorkerCache) remove(entry *sessionWorkerEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if current := c.entries[entry.key]; current != entry {
		return
	}
	c.closeEntryLocked(entry)
}

func (c *sessionWorkerCache) isCurrent(entry *sessionWorkerEntry) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !entry.closed && c.entries[entry.key] == entry
}

func (c *sessionWorkerCache) activeKeys() map[sessionWorkerKey]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	active := make(map[sessionWorkerKey]bool, len(c.entries))
	for key, entry := range c.entries {
		if !entry.closed {
			active[key] = true
		}
	}
	return active
}

func (c *sessionWorkerCache) closeAll() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopOnce.Do(func() { close(c.done) })
	for _, entry := range c.entries {
		c.closeEntryLocked(entry)
	}
	c.closed = true
	return nil
}

func (c *sessionWorkerCache) pruneLoop() {
	interval := c.idleTimeout / 2
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.mu.Lock()
			if c.closed {
				c.mu.Unlock()
				return
			}
			c.pruneIdleLocked(c.now())
			c.mu.Unlock()
		case <-c.done:
			return
		}
	}
}

func (c *sessionWorkerCache) pruneIdleLocked(now time.Time) {
	if c.idleTimeout < 0 {
		return
	}
	for _, entry := range c.entries {
		if entry.refs != 0 {
			continue
		}
		if now.Sub(entry.lastUsed) <= c.idleTimeout {
			continue
		}
		c.closeEntryLocked(entry)
	}
}

func (c *sessionWorkerCache) ensureRoomLocked(now time.Time) {
	for len(c.entries) >= c.max {
		var oldest *sessionWorkerEntry
		for _, entry := range c.entries {
			if entry.refs != 0 {
				continue
			}
			if oldest == nil || entry.lastUsed.Before(oldest.lastUsed) {
				oldest = entry
			}
		}
		if oldest == nil {
			return
		}
		oldest.lastUsed = now
		c.closeEntryLocked(oldest)
	}
}

func (c *sessionWorkerCache) closeEntryLocked(entry *sessionWorkerEntry) {
	if entry.closed {
		return
	}
	entry.closed = true
	delete(c.entries, entry.key)
	_ = entry.worker.Close()
}
