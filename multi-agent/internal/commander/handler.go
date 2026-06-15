package commander

import (
	"context"

	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// Handler is the transport-agnostic command dispatcher used by both the
// WebSocket client and the local HTTP server.
type Handler struct {
	Backend agentbackend.Backend
}

// ListSessions returns every session this backend has persisted.
func (h *Handler) ListSessions(ctx context.Context) ([]agentbackend.Session, error) {
	return h.Backend.ListSessions(ctx)
}

// GetSession returns descriptor and message history for one session ID.
func (h *Handler) GetSession(ctx context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
	return h.Backend.GetSession(ctx, id)
}

// SessionTurn runs one user turn against an existing session.
func (h *Handler) SessionTurn(ctx context.Context, id, prompt string, sink executor.Sink) (executor.Result, error) {
	return h.Backend.RunResume(ctx, id, prompt, sink)
}
