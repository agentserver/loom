// Package opencode reads opencode session storage directly.
//
// Storage captured on this host on 2026-06-15:
//
//	Unix:    $HOME/.local/share/opencode/opencode.db
//	Windows: %APPDATA%\opencode\opencode.db
//
// Relevant sqlite tables:
//
//	session         - id, directory, title, version, time_created, time_updated
//	message         - current chat source of truth; data contains role JSON
//	session_message - older/fallback schema and switch-event rows in current DBs
//	part            - id, message_id, session_id, time_created, data
//
// The reader opens the database read-only and never spawns opencode.
package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func sessionsDBPath() string {
	home, _ := os.UserHomeDir()
	return sessionsDBPathFor(runtime.GOOS, os.Getenv, home)
}

func sessionsDBPathFor(goos string, getenv func(string) string, home string) string {
	if goos == "windows" {
		if appData := getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "opencode", "opencode.db")
		}
		if home != "" {
			return filepath.Join(home, "AppData", "Roaming", "opencode", "opencode.db")
		}
		return ""
	}

	if xdgData := getenv("XDG_DATA_HOME"); xdgData != "" {
		return filepath.Join(xdgData, "opencode", "opencode.db")
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "share", "opencode", "opencode.db")
}

func openDB() (*sql.DB, error) {
	p := sessionsDBPath()
	if p == "" {
		return nil, nil
	}
	if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: p}
	q := u.Query()
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout=5000")
	u.RawQuery = q.Encode()
	return sql.Open("sqlite", u.String())
}

