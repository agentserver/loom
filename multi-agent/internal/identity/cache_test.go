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
// of ErrInvalid responses for the same bad token results in only ONE Publish
// call within a 1s window (per-key dedupe).
func TestCache_ErrInvalid_RateLimit_DedupesSameKeyWithin1s(t *testing.T) {
	now := time.Unix(1000, 0)
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

	// Resolve the same bad token many times within the same second.
	for i := 0; i < 50; i++ {
		_, _ = resolver.Resolve(context.Background(), "bad-token")
	}

	// Only 1 Publish should have been made (the rest deduped).
	count := rev.count(tokenKey("bad-token"))
	require.Equal(t, 1, count, "expected exactly 1 publish for same bad key within dedupe window, got %d", count)
}

// TestCache_ErrInvalid_RateLimit_GlobalCapAcrossKeys verifies that
// invalidPublishGlobalCap is enforced across distinct keys: after
// invalidPublishGlobalCap distinct keys are published in a single window,
// additional keys are silently dropped.
func TestCache_ErrInvalid_RateLimit_GlobalCapAcrossKeys(t *testing.T) {
	now := time.Unix(2000, 0)
	// Use a delegate that always returns ErrInvalid for any token.
	delegate := resolverFunc(func(_ context.Context, token string) (Identity, error) {
		return Identity{}, ErrInvalid
	})
	rev := newCountingRevocationChannel()
	resolver := NewCache(delegate, CacheConfig{
		FreshTTL: time.Second,
		Capacity: 1000,
		Now:      func() time.Time { return now },
		Jitter:   func() float64 { return 1 },
	}, WithRevocationChannel(rev))

	// Send more distinct bad tokens than the global cap allows in one window.
	total := invalidPublishGlobalCap + 10
	for i := 0; i < total; i++ {
		token := fmt.Sprintf("bad-token-%d", i)
		_, _ = resolver.Resolve(context.Background(), token)
	}

	// Total publishes must be capped at invalidPublishGlobalCap.
	got := rev.total()
	require.LessOrEqual(t, got, invalidPublishGlobalCap,
		"expected at most %d global publishes, got %d", invalidPublishGlobalCap, got)
}

// TestCache_ErrInvalid_RateLimit_AllowsAfterWindowExpires verifies that after
// the dedupe window expires the same bad key is allowed to publish again.
func TestCache_ErrInvalid_RateLimit_AllowsAfterWindowExpires(t *testing.T) {
	now := time.Unix(3000, 0)
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

	// First request: allowed.
	_, _ = resolver.Resolve(context.Background(), "bad-token")
	require.Equal(t, 1, rev.count(tokenKey("bad-token")), "first request should publish")

	// Advance clock past dedupe window.
	now = now.Add(invalidPublishDedupeWindow + time.Millisecond)

	// Second request after window: allowed again.
	_, _ = resolver.Resolve(context.Background(), "bad-token")
	require.Equal(t, 2, rev.count(tokenKey("bad-token")), "second request after window should publish")
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
