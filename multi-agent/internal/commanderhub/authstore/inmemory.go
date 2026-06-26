package authstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/yourorg/multi-agent/internal/identity"
)

// inmemoryStore is the single-pod fallback Store used for dev/sqlite
// observer-server deployments and for unit testing. Production (Postgres
// driver) constructs NewPostgresStore instead.
//
// All state lives in two maps guarded by one sync.Mutex; every method takes
// the lock once at entry, mirroring the postgres.go single-statement /
// single-tx semantics. There are no goroutines.
type inmemoryStore struct {
	mu       sync.Mutex
	logins   map[string]*loginRow
	sessions map[string]*sessionRow
}

type loginRow struct {
	loginID         string
	deviceCode      string
	codeExpiresAt   time.Time
	intervalSeconds int
	nextPollAt      time.Time
	createdAt       time.Time
	expiresAt       time.Time
	sessionIDHash   string
	failure         Failure
	finalizedAt     time.Time
}

type sessionRow struct {
	sessionIDHash string
	userID        string
	workspaceID   string
	role          string
	source        string
	expiresAt     time.Time
	createdAt     time.Time
}

// NewInMemoryStore builds a Store backed by in-process maps. Suitable for
// dev/sqlite observer-server (single pod) and for unit tests of the Store
// interface. Production must use NewPostgresStore.
func NewInMemoryStore() Store {
	return &inmemoryStore{
		logins:   make(map[string]*loginRow),
		sessions: make(map[string]*sessionRow),
	}
}

// hashSID is the single source of truth for sid → DB lookup key. Identical
// to postgres.go to keep cross-implementation behavior aligned.
func hashSID(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// snapshot copies a loginRow to a LoginRecord while holding the mutex,
// keeping the public view detached from internal map memory.
func (r *loginRow) snapshot() LoginRecord {
	return LoginRecord{
		LoginID:         r.loginID,
		DeviceCode:      r.deviceCode,
		CodeExpiresAt:   r.codeExpiresAt,
		IntervalSeconds: r.intervalSeconds,
		NextPollAt:      r.nextPollAt,
		ExpiresAt:       r.expiresAt,
		SessionIDHash:   r.sessionIDHash,
		Failure:         r.failure,
	}
}

func (s *inmemoryStore) ReserveLogin(_ context.Context, loginID string, now time.Time, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Lazy sweep: drop expired before counting (matches postgres.go behavior).
	for k, r := range s.logins {
		if !r.expiresAt.After(now) {
			delete(s.logins, k)
		}
	}
	if len(s.logins) >= MaxActiveLogins {
		return ErrCapped
	}
	if _, exists := s.logins[loginID]; exists {
		// Bug in caller — randomID collision is ~0. Defensive: refuse.
		return ErrCapped
	}
	s.logins[loginID] = &loginRow{
		loginID:         loginID,
		createdAt:       now,
		expiresAt:       now.Add(ttl),
		intervalSeconds: MinIntervalSeconds, // sane default until FinalizeReservedLogin
		nextPollAt:      now,
	}
	return nil
}

func (s *inmemoryStore) FinalizeReservedLogin(_ context.Context, loginID string,
	deviceCode string, codeExpiresAt time.Time, intervalSeconds int) error {

	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.logins[loginID]
	if !ok {
		return ErrNotFound
	}
	// Must be in reserved state (device_code == "" guards postgres UPDATE WHERE).
	if r.deviceCode != "" {
		return ErrNotFound
	}
	r.deviceCode = deviceCode
	r.codeExpiresAt = codeExpiresAt
	r.intervalSeconds = ClampIntervalSeconds(intervalSeconds)
	return nil
}

func (s *inmemoryStore) DeleteLogin(_ context.Context, loginID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.logins, loginID)
	return nil
}

func (s *inmemoryStore) GetLogin(_ context.Context, loginID string) (LoginRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.logins[loginID]
	if !ok {
		return LoginRecord{}, ErrNotFound
	}
	return r.snapshot(), nil
}

func (s *inmemoryStore) SetPollThrottle(_ context.Context, loginID string,
	intervalSeconds int, nextPollAt time.Time) error {

	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.logins[loginID]
	if !ok {
		return nil // best-effort; not found is not a hard failure for throttling
	}
	r.intervalSeconds = ClampIntervalSeconds(intervalSeconds)
	r.nextPollAt = nextPollAt
	return nil
}

func (s *inmemoryStore) MarkLoginDone(_ context.Context, loginID string, sess SessionRecord) error {
	hash := hashSID(sess.PlaintextSessionID)
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.logins[loginID]
	if !ok {
		return ErrNotFound
	}
	// Mirror postgres WHERE: device_code != '' AND failure IS NULL AND
	// session_id_hash IS NULL AND expires_at > now.
	if r.deviceCode == "" || r.sessionIDHash != "" || r.failure != "" || !r.expiresAt.After(now) {
		return ErrNotFound
	}

	// First-writer-wins: write both rows atomically under the lock.
	r.sessionIDHash = hash
	r.finalizedAt = now
	s.sessions[hash] = &sessionRow{
		sessionIDHash: hash,
		userID:        sess.Identity.UserID,
		workspaceID:   sess.Identity.WorkspaceID,
		role:          sess.Identity.Role,
		source:        string(sess.Identity.Source),
		expiresAt:     sess.ExpiresAt,
		createdAt:     now,
	}
	return nil
}

func (s *inmemoryStore) MarkLoginFailed(_ context.Context, loginID string, sanitized Failure) error {
	if !ValidFailure(sanitized) {
		return ErrInvalidFailure
	}
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.logins[loginID]
	if !ok {
		return ErrNotFound
	}
	if r.sessionIDHash != "" || r.failure != "" || !r.expiresAt.After(now) {
		return ErrNotFound
	}
	r.failure = sanitized
	r.finalizedAt = now
	return nil
}

func (s *inmemoryStore) ConsumeLogin(_ context.Context, loginID string) (LoginRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.logins[loginID]
	if !ok {
		return LoginRecord{}, ErrNotFound
	}
	rec := r.snapshot()
	delete(s.logins, loginID)
	return rec, nil
}

func (s *inmemoryStore) GetSession(_ context.Context, plaintext string) (SessionRecord, error) {
	hash := hashSID(plaintext)
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.sessions[hash]
	if !ok {
		return SessionRecord{}, ErrNotFound
	}
	if !r.expiresAt.After(now) {
		// Expired: clean up while we hold the lock (cheap) and report missing.
		delete(s.sessions, hash)
		return SessionRecord{}, ErrNotFound
	}
	return SessionRecord{
		PlaintextSessionID: "", // never reveal; in-flight only
		Identity: identity.Identity{
			UserID:      r.userID,
			WorkspaceID: r.workspaceID,
			Role:        r.role,
			Source:      r.source,
		},
		ExpiresAt: r.expiresAt,
	}, nil
}

func (s *inmemoryStore) DeleteSession(_ context.Context, plaintext string) error {
	hash := hashSID(plaintext)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, hash)
	return nil
}

func (s *inmemoryStore) SweepExpired(_ context.Context) (int64, int64, error) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	var logins, sessions int64
	for k, r := range s.logins {
		if !r.expiresAt.After(now) {
			delete(s.logins, k)
			logins++
		}
	}
	for k, r := range s.sessions {
		if !r.expiresAt.After(now) {
			delete(s.sessions, k)
			sessions++
		}
	}
	return logins, sessions, nil
}
