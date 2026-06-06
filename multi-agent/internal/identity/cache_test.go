package identity

import (
	"context"
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
