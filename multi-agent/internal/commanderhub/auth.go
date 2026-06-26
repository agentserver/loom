package commanderhub

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"

	"github.com/yourorg/multi-agent/internal/commanderhub/authstore"
	"github.com/yourorg/multi-agent/internal/identity"
)

const (
	sessionCookieName = "commander_sess"
	sessionTTL        = 12 * time.Hour
	loginTTL          = 10 * time.Minute
	deviceClientID    = "agentserver-agent-cli"

	// pollOnceTimeout is the per-call upper bound on agentserver
	// /api/oauth2/token round-trips. agentserver is LAN-local; p99 is far
	// below this.
	pollOnceTimeout = 5 * time.Second

	// storeWriteTimeout bounds every DB write that must survive client
	// disconnect. See Authenticator.writeCtx — paired with WithoutCancel so
	// a client cancellation does not abort the unkillable write but also
	// cannot leak a goroutine if Postgres or the pool stalls.
	storeWriteTimeout = 5 * time.Second
)

// DeviceCode is the observer-internal view of an agentserver
// device-authorization response. Code is the server-side secret handed to
// PollOnce.
type DeviceCode struct {
	Code                    string
	VerificationURIComplete string
	ExpiresIn               time.Duration
	Interval                time.Duration
}

type loginToken struct {
	AccessToken string
	IDToken     string
}

// deviceFlow is the seam between Authenticator and the OAuth grant.
// Production uses agentsdkDeviceFlow; tests inject a fake.
//
// PollOnce semantics:
//
//	tokenReady=true                          → token in `tok`, no more polls needed
//	tokenReady=false, retryable=true         → keep polling on the next /poll tick
//	    slowDown=true                        → the throttle should add 5 s
//	tokenReady=false, retryable=false, err!=nil
//	                                         → terminal upstream failure; err is a
//	                                            sentinel (authstore.ErrAuthorization*)
//	                                            or a generic err mapped to
//	                                            FailureDeviceFlow by SanitizeFailure.
//
// PollOnce MUST NOT echo raw HTTP response bodies in any return value (5xx
// bodies and even some 4xx OAuth bodies may carry token-shaped junk).
type deviceFlow interface {
	RequestCode(ctx context.Context) (DeviceCode, error)
	PollOnce(ctx context.Context, code DeviceCode) (
		tok loginToken, tokenReady, retryable, slowDown bool, err error,
	)
}

// agentsdkDeviceFlow wraps the real agentserver device-code endpoints.
//
// agentsdk shapes (confirmed via go doc on agentserver v0.48.1):
//
//	RequestDeviceCode(ctx, serverURL) (*agentsdk.DeviceAuthResponse, error)
//	DeviceAuthResponse{ DeviceCode, UserCode, VerificationURI,
//	  VerificationURIComplete, ExpiresIn int (seconds), Interval }.
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
		Interval:                time.Duration(dc.Interval) * time.Second,
	}, nil
}

