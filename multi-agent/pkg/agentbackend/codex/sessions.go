// Package codex reads codex session rollout storage directly.
//
// Storage layout captured on this host on 2026-06-15:
//
//	$HOME/.codex/sessions/<yyyy>/<mm>/<dd>/rollout-<iso>-<thread-uuid>.jsonl
//
// The trailing uuid in the rollout filename is the session id. The
// first line is usually a session_meta record carrying id and cwd; later
// lines include user_input, model_output, tool_call, and tool_result
// events. This reader exposes only user and assistant text turns and
// never spawns codex.
package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
	sessionjsonl "github.com/yourorg/multi-agent/pkg/agentbackend/internal/jsonl"
)

var filenameUUIDRe = regexp.MustCompile(`-([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\.jsonl$`)

func (b *Backend) sessionsRoot() string {
	base := b.EffectiveCodexHome()
	if base == "" {
		return ""
	}
	return filepath.Join(base, "sessions")
}

func sessionIDFromFilename(name string) string {
	m := filenameUUIDRe.FindStringSubmatch(name)
	if len(m) != 2 {
		return ""
	}
	return m[1]
}

func (b *Backend) ListSessions(ctx context.Context) ([]agentbackend.Session, error) {
	root := b.sessionsRoot()
	if root == "" {
		return nil, nil
	}
	base := b.EffectiveCodexHome()
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		reaper(base, nil)
		b.list.Prune(nil)
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	var out []agentbackend.Session
	seen := map[string]struct{}{}
	liveThreadIDs := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		id := sessionIDFromFilename(entry.Name())
		if id == "" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		sidecarMtime := ""
		if sidecarInfo, err := os.Stat(loomMetaPath(base, id)); err == nil {
			sidecarMtime = sidecarInfo.ModTime().Format(time.RFC3339Nano)
		}
		cacheKey := path + "|" + sidecarMtime
		seen[cacheKey] = struct{}{}
		liveThreadIDs = append(liveThreadIDs, id)
		session := b.list.Get(cacheKey, info, func() agentbackend.Session {
			return scanCodexSession(path, id, false, base).session
		})
		out = append(out, session)
		return nil
	})
	if err != nil {
		return nil, err
	}
	reaper(base, liveThreadIDs)
	b.list.Prune(seen)
	return out, nil
}

func (b *Backend) GetSession(ctx context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
	root := b.sessionsRoot()
	if root == "" {
		return agentbackend.Session{}, nil, agentbackend.ErrSessionNotFound
	}
	base := b.EffectiveCodexHome()
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return agentbackend.Session{}, nil, agentbackend.ErrSessionNotFound
	} else if err != nil {
		return agentbackend.Session{}, nil, err
	}

	var found string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		if sessionIDFromFilename(entry.Name()) == id {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return agentbackend.Session{}, nil, err
	}
	if found == "" {
		return agentbackend.Session{}, nil, agentbackend.ErrSessionNotFound
	}
	res := scanCodexSession(found, id, true, base)
	return res.session, res.messages, nil
}

func (b *Backend) sessionWorkingDir(ctx context.Context, id string) (string, bool, error) {
	root := b.sessionsRoot()
	if root == "" {
		return "", false, nil
	}
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	} else if err != nil {
		return "", false, err
	}

	var found string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		if sessionIDFromFilename(entry.Name()) == id {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", false, err
	}
	if found == "" {
		return "", false, nil
	}
	return scanCodexSessionWorkingDir(found), true, nil
}

type codexScanResult struct {
	session  agentbackend.Session
	messages []agentbackend.SessionMessage
}

type codexLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexMetaPayload struct {
	ID             string          `json:"id"`
	Cwd            string          `json:"cwd"`
	Originator     string          `json:"originator"`
	ParentThreadID string          `json:"parent_thread_id"`
	ThreadSource   string          `json:"thread_source"`
	AgentNickname  string          `json:"agent_nickname"`
	AgentRole      string          `json:"agent_role"`
	Source         codexMetaSource `json:"source"`
}

