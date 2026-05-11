package driver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AuditEvent is one JSONL line in the driver's audit log.
// Empty fields are still written (omitempty would silently drop SHA256
// for events that record one — surprising during postmortem).
type AuditEvent struct {
	TS          string `json:"ts"`
	Event       string `json:"event"`           // register_read | register_read_dir | register_write | fetch_blob | fetch_dir | put_blob
	Path        string `json:"path"`
	SHA256      string `json:"sha256,omitempty"`
	Bytes       int64  `json:"bytes,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
	PeerShortID string `json:"peer_short_id,omitempty"`
	Overwrite   bool   `json:"overwrite,omitempty"`
}

type AuditLog struct {
	mu sync.Mutex
	f  *os.File
}

// NewAuditLog opens path for append-only writes. Parent directories are created.
func NewAuditLog(path string) (*AuditLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir audit dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open audit log: %w", err)
	}
	return &AuditLog{f: f}, nil
}

// Log marshals the event, appends it on its own line, and fsyncs.
// Errors are logged but never returned — audit failures must not block IO.
func (a *AuditLog) Log(ev AuditEvent) {
	if ev.TS == "" {
		ev.TS = time.Now().UTC().Format(time.RFC3339Nano)
	}
	b, err := json.Marshal(ev)
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit marshal: %v\n", err)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.f.Write(append(b, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "audit write: %v\n", err)
		return
	}
	_ = a.f.Sync()
}

func (a *AuditLog) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.f.Close()
}