// PollOnce executes a single agentserver /api/oauth2/token round-trip and
// classifies the outcome. NEVER echoes the upstream response body in err.
func (f agentsdkDeviceFlow) PollOnce(ctx context.Context, code DeviceCode) (
	loginToken, bool, bool, bool, error,
) {
	tokenURL := strings.TrimRight(f.serverURL, "/") + "/api/oauth2/token"

	form := url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"client_id":   {deviceClientID},
		"device_code": {code.Code},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		// Local construction failed; treat as terminal (no upstream body).
		return loginToken{}, false, false, false, errors.New("device flow: bad request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Network error (dial/timeout/EOF). Don't surface raw err to keep
		// callers' sanitization simple; signal retryable.
		return loginToken{}, false, true, false, nil
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var tokenResp struct {
			AccessToken string `json:"access_token"`
			IDToken     string `json:"id_token"`
		}
		if err := json.Unmarshal(body, &tokenResp); err != nil {
			// 200 with malformed body: terminal, no body interpolation.
			return loginToken{}, false, false, false, errors.New("device flow: bad token response")
		}
		return loginToken{AccessToken: tokenResp.AccessToken, IDToken: tokenResp.IDToken},
			true, false, false, nil
	}

	if resp.StatusCode >= 500 {
		// Transient upstream error; do NOT include body (may contain
		// token-shaped junk per security analysis).
		return loginToken{}, false, true, false, nil
	}

	// 4xx: only the OAuth-defined error codes are well known. Parse just
	// the `error` field — never the full body.
	var errResp struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &errResp)

	switch errResp.Error {
	case "authorization_pending":
		return loginToken{}, false, true, false, nil
	case "slow_down":
		return loginToken{}, false, true, true, nil
	case "access_denied":
		return loginToken{}, false, false, false, authstore.ErrAuthorizationDenied
	case "expired_token":
		return loginToken{}, false, false, false, authstore.ErrAuthorizationExpired
	default:
		// Unknown 4xx (including bad request, slow_down typo, etc.):
		// terminal but no body interpolation.
		return loginToken{}, false, false, false, errors.New("device flow: unknown error")
	}
}

// Authenticator drives the web login (device flow) and owns CommanderIdentity
// for the /api/commander/* surface. All login + session state lives in
// authstore.Store; this struct holds zero cross-pod state.
type Authenticator struct {
	resolver identity.Resolver
	flow     deviceFlow
	store    authstore.Store
}

// NewAuthenticator builds an Authenticator backed by the real agentserver
// device flow at agentserverURL.
func NewAuthenticator(resolver identity.Resolver, agentserverURL string, store authstore.Store) *Authenticator {
	return newAuthenticatorWithFlow(resolver, agentsdkDeviceFlow{serverURL: agentserverURL}, store)
}

// newAuthenticatorWithFlow lets tests inject a fake deviceFlow + Store.
func newAuthenticatorWithFlow(resolver identity.Resolver, flow deviceFlow, store authstore.Store) *Authenticator {
	return &Authenticator{
		resolver: resolver,
		flow:     flow,
		store:    store,
	}
}

// writeCtx is the canonical wrapper for any store call that must survive
// client disconnect. WithoutCancel preserves request-scoped values (trace
// IDs etc.) while dropping cancel + deadline; the 5 s timeout caps how long
// a Postgres or pool stall can keep a goroutine alive.
//
// Acceptable parent values:
//   - r.Context()           — request-scoped, may already be done; writeCtx
//                             still gives the write a fresh 5 s budget
//   - context.Background()  — explicit when the request ctx is irrelevant
func (a *Authenticator) writeCtx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), storeWriteTimeout)
}

