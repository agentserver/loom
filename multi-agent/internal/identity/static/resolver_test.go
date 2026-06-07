package static

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/identity"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/observerstore"
)

type fakeValidator struct {
	agent observerstore.Agent
	ok    bool
	err   error
}

func (f fakeValidator) ValidateToken(string) (observerstore.Agent, bool, error) {
	return f.agent, f.ok, f.err
}

func TestResolverMapsValidObserverTokenToLocalIdentity(t *testing.T) {
	resolver := New(fakeValidator{
		agent: observerstore.Agent{
			WorkspaceID: "ws-1",
			ID:          "agent-1",
			Role:        observer.RoleDriver,
			DisplayName: "Driver",
		},
		ok: true,
	})

	got, err := resolver.Resolve(context.Background(), "tok")
	require.NoError(t, err)
	require.Equal(t, identity.Identity{
		WorkspaceID: "ws-1",
		AgentID:     "agent-1",
		Role:        observer.RoleDriver,
		Source:      identity.SourceLocal,
	}, got)
}

func TestResolverReturnsInvalidForUnknownObserverToken(t *testing.T) {
	resolver := New(fakeValidator{ok: false})

	_, err := resolver.Resolve(context.Background(), "tok")
	require.ErrorIs(t, err, identity.ErrInvalid)
}

func TestResolverBubblesStoreErrors(t *testing.T) {
	storeErr := errors.New("db failed")
	resolver := New(fakeValidator{err: storeErr})

	_, err := resolver.Resolve(context.Background(), "tok")
	require.ErrorIs(t, err, storeErr)
}
