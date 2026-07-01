package identity

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCacheReturnsFreshHitWithoutDelegateCall(t *testing.T) {
	now := time.Unix(100, 0)
	var calls atomic.Int32
	delegate := resolverFunc(func(context.Context, string) (Identity, error) {
		calls.Add(1)
		return Identity{WorkspaceID: "ws-1", AgentID: "agent-1"}, nil
	})
	resolver := NewCache(delegate, CacheConfig{
		FreshTTL:   10 * time.Second,
		StaleGrace: time.Minute,
		Capacity:   10,
		Now:        func() time.Time { return now },
		Jitter:     func() float64 { return 1 },
	})

	first, err := resolver.Resolve(context.Background(), "tok")
	require.NoError(t, err)
	second, err := resolver.Resolve(context.Background(), "tok")
	require.NoError(t, err)

	require.Equal(t, first, second)
	require.Equal(t, int32(1), calls.Load())
}

func TestCacheFailOpensWithStaleEntryOnUpstreamError(t *testing.T) {
	now := time.Unix(100, 0)
	var upstreamErr error
	var calls atomic.Int32
	delegate := resolverFunc(func(context.Context, string) (Identity, error) {
		calls.Add(1)
		if upstreamErr != nil {
			return Identity{}, upstreamErr
		}
		return Identity{WorkspaceID: "ws-1", AgentID: "agent-1"}, nil
	})
	resolver := NewCache(delegate, CacheConfig{
		FreshTTL:   10 * time.Second,
		StaleGrace: time.Minute,
		Capacity:   10,
		Now:        func() time.Time { return now },
		Jitter:     func() float64 { return 1 },
	})

	want, err := resolver.Resolve(context.Background(), "tok")
	require.NoError(t, err)
	now = now.Add(10*time.Second + time.Nanosecond)
	upstreamErr = ErrUpstream

	got, err := resolver.Resolve(context.Background(), "tok")
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.Equal(t, int32(2), calls.Load())
}

func TestCacheDoesNotFailOpenOnInvalidOrRevoked(t *testing.T) {
	for _, errToReturn := range []error{ErrInvalid, ErrRevoked} {
		t.Run(errToReturn.Error(), func(t *testing.T) {
			now := time.Unix(100, 0)
			var upstreamErr error
			delegate := resolverFunc(func(context.Context, string) (Identity, error) {
				if upstreamErr != nil {
					return Identity{}, upstreamErr
				}
				return Identity{WorkspaceID: "ws-1", AgentID: "agent-1"}, nil
			})
			resolver := NewCache(delegate, CacheConfig{
				FreshTTL:   10 * time.Second,
				StaleGrace: time.Minute,
				Capacity:   10,
				Now:        func() time.Time { return now },
				Jitter:     func() float64 { return 1 },
			})

			_, err := resolver.Resolve(context.Background(), "tok")
			require.NoError(t, err)
			now = now.Add(10*time.Second + time.Nanosecond)
			upstreamErr = errToReturn

			_, err = resolver.Resolve(context.Background(), "tok")
			require.ErrorIs(t, err, errToReturn)
		})
	}
}

func TestCacheDiscardsEntryBeyondStaleGrace(t *testing.T) {
	now := time.Unix(100, 0)
	var upstreamErr error
	delegate := resolverFunc(func(context.Context, string) (Identity, error) {
		if upstreamErr != nil {
			return Identity{}, upstreamErr
		}
		return Identity{WorkspaceID: "ws-1", AgentID: "agent-1"}, nil
	})
	resolver := NewCache(delegate, CacheConfig{
		FreshTTL:   10 * time.Second,
		StaleGrace: time.Minute,
		Capacity:   10,
		Now:        func() time.Time { return now },
		Jitter:     func() float64 { return 1 },
	})

	_, err := resolver.Resolve(context.Background(), "tok")
	require.NoError(t, err)
	now = now.Add(70*time.Second + time.Nanosecond)
	upstreamErr = ErrUpstream

	_, err = resolver.Resolve(context.Background(), "tok")
	require.ErrorIs(t, err, ErrUpstream)
}

