package driver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBindThread_SetsAndStores verifies a valid call mutates parentThread and
// returns the expected BindResult (carrying the configured agent_id + display_name).
func TestBindThread_SetsAndStores(t *testing.T) {
	tools := newLoomTestTools(t, &fakeSDK{}, "" /*codexHome*/, "drv-1", "prod-driver")
	res, err := tools.BindThread(context.Background(), "019ef3bd-42c8-7731-85b7-7177ae747389")
	require.NoError(t, err)
	require.Equal(t, BindResult{
		Bound:       true,
		ThreadID:    "019ef3bd-42c8-7731-85b7-7177ae747389",
		AgentID:     "drv-1",
		DisplayName: "prod-driver",
	}, res)
	p := tools.parentThread.Load()
	require.NotNil(t, p)
	require.Equal(t, "019ef3bd-42c8-7731-85b7-7177ae747389", *p)
}

// TestBindThread_RejectsInvalidFormat covers every shape the validator must
// reject. None of these may mutate parentThread.
func TestBindThread_RejectsInvalidFormat(t *testing.T) {
	cases := []struct {
		name, in string
	}{
		{"empty", ""},
		{"whitespace_only", "   "},
		{"contains_dollar", "thread$id"},
		{"contains_brace_open", "thread{id"},
		{"contains_brace_close", "thread}id"},
		{"contains_slash", "thread/id"},
		{"contains_newline", "thr\nid"},
		{"unexpanded_placeholder", "${CODEX_THREAD_ID}"},
		{"too_long", strings.Repeat("a", 129)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tools := newLoomTestTools(t, &fakeSDK{}, "", "drv-X", "driver-X")
			res, err := tools.BindThread(context.Background(), tc.in)
			require.Error(t, err, "input %q must be rejected", tc.in)
			require.Contains(t, err.Error(), "invalid thread_id format")
			require.Equal(t, BindResult{}, res)
			require.Nil(t, tools.parentThread.Load(), "parentThread must not be set on rejection")
		})
	}
}

// TestBindThread_Idempotent_SameValue calls with the same id twice; the second
// call must succeed and not change observable state.
func TestBindThread_Idempotent_SameValue(t *testing.T) {
	tools := newLoomTestTools(t, &fakeSDK{}, "", "drv-1", "d1")
	_, err := tools.BindThread(context.Background(), "thr-1")
	require.NoError(t, err)
	_, err = tools.BindThread(context.Background(), "thr-1")
	require.NoError(t, err)
	require.Equal(t, "thr-1", *tools.parentThread.Load())
}

// TestBindThread_Replaces_DifferentValue codifies the /resume semantic: a
// later bind_thread with a NEW value replaces the previously-bound id.
func TestBindThread_Replaces_DifferentValue(t *testing.T) {
	tools := newLoomTestTools(t, &fakeSDK{}, "", "drv-1", "d1")
	_, err := tools.BindThread(context.Background(), "thr-1")
	require.NoError(t, err)
	_, err = tools.BindThread(context.Background(), "thr-2")
	require.NoError(t, err)
	require.Equal(t, "thr-2", *tools.parentThread.Load())
}

// TestBindResult_JSONTags pins the wire-shape the MCP wrapper relies on.
func TestBindResult_JSONTags(t *testing.T) {
	b, err := json.Marshal(BindResult{Bound: true, ThreadID: "t", AgentID: "a", DisplayName: "d"})
	require.NoError(t, err)
	require.JSONEq(t, `{"bound":true,"thread_id":"t","agent_id":"a","display_name":"d"}`, string(b))
}
