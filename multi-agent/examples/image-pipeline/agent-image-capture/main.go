// agent-image-capture is a custom workspace agent that, on receiving any
// task, synthesizes a 256x256 PNG and returns a Handle JSON pointing at the
// in-process HTTP transport.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/examples/image-pipeline/internal/agentboot"
	"github.com/yourorg/multi-agent/examples/image-pipeline/internal/imageops"
	"github.com/yourorg/multi-agent/pkg/transport"
	httpx "github.com/yourorg/multi-agent/pkg/transport/http"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to YAML config")
	flag.Parse()

	cfg, err := agentboot.LoadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("agent-image-capture: %v", err)
	}
	srv, err := httpx.New(httpx.Options{Addr: cfg.ListenAddr})
	if err != nil {
		log.Fatalf("agent-image-capture: httpx: %v", err)
	}
	defer srv.Close()
	fmt.Fprintf(os.Stderr, "LISTEN %s\n", srv.Addr())

	handler := func(ctx context.Context, task *agentsdk.Task) error {
		out, err := runCapture(ctx, srv)
		if err != nil {
			return task.Fail(ctx, err.Error())
		}
		return task.Complete(ctx, agentsdk.TaskResult{Output: out})
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := agentboot.Run(ctx, *cfgPath, handler); err != nil {
		log.Fatalf("agent-image-capture: %v", err)
	}
}

// runCapture is the unit-testable core: synthesize a PNG, push it through the
// transport, return the Handle JSON string suitable for TaskResult.Output.
func runCapture(ctx context.Context, srv *httpx.Server) (string, error) {
	pngBytes, err := imageops.SynthPNG(256, 256, 42)
	if err != nil {
		return "", fmt.Errorf("synth: %w", err)
	}
	h, err := srv.Put(ctx, "image/png", bytes.NewReader(pngBytes))
	if err != nil {
		return "", fmt.Errorf("put: %w", err)
	}
	return transport.Handle{
		Type:  "image_url",
		URL:   h.URL,
		Bytes: h.Bytes,
		MIME:  h.MIME,
	}.Marshal(), nil
}
