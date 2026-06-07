package identity

import (
	"context"
	"errors"
)

const (
	SourceAgentserver = "agentserver"
	SourceLocal       = "local"
)

type Identity struct {
	UserID        string
	WorkspaceID   string
	WorkspaceName string
	AgentID       string
	SandboxID     string
	Role          string
	Source        string
}

type Resolver interface {
	Resolve(ctx context.Context, token string) (Identity, error)
}

var (
	ErrInvalid  = errors.New("identity: invalid token")
	ErrRevoked  = errors.New("identity: token revoked")
	ErrUpstream = errors.New("identity: upstream unavailable")
)
