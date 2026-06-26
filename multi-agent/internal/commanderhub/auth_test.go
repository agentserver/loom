package commanderhub

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commanderhub/authstore"
	"github.com/yourorg/multi-agent/internal/identity"
)

// fakeDeviceFlow is the synchronous PollOnce-based test double.
//
// Default behavior: PollOnce returns retryable=true (i.e. authorization_pending)
// until approve()/deny()/idTokenInvalid() is called.
type fakeDeviceFlow struct {
	mu            sync.Mutex
	idToken       string
	accessToken   string
	state         pollState
	requestCodeN  int32
	requestCodeFn func() (DeviceCode, error)
	pollOnceN     int32
}

type pollState int

const (
	pollStatePending pollState = iota
	pollStateApproved
	pollStateDenied
	pollStateExpired
	pollStateSlowDown
)

func newFakeDeviceFlow(idTokenSubject, workspaceID string) *fakeDeviceFlow {
	return &fakeDeviceFlow{
		idToken:     makeIDToken(idTokenSubject, workspaceID, ""),
		accessToken: "oauth-access-token",
	}
}

func (f *fakeDeviceFlow) approve()  { f.mu.Lock(); f.state = pollStateApproved; f.mu.Unlock() }
func (f *fakeDeviceFlow) deny()     { f.mu.Lock(); f.state = pollStateDenied; f.mu.Unlock() }
func (f *fakeDeviceFlow) expire()   { f.mu.Lock(); f.state = pollStateExpired; f.mu.Unlock() }
func (f *fakeDeviceFlow) slowDown() { f.mu.Lock(); f.state = pollStateSlowDown; f.mu.Unlock() }
func (f *fakeDeviceFlow) setIDToken(s string) {
	f.mu.Lock()
	f.idToken = s
	f.mu.Unlock()
}

func (f *fakeDeviceFlow) RequestCode(context.Context) (DeviceCode, error) {
	atomic.AddInt32(&f.requestCodeN, 1)
	if f.requestCodeFn != nil {
		return f.requestCodeFn()
	}
	return DeviceCode{
		Code:                    "dc-fake",
		VerificationURIComplete: "https://agent.example/verify?user_code=XX",
		ExpiresIn:               5 * time.Minute,
		Interval:                5 * time.Second,
	}, nil
}

func (f *fakeDeviceFlow) PollOnce(ctx context.Context, _ DeviceCode) (
	loginToken, bool, bool, bool, error,
) {
	atomic.AddInt32(&f.pollOnceN, 1)
	f.mu.Lock()
	st := f.state
	tok := loginToken{AccessToken: f.accessToken, IDToken: f.idToken}
	f.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return loginToken{}, false, false, false, ctx.Err()
	}
	switch st {
	case pollStateApproved:
		return tok, true, false, false, nil
	case pollStateDenied:
		return loginToken{}, false, false, false, authstore.ErrAuthorizationDenied
	case pollStateExpired:
		return loginToken{}, false, false, false, authstore.ErrAuthorizationExpired
	case pollStateSlowDown:
		return loginToken{}, false, true, true, nil
	default: // pollStatePending
		return loginToken{}, false, true, false, nil
	}
}

func newAuth(t *testing.T, resolver identity.Resolver, flow deviceFlow) (*Authenticator, authstore.Store) {
	t.Helper()
	store := authstore.NewInMemoryStore()
	return newAuthenticatorWithFlow(resolver, flow, store), store
}

