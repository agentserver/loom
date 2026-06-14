package opencode

import (
	"context"
	"fmt"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// Store is a NoOp PermissionsStore for opencode. opencode does not have
// an on-disk permissions schema analogous to claude's settings.json or
// codex's config.toml sandbox_mode — operators control opencode access
// via `opencode auth login` (provider keys) and the
// --dangerously-skip-permissions flag the slave-agent injects by default.
//
// Get always returns Mode="ask" so the surface looks like the other
// backends. Patch echoes a new Mode back without persisting (the value
// is meaningful only for THIS call, not future ones) and rejects the
// Allow/Deny list fields which are claude-only conventions.
//
// Mirrors codex/permissions.go's rejection list but skips the TOML
// round-trip because there's nothing to persist into.
type Store struct{ workdir string }

func NewStore(workdir string) *Store { return &Store{workdir: workdir} }

func (s *Store) Get(_ context.Context) (agentbackend.State, error) {
	return agentbackend.State{
		Backend: agentbackend.KindOpencode,
		Path:    "", // no on-disk file
		Mode:    "ask",
	}, nil
}

func (s *Store) Patch(_ context.Context, p agentbackend.Patch) (agentbackend.State, error) {
	if len(p.AllowAdd) > 0 || len(p.AllowRemove) > 0 ||
		len(p.DenyAdd) > 0 || len(p.DenyRemove) > 0 {
		return agentbackend.State{}, fmt.Errorf(
			"opencode backend does not accept Patch.AllowAdd/AllowRemove/DenyAdd/DenyRemove (claude-only); use Mode")
	}
	mode := "ask"
	if p.Mode != "" {
		mode = p.Mode
	} else if len(p.Presets) > 0 {
		// Preset "workspace_write" / "full_access" map to obvious modes;
		// anything else is unknown and rejected. Same shape codex uses.
		switch p.Presets[len(p.Presets)-1] {
		case "workspace_write":
			mode = "workspace-write"
		case "full_access":
			mode = "full-access"
		case "ask":
			mode = "ask"
		default:
			return agentbackend.State{}, fmt.Errorf("opencode: unknown permissions preset %q", p.Presets[len(p.Presets)-1])
		}
	}
	return agentbackend.State{
		Backend: agentbackend.KindOpencode,
		Path:    "",
		Mode:    mode,
	}, nil
}