func TestCacheDeduplicatesConcurrentMissesForSameToken(t *testing.T) {
	now := time.Unix(100, 0)
	var calls atomic.Int32
	delegate := resolverFunc(func(context.Context, string) (Identity, error) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return Identity{WorkspaceID: "ws-1", AgentID: "agent-1"}, nil
	})
	resolver := NewCache(delegate, CacheConfig{
		FreshTTL:   time.Minute,
		StaleGrace: time.Minute,
		Capacity:   10,
		Now:        func() time.Time { return now },
		Jitter:     func() float64 { return 1 },
	})

	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := resolver.Resolve(context.Background(), "tok")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, int32(1), calls.Load())
}

func TestCacheEvictsLeastRecentlyUsedEntryAtCapacity(t *testing.T) {
	now := time.Unix(100, 0)
	var calls atomic.Int32
	delegate := resolverFunc(func(_ context.Context, token string) (Identity, error) {
		calls.Add(1)
		return Identity{WorkspaceID: "ws-" + token, AgentID: "agent-" + token}, nil
	})
	resolver := NewCache(delegate, CacheConfig{
		FreshTTL:   time.Minute,
		StaleGrace: time.Minute,
		Capacity:   2,
		Now:        func() time.Time { return now },
		Jitter:     func() float64 { return 1 },
	})

	_, err := resolver.Resolve(context.Background(), "a")
	require.NoError(t, err)
	_, err = resolver.Resolve(context.Background(), "b")
	require.NoError(t, err)
	_, err = resolver.Resolve(context.Background(), "a")
	require.NoError(t, err)
	_, err = resolver.Resolve(context.Background(), "c")
	require.NoError(t, err)
	_, err = resolver.Resolve(context.Background(), "b")
	require.NoError(t, err)

	require.Equal(t, int32(4), calls.Load())
}

// countingRevocationChannel is a test double that counts Publish calls per key.
// Distinct from the fakeRevocationChannel in revocation_pg_test.go which tracks
// subscriber delivery; this one just counts publishes for rate-limit assertions.
type countingRevocationChannel struct {
	mu        sync.Mutex
	published map[string]int // key → publish count
}

func newCountingRevocationChannel() *countingRevocationChannel {
	return &countingRevocationChannel{published: make(map[string]int)}
}

func (f *countingRevocationChannel) Publish(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published[key]++
	return nil
}

func (f *countingRevocationChannel) Subscribe(_ context.Context, _ func(string)) (func(), error) {
	return func() {}, nil
}

func (f *countingRevocationChannel) count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.published[key]
}

func (f *countingRevocationChannel) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for _, c := range f.published {
		total += c
	}
	return total
}

// TestCache_ErrInvalid_RateLimit_DedupesSameKeyWithin1s verifies that a spray
// of ErrInvalid responses for the same previously-cached token results in only
// ONE Publish call within a 1s window (per-key dedupe).
// The token must be cached first (cache-gated: only cached tokens produce Publish).
func TestCache_ErrInvalid_RateLimit_DedupesSameKeyWithin1s(t *testing.T) {
	now := time.Unix(1000, 0)
	firstCall := true
	delegate := resolverFunc(func(context.Context, string) (Identity, error) {
		if firstCall {
			firstCall = false
			return Identity{WorkspaceID: "ws1"}, nil
		}
		return Identity{}, ErrInvalid
	})
	rev := newCountingRevocationChannel()
	resolver := NewCache(delegate, CacheConfig{
		FreshTTL:   time.Second,
		StaleGrace: time.Minute, // non-zero so stale() keeps entry while we call delegate
		Capacity:   10,
		Now:        func() time.Time { return now },
		Jitter:     func() float64 { return 1 },
	}, WithRevocationChannel(rev))

	// First call: successfully caches the token.
	_, _ = resolver.Resolve(context.Background(), "bad-token")
	// Advance past FreshTTL but within StaleGrace so stale() returns entry without evicting it.
	// evictInvalid → localEvictReporting finds the stale entry → hadEntry=true → publish allowed.
	now = now.Add(time.Second + time.Millisecond)

	// Resolve the same token many times. The first finds the stale entry (cache-gate passes),
	// evicts it, and publishes (count=1). All subsequent calls find no entry (already evicted)
	// → no publish.
	for i := 0; i < 50; i++ {
		_, _ = resolver.Resolve(context.Background(), "bad-token")
	}

	// Only 1 Publish should have been made (the rest had no cached entry to evict).
	count := rev.count(tokenKey("bad-token"))
	require.Equal(t, 1, count, "expected exactly 1 publish for same bad key within dedupe window, got %d", count)
}

