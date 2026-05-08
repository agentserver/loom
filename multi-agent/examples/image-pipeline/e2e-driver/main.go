// e2e-driver is the assertion runner for the image-pipeline e2e. It loads a
// pre-registered agent config, polls until the master and both image agents
// are visible in agentserver discovery, submits a fanout task to the master,
// waits for completion, and asserts:
//
//   - master task status == completed
//   - reducer output contains an image_url + an http URL we can GET
//   - GETting that URL yields valid JPEG bytes
//   - if --master-data-db is given, sub_tasks rows are both completed and
//     n2.bytes < n1.bytes (real compression happened)
//
// Exit 0 = pass, 1 = fail.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	_ "image/jpeg"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/examples/image-pipeline/internal/agentboot"
	"github.com/yourorg/multi-agent/examples/image-pipeline/internal/handlepick"
	"github.com/yourorg/multi-agent/pkg/transport"
)

func main() {
	cfg := flag.String("config", "", "driver agent config (must have credentials)")
	target := flag.String("target-display-name", "master-e2e-image", "master display_name to discover")
	expect := flag.String("expect-agents", "image-capture,image-compress", "comma-separated display_names that must also be visible before submitting")
	prompt := flag.String("prompt", "Capture a 256x256 image using the image-capture agent, then pass its output URL to the image-compress agent at quality=50. Sub-task n2 MUST reference n1's output via {{n1.output}} so my orchestrator can substitute it. Final answer should include the final compressed image URL and the byte size.", "task prompt")
	skill := flag.String("skill", "fanout", "task skill")
	timeout := flag.Duration("timeout", 300*time.Second, "overall driver timeout")
	dbPath := flag.String("master-data-db", "", "optional: path to master's data.db for deeper assertions")
	flag.Parse()

	if *cfg == "" {
		log.Fatal("--config required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	c, err := agentboot.LoadConfig(*cfg)
	if err != nil {
		log.Fatalf("driver: load config: %v", err)
	}
	cli := agentsdk.NewClient(agentsdk.Config{ServerURL: c.Server.URL, Name: c.Server.Name})
	if c.Credentials.ProxyToken == "" {
		log.Fatal("driver: config has no credentials; register first")
	}
	cli.SetRegistration(&agentsdk.Registration{
		SandboxID:   c.Credentials.SandboxID,
		TunnelToken: c.Credentials.TunnelToken,
		ProxyToken:  c.Credentials.ProxyToken,
		WorkspaceID: c.Credentials.WorkspaceID,
		ShortID:     c.Credentials.ShortID,
	})

	// Phase 1: wait for all expected agents to be visible.
	want := append(strings.Split(*expect, ","), *target)
	masterID, err := waitForAgents(ctx, cli, want, *target, 60*time.Second)
	if err != nil {
		log.Fatalf("driver: discover: %v", err)
	}
	fmt.Printf("driver: master agent_id=%s\n", masterID)

	// Phase 2: delegate.
	resp, err := cli.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       masterID,
		Skill:          *skill,
		Prompt:         *prompt,
		TimeoutSeconds: int((*timeout - 60*time.Second).Seconds()),
	})
	if err != nil {
		log.Fatalf("driver: delegate: %v", err)
	}
	fmt.Printf("driver: submitted task_id=%s\n", resp.TaskID)

	// Phase 3: wait for completion.
	info, err := cli.WaitForTask(ctx, resp.TaskID, 2*time.Second)
	if err != nil {
		log.Fatalf("driver: wait: %v", err)
	}
	if info.Status != "completed" {
		log.Fatalf("driver: master task status=%s reason=%s", info.Status, info.FailureReason)
	}
	fmt.Printf("driver: master output (len=%d): %s\n", len(info.Output), truncate(info.Output, 400))

	// Phase 4: assertions on reducer output.
	url, ok := handlepick.FirstURL(info.Output)
	if !ok {
		log.Fatalf("driver: reducer output contains no URL:\n%s", info.Output)
	}
	finalBytes, mime, err := fetchAndCheckJPEG(ctx, url)
	if err != nil {
		log.Fatalf("driver: final URL check: %v", err)
	}
	if !strings.HasPrefix(mime, "image/jpeg") {
		log.Fatalf("driver: final URL Content-Type = %q, want image/jpeg", mime)
	}
	fmt.Printf("driver: final URL %s -> %d bytes (%s)\n", url, finalBytes, mime)

	// Phase 5: optional sqlite deep check.
	if *dbPath != "" {
		if err := checkSubTasks(*dbPath, resp.TaskID); err != nil {
			log.Fatalf("driver: sub-task assertions: %v", err)
		}
		fmt.Println("driver: sub-task assertions passed")
	}

	fmt.Println("OK image-pipeline e2e")
}

