package dispatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/contract"
	"github.com/yourorg/multi-agent/internal/observerstore"
)

// openSQLiteForDispatchTest opens a fresh observerstore SQLite file (with the
// embedded schema applied, including the route_reasons table) and returns its
// *sql.DB. Cleanup closes the store.
func openSQLiteForDispatchTest(t *testing.T) *sql.DB {
	t.Helper()
	p := filepath.Join(t.TempDir(), "obs.db")
	st, err := observerstore.OpenSQLite(p)
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })
	return st.DB()
}

// capture is a Writer that stores every RouteDecision it received.
type capture struct {
	mu  sync.Mutex
	got []RouteDecision
	err error
}

func (c *capture) Write(_ context.Context, d RouteDecision) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, d)
	return c.err
}

func (c *capture) last() RouteDecision {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.got[len(c.got)-1]
}

func TestSanitize_RawSecret_Redacted(t *testing.T) {
	cases := []struct {
		name, in, mustNotContain string
	}{
		{"openai-sk", "leaked: sk-abcdefghijklmnopqrstuv", "sk-abcdefghij"},
		{"anthropic-sk-ant", "tok=sk-ant-api03-AbCdEfGhIjKlMnOpQrStUv", "sk-ant-api03"},
		{"jwt", "tok=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.payload", "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"},
		{"aws-akia", "creds AKIAIOSFODNN7EXAMPLE here", "AKIAIOSFODNN7EXAMPLE"},
		{"github-ghp", "leaked ghp_abcdefghijklmnopqrstuv12345 stuff", "ghp_abcdefghijklmnopqrstuv12345"},
		{"github-gho", "oauth gho_abcdefghijklmnopqrstuv12345 stuff", "gho_abcdefghijklmnopqrstuv12345"},
		{"github-ghs", "server ghs_abcdefghijklmnopqrstuv12345 stuff", "ghs_abcdefghijklmnopqrstuv12345"},
		{"github-ghr", "refresh ghr_abcdefghijklmnopqrstuv12345 stuff", "ghr_abcdefghijklmnopqrstuv12345"},
		{"github-ghu", "user ghu_abcdefghijklmnopqrstuv12345 stuff", "ghu_abcdefghijklmnopqrstuv12345"},
		{"github-pat", "tok=github_pat_11ABCDEFG0xyzABCDEFGHIJ stuff", "github_pat_11ABCDEFG0xyz"},
		{"gitlab-pat", "tok=glpat-xxxxxxxxxxxxxxxxxxxx", "glpat-xxxxxxxxxxxxxxxxxxxx"},
		{"google-api", "GOOG_KEY=AIzaSyA0123456789abcdefghij stuff", "AIzaSyA0123456789abcdefghij"},
		{"slack-xoxb", "slack xoxb-12345-67890-abcdef token", "xoxb-12345-67890-abcdef"},
		{"pem-private", "key:\n-----BEGIN RSA PRIVATE KEY-----\nMIIEvQ...\n-----END RSA PRIVATE KEY-----", "-----BEGIN RSA PRIVATE KEY-----"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := SanitizeReasonText(c.in)
			require.NotContains(t, out, c.mustNotContain, "%s prefix must be redacted", c.name)
			require.Contains(t, out, "[REDACTED]")
		})
	}
}

func TestSanitize_LongerThan256_Truncated(t *testing.T) {
	in := strings.Repeat("x", 1024)
	out := SanitizeReasonText(in)
	require.True(t, strings.HasSuffix(out, "...[truncated]"))
	body := strings.TrimSuffix(out, "...[truncated]")
	require.Equal(t, 256, len([]rune(body)))
}

// TestSanitize_LongerThan256_MultibyteRuneSafe verifies the 256-rune cap is
// rune-counted (not byte-counted): a multibyte-character input must truncate
// to exactly 256 runes and must NOT split a UTF-8 sequence mid-codepoint.
func TestSanitize_LongerThan256_MultibyteRuneSafe(t *testing.T) {
	// 300 "汉" runes (3 bytes each in UTF-8). Truncated body must be 256 runes
	// and round-trip as valid UTF-8.
	in := strings.Repeat("汉", 300)
	out := SanitizeReasonText(in)
	require.True(t, strings.HasSuffix(out, "...[truncated]"))
	body := strings.TrimSuffix(out, "...[truncated]")
	require.Equal(t, 256, len([]rune(body)), "must truncate to 256 RUNES, not bytes")
	require.Equal(t, strings.Repeat("汉", 256), body, "must not split a UTF-8 codepoint mid-sequence")

	// 200 emoji (4 bytes each in UTF-8) — below the rune limit, must round-trip unchanged.
	emoji := strings.Repeat("🚀", 200)
	require.Equal(t, emoji, SanitizeReasonText(emoji), "input ≤256 runes must round-trip even at high byte count")
}