type codexMetaSource struct {
	Kind     string
	Subagent codexMetaSubagent `json:"subagent"`
}

func (s *codexMetaSource) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return nil
	}
	if data[0] == '"' {
		return json.Unmarshal(data, &s.Kind)
	}
	type alias codexMetaSource
	return json.Unmarshal(data, (*alias)(s))
}

type codexMetaSubagent struct {
	ThreadSpawn codexThreadSpawn `json:"thread_spawn"`
}

type codexThreadSpawn struct {
	ParentThreadID string `json:"parent_thread_id"`
	AgentNickname  string `json:"agent_nickname"`
	AgentRole      string `json:"agent_role"`
}

type codexTextPayload struct {
	Text string `json:"text"`
}

type codexResponseItemPayload struct {
	Type    string                     `json:"type"`
	Role    string                     `json:"role"`
	Content []codexResponseItemContent `json:"content"`
}

type codexResponseItemContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func scanCodexSession(path, fallbackID string, withMessages bool, base string) codexScanResult {
	res := codexScanResult{session: agentbackend.Session{
		ID:     fallbackID,
		Kind:   agentbackend.KindCodex,
		Origin: agentbackend.SessionOriginUser,
	}}

	f, err := os.Open(path)
	if err != nil {
		return res
	}
	defer f.Close()

	var lastAssistantText string
	rd := bufio.NewReader(f)
	for {
		line, readErr := sessionjsonl.ReadLine(rd, sessionjsonl.MaxLineBytes)
		if len(line) > 0 {
			var ln codexLine
			if err := json.Unmarshal(line, &ln); err != nil {
				continue
			}
			ts := parseTimestamp(ln.Timestamp)
			switch ln.Type {
			case "session_meta":
				var p codexMetaPayload
				if err := json.Unmarshal(ln.Payload, &p); err != nil {
					continue
				}
				if p.ID != "" {
					res.session.ID = p.ID
				}
				if p.Cwd != "" {
					res.session.WorkingDir = p.Cwd
				}
				applyCodexSessionMeta(&res.session, p)
				if res.session.StartedAt.IsZero() && !ts.IsZero() {
					res.session.StartedAt = ts
				}
			case "user_input":
				text := codexPayloadText(ln.Payload)
				if text == "" {
					continue
				}
				if res.session.Title == "" {
					res.session.Title = titleFromUserText(text)
				}
				res.addMessage("user", text, ts, withMessages)
			case "model_output":
				text := codexPayloadText(ln.Payload)
				if text == "" {
					continue
				}
				res.addMessage("assistant", text, ts, withMessages)
				lastAssistantText = text
			case "response_item":
				role, text, ok := codexResponseItemMessage(ln.Payload)
				if !ok {
					continue
				}
				if role == "user" && res.session.Title == "" {
					res.session.Title = titleFromUserText(text)
				}
				res.addMessage(role, text, ts, withMessages)
				if role == "assistant" {
					lastAssistantText = text
				}
			}
		}
		if readErr != nil {
			break
		}
	}
	if lastAssistantText != "" {
		res.session.Preview = truncatePreview(lastAssistantText)
	}
	applyLoomMeta(base, &res.session)
	return res
}

func applyCodexSessionMeta(sess *agentbackend.Session, p codexMetaPayload) {
	spawn := p.Source.Subagent.ThreadSpawn
	parentID := firstNonEmpty(p.ParentThreadID, spawn.ParentThreadID)
	if p.ThreadSource == "subagent" || parentID != "" || p.AgentNickname != "" || spawn.AgentNickname != "" {
		sess.Origin = agentbackend.SessionOriginSubagent
		sess.ParentID = parentID
		sess.AgentName = firstNonEmpty(p.AgentNickname, spawn.AgentNickname)
		sess.AgentRole = firstNonEmpty(p.AgentRole, spawn.AgentRole)
		return
	}
	if p.Originator == "codex_exec" || p.Source.Kind == "exec" {
		sess.Origin = agentbackend.SessionOriginAgentTask
		return
	}
	if sess.Origin == "" {
		sess.Origin = agentbackend.SessionOriginUser
	}
}

