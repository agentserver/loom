package commanderhub

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDaemonConn_LegacyEmptyShortID_FallsBackToDcID: when shortID is empty
// (legacy single-pod daemon), routingID() must return dc.id unchanged so that
// existing tests and wire protocols that hard-code dc.id continue to work.
func TestDaemonConn_LegacyEmptyShortID_FallsBackToDcID(t *testing.T) {
	dc := &daemonConn{id: "legacy-id-abc123", shortID: ""}
	require.Equal(t, "legacy-id-abc123", dc.routingID(),
		"routingID() must fall back to dc.id when shortID is empty")
}

// TestDaemonConn_RoutingIDUsesShortIDWhenSet: when a daemon registers with a
// non-empty ShortID, routingID() must return it (so the shared registry can key
// by stable identity across reconnects).
func TestDaemonConn_RoutingIDUsesShortIDWhenSet(t *testing.T) {
	dc := &daemonConn{id: "ephemeral-conn-id", shortID: "stable-short-id"}
	require.Equal(t, "stable-short-id", dc.routingID(),
		"routingID() must return shortID when set")
}

// TestLocalRegistry_RemoveIf: removeIf removes a daemon entry only when the
// predicate returns true; leaving other entries untouched.
func TestLocalRegistry_RemoveIf(t *testing.T) {
	r := newLocalRegistry()
	o := owner{userID: "alice", workspaceID: "W1"}
	dc1 := &daemonConn{id: "d1", shortID: "short1", owner: o}
	dc2 := &daemonConn{id: "d2", shortID: "short2", owner: o}

	r.add(dc1)
	r.add(dc2)

	// Remove only dc1 (routing key "short1") by id predicate.
	r.removeIf(o, "short1", func(existing *daemonConn) bool {
		return existing.id == "d1"
	})

	_, ok := r.lookup(o, "short1")
	require.False(t, ok, "short1 should be removed")

	got, ok := r.lookup(o, "short2")
	require.True(t, ok, "short2 should remain")
	require.Equal(t, dc2, got)
}

// TestLocalRegistry_RemoveIf_PredicateFalse: removeIf leaves entry intact when
// predicate returns false (stale removal race: different connection arrived).
func TestLocalRegistry_RemoveIf_PredicateFalse(t *testing.T) {
	r := newLocalRegistry()
	o := owner{userID: "alice", workspaceID: "W1"}
	dc := &daemonConn{id: "d1", shortID: "short1", owner: o}
	r.add(dc)

	// Predicate says no → should NOT remove.
	r.removeIf(o, "short1", func(existing *daemonConn) bool {
		return existing.id == "different-conn"
	})

	got, ok := r.lookup(o, "short1")
	require.True(t, ok, "entry should survive predicate=false")
	require.Equal(t, dc, got)
}

// TestDaemonInfo_DaemonIDIsRoutingID: DaemonInfo.DaemonID must expose the
// routing ID (shortID when set, dc.id otherwise) — not the raw ephemeral id.
func TestDaemonInfo_DaemonIDIsRoutingID(t *testing.T) {
	dc := &daemonConn{id: "ephemeral", shortID: "stable", displayName: "test", kind: "claude"}
	info := dc.info()
	require.Equal(t, "stable", info.DaemonID,
		"DaemonInfo.DaemonID must be routingID() (shortID when set)")

	dcLegacy := &daemonConn{id: "legacy-ephemeral", shortID: "", displayName: "test", kind: "claude"}
	infoLegacy := dcLegacy.info()
	require.Equal(t, "legacy-ephemeral", infoLegacy.DaemonID,
		"DaemonInfo.DaemonID must fall back to dc.id when shortID is empty")
}
