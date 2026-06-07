package identity

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type resolverFunc func(context.Context, string) (Identity, error)

func (f resolverFunc) Resolve(ctx context.Context, token string) (Identity, error) {
	return f(ctx, token)
}

func TestChainFallsThroughInvalidResolver(t *testing.T) {
	want := Identity{WorkspaceID: "ws-1", AgentID: "agent-1", Source: "agentserver"}
	resolver := NewChain(
		resolverFunc(func(context.Context, string) (Identity, error) {
			return Identity{}, ErrInvalid
		}),
		resolverFunc(func(_ context.Context, token string) (Identity, error) {
			require.Equal(t, "tok", token)
			return want, nil
		}),
	)

	got, err := resolver.Resolve(context.Background(), "tok")
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestChainBubblesFatalResolverError(t *testing.T) {
	upstream := errors.New("boom")
	resolver := NewChain(
		resolverFunc(func(context.Context, string) (Identity, error) {
			return Identity{}, upstream
		}),
		resolverFunc(func(context.Context, string) (Identity, error) {
			t.Fatal("second resolver must not run after fatal error")
			return Identity{}, nil
		}),
	)

	_, err := resolver.Resolve(context.Background(), "tok")
	require.ErrorIs(t, err, upstream)
}

func TestChainReturnsInvalidWhenAllResolversReject(t *testing.T) {
	resolver := NewChain(
		resolverFunc(func(context.Context, string) (Identity, error) {
			return Identity{}, ErrInvalid
		}),
		resolverFunc(func(context.Context, string) (Identity, error) {
			return Identity{}, ErrInvalid
		}),
	)

	_, err := resolver.Resolve(context.Background(), "tok")
	require.ErrorIs(t, err, ErrInvalid)
}

func TestNewChainPanicsOnEmptyResolverList(t *testing.T) {
	require.Panics(t, func() {
		NewChain()
	})
}