func waitForAgents(ctx context.Context, cli *agentsdk.Client, displayNames []string, target string, deadline time.Duration) (string, error) {
	dctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	tk := time.NewTicker(2 * time.Second)
	defer tk.Stop()
	for {
		cards, err := cli.DiscoverAgents(dctx)
		if err == nil {
			present := make(map[string]string)
			for _, c := range cards {
				present[c.DisplayName] = c.AgentID
			}
			missing := []string{}
			for _, n := range displayNames {
				if _, ok := present[n]; !ok {
					missing = append(missing, n)
				}
			}
			if len(missing) == 0 {
				return present[target], nil
			}
			fmt.Printf("driver: waiting for agents: %v\n", missing)
		} else {
			fmt.Printf("driver: discover error (will retry): %v\n", err)
		}
		select {
		case <-dctx.Done():
			return "", fmt.Errorf("timeout waiting for agents %v", displayNames)
		case <-tk.C:
		}
	}
}

func fetchAndCheckJPEG(ctx context.Context, url string) (int, string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return 0, "", fmt.Errorf("status %s", resp.Status)
	}
	mime := resp.Header.Get("Content-Type")
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, mime, err
	}
	if _, _, err := image.Decode(bytesReader(body)); err != nil {
		return len(body), mime, fmt.Errorf("decode: %w", err)
	}
	return len(body), mime, nil
}

func bytesReader(b []byte) io.Reader {
	return strings.NewReader(string(b))
}

func checkSubTasks(dbPath, parentID string) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT node_id, status, output FROM sub_tasks WHERE parent_id = ? ORDER BY node_id`, parentID)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	defer rows.Close()
	type sub struct {
		NodeID, Status, Output string
	}
	var subs []sub
	for rows.Next() {
		var s sub
		var out sql.NullString
		if err := rows.Scan(&s.NodeID, &s.Status, &out); err != nil {
			return err
		}
		s.Output = out.String
		subs = append(subs, s)
	}
	if len(subs) != 2 {
		return fmt.Errorf("expected 2 sub_tasks, got %d", len(subs))
	}
	for _, s := range subs {
		if s.Status != "completed" {
			return fmt.Errorf("sub-task %s status=%s", s.NodeID, s.Status)
		}
	}
	h0, ok := transport.ParseHandle(subs[0].Output)
	if !ok {
		return fmt.Errorf("n1 output not a handle: %s", subs[0].Output)
	}
	h1, ok := transport.ParseHandle(subs[1].Output)
	if !ok {
		return fmt.Errorf("n2 output not a handle: %s", subs[1].Output)
	}
	if h0.MIME != "image/png" {
		return fmt.Errorf("n1.MIME = %q, want image/png", h0.MIME)
	}
	if h1.MIME != "image/jpeg" {
		return fmt.Errorf("n2.MIME = %q, want image/jpeg", h1.MIME)
	}
	if h1.Bytes >= h0.Bytes {
		return fmt.Errorf("compression did not shrink: n1=%d n2=%d", h0.Bytes, h1.Bytes)
	}
	bb, _ := json.Marshal(map[string]interface{}{"n1": h0, "n2": h1})
	fmt.Printf("driver: sub-tasks OK %s\n", string(bb))
	return nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}
