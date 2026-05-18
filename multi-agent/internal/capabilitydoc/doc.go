package capabilitydoc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/claudeperm"
	"github.com/yourorg/multi-agent/internal/config"
)

const Filename = "CAPABILITIES.md"

type Input struct {
	Config         *config.Config
	WorkDir        string
	DynamicMCPPath string
	MCPTools       []capability.MCPToolDescriptor
	Reason         string
}

type Store struct {
	dir string
	mu  sync.Mutex
}

func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

func (s *Store) Refresh(ctx context.Context, in Input) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	doc := render(scan(s.dir, in))
	return atomicWrite(filepath.Join(s.dir, Filename), []byte(doc))
}

type snapshot struct {
	GeneratedAt       string
	Reason            string
	DisplayName       string
	Description       string
	ServerName        string
	WorkDir           string
	Runtime           runtimeInfo
	Skills            []string
	ClaudePermissions claudeperm.State
	Resources         *config.Resources
	Servers           []serverDoc
	CurrentState      string
	RecentHistory     string
	CommandPresence   []commandPresence
}

type runtimeInfo struct {
	Hostname string
	GOOS     string
	GOARCH   string
	NumCPU   int
}

type commandPresence struct {
	Name string
	Path string
}

type serverDoc struct {
	Name      string
	Transport string
	Command   string
	Args      []string
	URL       string
	Dynamic   bool
	Tools     []capability.MCPToolDescriptor
}

func scan(dir string, in Input) snapshot {
	cfg := in.Config
	s := snapshot{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Reason:      in.Reason,
		WorkDir:     in.WorkDir,
		Runtime: runtimeInfo{
			Hostname: hostname(),
			GOOS:     runtime.GOOS,
			GOARCH:   runtime.GOARCH,
			NumCPU:   runtime.NumCPU(),
		},
		CommandPresence: scanCommands(cfg),
	}
	if cfg != nil {
		s.DisplayName = cfg.Discovery.DisplayName
		s.Description = cfg.Discovery.Description
		s.ServerName = cfg.Server.Name
		s.Skills = append([]string{}, cfg.Discovery.Skills...)
		sort.Strings(s.Skills)
		s.Resources = cfg.Resources
		s.Servers = append(s.Servers, staticServers(cfg.MCPServers)...)
	}
	if in.WorkDir != "" {
		if state, err := claudeperm.NewStore(in.WorkDir).Read(); err == nil {
			s.ClaudePermissions = state
		}
	}
	s.Servers = mergeServers(s.Servers, dynamicServers(in.DynamicMCPPath))
	s.Servers = mergeTools(s.Servers, in.MCPTools)
	sort.Slice(s.Servers, func(i, j int) bool { return s.Servers[i].Name < s.Servers[j].Name })
	s.CurrentState = readText(filepath.Join(dir, "CURRENT_STATE.md"))
	s.RecentHistory = tailLines(readText(filepath.Join(dir, "history.md")), 12)
	return s
}

