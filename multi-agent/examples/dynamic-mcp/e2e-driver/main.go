// e2e-driver for the dynamic-mcp example.
//
// Submits a fanout task to master that requires a sha256-image-parity tool
// no agent currently has. Asserts that:
//   - master task completes
//   - reducer output mentions the hash and "even" or "odd"
//   - generated_mcp/image_hash/v1.py exists in the builder's workdir with
//     the AUTO-GENERATED header
//   - dynamic_mcp.yaml has the entry
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"gopkg.in/yaml.v3"
)

// driverConfig is the minimal yaml shape needed by this driver.
type driverConfig struct {
	Server struct {
		URL  string `yaml:"url"`
		Name string `yaml:"name"`
	} `yaml:"server"`
	Credentials struct {
		SandboxID   string `yaml:"sandbox_id"`
		TunnelToken string `yaml:"tunnel_token"`
		ProxyToken  string `yaml:"proxy_token"`
		WorkspaceID string `yaml:"workspace_id"`
		ShortID     string `yaml:"short_id"`
	} `yaml:"credentials"`
}

func loadConfig(path string) (*driverConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c driverConfig
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if c.Server.URL == "" || c.Server.Name == "" {
		return nil, fmt.Errorf("config missing server.url or server.name")
	}
	return &c, nil
}

func main() {
	cfg := flag.String("config", "", "driver agent config (must have credentials)")
	target := flag.String("target-display-name", "master-dynmcp", "master display_name to discover")
	expect := flag.String("expect-agents", "dynmcp-builder", "comma-separated display_names that must also be visible")
	prompt := flag.String("prompt", `Compute SHA-256 of the body returned by GET https://www.example.com/ and tell me whether the last hex digit is even or odd. No agent in this workspace currently has a sha256+parity tool; use bash to generate it on the dynmcp-builder agent and register it with register_mcp.`, "task prompt")
	timeout := flag.Duration("timeout", 600*time.Second, "overall driver timeout")
	builderDir := flag.String("builder-dir", "", "path to builder agent's working directory (for file-existence assertions)")
	flag.Parse()

	if *cfg == "" {
		log.Fatal("--config required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	c, err := loadConfig(*cfg)
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

	want := append(strings.Split(*expect, ","), *target)
	masterID, err := waitForAgents(ctx, cli, want, *target, 60*time.Second)
	if err != nil {
		log.Fatalf("driver: discover: %v", err)
	}
	fmt.Printf("driver: master agent_id=%s\n", masterID)

	resp, err := cli.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       masterID,
		Skill:          "fanout",
		Prompt:         *prompt,
		TimeoutSeconds: int((*timeout - 60*time.Second).Seconds()),
	})
	if err != nil {
		log.Fatalf("driver: delegate: %v", err)
	}
	fmt.Printf("driver: submitted task_id=%s\n", resp.TaskID)

	info, err := cli.WaitForTask(ctx, resp.TaskID, 3*time.Second)
	if err != nil {
		log.Fatalf("driver: wait: %v", err)
	}
	if info.Status != "completed" {
		log.Fatalf("driver: master task status=%s reason=%s", info.Status, info.FailureReason)
	}
	fmt.Printf("driver: master output (len=%d): %s\n", len(info.Output), truncate(info.Output, 400))

	hexRe := regexp.MustCompile(`[0-9a-fA-F]{64}`)
	if !hexRe.MatchString(info.Output) {
		log.Fatalf("driver: reducer output missing 64-hex hash")
	}
	if !strings.Contains(strings.ToLower(info.Output), "even") && !strings.Contains(strings.ToLower(info.Output), "odd") {
		log.Fatalf("driver: reducer output missing parity word")
	}

	if *builderDir != "" {
		// dynamic_mcp.yaml is the source of truth for what got built; the
		// planner picks the tool name based on its understanding of the
		// task, so we don't hardcode it here.
		dyPath := filepath.Join(*builderDir, "dynamic_mcp.yaml")
		dy, err := os.ReadFile(dyPath)
		if err != nil {
			log.Fatalf("driver: expected %s: %v", dyPath, err)
		}
		if !strings.Contains(string(dy), "servers:") {
			log.Fatalf("driver: %s has no servers entries:\n%s", dyPath, string(dy))
		}
		// Find at least one generated python file with the AUTO-GENERATED header.
		matches, _ := filepath.Glob(filepath.Join(*builderDir, "generated_mcp", "*", "v*.py"))
		if len(matches) == 0 {
			log.Fatalf("driver: no generated_mcp/*/v*.py files found in %s", *builderDir)
		}
		ok := false
		for _, m := range matches {
			b, err := os.ReadFile(m)
			if err == nil && strings.HasPrefix(string(b), "# -*- coding: utf-8 -*-\n# AUTO-GENERATED") {
				fmt.Printf("driver: found generated file %s with AUTO-GENERATED header\n", m)
				ok = true
				break
			}
		}
		if !ok {
			log.Fatalf("driver: no generated_mcp/*/v*.py file has the AUTO-GENERATED header")
		}
	}

	fmt.Println("OK dynamic-mcp e2e")
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

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}
