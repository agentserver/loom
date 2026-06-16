// Package claude reads claude code session storage directly.
//
// Storage layout captured on this host on 2026-06-15:
//
//	$HOME/.claude/projects/<encoded_cwd>/<session_uuid>.jsonl
//
// <encoded_cwd> is the absolute cwd with each "/" replaced by "-".
// Example: /root/multi-agent -> -root-multi-agent.
//
// Each .jsonl file is one session. Relevant line shapes:
//
//	{"type":"user","timestamp":"...","message":{"role":"user","content":"..."}}
//	{"type":"assistant","timestamp":"...","message":{"role":"assistant","content":[{"type":"text","text":"..."}]}}
//
// Non-message events are ignored. The reader never spawns claude.
package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func sessionsRoot() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

func decodeCwd(encoded string) string {
	return "/" + strings.ReplaceAll(strings.TrimPrefix(encoded, "-"), "-", "/")
}

func (b *Backend) ListSessions(ctx context.Context) ([]agentbackend.Session, error) {
	root := sessionsRoot()
	if root == "" {
		return nil, nil
	}
	projectDirs, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var out []agentbackend.Session
	seen := map[string]struct{}{}
	for _, projectDir := range projectDirs {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if !projectDir.IsDir() {
			continue
		}
		dirPath := filepath.Join(root, projectDir.Name())
		files, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}
		cwd := decodeCwd(projectDir.Name())
		for _, f := range files {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			id := strings.TrimSuffix(f.Name(), ".jsonl")
			path := filepath.Join(dirPath, f.Name())
			info, err := f.Info()
			if err != nil {
				continue
			}
			seen[path] = struct{}{}
			out = append(out, b.list.Get(path, info, func() agentbackend.Session {
				return scanSession(path, id, cwd)
			}))
		}
	}
	b.list.Prune(seen)
	return out, nil
}

func (b *Backend) GetSession(ctx context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
	root := sessionsRoot()
	if root == "" {
		return agentbackend.Session{}, nil, agentbackend.ErrSessionNotFound
	}
	projectDirs, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return agentbackend.Session{}, nil, agentbackend.ErrSessionNotFound
	}
	if err != nil {
		return agentbackend.Session{}, nil, err
	}

	for _, projectDir := range projectDirs {
		if err := ctx.Err(); err != nil {
			return agentbackend.Session{}, nil, err
		}
		if !projectDir.IsDir() {
			continue
		}
		path := filepath.Join(root, projectDir.Name(), id+".jsonl")
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return agentbackend.Session{}, nil, err
		}
		return loadSession(path, id, decodeCwd(projectDir.Name()))
	}
	return agentbackend.Session{}, nil, agentbackend.ErrSessionNotFound
}

func (b *Backend) sessionWorkingDir(ctx context.Context, id string) (string, bool, error) {
	root := sessionsRoot()
	if root == "" {
		return "", false, nil
	}
	projectDirs, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}

	for _, projectDir := range projectDirs {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		if !projectDir.IsDir() {
			continue
		}
		path := filepath.Join(root, projectDir.Name(), id+".jsonl")
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return "", false, err
		}
		return decodeCwd(projectDir.Name()), true, nil
	}
	return "", false, nil
}

type claudeJSONLLine struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	SessionID string          `json:"sessionId"`
	Message   *claudeJSONLMsg `json:"message"`
}

type claudeJSONLMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func scanSession(path, id, cwd string) agentbackend.Session {
	s, _ := loadSessionImpl(path, id, cwd, false)
	return s
}

func loadSession(path, id, cwd string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
	s, msgs := loadSessionImpl(path, id, cwd, true)
	return s, msgs, nil
}

func loadSessionImpl(path, id, cwd string, withMessages bool) (agentbackend.Session, []agentbackend.SessionMessage) {
	sess := agentbackend.Session{
		ID:         id,
		Kind:       agentbackend.KindClaude,
		WorkingDir: cwd,
	}
	f, err := os.Open(path)
	if err != nil {
		return sess, nil
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var msgs []agentbackend.SessionMessage
	var lastAssistantText string
	for sc.Scan() {
		var ln claudeJSONLLine
		if err := json.Unmarshal(sc.Bytes(), &ln); err != nil {
			continue
		}
		if ln.Message == nil {
			continue
		}
		text := extractText(ln.Message.Content)
		if text == "" {
			continue
		}
		ts := parseTimestamp(ln.Timestamp)
		if sess.StartedAt.IsZero() && !ts.IsZero() {
			sess.StartedAt = ts
		}
		if !ts.IsZero() {
			sess.UpdatedAt = ts
		}
		sess.MessageCount++
		if ln.Message.Role == "assistant" {
			lastAssistantText = text
		}
		if withMessages {
			msgs = append(msgs, agentbackend.SessionMessage{
				Role: ln.Message.Role,
				Text: text,
				Ts:   ts,
			})
		}
	}
	if lastAssistantText != "" {
		sess.Preview = truncatePreview(lastAssistantText)
	}
	return sess, msgs
}

func extractText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ""
	}
	var out strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			out.WriteString(p.Text)
		}
	}
	return out.String()
}

func parseTimestamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func truncatePreview(s string) string {
	if len(s) <= agentbackend.SessionPreviewMaxBytes {
		return s
	}
	end := agentbackend.SessionPreviewMaxBytes
	for end > 0 && !utf8.ValidString(s[:end]) {
		end--
	}
	return s[:end]
}
