package commanderhub

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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
