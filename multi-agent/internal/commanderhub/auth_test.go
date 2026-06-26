package commanderhub

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	token    loginToken
}

func newFakeDeviceFlow(token string) *fakeDeviceFlow {
	return newFakeDeviceFlowToken(loginToken{
		AccessToken: token,
		IDToken:     makeIDToken("alice", "W1", ""),
	})
}

func newFakeDeviceFlowToken(token loginToken) *fakeDeviceFlow {
	return &fakeDeviceFlow{approved: make(chan struct{}), token: token}
}

func (f *fakeDeviceFlow) RequestCode(context.Context) (DeviceCode, error) {
	return DeviceCode{Code: "dc", VerificationURIComplete: "https://agent/verify?user_code=XX", ExpiresIn: 5 * time.Minute}, nil
}

func (f *fakeDeviceFlow) PollToken(ctx context.Context, _ DeviceCode) (loginToken, error) {
	select {
	case <-f.approved:
		return f.token, nil
	case <-ctx.Done():
		return loginToken{}, ctx.Err()
	}
}
func (f *fakeDeviceFlow) CheckToken(_ context.Context, _ DeviceCode) (loginToken, error) {
	select {
	case <-f.approved:
		return f.token, nil
	default:
		return loginToken{}, errAuthPending
	}
}

func (f *fakeDeviceFlow) approve() { close(f.approved) }

func newAuth(t *testing.T, resolver identity.Resolver, flow deviceFlow) *Authenticator {
	t.Helper()
	return newAuthenticatorWithFlow(resolver, flow, "https://test-agentserver")
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
	require.False(t, doneCookie.Secure, "loopback/http browser flows must accept the login cookie")
	require.Equal(t, http.SameSiteLaxMode, doneCookie.SameSite)

	// replay poll → in-memory entry consumed, but cross-pod fallback still
	// works (poll token decodes, CheckToken returns approved) — this is
	// correct: the signed cookie is idempotent.
	rec4 := httptest.NewRecorder()
	a.ServeLoginPoll(rec4, httptest.NewRequest(http.MethodGet, "/api/commander/login/poll?id="+lr.LoginID, nil))
	require.Equal(t, http.StatusOK, rec4.Code)
	require.Contains(t, rec4.Body.String(), `"ok"`)
}

