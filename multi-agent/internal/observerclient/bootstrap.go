package observerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"
)

// writeTokenFile writes the plaintext token to path with mode 0600, replacing
// any existing content (O_WRONLY|O_CREATE|O_TRUNC). The parent directory must
// already exist; this is the caller's responsibility (validated at config load).
func writeTokenFile(path, token string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(token); err != nil {
		return err
	}
	return f.Sync()
}

// readTokenFile reads the token from path. ok=false means the file does not
// exist or contains only whitespace; in that case err is nil. Any other I/O
// error is surfaced via err.
func readTokenFile(path string) (token string, ok bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	trimmed := string(bytes.TrimSpace(data))
	if trimmed == "" {
		return "", false, nil
	}
	return trimmed, true, nil
}

type registerRequest struct {
	AgentID       string `json:"agent_id"`
	Role          string `json:"role"`
	DisplayName   string `json:"display_name"`
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name,omitempty"`
	Force         bool   `json:"force,omitempty"`
}

type registerResponse struct {
	WorkspaceID string `json:"workspace_id"`
	AgentID     string `json:"agent_id"`
	Role        string `json:"role"`
	DisplayName string `json:"display_name"`
	Token       string `json:"token"`
}

// register POSTs to <baseURL>/api/agents/register with Authorization: Bearer <apiKey>.
// Returns the issued per-agent token and the workspace_id reported by the server.
// The caller is responsible for cross-checking workspace_id against the operator-declared value.
func register(
	ctx context.Context,
	httpc *http.Client,
	baseURL, apiKey, agentID, role, displayName, workspaceID, workspaceName string,
	force bool,
) (token, workspaceIDReturned string, err error) {
	body, _ := json.Marshal(registerRequest{
		AgentID:       agentID,
		Role:          role,
		DisplayName:   displayName,
		WorkspaceID:   workspaceID,
		WorkspaceName: workspaceName,
		Force:         force,
	})
	url := strings.TrimRight(baseURL, "/") + "/api/agents/register"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("observerclient register: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", "", fmt.Errorf("observerclient register: HTTP %d: %s",
			resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	var rr registerResponse
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		return "", "", fmt.Errorf("observerclient register: decode response: %w", err)
	}
	if rr.Token == "" {
		return "", "", errors.New("observerclient register: server returned empty token")
	}
	return rr.Token, rr.WorkspaceID, nil
}

// loadOrRegister returns the token to seed Client.token. It prefers an
// existing on-disk token (cfg.TokenStatePath) and falls back to a synchronous
// register call. On a register success the response is cross-checked against
// cfg.WorkspaceID and persisted to disk before returning.
func (c *Client) loadOrRegister(ctx context.Context) (string, error) {
	if c.cfg.AgentserverProxyToken != "" {
		return c.cfg.AgentserverProxyToken, nil
	}
	if tok, ok, err := readTokenFile(c.cfg.TokenStatePath); err != nil {
		return "", fmt.Errorf("observerclient: read token state: %w", err)
	} else if ok {
		return tok, nil
	}

	regCtx, cancel := context.WithTimeout(ctx, registerTimeout)
	defer cancel()

	httpc := &http.Client{Timeout: registerTimeout}
	tok, ws, err := register(regCtx, httpc, c.cfg.URL, c.cfg.APIKey,
		c.cfg.AgentID, c.cfg.AgentRole, c.cfg.AgentID, c.cfg.WorkspaceID, c.cfg.WorkspaceName,
		c.cfg.ForceRegister)
	if err != nil {
		return "", err
	}
	if ws != c.cfg.WorkspaceID {
		return "", fmt.Errorf(
			"observerclient: api_key belongs to workspace %q but yaml declares observer.workspace_id=%q",
			ws, c.cfg.WorkspaceID)
	}
	if err := writeTokenFile(c.cfg.TokenStatePath, tok); err != nil {
		return "", fmt.Errorf("observerclient: persist token: %w", err)
	}
	return tok, nil
}

// handle401 reacts to a 401 from /api/events by re-registering, swapping
// the in-memory token, and overwriting the token file. A 60-second cooldown
// per process prevents an unbounded re-register storm if the api-key itself
// has been revoked server-side.
func (c *Client) handle401(ctx context.Context) {
	if c.proxyTokenMode {
		fmt.Fprintln(os.Stderr, "observerclient: ingest 401 with agentserver proxy token; not re-registering")
		return
	}
	c.tokenMu.Lock()
	now := time.Now()
	if now.Sub(c.lastReRegister) < reRegisterCoolDur {
		c.tokenMu.Unlock()
		fmt.Fprintln(os.Stderr, "observerclient: ingest 401 within cooldown; not re-registering")
		return
	}
	c.lastReRegister = now
	c.tokenMu.Unlock()

	// Pause run()'s dequeue while we re-register; otherwise queued events
	// get popped, hit 401 again, fall under the per-process cooldown
	// check above, and silently drop. Fixes §1.3 #12.
	c.setCooldown(reRegisterCoolDur)

	regCtx, cancel := context.WithTimeout(ctx, registerTimeout)
	defer cancel()

	httpc := &http.Client{Timeout: registerTimeout}
	tok, _, err := register(regCtx, httpc, c.cfg.URL, c.cfg.APIKey,
		c.cfg.AgentID, c.cfg.AgentRole, c.cfg.AgentID, c.cfg.WorkspaceID, c.cfg.WorkspaceName,
		c.cfg.ForceRegister)
	if err != nil {
		fmt.Fprintf(os.Stderr, "observerclient: ingest 401 → re-register failed: %v\n", err)
		return
	}

	c.tokenMu.Lock()
	c.token = tok
	c.tokenMu.Unlock()

	if writeErr := writeTokenFile(c.cfg.TokenStatePath, tok); writeErr != nil {
		fmt.Fprintf(os.Stderr,
			"observerclient: token rotated in-memory but file write failed: %v\n", writeErr)
	}
	fmt.Fprintln(os.Stderr, "observerclient: ingest 401 → re-registered successfully")
	// Re-register succeeded — resume dequeue immediately rather than
	// waiting out the full cooldown window.
	c.clearCooldown()
}