func (b *Backend) ListSessions(ctx context.Context) ([]agentbackend.Session, error) {
	db, err := openDB()
	if err != nil {
		return nil, err
	}
	if db == nil {
		return nil, nil
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `SELECT id, directory, time_created, time_updated FROM session`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type sessionRow struct {
		id      string
		dir     string
		created int64
		updated int64
	}
	var recs []sessionRow
	var ids []string
	for rows.Next() {
		var r sessionRow
		if err := rows.Scan(&r.id, &r.dir, &r.created, &r.updated); err != nil {
			continue
		}
		recs = append(recs, r)
		ids = append(ids, r.id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	counts, lastAssistant, firstUser := loadAggregates(ctx, db, ids)
	out := make([]agentbackend.Session, 0, len(recs))
	for _, r := range recs {
		s := agentbackend.Session{
			ID:           r.id,
			Kind:         agentbackend.KindOpencode,
			WorkingDir:   r.dir,
			Origin:       agentbackend.SessionOriginUser,
			StartedAt:    msToTime(r.created),
			UpdatedAt:    msToTime(r.updated),
			MessageCount: counts[r.id],
		}
		if title := firstUser[r.id]; title != "" {
			s.Title = title
		}
		if text := lastAssistant[r.id]; text != "" {
			s.Preview = truncatePreview(text)
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (b *Backend) GetSession(ctx context.Context, id string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
	db, err := openDB()
	if err != nil {
		return agentbackend.Session{}, nil, err
	}
	if db == nil {
		return agentbackend.Session{}, nil, agentbackend.ErrSessionNotFound
	}
	defer db.Close()

	var dir string
	var created, updated int64
	err = db.QueryRowContext(ctx,
		`SELECT directory, time_created, time_updated FROM session WHERE id = ?`, id).
		Scan(&dir, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return agentbackend.Session{}, nil, agentbackend.ErrSessionNotFound
	}
	if err != nil {
		return agentbackend.Session{}, nil, err
	}

	msgs, found, err := loadMessagesFromMessageTable(ctx, db, id)
	if err != nil {
		return agentbackend.Session{}, nil, err
	}
	if !found {
		msgs, err = loadMessagesFromSessionMessage(ctx, db, id)
		if err != nil {
			return agentbackend.Session{}, nil, err
		}
	}

	sess := agentbackend.Session{
		ID:           id,
		Kind:         agentbackend.KindOpencode,
		WorkingDir:   dir,
		Origin:       agentbackend.SessionOriginUser,
		StartedAt:    msToTime(created),
		UpdatedAt:    msToTime(updated),
		MessageCount: len(msgs),
	}
	sess.Title = firstUserTitle(msgs)
	if last := lastAssistantText(msgs); last != "" {
		sess.Preview = truncatePreview(last)
	}
	return sess, msgs, nil
}

func (b *Backend) sessionWorkingDir(ctx context.Context, id string) (string, bool, error) {
	db, err := openDB()
	if err != nil {
		return "", false, err
	}
	if db == nil {
		return "", false, nil
	}
	defer db.Close()

	var dir string
	err = db.QueryRowContext(ctx, `SELECT directory FROM session WHERE id = ?`, id).Scan(&dir)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return dir, true, nil
}

type messageAccumulator struct {
	id    string
	role  string
	ts    time.Time
	parts []string
}

func (m *messageAccumulator) addText(text string) {
	if text != "" {
		m.parts = append(m.parts, text)
	}
}

func (m messageAccumulator) message() (agentbackend.SessionMessage, bool) {
	text := strings.Join(m.parts, "")
	if m.id == "" || text == "" {
		return agentbackend.SessionMessage{}, false
	}
	return agentbackend.SessionMessage{Role: m.role, Text: text, Ts: m.ts}, true
}

func loadAggregates(ctx context.Context, db *sql.DB, ids []string) (map[string]int, map[string]string, map[string]string) {
	counts := map[string]int{}
	lastAssistant := map[string]string{}
	firstUser := map[string]string{}
	if len(ids) == 0 {
		return counts, lastAssistant, firstUser
	}

	seenInMessageTable := map[string]bool{}
	if tableExists(ctx, db, "message") {
		// Current opencode DBs store chat turns in message+part. The
		// session_message table may still exist, but it records switch
		// events there; sessions seen in message suppress fallback reads.
		msgCounts, msgLast, msgFirst, seen := loadMessageTableAggregates(ctx, db)
		for sid, n := range msgCounts {
			counts[sid] = n
		}
		for sid, text := range msgLast {
			lastAssistant[sid] = text
		}
		for sid, text := range msgFirst {
			firstUser[sid] = text
		}
		seenInMessageTable = seen
	}
	loadSessionMessageAggregates(ctx, db, counts, lastAssistant, firstUser, seenInMessageTable)
	return counts, lastAssistant, firstUser
}

func loadMessageTableAggregates(ctx context.Context, db *sql.DB) (map[string]int, map[string]string, map[string]string, map[string]bool) {
	counts := map[string]int{}
	lastAssistant := map[string]string{}
	firstUser := map[string]string{}
	seen := map[string]bool{}

	rows, err := db.QueryContext(ctx, `
		SELECT m.session_id, m.id, m.data, m.time_created, p.data
		FROM message m
		LEFT JOIN part p ON p.message_id = m.id
		ORDER BY m.session_id, m.time_created, p.time_created`)
	if err != nil {
		return counts, lastAssistant, firstUser, seen
	}
	defer rows.Close()

	var currentSession string
	var current messageAccumulator
	flush := func() {
		if currentSession == "" || current.id == "" {
			return
		}
		seen[currentSession] = true
		msg, ok := current.message()
		if !ok {
			return
		}
		counts[currentSession]++
		if msg.Role == "assistant" {
			lastAssistant[currentSession] = msg.Text
		}
		if msg.Role == "user" && firstUser[currentSession] == "" {
			if title := titleFromUserText(msg.Text); title != "" {
				firstUser[currentSession] = title
			}
		}
	}
	for rows.Next() {
		var sessionID, msgID, msgData string
		var partData sql.NullString
		var createdMS int64
		if err := rows.Scan(&sessionID, &msgID, &msgData, &createdMS, &partData); err != nil {
			continue
		}
		if current.id != "" && (current.id != msgID || currentSession != sessionID) {
			flush()
			current = messageAccumulator{}
		}
		if current.id == "" {
			currentSession = sessionID
			current = messageAccumulator{id: msgID, role: normalizeRole(messageDataRole(msgData)), ts: msToTime(createdMS)}
		}
		if partData.Valid {
			current.addText(extractPartText(partData.String))
		}
	}
	flush()
	return counts, lastAssistant, firstUser, seen
}

func loadSessionMessageAggregates(ctx context.Context, db *sql.DB, counts map[string]int, lastAssistant map[string]string, firstUser map[string]string, skip map[string]bool) {
	// Fallback for older/pre-flight schemas where session_message carried
	// user/assistant turns. Current opencode DBs usually use message+part.
	rows, err := db.QueryContext(ctx, `
		SELECT session_id, COUNT(*)
		FROM session_message
		GROUP BY session_id`)
	if err == nil {
		for rows.Next() {
			var sid string
			var n int
			if err := rows.Scan(&sid, &n); err == nil {
				if skip[sid] {
					continue
				}
				counts[sid] = n
			}
		}
		rows.Close()
	}

	rows, err = db.QueryContext(ctx, `
		SELECT m.session_id, m.id, m.type, m.time_created, p.data
		FROM session_message m
		LEFT JOIN part p ON p.message_id = m.id
		WHERE m.type IN ('user', 'assistant')
		ORDER BY m.session_id, m.seq, p.time_created`)
	if err != nil {
		return
	}
	defer rows.Close()

	var currentSession string
	var current messageAccumulator
	flush := func() {
		if currentSession == "" || current.id == "" {
			return
		}
		msg, ok := current.message()
		if !ok {
			return
		}
		switch msg.Role {
		case "user":
			if firstUser[currentSession] == "" {
				if title := titleFromUserText(msg.Text); title != "" {
					firstUser[currentSession] = title
				}
			}
		case "assistant":
			lastAssistant[currentSession] = msg.Text
		}
	}
	for rows.Next() {
		var sid, msgID, role string
		var partData sql.NullString
		var createdMS int64
		if err := rows.Scan(&sid, &msgID, &role, &createdMS, &partData); err != nil {
			continue
		}
		if current.id != "" && (current.id != msgID || currentSession != sid) {
			flush()
			current = messageAccumulator{}
		}
		if skip[sid] {
			continue
		}
		if current.id == "" {
			currentSession = sid
			current = messageAccumulator{id: msgID, role: normalizeRole(role), ts: msToTime(createdMS)}
		}
		if partData.Valid {
			current.addText(extractPartText(partData.String))
		}
	}
	flush()
}

func loadMessagesFromMessageTable(ctx context.Context, db *sql.DB, id string) ([]agentbackend.SessionMessage, bool, error) {
	if !tableExists(ctx, db, "message") {
		return nil, false, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT m.id, m.data, m.time_created, p.data
		FROM message m
		LEFT JOIN part p ON p.message_id = m.id
		WHERE m.session_id = ?
		ORDER BY m.time_created, p.time_created`, id)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	var msgs []agentbackend.SessionMessage
	var current messageAccumulator
	var foundRows bool
	flush := func() {
		msg, ok := current.message()
		if ok {
			msgs = append(msgs, msg)
		}
	}
	for rows.Next() {
		var msgID, msgData string
		var partData sql.NullString
		var createdMS int64
		if err := rows.Scan(&msgID, &msgData, &createdMS, &partData); err != nil {
			continue
		}
		foundRows = true
		if current.id != "" && current.id != msgID {
			flush()
			current = messageAccumulator{}
		}
		if current.id == "" {
			current = messageAccumulator{id: msgID, role: normalizeRole(messageDataRole(msgData)), ts: msToTime(createdMS)}
		}
		if partData.Valid {
			current.addText(extractPartText(partData.String))
		}
	}
	flush()
	if err := rows.Err(); err != nil {
		return nil, foundRows, err
	}
	return msgs, foundRows, nil
}

func loadMessagesFromSessionMessage(ctx context.Context, db *sql.DB, id string) ([]agentbackend.SessionMessage, error) {
	// Fallback for older/pre-flight schemas where session_message carried
	// user/assistant turns. Current opencode DBs usually use message+part.
	rows, err := db.QueryContext(ctx, `
		SELECT m.id, m.type, p.data, m.time_created
		FROM session_message m
		LEFT JOIN part p ON p.message_id = m.id
		WHERE m.session_id = ?
		ORDER BY m.seq, p.time_created`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []agentbackend.SessionMessage
	var current messageAccumulator
	flush := func() {
		msg, ok := current.message()
		if ok {
			msgs = append(msgs, msg)
		}
	}
	for rows.Next() {
		var msgID, role string
		var partData sql.NullString
		var createdMS int64
		if err := rows.Scan(&msgID, &role, &partData, &createdMS); err != nil {
			continue
		}
		if current.id != "" && current.id != msgID {
			flush()
			current = messageAccumulator{}
		}
		if current.id == "" {
			current = messageAccumulator{id: msgID, role: normalizeRole(role), ts: msToTime(createdMS)}
		}
		if partData.Valid {
			current.addText(extractPartText(partData.String))
		}
	}
	flush()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return msgs, nil
}

func tableExists(ctx context.Context, db *sql.DB, name string) bool {
	var got string
	err := db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name).
		Scan(&got)
	return err == nil && got == name
}

func messageDataRole(data string) string {
	var m struct {
		Role string `json:"role"`
	}
	if json.Unmarshal([]byte(data), &m) != nil {
		return ""
	}
	return m.Role
}

func lastAssistantText(msgs []agentbackend.SessionMessage) string {
	var last string
	for _, msg := range msgs {
		if msg.Role == "assistant" {
			last = msg.Text
		}
	}
	return last
}

func firstUserTitle(msgs []agentbackend.SessionMessage) string {
	for _, msg := range msgs {
		if msg.Role == "user" {
			if title := titleFromUserText(msg.Text); title != "" {
				return title
			}
		}
	}
	return ""
}

func extractPartText(data string) string {
	var p struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(data), &p) != nil {
		return ""
	}
	if p.Type != "text" {
		return ""
	}
	return p.Text
}

func normalizeRole(t string) string {
	switch t {
	case "user", "assistant", "system", "tool":
		return t
	default:
		return t
	}
}

func msToTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
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