// CommanderIdentity authenticates a /api/commander/* request.
//   - cookie hits store.GetSession; on non-ErrNotFound store error → fail
//     closed (do NOT widen the attack surface via Bearer)
//   - ErrNotFound or no cookie → fall through to Bearer
func (a *Authenticator) CommanderIdentity(r *http.Request) (identity.Identity, bool) {
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		sess, err := a.store.GetSession(r.Context(), c.Value)
		switch {
		case err == nil:
			return sess.Identity, true
		case errors.Is(err, authstore.ErrNotFound):
			// fall through to Bearer fallback below
		default:
			log.Printf("commanderhub: GetSession error: %v", err)
			return identity.Identity{}, false
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

// ServeLogin: POST /api/commander/login → starts device flow, returns verify URL.
//
// Flow (see design § 6):
//  1. lid := randomID()
//  2. ReserveLogin (advisory-lock-serialized cap check + insert reservation)
//  3. RequestCode using r.Context() (so client cancel really cancels upstream)
//  4. FinalizeReservedLogin using writeCtx(r.Context()) — unkillable write
//  5. If anything past step 2 fails or client cancelled, DeleteLogin with
//     writeCtx(context.Background()) so cleanup uses a fresh budget
func (a *Authenticator) ServeLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	lid := randomID()
	now := time.Now()

	if err := a.store.ReserveLogin(r.Context(), lid, now, loginTTL); err != nil {
		if errors.Is(err, authstore.ErrCapped) {
			http.Error(w, "too many pending logins", http.StatusTooManyRequests)
			return
		}
		http.Error(w, "store unavailable", http.StatusBadGateway)
		return
	}

	// Reservation owned; any failure past this point requires cleanup.
	cleanup := func() {
		ctx, cancel := a.writeCtx(context.Background())
		defer cancel()
		if err := a.store.DeleteLogin(ctx, lid); err != nil {
			log.Printf("commanderhub: post-reserve DeleteLogin(%s) failed: %v", lid, err)
		}
	}

	dc, err := a.flow.RequestCode(r.Context())
	if err != nil {
		cleanup()
		http.Error(w, "device flow: "+string(authstore.SanitizeFailure(err)), http.StatusBadGateway)
		return
	}
	if r.Context().Err() != nil {
		// Client gave up between ReserveLogin and now; abandon.
		cleanup()
		return
	}

	wctx, cancel := a.writeCtx(r.Context())
	defer cancel()
	if err := a.store.FinalizeReservedLogin(wctx, lid,
		dc.Code, time.Now().Add(dc.ExpiresIn),
		int(dc.Interval/time.Second)); err != nil {
		cleanup()
		if errors.Is(err, authstore.ErrNotFound) {
			http.Error(w, "login expired during init", http.StatusBadGateway)
		} else {
			http.Error(w, "store unavailable", http.StatusBadGateway)
		}
		return
	}

	writeJSON(w, map[string]any{
		"verification_uri_complete": dc.VerificationURIComplete,
		"login_id":                  lid,
		"expires_in":                int(dc.ExpiresIn / time.Second),
	})
}

// ServeLoginPoll: GET /api/commander/login/poll?id=<login_id>.
//
// New design (see § 6): [C1] success Set-Cookie + 200 ok inline. [B] only
// services rare "another pod wrote terminal" cases and degrades to 401
// "authorization expired". Plaintext sid never crosses pods.
func (a *Authenticator) ServeLoginPoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	lid := r.URL.Query().Get("id")
	if lid == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}

	rec, err := a.store.GetLogin(r.Context(), lid)
	if errors.Is(err, authstore.ErrNotFound) {
		http.Error(w, "unknown login", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "store unavailable", http.StatusBadGateway)
		return
	}

	now := time.Now()
	if !rec.ExpiresAt.After(now) {
		// [A3] expired — best-effort consume so the row goes away even if
		// the sweeper hasn't run yet.
		cctx, cancel := a.writeCtx(r.Context())
		_, _ = a.store.ConsumeLogin(cctx, lid)
		cancel()
		http.Error(w, "unknown login", http.StatusNotFound)
		return
	}

	// [B] terminal: consume one-shot and respond.
	if rec.SessionIDHash != "" || rec.Failure != "" {
		cctx, cancel := a.writeCtx(r.Context())
		consumed, cerr := a.store.ConsumeLogin(cctx, lid)
		cancel()
		if errors.Is(cerr, authstore.ErrNotFound) {
			http.Error(w, "unknown login", http.StatusNotFound)
			return
		}
		if cerr != nil {
			http.Error(w, "store unavailable", http.StatusBadGateway)
			return
		}
		if consumed.Failure != "" {
			writeJSONStatus(w, http.StatusUnauthorized, map[string]any{
				"status": "error", "error": string(consumed.Failure),
			})
			return
		}
		// Done on another pod / lost-response replay — we don't have the
		// plaintext sid, so the user must reinitiate.
		writeJSONStatus(w, http.StatusUnauthorized, map[string]any{
			"status": "error", "error": string(authstore.FailureAuthorizationExpired),
		})
		return
	}

	// [A4] reserved (RequestCode hasn't returned yet from the pod that
	// owns the POST /login) — keep frontend polling.
	if rec.DeviceCode == "" {
		writeJSON(w, map[string]any{"status": "pending"})
		return
	}

	// [C-throttle] honor agentserver-derived next_poll_at.
	if rec.NextPollAt.After(now) {
		writeJSON(w, map[string]any{"status": "pending"})
		return
	}

	// [C] do a single PollOnce.
	pollCtx, pollCancel := context.WithTimeout(r.Context(), pollOnceTimeout)
	defer pollCancel()
	dc := DeviceCode{
		Code:      rec.DeviceCode,
		ExpiresIn: time.Until(rec.CodeExpiresAt),
		Interval:  time.Duration(rec.IntervalSeconds) * time.Second,
	}
	tok, ready, retryable, slowDown, perr := a.flow.PollOnce(pollCtx, dc)

	switch {
	case ready:
		// [C1] success: parse id_token, persist, set cookie.
		ident, idErr := identityFromIDToken(tok.IDToken, time.Now())
		if idErr != nil {
			wctx, wcancel := a.writeCtx(r.Context())
			if err := a.store.MarkLoginFailed(wctx, lid,
				authstore.SanitizeFailure(authstore.ErrIDTokenInvalid)); err != nil &&
				!errors.Is(err, authstore.ErrNotFound) {
				log.Printf("commanderhub: MarkLoginFailed(%s) failed: %v", lid, err)
			}
			wcancel()
			writeJSONStatus(w, http.StatusUnauthorized, map[string]any{
				"status": "error", "error": string(authstore.FailureIDTokenInvalid),
			})
			return
		}
		sid := randomID()
		wctx, wcancel := a.writeCtx(r.Context())
		mderr := a.store.MarkLoginDone(wctx, lid, authstore.SessionRecord{
			PlaintextSessionID: sid,
			Identity:           ident,
			ExpiresAt:          time.Now().Add(sessionTTL),
		})
		wcancel()
		if errors.Is(mderr, authstore.ErrNotFound) {
			// Another pod already finalized this login. Our sid was never
			// committed; nothing to clean up. Tell the user to retry.
			writeJSONStatus(w, http.StatusUnauthorized, map[string]any{
				"status": "error", "error": string(authstore.FailureAuthorizationExpired),
			})
			return
		}
		if mderr != nil {
			http.Error(w, "store unavailable", http.StatusBadGateway)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    sid,
			Path:     "/",
			HttpOnly: true,
			Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int(sessionTTL / time.Second),
		})
		writeJSON(w, map[string]any{"status": "ok"})
		return

	case retryable:
		// [C2] pending or slow_down — update throttle.
		nextInterval := rec.IntervalSeconds
		if slowDown {
			nextInterval += 5
		}
		if nextInterval < authstore.MinIntervalSeconds {
			nextInterval = authstore.MinIntervalSeconds
		}
		wctx, wcancel := a.writeCtx(r.Context())
		if err := a.store.SetPollThrottle(wctx, lid, nextInterval,
			time.Now().Add(time.Duration(nextInterval)*time.Second)); err != nil {
			log.Printf("commanderhub: SetPollThrottle(%s) failed: %v", lid, err)
		}
		wcancel()
		writeJSON(w, map[string]any{"status": "pending"})
		return

	default:
		// [C3] terminal upstream failure.
		fail := authstore.SanitizeFailure(perr)
		wctx, wcancel := a.writeCtx(r.Context())
		if err := a.store.MarkLoginFailed(wctx, lid, fail); err != nil &&
			!errors.Is(err, authstore.ErrNotFound) {
			log.Printf("commanderhub: MarkLoginFailed(%s) failed: %v", lid, err)
		}
		wcancel()
		writeJSONStatus(w, http.StatusUnauthorized, map[string]any{
			"status": "error", "error": string(fail),
		})
		return
	}
}

