package codex

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

func TestListSessions_EmptyDir(t *testing.T) {
	setTestHome(t, t.TempDir())

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
	setTestHome(t, home)

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
	gotByID := map[string]agentbackend.Session{}
	for _, s := range got {
		wantCwd, ok := wantIDs[s.ID]
		if !ok {
			t.Errorf("unexpected id %q", s.ID)
			continue
		}
		gotByID[s.ID] = s
		if s.Kind != agentbackend.KindCodex {
			t.Errorf("session %s: kind=%v want codex", s.ID, s.Kind)
		}
		if s.WorkingDir != wantCwd {
			t.Errorf("session %s: cwd=%q want %q", s.ID, s.WorkingDir, wantCwd)
		}
	}
	if gotByID["aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"].Title != "sum 2 and 3" {
		t.Fatalf("Title=%q want first user prompt", gotByID["aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"].Title)
	}
}

func TestListSessions_ToleratesCorruptFile(t *testing.T) {
	home := copyFixtureToHOME(t)
	setTestHome(t, home)

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
	setTestHome(t, home)

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
	if sess.Title != "sum 2 and 3" {
		t.Errorf("Title=%q want sum 2 and 3", sess.Title)
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

func TestGetSession_CurrentRolloutResponseItemsReturnMessages(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	dir := filepath.Join(home, ".codex", "sessions", "2026", "06", "16")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "bbbbbbbb-1111-2222-3333-cccccccccccc"
	body := strings.Join([]string{
		`{"timestamp":"2026-06-16T01:00:00.000Z","type":"session_meta","payload":{"id":"` + id + `","cwd":"/tmp/codex-current"}}`,
		`{"timestamp":"2026-06-16T01:00:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello new codex"}]}}`,
		`{"timestamp":"2026-06-16T01:00:02.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi from assistant"}]}}`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-06-16T01-00-00-"+id+".jsonl"), []byte(body+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	b := New(agentbackend.Config{Bin: "codex", WorkDir: t.TempDir()}, nil)
	sess, msgs, err := b.GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if sess.WorkingDir != "/tmp/codex-current" {
		t.Errorf("WorkingDir=%q", sess.WorkingDir)
	}
	if sess.MessageCount != 2 {
		t.Errorf("MessageCount=%d want 2", sess.MessageCount)
	}
	if sess.Preview != "hi from assistant" {
		t.Errorf("Preview=%q", sess.Preview)
	}
	if len(msgs) != 2 {
		t.Fatalf("len(msgs)=%d want 2", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Text != "hello new codex" {
		t.Errorf("msgs[0]=%+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Text != "hi from assistant" {
		t.Errorf("msgs[1]=%+v", msgs[1])
	}
}

func TestGetSession_ContinuesAfterOversizedCodexLine(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	dir := filepath.Join(home, ".codex", "sessions", "2026", "06", "17")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "11111111-2222-3333-4444-555555555555"
	body := strings.Repeat("{", 4*1024*1024+1) + "\n" +
		`{"timestamp":"2026-06-17T03:00:00.000Z","type":"session_meta","payload":{"id":"` + id + `","cwd":"/tmp/codex-huge-line"}}` + "\n" +
		`{"timestamp":"2026-06-17T03:00:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"still parsed after huge line"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-06-17T03-00-00-"+id+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	b := New(agentbackend.Config{Bin: "codex", WorkDir: t.TempDir()}, nil)
	sess, msgs, err := b.GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if sess.WorkingDir != "/tmp/codex-huge-line" {
		t.Fatalf("WorkingDir=%q want /tmp/codex-huge-line", sess.WorkingDir)
	}
	if sess.Title != "still parsed after huge line" {
		t.Fatalf("Title=%q want later valid line", sess.Title)
	}
	if len(msgs) != 1 || msgs[0].Text != "still parsed after huge line" {
		t.Fatalf("msgs=%+v want later valid line", msgs)
	}
}

func TestGetSession_CodexMetaSourceStringPreservesWorkingDir(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	dir := filepath.Join(home, ".codex", "sessions", "2026", "06", "17")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "cccccccc-1111-2222-3333-dddddddddddd"
	body := strings.Join([]string{
		`{"timestamp":"2026-06-17T03:00:00.000Z","type":"session_meta","payload":{"id":"` + id + `","cwd":"/tmp/codex-cli-source","source":"cli","thread_source":"user"}}`,
		`{"timestamp":"2026-06-17T03:00:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"open existing session"}]}}`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-06-17T03-00-00-"+id+".jsonl"), []byte(body+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	b := New(agentbackend.Config{Bin: "codex", WorkDir: t.TempDir()}, nil)
	listed, err := b.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("len(ListSessions)=%d want 1", len(listed))
	}
	if listed[0].WorkingDir != "/tmp/codex-cli-source" {
		t.Fatalf("ListSessions WorkingDir=%q want /tmp/codex-cli-source", listed[0].WorkingDir)
	}

	sess, _, err := b.GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if sess.WorkingDir != "/tmp/codex-cli-source" {
		t.Fatalf("GetSession WorkingDir=%q want /tmp/codex-cli-source", sess.WorkingDir)
	}
}

func TestGetSession_CodexExecManifestIsAgentTaskAndTitleSkipsManifest(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	dir := filepath.Join(home, ".codex", "sessions", "2026", "06", "17")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "abababab-1111-2222-3333-cdcdcdcdcdcd"
	body := strings.Join([]string{
		`{"timestamp":"2026-06-17T04:00:00.000Z","type":"session_meta","payload":{"id":"` + id + `","cwd":"/tmp/codex-agent-task","originator":"codex_exec","source":"exec","thread_source":"user"}}`,
		`{"timestamp":"2026-06-17T04:00:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<USER_FILES_MANIFEST version=1>\n{\"files\":[],\"writes\":[]}\n</USER_FILES_MANIFEST>\n\nack\n\n=== CAPABILITY ===\ne2e marker"}]}}`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-06-17T04-00-00-"+id+".jsonl"), []byte(body+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	b := New(agentbackend.Config{Bin: "codex", WorkDir: t.TempDir()}, nil)
	listed, err := b.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("len(ListSessions)=%d want 1", len(listed))
	}
	if listed[0].Origin != agentbackend.SessionOriginAgentTask {
		t.Fatalf("ListSessions Origin=%q want agent_task", listed[0].Origin)
	}
	if listed[0].Title != "ack === CAPABILITY === e2e marker" {
		t.Fatalf("ListSessions Title=%q", listed[0].Title)
	}

	sess, _, err := b.GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Origin != agentbackend.SessionOriginAgentTask {
		t.Fatalf("GetSession Origin=%q want agent_task", sess.Origin)
	}
	if sess.Title != "ack === CAPABILITY === e2e marker" {
		t.Fatalf("GetSession Title=%q", sess.Title)
	}
}

func TestGetSession_SkipsEnvironmentContextForTitle(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	dir := filepath.Join(home, ".codex", "sessions", "2026", "06", "17")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "eeeeeeee-1111-2222-3333-ffffffffffff"
	body := strings.Join([]string{
		`{"timestamp":"2026-06-17T01:00:00.000Z","type":"session_meta","payload":{"id":"` + id + `","cwd":"/tmp/codex-env-title"}}`,
		`{"timestamp":"2026-06-17T01:00:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <cwd>/tmp/codex-env-title</cwd>\n  <shell>zsh</shell>\n</environment_context>"}]}}`,
		`{"timestamp":"2026-06-17T01:00:02.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"实现 commander session 标题优化"}]}}`,
		`{"timestamp":"2026-06-17T01:00:03.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"我会调整标题逻辑。"}]}}`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-06-17T01-00-00-"+id+".jsonl"), []byte(body+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	b := New(agentbackend.Config{Bin: "codex", WorkDir: t.TempDir()}, nil)
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
	if len(msgs) != 3 {
		t.Fatalf("len(msgs)=%d want 3", len(msgs))
	}
}

func TestGetSession_CodexSubagentMetadata(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	dir := filepath.Join(home, ".codex", "sessions", "2026", "06", "17")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	parentID := "aaaaaaaa-1111-2222-3333-bbbbbbbbbbbb"
	id := "ffffffff-1111-2222-3333-eeeeeeeeeeee"
	body := strings.Join([]string{
		`{"timestamp":"2026-06-17T02:00:00.000Z","type":"session_meta","payload":{"id":"` + id + `","parent_thread_id":"` + parentID + `","cwd":"/tmp/codex-subagent","thread_source":"subagent","agent_nickname":"Lovelace","agent_role":"reviewer","source":{"subagent":{"thread_spawn":{"parent_thread_id":"` + parentID + `","depth":1,"agent_nickname":"Lovelace","agent_role":"reviewer"}}}}}`,
		`{"timestamp":"2026-06-17T02:00:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"检查实现质量"}]}}`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-06-17T02-00-00-"+id+".jsonl"), []byte(body+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	b := New(agentbackend.Config{Bin: "codex", WorkDir: t.TempDir()}, nil)
	listed, err := b.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("len(ListSessions)=%d want 1", len(listed))
	}
	if listed[0].Origin != agentbackend.SessionOriginSubagent {
		t.Fatalf("Origin=%q want subagent", listed[0].Origin)
	}
	if listed[0].ParentID != parentID || listed[0].AgentName != "Lovelace" || listed[0].AgentRole != "reviewer" {
		t.Fatalf("subagent metadata mismatch: %+v", listed[0])
	}

	sess, _, err := b.GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Origin != agentbackend.SessionOriginSubagent || sess.ParentID != parentID {
		t.Fatalf("GetSession subagent metadata mismatch: %+v", sess)
	}
}

func TestGetSession_UnknownIDReturnsErrSessionNotFound(t *testing.T) {
	home := copyFixtureToHOME(t)
	setTestHome(t, home)

	b := New(agentbackend.Config{Bin: "codex", WorkDir: t.TempDir()}, nil)
	_, _, err := b.GetSession(context.Background(), "no-such-id")
	if !errors.Is(err, agentbackend.ErrSessionNotFound) {
		t.Fatalf("err=%v want ErrSessionNotFound", err)
	}
}

func TestGetSession_RespectsPreviewCap(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
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
