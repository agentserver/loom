package identity

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/rand"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	defaultFreshTTL      = 180 * time.Second
	defaultStaleGrace    = 15 * time.Minute
	defaultCacheCapacity = 65536
)

type CacheConfig struct {
	FreshTTL   time.Duration
	StaleGrace time.Duration
	Capacity   int

	Now    func() time.Time
	Jitter func() float64
}

type cacheResolver struct {
	delegate Resolver
	cfg      CacheConfig

	mu      sync.Mutex
	entries map[string]*list.Element
	lru     *list.List
	group   singleflight.Group
}

type cacheEntry struct {
	key       string
	identity  Identity
	fetchedAt time.Time
	expiresAt time.Time
}

type resolveResult struct {
	identity Identity
	err      error
}

func NewCache(delegate Resolver, cfg CacheConfig) Resolver {
	if delegate == nil {
		panic("identity: nil cache delegate")
	}
	if cfg.FreshTTL <= 0 {
		cfg.FreshTTL = defaultFreshTTL
	}
	if cfg.StaleGrace < 0 {
		cfg.StaleGrace = defaultStaleGrace
	}
	if cfg.Capacity <= 0 {
		cfg.Capacity = defaultCacheCapacity
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Jitter == nil {
		cfg.Jitter = func() float64 {
			return 0.8 + rand.Float64()*0.4
		}
	}
	return &cacheResolver{
		delegate: delegate,
		cfg:      cfg,
		entries:  make(map[string]*list.Element),
		lru:      list.New(),
	}
}

func (c *cacheResolver) Resolve(ctx context.Context, token string) (Identity, error) {
	if token == "" {
		return Identity{}, ErrInvalid
	}
	key := tokenKey(token)
	now := c.cfg.Now()
	if ident, ok := c.fresh(key, now); ok {
		return ident, nil
	}

	value, _, _ := c.group.Do(key, func() (any, error) {
		now := c.cfg.Now()
		if ident, ok := c.fresh(key, now); ok {
			return resolveResult{identity: ident}, nil
		}
		stale, hasStale := c.stale(key, now)
		ident, err := c.delegate.Resolve(ctx, token)
		if err == nil {
			c.put(key, ident, now)
			return resolveResult{identity: ident}, nil
		}
		if errors.Is(err, ErrInvalid) || errors.Is(err, ErrRevoked) {
			c.evict(key)
			return resolveResult{err: err}, nil
		}
		if errors.Is(err, ErrUpstream) && hasStale {
			return resolveResult{identity: stale}, nil
		}
		return resolveResult{err: err}, nil
	})
	result := value.(resolveResult)
	return result.identity, result.err
}

func (c *cacheResolver) fresh(key string, now time.Time) (Identity, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	elem, ok := c.entries[key]
	if !ok {
		return Identity{}, false
	}
	ent := elem.Value.(*cacheEntry)
	if now.After(ent.expiresAt) {
		return Identity{}, false
	}
	c.lru.MoveToFront(elem)
	return ent.identity, true
}

func (c *cacheResolver) stale(key string, now time.Time) (Identity, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	elem, ok := c.entries[key]
	if !ok {
		return Identity{}, false
	}
	ent := elem.Value.(*cacheEntry)
	if now.After(ent.expiresAt.Add(c.cfg.StaleGrace)) {
		c.removeElement(elem)
		return Identity{}, false
	}
	return ent.identity, true
}

func (c *cacheResolver) put(key string, ident Identity, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	multiplier := c.cfg.Jitter()
	if multiplier <= 0 {
		multiplier = 1
	}
	ent := &cacheEntry{
		key:       key,
		identity:  ident,
		fetchedAt: now,
		expiresAt: now.Add(time.Duration(float64(c.cfg.FreshTTL) * multiplier)),
	}
	if elem, ok := c.entries[key]; ok {
		elem.Value = ent
		c.lru.MoveToFront(elem)
		return
	}
	elem := c.lru.PushFront(ent)
	c.entries[key] = elem
	for len(c.entries) > c.cfg.Capacity {
		c.removeElement(c.lru.Back())
	}
}

func (c *cacheResolver) evict(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.entries[key]; ok {
		c.removeElement(elem)
	}
}

func (c *cacheResolver) removeElement(elem *list.Element) {
	if elem == nil {
		return
	}
	c.lru.Remove(elem)
	delete(c.entries, elem.Value.(*cacheEntry).key)
}

func tokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
