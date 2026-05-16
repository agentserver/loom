package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/yourorg/multi-agent/internal/config"
)

const observerArtifactMaxBytes = 32 << 20

var (
	manifestRe        = regexp.MustCompile(`(?s)<USER_FILES_MANIFEST(?:\s[^>]*)?>\s*(.*?)\s*</USER_FILES_MANIFEST>`)
	observerGetFileRe = regexp.MustCompile(`(?is)^\s*(?:GET|Fetch)\b.*?\bat\s+(https?://\S+)`)
	observerWriteRe   = regexp.MustCompile(`(?is)\b(https?://\S*/api/writes/\S+)`)
)

type ObserverArtifactResolver struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewObserverArtifactResolver(cfg config.Observer) *ObserverArtifactResolver {
	if !cfg.Enabled || cfg.URL == "" || cfg.Token == "" {
		return nil
	}
	return &ObserverArtifactResolver{
		baseURL: strings.TrimRight(cfg.URL, "/"),
		token:   cfg.Token,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

func (r *ObserverArtifactResolver) GetArtifact(ctx context.Context, rawURL string) ([]byte, string, error) {
	if r == nil {
		return nil, "", fmt.Errorf("observer artifact resolver is not configured")
	}
	if err := r.validateObserverURL(rawURL, "/api/artifacts/"); err != nil {
		return nil, "", err
	}

	deadline := time.Now().Add(45 * time.Second)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, "", err
		}
		req.Header.Set("Authorization", "Bearer "+r.token)
		resp, err := r.http.Do(req)
		if err != nil {
			return nil, "", err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, observerArtifactMaxBytes+1))
		resp.Body.Close()
		if readErr != nil {
			return nil, "", readErr
		}
		if len(body) > observerArtifactMaxBytes {
			return nil, "", fmt.Errorf("observer artifact exceeds %d bytes", observerArtifactMaxBytes)
		}
		switch resp.StatusCode {
		case http.StatusOK:
			return body, resp.Header.Get("Content-Type"), nil
		case http.StatusAccepted:
			if time.Now().After(deadline) {
				return nil, "", fmt.Errorf("observer artifact %s still pending after timeout", rawURL)
			}
			select {
			case <-ctx.Done():
				return nil, "", ctx.Err()
			case <-time.After(retryAfter(resp.Header.Get("Retry-After"))):
			}
		default:
			if !isSQLiteBusy(body) || time.Now().After(deadline) {
				return nil, "", fmt.Errorf("observer artifact GET status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
			}
			select {
			case <-ctx.Done():
				return nil, "", ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
}

func (r *ObserverArtifactResolver) PutWrite(ctx context.Context, rawURL string, content []byte, mime string) error {
	if r == nil {
		return fmt.Errorf("observer artifact resolver is not configured")
	}
	if err := r.validateObserverURL(rawURL, "/api/writes/"); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, rawURL, bytes.NewReader(content))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	if mime != "" {
		req.Header.Set("Content-Type", mime)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("observer write PUT status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (r *ObserverArtifactResolver) AuthorizeArtifactURL(rawURL string) (string, bool) {
	if r == nil || r.token == "" {
		return rawURL, false
	}
	if err := r.validateObserverURL(rawURL, "/api/artifacts/"); err != nil {
		return rawURL, false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL, false
	}
	q := u.Query()
	q.Set("token", r.token)
	u.RawQuery = q.Encode()
	return u.String(), true
}

func (r *ObserverArtifactResolver) validateObserverURL(rawURL, pathPrefix string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	base, err := url.Parse(r.baseURL)
	if err != nil {
		return err
	}
	if u.Scheme != base.Scheme || u.Host != base.Host || !strings.HasPrefix(u.Path, pathPrefix) {
		return fmt.Errorf("url %q is outside observer %s%s", rawURL, r.baseURL, pathPrefix)
	}
	return nil
}

func (o *Orchestrator) authorizeMCPArtifactURLs(ctx context.Context, prompt string) (string, error) {
	auth, ok := o.artifacts.(ArtifactURLAuthorizer)
	if !ok || auth == nil {
		return prompt, nil
	}
	var call map[string]interface{}
	if err := json.Unmarshal([]byte(prompt), &call); err != nil {
		return prompt, nil
	}
	changed, err := o.authorizeArtifactValue(ctx, call, auth)
	if err != nil {
		return "", err
	}
	if !changed {
		return prompt, nil
	}
	out, err := json.Marshal(call)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (o *Orchestrator) authorizeArtifactValue(ctx context.Context, v interface{}, auth ArtifactURLAuthorizer) (bool, error) {
	changed := false
	switch x := v.(type) {
	case map[string]interface{}:
		for k, val := range x {
			if s, ok := val.(string); ok {
				if signed, did := auth.AuthorizeArtifactURL(s); did {
					if _, _, err := o.artifacts.GetArtifact(ctx, s); err != nil {
						return false, err
					}
					x[k] = signed
					changed = true
					continue
				}
			}
			did, err := o.authorizeArtifactValue(ctx, val, auth)
			if err != nil {
				return false, err
			}
			if did {
				changed = true
			}
		}
	case []interface{}:
		for i, val := range x {
			if s, ok := val.(string); ok {
				if signed, did := auth.AuthorizeArtifactURL(s); did {
					if _, _, err := o.artifacts.GetArtifact(ctx, s); err != nil {
						return false, err
					}
					x[i] = signed
					changed = true
					continue
				}
			}
			did, err := o.authorizeArtifactValue(ctx, val, auth)
			if err != nil {
				return false, err
			}
			if did {
				changed = true
			}
		}
	}
	return changed, nil
}

func retryAfter(v string) time.Duration {
	if v == "" {
		return 2 * time.Second
	}
	d, err := time.ParseDuration(v + "s")
	if err != nil || d <= 0 {
		return 2 * time.Second
	}
	return d
}

func isSQLiteBusy(body []byte) bool {
	s := string(body)
	return strings.Contains(s, "SQLITE_BUSY") || strings.Contains(strings.ToLower(s), "database is locked")
}

func observerArtifactGetURL(prompt string) (string, bool) {
	m := observerGetFileRe.FindStringSubmatch(prompt)
	if len(m) != 2 {
		return "", false
	}
	return strings.TrimRight(m[1], " \t\r\n.,);"), true
}

func observerWriteURL(prompt string) (string, bool) {
	if !strings.Contains(strings.ToUpper(prompt), "PUT") {
		return "", false
	}
	m := observerWriteRe.FindStringSubmatch(prompt)
	if len(m) != 2 {
		return "", false
	}
	return strings.TrimRight(m[1], " \t\r\n.,);"), true
}

func parseManifestWriteURLs(prompt string) ([]string, error) {
	m := manifestRe.FindStringSubmatch(prompt)
	if len(m) != 2 {
		return nil, nil
	}
	var manifest struct {
		Writes []struct {
			PutURL string `json:"put_url"`
		} `json:"writes"`
	}
	if err := json.Unmarshal([]byte(m[1]), &manifest); err != nil {
		return nil, fmt.Errorf("parse USER_FILES_MANIFEST: %w", err)
	}
	var urls []string
	for _, w := range manifest.Writes {
		if w.PutURL != "" {
			urls = append(urls, w.PutURL)
		}
	}
	return urls, nil
}
