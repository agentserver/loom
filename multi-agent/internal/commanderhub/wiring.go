package commanderhub

import (
	"net/http"

	"github.com/yourorg/multi-agent/internal/identity"
)

// MountAll wires the full commander surface onto mux: the daemon WebSocket
// endpoint, the /api/commander/* reverse proxy + auth, and the /commander page.
// One call from observerweb.NewWithResolverOptions.
func MountAll(mux *http.ServeMux, resolver identity.Resolver, agentserverURL string) {
	hub := NewHub(resolver)
	auth := NewAuthenticator(resolver, agentserverURL)
	mux.Handle("/api/daemon-link", hub) // hub.ServeHTTP upgrades the daemon WS
	Mount(mux, hub, auth)               // /api/commander/* + login/poll/logout
	MountWeb(mux)                       // /commander page + assets
}
