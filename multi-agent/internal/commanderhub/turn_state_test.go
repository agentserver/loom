package commanderhub

import (
	"fmt"
	"testing"
	"time"
)

func TestTurnStateStoreRejectsConcurrentTurn(t *testing.T) {
	s := newMemTurnStore()
	key := turnKey{owner: owner{"alice", "W1"}, shortID: "d1", sessionID: "s1"}
	if !s.begin(key) {
		t.Fatal("first begin should succeed")
	}
	if s.begin(key) {
		t.Fatal("second begin should be rejected")
	}
	s.finish(key, turnStateDone)
	if !s.begin(key) {
		t.Fatal("begin after done should succeed")
	}
}

func TestTurnStateStoreSnapshot(t *testing.T) {
	s := newMemTurnStore()
	key := turnKey{owner: owner{"alice", "W1"}, shortID: "d1", sessionID: "s1"}
	s.begin(key)
	s.set(key, turnStateAnswering)
	got := s.get(key)
	if got.State != turnStateAnswering || !got.InFlight {
		t.Fatalf("snapshot=%+v", got)
	}
}

func TestTurnStateStoreSetDoesNotPruneOnHotPath(t *testing.T) {
	s := newMemTurnStore()
	active := turnKey{owner: owner{"alice", "W1"}, shortID: "d1", sessionID: "active"}
	oldTerminal := turnKey{owner: owner{"alice", "W1"}, shortID: "d1", sessionID: "old"}
	s.m[active] = turnSnapshot{State: turnStateQueued, InFlight: true, updatedAt: time.Now()}
	s.m[oldTerminal] = turnSnapshot{State: turnStateDone, updatedAt: time.Now().Add(-time.Hour)}
	for i := 0; i < maxTurnStateEntries-1; i++ {
		key := turnKey{owner: owner{"alice", "W1"}, shortID: "d1", sessionID: fmt.Sprintf("extra-%d", i)}
		s.m[key] = turnSnapshot{State: turnStateDone, updatedAt: time.Now()}
	}

	s.set(active, turnStateAnswering)

	if got := s.get(oldTerminal); got.State != turnStateDone {
		t.Fatalf("set pruned terminal state on chunk hot path, got %+v", got)
	}
}

func TestTurnStateStorePrunesTerminalStatesPreservingInFlight(t *testing.T) {
	s := newMemTurnStore()
	inFlight := turnKey{owner: owner{"alice", "W1"}, shortID: "d1", sessionID: "active"}
	if !s.begin(inFlight) {
		t.Fatal("in-flight begin should succeed")
	}

	var firstTerminal turnKey
	for i := 0; i < maxTurnStateEntries-1; i++ {
		key := turnKey{owner: owner{"alice", "W1"}, shortID: "d1", sessionID: fmt.Sprintf("done-%d", i)}
		if i == 0 {
			firstTerminal = key
		}
		if !s.begin(key) {
			t.Fatalf("begin terminal %d should succeed", i)
		}
		s.finish(key, turnStateDone)
	}
	s.mu.Lock()
	snap := s.m[firstTerminal]
	snap.updatedAt = time.Now().Add(-time.Hour)
	s.m[firstTerminal] = snap
	s.mu.Unlock()
	latestTerminal := turnKey{owner: owner{"alice", "W1"}, shortID: "d1", sessionID: "latest"}
	if !s.begin(latestTerminal) {
		t.Fatal("latest terminal begin should succeed")
	}
	s.finish(latestTerminal, turnStateDone)

	if got := s.get(inFlight); got.State != turnStateQueued || !got.InFlight {
		t.Fatalf("in-flight snapshot pruned or changed: %+v", got)
	}
	if got := s.get(firstTerminal); got.State != turnStateIdle || got.InFlight {
		t.Fatalf("oldest terminal should be pruned, got %+v", got)
	}
	if got := s.get(latestTerminal); got.State != turnStateDone || got.InFlight {
		t.Fatalf("latest terminal should remain, got %+v", got)
	}
	s.mu.Lock()
	gotLen := len(s.m)
	s.mu.Unlock()
	if gotLen > maxTurnStateEntries {
		t.Fatalf("store len=%d, want <= %d", gotLen, maxTurnStateEntries)
	}
}
