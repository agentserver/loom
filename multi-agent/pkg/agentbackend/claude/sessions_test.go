package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

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
	gotByID := map[string]agentbackend.Session{}
	for _, s := range got {
		if _, ok := want[s.ID]; !ok {
			t.Errorf("unexpected session id %q", s.ID)
		}
		want[s.ID] = true
		gotByID[s.ID] = s
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
	if gotByID["aaaa1111-bbbb-2222-cccc-333333333333"].Title != "hello, claude" {
		t.Fatalf("Title=%q want first user prompt", gotByID["aaaa1111-bbbb-2222-cccc-333333333333"].Title)
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
	if sess.Title != "hello, claude" {
		t.Errorf("Title=%q want hello, claude", sess.Title)
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

func TestGetSession_SkipsClaudeMetaUserMessagesForTitle(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	dir := filepath.Join(home, ".claude", "projects", "-tmp-myproj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "eeee4444-ffff-5555-aaaa-666666666666"
	body := strings.Join([]string{
		`{"type":"user","timestamp":"2026-06-17T01:00:00Z","sessionId":"` + id + `","isMeta":true,"message":{"role":"user","content":"<local-command-caveat>ignore generated local command messages</local-command-caveat>"}}`,
		`{"type":"user","timestamp":"2026-06-17T01:00:01Z","sessionId":"` + id + `","message":{"role":"user","content":"<command-name>/status</command-name>\n<command-message>status</command-message>"}}`,
		`{"type":"user","timestamp":"2026-06-17T01:00:02Z","sessionId":"` + id + `","message":{"role":"user","content":"实现 commander session 标题优化"}}`,
		`{"type":"assistant","timestamp":"2026-06-17T01:00:03Z","sessionId":"` + id + `","message":{"role":"assistant","content":[{"type":"text","text":"可以。"}]}}`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	b := New(agentbackend.Config{Bin: "claude", WorkDir: t.TempDir()}, nil)
	listed, err := b.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("len(ListSessions)=%d want 1", len(listed))
	}
	if listed[0].Title != "实现 commander session 标题优化" {
		t.Fatalf("ListSessions title=%q want first real user prompt", listed[0].Title)
	}

	sess, msgs, err := b.GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Title != "实现 commander session 标题优化" {
		t.Fatalf("GetSession title=%q want first real user prompt", sess.Title)
	}
	if len(msgs) != 4 {
		t.Fatalf("len(msgs)=%d want 4", len(msgs))
	}
}

func TestGetSession_ContinuesAfterOversizedClaudeLine(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	dir := filepath.Join(home, ".claude", "projects", "-tmp-myproj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "99999999-8888-7777-6666-555555555555"
	body := strings.Repeat("{", 4*1024*1024+1) + "\n" +
		`{"type":"user","timestamp":"2026-06-17T01:00:02Z","sessionId":"` + id + `","message":{"role":"user","content":"still parsed after huge line"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	b := New(agentbackend.Config{Bin: "claude", WorkDir: t.TempDir()}, nil)
	sess, msgs, err := b.GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Title != "still parsed after huge line" {
		t.Fatalf("Title=%q want later valid line", sess.Title)
	}
	if len(msgs) != 1 || msgs[0].Text != "still parsed after huge line" {
		t.Fatalf("msgs=%+v want later valid line", msgs)
	}
}

func TestListSessions_IncludesClaudeSubagents(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	projectDir := filepath.Join(home, ".claude", "projects", "-tmp-myproj")
	parentID := "11111111-2222-3333-4444-555555555555"
	parentFile := filepath.Join(projectDir, parentID+".jsonl")
	subagentID := "agent-abcdef1234567890"
	subagentDir := filepath.Join(projectDir, parentID, "subagents")
	if err := os.MkdirAll(subagentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	parentBody := `{"type":"user","timestamp":"2026-06-17T02:00:00Z","sessionId":"` + parentID + `","message":{"role":"user","content":"父会话"}}` + "\n"
	if err := os.WriteFile(parentFile, []byte(parentBody), 0o600); err != nil {
		t.Fatal(err)
	}
	subagentBody := strings.Join([]string{
		`{"type":"user","timestamp":"2026-06-17T02:00:01Z","sessionId":"` + parentID + `","isSidechain":true,"agentId":"abcdef1234567890","message":{"role":"user","content":"审查父会话实现"}}`,
		`{"type":"assistant","timestamp":"2026-06-17T02:00:02Z","sessionId":"` + parentID + `","isSidechain":true,"agentId":"abcdef1234567890","message":{"role":"assistant","content":[{"type":"text","text":"审查完成"}]}}`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(subagentDir, subagentID+".jsonl"), []byte(subagentBody+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	b := New(agentbackend.Config{Bin: "claude", WorkDir: t.TempDir()}, nil)
	listed, err := b.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("len(ListSessions)=%d want 2", len(listed))
	}
	byID := map[string]agentbackend.Session{}
	for _, sess := range listed {
		byID[sess.ID] = sess
	}
	if byID[parentID].Origin != agentbackend.SessionOriginUser {
		t.Fatalf("parent Origin=%q want user", byID[parentID].Origin)
	}
	sub := byID[subagentID]
	if sub.Origin != agentbackend.SessionOriginSubagent {
		t.Fatalf("subagent Origin=%q want subagent", sub.Origin)
	}
	if sub.ParentID != parentID || sub.AgentName != "abcdef1234567890" {
		t.Fatalf("subagent metadata mismatch: %+v", sub)
	}

	detail, msgs, err := b.GetSession(context.Background(), subagentID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Origin != agentbackend.SessionOriginSubagent || detail.ParentID != parentID {
		t.Fatalf("GetSession subagent metadata mismatch: %+v", detail)
	}
	if len(msgs) != 2 {
		t.Fatalf("len(msgs)=%d want 2", len(msgs))
	}
}

func TestListSessions_DoesNotAssignClaudeSidechainSelfParent(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	projectDir := filepath.Join(home, ".claude", "projects", "-tmp-myproj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "77777777-8888-9999-aaaa-bbbbbbbbbbbb"
	body := `{"type":"user","timestamp":"2026-06-17T02:00:01Z","sessionId":"` + id + `","isSidechain":true,"agentId":"reviewer","message":{"role":"user","content":"sidechain without path parent"}}` + "\n"
	if err := os.WriteFile(filepath.Join(projectDir, id+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	b := New(agentbackend.Config{Bin: "claude", WorkDir: t.TempDir()}, nil)
	listed, err := b.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("len(ListSessions)=%d want 1", len(listed))
	}
	if listed[0].ParentID == listed[0].ID {
		t.Fatalf("ParentID self-reference: %+v", listed[0])
	}

	detail, _, err := b.GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ParentID == detail.ID {
		t.Fatalf("detail ParentID self-reference: %+v", detail)
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

func TestTitleFromUserText_TruncatesAtValidUTF8Boundary(t *testing.T) {
	title := titleFromUserText(strings.Repeat("界", agentbackend.SessionPreviewMaxBytes))
	if len(title) > agentbackend.SessionPreviewMaxBytes {
		t.Fatalf("title length=%d, want <= %d", len(title), agentbackend.SessionPreviewMaxBytes)
	}
	if !utf8.ValidString(title) {
		t.Fatalf("title is not valid UTF-8: %q", title)
	}
}

func sessionIDs(ss []agentbackend.Session) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.ID)
	}
	return out
}
