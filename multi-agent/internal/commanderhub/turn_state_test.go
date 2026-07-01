package commanderhub

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestTurnStateStoreRejectsConcurrentTurn(t *testing.T) {
	ctx := context.Background()
	s := newMemTurnStore()
	key := turnKey{owner: owner{"alice", "W1"}, shortID: "d1", sessionID: "s1"}
	ok, err := s.begin(ctx, key)
	if err != nil || !ok {
		t.Fatal("first begin should succeed")
	}
	ok2, err2 := s.begin(ctx, key)
	if err2 != nil || ok2 {
		t.Fatal("second begin should be rejected")
	}
	_ = s.finish(ctx, key, turnStateDone)
	ok3, err3 := s.begin(ctx, key)
	if err3 != nil || !ok3 {
		t.Fatal("begin after done should succeed")
	}
}

func TestTurnStateStoreSnapshot(t *testing.T) {
	ctx := context.Background()
	s := newMemTurnStore()
	key := turnKey{owner: owner{"alice", "W1"}, shortID: "d1", sessionID: "s1"}
	_, _ = s.begin(ctx, key)
	_ = s.set(ctx, key, turnStateAnswering)
	got, _ := s.get(ctx, key)
	if got.State != turnStateAnswering || !got.InFlight {
		t.Fatalf("snapshot=%+v", got)
	}
}

func TestTurnStateStoreSetDoesNotPruneOnHotPath(t *testing.T) {
	ctx := context.Background()
	s := newMemTurnStore()
	active := turnKey{owner: owner{"alice", "W1"}, shortID: "d1", sessionID: "active"}
	oldTerminal := turnKey{owner: owner{"alice", "W1"}, shortID: "d1", sessionID: "old"}
	s.m[active] = turnSnapshot{State: turnStateQueued, InFlight: true, updatedAt: time.Now()}
	s.m[oldTerminal] = turnSnapshot{State: turnStateDone, updatedAt: time.Now().Add(-time.Hour)}
	for i := 0; i < maxTurnStateEntries-1; i++ {
		key := turnKey{owner: owner{"alice", "W1"}, shortID: "d1", sessionID: fmt.Sprintf("extra-%d", i)}
		s.m[key] = turnSnapshot{State: turnStateDone, updatedAt: time.Now()}
	}

	_ = s.set(ctx, active, turnStateAnswering)

	if got, _ := s.get(ctx, oldTerminal); got.State != turnStateDone {
		t.Fatalf("set pruned terminal state on chunk hot path, got %+v", got)
	}
}

func TestTurnStateStorePrunesTerminalStatesPreservingInFlight(t *testing.T) {
	ctx := context.Background()
	s := newMemTurnStore()
	inFlight := turnKey{owner: owner{"alice", "W1"}, shortID: "d1", sessionID: "active"}
	if ok, _ := s.begin(ctx, inFlight); !ok {
		t.Fatal("in-flight begin should succeed")
	}

	var firstTerminal turnKey
	for i := 0; i < maxTurnStateEntries-1; i++ {
		key := turnKey{owner: owner{"alice", "W1"}, shortID: "d1", sessionID: fmt.Sprintf("done-%d", i)}
		if i == 0 {
			firstTerminal = key
		}
		if ok, _ := s.begin(ctx, key); !ok {
			t.Fatalf("begin terminal %d should succeed", i)
		}
		_ = s.finish(ctx, key, turnStateDone)
	}
	s.mu.Lock()
	snap := s.m[firstTerminal]
	snap.updatedAt = time.Now().Add(-time.Hour)
	s.m[firstTerminal] = snap
	s.mu.Unlock()
	latestTerminal := turnKey{owner: owner{"alice", "W1"}, shortID: "d1", sessionID: "latest"}
	if ok, _ := s.begin(ctx, latestTerminal); !ok {
		t.Fatal("latest terminal begin should succeed")
	}
	_ = s.finish(ctx, latestTerminal, turnStateDone)

	if got, _ := s.get(ctx, inFlight); got.State != turnStateQueued || !got.InFlight {
		t.Fatalf("in-flight snapshot pruned or changed: %+v", got)
	}
	if got, _ := s.get(ctx, firstTerminal); got.State != turnStateIdle || got.InFlight {
		t.Fatalf("oldest terminal should be pruned, got %+v", got)
	}
	if got, _ := s.get(ctx, latestTerminal); got.State != turnStateDone || got.InFlight {
		t.Fatalf("latest terminal should remain, got %+v", got)
	}
	s.mu.Lock()
	gotLen := len(s.m)
	s.mu.Unlock()
	if gotLen > maxTurnStateEntries {
		t.Fatalf("store len=%d, want <= %d", gotLen, maxTurnStateEntries)
	}
}
