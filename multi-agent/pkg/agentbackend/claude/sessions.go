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

func encodeCwd(cwd string) string {
	replacer := strings.NewReplacer(":", "-", "\\", "-", "/", "-")
	return replacer.Replace(cwd)
}

func decodeCwd(encoded string) string {
	if len(encoded) >= 3 && isASCIIAlpha(encoded[0]) && encoded[1] == '-' && encoded[2] == '-' {
		rest := strings.ReplaceAll(encoded[3:], "-", "/")
		return filepath.FromSlash(string(encoded[0]) + ":/" + rest)
	}
	return "/" + strings.ReplaceAll(strings.TrimPrefix(encoded, "-"), "-", "/")
}

func isASCIIAlpha(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
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
		cwd := decodeCwd(projectDir.Name())
		projectPath := filepath.Join(root, projectDir.Name())
		err = filepath.WalkDir(projectPath, func(path string, f fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				return nil
			}
			rel, err := filepath.Rel(projectPath, path)
			if err != nil {
				return nil
			}
			meta, ok := claudeSessionFileMeta(rel)
			if !ok {
				return nil
			}
			info, err := f.Info()
			if err != nil {
				return nil
			}
			seen[path] = struct{}{}
			out = append(out, b.list.Get(path, info, func() agentbackend.Session {
				return scanSession(path, meta, cwd)
			}))
			return nil
		})
		if err != nil {
			return out, err
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
		projectPath := filepath.Join(root, projectDir.Name())
		var foundPath string
		var foundMeta claudeSessionMeta
		err := filepath.WalkDir(projectPath, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
				return nil
			}
			rel, err := filepath.Rel(projectPath, path)
			if err != nil {
				return nil
			}
			meta, ok := claudeSessionFileMeta(rel)
			if !ok || meta.id != id {
				return nil
			}
			foundPath = path
			foundMeta = meta
			return filepath.SkipAll
		})
		if err != nil {
			return agentbackend.Session{}, nil, err
		}
		if foundPath != "" {
			return loadSession(foundPath, foundMeta, decodeCwd(projectDir.Name()))
		}
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
		projectPath := filepath.Join(root, projectDir.Name())
		found := false
		err := filepath.WalkDir(projectPath, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
				return nil
			}
			rel, err := filepath.Rel(projectPath, path)
			if err != nil {
				return nil
			}
			meta, ok := claudeSessionFileMeta(rel)
			if ok && meta.id == id {
				found = true
				return filepath.SkipAll
			}
			return nil
		})
		if err != nil {
			return "", false, err
		}
		if found {
			return decodeCwd(projectDir.Name()), true, nil
		}
	}
	return "", false, nil
}

type claudeJSONLLine struct {
	Type        string          `json:"type"`
	Timestamp   string          `json:"timestamp"`
	SessionID   string          `json:"sessionId"`
	IsMeta      bool            `json:"isMeta"`
	IsSidechain bool            `json:"isSidechain"`
	AgentID     string          `json:"agentId"`
	Message     *claudeJSONLMsg `json:"message"`
}

type claudeJSONLMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type claudeSessionMeta struct {
	id       string
	origin   agentbackend.SessionOrigin
	parentID string
}

func claudeSessionFileMeta(rel string) (claudeSessionMeta, bool) {
	rel = filepath.ToSlash(rel)
	if !strings.HasSuffix(rel, ".jsonl") {
		return claudeSessionMeta{}, false
	}
	base := strings.TrimSuffix(filepath.Base(rel), ".jsonl")
	parts := strings.Split(rel, "/")
	if len(parts) == 1 {
		return claudeSessionMeta{id: base, origin: agentbackend.SessionOriginUser}, true
	}
	if len(parts) == 3 && parts[1] == "subagents" && parts[0] != "" {
		return claudeSessionMeta{id: base, origin: agentbackend.SessionOriginSubagent, parentID: parts[0]}, true
	}
	return claudeSessionMeta{}, false
}

func scanSession(path string, meta claudeSessionMeta, cwd string) agentbackend.Session {
	s, _ := loadSessionImpl(path, meta, cwd, false)
	return s
}

func loadSession(path string, meta claudeSessionMeta, cwd string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
	s, msgs := loadSessionImpl(path, meta, cwd, true)
	return s, msgs, nil
}

func loadSessionImpl(path string, meta claudeSessionMeta, cwd string, withMessages bool) (agentbackend.Session, []agentbackend.SessionMessage) {
	origin := meta.origin
	if origin == "" {
		origin = agentbackend.SessionOriginUser
	}
	sess := agentbackend.Session{
		ID:         meta.id,
		Kind:       agentbackend.KindClaude,
		WorkingDir: cwd,
		Origin:     origin,
		ParentID:   meta.parentID,
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
		applyClaudeLineMeta(&sess, ln)
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
		if ln.Message.Role == "user" && sess.Title == "" {
			sess.Title = titleFromClaudeUserText(ln, text)
		}
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

func applyClaudeLineMeta(sess *agentbackend.Session, ln claudeJSONLLine) {
	if ln.IsSidechain || ln.AgentID != "" {
		sess.Origin = agentbackend.SessionOriginSubagent
		if sess.ParentID == "" {
			sess.ParentID = ln.SessionID
		}
		if sess.AgentName == "" {
			sess.AgentName = ln.AgentID
		}
	}
	if sess.Origin == "" {
		sess.Origin = agentbackend.SessionOriginUser
	}
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

func titleFromUserText(s string) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if s == "" {
		return ""
	}
	if len(s) <= agentbackend.SessionPreviewMaxBytes {
		return s
	}
	return truncatePreview(s)
}

func titleFromClaudeUserText(ln claudeJSONLLine, s string) string {
	if ln.IsMeta || isClaudeInjectedUserText(s) {
		return ""
	}
	return titleFromUserText(s)
}

func isClaudeInjectedUserText(s string) bool {
	s = strings.TrimSpace(s)
	for _, prefix := range []string{
		"<local-command-caveat>",
		"<local-command-stdout>",
		"<command-name>",
		"<system-reminder>",
	} {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}