func TestAuth_LoginUsesOAuthIDTokenIdentityNotProxyWhoami(t *testing.T) {
	resolver := &countingResolver{err: identity.ErrInvalid}
	flow := newFakeDeviceFlowToken(loginToken{
		AccessToken: "oauth-access-token-not-a-proxy-token",
		IDToken:     makeIDToken("alice", "W1", ""),
	})
	a := newAuth(t, resolver, flow)

	rec := httptest.NewRecorder()
	a.ServeLogin(rec, httptest.NewRequest(http.MethodPost, "/api/commander/login", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var lr struct {
		LoginID string `json:"login_id"`
	}
	require.NoError(t, jsonUnmarshal(rec.Body.Bytes(), &lr))
	require.NotEmpty(t, lr.LoginID)

	flow.approve()
	var cookie *http.Cookie
	require.Eventually(t, func() bool {
		pollRec := httptest.NewRecorder()
		a.ServeLoginPoll(pollRec, httptest.NewRequest(http.MethodGet, "/api/commander/login/poll?id="+lr.LoginID, nil))
		if pollRec.Code != http.StatusOK || len(pollRec.Result().Cookies()) != 1 {
			return false
		}
		cookie = pollRec.Result().Cookies()[0]
		return true
	}, time.Second, 10*time.Millisecond)
	require.Equal(t, int32(0), atomic.LoadInt32(&resolver.calls), "OAuth access tokens are not proxy tokens and must not be sent to whoami")

	req := httptest.NewRequest(http.MethodGet, "/api/commander/daemons", nil)
	req.AddCookie(cookie)
	ident, ok := a.CommanderIdentity(req)
	require.True(t, ok)
	require.Equal(t, identity.Identity{
		UserID:      "alice",
		WorkspaceID: "W1",
		Source:      identity.SourceAgentserver,
	}, ident)
}

func TestAuth_LoginPollFailureIncludesReason(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{}}
	flow := newFakeDeviceFlowToken(loginToken{AccessToken: "oauth-access-token", IDToken: ""})
	a := newAuth(t, resolver, flow)

	rec := httptest.NewRecorder()
	a.ServeLogin(rec, httptest.NewRequest(http.MethodPost, "/api/commander/login", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var lr struct {
		LoginID string `json:"login_id"`
	}
	require.NoError(t, jsonUnmarshal(rec.Body.Bytes(), &lr))

	flow.approve()
	require.Eventually(t, func() bool {
		pollRec := httptest.NewRecorder()
		a.ServeLoginPoll(pollRec, httptest.NewRequest(http.MethodGet, "/api/commander/login/poll?id="+lr.LoginID, nil))
		if pollRec.Code != http.StatusUnauthorized {
			return false
		}
		require.Contains(t, pollRec.Header().Get("Content-Type"), "application/json")
		require.Contains(t, pollRec.Body.String(), "id_token")
		return true
	}, time.Second, 10*time.Millisecond)
}

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
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	flow := newFakeDeviceFlow("tok-alice")
	a := newAuth(t, resolver, flow)

	req := httptest.NewRequest(http.MethodPost, "/api/commander/login", nil)
	rec := httptest.NewRecorder()
	a.ServeLogin(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var lr struct {
		LoginID string `json:"login_id"`
	}
	require.NoError(t, jsonUnmarshal(rec.Body.Bytes(), &lr))
	require.NotEmpty(t, lr.LoginID)

	flow.approve()
	var cookie *http.Cookie
	require.Eventually(t, func() bool {
		pollReq := httptest.NewRequest(http.MethodGet, "/api/commander/login/poll?id="+lr.LoginID, nil)
		if useTLS {
			pollReq.TLS = &tls.ConnectionState{}
		}
		if forwardedProto != "" {
			pollReq.Header.Set("X-Forwarded-Proto", forwardedProto)
		}
		pollRec := httptest.NewRecorder()
		a.ServeLoginPoll(pollRec, pollReq)
		if pollRec.Code != http.StatusOK || len(pollRec.Result().Cookies()) != 1 {
			return false
		}
		cookie = pollRec.Result().Cookies()[0]
		return true
	}, time.Second, 10*time.Millisecond)
	require.NotNil(t, cookie)
	return cookie
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
	expiresIn    time.Duration
	requestCodeN int32 // atomic
}

func (f *countingDeviceFlow) RequestCode(context.Context) (DeviceCode, error) {
	atomic.AddInt32(&f.requestCodeN, 1)
	return DeviceCode{
		Code:                    "dc",
		VerificationURIComplete: "https://agent/verify?user_code=XX",
		ExpiresIn:               f.expiresIn,
	}, nil
}

func (f *countingDeviceFlow) PollToken(ctx context.Context, _ DeviceCode) (loginToken, error) {
	<-ctx.Done()
	return loginToken{}, ctx.Err()
}

func (f *countingDeviceFlow) CheckToken(_ context.Context, _ DeviceCode) (loginToken, error) {
	return loginToken{}, errAuthPending
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

type countingResolver struct {
	calls int32
	err   error
}

func (r *countingResolver) Resolve(context.Context, string) (identity.Identity, error) {
	atomic.AddInt32(&r.calls, 1)
	if r.err != nil {
		return identity.Identity{}, r.err
	}
	return identity.Identity{UserID: "unexpected", WorkspaceID: "unexpected"}, nil
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

// TestAuth_CrossPodPoll: simulates the Istio round-robin scenario where
// POST /login hits pod A and GET /login/poll hits pod B (no in-memory state).
// Pod B should decode the poll token and call CheckToken directly.
func TestAuth_CrossPodPoll(t *testing.T) {
	token := loginToken{
		AccessToken: "tok-cross",
		IDToken:     makeIDToken("bob", "W2", "admin"),
	}
	flow := newFakeDeviceFlowToken(token)

	// Pod A: creates the login
	podA := newAuth(t, &fakeResolver{mu: map[string]identity.Identity{}}, flow)
	rec := httptest.NewRecorder()
	podA.ServeLogin(rec, httptest.NewRequest(http.MethodPost, "/api/commander/login", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var lr struct {
		LoginID  string `json:"login_id"`
		Interval int    `json:"interval"`
	}
	require.NoError(t, jsonUnmarshal(rec.Body.Bytes(), &lr))
	require.NotEmpty(t, lr.LoginID)

	// Pod B: different Authenticator (no shared in-memory state)
	podB := newAuth(t, &fakeResolver{mu: map[string]identity.Identity{}}, flow)

	// Before approval: should get pending (not 404)
	rec2 := httptest.NewRecorder()
	podB.ServeLoginPoll(rec2, httptest.NewRequest(http.MethodGet, "/api/commander/login/poll?id="+lr.LoginID, nil))
	require.Equal(t, http.StatusOK, rec2.Code)
	require.Contains(t, rec2.Body.String(), `"pending"`)

	// Approve and poll again
	flow.approve()
	rec3 := httptest.NewRecorder()
	podB.ServeLoginPoll(rec3, httptest.NewRequest(http.MethodGet, "/api/commander/login/poll?id="+lr.LoginID, nil))
	require.Equal(t, http.StatusOK, rec3.Code)
	require.Contains(t, rec3.Body.String(), `"ok"`)
	cookies := rec3.Result().Cookies()
	require.Len(t, cookies, 1)

	// The cookie from cross-pod poll should work on any pod (signed, not in-memory)
	req := httptest.NewRequest(http.MethodGet, "/api/commander/daemons", nil)
	req.AddCookie(cookies[0])
	ident, ok := podB.CommanderIdentity(req)
	require.True(t, ok)
	require.Equal(t, "bob", ident.UserID)
	require.Equal(t, "W2", ident.WorkspaceID)

	// Also works on pod A
	identA, okA := podA.CommanderIdentity(req)
	require.True(t, okA)
	require.Equal(t, "bob", identA.UserID)
}

// TestAuth_SignedCookieIdentity verifies that the HMAC-signed cookie can be
// created and verified across different Authenticator instances (cross-pod).
func TestAuth_SignedCookieIdentity(t *testing.T) {
	key := deriveSessionKey("https://test-agentserver")
	ident := identity.Identity{UserID: "alice", WorkspaceID: "W1", Role: "admin", Source: identity.SourceAgentserver}
	cookie := signSessionCookie(key, ident, time.Hour)

	got, ok := verifySessionCookie(key, cookie)
	require.True(t, ok)
	require.Equal(t, "alice", got.UserID)
	require.Equal(t, "W1", got.WorkspaceID)
	require.Equal(t, "admin", got.Role)
	require.Equal(t, identity.SourceAgentserver, got.Source)

	// Wrong key rejects
	wrongKey := deriveSessionKey("https://other-server")
	_, ok = verifySessionCookie(wrongKey, cookie)
	require.False(t, ok)

	// Expired cookie rejects
	expired := signSessionCookie(key, ident, -time.Hour)
	_, ok = verifySessionCookie(key, expired)
	require.False(t, ok)

	// Tampered cookie rejects
	_, ok = verifySessionCookie(key, cookie+"x")
	require.False(t, ok)
}

// TestAuth_PollTokenEncodeDecode verifies the poll token round-trips correctly.
func TestAuth_PollTokenEncodeDecode(t *testing.T) {
	dc := DeviceCode{
		Code:      "device-code-123",
		ExpiresIn: 10 * time.Minute,
		Interval:  5 * time.Second,
	}
	id := "random-hex-id"
	encoded := encodePollToken(dc, id)
	decoded, err := decodePollToken(encoded)
	require.NoError(t, err)
	require.Equal(t, "device-code-123", decoded.DeviceCode)
	require.Equal(t, 5, decoded.Interval)
	require.Equal(t, "random-hex-id", decoded.ID)
	require.True(t, time.Unix(decoded.ExpiresAt, 0).After(time.Now()))
}
