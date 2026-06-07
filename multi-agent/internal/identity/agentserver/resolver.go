package agentserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/yourorg/multi-agent/internal/identity"
)

const defaultTimeout = 2 * time.Second

type Config struct {
	BaseURL string
	Timeout time.Duration
	Client  *http.Client
}

type Resolver struct {
	baseURL string
	timeout time.Duration
	client  *http.Client
}

func New(cfg Config) *Resolver {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	client := cfg.Client
	if client == nil {
		client = http.DefaultClient
	}
	return &Resolver{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		timeout: timeout,
		client:  client,
	}
}

func (r *Resolver) Resolve(ctx context.Context, token string) (identity.Identity, error) {
	if token == "" {
		return identity.Identity{}, identity.ErrInvalid
	}
	if r.baseURL == "" {
		return identity.Identity{}, fmt.Errorf("%w: missing base url", identity.ErrUpstream)
	}
	reqCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, r.baseURL+"/api/agent/whoami", nil)
	if err != nil {
		return identity.Identity{}, fmt.Errorf("%w: build request: %v", identity.ErrUpstream, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := r.client.Do(req)
	if err != nil {
		return identity.Identity{}, fmt.Errorf("%w: %v", identity.ErrUpstream, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return identity.Identity{}, identity.ErrInvalid
	case http.StatusForbidden:
		return identity.Identity{}, identity.ErrRevoked
	default:
		return identity.Identity{}, fmt.Errorf("%w: status %d", identity.ErrUpstream, resp.StatusCode)
	}

	var body whoamiResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return identity.Identity{}, fmt.Errorf("%w: decode whoami: %v", identity.ErrUpstream, err)
	}
	if err := body.validate(); err != nil {
		return identity.Identity{}, err
	}
	agentID := body.ShortID
	if agentID == "" {
		agentID = body.SandboxID
	}
	return identity.Identity{
		UserID:        body.UserID,
		WorkspaceID:   body.WorkspaceID,
		WorkspaceName: body.WorkspaceName,
		AgentID:       agentID,
		SandboxID:     body.SandboxID,
		Role:          body.Role,
		Source:        identity.SourceAgentserver,
	}, nil
}

type whoamiResponse struct {
	UserID        string `json:"user_id"`
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
	SandboxID     string `json:"sandbox_id"`
	ShortID       string `json:"short_id"`
	Role          string `json:"role"`
}

func (r whoamiResponse) validate() error {
	if r.UserID == "" {
		return fmt.Errorf("%w: whoami missing user_id", identity.ErrUpstream)
	}
	if r.WorkspaceID == "" {
		return fmt.Errorf("%w: whoami missing workspace_id", identity.ErrUpstream)
	}
	if r.SandboxID == "" {
		return fmt.Errorf("%w: whoami missing sandbox_id", identity.ErrUpstream)
	}
	if r.Role == "" {
		return fmt.Errorf("%w: whoami missing role", identity.ErrUpstream)
	}
	return nil
}