func TestDecisionID_NoParameter(t *testing.T) {
	rt := reflect.TypeOf(NewDecision)
	require.Equal(t, 1, rt.NumIn())
	require.Equal(t, "string", rt.In(0).String())
}

func TestTimestamp_FromMonotonic_NoExternalInject(t *testing.T) {
	rt := reflect.TypeOf(NewDecision)
	for i := 0; i < rt.NumIn(); i++ {
		require.NotEqual(t, "time.Time", rt.In(i).String(),
			"NewDecision must NOT accept an externally-provided time.Time (would break §6 (c))")
	}
	a := NewDecision("c")
	b := NewDecision("c")
	require.False(t, b.DecisionStartedAt.Before(a.DecisionStartedAt),
		"second NewDecision must have >= first's StartedAt (monotonic clock)")
	SetWriter(&capture{})
	t.Cleanup(func() { SetWriter(nil) })
	d := NewDecision("c")
	FinalizeAndEmit(context.Background(), d)
	require.GreaterOrEqual(t, d.DecisionDurationNs, int64(0),
		"DecisionDurationNs must be non-negative (monotonic-clock guarantee)")
}

func TestDecisionID_UniquePerCall(t *testing.T) {
	seen := make(map[string]struct{}, 10000)
	for i := 0; i < 10000; i++ {
		d := NewDecision("same-conv")
		if _, dup := seen[d.DecisionID]; dup {
			t.Fatalf("duplicate DecisionID after %d iterations", i)
		}
		seen[d.DecisionID] = struct{}{}
	}
}

func TestForgery_ConversationID_OverwrittenOnFinalize(t *testing.T) {
	cap := &capture{}
	SetWriter(cap)
	t.Cleanup(func() { SetWriter(nil) })

	d := NewDecision("real-conv")
	d.ConversationID = "FORGED"
	FinalizeAndEmit(context.Background(), d)
	require.Equal(t, "real-conv", cap.last().ConversationID)
}

func TestForgery_StartedAt_OverwrittenOnFinalize(t *testing.T) {
	cap := &capture{}
	SetWriter(cap)
	t.Cleanup(func() { SetWriter(nil) })

	d := NewDecision("c")
	real := d.DecisionStartedAt
	d.DecisionStartedAt = time.Time{}
	FinalizeAndEmit(context.Background(), d)
	require.True(t, cap.last().DecisionStartedAt.Equal(real))
}

func TestForgery_DecisionID_OverwrittenOnFinalize(t *testing.T) {
	cap := &capture{}
	SetWriter(cap)
	t.Cleanup(func() { SetWriter(nil) })

	d := NewDecision("c")
	real := d.DecisionID
	d.DecisionID = "FORGED-ID"
	FinalizeAndEmit(context.Background(), d)
	require.Equal(t, real, cap.last().DecisionID)
}

func TestPeekConversationID(t *testing.T) {
	start := contract.EnvelopeStart
	end := contract.EnvelopeEnd
	cases := []struct{ name, in, want string }{
		{"absent", "hello", ""},
		{"malformed", start + "{bogus}" + end, ""},
		{"present", start + "\n{\"conversation_id\":\"abc-123\",\"version\":1}\n" + end + "\nbody", "abc-123"},
		{"escaped-quotes", start + "\n{\"conversation_id\":\"a\\\"b\"}\n" + end, "a\"b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, peekConversationID(c.in))
		})
	}
}

func TestSetWriter_AtomicValueNoPanic(t *testing.T) {
	SetWriter(&capture{})
	SetWriter(&capture{})
	SetWriter(nil)
	require.True(t, IsNoopWriter())
}

