package identity

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// fakeRevocationChannel records Publish calls and delivers events synchronously
// via Subscribe for use in cache unit tests.
type fakeRevocationChannel struct {
	mu        sync.Mutex
	published []string
	subs      []func(string)
}

func (f *fakeRevocationChannel) Publish(_ context.Context, key string) error {
	f.mu.Lock()
	f.published = append(f.published, key)
	subs := make([]func(string), len(f.subs))
	copy(subs, f.subs)
	f.mu.Unlock()
	for _, sub := range subs {
		sub(key)
	}
	return nil
}

func (f *fakeRevocationChannel) Subscribe(_ context.Context, onRevoke func(string)) (func(), error) {
	f.mu.Lock()
	f.subs = append(f.subs, onRevoke)
	f.mu.Unlock()
	return func() {}, nil
}

func (f *fakeRevocationChannel) Published() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.published))
	copy(out, f.published)
	return out
}

// ---------------------------------------------------------------------------
// pgRevocationChannel unit tests (sqlmock)
// ---------------------------------------------------------------------------

func TestRevocationChannel_PublishInsertsRow(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	key := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	mock.ExpectExec(`INSERT INTO commander_identity_revocations (key) VALUES ($1)`).
		WithArgs(key).
		WillReturnResult(sqlmock.NewResult(1, 1))

	ch := &pgRevocationChannel{db: db}
	require.NoError(t, ch.Publish(context.Background(), key))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRevocationChannel_SubscribePollsRows(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	key1 := "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111"
	key2 := "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222"

	// Seed query: MAX(seq) = 0
	mock.ExpectQuery(`SELECT COALESCE(MAX(seq), 0) FROM commander_identity_revocations`).
		WillReturnRows(sqlmock.NewRows([]string{"coalesce"}).AddRow(0))

	// First poll: 2 rows
	firstRows := sqlmock.NewRows([]string{"seq", "key"}).
		AddRow(1, key1).
		AddRow(2, key2)
	mock.ExpectQuery(`SELECT seq, key FROM commander_identity_revocations WHERE seq > $1 ORDER BY seq`).
		WithArgs(int64(0)).
		WillReturnRows(firstRows)

	// Second poll: no new rows
	mock.ExpectQuery(`SELECT seq, key FROM commander_identity_revocations WHERE seq > $1 ORDER BY seq`).
		WithArgs(int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"seq", "key"}))

	var received []string
	var mu sync.Mutex

	ch := &pgRevocationChannel{db: db}
	ctx, cancel := context.WithCancel(context.Background())
	stop, err := ch.Subscribe(ctx, func(key string) {
		mu.Lock()
		received = append(received, key)
		mu.Unlock()
	})
	require.NoError(t, err)

	// Manually drive two poll cycles.
	lastSeq := int64(0)
	require.NoError(t, ch.poll(ctx, func(key string) {
		mu.Lock()
		received = append(received, key)
		mu.Unlock()
	}, &lastSeq))
	require.NoError(t, ch.poll(ctx, func(key string) {
		mu.Lock()
		received = append(received, key)
		mu.Unlock()
	}, &lastSeq))

	stop()
	cancel()

	mu.Lock()
	got := received
	mu.Unlock()

	require.Equal(t, []string{key1, key2}, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRevocationChannel_SubscribeRespectsCtx(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	mock.ExpectQuery(`SELECT COALESCE(MAX(seq), 0) FROM commander_identity_revocations`).
		WillReturnRows(sqlmock.NewRows([]string{"coalesce"}).AddRow(0))

	ch := &pgRevocationChannel{db: db}
	ctx, cancel := context.WithCancel(context.Background())

	stop, err := ch.Subscribe(ctx, func(string) {})
	require.NoError(t, err)

	// Cancel should cause the goroutine to exit. stop() is idempotent.
	cancel()
	stop()

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRevocationChannel_DropsOversizedKey(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	// Build a key that is exactly 257 characters (one over the limit).
	oversized := ""
	for i := 0; i < 257; i++ {
		oversized += "x"
	}
	normalKey := "cccc3333cccc3333cccc3333cccc3333cccc3333cccc3333cccc3333cccc3333"

	mock.ExpectQuery(`SELECT COALESCE(MAX(seq), 0) FROM commander_identity_revocations`).
		WillReturnRows(sqlmock.NewRows([]string{"coalesce"}).AddRow(0))

	rows := sqlmock.NewRows([]string{"seq", "key"}).
		AddRow(1, oversized).
		AddRow(2, normalKey)
	mock.ExpectQuery(`SELECT seq, key FROM commander_identity_revocations WHERE seq > $1 ORDER BY seq`).
		WithArgs(int64(0)).
		WillReturnRows(rows)

	var received []string
	ch := &pgRevocationChannel{db: db}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop, err := ch.Subscribe(ctx, func(key string) {
		received = append(received, key)
	})
	require.NoError(t, err)
	defer stop()

	lastSeq := int64(0)
	require.NoError(t, ch.poll(ctx, func(key string) {
		received = append(received, key)
	}, &lastSeq))

	require.Equal(t, []string{normalKey}, received)
	require.Equal(t, int64(1), ch.dropsOversized.Load())
	require.NoError(t, mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// Cache integration tests with fake RevocationChannel
// ---------------------------------------------------------------------------

func TestCache_WithRevocationChannel_EvictPublishes(t *testing.T) {
	fake := &fakeRevocationChannel{}
	delegate := resolverFunc(func(_ context.Context, token string) (Identity, error) {
		return Identity{WorkspaceID: "ws-1"}, ErrRevoked
	})
	resolver := NewCache(delegate, CacheConfig{
		FreshTTL:   time.Minute,
		StaleGrace: time.Minute,
		Capacity:   10,
		Now:        time.Now,
		Jitter:     func() float64 { return 1 },
	}, WithRevocationChannel(fake))

	// Resolve triggers ErrRevoked → evict → Publish.
	_, err := resolver.Resolve(context.Background(), "tok1")
	require.ErrorIs(t, err, ErrRevoked)

	published := fake.Published()
	require.Len(t, published, 1)
	// Published key must be the SHA-256 hex of "tok1" — not the token itself.
	require.Equal(t, tokenKey("tok1"), published[0])
	// Verify it is NOT the raw token (security check).
	require.NotEqual(t, "tok1", published[0])
}

func TestCache_WithRevocationChannel_RemoteRevokeEvicts(t *testing.T) {
	fake := &fakeRevocationChannel{}

	var calls atomic.Int32
	delegate := resolverFunc(func(_ context.Context, token string) (Identity, error) {
		calls.Add(1)
		return Identity{WorkspaceID: "ws-1"}, nil
	})

	now := time.Unix(100, 0)
	resolver := NewCache(delegate, CacheConfig{
		FreshTTL:   10 * time.Second,
		StaleGrace: time.Minute,
		Capacity:   10,
		Now:        func() time.Time { return now },
		Jitter:     func() float64 { return 1 },
	}, WithRevocationChannel(fake))

	// Populate cache.
	_, err := resolver.Resolve(context.Background(), "tok2")
	require.NoError(t, err)
	require.Equal(t, int32(1), calls.Load())

	// Simulate remote revocation arriving via Subscribe callback.
	key := tokenKey("tok2")
	// Deliver via the fake channel's registered subscribers.
	fake.mu.Lock()
	subs := make([]func(string), len(fake.subs))
	copy(subs, fake.subs)
	fake.mu.Unlock()
	for _, sub := range subs {
		sub(key)
	}

	// Next Resolve must hit the delegate again (cache entry gone).
	_, err = resolver.Resolve(context.Background(), "tok2")
	require.NoError(t, err)
	require.Equal(t, int32(2), calls.Load())
}

func TestCache_NoRevocationChannel_LegacyBehavior(t *testing.T) {
	now := time.Unix(100, 0)
	var calls atomic.Int32
	delegate := resolverFunc(func(context.Context, string) (Identity, error) {
		calls.Add(1)
		return Identity{WorkspaceID: "ws-legacy"}, nil
	})
	// No options → legacy single-pod path.
	resolver := NewCache(delegate, CacheConfig{
		FreshTTL:   10 * time.Second,
		StaleGrace: time.Minute,
		Capacity:   10,
		Now:        func() time.Time { return now },
		Jitter:     func() float64 { return 1 },
	})

	got, err := resolver.Resolve(context.Background(), "tok-legacy")
	require.NoError(t, err)
	require.Equal(t, "ws-legacy", got.WorkspaceID)

	// Second call returns from cache.
	got2, err := resolver.Resolve(context.Background(), "tok-legacy")
	require.NoError(t, err)
	require.Equal(t, got, got2)
	require.Equal(t, int32(1), calls.Load())
}
