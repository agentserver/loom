package commanderhub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/yourorg/multi-agent/internal/commander"
)

const (
	// forwardHMACTimestampWindow is the allowed clock skew for HMAC timestamp validation.
	forwardHMACTimestampWindow = 60 * time.Second
	// forwardNonceHexLen is the expected length of a nonce in hex chars.
	forwardNonceHexLen = 32
	// maxForwardBodySize is the max size of the forwarded request body (1.5 MiB).
	maxForwardBodySize = 1536 * 1024
	// forwardStreamBuf is the channel buffer for the stream variant.
	forwardStreamBuf = 256
)

// forwardRequest is the JSON body POSTed to /api/commander/_internal/forward.
type forwardRequest struct {
	UserID      string          `json:"user_id"`
	WorkspaceID string          `json:"workspace_id"`
	DaemonID    string          `json:"daemon_id"`
	Command     string          `json:"command"`
	Args        json.RawMessage `json:"args,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
}

// forwardResponse is the JSON body returned for non-streaming forwards.
// Exactly one of Result or Error is non-nil.
type forwardResponse struct {
	Result json.RawMessage  `json:"result,omitempty"`
	Error  *forwardRespErr  `json:"error,omitempty"`
}

// forwardRespErr is the error shape inside forwardResponse.
type forwardRespErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// forwardClient is an HTTP client that forwards commands to a peer pod's
// /api/commander/_internal/forward endpoint using HMAC-authenticated requests.
type forwardClient struct {
	secret       []byte
	prevSecret   []byte
	advertiseURL string // self URL — used for loop detection
	httpClient   *http.Client
}

// newForwardClient constructs a forwardClient. advertiseURL is this pod's own
// public URL and is used to detect forwarding loops.
func newForwardClient(secret, prevSecret []byte, advertiseURL string) *forwardClient {
	return &forwardClient{
		secret:       secret,
		prevSecret:   prevSecret,
		advertiseURL: advertiseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// keysToTry returns the signing keys to attempt, starting with the current
// secret. If prevSecret is non-empty, it is appended so retry-on-403 can
// try the previous secret once.
func (fc *forwardClient) keysToTry() [][]byte {
	if len(fc.prevSecret) > 0 {
		return [][]byte{fc.secret, fc.prevSecret}
	}
	return [][]byte{fc.secret}
}

// wouldLoop reports true when peerURL is empty, equals self, or resolves to a
// loopback address (127.x.x.x, ::1, localhost). Uses net.IP.IsLoopback so all
// IPv4 127.x loopback addresses are detected, not just the self-URL match.
func (fc *forwardClient) wouldLoop(peerURL string) bool {
	if peerURL == "" || peerURL == fc.advertiseURL {
		return true
	}
	u, err := url.Parse(peerURL)
	if err != nil {
		return true // malformed URL → refuse
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}

// send forwards a non-streaming command to peerURL and returns the result payload.
// Returns ErrDaemonNotFound on 404 and loop refusal.
// Returns ErrDaemonGone on 403 (both secrets exhausted) and 5xx.
// Returns *DaemonError when the peer returns an application-level error.
func (fc *forwardClient) send(ctx context.Context, peerURL string, req forwardRequest) (json.RawMessage, error) {
	if fc.wouldLoop(peerURL) {
		return nil, ErrDaemonNotFound
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("forward_client: marshal request: %w", err)
	}
	if len(body) > maxForwardBodySize {
		return nil, fmt.Errorf("forward_client: request body too large (%d > %d)", len(body), maxForwardBodySize)
	}

	keys := fc.keysToTry()
	for i, key := range keys {
		result, err := fc.doSend(ctx, peerURL, body, key)
		if err == ErrDaemonGone && i == 0 && len(keys) > 1 {
			// 403 with current secret — retry with previous secret.
			// But we only know it was 403 if the error is a sentinel from
			// doSend with a specific marker. We handle this differently:
			// doSend returns (nil, errForward403) for 403 so we can retry.
			continue
		}
		if err == errForward403 && i == 0 && len(keys) > 1 {
			continue
		}
		if err == errForward403 {
			// Last key also returned 403.
			log.Printf("forward_client: peer=%s returned 403 with all %d secret(s); treating as gone", peerURL, len(keys))
			return nil, ErrDaemonGone
		}
		return result, err
	}
	// Unreachable — loop always returns or continues.
	return nil, ErrDaemonGone
}


// errForward403 is an internal sentinel meaning the peer returned HTTP 403.
// It is never returned to callers of send/stream — they see ErrDaemonGone instead.
var errForward403 = fmt.Errorf("forward_client: peer returned 403")

// doSend executes one HTTP POST attempt with the given signing key.
// Returns errForward403 on 403 so the caller can retry with the prev secret.
func (fc *forwardClient) doSend(ctx context.Context, peerURL string, body []byte, key []byte) (json.RawMessage, error) {
	endpoint := strings.TrimRight(peerURL, "/") + "/api/commander/_internal/forward"

	ts := time.Now().Unix()
	nonce, err := freshNonce()
	if err != nil {
		return nil, fmt.Errorf("forward_client: freshNonce: %w", err)
	}
	sig := signForward(key, ts, nonce, body)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("forward_client: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Forward-Ts", fmt.Sprintf("%d", ts))
	httpReq.Header.Set("X-Forward-Nonce", nonce)
	httpReq.Header.Set("X-Forward-Sig", sig)

	resp, err := fc.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("forward_client: do request: %w", err)
	}
	defer resp.Body.Close()

	return fc.mapResponse(peerURL, resp)
}

// mapResponse maps an HTTP response to a result or error. Shared between
// send and stream (stream calls mapResponse only for error paths).
func (fc *forwardClient) mapResponse(peerURL string, resp *http.Response) (json.RawMessage, error) {
	switch {
	case resp.StatusCode == http.StatusOK:
		var fr forwardResponse
		if err := json.NewDecoder(resp.Body).Decode(&fr); err != nil {
			return nil, fmt.Errorf("forward_client: decode response: %w", err)
		}
		if fr.Error != nil {
			return nil, &DaemonError{Code: fr.Error.Code, Message: fr.Error.Message}
		}
		return fr.Result, nil

	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrDaemonNotFound

	case resp.StatusCode == http.StatusUpgradeRequired:
		return nil, &DaemonError{Code: commander.ErrCodeDaemonUpgradeRequired}

	case resp.StatusCode == http.StatusForbidden:
		return nil, errForward403

	case resp.StatusCode >= 500:
		log.Printf("forward_client: peer=%s returned %d", peerURL, resp.StatusCode)
		return nil, ErrDaemonGone

	default:
		log.Printf("forward_client: peer=%s returned unexpected %d", peerURL, resp.StatusCode)
		return nil, ErrDaemonGone
	}
}

// stream forwards a streaming command to peerURL. It returns a channel of
// Envelope values. The channel is closed when the stream ends or the context
// is cancelled. Returns ErrDaemonNotFound on loop refusal or 404.
func (fc *forwardClient) stream(ctx context.Context, peerURL string, req forwardRequest) (<-chan commander.Envelope, error) {
	if fc.wouldLoop(peerURL) {
		return nil, ErrDaemonNotFound
	}

	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("forward_client: marshal request: %w", err)
	}
	if len(body) > maxForwardBodySize {
		return nil, fmt.Errorf("forward_client: request body too large (%d > %d)", len(body), maxForwardBodySize)
	}

	keys := fc.keysToTry()

	// Try each key, collecting the response so we can retry on 403.
	var resp *http.Response
	for i, key := range keys {
		var attempt *http.Response
		attempt, err = fc.doStreamRequest(ctx, peerURL, body, key)
		if err != nil {
			return nil, err
		}
		if attempt.StatusCode == http.StatusForbidden {
			attempt.Body.Close()
			if i == 0 && len(keys) > 1 {
				continue
			}
			log.Printf("forward_client: peer=%s returned 403 with all %d secret(s); treating as gone", peerURL, len(keys))
			return nil, ErrDaemonGone
		}
		resp = attempt
		break
	}
	if resp == nil {
		return nil, ErrDaemonGone
	}

	// Handle non-200 responses.
	if resp.StatusCode != http.StatusOK {
		// Read a small amount to allow mapResponse to decode error JSON.
		_, mapErr := fc.mapResponse(peerURL, resp)
		resp.Body.Close()
		if mapErr != nil {
			return nil, mapErr
		}
		return nil, ErrDaemonGone
	}

	out := make(chan commander.Envelope, forwardStreamBuf)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		dec := NewEnvelopeDecoder(resp.Body)
		for {
			env, err := dec.Decode()
			switch {
			case err == nil:
				select {
				case out <- *env:
				case <-ctx.Done():
					return
				}
			case errors.Is(err, io.EOF):
				// Normal stream end.
				return
			default:
				// Non-EOF decode error: emit a synthetic terminal error envelope
				// so the consumer learns about the failure instead of silently
				// receiving a closed channel.
				payload, _ := json.Marshal(map[string]string{
					"code":    commander.ErrCodeBackendUnavailable,
					"message": err.Error(),
				})
				errEnv := commander.Envelope{
					Type:    "error",
					Payload: payload,
				}
				select {
				case out <- errEnv:
				case <-ctx.Done():
				}
				return
			}
		}
	}()
	return out, nil
}

// doStreamRequest sends the HTTP POST for a streaming forward. Returns the
// raw *http.Response so the caller can inspect the status code before
// deciding whether to retry.
func (fc *forwardClient) doStreamRequest(ctx context.Context, peerURL string, body []byte, key []byte) (*http.Response, error) {
	endpoint := strings.TrimRight(peerURL, "/") + "/api/commander/_internal/forward"

	ts := time.Now().Unix()
	nonce, err := freshNonce()
	if err != nil {
		return nil, fmt.Errorf("forward_client: freshNonce: %w", err)
	}
	sig := signForward(key, ts, nonce, body)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("forward_client: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Forward-Ts", fmt.Sprintf("%d", ts))
	httpReq.Header.Set("X-Forward-Nonce", nonce)
	httpReq.Header.Set("X-Forward-Sig", sig)

	// Use a transport without a global timeout for streaming.
	streamClient := &http.Client{
		Transport: fc.httpClient.Transport,
	}
	return streamClient.Do(httpReq)
}

