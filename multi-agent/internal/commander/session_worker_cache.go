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
}

type sessionWorkerCache struct {
	mu          sync.Mutex
	cond        *sync.Cond
	entries     map[sessionWorkerKey]*sessionWorkerEntry
	max         int
	idleTimeout time.Duration
	now         func() time.Time
	closed      bool
	done        chan struct{}
	stopOnce    sync.Once
}

func newSessionWorkerCache(max int, idleTimeout time.Duration) *sessionWorkerCache {
	if max < 0 {
		return nil
	}
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
	c.cond = sync.NewCond(&c.mu)
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
	var toClose []*sessionWorkerEntry
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, agentbackend.ErrSessionWorkerUnavailable
	}
	toClose = append(toClose, c.pruneIdleLocked(now)...)
	if entry := c.entries[key]; entry != nil {
		entry.refs++
		entry.lastUsed = now
		c.mu.Unlock()
		closeWorkerEntries(toClose)
		return entry, nil
	}
	toClose = append(toClose, c.ensureRoomLocked()...)
	if len(c.entries) >= c.max {
		c.mu.Unlock()
		closeWorkerEntries(toClose)
		return nil, agentbackend.ErrSessionWorkerUnavailable
	}
	c.mu.Unlock()
	closeWorkerEntries(toClose)

	worker, err := create(ctx)
	if err != nil {
		return nil, err
	}

	now = c.now()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = worker.Close()
		return nil, agentbackend.ErrSessionWorkerUnavailable
	}
	toClose = c.pruneIdleLocked(now)
	toClose = append(toClose, c.ensureRoomLocked()...)
	if len(c.entries) >= c.max {
		c.mu.Unlock()
		closeWorkerEntries(toClose)
		_ = worker.Close()
		return nil, agentbackend.ErrSessionWorkerUnavailable
	}
	entry := &sessionWorkerEntry{key: key, worker: worker, refs: 1, lastUsed: now}
	c.entries[key] = entry
	c.mu.Unlock()
	closeWorkerEntries(toClose)
	return entry, nil
}

func (c *sessionWorkerCache) release(entry *sessionWorkerEntry) {
	c.mu.Lock()
	var toClose []*sessionWorkerEntry
	if current := c.entries[entry.key]; current != entry {
		c.cond.Broadcast()
		c.mu.Unlock()
		return
	}
	if entry.refs > 0 {
		entry.refs--
	}
	entry.lastUsed = c.now()
	if c.closed && entry.refs == 0 {
		toClose = append(toClose, c.detachEntryLocked(entry))
	}
	c.cond.Broadcast()
	c.mu.Unlock()
	closeWorkerEntries(toClose)
}

func (c *sessionWorkerCache) remove(entry *sessionWorkerEntry) {
	c.mu.Lock()
	var toClose []*sessionWorkerEntry
	if current := c.entries[entry.key]; current != entry {
		c.cond.Broadcast()
		c.mu.Unlock()
		return
	}
	toClose = append(toClose, c.detachEntryLocked(entry))
	c.cond.Broadcast()
	c.mu.Unlock()
	closeWorkerEntries(toClose)
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
	c.stopOnce.Do(func() { close(c.done) })
	c.closed = true
	for {
		toClose := c.detachIdleLocked()
		if len(c.entries) == 0 {
			c.mu.Unlock()
			closeWorkerEntries(toClose)
			return nil
		}
		c.mu.Unlock()
		closeWorkerEntries(toClose)
		c.mu.Lock()
		if len(c.entries) > 0 {
			c.cond.Wait()
		}
	}
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
			toClose := c.pruneIdleLocked(c.now())
			c.mu.Unlock()
			closeWorkerEntries(toClose)
		case <-c.done:
			return
		}
	}
}

func (c *sessionWorkerCache) pruneIdleLocked(now time.Time) []*sessionWorkerEntry {
	if c.idleTimeout < 0 {
		return nil
	}
	var toClose []*sessionWorkerEntry
	for _, entry := range c.entries {
		if entry.refs != 0 {
			continue
		}
		if now.Sub(entry.lastUsed) <= c.idleTimeout {
			continue
		}
		toClose = append(toClose, c.detachEntryLocked(entry))
	}
	return toClose
}

func (c *sessionWorkerCache) ensureRoomLocked() []*sessionWorkerEntry {
	var toClose []*sessionWorkerEntry
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
			return toClose
		}
		toClose = append(toClose, c.detachEntryLocked(oldest))
	}
	return toClose
}

func (c *sessionWorkerCache) detachIdleLocked() []*sessionWorkerEntry {
	var toClose []*sessionWorkerEntry
	for _, entry := range c.entries {
		if entry.refs != 0 {
			continue
		}
		toClose = append(toClose, c.detachEntryLocked(entry))
	}
	return toClose
}

func (c *sessionWorkerCache) detachEntryLocked(entry *sessionWorkerEntry) *sessionWorkerEntry {
	if entry.closed {
		return nil
	}
	entry.closed = true
	delete(c.entries, entry.key)
	return entry
}

func closeWorkerEntries(entries []*sessionWorkerEntry) {
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		_ = entry.worker.Close()
	}
}
