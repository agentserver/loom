package identity

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"math/rand"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	defaultFreshTTL      = 180 * time.Second
	defaultStaleGrace    = 15 * time.Minute
	defaultCacheCapacity = 65536

	// recentPublishCapacity is the size of the dedupe ring used to suppress
	// re-logging when self-published revocations loop back via Subscribe.
	recentPublishCapacity = 32

	// invalidPublishDedupeWindow is the minimum interval between Publish calls
	// for the same bad-token key. Prevents a single attacker-controlled token
	// from producing a PG NOTIFY per request.
	invalidPublishDedupeWindow = time.Second

	// invalidPublishGlobalCap is the maximum number of invalid-token Publish
	// calls allowed per invalidPublishGlobalWindow. Exceeding this cap drops
	// the publish and increments a counter (DoS protection).
	invalidPublishGlobalCap    = 20
	invalidPublishGlobalWindow = time.Second
)

// RevocationChannel propagates identity cache invalidations across pods.
type RevocationChannel interface {
	// Subscribe starts delivering revocation events to onRevoke.
	// Returns a stop func; safe to call from any goroutine.
	// Events deliver only the cache key string (a hex-encoded SHA-256 of the
	// token) — never the secret material itself.
	Subscribe(ctx context.Context, onRevoke func(key string)) (stop func(), err error)
	// Publish broadcasts a revocation to all subscribers (including self).
	Publish(ctx context.Context, key string) error
}

// Option is a functional option for NewCache.
type Option func(*cacheOptions)

type cacheOptions struct {
	revocation RevocationChannel
}

// WithRevocationChannel attaches a cross-pod revocation channel to the cache.
// When a cache entry is evicted locally the key is published; when a remote
// revocation arrives the local entry is evicted.
func WithRevocationChannel(c RevocationChannel) Option {
	return func(o *cacheOptions) { o.revocation = c }
}

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
	opts     cacheOptions

	mu            sync.Mutex
	entries       map[string]*list.Element
	lru           *list.List
	recentPublish []string // ring buffer for dedupe

	// invalidPublish tracks the last time each bad-token key was published so
	// we can dedupe within a 1s window and enforce a global publish rate cap.
	// Protected by mu.
	invalidLastPublish   map[string]time.Time // key → last published time
	invalidGlobalCount   int                  // publishes in current window
	invalidGlobalWindowT time.Time            // start of current window

	group singleflight.Group
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

