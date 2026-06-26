package authstore

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/identity"
)

// RunConformanceTests drives every Store contract assertion. Both
// inmemoryStore and postgresStore must pass it; divergence between the two
// implementations is a Stage 3 blocker.
//
// `newStore` returns a fresh, empty Store. Postgres callers should TRUNCATE
// the two tables in their factory; inmemory callers just return
// NewInMemoryStore().
func RunConformanceTests(t *testing.T, newStore func(t *testing.T) Store) {
	t.Helper()

	mkIdentity := func() identity.Identity {
		return identity.Identity{
			UserID:      "u-test",
			WorkspaceID: "ws-test",
			Role:        "member",
			Source:      identity.SourceAgentserver,
		}
	}

	t.Run("ReserveLogin_basic", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ReserveLogin(ctx, "lid1", time.Now(), 10*time.Minute))
		rec, err := s.GetLogin(ctx, "lid1")
		require.NoError(t, err)
		require.Equal(t, "", rec.DeviceCode)
		require.WithinDuration(t, time.Now().Add(10*time.Minute), rec.ExpiresAt, 5*time.Second)
	})

	t.Run("ReserveLogin_capped_then_sweep_releases", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		for i := 0; i < MaxActiveLogins; i++ {
			require.NoError(t, s.ReserveLogin(ctx, fmt.Sprintf("lid%d", i),
				time.Now(), 50*time.Millisecond))
		}
		err := s.ReserveLogin(ctx, "overflow", time.Now(), 10*time.Minute)
		require.ErrorIs(t, err, ErrCapped)

		time.Sleep(150 * time.Millisecond)

		require.NoError(t, s.ReserveLogin(ctx, "after-sweep", time.Now(), 10*time.Minute))
	})

	t.Run("FinalizeReservedLogin_OK_then_double_call_NotFound", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ReserveLogin(ctx, "lid", time.Now(), 10*time.Minute))
		require.NoError(t, s.FinalizeReservedLogin(ctx, "lid",
			"dc-1", time.Now().Add(5*time.Minute), 5))
		// Second call: row is no longer in reserved state.
		err := s.FinalizeReservedLogin(ctx, "lid",
			"dc-2", time.Now().Add(5*time.Minute), 5)
		require.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("FinalizeReservedLogin_intervalSeconds_below_5_clamped", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ReserveLogin(ctx, "lid", time.Now(), 10*time.Minute))
		require.NoError(t, s.FinalizeReservedLogin(ctx, "lid",
			"dc", time.Now().Add(5*time.Minute), 0))
		rec, err := s.GetLogin(ctx, "lid")
		require.NoError(t, err)
		require.GreaterOrEqual(t, rec.IntervalSeconds, MinIntervalSeconds)
	})

	t.Run("DeleteLogin_idempotent_and_frees_cap", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.DeleteLogin(ctx, "missing")) // idempotent
		require.NoError(t, s.ReserveLogin(ctx, "lid", time.Now(), 10*time.Minute))
		require.NoError(t, s.DeleteLogin(ctx, "lid"))
		// Confirm slot freed: should still be able to reserve again immediately.
		require.NoError(t, s.ReserveLogin(ctx, "lid", time.Now(), 10*time.Minute))
	})

	t.Run("GetLogin_missing_NotFound", func(t *testing.T) {
		s := newStore(t)
		_, err := s.GetLogin(context.Background(), "nope")
		require.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("SetPollThrottle_writes_both_fields", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ReserveLogin(ctx, "lid", time.Now(), 10*time.Minute))
		require.NoError(t, s.FinalizeReservedLogin(ctx, "lid",
			"dc", time.Now().Add(5*time.Minute), 5))
		future := time.Now().Add(30 * time.Second)
		require.NoError(t, s.SetPollThrottle(ctx, "lid", 60, future))
		rec, err := s.GetLogin(ctx, "lid")
		require.NoError(t, err)
		require.Equal(t, 60, rec.IntervalSeconds)
		require.WithinDuration(t, future, rec.NextPollAt, 2*time.Second)
	})

	t.Run("SetPollThrottle_missing_lid_returns_nil", func(t *testing.T) {
		require.NoError(t, newStore(t).SetPollThrottle(context.Background(),
			"missing", 30, time.Now().Add(time.Minute)))
	})

	t.Run("MarkLoginDone_OK_writes_session", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ReserveLogin(ctx, "lid", time.Now(), 10*time.Minute))
		require.NoError(t, s.FinalizeReservedLogin(ctx, "lid",
			"dc", time.Now().Add(5*time.Minute), 5))
		require.NoError(t, s.MarkLoginDone(ctx, "lid", SessionRecord{
			PlaintextSessionID: "my-sid",
			Identity:           mkIdentity(),
			ExpiresAt:          time.Now().Add(12 * time.Hour),
		}))
		sess, err := s.GetSession(ctx, "my-sid")
		require.NoError(t, err)
		require.Equal(t, "u-test", sess.Identity.UserID)
		require.Equal(t, "ws-test", sess.Identity.WorkspaceID)
		require.Equal(t, identity.SourceAgentserver, sess.Identity.Source)
	})

	t.Run("MarkLoginDone_terminal_existing_NotFound", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ReserveLogin(ctx, "lid", time.Now(), 10*time.Minute))
		require.NoError(t, s.FinalizeReservedLogin(ctx, "lid",
			"dc", time.Now().Add(5*time.Minute), 5))
		require.NoError(t, s.MarkLoginDone(ctx, "lid", SessionRecord{
			PlaintextSessionID: "first-sid",
			Identity:           mkIdentity(),
			ExpiresAt:          time.Now().Add(12 * time.Hour),
		}))
		// Second call with different sid: row is now terminal-done.
		err := s.MarkLoginDone(ctx, "lid", SessionRecord{
			PlaintextSessionID: "second-sid",
			Identity:           mkIdentity(),
			ExpiresAt:          time.Now().Add(12 * time.Hour),
		})
		require.ErrorIs(t, err, ErrNotFound)
		_, err = s.GetSession(ctx, "second-sid")
		require.ErrorIs(t, err, ErrNotFound, "second sid must not exist as session")
	})

	t.Run("MarkLoginDone_on_expired_login_NotFound_no_session_insert", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ReserveLogin(ctx, "expired-pending", time.Now(), 50*time.Millisecond))
		require.NoError(t, s.FinalizeReservedLogin(ctx, "expired-pending",
			"dc-x", time.Now().Add(5*time.Minute), 5))
		time.Sleep(150 * time.Millisecond)
		err := s.MarkLoginDone(ctx, "expired-pending", SessionRecord{
			PlaintextSessionID: "should-not-stick",
			Identity:           mkIdentity(),
			ExpiresAt:          time.Now().Add(12 * time.Hour),
		})
		require.ErrorIs(t, err, ErrNotFound)
		_, err = s.GetSession(ctx, "should-not-stick")
		require.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("MarkLoginDone_on_failed_login_NotFound", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ReserveLogin(ctx, "lid", time.Now(), 10*time.Minute))
		require.NoError(t, s.FinalizeReservedLogin(ctx, "lid",
			"dc", time.Now().Add(5*time.Minute), 5))
		require.NoError(t, s.MarkLoginFailed(ctx, "lid", FailureAuthorizationDenied))
		err := s.MarkLoginDone(ctx, "lid", SessionRecord{
			PlaintextSessionID: "any",
			Identity:           mkIdentity(),
			ExpiresAt:          time.Now().Add(12 * time.Hour),
		})
		require.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("MarkLoginDone_on_reserved_login_NotFound", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ReserveLogin(ctx, "lid", time.Now(), 10*time.Minute))
		// no FinalizeReservedLogin → device_code == "" → MarkLoginDone forbidden
		err := s.MarkLoginDone(ctx, "lid", SessionRecord{
			PlaintextSessionID: "any",
			Identity:           mkIdentity(),
			ExpiresAt:          time.Now().Add(12 * time.Hour),
		})
		require.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("MarkLoginDone_strong_consistency_concurrent", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		lid := "concurrent-lid"
		require.NoError(t, s.ReserveLogin(ctx, lid, time.Now(), 10*time.Minute))
		require.NoError(t, s.FinalizeReservedLogin(ctx, lid, "dc-1",
			time.Now().Add(5*time.Minute), 5))

		const N = 20
		var wg sync.WaitGroup
		wg.Add(N)
		results := make([]error, N)
		sids := make([]string, N)
		start := make(chan struct{})
		for i := 0; i < N; i++ {
			sids[i] = fmt.Sprintf("plain-sid-%02d", i)
			go func(i int) {
				defer wg.Done()
				<-start
				results[i] = s.MarkLoginDone(ctx, lid, SessionRecord{
					PlaintextSessionID: sids[i],
					Identity:           mkIdentity(),
					ExpiresAt:          time.Now().Add(12 * time.Hour),
				})
			}(i)
		}
		close(start)
		wg.Wait()

		wins := 0
		for _, r := range results {
			if r == nil {
				wins++
			} else {
				require.ErrorIs(t, r, ErrNotFound)
			}
		}
		require.Equal(t, 1, wins, "exactly one MarkLoginDone must succeed")

		hits := 0
		for _, sid := range sids {
			_, err := s.GetSession(ctx, sid)
			if err == nil {
				hits++
			} else {
				require.ErrorIs(t, err, ErrNotFound)
			}
		}
		require.Equal(t, 1, hits, "no orphan sessions left in DB")
	})

	t.Run("MarkLoginFailed_OK_then_double_call_NotFound", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ReserveLogin(ctx, "lid", time.Now(), 10*time.Minute))
		require.NoError(t, s.FinalizeReservedLogin(ctx, "lid",
			"dc", time.Now().Add(5*time.Minute), 5))
		require.NoError(t, s.MarkLoginFailed(ctx, "lid", FailureDeviceFlow))
		err := s.MarkLoginFailed(ctx, "lid", FailureAuthorizationDenied)
		require.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("MarkLoginFailed_with_invalid_Failure_value_rejected", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ReserveLogin(ctx, "bad-fail", time.Now(), 10*time.Minute))
		require.NoError(t, s.FinalizeReservedLogin(ctx, "bad-fail",
			"dc-y", time.Now().Add(5*time.Minute), 5))
		err := s.MarkLoginFailed(ctx, "bad-fail", Failure("custom raw error not in enum"))
		require.ErrorIs(t, err, ErrInvalidFailure)
		rec, err := s.GetLogin(ctx, "bad-fail")
		require.NoError(t, err)
		require.Empty(t, string(rec.Failure))
		require.Equal(t, "", rec.SessionIDHash)
	})

	t.Run("ConsumeLogin_reserved_pending_done_failed_all_consumable", func(t *testing.T) {
		ctx := context.Background()
		tests := []struct {
			name  string
			setup func(s Store)
		}{
			{"reserved", func(s Store) {
				require.NoError(t, s.ReserveLogin(ctx, "lid", time.Now(), 10*time.Minute))
			}},
			{"pending", func(s Store) {
				require.NoError(t, s.ReserveLogin(ctx, "lid", time.Now(), 10*time.Minute))
				require.NoError(t, s.FinalizeReservedLogin(ctx, "lid", "dc",
					time.Now().Add(5*time.Minute), 5))
			}},
			{"done", func(s Store) {
				require.NoError(t, s.ReserveLogin(ctx, "lid", time.Now(), 10*time.Minute))
				require.NoError(t, s.FinalizeReservedLogin(ctx, "lid", "dc",
					time.Now().Add(5*time.Minute), 5))
				require.NoError(t, s.MarkLoginDone(ctx, "lid", SessionRecord{
					PlaintextSessionID: "sid", Identity: mkIdentity(),
					ExpiresAt: time.Now().Add(12 * time.Hour),
				}))
			}},
			{"failed", func(s Store) {
				require.NoError(t, s.ReserveLogin(ctx, "lid", time.Now(), 10*time.Minute))
				require.NoError(t, s.FinalizeReservedLogin(ctx, "lid", "dc",
					time.Now().Add(5*time.Minute), 5))
				require.NoError(t, s.MarkLoginFailed(ctx, "lid", FailureAuthorizationDenied))
			}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				s := newStore(t)
				tt.setup(s)
				rec, err := s.ConsumeLogin(ctx, "lid")
				require.NoError(t, err)
				require.Equal(t, "lid", rec.LoginID)
				// Second consume → ErrNotFound.
				_, err = s.ConsumeLogin(ctx, "lid")
				require.ErrorIs(t, err, ErrNotFound)
			})
		}
	})

	t.Run("ConsumeLogin_oneshot_concurrent", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		lid := "oneshot-lid"
		require.NoError(t, s.ReserveLogin(ctx, lid, time.Now(), 10*time.Minute))
		require.NoError(t, s.FinalizeReservedLogin(ctx, lid, "dc-x",
			time.Now().Add(5*time.Minute), 5))

		const N = 10
		var wg sync.WaitGroup
		wg.Add(N)
		observed := make([]error, N)
		start := make(chan struct{})
		for i := 0; i < N; i++ {
			go func(i int) {
				defer wg.Done()
				<-start
				_, observed[i] = s.ConsumeLogin(ctx, lid)
			}(i)
		}
		close(start)
		wg.Wait()

		wins := 0
		for _, e := range observed {
			if e == nil {
				wins++
			} else {
				require.ErrorIs(t, e, ErrNotFound)
			}
		}
		require.Equal(t, 1, wins)
	})

	t.Run("GetSession_hash_lookup_works_and_wrong_plaintext_misses", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ReserveLogin(ctx, "lid", time.Now(), 10*time.Minute))
		require.NoError(t, s.FinalizeReservedLogin(ctx, "lid", "dc",
			time.Now().Add(5*time.Minute), 5))
		require.NoError(t, s.MarkLoginDone(ctx, "lid", SessionRecord{
			PlaintextSessionID: "P",
			Identity:           mkIdentity(),
			ExpiresAt:          time.Now().Add(12 * time.Hour),
		}))
		_, err := s.GetSession(ctx, "P")
		require.NoError(t, err)
		_, err = s.GetSession(ctx, "Q")
		require.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("GetSession_expired_NotFound", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ReserveLogin(ctx, "lid", time.Now(), 10*time.Minute))
		require.NoError(t, s.FinalizeReservedLogin(ctx, "lid", "dc",
			time.Now().Add(5*time.Minute), 5))
		require.NoError(t, s.MarkLoginDone(ctx, "lid", SessionRecord{
			PlaintextSessionID: "S",
			Identity:           mkIdentity(),
			ExpiresAt:          time.Now().Add(-1 * time.Second), // already expired
		}))
		_, err := s.GetSession(ctx, "S")
		require.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("DeleteSession_then_GetSession_NotFound", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		require.NoError(t, s.ReserveLogin(ctx, "lid", time.Now(), 10*time.Minute))
		require.NoError(t, s.FinalizeReservedLogin(ctx, "lid", "dc",
			time.Now().Add(5*time.Minute), 5))
		require.NoError(t, s.MarkLoginDone(ctx, "lid", SessionRecord{
			PlaintextSessionID: "S",
			Identity:           mkIdentity(),
			ExpiresAt:          time.Now().Add(12 * time.Hour),
		}))
		require.NoError(t, s.DeleteSession(ctx, "S"))
		_, err := s.GetSession(ctx, "S")
		require.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("DeleteSession_missing_idempotent_nil", func(t *testing.T) {
		require.NoError(t, newStore(t).DeleteSession(context.Background(), "missing"))
	})

	t.Run("SweepExpired_deletes_expired_only_correct_counts", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		// 3 expired logins.
		for i := 0; i < 3; i++ {
			lid := fmt.Sprintf("expired-l-%d", i)
			require.NoError(t, s.ReserveLogin(ctx, lid, time.Now(), 50*time.Millisecond))
		}
		// 2 fresh logins.
		for i := 0; i < 2; i++ {
			lid := fmt.Sprintf("fresh-l-%d", i)
			require.NoError(t, s.ReserveLogin(ctx, lid, time.Now(), 10*time.Minute))
		}

		// 4 expired sessions via tiny-TTL MarkLoginDone.
		for i := 0; i < 4; i++ {
			lid := fmt.Sprintf("expired-s-l-%d", i)
			sid := fmt.Sprintf("expired-sid-%d", i)
			require.NoError(t, s.ReserveLogin(ctx, lid, time.Now(), 10*time.Minute))
			require.NoError(t, s.FinalizeReservedLogin(ctx, lid, "dc",
				time.Now().Add(5*time.Minute), 5))
			require.NoError(t, s.MarkLoginDone(ctx, lid, SessionRecord{
				PlaintextSessionID: sid,
				Identity:           mkIdentity(),
				ExpiresAt:          time.Now().Add(50 * time.Millisecond),
			}))
		}
		// 1 fresh session.
		require.NoError(t, s.ReserveLogin(ctx, "fresh-s-l", time.Now(), 10*time.Minute))
		require.NoError(t, s.FinalizeReservedLogin(ctx, "fresh-s-l", "dc",
			time.Now().Add(5*time.Minute), 5))
		require.NoError(t, s.MarkLoginDone(ctx, "fresh-s-l", SessionRecord{
			PlaintextSessionID: "fresh-sid",
			Identity:           mkIdentity(),
			ExpiresAt:          time.Now().Add(12 * time.Hour),
		}))

		time.Sleep(150 * time.Millisecond)

		ld, sd, err := s.SweepExpired(ctx)
		require.NoError(t, err)
		require.Equal(t, int64(3), ld, "3 expired logins must be swept")
		require.Equal(t, int64(4), sd, "4 expired sessions must be swept")

		// Fresh rows remain.
		for i := 0; i < 2; i++ {
			_, err := s.GetLogin(ctx, fmt.Sprintf("fresh-l-%d", i))
			require.NoError(t, err)
		}
		_, err = s.GetSession(ctx, "fresh-sid")
		require.NoError(t, err)
	})

	t.Run("SweepExpired_empty_tables_returns_zero", func(t *testing.T) {
		ld, sd, err := newStore(t).SweepExpired(context.Background())
		require.NoError(t, err)
		require.Equal(t, int64(0), ld)
		require.Equal(t, int64(0), sd)
	})
}
