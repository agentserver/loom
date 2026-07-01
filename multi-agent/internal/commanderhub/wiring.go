package commanderhub

import (
	"context"
	"net/http"
	"time"

	"github.com/yourorg/multi-agent/internal/commanderhub/authstore"
	"github.com/yourorg/multi-agent/internal/identity"
)

// sweepInterval is the per-pod commander sweep cadence. Exposed as a var so
// tests can shorten it; production sets the default once via MountAll.
var sweepInterval = time.Hour

// MountAll wires the full commander surface onto publicMux and, when cluster mode
// is active (cluster.AdvertiseURL != ""), also mounts internal endpoints on
// internalMux. internalMux may be nil for single-pod deployments.
//
// Cluster-mode wiring (cluster.AdvertiseURL != ""):
//   - Builds a *sharedRegistry backed by cluster.DB with timing values from cluster.
//   - Builds a *forwardClient using cluster.Secret, cluster.PrevSecret,
//     cluster.AdvertiseURL, and cluster.ForwardTimeout.
//   - Calls hub.attachSharedRegistry(cluster, sr, fc, turns).
//   - Mounts /api/commander/_internal/forward + /api/commander/_internal/drain on
//     internalMux (when non-nil).
//   - Starts the shared-registry sweeper goroutine.
//   - Returns the Hub so callers can wire Close into the shutdown sequence.
//
// store is required — observerweb panics if it is nil when AgentserverURL != "".
func MountAll(publicMux *http.ServeMux, internalMux *http.ServeMux, resolver identity.Resolver, agentserverURL string, store authstore.Store, cluster ClusterRuntime) *Hub {
	hub := NewHub(resolver)
	auth := NewAuthenticator(resolver, agentserverURL, store)
	publicMux.Handle("/api/daemon-link", hub) // hub.ServeHTTP upgrades the daemon WS
	Mount(publicMux, hub, auth)               // /api/commander/* + login/poll/logout
	MountWeb(publicMux)                       // /commander page + assets
	go auth.runSweep(sweepInterval)

	if cluster.AdvertiseURL != "" {
		// Build shared registry with configured timing (falls back to defaults for zero values).
		srCfg := SharedRegistryConfig{
			HeartbeatEvery: cluster.HeartbeatInterval,
			SweepEvery:     cluster.SweepInterval,
			// deleteAfter is the daemon_expiry_after config value.
			DeleteAfter: cluster.DaemonExpiryAfter,
		}
		if cluster.DaemonExpiryAfter > 0 {
			// onlineTTL = half of DaemonExpiryAfter, min 30s.
			half := cluster.DaemonExpiryAfter / 2
			if half < 30*time.Second {
				half = 30 * time.Second
			}
			srCfg.OnlineTTL = half
		}
		sr := newSharedRegistryWithConfig(cluster.DB, cluster.AdvertiseURL, srCfg)
		fc := newForwardClient(cluster.Secret, cluster.PrevSecret, cluster.AdvertiseURL, cluster.ForwardTimeout)
		var turns turnStateBackend
		if cluster.DB != nil {
			turns = newPGTurnStore(cluster.DB)
		}
		hub.attachSharedRegistry(cluster, sr, fc, turns)

		// Wire turn-orphan cleanup into the sweeper. TurnTimeout comes from
		// hub.TurnTimeout (set by NewHub to defaultTurnTimeout, overrideable
		// by the caller). A nil turns backend (single-pod memTurnStore path)
		// is safe: attachTurns is a no-op when turns == nil or timeout == 0.
		if turns != nil {
			sr.attachTurns(turns, hub.TurnTimeout)
		}

		if internalMux != nil {
			internalMux.HandleFunc("/api/commander/_internal/forward", hub.forwardHandler)
			internalMux.HandleFunc("/api/commander/_internal/drain", hub.drainHandler)
		}

		// Start shared-registry sweeper goroutine. Runs until process exit.
		go sr.runSweep(context.Background())
	}
	return hub
}
