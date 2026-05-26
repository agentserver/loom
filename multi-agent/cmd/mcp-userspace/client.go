package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	cfg  Config
	http *http.Client
}

func newClient(cfg Config) *Client {
	return &Client{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) do(method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequest(method, strings.TrimRight(c.cfg.URL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return c.http.Do(req)
}

func (c *Client) Push(tarball, manifestJSON []byte) (map[string]any, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("manifest", string(manifestJSON)); err != nil {
		return nil, err
	}
	fw, err := mw.CreateFormFile("tarball", "pkg.tar.gz")
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(tarball); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	resp, err := c.do(http.MethodPost, "/api/userspace/packages", &body, mw.FormDataContentType())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeOrErr(resp)
}

func (c *Client) Search(q, kind string, limit int) (map[string]any, error) {
	u := fmt.Sprintf("/api/userspace/search?q=%s&kind=%s&limit=%d",
		url.QueryEscape(q), url.QueryEscape(kind), limit)
	resp, err := c.do(http.MethodGet, u, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeOrErr(resp)
}

func (c *Client) List(scope, kind string) (map[string]any, error) {
	u := fmt.Sprintf("/api/userspace/packages?workspace=%s&kind=%s",
		url.QueryEscape(scope), url.QueryEscape(kind))
	resp, err := c.do(http.MethodGet, u, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeOrErr(resp)
}

func (c *Client) PullTarball(slug, ver string) ([]byte, error) {
	resp, err := c.do(http.MethodGet,
		fmt.Sprintf("/api/userspace/packages/%s/versions/%s/source.tar.gz", slug, ver),
		nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("pull HTTP %d: %s", resp.StatusCode, body)
	}
	return io.ReadAll(resp.Body)
}

func (c *Client) Yank(slug, ver string) error {
	resp, err := c.do(http.MethodPost,
		fmt.Sprintf("/api/userspace/packages/%s/yank/%s", slug, ver), nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 204 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("yank HTTP %d: %s", resp.StatusCode, body)
	}
	return nil
}

func decodeOrErr(resp *http.Response) (map[string]any, error) {
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode: %w; body=%s", err, body)
	}
	return out, nil
}