func render(s snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Capability Document\n\n")
	fmt.Fprintf(&b, "Generated: %s\n\n", s.GeneratedAt)
	fmt.Fprintf(&b, "## Summary\n\n")
	writeKV(&b, "display_name", s.DisplayName)
	writeKV(&b, "server_name", s.ServerName)
	writeKV(&b, "description", s.Description)
	writeKV(&b, "refresh_reason", s.Reason)
	fmt.Fprintf(&b, "\n## Runtime\n\n")
	writeKV(&b, "hostname", s.Runtime.Hostname)
	writeKV(&b, "os", s.Runtime.GOOS)
	writeKV(&b, "arch", s.Runtime.GOARCH)
	writeKV(&b, "cpu_cores", fmt.Sprintf("%d", s.Runtime.NumCPU))
	writeKV(&b, "workdir", s.WorkDir)
	if len(s.CommandPresence) > 0 {
		fmt.Fprintf(&b, "\n### Commands\n\n")
		for _, c := range s.CommandPresence {
			fmt.Fprintf(&b, "- `%s`: `%s`\n", c.Name, c.Path)
		}
	}
	fmt.Fprintf(&b, "\n## Skills\n\n")
	if len(s.Skills) == 0 {
		fmt.Fprintf(&b, "- none advertised\n")
	} else {
		for _, skill := range s.Skills {
			fmt.Fprintf(&b, "- %s\n", skill)
		}
	}
	fmt.Fprintf(&b, "\n## Claude Code Permissions\n\n")
	if len(s.ClaudePermissions.Allow) == 0 && len(s.ClaudePermissions.Deny) == 0 {
		fmt.Fprintf(&b, "- none configured\n")
	} else {
		writeList(&b, "allow", s.ClaudePermissions.Allow)
		writeList(&b, "deny", s.ClaudePermissions.Deny)
	}
	fmt.Fprintf(&b, "\n## MCP Servers\n\n")
	if len(s.Servers) == 0 {
		fmt.Fprintf(&b, "- none configured\n")
	} else {
		for _, srv := range s.Servers {
			fmt.Fprintf(&b, "### %s\n\n", srv.Name)
			writeKV(&b, "transport", srv.Transport)
			if srv.Dynamic {
				writeKV(&b, "source", "dynamic_mcp.yaml")
			} else {
				writeKV(&b, "source", "config.yaml")
			}
			writeKV(&b, "command", strings.TrimSpace(strings.Join(append([]string{srv.Command}, srv.Args...), " ")))
			writeKV(&b, "url", srv.URL)
			if len(srv.Tools) == 0 {
				fmt.Fprintf(&b, "\n#### Tools\n\n- no tools discovered yet\n\n")
				continue
			}
			fmt.Fprintf(&b, "\n#### Tools\n\n")
			for _, tool := range srv.Tools {
				name := tool.Name
				if tool.Server != "" && !strings.Contains(name, "/") {
					name = tool.Server + "/" + tool.Name
				}
				fmt.Fprintf(&b, "- `%s`", name)
				if tool.Description != "" {
					fmt.Fprintf(&b, ": %s", tool.Description)
				}
				fmt.Fprintf(&b, "\n")
				if tool.ResultDescription != "" {
					fmt.Fprintf(&b, "  - result: %s\n", tool.ResultDescription)
				}
			}
			fmt.Fprintf(&b, "\n")
		}
	}
	fmt.Fprintf(&b, "## Resources\n\n")
	if s.Resources == nil {
		fmt.Fprintf(&b, "- none configured\n")
	} else {
		if s.Resources.CPU != nil {
			writeKV(&b, "cpu.cores", fmt.Sprintf("%d", s.Resources.CPU.Cores))
			writeKV(&b, "cpu.arch", s.Resources.CPU.Arch)
		}
		if s.Resources.GPU != nil {
			writeKV(&b, "gpu.count", fmt.Sprintf("%d", s.Resources.GPU.Count))
			writeKV(&b, "gpu.model", s.Resources.GPU.Model)
			if s.Resources.GPU.VRAMGB > 0 {
				writeKV(&b, "gpu.vram_gb", fmt.Sprintf("%d", s.Resources.GPU.VRAMGB))
			}
		}
		if s.Resources.MemoryGB > 0 {
			writeKV(&b, "memory_gb", fmt.Sprintf("%d", s.Resources.MemoryGB))
		}
		writeList(&b, "devices", s.Resources.Devices)
		writeList(&b, "tags", s.Resources.Tags)
	}
	fmt.Fprintf(&b, "\n## Current State\n\n")
	if strings.TrimSpace(s.CurrentState) == "" {
		fmt.Fprintf(&b, "_No CURRENT_STATE.md has been recorded yet._\n")
	} else {
		fmt.Fprintf(&b, "%s\n", strings.TrimSpace(s.CurrentState))
	}
	fmt.Fprintf(&b, "\n## Recent Capability Changes\n\n")
	if strings.TrimSpace(s.RecentHistory) == "" {
		fmt.Fprintf(&b, "_No capability change history has been recorded yet._\n")
	} else {
		fmt.Fprintf(&b, "%s\n", strings.TrimSpace(s.RecentHistory))
	}
	return b.String()
}

func writeKV(b *strings.Builder, key, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	fmt.Fprintf(b, "- %s: %s\n", key, value)
}

func writeList(b *strings.Builder, key string, values []string) {
	if len(values) == 0 {
		return
	}
	cp := append([]string{}, values...)
	sort.Strings(cp)
	fmt.Fprintf(b, "- %s: %s\n", key, strings.Join(cp, ", "))
}

