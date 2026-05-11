package observerclient

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/observer"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestClientEmitPostsBearerEvent(t *testing.T) {
	received := make(chan observer.Event, 1)
	errc := make(chan error, 1)
	var gotPath string
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var ev observer.Event
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			errc <- err
			return
		}
		received <- ev
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := New(Config{
		Enabled:     true,
		URL:         srv.URL + "/",
		WorkspaceID: "ws-1",
		AgentID:     "agent-1",
		AgentRole:   observer.RoleMaster,
		Token:       "tok",
	})
	require.True(t, c.Enabled())

	c.Emit(observer.Event{TaskID: "task-1"})
	c.Close()

	select {
	case ev := <-received:
		require.Equal(t, "/api/events", gotPath)
		require.Equal(t, "Bearer tok", gotAuth)
		require.Equal(t, "ws-1", ev.WorkspaceID)
		require.Equal(t, "agent-1", ev.AgentID)
		require.Equal(t, observer.RoleMaster, ev.AgentRole)
		require.Equal(t, "task-1", ev.TaskID)
		require.NotEmpty(t, ev.TS)
	case err := <-errc:
		require.NoError(t, err)
	default:
		t.Fatal("server did not receive event")
	}
}

func TestClientDisabledDropsEvents(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer srv.Close()

	c := New(Config{
		Enabled: true,
		URL:     srv.URL,
		Token:   "",
	})
	require.False(t, c.Enabled())

	c.Emit(observer.Event{TaskID: "task-1"})
	c.Close()

	require.Equal(t, int32(0), calls.Load())
}

func TestClientCloseReturnsWhenPostStalls(t *testing.T) {
	block := make(chan struct{})
	c := New(Config{
		Enabled: true,
		URL:     "http://observer.example",
		Token:   "tok",
	})
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-block
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Status:     "202 Accepted",
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})

	c.Emit(observer.Event{TaskID: "task-1"})
	done := make(chan struct{})
	go func() {
		c.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(4 * time.Second):
		close(block)
		t.Fatal("Close did not return after bounded flush timeout")
	}
	close(block)
}