func applyLoomMeta(base string, sess *agentbackend.Session) {
	if base == "" || sess == nil {
		return
	}
	m, ok := readLoomMeta(base, sess.ID)
	if !ok {
		return
	}
	if m.Schema != loomMetaSchema || m.Kind != "codex" || m.Origin != "agent_task" || m.SessionID != sess.ID {
		return
	}
	if sess.Origin != agentbackend.SessionOriginAgentTask || m.ParentSessionID == "" || sess.ParentID != "" {
		return
	}
	sess.ParentID = m.ParentSessionID
	sess.ParentAgentID = m.ParentAgentID
	sess.ParentDisplayName = m.ParentDisplayName
}

func scanCodexSessionWorkingDir(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	rd := bufio.NewReader(f)
	for {
		line, readErr := sessionjsonl.ReadLine(rd, sessionjsonl.MaxLineBytes)
		if len(line) == 0 && readErr != nil {
			break
		}
		var ln codexLine
		if len(line) > 0 && json.Unmarshal(line, &ln) == nil && ln.Type == "session_meta" {
			var p codexMetaPayload
			if err := json.Unmarshal(ln.Payload, &p); err != nil {
				return ""
			}
			return p.Cwd
		}
		if readErr != nil {
			break
		}
	}
	return ""
}

func (r *codexScanResult) addMessage(role, text string, ts time.Time, withMessages bool) {
	if r.session.StartedAt.IsZero() && !ts.IsZero() {
		r.session.StartedAt = ts
	}
	if !ts.IsZero() {
		r.session.UpdatedAt = ts
	}
	r.session.MessageCount++
	if withMessages {
		r.messages = append(r.messages, agentbackend.SessionMessage{
			Role: role,
			Text: text,
			Ts:   ts,
		})
	}
}

func codexPayloadText(raw json.RawMessage) string {
	var p codexTextPayload
	if err := json.Unmarshal(raw, &p); err == nil && p.Text != "" {
		return p.Text
	}
	return ""
}

func codexResponseItemMessage(raw json.RawMessage) (string, string, bool) {
	var p codexResponseItemPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", "", false
	}
	if p.Type != "message" {
		return "", "", false
	}
	if p.Role != "user" && p.Role != "assistant" {
		return "", "", false
	}
	parts := make([]string, 0, len(p.Content))
	for _, c := range p.Content {
		if c.Text == "" {
			continue
		}
		parts = append(parts, c.Text)
	}
	if len(parts) == 0 {
		return "", "", false
	}
	return p.Role, strings.Join(parts, "\n"), true
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
	s = stripCodexInjectedUserPrefix(s)
	if s == "" {
		return ""
	}
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if s == "" {
		return ""
	}
	if len(s) <= agentbackend.SessionPreviewMaxBytes {
		return s
	}
	return truncatePreview(s)
}

func stripCodexInjectedUserPrefix(s string) string {
	s = strings.TrimSpace(s)
	for {
		switch {
		case strings.HasPrefix(s, "<environment_context>"):
			next, ok := stripThroughEndTag(s, "</environment_context>")
			if !ok {
				return s
			}
			s = next
		case strings.HasPrefix(s, "<USER_FILES_MANIFEST"):
			next, ok := stripThroughEndTag(s, "</USER_FILES_MANIFEST>")
			if !ok {
				return s
			}
			s = next
		default:
			return s
		}
	}
}

func stripThroughEndTag(s, endTag string) (string, bool) {
	end := strings.Index(s, endTag)
	if end < 0 {
		return "", false
	}
	return strings.TrimSpace(s[end+len(endTag):]), true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
