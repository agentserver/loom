package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRunDrainLocal_PostsToInternalEndpoint verifies that runDrainLocal POSTs
// to http://127.0.0.1:<port>/api/commander/_internal/drain and returns nil
// on HTTP 200. The drain handler is mounted by commanderhub.MountAll on the
// internal listener; this test uses a minimal httptest.Server to isolate the
// client behaviour.
func TestRunDrainLocal_PostsToInternalEndpoint(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)

	cfg := &Config{
		Cluster: ClusterConfig{
			InternalListenAddr: ":" + portStr,
		},
	}

	err = runDrainLocal(cfg, 0)
	require.NoError(t, err, "runDrainLocal must return nil on HTTP 200")
	require.Equal(t, http.MethodPost, gotMethod, "drain must use POST")
	require.Equal(t, "/api/commander/_internal/drain", gotPath, "drain must POST to the correct path")
}

// TestRunDrainLocal_PortOverride verifies that --internal-port overrides the
// cluster.internal_listen_addr from config.
func TestRunDrainLocal_PortOverride(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)
	portNum, err := strconv.Atoi(portStr)
	require.NoError(t, err)

	// Config has an unused internal addr; port override must win.
	cfg := &Config{
		Cluster: ClusterConfig{
			InternalListenAddr: ":19999",
		},
	}

	err = runDrainLocal(cfg, portNum)
	require.NoError(t, err, "runDrainLocal with port override must return nil on HTTP 200")
	require.Equal(t, "/api/commander/_internal/drain", gotPath)
}

// TestRunDrainLocal_ConnectionRefused_ExitsCleanly verifies that
// connection-refused (server already stopped) is treated as success so the
// preStop hook does not cause Kubernetes to send an immediate SIGKILL.
func TestRunDrainLocal_ConnectionRefused_ExitsCleanly(t *testing.T) {
	// Find a port that is definitely not listening by binding then closing.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	l.Close() // Close so no one is listening on this port.

	cfg := &Config{
		Cluster: ClusterConfig{
			InternalListenAddr: fmt.Sprintf(":%d", port),
		},
	}
	err = runDrainLocal(cfg, 0)
	require.NoError(t, err, "connection-refused must be treated as success")
}

// TestRunDrainLocal_NoInternalAddr_Skips verifies that when
// cluster.internal_listen_addr is not set (single-pod mode), runDrainLocal
// returns nil without making any HTTP request.
func TestRunDrainLocal_NoInternalAddr_Skips(t *testing.T) {
	cfg := &Config{
		Cluster: ClusterConfig{
			InternalListenAddr: "",
		},
	}
	err := runDrainLocal(cfg, 0)
	require.NoError(t, err, "missing internal_listen_addr must be a no-op")
}

// TestRunDrainLocal_BadConfig_Exits1 verifies that a malformed
// internal_listen_addr causes runDrainLocal to return a non-nil error (which
// main() turns into exit 1 via log.Fatalf).
func TestRunDrainLocal_BadConfig_Exits1(t *testing.T) {
	cfg := &Config{
		Cluster: ClusterConfig{
			InternalListenAddr: "not-a-valid-addr",
		},
	}
	err := runDrainLocal(cfg, 0)
	require.Error(t, err, "malformed internal_listen_addr must return an error")
}
