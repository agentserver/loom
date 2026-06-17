package commanderhub

import (
	"sync"
	"time"
)

const maxTurnStateEntries = 1024

type turnState string

const (
	turnStateIdle             turnState = "idle"
	turnStateQueued           turnState = "queued"
	turnStateAnswering        turnState = "answering"
	turnStateDone             turnState = "done"
	turnStateError            turnState = "error"
	turnStateAwaitingApproval turnState = "awaiting_approval"
	turnStateDisconnected     turnState = "disconnected"
)

type turnKey struct {
	owner     owner
	daemonID  string
	sessionID string
}

type turnSnapshot struct {
	State            turnState `json:"turn_state"`
	InFlight         bool      `json:"-"`
	AwaitingApproval bool      `json:"awaiting_approval"`
	ActiveWorker     bool      `json:"active_worker"`
	Message          string    `json:"turn_message,omitempty"`
	updatedAt        time.Time
}

type turnStateStore struct {
	mu sync.Mutex
	m  map[turnKey]turnSnapshot
}

func newTurnStateStore() *turnStateStore {
	return &turnStateStore{m: make(map[turnKey]turnSnapshot)}
}

func (s *turnStateStore) begin(key turnKey) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.m[key]
	if cur.InFlight {
		return false
	}
	s.m[key] = turnSnapshot{State: turnStateQueued, InFlight: true, updatedAt: time.Now()}
	s.pruneLocked()
	return true
}

func (s *turnStateStore) set(key turnKey, state turnState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.m[key]
	cur.State = state
	cur.InFlight = state == turnStateQueued || state == turnStateAnswering
	cur.updatedAt = time.Now()
	s.m[key] = cur
}

func (s *turnStateStore) finish(key turnKey, state turnState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.m[key]
	cur.State = state
	cur.InFlight = false
	cur.AwaitingApproval = state == turnStateAwaitingApproval
	cur.updatedAt = time.Now()
	s.m[key] = cur
	s.pruneLocked()
}

func (s *turnStateStore) fail(key turnKey, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.m[key]
	cur.State = turnStateError
	cur.InFlight = false
	cur.Message = msg
	cur.updatedAt = time.Now()
	s.m[key] = cur
	s.pruneLocked()
}

func (s *turnStateStore) get(key turnKey) turnSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if snap, ok := s.m[key]; ok {
		return snap
	}
	return turnSnapshot{State: turnStateIdle}
}

func (s *turnStateStore) pruneLocked() {
	for len(s.m) > maxTurnStateEntries {
		var oldestKey turnKey
		var oldest turnSnapshot
		found := false
		for key, snap := range s.m {
			if snap.InFlight {
				continue
			}
			if !found || snap.updatedAt.Before(oldest.updatedAt) {
				oldestKey = key
				oldest = snap
				found = true
			}
		}
		if !found {
			return
		}
		delete(s.m, oldestKey)
	}
}
