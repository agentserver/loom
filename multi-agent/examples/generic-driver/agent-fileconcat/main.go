package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/examples/internal/agentboot"
)

type manifest struct {
	Files []struct {
		Path string `json:"path"`
		Kind string `json:"kind"`
		URL  string `json:"url"`
	} `json:"files"`
	Writes []struct {
		Path   string `json:"path"`
		PutURL string `json:"put_url"`
	} `json:"writes"`
}

func main() {
	cfgPath := flag.String("config", "", "path to config.yaml")
	flag.Parse()
	if *cfgPath == "" {
		fmt.Fprintln(os.Stderr, "--config required")
		os.Exit(2)
	}
	if err := agentboot.Run(context.Background(), *cfgPath, runTask); err != nil {
		fmt.Fprintln(os.Stderr, "fileconcat:", err)
		os.Exit(1)
	}
}

// proxyToken comes from the FILECONCAT_PROXY_TOKEN env (set by scripts/e2e.sh
// from the saved config). agentboot does not currently surface the live token
// through Handlers; the env-var workaround is a known v1 limitation.
var proxyToken = os.Getenv("FILECONCAT_PROXY_TOKEN")

func runTask(ctx context.Context, task *agentsdk.Task) error {
	m, _, err := parseManifest(task.Prompt)
	if err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}
	if len(m.Files) == 0 {
		return fmt.Errorf("no files in manifest")
	}
	if len(m.Writes) == 0 {
		return fmt.Errorf("no write target in manifest")
	}
	pieces := make([]string, 0, len(m.Files))
	for _, f := range m.Files {
		if f.Kind != "file" {
			continue
		}
		b, err := authedGet(ctx, f.URL)
		if err != nil {
			return fmt.Errorf("get %s: %w", f.Path, err)
		}
		pieces = append(pieces, string(b))
	}
	out := []byte(strings.Join(pieces, " "))
	if err := authedPut(ctx, m.Writes[0].PutURL, out); err != nil {
		return fmt.Errorf("put %s: %w", m.Writes[0].Path, err)
	}
	fmt.Fprintf(os.Stderr, "fileconcat: wrote %d bytes to %s\n", len(out), m.Writes[0].Path)
	return nil
}

func parseManifest(prompt string) (manifest, string, error) {
	const open = "<USER_FILES_MANIFEST"
	const close = "</USER_FILES_MANIFEST>"
	openIdx := strings.Index(prompt, open)
	if openIdx < 0 {
		return manifest{}, prompt, fmt.Errorf("no manifest")
	}
	endLine := strings.Index(prompt[openIdx:], "\n")
	if endLine < 0 {
		return manifest{}, prompt, fmt.Errorf("malformed open fence")
	}
	jsonStart := openIdx + endLine + 1
	closeIdx := strings.Index(prompt[jsonStart:], "\n"+close)
	if closeIdx < 0 {
		return manifest{}, prompt, fmt.Errorf("no close fence")
	}
	jsonLine := prompt[jsonStart : jsonStart+closeIdx]
	var m manifest
	if err := json.Unmarshal([]byte(jsonLine), &m); err != nil {
		return manifest{}, prompt, fmt.Errorf("unmarshal manifest: %w", err)
	}
	bodyStart := jsonStart + closeIdx + len("\n"+close)
	body := strings.TrimLeft(prompt[bodyStart:], "\n")
	return m, body, nil
}

func authedGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+proxyToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}
	return io.ReadAll(resp.Body)
}

func authedPut(ctx context.Context, url string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+proxyToken)
	req.ContentLength = int64(len(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}
	return nil
}
