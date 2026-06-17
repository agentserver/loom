package commanderhub

import (
	"context"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commander"
	"github.com/yourorg/multi-agent/internal/identity"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestSessionTitleFallback(t *testing.T) {
	cases := []struct {
		name    string
		title   string
		preview string
		id      string
		want    string
	}{
		{name: "title", title: "  first\n\tprompt  ", preview: "preview", id: "abcdef123456", want: "first prompt"},
		{name: "preview", preview: "  recent\nanswer  ", id: "abcdef123456", want: "recent answer"},
		{name: "id", id: "abcdef123456", want: "abcdef12"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sessionTitle(tc.title, tc.preview, tc.id)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestSortSessionRowsNewestFirst(t *testing.T) {
	now := time.Now()
	rows := []SessionRow{
		{SessionID: "old", UpdatedAt: now.Add(-time.Hour)},
		{SessionID: "new", UpdatedAt: now},
	}

	sortSessionRows(rows)

	require.Equal(t, "new", rows[0].SessionID)
	require.Equal(t, "old", rows[1].SessionID)
}

func TestSessionRowFromBackendMergesTurnState(t *testing.T) {
	updatedAt := time.Now()
	row := sessionRowFromBackend("d1", agentbackend.Session{
		ID:           "s1",
		Kind:         agentbackend.KindCodex,
		WorkingDir:   "/repo",
		Title:        "investigate",
		Origin:       agentbackend.SessionOriginSubagent,
		ParentID:     "parent-1",
		AgentName:    "Lovelace",
		AgentRole:    "reviewer",
		UpdatedAt:    updatedAt,
		MessageCount: 7,
		Preview:      "latest answer",
	}, turnSnapshot{
		State:            turnStateAnswering,
		ActiveWorker:     true,
		AwaitingApproval: true,
	})

	require.Equal(t, "d1", row.DaemonID)
	require.Equal(t, "s1", row.SessionID)
	require.Equal(t, "codex", row.Kind)
	require.Equal(t, "investigate", row.Title)
	require.Equal(t, "subagent", row.Origin)
	require.Equal(t, "parent-1", row.ParentID)
	require.Equal(t, "Lovelace", row.AgentName)
	require.Equal(t, "reviewer", row.AgentRole)
	require.Equal(t, "/repo", row.WorkingDir)
	require.Equal(t, updatedAt, row.UpdatedAt)
	require.Equal(t, 7, row.MessageCount)
	require.Equal(t, "latest answer", row.Preview)
	require.Equal(t, "answering", row.TurnState)
	require.True(t, row.ActiveWorker)
	require.True(t, row.AwaitingApproval)
}

func TestSessionRowFromBackendDefaultsIdleTurnState(t *testing.T) {
	row := sessionRowFromBackend("d1", agentbackend.Session{ID: "s1"}, turnSnapshot{})

	require.Equal(t, "idle", row.TurnState)
}

func TestCachedSessionRowsUsesTTLAndCopiesSlices(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	var mu sync.Mutex
	var calls int
	hub, _, o, cleanup := dialFakeDaemon(t, resolver, "tok-alice", &tbBackend{
		listFn: func(context.Context) ([]agentbackend.Session, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			return []agentbackend.Session{{
				ID:        "s1",
				Kind:      agentbackend.KindClaude,
				Title:     "first",
				UpdatedAt: time.Unix(10, 0),
			}}, nil
		},
	})
	defer cleanup()
	info := hub.reg.daemons(o)[0]

	first, err := hub.cachedSessionRows(context.Background(), o, info)
	require.NoError(t, err)
	first[0].Title = "mutated"

	second, err := hub.cachedSessionRows(context.Background(), o, info)
	require.NoError(t, err)
	require.Equal(t, "first", second[0].Title)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, calls)
}

func TestCachedSessionRowsDoesNotStoreRefreshAfterInvalidation(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var firstOnce sync.Once
	var mu sync.Mutex
	var calls int
	hub, _, o, cleanup := dialFakeDaemon(t, resolver, "tok-alice", &tbBackend{
		listFn: func(ctx context.Context) ([]agentbackend.Session, error) {
			mu.Lock()
			calls++
			call := calls
			mu.Unlock()
			if call == 1 {
				firstOnce.Do(func() { close(firstStarted) })
				select {
				case <-releaseFirst:
					return []agentbackend.Session{{ID: "s1", Kind: agentbackend.KindClaude, Title: "stale"}}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return []agentbackend.Session{{ID: "s1", Kind: agentbackend.KindClaude, Title: "fresh"}}, nil
		},
	})
	defer cleanup()
	info := hub.reg.daemons(o)[0]

	type cacheResult struct {
		rows []SessionRow
		err  error
	}
	resultCh := make(chan cacheResult, 1)
	go func() {
		rows, err := hub.cachedSessionRows(context.Background(), o, info)
		resultCh <- cacheResult{rows: rows, err: err}
	}()

	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first refresh did not start")
	}
	hub.invalidateDaemonSessions(o, info.DaemonID)
	close(releaseFirst)

	first := <-resultCh
	require.NoError(t, first.err)
	require.Equal(t, "stale", first.rows[0].Title)

	second, err := hub.cachedSessionRows(context.Background(), o, info)
	require.NoError(t, err)
	require.Equal(t, "fresh", second[0].Title)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 2, calls)
}

func TestDaemonDisconnectInvalidatesSessionCache(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	hub, _, o, cleanup := dialFakeDaemon(t, resolver, "tok-alice", &tbBackend{
		listFn: func(context.Context) ([]agentbackend.Session, error) {
			return []agentbackend.Session{{ID: "s1", Kind: agentbackend.KindClaude, Title: "cached"}}, nil
		},
	})
	info := hub.reg.daemons(o)[0]

	_, err := hub.cachedSessionRows(context.Background(), o, info)
	require.NoError(t, err)
	require.True(t, sessionCacheHasEntry(hub, o, info.DaemonID))

	cleanup()

	waitFor(t, func() bool {
		return !sessionCacheHasEntry(hub, o, info.DaemonID)
	}, time.Second, "session cache invalidated after daemon disconnect")
}

func TestCommanderTreeRefreshesDaemonsConcurrently(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	hub := NewHub(resolver)
	srv := httptest.NewServer(hub)
	defer srv.Close()

	slowCleanup := startTreeTestDaemon(t, srv, "tok-alice", "slow", &tbBackend{
		listFn: func(ctx context.Context) ([]agentbackend.Session, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	defer slowCleanup()
	fastCleanup := startTreeTestDaemon(t, srv, "tok-alice", "fast", &tbBackend{
		listFn: func(context.Context) ([]agentbackend.Session, error) {
			return []agentbackend.Session{{ID: "fast-session", Kind: agentbackend.KindClaude, Title: "fast"}}, nil
		},
	})
	defer fastCleanup()

	o := owner{userID: "alice", workspaceID: "W1"}
	waitFor(t, func() bool { return len(hub.reg.daemons(o)) == 2 }, time.Second, "two daemons linked")
	infos := hub.reg.daemons(o)
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].DisplayName == "slow" && infos[j].DisplayName != "slow"
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	tree := hub.commanderTreeForInfos(ctx, o, infos)

	byName := map[string]DaemonTree{}
	for _, daemon := range tree.Daemons {
		byName[daemon.DisplayName] = daemon
	}
	require.Equal(t, "ok", byName["fast"].Status)
	require.Len(t, byName["fast"].Sessions, 1)
	require.Equal(t, "fast-session", byName["fast"].Sessions[0].SessionID)
	require.NotEqual(t, "ok", byName["slow"].Status)
	require.NotEmpty(t, byName["slow"].Error)
}

func sessionCacheHasEntry(hub *Hub, o owner, daemonID string) bool {
	hub.sessionCache.mu.Lock()
	defer hub.sessionCache.mu.Unlock()
	_, ok := hub.sessionCache.entries[cacheKey{owner: o, daemonID: daemonID}]
	return ok
}

func startTreeTestDaemon(t *testing.T, srv *httptest.Server, token, displayName string, backend agentbackend.Backend) func() {
	t.Helper()
	wsURL := "ws" + srv.URL[len("http"):] + "/api/daemon-link"
	c := commander.NewWSClient(commander.WSConfig{
		URL:            wsURL,
		ProxyToken:     token,
		Register:       commander.RegisterPayload{SchemaVersion: commander.SchemaVersion, Kind: "claude", DisplayName: displayName},
		Handler:        &commander.Handler{Backend: backend},
		HeartbeatInt:   10 * time.Second,
		InitialBackoff: 50 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()
	waitFor(t, func() bool { return c.Linked() }, time.Second, displayName+" linked")
	return func() {
		cancel()
		<-errCh
	}
}
