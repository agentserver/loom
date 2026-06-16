package commanderhub

import "testing"

func TestTurnStateStoreRejectsConcurrentTurn(t *testing.T) {
	s := newTurnStateStore()
	key := turnKey{owner: owner{"alice", "W1"}, daemonID: "d1", sessionID: "s1"}
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
	s := newTurnStateStore()
	key := turnKey{owner: owner{"alice", "W1"}, daemonID: "d1", sessionID: "s1"}
	s.begin(key)
	s.set(key, turnStateAnswering)
	got := s.get(key)
	if got.State != turnStateAnswering || !got.InFlight {
		t.Fatalf("snapshot=%+v", got)
	}
}