// startLogin POSTs /login and returns (login_id, verify_url, status_code).
// Test helper.
func startLogin(t *testing.T, a *Authenticator) (string, string, int) {
	t.Helper()
	rec := httptest.NewRecorder()
	a.ServeLogin(rec, httptest.NewRequest(http.MethodPost, "/api/commander/login", nil))
	if rec.Code != http.StatusOK {
		return "", "", rec.Code
	}
	var lr struct {
		LoginID                 string `json:"login_id"`
		VerificationURIComplete string `json:"verification_uri_complete"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &lr))
	return lr.LoginID, lr.VerificationURIComplete, rec.Code
}

// pollLogin issues a single GET /poll on the given lid and returns the
// recorder. options control TLS / forwarded proto.
type pollOpts struct {
	useTLS         bool
	forwardedProto string
}

func pollLogin(a *Authenticator, lid string, opts pollOpts) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/commander/login/poll?id="+lid, nil)
	if opts.useTLS {
		req.TLS = &tls.ConnectionState{}
	}
	if opts.forwardedProto != "" {
		req.Header.Set("X-Forwarded-Proto", opts.forwardedProto)
	}
	rec := httptest.NewRecorder()
	a.ServeLoginPoll(rec, req)
	return rec
}

// --- core flow ---------------------------------------------------------------

func TestAuth_LoginPoll_PendingThenApprovedSetsCookie(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{}}
	flow := newFakeDeviceFlow("alice", "W1")
	a, store := newAuth(t, resolver, flow)

	lid, verify, code := startLogin(t, a)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "https://agent.example/verify?user_code=XX", verify)
	require.NotEmpty(t, lid)

	// First poll: PollOnce returns retryable=true (pending), no cookie.
	rec := pollLogin(a, lid, pollOpts{})
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"pending"`)
	require.Empty(t, rec.Result().Cookies())

	// Now throttle says the row has next_poll_at in the future; another poll
	// would short-circuit. Bypass by clearing it.
	require.NoError(t, store.SetPollThrottle(context.Background(), lid, 5, time.Now()))

	flow.approve()
	rec2 := pollLogin(a, lid, pollOpts{})
	require.Equal(t, http.StatusOK, rec2.Code, "approved poll must be 200 ok")
	require.Contains(t, rec2.Body.String(), `"ok"`)
	cookies := rec2.Result().Cookies()
	require.Len(t, cookies, 1)
	cookie := cookies[0]
	require.Equal(t, sessionCookieName, cookie.Name)
	require.True(t, cookie.HttpOnly)
	require.Equal(t, http.SameSiteLaxMode, cookie.SameSite)
	require.False(t, cookie.Secure, "plain http must not set Secure")
	require.NotEmpty(t, cookie.Value)

	// Cookie immediately authenticates against the same Authenticator.
	req := httptest.NewRequest(http.MethodGet, "/api/commander/daemons", nil)
	req.AddCookie(cookie)
	ident, ok := a.CommanderIdentity(req)
	require.True(t, ok)
	require.Equal(t, "alice", ident.UserID)
	require.Equal(t, "W1", ident.WorkspaceID)
	require.Equal(t, identity.SourceAgentserver, ident.Source)
}

func TestAuth_LoginPoll_ReplayAfterDoneReturns401AuthorizationExpired(t *testing.T) {
	a, store := newAuth(t, &fakeResolver{}, newFakeDeviceFlow("alice", "W1"))
	lid, _, _ := startLogin(t, a)

	require.NoError(t, store.SetPollThrottle(context.Background(), lid, 5, time.Now()))
	flowApproveAndAdvance(t, a, lid)

	// Replay: row still exists with session_id_hash set; [B] consumes and
	// returns 401 "authorization expired" because the producing pod
	// already delivered the cookie inline at [C1].
	rec := pollLogin(a, lid, pollOpts{})
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), string(authstore.FailureAuthorizationExpired))
	require.Empty(t, rec.Result().Cookies())

	// Second replay: now the row is gone → 404.
	rec2 := pollLogin(a, lid, pollOpts{})
	require.Equal(t, http.StatusNotFound, rec2.Code)
}

func TestAuth_LoginPoll_DenialReturns401AuthorizationDenied(t *testing.T) {
	a, store := newAuth(t, &fakeResolver{}, newFakeDeviceFlow("alice", "W1"))
	lid, _, _ := startLogin(t, a)

	require.NoError(t, store.SetPollThrottle(context.Background(), lid, 5, time.Now()))
	flow := authFlow(a).(*fakeDeviceFlow)
	flow.deny()

	rec := pollLogin(a, lid, pollOpts{})
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), string(authstore.FailureAuthorizationDenied))
}

