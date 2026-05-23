package codex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// Store is a PermissionsStore backed by <workdir>/.codex/config.toml.
type Store struct{ workdir string }

// NewStore creates a Store rooted at workdir. The .codex/ subdirectory and
// config.toml are created lazily on the first Patch call.
func NewStore(workdir string) *Store { return &Store{workdir: workdir} }

func (s *Store) path() string { return filepath.Join(s.workdir, ".codex", "config.toml") }

// rawConfig is a minimal representation of .codex/config.toml.
// Only the two keys we own are decoded; all other keys in a real-world file
// are intentionally discarded on write (known limitation — see task spec).
type rawConfig struct {
	ApprovalPolicy string `toml:"approval_policy,omitempty"`
	SandboxMode    string `toml:"sandbox_mode,omitempty"`
}

func (s *Store) read() (rawConfig, error) {
	var c rawConfig
	data, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := toml.Unmarshal(data, &c); err != nil {
		return c, err
	}
	return c, nil
}

// Get returns the current permissions state without modifying the file.
// If config.toml does not exist the mode defaults to "ask".
func (s *Store) Get(_ context.Context) (agentbackend.State, error) {
	c, err := s.read()
	if err != nil {
		return agentbackend.State{}, err
	}
	return agentbackend.State{
		Backend: agentbackend.KindCodex,
		Path:    s.path(),
		Mode:    codexMode(c),
	}, nil
}

// codexMode maps rawConfig fields to the canonical mode string.
func codexMode(c rawConfig) string {
	switch c.SandboxMode {
	case "danger-full-access":
		return "full-access"
	case "workspace-write":
		return "workspace-write"
	default:
		return "ask"
	}
}

// Patch applies p and persists the result to config.toml.
// AllowAdd/AllowRemove/DenyAdd/DenyRemove are rejected — those are Claude-only.
func (s *Store) Patch(ctx context.Context, p agentbackend.Patch) (agentbackend.State, error) {
	if len(p.AllowAdd) > 0 || len(p.AllowRemove) > 0 ||
		len(p.DenyAdd) > 0 || len(p.DenyRemove) > 0 {
		return agentbackend.State{}, fmt.Errorf(
			"codex backend does not accept Patch.AllowAdd/AllowRemove/DenyAdd/DenyRemove (claude-only); use Presets or Mode",
		)
	}

	c, err := s.read()
	if err != nil {
		return agentbackend.State{}, err
	}

	cur := codexMode(c)
	var newMode string
	switch {
	case p.Mode != "":
		newMode = p.Mode
	case len(p.Presets) > 0:
		m, _ := presetToMode(p.Presets, cur)
		newMode = m
	default:
		newMode = cur
	}

	switch newMode {
	case "ask":
		c.SandboxMode = ""
		c.ApprovalPolicy = ""
	case "workspace-write":
		c.SandboxMode = "workspace-write"
		c.ApprovalPolicy = "on-request"
	case "full-access":
		c.SandboxMode = "danger-full-access"
		c.ApprovalPolicy = "never"
	default:
		return agentbackend.State{}, fmt.Errorf("unknown codex mode %q", newMode)
	}

	if err := os.MkdirAll(filepath.Dir(s.path()), 0o700); err != nil {
		return agentbackend.State{}, err
	}
	f, err := os.OpenFile(s.path(), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return agentbackend.State{}, err
	}
	defer f.Close()
	if err := toml.NewEncoder(f).Encode(c); err != nil {
		return agentbackend.State{}, err
	}

	return s.Get(ctx)
}
