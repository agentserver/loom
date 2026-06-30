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
//   - Builds a *sharedRegistry backed by cluster.DB.
//   - Builds a *forwardClient using cluster.Secret, cluster.PrevSecret, cluster.AdvertiseURL.
//   - Passes nil for turns (pgTurnStore is Phase D D2; memTurnStore remains active).
//   - Calls hub.attachSharedRegistry(cluster, sr, fc, nil).
//   - Mounts /api/commander/_internal/forward + /api/commander/_internal/drain on
//     internalMux (when non-nil).
//   - Starts the shared-registry sweeper goroutine.
//
// store is required — observerweb panics if it is nil when AgentserverURL != "".
func MountAll(publicMux *http.ServeMux, internalMux *http.ServeMux, resolver identity.Resolver, agentserverURL string, store authstore.Store, cluster ClusterRuntime) {
	hub := NewHub(resolver)
	auth := NewAuthenticator(resolver, agentserverURL, store)
	publicMux.Handle("/api/daemon-link", hub) // hub.ServeHTTP upgrades the daemon WS
	Mount(publicMux, hub, auth)               // /api/commander/* + login/poll/logout
	MountWeb(publicMux)                       // /commander page + assets
	go auth.runSweep(sweepInterval)

	if cluster.AdvertiseURL != "" {
		sr := newSharedRegistry(cluster.DB, cluster.AdvertiseURL)
		fc := newForwardClient(cluster.Secret, cluster.PrevSecret, cluster.AdvertiseURL)
		// TODO(D2): pass pgTurnStore once implemented; for now pass nil so Hub
		// keeps its memTurnStore.
		var turns turnStateBackend
		hub.attachSharedRegistry(cluster, sr, fc, turns)

		if internalMux != nil {
			internalMux.HandleFunc("/api/commander/_internal/forward", hub.forwardHandler)
			internalMux.HandleFunc("/api/commander/_internal/drain", hub.drainHandler)
		}

		// Start shared-registry sweeper goroutine. Runs until process exit.
		go sr.runSweep(context.Background())
	}
}
