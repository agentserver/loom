package opencode

import (
	"context"
	"errors"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestListSessions_EmptyDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", home+"/.local/share")

	b := New(agentbackend.Config{Bin: "opencode", WorkDir: t.TempDir()}, nil)
	got, err := b.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d sessions, want 0", len(got))
	}
}

func TestListSessions_ReturnsKnownSessions(t *testing.T) {
	home := buildFixtureDB(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", home+"/.local/share")

	b := New(agentbackend.Config{Bin: "opencode", WorkDir: t.TempDir()}, nil)
	got, err := b.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d sessions, want 3", len(got))
	}

	wantIDs := map[string]string{
		"ses_a": "/tmp/opencode-a",
		"ses_b": "/tmp/opencode-b",
		"ses_c": "/tmp/opencode-c",
	}
	for _, s := range got {
		wantCwd, ok := wantIDs[s.ID]
		if !ok {
			t.Errorf("unexpected id %q", s.ID)
			continue
		}
		if s.Kind != agentbackend.KindOpencode {
			t.Errorf("session %s: kind=%v want opencode", s.ID, s.Kind)
		}
		if s.WorkingDir != wantCwd {
			t.Errorf("session %s: cwd=%q want %q", s.ID, s.WorkingDir, wantCwd)
		}
	}
}

func TestListSessions_ToleratesCorruptParts(t *testing.T) {
	home := buildFixtureDB(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", home+"/.local/share")

	b := New(agentbackend.Config{Bin: "opencode", WorkDir: t.TempDir()}, nil)
	got, err := b.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions errored with corrupt part: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 sessions even with corrupt part; got %d", len(got))
	}
}

func TestGetSession_ReturnsMessages(t *testing.T) {
	home := buildFixtureDB(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", home+"/.local/share")

	b := New(agentbackend.Config{Bin: "opencode", WorkDir: t.TempDir()}, nil)
	sess, msgs, err := b.GetSession(context.Background(), "ses_a")
	if err != nil {
		t.Fatal(err)
	}
	if sess.WorkingDir != "/tmp/opencode-a" {
		t.Errorf("WorkingDir=%q", sess.WorkingDir)
	}
	if sess.MessageCount != 4 {
		t.Errorf("MessageCount=%d want 4", sess.MessageCount)
	}
	if sess.Preview != "final answer" {
		t.Errorf("Preview=%q", sess.Preview)
	}
	if len(msgs) != 4 {
		t.Fatalf("len(msgs)=%d want 4", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Text != "hello from a" {
		t.Errorf("msgs[0]=%+v", msgs[0])
	}
	if msgs[3].Role != "assistant" || msgs[3].Text != "final answer" {
		t.Errorf("msgs[3]=%+v", msgs[3])
	}
}

func TestGetSession_UnknownIDReturnsErrSessionNotFound(t *testing.T) {
	home := buildFixtureDB(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", home+"/.local/share")

	b := New(agentbackend.Config{Bin: "opencode", WorkDir: t.TempDir()}, nil)
	_, _, err := b.GetSession(context.Background(), "no-such-id")
	if !errors.Is(err, agentbackend.ErrSessionNotFound) {
		t.Fatalf("err=%v want ErrSessionNotFound", err)
	}
}

func TestGetSession_RespectsPreviewCap(t *testing.T) {
	home := buildFixtureDB(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", home+"/.local/share")
	id := addLongPreviewSession(t, home)

	b := New(agentbackend.Config{Bin: "opencode", WorkDir: t.TempDir()}, nil)
	sess, _, err := b.GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Preview) > 256 {
		t.Fatalf("preview length=%d, want <= 256", len(sess.Preview))
	}
}

func TestGetSession_MessageTableSchemaReturnsMessages(t *testing.T) {
	home := buildFixtureDB(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", home+"/.local/share")
	id := addMessageTableSession(t, home)

	b := New(agentbackend.Config{Bin: "opencode", WorkDir: t.TempDir()}, nil)
	sess, msgs, err := b.GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if sess.MessageCount != 2 {
		t.Errorf("MessageCount=%d want 2", sess.MessageCount)
	}
	if sess.Preview != "real assistant" {
		t.Errorf("Preview=%q want real assistant", sess.Preview)
	}
	if len(msgs) != 2 {
		t.Fatalf("len(msgs)=%d want 2", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Text != "real user" {
		t.Errorf("msgs[0]=%+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Text != "real assistant" {
		t.Errorf("msgs[1]=%+v", msgs[1])
	}
}

func TestListSessions_MessageTableSchemaSetsPreview(t *testing.T) {
	home := buildFixtureDB(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", home+"/.local/share")
	id := addMessageTableSession(t, home)

	b := New(agentbackend.Config{Bin: "opencode", WorkDir: t.TempDir()}, nil)
	got, err := b.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range got {
		if s.ID != id {
			continue
		}
		if s.MessageCount != 2 {
			t.Errorf("MessageCount=%d want 2", s.MessageCount)
		}
		if s.Preview != "real assistant" {
			t.Errorf("Preview=%q want real assistant", s.Preview)
		}
		return
	}
	t.Fatalf("session %s not listed", id)
}
