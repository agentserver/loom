package codex

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoomMetaPath(t *testing.T) {
	got := loomMetaPath("/tmp/codex-home", "thread-1")
	want := filepath.Join("/tmp/codex-home", "loom-meta", "thread-1.json")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWriteLoomMetaRoundTrip(t *testing.T) {
	base := t.TempDir()
	in := loomMeta{
		Schema:            loomMetaSchema,
		SessionID:         "thread-1",
		ParentSessionID:   "parent-thread",
		ParentAgentID:     "drv-abc",
		ParentDisplayName: "prod-driver",
		Origin:            "agent_task",
		Kind:              "codex",
		CreatedAt:         "2026-06-17T00:00:00Z",
	}
	if err := writeLoomMeta(base, in); err != nil {
		t.Fatalf("writeLoomMeta: %v", err)
	}
	out, ok := readLoomMeta(base, "thread-1")
	if !ok {
		t.Fatal("readLoomMeta: not found")
	}
	if out.ParentSessionID != "parent-thread" || out.ParentAgentID != "drv-abc" || out.ParentDisplayName != "prod-driver" {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}

func TestWriteLoomMetaInvalidRecordSkipped(t *testing.T) {
	base := t.TempDir()
	err := writeLoomMeta(base, loomMeta{
		Schema:    loomMetaSchema,
		SessionID: "thread-1",
		Origin:    "user",
		Kind:      "codex",
		CreatedAt: "2026-06-17T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("writeLoomMeta invalid returned error: %v", err)
	}
	if _, err := os.Stat(loomMetaPath(base, "thread-1")); !os.IsNotExist(err) {
		t.Fatalf("invalid sidecar was written; stat err=%v", err)
	}
}

func TestWriteLoomMetaEmptyBaseSkipped(t *testing.T) {
	err := writeLoomMeta("", loomMeta{
		Schema:    loomMetaSchema,
		SessionID: "thread-1",
		Origin:    "agent_task",
		Kind:      "codex",
		CreatedAt: "2026-06-17T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("writeLoomMeta empty base returned error: %v", err)
	}
}

func TestReadLoomMetaMissing(t *testing.T) {
	if _, ok := readLoomMeta(t.TempDir(), "nope"); ok {
		t.Fatal("expected missing sidecar")
	}
}

func TestReadLoomMetaCorruptSkipped(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(loomMetaDir(base), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(loomMetaPath(base, "bad"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readLoomMeta(base, "bad"); ok {
		t.Fatal("corrupt sidecar should be skipped")
	}
}

func TestThreadIDFromLoomMetaName(t *testing.T) {
	id, ok := threadIDFromLoomMetaName("thread-1.json")
	if !ok || id != "thread-1" {
		t.Fatalf("threadIDFromLoomMetaName = %q, %v; want thread-1, true", id, ok)
	}
	if _, ok := threadIDFromLoomMetaName("thread-1.txt"); ok {
		t.Fatal("non-json sidecar name accepted")
	}
}

func TestReaperRemovesOrphanEvenWhenNotAged(t *testing.T) {
	base := t.TempDir()
	if err := writeLoomMeta(base, loomMeta{
		Schema:    loomMetaSchema,
		SessionID: "orphan",
		Origin:    "agent_task",
		Kind:      "codex",
		CreatedAt: "2026-06-17T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	reaper(base, []string{"live-only"})
	if _, ok := readLoomMeta(base, "orphan"); ok {
		t.Fatal("orphan sidecar must be removed")
	}
}

func TestReaperRemovesAgedEvenWhenLive(t *testing.T) {
	base := t.TempDir()
	if err := writeLoomMeta(base, loomMeta{
		Schema:    loomMetaSchema,
		SessionID: "stale-live",
		Origin:    "agent_task",
		Kind:      "codex",
		CreatedAt: "2026-06-17T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	past := timeNow().Add(-(loomMetaMaxAge + time.Hour))
	if err := os.Chtimes(loomMetaPath(base, "stale-live"), past, past); err != nil {
		t.Fatal(err)
	}
	reaper(base, []string{"stale-live"})
	if _, ok := readLoomMeta(base, "stale-live"); ok {
		t.Fatal("aged sidecar must be removed even when its thread id is live")
	}
}

func TestReaperKeepsLiveAndFresh(t *testing.T) {
	base := t.TempDir()
	if err := writeLoomMeta(base, loomMeta{
		Schema:    loomMetaSchema,
		SessionID: "live",
		Origin:    "agent_task",
		Kind:      "codex",
		CreatedAt: "2026-06-17T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	reaper(base, []string{"live"})
	if _, ok := readLoomMeta(base, "live"); !ok {
		t.Fatal("live fresh sidecar must survive")
	}
}

func TestWriteReadCurrentSession(t *testing.T) {
	base := t.TempDir()
	if err := writeCurrentSession(base, "thread-now"); err != nil {
		t.Fatalf("writeCurrentSession: %v", err)
	}
	if got := ReadCurrentSession(base); got != "thread-now" {
		t.Fatalf("readCurrentSession = %q, want thread-now", got)
	}
}

func TestReadCurrentSessionMissing(t *testing.T) {
	if got := ReadCurrentSession(t.TempDir()); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}
