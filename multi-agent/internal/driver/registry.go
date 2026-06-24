package driver

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// WriteEntry is what ConsumeWriteToken returns to the /files/put handler.
type WriteEntry struct {
	Path      string
	Overwrite bool
	TaskID    string
}

// WrittenFile is recorded when a slave successfully PUTs to a write token;
// surfaced via wait_task to Claude.
type WrittenFile struct {
	Path      string `json:"path"`
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256"`
	WrittenAt string `json:"written_at"`
}

// FileRegistry holds read/dir/write tokens for an in-flight driver process.
type FileRegistry struct {
	mu                sync.RWMutex
	blobs             map[string]string              // sha256 -> abs path
	blobMeta          map[string]blobMeta            // sha256 -> size, mime
	dirs              map[string]string              // dir token -> abs root
	writes            map[string]WriteEntry          // write token -> entry (single-use)
	dirSHA            map[string]map[string]dirEntry // dir token -> relpath -> entry
	observerArtifacts map[string]observerArtifactEntry
	maxDirEntries     int

	taskMu      sync.Mutex
	taskWritten map[string][]WrittenFile // task_id -> appended writes
	taskOrder   []string                 // insertion order for bounded eviction
}

type observerArtifactEntry struct {
	path string
	kind string
}

type blobMeta struct {
	size int64
	mime string
}

type dirEntry struct {
	sha  string
	size int64
}

func NewFileRegistry(maxDirEntries int) *FileRegistry {
	if maxDirEntries <= 0 {
		maxDirEntries = 50000
	}
	return &FileRegistry{
		blobs:             map[string]string{},
		blobMeta:          map[string]blobMeta{},
		dirs:              map[string]string{},
		writes:            map[string]WriteEntry{},
		dirSHA:            map[string]map[string]dirEntry{},
		observerArtifacts: map[string]observerArtifactEntry{},
		maxDirEntries:     maxDirEntries,
		taskWritten:       map[string][]WrittenFile{},
	}
}

// RegisterFile reads the file, computes sha256, stores under that key, and
// returns (sha, size, mime, err). Re-registering an unchanged file is a no-op.
func (r *FileRegistry) RegisterFile(absPath string) (string, int64, string, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return "", 0, "", fmt.Errorf("open %s: %w", absPath, err)
	}
	defer f.Close()
	hasher := sha256.New()
	size, err := io.Copy(hasher, f)
	if err != nil {
		return "", 0, "", fmt.Errorf("read %s: %w", absPath, err)
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	mt := mime.TypeByExtension(filepath.Ext(absPath))
	if mt == "" {
		// Probe first 512 bytes for magic-bytes-based detection.
		if _, err := f.Seek(0, 0); err == nil {
			head := make([]byte, 512)
			n, _ := f.Read(head)
			mt = http.DetectContentType(head[:n])
		}
	}
	r.mu.Lock()
	r.blobs[sum] = absPath
	r.blobMeta[sum] = blobMeta{size: size, mime: mt}
	r.mu.Unlock()
	return sum, size, mt, nil
}

func (r *FileRegistry) LookupBlob(sha string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.blobs[sha]
	return p, ok
}

func (r *FileRegistry) BlobMeta(sha string) (int64, string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.blobMeta[sha]
	return m.size, m.mime, ok
}

// RegisterDir mints a random opaque token for a directory root.
func (r *FileRegistry) RegisterDir(absRoot string) string {
	tok := newToken()
	r.mu.Lock()
	r.dirs[tok] = absRoot
	r.dirSHA[tok] = map[string]dirEntry{}
	r.mu.Unlock()
	return tok
}

func (r *FileRegistry) LookupDir(tok string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.dirs[tok]
	return p, ok
}

// SetDirEntrySHA caches a (relpath -> sha,size) entry for a dir token, with
// bounded eviction (drop-arbitrary on overflow — order is not load-bearing).
func (r *FileRegistry) SetDirEntrySHA(tok, rel, sha string, size int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tab, ok := r.dirSHA[tok]
	if !ok {
		return // unknown token; caller's bug, not ours
	}
	if len(tab) >= r.maxDirEntries {
		// Drop one arbitrary entry — Go's map iteration order is randomized.
		for k := range tab {
			delete(tab, k)
			break
		}
	}
	tab[rel] = dirEntry{sha: sha, size: size}
}

