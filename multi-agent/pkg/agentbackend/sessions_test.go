package agentbackend_test

import (
	"context"
	"errors"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/claude"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/codex"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/opencode"
)

func TestListSessions_EmptyHomeEveryBackend(t *testing.T) {
	emptyHome := t.TempDir()
	setTestHome(t, emptyHome)

	for _, k := range agentbackend.RegisteredKinds() {
		t.Run(k, func(t *testing.T) {
			b, err := agentbackend.New(agentbackend.Config{
				Kind:    agentbackend.Kind(k),
				Bin:     k,
				WorkDir: emptyHome,
			}, nil)
			if err != nil {
				t.Fatalf("New(%s): %v", k, err)
			}

			sessions, err := b.ListSessions(context.Background())
			if err != nil {
				t.Fatalf("ListSessions(%s) on empty home: %v", k, err)
			}
			if len(sessions) != 0 {
				t.Fatalf("ListSessions(%s) on empty home: got %d sessions, want 0", k, len(sessions))
			}
		})
	}
}

func TestGetSession_UnknownIDEveryBackend(t *testing.T) {
	emptyHome := t.TempDir()
	setTestHome(t, emptyHome)

	for _, k := range agentbackend.RegisteredKinds() {
		t.Run(k, func(t *testing.T) {
			b, err := agentbackend.New(agentbackend.Config{
				Kind:    agentbackend.Kind(k),
				Bin:     k,
				WorkDir: emptyHome,
			}, nil)
			if err != nil {
				t.Fatalf("New(%s): %v", k, err)
			}

			_, _, err = b.GetSession(context.Background(), "no-such-session-id")
			if !errors.Is(err, agentbackend.ErrSessionNotFound) {
				t.Fatalf("GetSession(%s) unknown id: got err=%v, want ErrSessionNotFound", k, err)
			}
		})
	}
}

func setTestHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/.config")
	t.Setenv("XDG_DATA_HOME", home+"/.local/share")
	t.Setenv("APPDATA", home+"/.local/share")
}
