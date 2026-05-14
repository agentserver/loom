package driver

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/agentserver/agentserver/pkg/agentsdk"
)

// AgentSDKClient adapts the published agentsdk.Client to the driver's local
// SDKClient interface. The v0.40.0 SDK does not expose PeerProxy as a helper,
// so the driver issues the same proxy-token-authenticated HTTP request itself.
type AgentSDKClient struct {
	*agentsdk.Client
	serverURL  string
	proxyToken string
}

func NewAgentSDKClient(c *agentsdk.Client, serverURL, proxyToken string) *AgentSDKClient {
	return &AgentSDKClient{Client: c, serverURL: serverURL, proxyToken: proxyToken}
}

func (c *AgentSDKClient) PeerProxy(ctx context.Context, method, targetShortID, path string, body io.Reader) (*http.Response, error) {
	if targetShortID == "" {
		return nil, fmt.Errorf("target short_id is required")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	url := strings.TrimRight(c.serverURL, "/") + "/api/agent/peer/" + targetShortID + "/proxy" + path
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("create peer proxy request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.proxyToken)
	return http.DefaultClient.Do(req)
}