func (r *FileRegistry) GetDirEntrySHA(tok, rel string) (string, int64, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tab, ok := r.dirSHA[tok]
	if !ok {
		return "", 0, false
	}
	e, ok := tab[rel]
	return e.sha, e.size, ok
}

// RegisterWrite mints a single-use write token bound to (path, overwrite, task_id).
// taskID may be empty if the caller has not yet delegated.
func (r *FileRegistry) RegisterWrite(absPath string, overwrite bool, taskID string) string {
	tok := newToken()
	r.mu.Lock()
	r.writes[tok] = WriteEntry{Path: absPath, Overwrite: overwrite, TaskID: taskID}
	r.mu.Unlock()
	return tok
}

// ConsumeWriteToken atomically removes and returns the write entry.
// A second consume of the same token returns ok=false.
func (r *FileRegistry) ConsumeWriteToken(tok string) (WriteEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.writes[tok]
	if !ok {
		return WriteEntry{}, false
	}
	delete(r.writes, tok)
	return e, true
}

// RebindWriteTokenTaskID updates the TaskID on an existing (un-consumed)
// write token. Used by submit_task after DelegateTask returns.
func (r *FileRegistry) RebindWriteTokenTaskID(tok, taskID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.writes[tok]
	if !ok {
		return
	}
	e.TaskID = taskID
	r.writes[tok] = e
}

// TrackTask records a task_id -> write tokens association. Used by tools.go to
// later look up "what writes belong to this task" for wait_task reporting.
// Evicts oldest entry when the map exceeds 256 tasks.
func (r *FileRegistry) TrackTask(taskID string, writeTokens []string) {
	r.taskMu.Lock()
	defer r.taskMu.Unlock()
	if _, ok := r.taskWritten[taskID]; ok {
		return // already tracked
	}
	r.taskWritten[taskID] = nil
	r.taskOrder = append(r.taskOrder, taskID)
	const maxTasks = 256
	if len(r.taskOrder) > maxTasks {
		evict := r.taskOrder[0]
		r.taskOrder = r.taskOrder[1:]
		delete(r.taskWritten, evict)
	}
}

// RecordWritten appends a successful PUT to a task's written list.
// Called from the /files/put handler when it knows which task owns the token.
func (r *FileRegistry) RecordWritten(taskID string, w WrittenFile) {
	r.taskMu.Lock()
	defer r.taskMu.Unlock()
	r.taskWritten[taskID] = append(r.taskWritten[taskID], w)
}

// WrittenFiles returns the files written for a task, or nil if the task is
// not tracked (e.g. evicted or forgotten). An empty non-nil slice means the
// task is tracked but no files have been written yet.
func (r *FileRegistry) WrittenFiles(taskID string) []WrittenFile {
	r.taskMu.Lock()
	defer r.taskMu.Unlock()
	v, ok := r.taskWritten[taskID]
	if !ok {
		return nil
	}
	out := make([]WrittenFile, len(v))
	copy(out, v)
	return out
}

func (r *FileRegistry) ForgetTask(taskID string) {
	r.taskMu.Lock()
	defer r.taskMu.Unlock()
	delete(r.taskWritten, taskID)
	for i, id := range r.taskOrder {
		if id == taskID {
			r.taskOrder = append(r.taskOrder[:i], r.taskOrder[i+1:]...)
			return
		}
	}
}

func (r *FileRegistry) RegisterObserverArtifact(id, absPath, kind string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observerArtifacts[id] = observerArtifactEntry{path: absPath, kind: kind}
}

func (r *FileRegistry) LookupObserverArtifact(id string) (path, kind string, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.observerArtifacts[id]
	if !ok {
		return "", "", false
	}
	return entry.path, entry.kind, true
}

// FileRegistrySnapshot is a count-of-each-bucket view used by tests to
// assert "no side effects" without touching internal fields.
type FileRegistrySnapshot struct {
	Blobs             int
	Dirs              int
	Writes            int
	ObserverArtifacts int
}

// snapshotForTest returns bucket counts for the live registry. Test-only;
// kept lowercase to stay package-private.
func (r *FileRegistry) snapshotForTest() FileRegistrySnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return FileRegistrySnapshot{
		Blobs:             len(r.blobs),
		Dirs:              len(r.dirs),
		Writes:            len(r.writes),
		ObserverArtifacts: len(r.observerArtifacts),
	}
}

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