func TestAuth_LoginPoll_UnknownLIDReturns404(t *testing.T) {
	a, _ := newAuth(t, &fakeResolver{}, newFakeDeviceFlow("alice", "W1"))
	rec := pollLogin(a, "no-such-lid", pollOpts{})
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAuth_LoginPoll_MissingIDReturns400(t *testing.T) {
	a, _ := newAuth(t, &fakeResolver{}, newFakeDeviceFlow("alice", "W1"))
	rec := httptest.NewRecorder()
	a.ServeLoginPoll(rec, httptest.NewRequest(http.MethodGet, "/api/commander/login/poll", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAuth_LoginPoll_ExpiredLoginReturns404(t *testing.T) {
	a, store := newAuth(t, &fakeResolver{}, newFakeDeviceFlow("alice", "W1"))
	lid, _, _ := startLogin(t, a)

	// Force the row to be expired by deleting + re-reserving with tiny TTL.
	require.NoError(t, store.DeleteLogin(context.Background(), lid))
	require.NoError(t, store.ReserveLogin(context.Background(), lid, time.Now(), 5*time.Millisecond))
	time.Sleep(20 * time.Millisecond)

	rec := pollLogin(a, lid, pollOpts{})
	require.Equal(t, http.StatusNotFound, rec.Code)

	// Best-effort ConsumeLogin should have cleared the row.
	_, err := store.GetLogin(context.Background(), lid)
	require.ErrorIs(t, err, authstore.ErrNotFound)
}

func TestAuth_LoginPoll_ReservedState_ReturnsPending(t *testing.T) {
	a, store := newAuth(t, &fakeResolver{}, newFakeDeviceFlow("alice", "W1"))
	// Reserve only (don't go through ServeLogin → no FinalizeReservedLogin).
	require.NoError(t, store.ReserveLogin(context.Background(), "lid-reserved", time.Now(), 10*time.Minute))
	rec := pollLogin(a, "lid-reserved", pollOpts{})
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"pending"`)
}

func TestAuth_LoginPoll_RespectsNextPollAtThrottle(t *testing.T) {
	a, store := newAuth(t, &fakeResolver{}, newFakeDeviceFlow("alice", "W1"))
	lid, _, _ := startLogin(t, a)

	// Push next_poll_at into the future.
	require.NoError(t, store.SetPollThrottle(context.Background(), lid, 30, time.Now().Add(time.Hour)))

	flow := authFlow(a).(*fakeDeviceFlow)
	prevPolls := atomic.LoadInt32(&flow.pollOnceN)

	rec := pollLogin(a, lid, pollOpts{})
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"pending"`)
	require.Equal(t, prevPolls, atomic.LoadInt32(&flow.pollOnceN),
		"throttle window must skip PollOnce entirely")
}

func TestAuth_LoginPoll_SlowDown_BumpsInterval(t *testing.T) {
	a, store := newAuth(t, &fakeResolver{}, newFakeDeviceFlow("alice", "W1"))
	lid, _, _ := startLogin(t, a)
	// Force next_poll_at to now so PollOnce actually runs.
	require.NoError(t, store.SetPollThrottle(context.Background(), lid, 5, time.Now()))
	flow := authFlow(a).(*fakeDeviceFlow)
	flow.slowDown()

	pollLogin(a, lid, pollOpts{}) // pending response

	rec, err := store.GetLogin(context.Background(), lid)
	require.NoError(t, err)
	require.GreaterOrEqual(t, rec.IntervalSeconds, 10, "slow_down must add 5 to interval")
	require.True(t, rec.NextPollAt.After(time.Now()), "next_poll_at must be set forward")
}

func TestAuth_LoginPoll_IDTokenInvalid_Returns401AndPersistsSanitized(t *testing.T) {
	a, store := newAuth(t, &fakeResolver{}, newFakeDeviceFlow("alice", "W1"))
	lid, _, _ := startLogin(t, a)
	require.NoError(t, store.SetPollThrottle(context.Background(), lid, 5, time.Now()))

	flow := authFlow(a).(*fakeDeviceFlow)
	flow.setIDToken("not-a-jwt")
	flow.approve()

	rec := pollLogin(a, lid, pollOpts{})
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), string(authstore.FailureIDTokenInvalid))

	// Row now has sanitized failure persisted; raw "not-a-jwt" must NOT be in it.
	row, err := store.GetLogin(context.Background(), lid)
	require.NoError(t, err)
	require.Equal(t, authstore.FailureIDTokenInvalid, row.Failure)
	require.NotContains(t, string(row.Failure), "not-a-jwt")
}

// flowApproveAndAdvance: convenience for tests that need to drive one
// successful [C1] poll and ignore the cookie.
func flowApproveAndAdvance(t *testing.T, a *Authenticator, lid string) {
	t.Helper()
	authFlow(a).(*fakeDeviceFlow).approve()
	rec := pollLogin(a, lid, pollOpts{})
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"ok"`)
}

// authFlow exposes the Authenticator's deviceFlow for tests. Package-private
// because it relies on knowing the field is a concrete fake; tests construct
// the Authenticator via newAuth so this assertion is safe in their hands.
func authFlow(a *Authenticator) deviceFlow { return a.flow }

// --- cookie attributes --------------------------------------------------------

func TestAuth_LoginCookieSecureOnlyForHTTPS(t *testing.T) {
	t.Run("direct tls", func(t *testing.T) {
		cookie := completeLoginAndReturnCookie(t, true, "")
		require.True(t, cookie.Secure)
	})
	t.Run("forwarded https", func(t *testing.T) {
		cookie := completeLoginAndReturnCookie(t, false, "https")
		require.True(t, cookie.Secure)
	})
	t.Run("plain http", func(t *testing.T) {
		cookie := completeLoginAndReturnCookie(t, false, "")
		require.False(t, cookie.Secure)
	})
}

func completeLoginAndReturnCookie(t *testing.T, useTLS bool, forwardedProto string) *http.Cookie {
	t.Helper()
	a, store := newAuth(t, &fakeResolver{}, newFakeDeviceFlow("alice", "W1"))
	lid, _, _ := startLogin(t, a)
	require.NoError(t, store.SetPollThrottle(context.Background(), lid, 5, time.Now()))
	authFlow(a).(*fakeDeviceFlow).approve()

	rec := pollLogin(a, lid, pollOpts{useTLS: useTLS, forwardedProto: forwardedProto})
	require.Equal(t, http.StatusOK, rec.Code)
	cs := rec.Result().Cookies()
	require.Len(t, cs, 1)
	return cs[0]
}

// --- CommanderIdentity --------------------------------------------------------

func TestAuth_CommanderIdentity_CookieThenBearerFallback(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-bearer": {UserID: "bob", WorkspaceID: "W2", Source: identity.SourceAgentserver},
	}}
	a, store := newAuth(t, resolver, newFakeDeviceFlow("alice", "W1"))

	// Seed a session via the store directly (avoids the whole device flow).
	sid := seedSession(t, store, identity.Identity{
		UserID: "alice", WorkspaceID: "W1", Source: identity.SourceAgentserver,
	})

	// Cookie path.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
	ident, ok := a.CommanderIdentity(req)
	require.True(t, ok)
	require.Equal(t, "alice", ident.UserID)

	// Bearer fallback (no cookie).
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("Authorization", "Bearer tok-bearer")
	ident2, ok2 := a.CommanderIdentity(req2)
	require.True(t, ok2)
	require.Equal(t, "bob", ident2.UserID)

	// Neither.
	req3 := httptest.NewRequest(http.MethodGet, "/", nil)
	_, ok3 := a.CommanderIdentity(req3)
	require.False(t, ok3)
}

func TestAuth_CommanderIdentity_CookieFailsClosedOnStoreError(t *testing.T) {
	// Wrap inmemory with an "error on GetSession" store so we can simulate a
	// DB outage. A cookie present + store erroring must NOT fall through to
	// Bearer — that would widen the attack surface.
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-bearer": {UserID: "bob"},
	}}
	a := &Authenticator{
		resolver: resolver,
		flow:     newFakeDeviceFlow("x", "x"),
		store:    errorStore{err: errors.New("db down")},
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "some-sid"})
	req.Header.Set("Authorization", "Bearer tok-bearer")

	_, ok := a.CommanderIdentity(req)
	require.False(t, ok, "store error must fail closed, NOT fall through to Bearer")
}

// --- logout ------------------------------------------------------------------

func TestAuth_LogoutDeletesSession(t *testing.T) {
	a, store := newAuth(t, &fakeResolver{}, newFakeDeviceFlow("alice", "W1"))
	sid := seedSession(t, store, identity.Identity{
		UserID: "alice", WorkspaceID: "W1", Source: identity.SourceAgentserver,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/commander/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
	rec := httptest.NewRecorder()
	a.ServeLogout(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	// Session gone → cookie no longer authenticates.
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
	_, ok := a.CommanderIdentity(req2)
	require.False(t, ok)
}

// --- cap regression ----------------------------------------------------------

// TestAuth_LoginCapsPendingLogins: hammering POST /login without ever polling
// must (a) call RequestCode at most MaxActiveLogins times, (b) cap reservations
// at MaxActiveLogins, (c) reject overflow with 429. Regression: pre-fix code
// called RequestCode before the cap check, allowing unbounded amplification.
func TestAuth_LoginCapsPendingLogins(t *testing.T) {
	a, _ := newAuth(t, &fakeResolver{}, newFakeDeviceFlow("alice", "W1"))
	flow := authFlow(a).(*fakeDeviceFlow)

	total := authstore.MaxActiveLogins + 10
	var four29s int
	for i := 0; i < total; i++ {
		rec := httptest.NewRecorder()
		a.ServeLogin(rec, httptest.NewRequest(http.MethodPost, "/api/commander/login", nil))
		switch rec.Code {
		case http.StatusOK:
		case http.StatusTooManyRequests:
			four29s++
		default:
			t.Fatalf("unexpected status %d on call %d", rec.Code, i)
		}
	}
	require.Equal(t, 10, four29s, "calls beyond MaxActiveLogins should be rejected with 429")
	require.LessOrEqual(t, int(atomic.LoadInt32(&flow.requestCodeN)), authstore.MaxActiveLogins,
		"RequestCode must be gated by the cap")
}

// --- WithoutCancel write semantics -------------------------------------------

// TestAuth_LoginPoll_MarkLoginDoneSurvivesClientCancel:
// cancel r.Context() AFTER PollOnce returns ready but BEFORE MarkLoginDone
// would observe its DB write. WithoutCancel(ctx) inside writeCtx must let the
// write land regardless.
//
// We use an instrumentedStore that signals on MarkLoginDone entry and lets
// us release after the cancellation.
func TestAuth_LoginPoll_MarkLoginDoneSurvivesClientCancel(t *testing.T) {
	inner := authstore.NewInMemoryStore()
	gate := make(chan struct{})
	hit := make(chan struct{})
	wrapped := &delayingStore{
		Store:    inner,
		onEntry:  func() { close(hit); <-gate },
		hookOnce: true,
	}
	resolver := &fakeResolver{}
	flow := newFakeDeviceFlow("alice", "W1")
	a := newAuthenticatorWithFlow(resolver, flow, wrapped)

	// Set up reservation + finalize so the throttle window is open.
	lid, _, _ := startLogin(t, a)
	require.NoError(t, wrapped.SetPollThrottle(context.Background(), lid, 5, time.Now()))
	flow.approve()

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/commander/login/poll?id="+lid, nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.ServeLoginPoll(rec, req)
	}()

	<-hit         // MarkLoginDone entered
	cancel()      // simulate client disconnect
	close(gate)   // let MarkLoginDone proceed
	<-done        // handler returns

	// The write must have landed despite cancel.
	row, err := inner.GetLogin(context.Background(), lid)
	require.NoError(t, err)
	require.NotEmpty(t, row.SessionIDHash, "MarkLoginDone must commit despite client cancel")
}

// --- helpers -----------------------------------------------------------------

// seedSession bypasses the device flow and writes a session row directly via
// the store. Returns the plaintext sid (which is what the cookie carries).
func seedSession(t *testing.T, store authstore.Store, ident identity.Identity) string {
	t.Helper()
	lid := "seed-" + randomID()
	ctx := context.Background()
	require.NoError(t, store.ReserveLogin(ctx, lid, time.Now(), 10*time.Minute))
	require.NoError(t, store.FinalizeReservedLogin(ctx, lid, "dc",
		time.Now().Add(5*time.Minute), 5))
	sid := randomID()
	require.NoError(t, store.MarkLoginDone(ctx, lid, authstore.SessionRecord{
		PlaintextSessionID: sid,
		Identity:           ident,
		ExpiresAt:          time.Now().Add(sessionTTL),
	}))
	return sid
}

// errorStore returns the same err from every Store method. Used to drive the
// "fail-closed on store error" path.
type errorStore struct{ err error }

func (e errorStore) ReserveLogin(context.Context, string, time.Time, time.Duration) error {
	return e.err
}
func (e errorStore) FinalizeReservedLogin(context.Context, string, string, time.Time, int) error {
	return e.err
}
func (e errorStore) DeleteLogin(context.Context, string) error { return e.err }
func (e errorStore) GetLogin(context.Context, string) (authstore.LoginRecord, error) {
	return authstore.LoginRecord{}, e.err
}
func (e errorStore) SetPollThrottle(context.Context, string, int, time.Time) error { return e.err }
func (e errorStore) MarkLoginDone(context.Context, string, authstore.SessionRecord) error {
	return e.err
}
func (e errorStore) MarkLoginFailed(context.Context, string, authstore.Failure) error { return e.err }
func (e errorStore) ConsumeLogin(context.Context, string) (authstore.LoginRecord, error) {
	return authstore.LoginRecord{}, e.err
}
func (e errorStore) GetSession(context.Context, string) (authstore.SessionRecord, error) {
	return authstore.SessionRecord{}, e.err
}
func (e errorStore) DeleteSession(context.Context, string) error { return e.err }
func (e errorStore) SweepExpired(context.Context) (int64, int64, error) {
	return 0, 0, e.err
}

// delayingStore wraps a Store and runs `onEntry` once before MarkLoginDone
// is forwarded. Used to assert WithoutCancel keeps the write alive past a
// client cancel.
type delayingStore struct {
	authstore.Store
	mu       sync.Mutex
	onEntry  func()
	hookOnce bool
	fired    bool
}

func (d *delayingStore) MarkLoginDone(ctx context.Context, lid string, sess authstore.SessionRecord) error {
	d.mu.Lock()
	first := d.hookOnce && !d.fired
	d.fired = true
	d.mu.Unlock()
	if first && d.onEntry != nil {
		d.onEntry()
	}
	return d.Store.MarkLoginDone(ctx, lid, sess)
}

func makeIDToken(sub, workspaceID, workspaceRole string) string {
	header := map[string]string{"alg": "none", "typ": "JWT"}
	payload := map[string]any{
		"sub":            sub,
		"workspace_id":   workspaceID,
		"workspace_role": workspaceRole,
	}
	return encodeJWTPart(header) + "." + encodeJWTPart(payload) + "."
}

func encodeJWTPart(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return strings.TrimRight(base64.URLEncoding.EncodeToString(b), "=")
}
