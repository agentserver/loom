// Package opencode reads opencode session storage directly.
//
// Storage captured on this host on 2026-06-15:
//
//	$HOME/.local/share/opencode/opencode.db
//
// Relevant sqlite tables:
//
//	session         — id, directory, title, version, time_created, time_updated
//	session_message — id, session_id, type, seq, time_created, time_updated, data
//	part            — id, message_id, session_id, time_created, data
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
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

const sessionPreviewMaxBytes = 256

func sessionsDBPath() string {
	if xdgData := os.Getenv("XDG_DATA_HOME"); xdgData != "" {
		return filepath.Join(xdgData, "opencode", "opencode.db")
	}
	home, _ := os.UserHomeDir()
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

	counts, lastAssistant := loadAggregates(ctx, db, ids)
	out := make([]agentbackend.Session, 0, len(recs))
	for _, r := range recs {
		s := agentbackend.Session{
			ID:           r.id,
			Kind:         agentbackend.KindOpencode,
			WorkingDir:   r.dir,
			StartedAt:    msToTime(r.created),
			UpdatedAt:    msToTime(r.updated),
			MessageCount: counts[r.id],
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

	rows, err := db.QueryContext(ctx, `
		SELECT m.id, m.type, p.data, m.time_created
		FROM session_message m
		LEFT JOIN part p ON p.message_id = m.id
		WHERE m.session_id = ?
		ORDER BY m.seq, p.time_created`, id)
	if err != nil {
		return agentbackend.Session{}, nil, err
	}
	defer rows.Close()

	var msgs []agentbackend.SessionMessage
	var current messageAccumulator
	var lastAssistant string
	flush := func() {
		msg, ok := current.message()
		if !ok {
			return
		}
		msgs = append(msgs, msg)
		if msg.Role == "assistant" {
			lastAssistant = msg.Text
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
		return agentbackend.Session{}, nil, err
	}

	sess := agentbackend.Session{
		ID:           id,
		Kind:         agentbackend.KindOpencode,
		WorkingDir:   dir,
		StartedAt:    msToTime(created),
		UpdatedAt:    msToTime(updated),
		MessageCount: len(msgs),
	}
	if lastAssistant != "" {
		sess.Preview = truncatePreview(lastAssistant)
	}
	return sess, msgs, nil
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

func loadAggregates(ctx context.Context, db *sql.DB, ids []string) (map[string]int, map[string]string) {
	counts := map[string]int{}
	lastAssistant := map[string]string{}
	if len(ids) == 0 {
		return counts, lastAssistant
	}

	rows, err := db.QueryContext(ctx, `
		SELECT session_id, COUNT(*)
		FROM session_message
		GROUP BY session_id`)
	if err == nil {
		for rows.Next() {
			var sid string
			var n int
			if err := rows.Scan(&sid, &n); err == nil {
				counts[sid] = n
			}
		}
		rows.Close()
	}

	rows, err = db.QueryContext(ctx, `
		SELECT m.session_id, p.data
		FROM session_message m
		JOIN part p ON p.message_id = m.id
		WHERE m.type = 'assistant'
		ORDER BY m.session_id, m.seq, p.time_created`)
	if err != nil {
		return counts, lastAssistant
	}
	defer rows.Close()
	for rows.Next() {
		var sid, data string
		if err := rows.Scan(&sid, &data); err != nil {
			continue
		}
		if text := extractPartText(data); text != "" {
			lastAssistant[sid] = text
		}
	}
	return counts, lastAssistant
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
	if len(s) <= sessionPreviewMaxBytes {
		return s
	}
	end := sessionPreviewMaxBytes
	for end > 0 && !utf8.ValidString(s[:end]) {
		end--
	}
	return s[:end]
}
