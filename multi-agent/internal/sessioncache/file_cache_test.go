package sessioncache

import (
	"io/fs"
	"testing"
	"time"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type fakeFileInfo struct {
	size    int64
	modTime time.Time
}

func (f fakeFileInfo) Name() string       { return "session.jsonl" }
func (f fakeFileInfo) Size() int64        { return f.size }
func (f fakeFileInfo) Mode() fs.FileMode  { return 0o600 }
func (f fakeFileInfo) ModTime() time.Time { return f.modTime }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }

func TestFileCacheReusesUnchangedFileSignature(t *testing.T) {
	cache := NewFileCache()
	info := fakeFileInfo{size: 100, modTime: time.Unix(1000, 0)}
	var scans int

	first := cache.Get("rollout.jsonl", info, func() agentbackend.Session {
		scans++
		return agentbackend.Session{ID: "scan-1", MessageCount: scans}
	})
	second := cache.Get("rollout.jsonl", info, func() agentbackend.Session {
		scans++
		return agentbackend.Session{ID: "scan-2", MessageCount: scans}
	})

	if scans != 1 {
		t.Fatalf("scans=%d want 1", scans)
	}
	if second != first {
		t.Fatalf("second=%+v want cached first=%+v", second, first)
	}
}

func TestFileCacheInvalidatesWhenFileSignatureChanges(t *testing.T) {
	cache := NewFileCache()
	info := fakeFileInfo{size: 100, modTime: time.Unix(1000, 0)}
	updated := fakeFileInfo{size: 101, modTime: time.Unix(1001, 0)}
	var scans int

	_ = cache.Get("rollout.jsonl", info, func() agentbackend.Session {
		scans++
		return agentbackend.Session{ID: "old", MessageCount: scans}
	})
	got := cache.Get("rollout.jsonl", updated, func() agentbackend.Session {
		scans++
		return agentbackend.Session{ID: "new", MessageCount: scans}
	})

	if scans != 2 {
		t.Fatalf("scans=%d want 2", scans)
	}
	if got.ID != "new" {
		t.Fatalf("got session ID %q want new", got.ID)
	}
}

func TestFileCachePruneDropsMissingPaths(t *testing.T) {
	cache := NewFileCache()
	info := fakeFileInfo{size: 100, modTime: time.Unix(1000, 0)}
	var scans int

	_ = cache.Get("gone.jsonl", info, func() agentbackend.Session {
		scans++
		return agentbackend.Session{ID: "gone"}
	})
	cache.Prune(map[string]struct{}{"kept.jsonl": {}})
	_ = cache.Get("gone.jsonl", info, func() agentbackend.Session {
		scans++
		return agentbackend.Session{ID: "gone-again"}
	})

	if scans != 2 {
		t.Fatalf("scans=%d want 2 after prune", scans)
	}
}
