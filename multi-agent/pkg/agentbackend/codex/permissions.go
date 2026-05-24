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

// readMap decodes config.toml into a top-level key map. Missing file ⇒ empty map.
// Returning a map (rather than a struct) lets Patch preserve unknown keys —
// e.g. mcp_servers tables or model overrides written by the user — across writes.
func (s *Store) readMap() (map[string]any, error) {
	data, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]any{}
	if len(data) == 0 {
		return m, nil
	}
	if err := toml.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// modeOf extracts the canonical mode string from the on-disk sandbox_mode value.
func modeOf(m map[string]any) string {
	v, _ := m["sandbox_mode"].(string)
	switch v {
	case "danger-full-access":
		return "full-access"
	case "workspace-write":
		return "workspace-write"
	default:
		return "ask"
	}
}

// Get returns the current permissions state without modifying the file.
// If config.toml does not exist the mode defaults to "ask".
func (s *Store) Get(_ context.Context) (agentbackend.State, error) {
	m, err := s.readMap()
	if err != nil {
		return agentbackend.State{}, err
	}
	return agentbackend.State{
		Backend: agentbackend.KindCodex,
		Path:    s.path(),
		Mode:    modeOf(m),
	}, nil
}

// Patch applies p and persists the result to config.toml. Unknown top-level keys
// in the existing file are preserved verbatim across the write.
// AllowAdd/AllowRemove/DenyAdd/DenyRemove are rejected — those are Claude-only.
func (s *Store) Patch(ctx context.Context, p agentbackend.Patch) (agentbackend.State, error) {
	if len(p.AllowAdd) > 0 || len(p.AllowRemove) > 0 ||
		len(p.DenyAdd) > 0 || len(p.DenyRemove) > 0 {
		return agentbackend.State{}, fmt.Errorf(
			"codex backend does not accept Patch.AllowAdd/AllowRemove/DenyAdd/DenyRemove (claude-only); use Presets or Mode",
		)
	}

	m, err := s.readMap()
	if err != nil {
		return agentbackend.State{}, err
	}

	cur := modeOf(m)
	var newMode string
	switch {
	case p.Mode != "":
		newMode = p.Mode
	case len(p.Presets) > 0:
		mode, _ := presetToMode(p.Presets, cur)
		newMode = mode
	default:
		newMode = cur
	}

	switch newMode {
	case "ask":
		delete(m, "sandbox_mode")
		delete(m, "approval_policy")
	case "workspace-write":
		m["sandbox_mode"] = "workspace-write"
		m["approval_policy"] = "on-request"
	case "full-access":
		m["sandbox_mode"] = "danger-full-access"
		m["approval_policy"] = "never"
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
	if err := toml.NewEncoder(f).Encode(m); err != nil {
		return agentbackend.State{}, err
	}

	return s.Get(ctx)
}
