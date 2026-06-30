package commanderhub

import (
	"context"
	"sync"
	"time"

	"github.com/yourorg/multi-agent/internal/commander"
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
	shortID   string
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

// turnStateBackend is the storage interface for turn state. The in-process
// implementation is *memTurnStore; Phase D will add a *pgTurnStore that
// persists state across pod restarts.
type turnStateBackend interface {
	begin(ctx context.Context, key turnKey) (bool, error)
	set(ctx context.Context, key turnKey, state turnState) error
	finish(ctx context.Context, key turnKey, state turnState) error
	fail(ctx context.Context, key turnKey, msg string) error
	rekey(ctx context.Context, oldKey, newKey turnKey) error
	get(ctx context.Context, key turnKey) (turnSnapshot, error)
	// updateFromEnvelope persists envelope-derived state changes in backends
	// that require it (e.g. pgTurnStore). memTurnStore is a no-op because
	// the callers in http.go call begin/set/finish/fail directly.
	updateFromEnvelope(ctx context.Context, key turnKey, command string, env commander.Envelope) error
	// cleanupOrphans removes turn-state entries older than the given duration
	// whose associated daemon is no longer connected. Used by the periodic
	// sweeper in Phase D. memTurnStore is a no-op (in-memory state evicts
	// itself via pruneLocked).
	cleanupOrphans(ctx context.Context, older time.Duration) error
}

type memTurnStore struct {
	mu sync.Mutex
	m  map[turnKey]turnSnapshot
}

func newMemTurnStore() *memTurnStore {
	return &memTurnStore{m: make(map[turnKey]turnSnapshot)}
}

func (s *memTurnStore) begin(_ context.Context, key turnKey) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.m[key]
	if cur.InFlight {
		return false, nil
	}
	s.m[key] = turnSnapshot{State: turnStateQueued, InFlight: true, updatedAt: time.Now()}
	s.pruneLocked()
	return true, nil
}

func (s *memTurnStore) set(_ context.Context, key turnKey, state turnState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.m[key]
	cur.State = state
	cur.InFlight = state == turnStateQueued || state == turnStateAnswering
	cur.updatedAt = time.Now()
	s.m[key] = cur
	return nil
}

func (s *memTurnStore) finish(_ context.Context, key turnKey, state turnState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.m[key]
	cur.State = state
	cur.InFlight = false
	cur.AwaitingApproval = state == turnStateAwaitingApproval
	cur.updatedAt = time.Now()
	s.m[key] = cur
	s.pruneLocked()
	return nil
}

func (s *memTurnStore) fail(_ context.Context, key turnKey, msg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.m[key]
	cur.State = turnStateError
	cur.InFlight = false
	cur.Message = msg
	cur.updatedAt = time.Now()
	s.m[key] = cur
	s.pruneLocked()
	return nil
}

// rekey migrates an in-flight entry from oldKey to newKey, used when the
// fresh-session protocol returns the real backend session ID in the
// terminal command_result payload. Idempotent: when oldKey has no entry,
// this is a no-op; when newKey already exists, the existing entry is
// preserved (the caller's subsequent finish/fail then writes the
// terminal state under newKey).
func (s *memTurnStore) rekey(_ context.Context, oldKey, newKey turnKey) error {
	if oldKey == newKey {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.m[oldKey]
	if !ok {
		return nil
	}
	delete(s.m, oldKey)
	if _, exists := s.m[newKey]; !exists {
		cur.updatedAt = time.Now()
		s.m[newKey] = cur
	}
	return nil
}

func (s *memTurnStore) get(_ context.Context, key turnKey) (turnSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if snap, ok := s.m[key]; ok {
		return snap, nil
	}
	return turnSnapshot{State: turnStateIdle}, nil
}

// updateFromEnvelope is a no-op for memTurnStore. Phase D's pgTurnStore
// will use this to persist envelope-derived state changes.
func (s *memTurnStore) updateFromEnvelope(_ context.Context, _ turnKey, _ string, _ commander.Envelope) error {
	return nil
}

// cleanupOrphans is a no-op for memTurnStore. In-memory state is bounded
// by pruneLocked; pgTurnStore will implement periodic SQL cleanup here.
func (s *memTurnStore) cleanupOrphans(_ context.Context, _ time.Duration) error {
	return nil
}

func (s *memTurnStore) pruneLocked() {
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

// snapshotForTest returns the raw map entry for key. Only for use in tests
// that need to inspect or manipulate internal state directly. Not for
// production use.
func (s *memTurnStore) snapshotForTest(key turnKey) (turnSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, ok := s.m[key]
	return snap, ok
}

// setForTest writes snap directly into the map under key. Only for use in
// tests that need to pre-populate state without going through begin/set/finish.
// Not for production use.
func (s *memTurnStore) setForTest(key turnKey, snap turnSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = snap
}