// TestSetWriter_TypedNilResetsToNoop is the round-9 P2 regression guard.
// The plain `w == nil` guard in SetWriter does not catch a typed-nil
// interface (common Go footgun: `var w *observerWriterAdapter;
// SetWriter(w)` reaches SetWriter as a non-nil interface value wrapping
// a nil pointer). Without the reflect check, IsNoopWriter would return
// false and the first Write call would panic. With the check, a
// typed-nil is treated as "reset to noop" — same semantics as `nil`.
func TestSetWriter_TypedNilResetsToNoop(t *testing.T) {
	SetWriter(nil)
	t.Cleanup(func() { SetWriter(nil) })
	require.True(t, IsNoopWriter(), "baseline: nil resets to noop")

	// Typed-nil pointer wrapped in a Writer interface.
	var typedNil *observerWriterAdapter
	SetWriter(typedNil)
	require.True(t, IsNoopWriter(),
		"typed-nil Writer must be treated as noop, not stored as-is")
	// Confirm no panic on Write.
	require.NotPanics(t, func() {
		_ = currentWriter().Write(context.Background(), RouteDecision{})
	})
}

func TestIsNoopWriter(t *testing.T) {
	SetWriter(nil)
	require.True(t, IsNoopWriter())
	SetWriter(&capture{})
	t.Cleanup(func() { SetWriter(nil) })
	require.False(t, IsNoopWriter())
}

// TestPersistedRow_ReasonText_Redacted (#3): end-to-end through the real
// observerstore SQL writer — asserts a ReasonText containing a raw secret
// lands in route_reasons.reason_text as "[REDACTED]" and that
// route_reason_redacted_total expvar was incremented.
func TestPersistedRow_ReasonText_Redacted(t *testing.T) {
	db := openSQLiteForDispatchTest(t)
	SetWriter(WrapRouteWriter(observerstore.NewRouteWriter(db)))
	t.Cleanup(func() { SetWriter(nil) })

	before := routeReasonRedactedTotal.Value()
	d := NewDecision("conv-leak")
	d.SelectedAgentID = "agent-X"
	d.ReasonCode = ReasonCapabilityMatch
	d.ReasonText = "matched; token=sk-abcdefghijklmnop in capability"
	FinalizeAndEmit(context.Background(), d)

	var stored string
	require.NoError(t, db.QueryRow(
		`SELECT reason_text FROM route_reasons WHERE conversation_id=?`, "conv-leak",
	).Scan(&stored))
	require.NotContains(t, stored, "sk-abcdefghij")
	require.Contains(t, stored, "[REDACTED]")
	require.Greater(t, routeReasonRedactedTotal.Value(), before,
		"route_reason_redacted_total expvar must be incremented")
}

// TestCandidatesJSON_NoCapabilitySnapshot (#5) asserts the DECODED
// candidates_json column in route_reasons contains exactly {agent_id, score,
// reason} per candidate — never any additional fields.
func TestCandidatesJSON_NoCapabilitySnapshot(t *testing.T) {
	db := openSQLiteForDispatchTest(t)
	SetWriter(WrapRouteWriter(observerstore.NewRouteWriter(db)))
	t.Cleanup(func() { SetWriter(nil) })

	d := NewDecision("conv-cand")
	d.Candidates = []Candidate{
		{AgentID: "x", Score: 0.5, Reason: ReasonCapabilityMatch},
		{AgentID: "y", Score: 0.0, Reason: ReasonLoadTooHigh},
	}
	d.SelectedAgentID = "x"
	d.ReasonCode = ReasonCapabilityMatch
	FinalizeAndEmit(context.Background(), d)

	var raw string
	require.NoError(t, db.QueryRow(
		`SELECT candidates_json FROM route_reasons WHERE conversation_id=?`, "conv-cand",
	).Scan(&raw))
	var arr []map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &arr))
	require.Len(t, arr, 2)
	for _, c := range arr {
		keys := make([]string, 0, len(c))
		for k := range c {
			keys = append(keys, k)
		}
		require.ElementsMatch(t, []string{"agent_id", "score", "reason"}, keys,
			"candidates_json must contain ONLY agent_id, score, reason — no capability snapshot")
	}
}

// rwFunc is a function-type adapter implementing observerstore.RouteWriter.
type rwFunc func(context.Context, observerstore.RouteReasonRow) error

func (f rwFunc) WriteRouteReason(ctx context.Context, r observerstore.RouteReasonRow) error {
	return f(ctx, r)
}

