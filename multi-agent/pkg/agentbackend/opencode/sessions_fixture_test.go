package opencode

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func buildFixtureDB(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	dbDir := filepath.Join(home, ".local", "share", "opencode")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dbDir, "opencode.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	stmts := []string{
		`CREATE TABLE session (id TEXT PRIMARY KEY, directory TEXT NOT NULL, title TEXT NOT NULL, version TEXT NOT NULL DEFAULT '', time_created INTEGER NOT NULL DEFAULT 0, time_updated INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE session_message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, type TEXT NOT NULL, seq INTEGER NOT NULL, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,

		`INSERT INTO session VALUES ('ses_a','/tmp/opencode-a','first session','1.17.6',1781442100000,1781442200000)`,
		`INSERT INTO session_message VALUES ('msg_a1','ses_a','user',1,1781442101000,1781442101000,'{}')`,
		`INSERT INTO part VALUES ('prt_a1','msg_a1','ses_a',1781442101000,1781442101000,'{"type":"text","text":"hello from a"}')`,
		`INSERT INTO session_message VALUES ('msg_a2','ses_a','assistant',2,1781442102000,1781442102000,'{}')`,
		`INSERT INTO part VALUES ('prt_a2','msg_a2','ses_a',1781442102000,1781442102000,'{"type":"text","text":"hi back"}')`,
		`INSERT INTO session_message VALUES ('msg_a3','ses_a','user',3,1781442150000,1781442150000,'{}')`,
		`INSERT INTO part VALUES ('prt_a3','msg_a3','ses_a',1781442150000,1781442150000,'{"type":"text","text":"another turn"}')`,
		`INSERT INTO session_message VALUES ('msg_a4','ses_a','assistant',4,1781442200000,1781442200000,'{}')`,
		`INSERT INTO part VALUES ('prt_a4','msg_a4','ses_a',1781442200000,1781442200000,'{"type":"text","text":"final answer"}')`,

		`INSERT INTO session VALUES ('ses_b','/tmp/opencode-b','corrupt-mix','1.17.6',1781442300000,1781442400000)`,
		`INSERT INTO session_message VALUES ('msg_b1','ses_b','user',1,1781442301000,1781442301000,'{}')`,
		`INSERT INTO part VALUES ('prt_b1','msg_b1','ses_b',1781442301000,1781442301000,'this is not valid json {{{')`,

		`INSERT INTO session VALUES ('ses_c','/tmp/opencode-c','empty','1.17.6',1781442500000,1781442500000)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("fixture exec %q: %v", q, err)
		}
	}
	return home
}

func addLongPreviewSession(t *testing.T, home string) string {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(home, ".local", "share", "opencode", "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	id := "ses_long"
	long := strings.Repeat("c", 400)
	stmts := []string{
		`INSERT INTO session VALUES ('ses_long','/tmp/opencode-long','long','1.17.6',1781442600000,1781442601000)`,
		`INSERT INTO session_message VALUES ('msg_long','ses_long','assistant',1,1781442601000,1781442601000,'{}')`,
		`INSERT INTO part VALUES ('prt_long','msg_long','ses_long',1781442601000,1781442601000,?)`,
	}
	for i, q := range stmts {
		var err error
		if i == len(stmts)-1 {
			_, err = db.Exec(q, `{"type":"text","text":"`+long+`"}`)
		} else {
			_, err = db.Exec(q)
		}
		if err != nil {
			t.Fatalf("long fixture exec %q: %v", q, err)
		}
	}
	return id
}

func addMessageTableSession(t *testing.T, home string) string {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(home, ".local", "share", "opencode", "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,
		`INSERT INTO session VALUES ('ses_real','/tmp/opencode-real','real-schema','1.17.6',1781442700000,1781442704000)`,
		`INSERT INTO message VALUES ('msg_real_user','ses_real',1781442701000,1781442701000,'{"role":"user","time":{"created":1781442701000}}')`,
		`INSERT INTO part VALUES ('prt_real_user','msg_real_user','ses_real',1781442701000,1781442701000,'{"type":"text","text":"real user"}')`,
		`INSERT INTO message VALUES ('msg_real_assistant','ses_real',1781442702000,1781442704000,'{"role":"assistant","time":{"created":1781442702000}}')`,
		`INSERT INTO part VALUES ('prt_real_reason','msg_real_assistant','ses_real',1781442702000,1781442702000,'{"type":"reasoning","text":"hidden thought"}')`,
		`INSERT INTO part VALUES ('prt_real_assistant','msg_real_assistant','ses_real',1781442703000,1781442703000,'{"type":"text","text":"real assistant"}')`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("message-table fixture exec %q: %v", q, err)
		}
	}
	return "ses_real"
}

func addWhitespaceFirstUserSession(t *testing.T, home string) string {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(home, ".local", "share", "opencode", "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	stmts := []string{
		`INSERT INTO session VALUES ('ses_title','/tmp/opencode-title','title','1.17.6',1781442800000,1781442803000)`,
		`INSERT INTO session_message VALUES ('msg_title_blank','ses_title','user',1,1781442801000,1781442801000,'{}')`,
		`INSERT INTO part VALUES ('prt_title_blank','msg_title_blank','ses_title',1781442801000,1781442801000,'{"type":"text","text":"   \n\t   "}')`,
		`INSERT INTO session_message VALUES ('msg_title_user','ses_title','user',2,1781442802000,1781442802000,'{}')`,
		`INSERT INTO part VALUES ('prt_title_user','msg_title_user','ses_title',1781442802000,1781442802000,'{"type":"text","text":"real prompt"}')`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("whitespace-title fixture exec %q: %v", q, err)
		}
	}
	return "ses_title"
}

func addMessageTableWhitespaceFirstUserSession(t *testing.T, home string) string {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(home, ".local", "share", "opencode", "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,
		`INSERT INTO session VALUES ('ses_message_title','/tmp/opencode-message-title','title','1.17.6',1781442900000,1781442903000)`,
		`INSERT INTO message VALUES ('msg_message_title_blank','ses_message_title',1781442901000,1781442901000,'{"role":"user","time":{"created":1781442901000}}')`,
		`INSERT INTO part VALUES ('prt_message_title_blank','msg_message_title_blank','ses_message_title',1781442901000,1781442901000,'{"type":"text","text":"   \n\t   "}')`,
		`INSERT INTO message VALUES ('msg_message_title_user','ses_message_title',1781442902000,1781442902000,'{"role":"user","time":{"created":1781442902000}}')`,
		`INSERT INTO part VALUES ('prt_message_title_user','msg_message_title_user','ses_message_title',1781442902000,1781442902000,'{"type":"text","text":"real message prompt"}')`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("message-table whitespace-title fixture exec %q: %v", q, err)
		}
	}
	return "ses_message_title"
}
