package sessioncache

import (
	"io/fs"
	"sync"
	"time"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// FileCache caches session descriptors derived from file-backed agent session
// storage. A descriptor is reused while the file path, size, and mtime are
// unchanged; callers still choose how to scan the file on cache miss.
type FileCache struct {
	mu      sync.Mutex
	entries map[string]fileEntry
}

type fileEntry struct {
	size    int64
	modTime time.Time
	session agentbackend.Session
}

func NewFileCache() *FileCache {
	return &FileCache{entries: make(map[string]fileEntry)}
}

func (c *FileCache) Get(path string, info fs.FileInfo, scan func() agentbackend.Session) agentbackend.Session {
	if c == nil || info == nil {
		return scan()
	}
	size := info.Size()
	modTime := info.ModTime()

	c.mu.Lock()
	if ent, ok := c.entries[path]; ok && ent.size == size && ent.modTime.Equal(modTime) {
		session := ent.session
		c.mu.Unlock()
		return session
	}
	c.mu.Unlock()

	session := scan()

	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[string]fileEntry)
	}
	c.entries[path] = fileEntry{size: size, modTime: modTime, session: session}
	c.mu.Unlock()
	return session
}

func (c *FileCache) Prune(seen map[string]struct{}) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for path := range c.entries {
		if _, ok := seen[path]; !ok {
			delete(c.entries, path)
		}
	}
}