// TestCache_ErrInvalid_RateLimit_GlobalCapAcrossKeys verifies that
// invalidPublishGlobalCap is enforced across distinct keys: after
// invalidPublishGlobalCap distinct keys are published in a single window,
// additional keys are silently dropped.
// Tokens must be pre-cached (cache-gate: only cached tokens may publish).
func TestCache_ErrInvalid_RateLimit_GlobalCapAcrossKeys(t *testing.T) {
	now := time.Unix(2000, 0)
	// Track which tokens have been cached (first call = success, subsequent = ErrInvalid).
	cached := make(map[string]bool)
	delegate := resolverFunc(func(_ context.Context, token string) (Identity, error) {
		if !cached[token] {
			cached[token] = true
			return Identity{WorkspaceID: "ws-" + token}, nil
		}
		return Identity{}, ErrInvalid
	})
	rev := newCountingRevocationChannel()
	resolver := NewCache(delegate, CacheConfig{
		FreshTTL:   time.Second,
		StaleGrace: time.Minute, // non-zero so stale() keeps entry for evictInvalid's cache-gate
		Capacity:   1000,
		Now:        func() time.Time { return now },
		Jitter:     func() float64 { return 1 },
	}, WithRevocationChannel(rev))

	total := invalidPublishGlobalCap + 10
	tokens := make([]string, total)
	for i := 0; i < total; i++ {
		tokens[i] = fmt.Sprintf("bad-token-%d", i)
	}

	// Step 1: Cache all tokens successfully.
	for _, tok := range tokens {
		_, _ = resolver.Resolve(context.Background(), tok)
	}

	// Step 2: Advance past FreshTTL so all entries go stale.
	now = now.Add(time.Second + time.Millisecond)

	// Step 3: Re-resolve all tokens — each finds a stale entry, calls delegate
	// (ErrInvalid), evicts the local entry (cache-gate passes), and attempts to
	// publish. The global cap must prevent more than invalidPublishGlobalCap publishes.
	for _, tok := range tokens {
		_, _ = resolver.Resolve(context.Background(), tok)
	}

	// Total publishes must be capped at invalidPublishGlobalCap.
	got := rev.total()
	require.LessOrEqual(t, got, invalidPublishGlobalCap,
		"expected at most %d global publishes, got %d", invalidPublishGlobalCap, got)
}

// TestCache_ErrInvalid_RateLimit_AllowsAfterWindowExpires verifies that after
// the dedupe window expires the same previously-cached bad key is allowed to
// publish again.
//
// The token is cached on the first call (success), then we drive two ErrInvalid
// events: one at t=0 (publishes), one after the dedupe window (publishes again).
// Between the two ErrInvalid events the token must be re-cached (success) so
// that the cache-gate allows the second publish.
func TestCache_ErrInvalid_RateLimit_AllowsAfterWindowExpires(t *testing.T) {
	now := time.Unix(3000, 0)
	callN := 0
	delegate := resolverFunc(func(context.Context, string) (Identity, error) {
		callN++
		switch callN {
		case 1: // cache the token
			return Identity{WorkspaceID: "ws1"}, nil
		case 2: // first ErrInvalid — evicts the entry
			return Identity{}, ErrInvalid
		case 3: // re-cache the token so the cache-gate allows the next ErrInvalid
			return Identity{WorkspaceID: "ws1"}, nil
		default: // second ErrInvalid after dedupe window
			return Identity{}, ErrInvalid
		}
	})
	rev := newCountingRevocationChannel()
	resolver := NewCache(delegate, CacheConfig{
		FreshTTL:   time.Second,
		StaleGrace: time.Minute, // non-zero so stale() keeps entry for evictInvalid's cache-gate
		Capacity:   10,
		Now:        func() time.Time { return now },
		Jitter:     func() float64 { return 1 },
	}, WithRevocationChannel(rev))

	// Call 1: cache the token (delegate returns success).
	_, _ = resolver.Resolve(context.Background(), "bad-token")

	// Advance past FreshTTL so the cache entry goes stale.
	now = now.Add(time.Second + time.Millisecond)

	// Call 2: delegate returns ErrInvalid → entry evicted → publish (count=1).
	_, _ = resolver.Resolve(context.Background(), "bad-token")
	require.Equal(t, 1, rev.count(tokenKey("bad-token")), "first ErrInvalid should publish")

	// Call 3: re-cache the token (advance time to ensure a fresh resolve runs).
	// We advance past dedupe window so the next resolve can publish.
	now = now.Add(time.Second + time.Millisecond)
	_, _ = resolver.Resolve(context.Background(), "bad-token") // success re-caches

	// Advance past FreshTTL again AND past the dedupe window.
	now = now.Add(time.Second + time.Millisecond + invalidPublishDedupeWindow)

	// Call 4: delegate returns ErrInvalid again → entry evicted → publish (count=2).
	_, _ = resolver.Resolve(context.Background(), "bad-token")
	require.Equal(t, 2, rev.count(tokenKey("bad-token")), "second ErrInvalid after window should publish")
}