func TestWrapRouteWriter_PassesAllFields(t *testing.T) {
	var got observerstore.RouteReasonRow
	w := WrapRouteWriter(rwFunc(func(_ context.Context, r observerstore.RouteReasonRow) error {
		got = r
		return nil
	}))
	d := NewDecision("conv-xyz")
	d.SelectedAgentID = "agent-X"
	d.SelectedNone = false
	d.ReasonCode = ReasonCapabilityMatch
	d.ReasonText = "all-fields-must-round-trip"
	d.Candidates = []Candidate{
		{AgentID: "agent-X", Score: 1.0, Reason: ReasonCapabilityMatch},
		{AgentID: "agent-Y", Score: 0.25, Reason: ReasonLoadTooHigh},
	}
	FinalizeAndEmit(context.Background(), d)
	require.NoError(t, w.Write(context.Background(), *d))

	require.Equal(t, d.DecisionID, got.DecisionID)
	require.Equal(t, d.ConversationID, got.ConversationID)
	require.Equal(t, d.SelectedAgentID, got.SelectedAgentID)
	require.Equal(t, string(d.ReasonCode), got.ReasonCode)
	require.Equal(t, d.ReasonText, got.ReasonText)
	require.Equal(t, len(d.Candidates), len(got.Candidates))
	for i := range d.Candidates {
		require.Equal(t, d.Candidates[i].AgentID, got.Candidates[i].AgentID)
		require.Equal(t, d.Candidates[i].Score, got.Candidates[i].Score)
		require.Equal(t, string(d.Candidates[i].Reason), got.Candidates[i].Reason)
	}
	require.True(t, d.DecisionStartedAt.Equal(got.DecisionStartedAt))
	require.True(t, d.DecisionEndedAt.Equal(got.DecisionEndedAt))
	require.Equal(t, d.DecisionDurationNs, got.DecisionDurationNs)
}

func TestWrapRouteWriter_SelectedNoneSentinel(t *testing.T) {
	var got observerstore.RouteReasonRow
	w := WrapRouteWriter(rwFunc(func(_ context.Context, r observerstore.RouteReasonRow) error {
		got = r
		return nil
	}))
	d := NewDecision("c")
	d.SelectedNone = true
	d.SelectedAgentID = "" // would be ambiguous without SelectedNone
	FinalizeAndEmit(context.Background(), d)
	require.NoError(t, w.Write(context.Background(), *d))
	require.Equal(t, "<none>", got.SelectedAgentID)
}

func TestPersistedRow_SelectedNoneSerialization(t *testing.T) {
	// (a) SelectedNone=true → persisted "<none>".
	dbA := openSQLiteForDispatchTest(t)
	SetWriter(WrapRouteWriter(observerstore.NewRouteWriter(dbA)))
	t.Cleanup(func() { SetWriter(nil) })

	da := NewDecision("conv-none")
	da.SelectedNone = true
	da.ReasonCode = ReasonNoCapabilityMatch
	FinalizeAndEmit(context.Background(), da)
	var got string
	require.NoError(t, dbA.QueryRow(
		`SELECT selected_agent_id FROM route_reasons WHERE conversation_id=?`, "conv-none",
	).Scan(&got))
	require.Equal(t, "<none>", got)

	// (b) SelectedNone=false, SelectedAgentID="" (fallback executor selected) → persisted "".
	dbB := openSQLiteForDispatchTest(t)
	SetWriter(WrapRouteWriter(observerstore.NewRouteWriter(dbB)))
	dec := NewDecision("conv-fallback")
	dec.SelectedNone = false
	dec.SelectedAgentID = ""
	dec.ReasonCode = ReasonCapabilityMatch
	FinalizeAndEmit(context.Background(), dec)
	var got2 string
	require.NoError(t, dbB.QueryRow(
		`SELECT selected_agent_id FROM route_reasons WHERE conversation_id=?`, "conv-fallback",
	).Scan(&got2))
	require.Equal(t, "", got2)
}

func TestWriter_Errors_AreReturnedNotPanicked(t *testing.T) {
	cap := &capture{err: errors.New("boom")}
	SetWriter(cap)
	t.Cleanup(func() { SetWriter(nil) })
	d := NewDecision("c")
	// FinalizeAndEmit must not panic on writer error.
	FinalizeAndEmit(context.Background(), d)
}
