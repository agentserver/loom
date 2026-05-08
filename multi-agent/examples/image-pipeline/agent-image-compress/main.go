// agent-image-compress is a custom workspace agent that, on receiving a
// task, finds the first http(s) URL in the task prompt, downloads the
// image, re-encodes it as JPEG, and returns a Handle JSON pointing at the
// new bytes.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"image"
	_ "image/png" // for SynthPNG-produced inputs
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"syscall"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/examples/image-pipeline/internal/agentboot"
	"github.com/yourorg/multi-agent/examples/image-pipeline/internal/handlepick"
	"github.com/yourorg/multi-agent/examples/image-pipeline/internal/imageops"
	"github.com/yourorg/multi-agent/pkg/transport"
	httpx "github.com/yourorg/multi-agent/pkg/transport/http"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to YAML config")
	flag.Parse()

	cfg, err := agentboot.LoadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("agent-image-compress: %v", err)
	}
	srv, err := httpx.New(httpx.Options{Addr: cfg.ListenAddr})
	if err != nil {
		log.Fatalf("agent-image-compress: httpx: %v", err)
	}
	defer srv.Close()
	fmt.Fprintf(os.Stderr, "LISTEN %s\n", srv.Addr())

	handler := func(ctx context.Context, task *agentsdk.Task) error {
		out, err := runCompress(ctx, srv, task.Prompt)
		if err != nil {
			return task.Fail(ctx, err.Error())
		}
		return task.Complete(ctx, agentsdk.TaskResult{Output: out})
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := agentboot.Run(ctx, *cfgPath, handler); err != nil {
		log.Fatalf("agent-image-compress: %v", err)
	}
}

var qualityRe = regexp.MustCompile(`quality\s*=\s*(\d+)`)

// runCompress: pluck a URL out of prompt, fetch image, re-encode, push, return Handle.
func runCompress(ctx context.Context, srv *httpx.Server, prompt string) (string, error) {
	url, ok := handlepick.FirstURL(prompt)
	if !ok {
		return "", fmt.Errorf("no URL in prompt")
	}
	quality := 50
	if m := qualityRe.FindStringSubmatch(prompt); m != nil {
		if q, err := strconv.Atoi(m[1]); err == nil && q >= 1 && q <= 100 {
			quality = q
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("get %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("get %s: %s", url, resp.Status)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	originalBytes := len(raw)
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	jpegBytes, err := imageops.EncodeJPEG(img, quality)
	if err != nil {
		return "", err
	}
	h, err := srv.Put(ctx, "image/jpeg", bytes.NewReader(jpegBytes))
	if err != nil {
		return "", fmt.Errorf("put: %w", err)
	}
	out := transport.Handle{
		Type:  "image_url",
		URL:   h.URL,
		Bytes: h.Bytes,
		MIME:  "image/jpeg",
		Meta: map[string]string{
			"original_bytes": strconv.Itoa(originalBytes),
			"ratio":          fmt.Sprintf("%.2f", float64(len(jpegBytes))/float64(originalBytes)),
			"quality":        strconv.Itoa(quality),
		},
	}
	return out.Marshal(), nil
}