// TestCache_ErrRevoked_NotRateLimited verifies that legitimate revocations
// (ErrRevoked) bypass the invalid-token rate limiter entirely: each revocation
// triggers an unconditional Publish.
func TestCache_ErrRevoked_NotRateLimited(t *testing.T) {
	now := time.Unix(4000, 0)
	calls := 0
	delegate := resolverFunc(func(context.Context, string) (Identity, error) {
		calls++
		if calls == 1 {
			return Identity{WorkspaceID: "ws1"}, nil
		}
		return Identity{}, ErrRevoked
	})
	rev := newCountingRevocationChannel()
	resolver := NewCache(delegate, CacheConfig{
		FreshTTL: time.Nanosecond, // expire immediately so delegate is called every time
		Capacity: 10,
		Now:      func() time.Time { return now },
		Jitter:   func() float64 { return 1 },
	}, WithRevocationChannel(rev))

	// First call: succeeds (puts in cache).
	_, _ = resolver.Resolve(context.Background(), "good-token")

	// Second call: ErrRevoked → must publish without rate limit.
	now = now.Add(time.Second) // advance past FreshTTL
	_, _ = resolver.Resolve(context.Background(), "good-token")

	// Third call: another ErrRevoked for the same key within the same second
	// should still publish (because ErrRevoked bypasses the rate limiter).
	now = now.Add(time.Millisecond)
	_, _ = resolver.Resolve(context.Background(), "good-token")

	key := tokenKey("good-token")
	count := rev.count(key)
	// Each ErrRevoked triggers evict → Publish (no rate limit). At least 2.
	require.GreaterOrEqual(t, count, 2, "ErrRevoked must always publish, got %d", count)
}

// ---------------------------------------------------------------------------
// D-fix2 Finding-4 tests
// ---------------------------------------------------------------------------

// TestCache_ErrInvalid_NotCached_DoesNotPublish verifies that a token returning
// ErrInvalid that was NEVER cached does NOT produce a Publish call. Cache-gated
// publish: nothing-to-evict means nothing-to-broadcast.
func TestCache_ErrInvalid_NotCached_DoesNotPublish(t *testing.T) {
	now := time.Unix(5000, 0)
	delegate := resolverFunc(func(context.Context, string) (Identity, error) {
		return Identity{}, ErrInvalid
	})
	rev := newCountingRevocationChannel()
	resolver := NewCache(delegate, CacheConfig{
		FreshTTL: time.Second,
		Capacity: 10,
		Now:      func() time.Time { return now },
		Jitter:   func() float64 { return 1 },
	}, WithRevocationChannel(rev))

	// Spray 30 distinct attacker tokens — none were ever cached.
	for i := 0; i < 30; i++ {
		token := fmt.Sprintf("attacker-token-%d", i)
		_, _ = resolver.Resolve(context.Background(), token)
	}

	// No Publish calls should have been made.
	require.Equal(t, 0, rev.total(),
		"attacker tokens that were never cached must not produce Publish calls, got %d", rev.total())
}