// NewCache returns a caching Resolver wrapping delegate.
// Optional Option values (e.g. WithRevocationChannel) extend the cache
// with cross-pod invalidation; callers that pass no opts retain the
// existing single-pod behaviour unchanged.
func NewCache(delegate Resolver, cfg CacheConfig, opts ...Option) Resolver {
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
	var options cacheOptions
	for _, opt := range opts {
		opt(&options)
	}
	c := &cacheResolver{
		delegate:           delegate,
		cfg:                cfg,
		opts:               options,
		entries:            make(map[string]*list.Element),
		lru:                list.New(),
		invalidLastPublish: make(map[string]time.Time),
	}
	if options.revocation != nil {
		c.subscribe()
	}
	return c
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
		if errors.Is(err, ErrInvalid) {
			// Use rate-limited eviction for bad tokens to prevent a spray of
			// attacker-controlled invalid tokens from triggering a PG NOTIFY per
			// request. Legitimate ErrRevoked from valid-but-revoked tokens takes
			// the unrestricted evict path below.
			c.evictInvalid(key)
			return resolveResult{err: err}, nil
		}
		if errors.Is(err, ErrRevoked) {
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

// evict removes a key from the local cache and, if a revocation channel is
// configured, publishes the invalidation to other pods.
func (c *cacheResolver) evict(key string) {
	c.localEvict(key)
	if c.opts.revocation != nil {
		// Pre-register this key so that when the subscribe loop receives the
		// broadcast of our own publish, it can suppress the redundant log.
		c.markSelfPublished(key)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := c.opts.revocation.Publish(ctx, key); err != nil {
			log.Printf("identity cache: revocation publish error key_prefix=%s len=%d: %v",
				keyPrefix(key), len(key), err)
		}
	}
}

// evictInvalid is like evict but applies a per-key dedupe window and a global
// publish-rate cap before calling Publish. This prevents a spray of bad tokens
// from producing one PG NOTIFY per request (DoS vector).
//
// The local eviction is unconditional; only the Publish is rate-limited.
func (c *cacheResolver) evictInvalid(key string) {
	c.localEvict(key)
	if c.opts.revocation != nil && c.allowInvalidPublish(key) {
		c.markSelfPublished(key)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := c.opts.revocation.Publish(ctx, key); err != nil {
			log.Printf("identity cache: revocation publish (invalid) error key_prefix=%s len=%d: %v",
				keyPrefix(key), len(key), err)
		}
	}
}

// allowInvalidPublish returns true if it is okay to Publish a revocation for
// this key at the current time. It enforces two limits under mu:
//  1. Per-key dedupe: the same key may not be published more than once per
//     invalidPublishDedupeWindow (default 1s).
//  2. Global cap: at most invalidPublishGlobalCap Publish calls per
//     invalidPublishGlobalWindow across all keys.
//
// Both are conservative defaults that have no impact on normal revocation
// traffic (legitimate revocations arrive through ErrRevoked, not ErrInvalid).
func (c *cacheResolver) allowInvalidPublish(key string) bool {
	now := c.cfg.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	// Per-key dedupe.
	if last, ok := c.invalidLastPublish[key]; ok {
		if now.Sub(last) < invalidPublishDedupeWindow {
			return false
		}
	}

	// Global rate cap: reset window if expired, then check.
	if now.Sub(c.invalidGlobalWindowT) >= invalidPublishGlobalWindow {
		c.invalidGlobalWindowT = now
		c.invalidGlobalCount = 0
	}
	if c.invalidGlobalCount >= invalidPublishGlobalCap {
		return false
	}

	// Allow: record state.
	c.invalidLastPublish[key] = now
	c.invalidGlobalCount++
	return true
}

// localEvict removes a key from the local cache only. Safe to call when a
// remote revocation arrives — does not trigger a further Publish.
func (c *cacheResolver) localEvict(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.entries[key]; ok {
		c.removeElement(elem)
	}
}

// subscribe starts the background goroutine that receives remote revocations
// and applies them via localEvict. The goroutine exits when the cache is
// garbage-collected (we use a background context; real lifetime management is
// left to the caller via RevocationChannel.Subscribe's stop func, but we
// deliberately never call stop here to keep the cache live for the process
// lifetime — matching the existing single-pod cache lifecycle).
func (c *cacheResolver) subscribe() {
	ctx := context.Background()
	_, err := c.opts.revocation.Subscribe(ctx, func(key string) {
		if c.isSelfPublished(key) {
			// Self-loop: localEvict would be a no-op; suppress the log.
			return
		}
		c.localEvict(key)
	})
	if err != nil {
		log.Printf("identity cache: revocation subscribe error: %v", err)
	}
}

// markSelfPublished records key in the dedupe ring so that the subscribe
// callback can recognise it as a self-loop and suppress logging.
func (c *cacheResolver) markSelfPublished(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.recentPublish) >= recentPublishCapacity {
		copy(c.recentPublish, c.recentPublish[1:])
		c.recentPublish = c.recentPublish[:len(c.recentPublish)-1]
	}
	c.recentPublish = append(c.recentPublish, key)
}

// isSelfPublished returns true if this pod recently published key itself.
func (c *cacheResolver) isSelfPublished(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, k := range c.recentPublish {
		if k == key {
			return true
		}
	}
	return false
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

// keyPrefix returns the first 8 characters of a key for safe logging.
func keyPrefix(key string) string {
	if len(key) >= 8 {
		return key[:8]
	}
	return key
}