func staticServers(in map[string]config.MCPServer) []serverDoc {
	out := make([]serverDoc, 0, len(in))
	for name, srv := range in {
		out = append(out, serverDoc{
			Name: name, Transport: srv.Transport, Command: srv.Command,
			Args: append([]string{}, srv.Args...), URL: srv.URL,
		})
	}
	return out
}

type dynamicFile struct {
	Servers map[string]dynamicEntry `yaml:"servers"`
}

type dynamicEntry struct {
	Transport string                         `yaml:"transport"`
	Command   string                         `yaml:"command"`
	Args      []string                       `yaml:"args"`
	Tools     []capability.MCPToolDescriptor `yaml:"tools"`
}

func (d *dynamicEntry) UnmarshalYAML(value *yaml.Node) error {
	var raw struct {
		Transport string    `yaml:"transport"`
		Command   string    `yaml:"command"`
		Args      []string  `yaml:"args"`
		Tools     yaml.Node `yaml:"tools"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	d.Transport = raw.Transport
	d.Command = raw.Command
	d.Args = raw.Args
	if raw.Tools.Kind == 0 {
		return nil
	}
	var descriptors []capability.MCPToolDescriptor
	if err := raw.Tools.Decode(&descriptors); err == nil {
		d.Tools = descriptors
		return nil
	}
	var names []string
	if err := raw.Tools.Decode(&names); err != nil {
		return err
	}
	d.Tools = make([]capability.MCPToolDescriptor, 0, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		d.Tools = append(d.Tools, capability.MCPToolDescriptor{Name: name})
	}
	return nil
}

func dynamicServers(path string) []serverDoc {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var df dynamicFile
	if err := yaml.Unmarshal(data, &df); err != nil {
		return nil
	}
	out := make([]serverDoc, 0, len(df.Servers))
	for name, srv := range df.Servers {
		out = append(out, serverDoc{
			Name: name, Transport: srv.Transport, Command: srv.Command,
			Args: append([]string{}, srv.Args...), Dynamic: true,
			Tools: capability.WithServer(name, srv.Tools),
		})
	}
	return out
}

func mergeServers(base, extra []serverDoc) []serverDoc {
	byName := map[string]serverDoc{}
	for _, srv := range base {
		byName[srv.Name] = srv
	}
	for _, srv := range extra {
		byName[srv.Name] = srv
	}
	out := make([]serverDoc, 0, len(byName))
	for _, srv := range byName {
		out = append(out, srv)
	}
	return out
}

func mergeTools(servers []serverDoc, tools []capability.MCPToolDescriptor) []serverDoc {
	idx := map[string]int{}
	for i, srv := range servers {
		idx[srv.Name] = i
	}
	for _, tool := range tools {
		if tool.Server == "" {
			continue
		}
		i, ok := idx[tool.Server]
		if !ok {
			servers = append(servers, serverDoc{Name: tool.Server})
			i = len(servers) - 1
			idx[tool.Server] = i
		}
		servers[i].Tools = upsertTool(servers[i].Tools, tool)
	}
	for i := range servers {
		sort.Slice(servers[i].Tools, func(a, b int) bool {
			return servers[i].Tools[a].Name < servers[i].Tools[b].Name
		})
	}
	return servers
}

func upsertTool(tools []capability.MCPToolDescriptor, tool capability.MCPToolDescriptor) []capability.MCPToolDescriptor {
	for i, existing := range tools {
		if existing.Name == tool.Name && existing.Server == tool.Server {
			tools[i] = tool
			return tools
		}
	}
	return append(tools, tool)
}

func scanCommands(cfg *config.Config) []commandPresence {
	names := []string{"claude", "python3", "node", "npm", "go", "docker"}
	if cfg != nil && cfg.Claude.Bin != "" {
		names = append(names, cfg.Claude.Bin)
	}
	seen := map[string]bool{}
	out := []commandPresence{}
	for _, name := range names {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if p, err := exec.LookPath(name); err == nil {
			out = append(out, commandPresence{Name: name, Path: p})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}

func readText(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) <= n {
		return strings.TrimSpace(s)
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
