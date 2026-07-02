package probes

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHumanCount_IgnoresSiblingLogFile(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".probes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".probes/humanloop.count"), []byte("3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".probes/humanloop.log"), []byte("my secret sk-DEADBEEF12345678"), 0o644); err != nil {
		t.Fatal(err)
	}

	before := readRedactedCounter()
	e := NewEmitter(0, io.Discard)
	EmitHumanCount(context.Background(), e, ws, io.Discard)
	recs, _ := e.Close()
	if len(recs) != 1 || recs[0].Value != 3 {
		t.Fatalf("record: %#v", recs)
	}
	for k, v := range recs[0].Labels {
		if strings.Contains(v, "sk-DEADBEEF") || strings.Contains(v, "secret") {
			t.Fatalf("label %s leaked log content: %q", k, v)
		}
	}
	if after := readRedactedCounter(); after != before {
		t.Fatalf("secretscrub fired unexpectedly (%d vs %d) — did the reader read the log file?", after, before)
	}
}

func TestHumanCount_AbsentFile(t *testing.T) {
	ws := t.TempDir()
	var buf synchronizedBuf
	// Warns now flow through the emitter's stderr (§7(a) — off the
	// hot path). Pass io.Discard as the deprecated stderr param.
	e := NewEmitter(0, &buf)
	EmitHumanCount(context.Background(), e, ws, io.Discard)
	recs, _ := e.Close()
	if len(recs) != 1 || recs[0].Value != 0 || recs[0].Labels["source"] != "absent" {
		t.Fatalf("record: %#v", recs)
	}
	if buf.String() != "" {
		t.Fatalf("stderr non-empty on absent file: %q", buf.String())
	}
}

func TestHumanCount_MalformedFile(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".probes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".probes/humanloop.count"), []byte("3abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf synchronizedBuf
	// Warns now flow through the emitter's stderr (§7(a) — off the
	// hot path). Pass io.Discard as the deprecated stderr param.
	e := NewEmitter(0, &buf)
	EmitHumanCount(context.Background(), e, ws, io.Discard)
	recs, _ := e.Close()
	if len(recs) != 1 || recs[0].Value != 0 || recs[0].Labels["source"] != "malformed" {
		t.Fatalf("record: %#v", recs)
	}
	if !strings.Contains(buf.String(), "humanloop") {
		t.Fatalf("stderr warn missing: %q", buf.String())
	}
}

func TestHumanCount_SizeCap(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".probes"), 0o700); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("1", 5*1024) // 5 KiB
	if err := os.WriteFile(filepath.Join(ws, ".probes/humanloop.count"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf synchronizedBuf
	// Warns now flow through the emitter's stderr (§7(a) — off the
	// hot path). Pass io.Discard as the deprecated stderr param.
	e := NewEmitter(0, &buf)
	EmitHumanCount(context.Background(), e, ws, io.Discard)
	recs, _ := e.Close()
	if len(recs) != 1 || recs[0].Value != 0 || recs[0].Labels["source"] != "malformed" {
		t.Fatalf("record: %#v", recs)
	}
	if !strings.Contains(buf.String(), "humanloop") {
		t.Fatalf("stderr warn missing: %q", buf.String())
	}
}
