package executor

import (
	"context"
	"encoding/json"
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
	if len(tools) != 1 || tools[0].Name != "foo" || len(tools[0].InputSchema) != 0 {
		t.Fatalf("tools = %v", tools)
	}
}

func TestSmokeLaunch_Descriptors(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	dir := t.TempDir()
	src := `
import sys, json
for line in sys.stdin:
    req = json.loads(line)
    if req.get("method") == "tools/list":
        print(json.dumps({"jsonrpc":"2.0","id":req["id"],"result":{"tools":[{
            "name":"foo",
            "description":"Foo tool",
            "inputSchema":{"type":"object","properties":{"msg":{"type":"string"}}},
            "result_description":"Foo result"
        }]}}), flush=True)
`
	path := filepath.Join(dir, "descriptors.py")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	tools, err := SmokeLaunchPython(context.Background(), path, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 {
		t.Fatalf("tools = %v", tools)
	}
	if tools[0].Name != "foo" || tools[0].Description != "Foo tool" || tools[0].ResultDescription != "Foo result" {
		t.Fatalf("tool descriptor = %+v", tools[0])
	}
	var schema struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tools[0].InputSchema, &schema); err != nil {
		t.Fatalf("schema unmarshal: %v", err)
	}
	if schema.Type != "object" || schema.Properties["msg"].Type != "string" {
		t.Fatalf("schema = %s", tools[0].InputSchema)
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
