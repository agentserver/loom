package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSmokeLaunch_OK(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	dir := t.TempDir()
	src := `
import sys, json
for line in sys.stdin:
    req = json.loads(line)
    if req.get("method") == "tools/list":
        print(json.dumps({"jsonrpc":"2.0","id":req["id"],"result":{"tools":[{"name":"foo"}]}}), flush=True)
`
	path := filepath.Join(dir, "ok.py")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	tools, err := SmokeLaunchPython(context.Background(), path, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0] != "foo" {
		t.Fatalf("tools = %v", tools)
	}
}

func TestSmokeLaunch_NoResponse(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	dir := t.TempDir()
	src := `
import sys, time
time.sleep(10)
` // never responds
	path := filepath.Join(dir, "hang.py")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := SmokeLaunchPython(context.Background(), path, 500*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestSmokeLaunch_PythonCrash(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	dir := t.TempDir()
	src := `raise RuntimeError("intentional")`
	path := filepath.Join(dir, "boom.py")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := SmokeLaunchPython(context.Background(), path, 2*time.Second)
	if err == nil {
		t.Fatal("expected error from crashing script")
	}
}
