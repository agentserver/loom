package driver

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestAuditLog_LineFormat(t *testing.T) {
	dir := t.TempDir()
	a, err := NewAuditLog(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatalf("NewAuditLog: %v", err)
	}
	defer a.Close()
	a.Log(AuditEvent{
		Event: "register_read", Path: "/home/me/x.csv",
		SHA256: "abc", Bytes: 123, TaskID: "",
	})
	a.Log(AuditEvent{
		Event: "fetch_blob", Path: "/home/me/x.csv",
		SHA256: "abc", Bytes: 123, TaskID: "t-9", PeerShortID: "slv-1",
	})

	f, err := os.Open(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	var lines []AuditEvent
	for scanner.Scan() {
		var e AuditEvent
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			t.Fatalf("line %q: %v", scanner.Text(), err)
		}
		lines = append(lines, e)
	}
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d", len(lines))
	}
	if lines[0].Event != "register_read" || lines[0].SHA256 != "abc" || lines[0].TS == "" {
		t.Errorf("line 0: %+v", lines[0])
	}
	if lines[1].PeerShortID != "slv-1" || lines[1].TaskID != "t-9" {
		t.Errorf("line 1: %+v", lines[1])
	}
}

func TestAuditLog_ConcurrentLineAtomicity(t *testing.T) {
	dir := t.TempDir()
	a, err := NewAuditLog(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				a.Log(AuditEvent{Event: "register_read", Path: "/p", SHA256: "s"})
			}
		}(i)
	}
	wg.Wait()
	b, _ := os.ReadFile(filepath.Join(dir, "audit.log"))
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		var e AuditEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("torn line: %q (%v)", line, err)
		}
	}
}

func TestAuditLog_AutoCreatesDir(t *testing.T) {
	dir := t.TempDir()
	deep := filepath.Join(dir, "a", "b", "c", "audit.log")
	a, err := NewAuditLog(deep)
	if err != nil {
		t.Fatalf("NewAuditLog: %v", err)
	}
	a.Log(AuditEvent{Event: "x"})
	a.Close()
	if _, err := os.Stat(deep); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}