// ServeLogout: POST /api/commander/logout.
func (a *Authenticator) ServeLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		ctx, cancel := a.writeCtx(r.Context())
		if err := a.store.DeleteSession(ctx, c.Value); err != nil {
			log.Printf("commanderhub: DeleteSession failed: %v", err)
		}
		cancel()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, map[string]any{"status": "ok"})
}

// putSession is a test helper kept for compatibility with cross-file tests
// (http_test.go uses it to bypass the device flow when only the cookie path
// matters). It exercises the same store.MarkLoginDone path the production
// flow uses, so the test surface is real.
//
// Returns the plaintext sid (what the cookie carries). _token is unused —
// commander no longer persists access_token; the parameter is kept so test
// call sites do not need to change.
func (a *Authenticator) putSession(_token string, ident identity.Identity) string {
	ctx := context.Background()
	lid := "test-seed-" + randomID()
	if err := a.store.ReserveLogin(ctx, lid, time.Now(), loginTTL); err != nil {
		panic(err)
	}
	if err := a.store.FinalizeReservedLogin(ctx, lid, "dc",
		time.Now().Add(5*time.Minute), 5); err != nil {
		panic(err)
	}
	sid := randomID()
	if err := a.store.MarkLoginDone(ctx, lid, authstore.SessionRecord{
		PlaintextSessionID: sid,
		Identity:           ident,
		ExpiresAt:          time.Now().Add(sessionTTL),
	}); err != nil {
		panic(err)
	}
	return sid
}

