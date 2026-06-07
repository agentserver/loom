package static

import (
	"context"

	"github.com/yourorg/multi-agent/internal/identity"
	"github.com/yourorg/multi-agent/internal/observerstore"
)

type Validator interface {
	ValidateToken(token string) (observerstore.Agent, bool, error)
}

type Resolver struct {
	validator Validator
}

func New(validator Validator) *Resolver {
	if validator == nil {
		panic("identity/static: nil validator")
	}
	return &Resolver{validator: validator}
}

func (r *Resolver) Resolve(_ context.Context, token string) (identity.Identity, error) {
	agent, ok, err := r.validator.ValidateToken(token)
	if err != nil {
		return identity.Identity{}, err
	}
	if !ok {
		return identity.Identity{}, identity.ErrInvalid
	}
	return identity.Identity{
		WorkspaceID: agent.WorkspaceID,
		AgentID:     agent.ID,
		Role:        agent.Role,
		Source:      identity.SourceLocal,
	}, nil
}
