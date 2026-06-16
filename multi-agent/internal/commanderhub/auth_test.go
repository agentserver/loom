package commanderhub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/identity"
)

// fakeDeviceFlow hands out a fixed verify URL and blocks PollToken until
// approve() is called, then returns a token the resolver can map.
type fakeDeviceFlow struct {
	mu       sync.Mutex
	approved chan struct{}
	token    string
}

func newFakeDeviceFlow(token string) *fakeDeviceFlow {
	return &fakeDeviceFlow{approved: make(chan struct{}), token: token}
}

func (f *fakeDeviceFlow) RequestCode(context.Context) (DeviceCode, error) {
	return DeviceCode{Code: "dc", VerificationURIComplete: "https://agent/verify?user_code=XX", ExpiresIn: 5 * time.Minute}, nil
}

func (f *fakeDeviceFlow) PollToken(ctx context.Context, _ DeviceCode) (string, error) {
	select {
	case <-f.approved:
		return f.token, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
func (f *fakeDeviceFlow) approve() { close(f.approved) }

func newAuth(t *testing.T, resolver identity.Resolver, flow deviceFlow) *Authenticator {
	t.Helper()
	return newAuthenticatorWithFlow(resolver, flow)
}

// TestAuth_LoginPollPendingThenApproved: login returns verify URL + login_id;
// poll is pending until approve, then returns ok and sets the httpOnly cookie.
func TestAuth_LoginPollPendingThenApproved(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	flow := newFakeDeviceFlow("tok-alice")
	a := newAuth(t, resolver, flow)

	// POST /login
	req := httptest.NewRequest(http.MethodPost, "/api/commander/login", nil)
	rec := httptest.NewRecorder()
	a.ServeLogin(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var lr struct {
		VerificationURIComplete string `json:"verification_uri_complete"`
		LoginID                 string `json:"login_id"`
	}
	require.NoError(t, jsonUnmarshal(rec.Body.Bytes(), &lr))
	require.Equal(t, "https://agent/verify?user_code=XX", lr.VerificationURIComplete)
	require.NotEmpty(t, lr.LoginID)

	// poll before approval → pending
	rec2 := httptest.NewRecorder()
	a.ServeLoginPoll(rec2, httptest.NewRequest(http.MethodGet, "/api/commander/login/poll?id="+lr.LoginID, nil))
	require.Equal(t, http.StatusOK, rec2.Code)
	require.Contains(t, rec2.Body.String(), `"pending"`)
	require.Empty(t, rec2.Result().Cookies())

	// approve on the agentserver side → observer's poller gets the token.
	// The done entry is one-shot: it is deleted on the poll that observes the
	// cookie, so capture the cookie from that first consuming poll.
	flow.approve()
	var doneCookie *http.Cookie
	require.Eventually(t, func() bool {
		rec3 := httptest.NewRecorder()
		a.ServeLoginPoll(rec3, httptest.NewRequest(http.MethodGet, "/api/commander/login/poll?id="+lr.LoginID, nil))
		if rec3.Code != http.StatusOK || len(rec3.Result().Cookies()) != 1 {
			return false
		}
		doneCookie = rec3.Result().Cookies()[0]
		return true
	}, time.Second, 10*time.Millisecond, "poll never completed after approval")

	require.NotNil(t, doneCookie)
	require.Equal(t, sessionCookieName, doneCookie.Name)
	require.True(t, doneCookie.HttpOnly)
	require.Equal(t, http.SameSiteLaxMode, doneCookie.SameSite)

	// replay poll → entry consumed (deleted) → 404, no cookie
	rec4 := httptest.NewRecorder()
	a.ServeLoginPoll(rec4, httptest.NewRequest(http.MethodGet, "/api/commander/login/poll?id="+lr.LoginID, nil))
	require.Equal(t, http.StatusNotFound, rec4.Code)
	require.Empty(t, rec4.Result().Cookies())
}

// TestAuth_CommanderIdentityCookieOrBearer: cookie session → cached identity;
// no cookie but bearer → resolve; neither → false.
func TestAuth_CommanderIdentityCookieOrBearer(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	a := newAuth(t, resolver, newFakeDeviceFlow("tok-alice"))

	// seed a session directly
	sessID := a.putSession("tok-alice", identity.Identity{UserID: "alice", WorkspaceID: "W1"})

	// cookie path
	req := httptest.NewRequest(http.MethodGet, "/api/commander/daemons", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessID})
	ident, ok := a.CommanderIdentity(req)
	require.True(t, ok)
	require.Equal(t, "alice", ident.UserID)

	// bearer path (no cookie)
	req2 := httptest.NewRequest(http.MethodGet, "/api/commander/daemons", nil)
	req2.Header.Set("Authorization", "Bearer tok-alice")
	ident2, ok2 := a.CommanderIdentity(req2)
	require.True(t, ok2)
	require.Equal(t, "alice", ident2.UserID)

	// neither
	req3 := httptest.NewRequest(http.MethodGet, "/api/commander/daemons", nil)
	_, ok3 := a.CommanderIdentity(req3)
	require.False(t, ok3)
}

// TestAuth_LogoutClearsSession: logout deletes the session and expires the cookie.
func TestAuth_LogoutClearsSession(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{}}
	a := newAuth(t, resolver, newFakeDeviceFlow("x"))
	sessID := a.putSession("tok", identity.Identity{UserID: "alice", WorkspaceID: "W1"})

	req := httptest.NewRequest(http.MethodPost, "/api/commander/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessID})
	rec := httptest.NewRecorder()
	a.ServeLogout(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	// session gone → cookie no longer authenticates
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessID})
	_, ok := a.CommanderIdentity(req2)
	require.False(t, ok)
}

// countingDeviceFlow wraps a blockingDeviceFlow and COUNTS RequestCode calls.
// This proves the cap GATES the agentserver call (not just the local map): if
// ServeLogin called RequestCode before the cap check, the counter would equal
// the number of requests; with the slot reserved first, it is capped.
type countingDeviceFlow struct {
	expiresIn     time.Duration
	requestCodeN  int32 // atomic
}

func (f *countingDeviceFlow) RequestCode(context.Context) (DeviceCode, error) {
	atomic.AddInt32(&f.requestCodeN, 1)
	return DeviceCode{
		Code:                    "dc",
		VerificationURIComplete: "https://agent/verify?user_code=XX",
		ExpiresIn:               f.expiresIn,
	}, nil
}

func (f *countingDeviceFlow) PollToken(ctx context.Context, _ DeviceCode) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// TestAuth_LoginCapsPendingLogins: hammering the unauthenticated POST /login
// without ever polling must (a) call RequestCode at most maxPendingLogins times
// — i.e. the cap GATES the agentserver call, not just the local map — (b) never
// grow the logins map beyond the cap, and (c) reject overflow requests with 429.
//
// Regression: the pre-fix code called RequestCode before the cap check, so the
// counter hit maxPendingLogins+N (unbounded upstream amplification).
func TestAuth_LoginCapsPendingLogins(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	// Short ExpiresIn: pollLogin's PollToken ctx times out → goroutines exit.
	flow := &countingDeviceFlow{expiresIn: 50 * time.Millisecond}
	a := newAuth(t, resolver, flow)

	total := maxPendingLogins + 10
	var four29s int
	for i := 0; i < total; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/commander/login", nil)
		rec := httptest.NewRecorder()
		a.ServeLogin(rec, req)
		switch rec.Code {
		case http.StatusOK:
		case http.StatusTooManyRequests:
			four29s++
		default:
			t.Fatalf("unexpected status %d on call %d", rec.Code, i)
		}
		// Map must never exceed the cap at any point.
		a.loginMu.Lock()
		got := len(a.logins)
		a.loginMu.Unlock()
		require.LessOrEqualf(t, got, maxPendingLogins, "logins map exceeded cap on call %d: %d", i, got)
	}

	// Exactly the overflow calls beyond the cap were rejected.
	require.Equal(t, 10, four29s, "calls beyond maxPendingLogins should be rejected with 429")
	final := len(a.logins)
	require.Equal(t, maxPendingLogins, final, "map should be exactly at the cap")

	// The cap must GATE the agentserver RequestCode call: at most
	// maxPendingLogins calls, never one per request.
	n := atomic.LoadInt32(&flow.requestCodeN)
	require.LessOrEqualf(t, int(n), maxPendingLogins,
		"RequestCode was called %d times; the cap must gate it at %d", n, maxPendingLogins)
}
