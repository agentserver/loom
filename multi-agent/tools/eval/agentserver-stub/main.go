// agentserver-stub is the stripped-down agentserver eval-runner uses to bring
// driver/slave/observer up without OAuth device flow. See README.md.
//
// ⚠️  NOT FOR PRODUCTION.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

const productionWarning = "⚠️  NOT FOR PRODUCTION — see tools/eval/agentserver-stub/README.md"

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "issue" {
		runIssue(os.Args[2:])
		return
	}
	runServe(os.Args[1:])
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", ":18080", "listen address; loopback recommended (NOT FOR PRODUCTION)")
	workspace := fs.String("workspace-id", "auto", `default workspace_id for registrations; "auto" => "ws-eval-auto"`)
	_ = fs.Parse(args)

	srv := NewServer(*workspace)
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("agentserver-stub ")
	log.Println(productionWarning)
	log.Printf("listening on %s, default workspace=%s", *listen, srv.defaultWorkspace)
	if err := http.ListenAndServe(*listen, srv.Handler()); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

func runIssue(args []string) {
	fs := flag.NewFlagSet("issue", flag.ExitOnError)
	server := fs.String("server", "http://127.0.0.1:18080", "stub server URL")
	role := fs.String("role", "", "agent role: driver | slave-a | slave-b | observer | ...")
	shortID := fs.String("short-id", "", "agent short_id (e.g. drv-001)")
	workspace := fs.String("workspace-id", "", "optional workspace_id; empty => server default")
	timeout := fs.Duration("timeout", 3*time.Second, "request timeout")
	_ = fs.Parse(args)
	if *role == "" || *shortID == "" {
		fmt.Fprintln(os.Stderr, "issue: --role and --short-id are required")
		os.Exit(2)
	}

	body, _ := json.Marshal(registerRequest{
		Role:        *role,
		ShortID:     *shortID,
		WorkspaceID: *workspace,
	})
	req, err := http.NewRequest(http.MethodPost, *server+"/api/v1/agents/register", bytes.NewReader(body))
	if err != nil {
		die("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: *timeout}
	resp, err := client.Do(req)
	if err != nil {
		die("post register: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		die("register: status %d: %s", resp.StatusCode, b)
	}
	if _, err := io.Copy(os.Stdout, resp.Body); err != nil {
		die("copy response: %v", err)
	}
	fmt.Println()
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "agentserver-stub: "+format+"\n", args...)
	os.Exit(1)
}
