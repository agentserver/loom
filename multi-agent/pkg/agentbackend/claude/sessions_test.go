package claude

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

func setTestHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func TestEncodeCwdWindowsDrivePathIsAValidProjectDir(t *testing.T) {
	encoded := encodeCwd(`C:\Users\runneradmin\project`)
	if strings.ContainsAny(encoded, `<>:"/\|?*`) {
		t.Fatalf("encoded cwd contains characters invalid in Windows directory names: %q", encoded)
	}
	if encoded != "C--Users-runneradmin-project" {
		t.Fatalf("encoded cwd=%q want C--Users-runneradmin-project", encoded)
	}
}

func TestDecodeCwdWindowsDrivePath(t *testing.T) {
	got := decodeCwd("C--Users-runneradmin-project")
	want := filepath.FromSlash("C:/Users/runneradmin/project")
	if got != want {
		t.Fatalf("decodeCwd=%q want %q", got, want)
	}
}

func TestListSessions_EmptyDir(t *testing.T) {
	setTestHome(t, t.TempDir())

	b := New(agentbackend.Config{Bin: "claude", WorkDir: t.TempDir()}, nil)
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
	setTestHome(t, home)

	b := New(agentbackend.Config{Bin: "claude", WorkDir: t.TempDir()}, nil)
	got, err := b.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d sessions, want 3 (ids=%v)", len(got), sessionIDs(got))
	}

	want := map[string]bool{
		"aaaa1111-bbbb-2222-cccc-333333333333": false,
		"bbbb2222-cccc-3333-dddd-444444444444": false,
		"cccc3333-dddd-4444-eeee-555555555555": false,
	}
	for _, s := range got {
		if _, ok := want[s.ID]; !ok {
			t.Errorf("unexpected session id %q", s.ID)
		}
		want[s.ID] = true
		if s.Kind != agentbackend.KindClaude {
			t.Errorf("session %s: kind=%v want claude", s.ID, s.Kind)
		}
		if s.WorkingDir != "/tmp/myproj" {
			t.Errorf("session %s: WorkingDir=%q want /tmp/myproj", s.ID, s.WorkingDir)
		}
	}
	for id, found := range want {
		if !found {
			t.Errorf("missing session %s", id)
		}
	}
}

func TestListSessions_ToleratesCorruptFile(t *testing.T) {
	home := copyFixtureToHOME(t)
	setTestHome(t, home)

	b := New(agentbackend.Config{Bin: "claude", WorkDir: t.TempDir()}, nil)
	got, err := b.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions errored even though one file is corrupt: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 sessions even with corrupt file; got %d", len(got))
	}
}

func TestGetSession_ReturnsMessages(t *testing.T) {
	home := copyFixtureToHOME(t)
	setTestHome(t, home)

	b := New(agentbackend.Config{Bin: "claude", WorkDir: t.TempDir()}, nil)
	sess, msgs, err := b.GetSession(context.Background(), "aaaa1111-bbbb-2222-cccc-333333333333")
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID != "aaaa1111-bbbb-2222-cccc-333333333333" {
		t.Errorf("sess.ID=%q", sess.ID)
	}
	if sess.MessageCount != 4 {
		t.Errorf("MessageCount=%d want 4", sess.MessageCount)
	}
	if sess.Preview != "4" {
		t.Errorf("Preview=%q want 4", sess.Preview)
	}
	if len(msgs) != 4 {
		t.Fatalf("got %d messages, want 4", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Text != "hello, claude" {
		t.Errorf("msgs[0]=%+v", msgs[0])
	}
	if msgs[3].Role != "assistant" || msgs[3].Text != "4" {
		t.Errorf("msgs[3]=%+v", msgs[3])
	}
}

func TestGetSession_UnknownIDReturnsErrSessionNotFound(t *testing.T) {
	home := copyFixtureToHOME(t)
	setTestHome(t, home)

	b := New(agentbackend.Config{Bin: "claude", WorkDir: t.TempDir()}, nil)
	_, _, err := b.GetSession(context.Background(), "no-such-id")
	if !errors.Is(err, agentbackend.ErrSessionNotFound) {
		t.Fatalf("err=%v, want ErrSessionNotFound", err)
	}
}

func TestGetSession_RespectsPreviewCap(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	dir := filepath.Join(home, ".claude", "projects", "-tmp-myproj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "dddd4444-eeee-5555-ffff-666666666666"
	body := `{"type":"assistant","timestamp":"2026-06-14T12:00:00Z","sessionId":"` + id + `","message":{"role":"assistant","content":"` + strings.Repeat("a", 400) + `"}}`
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	b := New(agentbackend.Config{Bin: "claude", WorkDir: t.TempDir()}, nil)
	sess, _, err := b.GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Preview) > agentbackend.SessionPreviewMaxBytes {
		t.Fatalf("preview length=%d, want <= %d", len(sess.Preview), agentbackend.SessionPreviewMaxBytes)
	}
}

func sessionIDs(ss []agentbackend.Session) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.ID)
	}
	return out
}
