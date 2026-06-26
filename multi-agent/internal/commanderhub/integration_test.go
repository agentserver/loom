package commanderhub

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commanderhub/authstore"
	"github.com/yourorg/multi-agent/internal/identity"
)

// TestCrossPodIntegration covers the scenarios from spec §10 that exercise
// state sharing across "pods" — two Authenticators backed by the SAME
// Postgres *sql.DB. DSN-gated with OBSERVER_POSTGRES_TEST_DSN.
func TestCrossPodIntegration(t *testing.T) {
	dsn := os.Getenv("OBSERVER_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set OBSERVER_POSTGRES_TEST_DSN to run cross-pod integration tests")
	}

	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, authstore.MigratePostgres(db))

	reset := func(t *testing.T) {
		t.Helper()
		_, err := db.ExecContext(context.Background(),
			`TRUNCATE commander_logins, commander_sessions`)
		require.NoError(t, err)
	}

	mkResolver := func() *fakeResolver {
		return &fakeResolver{mu: map[string]identity.Identity{
			"tok-bearer": {UserID: "u", WorkspaceID: "w", Source: identity.SourceAgentserver},
		}}
	}

	mkPod := func(t *testing.T, flow deviceFlow) (*Authenticator, *httptest.Server) {
		t.Helper()
		store := authstore.NewPostgresStore(db)
		auth := newAuthenticatorWithFlow(mkResolver(), flow, store)
		mux := http.NewServeMux()
		Mount(mux, nil, auth) // only auth routes; hub paths not exercised here
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		return auth, srv
	}

	// Helper: POST /login on srv, returns login_id.
	startLogin := func(t *testing.T, srv *httptest.Server) string {
		t.Helper()
		resp, err := http.Post(srv.URL+"/api/commander/login", "", nil)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var lr struct {
			LoginID string `json:"login_id"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&lr))
		require.NotEmpty(t, lr.LoginID)
		return lr.LoginID
	}

	// Helper: GET /poll, returns (status, body, cookie-or-nil).
	pollOnce := func(t *testing.T, srv *httptest.Server, lid string) (int, string, *http.Cookie) {
		t.Helper()
		resp, err := http.Get(srv.URL + "/api/commander/login/poll?id=" + lid)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var cookie *http.Cookie
		for _, c := range resp.Cookies() {
			if c.Name == sessionCookieName {
				cookie = c
				break
			}
		}
		return resp.StatusCode, string(body), cookie
	}

	t.Run("subcase1_lid_on_A_polled_on_B_completes_on_B", func(t *testing.T) {
		reset(t)
		flow := newFakeDeviceFlow("alice", "W1")
		_, podA := mkPod(t, flow)
		_, podB := mkPod(t, flow)

		lid := startLogin(t, podA)
		// First poll on B → pending (PollOnce returns retryable).
		status, body, cookie := pollOnce(t, podB, lid)
		require.Equal(t, http.StatusOK, status)
		require.Contains(t, body, `"pending"`)
		require.Nil(t, cookie)

		// Clear throttle so next poll calls PollOnce again.
		require.NoError(t, authstore.NewPostgresStore(db).SetPollThrottle(
			context.Background(), lid, 5, time.Now()))

		flow.approve()
		// Poll on B again → completes on B.
		status, body, cookie = pollOnce(t, podB, lid)
		require.Equal(t, http.StatusOK, status)
		require.Contains(t, body, `"ok"`)
		require.NotNil(t, cookie)
	})

	t.Run("subcase2_cookie_from_A_works_on_B_for_GetSession", func(t *testing.T) {
		reset(t)
		flow := newFakeDeviceFlow("alice", "W1")
		authA, podA := mkPod(t, flow)
		authB, _ := mkPod(t, flow)

		lid := startLogin(t, podA)
		require.NoError(t, authstore.NewPostgresStore(db).SetPollThrottle(
			context.Background(), lid, 5, time.Now()))
		flow.approve()
		status, _, cookie := pollOnce(t, podA, lid)
		require.Equal(t, http.StatusOK, status)
		require.NotNil(t, cookie)

		// authB CommanderIdentity with the cookie issued on authA.
		_ = authA // unused except to prove the cookie crossed pods
		req := httptest.NewRequest(http.MethodGet, "/api/commander/daemons", nil)
		req.AddCookie(cookie)
		ident, ok := authB.CommanderIdentity(req)
		require.True(t, ok, "cookie issued on pod A must authenticate on pod B")
		require.Equal(t, "alice", ident.UserID)
		require.Equal(t, "W1", ident.WorkspaceID)
	})

	t.Run("subcase3_logout_on_A_invalidates_cookie_on_B", func(t *testing.T) {
		reset(t)
		flow := newFakeDeviceFlow("alice", "W1")
		authA, _ := mkPod(t, flow)
		authB, _ := mkPod(t, flow)

		sid := authA.putSession("ignored", identity.Identity{
			UserID: "alice", WorkspaceID: "W1", Source: identity.SourceAgentserver,
		})
		cookie := &http.Cookie{Name: sessionCookieName, Value: sid}

		// Confirm B sees the session before logout.
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(cookie)
		_, ok := authB.CommanderIdentity(req)
		require.True(t, ok)

		// Logout on A.
		out := httptest.NewRecorder()
		logoutReq := httptest.NewRequest(http.MethodPost, "/api/commander/logout", nil)
		logoutReq.AddCookie(cookie)
		authA.ServeLogout(out, logoutReq)
		require.Equal(t, http.StatusOK, out.Code)

		// B no longer accepts the cookie.
		req2 := httptest.NewRequest(http.MethodGet, "/", nil)
		req2.AddCookie(cookie)
		_, ok = authB.CommanderIdentity(req2)
		require.False(t, ok, "cookie logged out on pod A must be rejected by pod B")
	})

	t.Run("subcase4_concurrent_MarkLoginDone_exactly_one_winner", func(t *testing.T) {
		reset(t)
		flow := newFakeDeviceFlow("alice", "W1")
		authA, _ := mkPod(t, flow)
		authB, _ := mkPod(t, flow)
		ctx := context.Background()
		store := authstore.NewPostgresStore(db)

		require.NoError(t, store.ReserveLogin(ctx, "race-lid", time.Now(), 10*time.Minute))
		require.NoError(t, store.FinalizeReservedLogin(ctx, "race-lid",
			"dc", time.Now().Add(5*time.Minute), 5))

		var wg sync.WaitGroup
		wg.Add(2)
		var errA, errB error
		start := make(chan struct{})
		go func() {
			defer wg.Done()
			<-start
			errA = authA.store.MarkLoginDone(ctx, "race-lid", authstore.SessionRecord{
				PlaintextSessionID: "sidA",
				Identity: identity.Identity{
					UserID: "alice", WorkspaceID: "W1", Source: identity.SourceAgentserver,
				},
				ExpiresAt: time.Now().Add(12 * time.Hour),
			})
		}()
		go func() {
			defer wg.Done()
			<-start
			errB = authB.store.MarkLoginDone(ctx, "race-lid", authstore.SessionRecord{
				PlaintextSessionID: "sidB",
				Identity: identity.Identity{
					UserID: "alice", WorkspaceID: "W1", Source: identity.SourceAgentserver,
				},
				ExpiresAt: time.Now().Add(12 * time.Hour),
			})
		}()
		close(start)
		wg.Wait()

		// Exactly one win.
		wins := 0
		if errA == nil {
			wins++
		}
		if errB == nil {
			wins++
		}
		require.Equal(t, 1, wins)

		// Exactly one session row.
		hits := 0
		for _, sid := range []string{"sidA", "sidB"} {
			_, err := store.GetSession(ctx, sid)
			if err == nil {
				hits++
			}
		}
		require.Equal(t, 1, hits, "no orphan sessions across pods")
	})

	t.Run("subcase5_lost_setcookie_replay_returns_401_authorization_expired", func(t *testing.T) {
		reset(t)
		flow := newFakeDeviceFlow("alice", "W1")
		_, podA := mkPod(t, flow)
		_, podB := mkPod(t, flow)

		lid := startLogin(t, podA)
		require.NoError(t, authstore.NewPostgresStore(db).SetPollThrottle(
			context.Background(), lid, 5, time.Now()))
		flow.approve()
		status, _, cookie := pollOnce(t, podA, lid)
		require.Equal(t, http.StatusOK, status)
		require.NotNil(t, cookie)
		// Discard the cookie — model the client never receiving Set-Cookie.

		// Replay /poll on pod B. Row still has session_id_hash set; [B] consumes
		// and returns 401 "authorization expired".
		status, body, cookie2 := pollOnce(t, podB, lid)
		require.Equal(t, http.StatusUnauthorized, status)
		require.Contains(t, body, string(authstore.FailureAuthorizationExpired))
		require.Nil(t, cookie2)
	})

	t.Run("subcase6_cap_under_high_concurrency_strictly_bounded", func(t *testing.T) {
		reset(t)
		flow := &countingPostFlow{}
		_, podA := mkPod(t, flow)

		const N = authstore.MaxActiveLogins + 76
		var wg sync.WaitGroup
		wg.Add(N)
		statuses := make([]int, N)
		for i := 0; i < N; i++ {
			go func(i int) {
				defer wg.Done()
				resp, err := http.Post(podA.URL+"/api/commander/login", "", nil)
				if err != nil {
					statuses[i] = -1
					return
				}
				statuses[i] = resp.StatusCode
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}(i)
		}
		wg.Wait()

		ok, capped := 0, 0
		for _, s := range statuses {
			switch s {
			case http.StatusOK:
				ok++
			case http.StatusTooManyRequests:
				capped++
			default:
				t.Errorf("unexpected status %d", s)
			}
		}
		require.Equal(t, authstore.MaxActiveLogins, ok,
			"cap must be strictly enforced under concurrency")
		require.Equal(t, 76, capped)
		require.Equal(t, int32(authstore.MaxActiveLogins),
			atomic.LoadInt32(&flow.requestCodeN),
			"RequestCode must run exactly MaxActiveLogins times")
	})
}

// countingPostFlow is a minimal deviceFlow used by the cap-stress subcase.
// PollOnce is never called (no /poll requests issued).
type countingPostFlow struct {
	requestCodeN int32
}

func (f *countingPostFlow) RequestCode(context.Context) (DeviceCode, error) {
	atomic.AddInt32(&f.requestCodeN, 1)
	return DeviceCode{
		Code: "dc-" + fmt.Sprint(time.Now().UnixNano()),
		VerificationURIComplete: strings.Repeat("x", 10),
		ExpiresIn:               5 * time.Minute,
		Interval:                5 * time.Second,
	}, nil
}

func (f *countingPostFlow) PollOnce(context.Context, DeviceCode) (
	loginToken, bool, bool, bool, error,
) {
	return loginToken{}, false, true, false, nil
}
