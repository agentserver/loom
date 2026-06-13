package driver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// TokenSource exposes a live observer Bearer token. *observerclient.Client
// satisfies this; the driver test suite supplies a stub implementation.
type TokenSource interface {
	Token() string
}

type ObserverRelay struct {
	baseURL string
	src     TokenSource
	http    *http.Client
}

func NewObserverRelay(cfg *Config, src TokenSource) *ObserverRelay {
	if cfg == nil || !cfg.Observer.Enabled || cfg.Observer.URL == "" || src == nil {
		return nil
	}
	return &ObserverRelay{
		baseURL: strings.TrimRight(cfg.Observer.URL, "/"),
		src:     src,
		http:    http.DefaultClient,
	}
}

type observerArtifactCreate struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	MIME   string `json:"mime,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Mode   string `json:"mode"`
}

type observerArtifactCreateResponse struct {
	ArtifactID string `json:"artifact_id"`
	URL        string `json:"url"`
	State      string `json:"state"`
}

type observerTaskContractSave struct {
	TaskID         string          `json:"task_id"`
	ConversationID string          `json:"conversation_id"`
	Body           json.RawMessage `json:"body"`
}

type observerResourceSnapshotSave struct {
	SnapshotID string          `json:"snapshot_id"`
	Body       json.RawMessage `json:"body"`
}

func (r *ObserverRelay) SaveTaskContract(ctx context.Context, taskID, conversationID string, body json.RawMessage) error {
	if r == nil {
		return nil
	}
	payload, _ := json.Marshal(observerTaskContractSave{
		TaskID:         taskID,
		ConversationID: conversationID,
		Body:           body,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/api/task-contracts", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.src.Token())
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("save task contract status %d", resp.StatusCode)
	}
	return nil
}

func (r *ObserverRelay) SaveResourceSnapshot(ctx context.Context, body json.RawMessage) error {
	if r == nil {
		return nil
	}
	payload, _ := json.Marshal(observerResourceSnapshotSave{
		SnapshotID: "snap-" + strconv.FormatInt(time.Now().UnixNano(), 10),
		Body:       body,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/api/resource-snapshots", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.src.Token())
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("save resource snapshot status %d", resp.StatusCode)
	}
	return nil
}

func (r *ObserverRelay) RegisterArtifact(ctx context.Context, entry observerArtifactCreate) (*observerArtifactCreateResponse, error) {
	if r == nil {
		return nil, fmt.Errorf("observer relay is not configured")
	}
	body, _ := json.Marshal(entry)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/api/artifacts", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.src.Token())
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("register artifact status %d", resp.StatusCode)
	}
	var out observerArtifactCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

type observerWriteCreate struct {
	TaskID    string `json:"task_id"`
	Path      string `json:"path"`
	Overwrite bool   `json:"overwrite"`
}

type observerWriteCreateResponse struct {
	WriteID string `json:"write_id"`
	PutURL  string `json:"put_url"`
}

func (r *ObserverRelay) CreateWrite(ctx context.Context, entry observerWriteCreate) (*observerWriteCreateResponse, error) {
	if r == nil {
		return nil, fmt.Errorf("observer relay is not configured")
	}
	body, _ := json.Marshal(entry)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/api/write-tokens", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.src.Token())
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("create write status %d", resp.StatusCode)
	}
	var out observerWriteCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (r *ObserverRelay) UpdateWriteTask(ctx context.Context, writeID, taskID string) error {
	if r == nil || writeID == "" {
		return nil
	}
	body, _ := json.Marshal(map[string]string{"task_id": taskID})
	deadline := time.Now().Add(15 * time.Second)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodPatch, r.baseURL+"/api/writes/"+writeID, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+r.src.Token())
		req.Header.Set("Content-Type", "application/json")
		resp, err := r.http.Do(req)
		if err != nil {
			return err
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode/100 == 2 {
			return nil
		}
		msg := strings.TrimSpace(string(respBody))
		if resp.StatusCode == http.StatusInternalServerError && isObserverSQLiteBusy(msg) && time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
				continue
			}
		}
		if msg != "" {
			return fmt.Errorf("update write task status %d: %s", resp.StatusCode, msg)
		}
		return fmt.Errorf("update write task status %d", resp.StatusCode)
	}
}

func isObserverSQLiteBusy(msg string) bool {
	return strings.Contains(msg, "SQLITE_BUSY") || strings.Contains(strings.ToLower(msg), "database is locked")
}

type observerArtifactRequestsResponse struct {
	Requests []struct {
		RequestID  string `json:"request_id"`
		ArtifactID string `json:"artifact_id"`
		Kind       string `json:"kind"`
		Path       string `json:"path"`
		State      string `json:"state"`
	} `json:"requests"`
}

func (r *ObserverRelay) ServePendingOnce(ctx context.Context, reg *FileRegistry, audit *AuditLog) error {
	if r == nil {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+"/api/artifact-requests", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.src.Token())
	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("artifact requests status %d", resp.StatusCode)
	}
	var listed observerArtifactRequestsResponse
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		return err
	}
	var errs []error
	for _, pending := range listed.Requests {
		path, kind, ok := reg.LookupObserverArtifact(pending.ArtifactID)
		if !ok {
			continue
		}
		if kind != "file" {
			continue
		}
		if err := AssertNoSymlinkLeaf(path); err != nil {
			errs = append(errs, fmt.Errorf("artifact %s: %w", pending.ArtifactID, err))
			continue
		}
		if err := r.uploadFile(ctx, pending.ArtifactID, path, audit); err != nil {
			errs = append(errs, fmt.Errorf("artifact %s: %w", pending.ArtifactID, err))
			continue
		}
	}
	return errors.Join(errs...)
}

func (r *ObserverRelay) uploadFile(ctx context.Context, artifactID, path string, audit *AuditLog) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, r.baseURL+"/api/artifacts/"+artifactID+"/content", f)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.src.Token())
	if mt := mimeForPath(path); mt != "" {
		req.Header.Set("Content-Type", mt)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("upload artifact status %d", resp.StatusCode)
	}
	if audit != nil {
		if sha, size, err := hashFile(path); err == nil {
			audit.Log(AuditEvent{Event: "observer_upload_artifact", Path: path, SHA256: sha, Bytes: size})
		}
	}
	return nil
}

func (r *ObserverRelay) ServePendingLoop(ctx context.Context, reg *FileRegistry, audit *AuditLog, interval time.Duration) {
	if r == nil {
		return
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := r.ServePendingOnce(ctx, reg, audit); err != nil && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "driver: observer relay serve pending: %v\n", err)
			if audit != nil {
				audit.Log(AuditEvent{Event: "observer_relay_error", Op: "serve_pending", Error: err.Error()})
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type observerWritesResponse struct {
	Writes []struct {
		WriteID   string `json:"write_id"`
		Path      string `json:"path"`
		Overwrite bool   `json:"overwrite"`
		Bytes     int64  `json:"bytes"`
		SHA256    string `json:"sha256"`
		Content   []byte `json:"content"`
	} `json:"writes"`
}

func (r *ObserverRelay) SyncWrites(ctx context.Context, taskID string, disableUIDCheck bool, reg *FileRegistry) ([]WrittenFile, error) {
	if r == nil {
		return nil, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+"/api/writes?task_id="+taskID, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.src.Token())
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("list writes status %d", resp.StatusCode)
	}
	var listed observerWritesResponse
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		return nil, err
	}
	written := make([]WrittenFile, 0, len(listed.Writes))
	for _, item := range listed.Writes {
		if err := AssertWritableTarget(item.Path, disableUIDCheck); err != nil {
			return nil, err
		}
		if _, err := os.Stat(item.Path); err == nil && !item.Overwrite {
			return nil, fmt.Errorf("target exists and overwrite=false")
		}
		if err := os.MkdirAll(filepath.Dir(item.Path), 0o755); err != nil {
			return nil, err
		}
		tmp := fmt.Sprintf("%s.tmp.%d", item.Path, time.Now().UnixNano())
		if err := os.WriteFile(tmp, item.Content, 0o644); err != nil {
			return nil, err
		}
		if err := os.Rename(tmp, item.Path); err != nil {
			_ = os.Remove(tmp)
			return nil, err
		}
		sha := item.SHA256
		if sha == "" {
			sum := sha256.Sum256(item.Content)
			sha = hex.EncodeToString(sum[:])
		}
		row := WrittenFile{Path: item.Path, Bytes: int64(len(item.Content)), SHA256: sha, WrittenAt: time.Now().UTC().Format(time.RFC3339Nano)}
		if item.Bytes > 0 {
			row.Bytes = item.Bytes
		}
		written = append(written, row)
		if reg != nil {
			reg.RecordWritten(taskID, row)
		}
	}
	return written, nil
}

func mimeForPath(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".txt", ".md", ".csv", ".json", ".yaml", ".yml":
		return "text/plain"
	default:
		return "application/octet-stream"
	}
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	hasher := sha256.New()
	n, err := io.Copy(hasher, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), n, nil
}
