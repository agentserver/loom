package opencode

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestBackendRunResumeUsesSessionWorkingDir(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	sessionDir := t.TempDir()
	configDir := t.TempDir()

	dbDir := filepath.Join(home, ".local", "share", "opencode")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dbDir, "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE session (id TEXT PRIMARY KEY, directory TEXT NOT NULL, title TEXT NOT NULL, version TEXT NOT NULL DEFAULT '', time_created INTEGER NOT NULL DEFAULT 0, time_updated INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE session_message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, type TEXT NOT NULL, seq INTEGER NOT NULL, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO session VALUES ('ses-cwd', ?, 'cwd', '1.17.6', 1781442100000, 1781442200000)`, sessionDir); err != nil {
		t.Fatal(err)
	}

	cwdPath := filepath.Join(home, "cwd.txt")
	argvPath := filepath.Join(home, "argv.txt")
	fakeBin := goBuildFake(t, fmt.Sprintf(`package main
import (
	"io"
	"os"
	"strings"
)
func main() {
	cwd, _ := os.Getwd()
	_ = os.WriteFile(%q, []byte(cwd), 0o600)
	_ = os.WriteFile(%q, []byte(strings.Join(os.Args[1:], "|")), 0o600)
	_, _ = io.Copy(io.Discard, os.Stdin)
}
`, cwdPath, argvPath), "opencode")

	b := New(agentbackend.Config{Bin: fakeBin, WorkDir: configDir}, nil)
	if _, err := b.RunResume(context.Background(), agentbackend.NewBackend(agentbackend.KindOpencode, "", "ses-cwd"), "continue", &captureSink{}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(cwdPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != sessionDir {
		t.Fatalf("resume cwd=%q want session WorkingDir %q (config WorkDir was %q)", got, sessionDir, configDir)
	}
	argv, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(argv), "--dir|"+sessionDir) {
		t.Fatalf("resume argv=%q want --dir %q", argv, sessionDir)
	}
}

func TestBackendRunResumeDoesNotLoadMessagesForWorkingDir(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	sessionDir := t.TempDir()
	configDir := t.TempDir()

	dbDir := filepath.Join(home, ".local", "share", "opencode")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dbDir, "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE session (id TEXT PRIMARY KEY, directory TEXT NOT NULL, title TEXT NOT NULL, version TEXT NOT NULL DEFAULT '', time_created INTEGER NOT NULL DEFAULT 0, time_updated INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO session VALUES ('ses-meta-only', ?, 'cwd', '1.17.6', 1781442100000, 1781442200000)`, sessionDir); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	cwdPath := filepath.Join(home, "cwd-meta-only.txt")
	fakeBin := goBuildFake(t, fmt.Sprintf(`package main
import (
	"io"
	"os"
)
func main() {
	cwd, _ := os.Getwd()
	_ = os.WriteFile(%q, []byte(cwd), 0o600)
	_, _ = io.Copy(io.Discard, os.Stdin)
}
`, cwdPath), "opencode")

	b := New(agentbackend.Config{Bin: fakeBin, WorkDir: configDir}, nil)
	if _, err := b.RunResume(context.Background(), agentbackend.NewBackend(agentbackend.KindOpencode, "", "ses-meta-only"), "continue", &captureSink{}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(cwdPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != sessionDir {
		t.Fatalf("resume cwd=%q want session WorkingDir %q", got, sessionDir)
	}
}
