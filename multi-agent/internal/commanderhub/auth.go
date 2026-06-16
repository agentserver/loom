package commanderhub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"

	"github.com/yourorg/multi-agent/internal/identity"
)

const (
	sessionCookieName = "commander_sess"
	sessionTTL        = 12 * time.Hour
	loginTTL          = 10 * time.Minute
	// maxPendingLogins bounds the in-flight login map. POST /login is
	// unauthenticated (it's the auth entry); an attacker spamming it without
	// ever polling would otherwise grow the map without bound. pollLogin
	// goroutines self-terminate after dc.ExpiresIn, so this cap plus the lazy
	// TTL sweep below bounds both the map and the goroutine count — no
	// background sweeper is needed.
	maxPendingLogins = 64
)

// DeviceCode is the observer-internal view of an agentserver device-authorization
// response. Code is the server-side secret handed to PollToken.
type DeviceCode struct {
	Code                    string
	VerificationURIComplete string
	ExpiresIn               time.Duration
}

// deviceFlow is the seam between Authenticator and the OAuth grant. Production
// uses agentsdkDeviceFlow; tests inject a fake.
type deviceFlow interface {
	RequestCode(ctx context.Context) (DeviceCode, error)
	PollToken(ctx context.Context, code DeviceCode) (string, error)
}

// agentsdkDeviceFlow wraps the real agentserver device-code endpoints.
//
// agentsdk shapes (confirmed via go doc on agentserver v0.48.1):
//
//	RequestDeviceCode(ctx, serverURL) (*agentsdk.DeviceAuthResponse, error)
//	PollForToken(ctx, serverURL, *agentsdk.DeviceAuthResponse) (*agentsdk.TokenResponse, error)
//
// DeviceAuthResponse{ DeviceCode, UserCode, VerificationURI,
// VerificationURIComplete, ExpiresIn int (seconds), Interval }.
type agentsdkDeviceFlow struct{ serverURL string }

func (f agentsdkDeviceFlow) RequestCode(ctx context.Context) (DeviceCode, error) {
	dc, err := agentsdk.RequestDeviceCode(ctx, f.serverURL)
	if err != nil {
		return DeviceCode{}, err
	}
	return DeviceCode{
		Code:                    dc.DeviceCode,
		VerificationURIComplete: dc.VerificationURIComplete,
		ExpiresIn:               time.Duration(dc.ExpiresIn) * time.Second,
	}, nil
}

func (f agentsdkDeviceFlow) PollToken(ctx context.Context, code DeviceCode) (string, error) {
	// PollForToken wants the agentsdk DeviceAuthResponse; rebuild the minimal
	// shape it reads (DeviceCode + ExpiresIn for its own poll deadline).
	dc := &agentsdk.DeviceAuthResponse{
		DeviceCode: code.Code,
		ExpiresIn:  int(code.ExpiresIn / time.Second),
	}
	tok, err := agentsdk.PollForToken(ctx, f.serverURL, dc)
	if err != nil {
		return "", err
	}
	return tok.AccessToken, nil
}

type session struct {
	token     string
	identity  identity.Identity
	expiresAt time.Time
}

type loginState struct {
	code      DeviceCode
	sessionID string // set when PollToken succeeds
	failed    bool
	done      bool
	createdAt time.Time // set when the entry is created; drives loginTTL reaping
}

// Authenticator drives the web login (device flow) and owns the cookie→token
// session store. CommanderIdentity is the auth check used by /api/commander/*.
type Authenticator struct {
	resolver identity.Resolver
	flow     deviceFlow

	sessMu   sync.Mutex
	sessions map[string]*session

	loginMu sync.Mutex
	logins  map[string]*loginState
}

// NewAuthenticator builds an Authenticator backed by the real agentserver
// device flow at agentserverURL. Used by observerweb wiring (Task 8).
func NewAuthenticator(resolver identity.Resolver, agentserverURL string) *Authenticator {
	return newAuthenticatorWithFlow(resolver, agentsdkDeviceFlow{serverURL: agentserverURL})
}

// newAuthenticatorWithFlow lets tests inject a fake deviceFlow.
func newAuthenticatorWithFlow(resolver identity.Resolver, flow deviceFlow) *Authenticator {
	return &Authenticator{
		resolver: resolver,
		flow:     flow,
		sessions: make(map[string]*session),
		logins:   make(map[string]*loginState),
	}
}

// CommanderIdentity authenticates a /api/commander/* request: cookie session
// first (cached identity), then Authorization: Bearer (resolve), else false.
func (a *Authenticator) CommanderIdentity(r *http.Request) (identity.Identity, bool) {
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		a.sessMu.Lock()
		s := a.sessions[c.Value]
		a.sessMu.Unlock()
		if s != nil && time.Now().Before(s.expiresAt) {
			return s.identity, true
		}
	}
	if tok, ok := bearerToken(r.Header.Get("Authorization")); ok {
		ident, err := a.resolver.Resolve(r.Context(), tok)
		if err == nil {
			return ident, true
		}
	}
	return identity.Identity{}, false
}

// putSession is a test helper that seeds a session and returns its id.
func (a *Authenticator) putSession(token string, ident identity.Identity) string {
	sid := randomID()
	a.sessMu.Lock()
	a.sessions[sid] = &session{token: token, identity: ident, expiresAt: time.Now().Add(sessionTTL)}
	a.sessMu.Unlock()
	return sid
}