// runSweep is the per-pod sweep loop. Launched as a goroutine from MountAll.
// Tied to the process lifetime; observer-server has no graceful shutdown
// path that this would benefit from.
func (a *Authenticator) runSweep(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		sweepCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		loginsDel, sessionsDel, err := a.store.SweepExpired(sweepCtx)
		cancel()
		if err != nil {
			log.Printf("commanderhub: sweep error: %v", err)
			continue
		}
		if loginsDel > 0 || sessionsDel > 0 {
			log.Printf("commanderhub: sweep removed %d logins, %d sessions",
				loginsDel, sessionsDel)
		}
	}
}

// --- shared helpers (writeJSON also used by http.go in Phase 3) ---

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func randomID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func identityFromIDToken(raw string, now time.Time) (identity.Identity, error) {
	// /api/agent/whoami only accepts agentserver ProxyToken values. Commander
	// web login gets an OAuth token response instead, so the user/workspace
	// owner key comes from the device-flow id_token claims.
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return identity.Identity{}, fmt.Errorf("%w: OAuth token response missing id_token claims", authstore.ErrIDTokenInvalid)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return identity.Identity{}, fmt.Errorf("%w: decode id_token claims: %v", authstore.ErrIDTokenInvalid, err)
	}
	var claims struct {
		Subject       string `json:"sub"`
		WorkspaceID   string `json:"workspace_id"`
		WorkspaceRole string `json:"workspace_role"`
		ExpiresAt     int64  `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return identity.Identity{}, fmt.Errorf("%w: decode id_token claims: %v", authstore.ErrIDTokenInvalid, err)
	}
	if claims.Subject == "" {
		return identity.Identity{}, fmt.Errorf("%w: id_token missing sub", authstore.ErrIDTokenInvalid)
	}
	if claims.WorkspaceID == "" {
		return identity.Identity{}, fmt.Errorf("%w: id_token missing workspace_id", authstore.ErrIDTokenInvalid)
	}
	if claims.ExpiresAt > 0 && !now.Before(time.Unix(claims.ExpiresAt, 0)) {
		return identity.Identity{}, fmt.Errorf("%w: id_token expired", authstore.ErrIDTokenInvalid)
	}
	return identity.Identity{
		UserID:      claims.Subject,
		WorkspaceID: claims.WorkspaceID,
		Role:        claims.WorkspaceRole,
		Source:      identity.SourceAgentserver,
	}, nil
}
