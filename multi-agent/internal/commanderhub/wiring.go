package commanderhub

import (
	"net/http"
	"time"

	"github.com/yourorg/multi-agent/internal/commanderhub/authstore"
	"github.com/yourorg/multi-agent/internal/identity"
)

// sweepInterval is the per-pod commander sweep cadence. Exposed as a var so
// tests can shorten it; production sets the default once via MountAll.
var sweepInterval = time.Hour

// MountAll wires the full commander surface onto mux: the daemon WebSocket
// endpoint, the /api/commander/* reverse proxy + auth, and the /commander page.
// One call from observerweb.NewWithResolverOptions. store is required —
// observerweb panics if it is nil when AgentserverURL != "".
func MountAll(mux *http.ServeMux, resolver identity.Resolver, agentserverURL string, store authstore.Store) {
	hub := NewHub(resolver)
	auth := NewAuthenticator(resolver, agentserverURL, store)
	mux.Handle("/api/daemon-link", hub) // hub.ServeHTTP upgrades the daemon WS
	Mount(mux, hub, auth)               // /api/commander/* + login/poll/logout
	MountWeb(mux)                       // /commander page + assets
	go auth.runSweep(sweepInterval)
}