// ServeLogin: POST /api/commander/login → starts device flow, returns verify URL.
//
// The pending slot is RESERVED under the lock BEFORE the agentserver RequestCode
// call: an unauthenticated POST /login is the auth entry point, so it must not
// amplify upstream. Reserving the placeholder first (lazy sweep + cap + insert)
// means concurrent requests serialize on the cap before any HTTP call — overflow
// requests are 429'd without ever hitting agentserver. On RequestCode failure the
// reserved slot is released.
func (a *Authenticator) ServeLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	lid := randomID()
	now := time.Now()
	a.loginMu.Lock()
	// Lazy reaping of orphan entries (created but never polled to a terminal /
	// expired state): drop anything older than loginTTL before deciding whether
	// there's room for a new entry. No background sweeper goroutine — this is
	// the only place orphans are reaped, and it runs on every /login.
	for k, st := range a.logins {
		if now.Sub(st.createdAt) > loginTTL {
			delete(a.logins, k)
		}
	}
	if len(a.logins) >= maxPendingLogins {
		a.loginMu.Unlock()
		http.Error(w, "too many pending logins", http.StatusTooManyRequests)
		return
	}
	// RESERVE the slot: insert a placeholder (no code yet) so the cap holds
	// atomically before the upstream call. Concurrent requests serialize here.
	a.logins[lid] = &loginState{createdAt: now}
	a.loginMu.Unlock()

	// Now the agentserver call, gated by the reserved slot. Do NOT hold the
	// lock during this HTTP call.
	dc, err := a.flow.RequestCode(r.Context())
	if err != nil {
		a.loginMu.Lock()
		delete(a.logins, lid) // release the reserved slot
		a.loginMu.Unlock()
		http.Error(w, "device flow: "+err.Error(), http.StatusBadGateway)
		return
	}

	// Fill in the reserved entry. It was created moments ago (createdAt=now),
	// so it cannot have been TTL-swept in the sub-millisecond window; if it
	// were somehow nil, skip the poller — defensive, effectively unreachable.
	a.loginMu.Lock()
	st := a.logins[lid]
	if st != nil {
		st.code = dc
	}
	a.loginMu.Unlock()

	if st != nil {
		go a.pollLogin(lid, dc)
	}

	writeJSON(w, map[string]any{
		"verification_uri_complete": dc.VerificationURIComplete,
		"login_id":                  lid,
		"expires_in":                int(dc.ExpiresIn / time.Second),
	})
}

func (a *Authenticator) pollLogin(lid string, dc DeviceCode) {
	ctx, cancel := context.WithTimeout(context.Background(), dc.ExpiresIn)
	defer cancel()
	tok, err := a.flow.PollToken(ctx, dc)
	a.loginMu.Lock()
	st := a.logins[lid]
	a.loginMu.Unlock()
	if st == nil {
		return
	}
	if err != nil {
		a.loginMu.Lock()
		st.failed = true
		a.loginMu.Unlock()
		return
	}
	ident, err := a.resolver.Resolve(ctx, tok)
	if err != nil {
		a.loginMu.Lock()
		st.failed = true
		a.loginMu.Unlock()
		return
	}
	sid := a.putSession(tok, ident)
	a.loginMu.Lock()
	st.sessionID = sid
	st.done = true
	a.loginMu.Unlock()
}

// ServeLoginPoll: GET /api/commander/login/poll?id=<login_id>.
//
// Each login is one-shot: a terminal result (failed or done) is returned at
// most once — the entry is deleted on the poll that observes it, so a replay
// poll gets 404. Abandoned or never-completing entries are reaped lazily once
// they exceed loginTTL (best-effort; no background sweeper).
func (a *Authenticator) ServeLoginPoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	lid := r.URL.Query().Get("id")
	// Snapshot the entry's state under the lock; pollLogin writes these fields
	// from its own goroutine, so reading them unlocked would race.
	a.loginMu.Lock()
	st := a.logins[lid]
	var (
		failed    bool
		done      bool
		sessionID string
		expired   bool
	)
	if st != nil {
		failed = st.failed
		done = st.done
		sessionID = st.sessionID
		expired = time.Since(st.createdAt) > loginTTL
	}
	// One-shot: consume terminal/expired entries on the poll that observes them.
	if st != nil && (failed || done || expired) {
		delete(a.logins, lid)
	}
	a.loginMu.Unlock()

	if st == nil || expired {
		http.Error(w, "unknown login", http.StatusNotFound)
		return
	}
	if failed {
		// Consumed: one-shot. A replay poll will get 404.
		http.Error(w, "login failed", http.StatusUnauthorized)
		return
	}
	if !done {
		writeJSON(w, map[string]any{"status": "pending"})
		return
	}
	// done: set cookie (entry already consumed above — one-shot). A replay poll gets 404.
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL / time.Second),
	})
	writeJSON(w, map[string]any{"status": "ok"})
}

// ServeLogout: POST /api/commander/logout.
func (a *Authenticator) ServeLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if c, err := r.Cookie(sessionCookieName); err == nil {
		a.sessMu.Lock()
		delete(a.sessions, c.Value)
		a.sessMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, map[string]any{"status": "ok"})
}

// --- shared helpers (writeJSON also used by http.go in Phase 3) ---

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func randomID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
