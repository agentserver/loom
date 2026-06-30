package commanderhub

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commander"
)

func TestRegistry_AddLookupRemove(t *testing.T) {
	r := newLocalRegistry()
	o := owner{userID: "alice", workspaceID: "W1"}
	// shortID is empty → routingID() falls back to dc.id ("d1").
	dc := &daemonConn{id: "d1", shortID: "", owner: o, displayName: "mac", kind: "claude", driverVersion: "v1"}

	r.add(dc)

	got, ok := r.lookup(o, "d1")
	require.True(t, ok)
	require.Equal(t, "mac", got.displayName)

	// 跨用户不可见
	_, ok = r.lookup(owner{userID: "bob", workspaceID: "W1"}, "d1")
	require.False(t, ok)

	// 同用户跨 workspace 不可见
	_, ok = r.lookup(owner{userID: "alice", workspaceID: "W2"}, "d1")
	require.False(t, ok)
}

func TestRegistry_DaemonsSnapshot(t *testing.T) {
	r := newLocalRegistry()
	o := owner{userID: "alice", workspaceID: "W1"}
	// shortID empty → routingID() == dc.id → keyed as "d1", "d2"
	r.add(&daemonConn{id: "d1", shortID: "", owner: o, displayName: "mac", kind: "claude", driverVersion: "v1"})
	r.add(&daemonConn{id: "d2", shortID: "", owner: o, displayName: "linux", kind: "codex", driverVersion: "v2"})

	infos := r.daemons(o)
	require.Len(t, infos, 2)
	got := map[string]DaemonInfo{}
	for _, di := range infos {
		got[di.DaemonID] = di
	}
	require.Equal(t, "claude", got["d1"].Kind)
	require.Equal(t, "codex", got["d2"].Kind)

	// 别人的 owner 快照为空
	require.Empty(t, r.daemons(owner{userID: "bob", workspaceID: "W1"}))
}

func TestRegistryDaemonInfoIncludesCapabilities(t *testing.T) {
	r := newLocalRegistry()
	o := owner{userID: "alice", workspaceID: "W1"}
	r.add(&daemonConn{
		id:           "d1",
		shortID:      "d1",
		owner:        o,
		displayName:  "prod-codex",
		kind:         "codex",
		capabilities: map[string]bool{commander.CapabilityFiles: true},
	})

	got := r.daemons(o)
	require.Len(t, got, 1)
	require.Contains(t, got[0].Capabilities, commander.CapabilityFiles)
}

func TestRegistry_RemoveCleansEmptyOwner(t *testing.T) {
	r := newLocalRegistry()
	o := owner{userID: "alice", workspaceID: "W1"}
	// shortID empty → routingID() == "d1"
	r.add(&daemonConn{id: "d1", shortID: "", owner: o})
	r.remove(o, "d1")

	_, ok := r.lookup(o, "d1")
	require.False(t, ok)
	require.Empty(t, r.daemons(o))
}