// TestCache_InvalidLastPublishLRUBound verifies that the per-key publish-dedupe
// LRU is bounded at invalidLastPublishLRUCap entries. Spraying more than the cap
// of distinct keys must not grow the internal LRU beyond the cap.
func TestCache_InvalidLastPublishLRUBound(t *testing.T) {
	now := time.Unix(6000, 0)
	// Delegate always succeeds on first call to populate cache, then returns ErrInvalid.
	firstCall := make(map[string]bool)
	delegate := resolverFunc(func(_ context.Context, token string) (Identity, error) {
		if !firstCall[token] {
			firstCall[token] = true
			return Identity{WorkspaceID: "ws-" + token}, nil
		}
		return Identity{}, ErrInvalid
	})
	rev := newCountingRevocationChannel()
	cr := NewCache(delegate, CacheConfig{
		FreshTTL: time.Nanosecond, // expire immediately so delegate is called on every resolve
		Capacity: 10000,
		Now:      func() time.Time { return now },
		Jitter:   func() float64 { return 1 },
	}, WithRevocationChannel(rev)).(*cacheResolver)

	// Populate cache entries, then advance time so they become stale.
	for i := 0; i < invalidLastPublishLRUCap+100; i++ {
		token := fmt.Sprintf("tok-%d", i)
		_, _ = cr.Resolve(context.Background(), token)
	}

	// Advance time so FreshTTL expires, forcing re-resolve with ErrInvalid.
	now = now.Add(time.Second * 2)

	// Re-resolve all tokens — each has a cache entry, so publish is attempted.
	// Spread across multiple time windows so global cap doesn't interfere.
	for i := 0; i < invalidLastPublishLRUCap+100; i++ {
		token := fmt.Sprintf("tok-%d", i)
		_, _ = cr.Resolve(context.Background(), token)
		// Advance time per key to avoid per-key dedupe AND global cap.
		now = now.Add(invalidPublishDedupeWindow + time.Millisecond)
		// Reset global window every few iterations.
		if i%invalidPublishGlobalCap == 0 {
			now = now.Add(invalidPublishGlobalWindow)
		}
	}

	// The LRU must not exceed the cap.
	cr.mu.Lock()
	lruSize := len(cr.invalidLastPublish)
	cr.mu.Unlock()
	require.LessOrEqual(t, lruSize, invalidLastPublishLRUCap,
		"invalidLastPublish LRU must be bounded at %d, got %d", invalidLastPublishLRUCap, lruSize)
}

// TestCache_Subscribe_RetriesOnError verifies that when Subscribe returns an
// error, the cache retries with backoff. We use a test RevocationChannel that
// fails the first N Subscribe calls, then succeeds. The resolver must still
// function (Resolve calls work regardless) and the subscribe goroutine must
// eventually succeed.
func TestCache_Subscribe_RetriesOnError(t *testing.T) {
	now := time.Unix(7000, 0)
	delegate := resolverFunc(func(context.Context, string) (Identity, error) {
		return Identity{WorkspaceID: "ws1"}, nil
	})

	const failCount = 3
	attempts := make(chan struct{}, failCount+2)
	subscribeSuccess := make(chan struct{})
	rev := &retryTestRevocationChannel{
		failCount:       failCount,
		attempts:        attempts,
		subscribeSuccess: subscribeSuccess,
	}

	_ = NewCache(delegate, CacheConfig{
		FreshTTL: time.Minute,
		Capacity: 10,
		Now:      func() time.Time { return now },
		Jitter:   func() float64 { return 1 },
	}, WithRevocationChannel(rev))

	// Wait for the subscribe goroutine to succeed (after failCount retries).
	select {
	case <-subscribeSuccess:
		// expected
	case <-time.After(10 * time.Second):
		t.Fatal("subscribe goroutine did not eventually succeed within 10s")
	}

	// Total Subscribe attempts must be > failCount (retries happened).
	require.GreaterOrEqual(t, len(attempts), failCount+1,
		"expected at least %d Subscribe attempts (including retries)", failCount+1)
}

// retryTestRevocationChannel is a test RevocationChannel that fails the first
// failCount Subscribe calls, then succeeds. It records all attempts.
type retryTestRevocationChannel struct {
	mu              sync.Mutex
	callCount       int
	failCount       int
	attempts        chan struct{}
	subscribeSuccess chan struct{}
}

func (r *retryTestRevocationChannel) Subscribe(_ context.Context, _ func(string)) (func(), error) {
	r.mu.Lock()
	r.callCount++
	count := r.callCount
	r.mu.Unlock()

	select {
	case r.attempts <- struct{}{}:
	default:
	}

	if count <= r.failCount {
		return nil, fmt.Errorf("subscribe failed (attempt %d/%d)", count, r.failCount)
	}
	// Success: signal and return nil stop func.
	select {
	case r.subscribeSuccess <- struct{}{}:
	default:
	}
	return func() {}, nil
}

func (r *retryTestRevocationChannel) Publish(_ context.Context, _ string) error {
	return nil
}
