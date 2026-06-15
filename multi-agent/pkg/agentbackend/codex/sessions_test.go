package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func copyFixtureToHOME(t *testing.T) string {
	t.Helper()
	src := filepath.Join("testdata", "sessions")
	dst := t.TempDir()
	if err := copyTree(src, dst); err != nil {
		t.Fatal(err)
	}
	return dst
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, data, 0o600)
	})
}

func TestListSessions_EmptyDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	b := New(agentbackend.Config{Bin: "codex", WorkDir: t.TempDir()}, nil)
	got, err := b.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d sessions, want 0", len(got))
	}
}

func TestListSessions_ReturnsKnownSessions(t *testing.T) {
	home := copyFixtureToHOME(t)
	t.Setenv("HOME", home)

	b := New(agentbackend.Config{Bin: "codex", WorkDir: t.TempDir()}, nil)
	got, err := b.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d sessions, want 3", len(got))
	}

	wantIDs := map[string]string{
		"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee": "/tmp/codex-proj",
		"ffffffff-1111-2222-3333-444444444444": "",
		"99999999-aaaa-bbbb-cccc-dddddddddddd": "/tmp/empty-codex",
	}
	for _, s := range got {
		wantCwd, ok := wantIDs[s.ID]
		if !ok {
			t.Errorf("unexpected id %q", s.ID)
			continue
		}
		if s.Kind != agentbackend.KindCodex {
			t.Errorf("session %s: kind=%v want codex", s.ID, s.Kind)
		}
		if s.WorkingDir != wantCwd {
			t.Errorf("session %s: cwd=%q want %q", s.ID, s.WorkingDir, wantCwd)
		}
	}
}

func TestListSessions_ToleratesCorruptFile(t *testing.T) {
	home := copyFixtureToHOME(t)
	t.Setenv("HOME", home)

	b := New(agentbackend.Config{Bin: "codex", WorkDir: t.TempDir()}, nil)
	got, err := b.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions errored with corrupt file: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 sessions even with corrupt file; got %d", len(got))
	}
}

func TestGetSession_ReturnsMessages(t *testing.T) {
	home := copyFixtureToHOME(t)
	t.Setenv("HOME", home)

	b := New(agentbackend.Config{Bin: "codex", WorkDir: t.TempDir()}, nil)
	sess, msgs, err := b.GetSession(context.Background(), "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	if err != nil {
		t.Fatal(err)
	}
	if sess.WorkingDir != "/tmp/codex-proj" {
		t.Errorf("WorkingDir=%q", sess.WorkingDir)
	}
	if sess.MessageCount != 4 {
		t.Errorf("MessageCount=%d want 4", sess.MessageCount)
	}
	if sess.Preview != "division by zero is undefined" {
		t.Errorf("Preview=%q", sess.Preview)
	}
	if len(msgs) != 4 {
		t.Fatalf("len(msgs)=%d want 4", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Text != "sum 2 and 3" {
		t.Errorf("msgs[0]=%+v", msgs[0])
	}
	if msgs[3].Role != "assistant" || msgs[3].Text != "division by zero is undefined" {
		t.Errorf("msgs[3]=%+v", msgs[3])
	}
}

func TestGetSession_UnknownIDReturnsErrSessionNotFound(t *testing.T) {
	home := copyFixtureToHOME(t)
	t.Setenv("HOME", home)

	b := New(agentbackend.Config{Bin: "codex", WorkDir: t.TempDir()}, nil)
	_, _, err := b.GetSession(context.Background(), "no-such-id")
	if !errors.Is(err, agentbackend.ErrSessionNotFound) {
		t.Fatalf("err=%v want ErrSessionNotFound", err)
	}
}

func TestGetSession_RespectsPreviewCap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".codex", "sessions", "2026", "01", "15")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "dddd4444-eeee-5555-ffff-666666666666"
	body := `{"timestamp":"2026-01-15T10:00:00.000Z","type":"session_meta","payload":{"id":"` + id + `","cwd":"/tmp/codex-long"}}` + "\n" +
		`{"timestamp":"2026-01-15T10:00:01.000Z","type":"model_output","payload":{"text":"` + strings.Repeat("b", 400) + `"}}`
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-01-15T10-00-00-"+id+".jsonl"), []byte(body+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	b := New(agentbackend.Config{Bin: "codex", WorkDir: t.TempDir()}, nil)
	sess, _, err := b.GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Preview) > 256 {
		t.Fatalf("preview length=%d, want <= 256", len(sess.Preview))
	}
}
