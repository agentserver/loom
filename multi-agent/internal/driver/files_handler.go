package driver

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FilesHandler is the http.Handler for /files/* serving the FileRegistry.
type FilesHandler struct {
	reg   *FileRegistry
	audit *AuditLog
}

func NewFilesHandler(reg *FileRegistry, audit *AuditLog) *FilesHandler {
	return &FilesHandler{reg: reg, audit: audit}
}

// ServeHTTP enforces the peer header then dispatches by path.
func (h *FilesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	peer := r.Header.Get("X-Agentserver-Peer-Short-Id")
	if peer == "" {
		http.Error(w, "missing X-Agentserver-Peer-Short-Id", http.StatusUnauthorized)
		return
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/files/blob/"):
		h.handleBlob(w, r, peer)
	case strings.HasPrefix(r.URL.Path, "/files/dir/"):
		// /files/dir/{tok}/blob OR /files/dir/{tok}
		rest := strings.TrimPrefix(r.URL.Path, "/files/dir/")
		if i := strings.Index(rest, "/blob"); i > 0 && rest[i:] == "/blob" {
			tok := rest[:i]
			h.handleDirBlob(w, r, peer, tok)
		} else {
			h.handleDirList(w, r, peer, rest)
		}
	case strings.HasPrefix(r.URL.Path, "/files/put/"):
		h.handlePut(w, r, peer)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (h *FilesHandler) handleBlob(w http.ResponseWriter, r *http.Request, peer string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sha := strings.TrimPrefix(r.URL.Path, "/files/blob/")
	path, ok := h.reg.LookupBlob(sha)
	if !ok {
		http.Error(w, "blob not found", http.StatusNotFound)
		return
	}
	if err := AssertNoSymlinkLeaf(path); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	size, mt, _ := h.reg.BlobMeta(sha)
	if mt != "" {
		w.Header().Set("Content-Type", mt)
	}
	if size > 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	}
	written, _ := io.Copy(w, f)
	h.audit.Log(AuditEvent{
		Event: "fetch_blob", Path: path, SHA256: sha,
		Bytes: written, PeerShortID: peer,
	})
}

func (h *FilesHandler) handleDirList(w http.ResponseWriter, r *http.Request, peer, tok string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	root, ok := h.reg.LookupDir(tok)
	if !ok {
		http.Error(w, "dir token not found", http.StatusNotFound)
		return
	}
	recursive := r.URL.Query().Get("recursive") == "true"
	prefix := r.URL.Query().Get("prefix")
	if prefix != "" {
		if err := AssertSafeRelPath(prefix); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	type entry struct {
		RelPath   string `json:"relpath"`
		Size      int64  `json:"size"`
		MTime     string `json:"mtime"`
		SHA256    string `json:"sha256,omitempty"`
		IsDir     bool   `json:"is_dir"`
		IsSymlink bool   `json:"is_symlink,omitempty"`
		Skipped   bool   `json:"skipped,omitempty"`
	}

	walkRoot := root
	if prefix != "" {
		walkRoot = filepath.Join(root, prefix)
	}
	var out []entry
	walkErr := filepath.WalkDir(walkRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if path == walkRoot {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		info, _ := d.Info()
		isSym := info.Mode()&os.ModeSymlink != 0
		if isSym {
			out = append(out, entry{RelPath: rel, IsSymlink: true, Skipped: true,
				MTime: info.ModTime().UTC().Format(time.RFC3339)})
			return nil
		}
		if d.IsDir() {
			out = append(out, entry{RelPath: rel, IsDir: true,
				MTime: info.ModTime().UTC().Format(time.RFC3339)})
			if !recursive {
				return filepath.SkipDir
			}
			return nil
		}
		// regular file
		var sha string
		var size int64 = info.Size()
		if cachedSha, _, ok := h.reg.GetDirEntrySHA(tok, rel); ok {
			sha = cachedSha
		} else {
			sha, _ = sha256OfFile(path)
			h.reg.SetDirEntrySHA(tok, rel, sha, size)
		}
		out = append(out, entry{RelPath: rel, Size: size, SHA256: sha,
			MTime: info.ModTime().UTC().Format(time.RFC3339)})
		return nil
	})
	if walkErr != nil {
		http.Error(w, walkErr.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"root":    root,
		"entries": out,
	})
	h.audit.Log(AuditEvent{Event: "fetch_dir", Path: root, PeerShortID: peer})
}

func (h *FilesHandler) handleDirBlob(w http.ResponseWriter, r *http.Request, peer, tok string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	root, ok := h.reg.LookupDir(tok)
	if !ok {
		http.Error(w, "dir token not found", http.StatusNotFound)
		return
	}
	rel := r.URL.Query().Get("path")
	if err := AssertSafeRelPath(rel); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	target := filepath.Join(root, rel)
	if !strings.HasPrefix(target+string(filepath.Separator), root+string(filepath.Separator)) &&
		target != root {
		http.Error(w, "path escapes root", http.StatusBadRequest)
		return
	}
	cur := root
	for _, part := range strings.Split(filepath.Clean(rel), string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if info.Mode()&os.ModeSymlink != 0 {
			http.Error(w, "symlink in path: "+cur, http.StatusForbidden)
			return
		}
	}
	f, err := os.Open(target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()
	written, _ := io.Copy(w, f)
	h.audit.Log(AuditEvent{
		Event: "fetch_blob", Path: target,
		Bytes: written, PeerShortID: peer,
	})
}

func (h *FilesHandler) handlePut(w http.ResponseWriter, r *http.Request, peer string) {
	if r.Method != http.MethodPut {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	tok := strings.TrimPrefix(r.URL.Path, "/files/put/")
	entry, ok := h.reg.ConsumeWriteToken(tok)
	if !ok {
		http.Error(w, "token not found or already used", http.StatusGone)
		return
	}
	target := entry.Path
	parent := filepath.Dir(target)
	if _, err := os.Stat(parent); err != nil {
		http.Error(w, "parent dir missing: "+parent, http.StatusConflict)
		return
	}
	if _, err := os.Stat(target); err == nil && !entry.Overwrite {
		http.Error(w, "target exists and overwrite=false", http.StatusConflict)
		return
	}
	tmpName := fmt.Sprintf("%s.tmp.%s", target, randSuffix())
	out, err := os.OpenFile(tmpName, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	hasher := sha256.New()
	mw := io.MultiWriter(out, hasher)
	written, copyErr := io.Copy(mw, r.Body)
	if copyErr != nil {
		out.Close()
		os.Remove(tmpName)
		http.Error(w, copyErr.Error(), http.StatusInternalServerError)
		return
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmpName)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out.Close()
	if err := os.Rename(tmpName, target); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sha := hex.EncodeToString(hasher.Sum(nil))
	if entry.TaskID != "" {
		h.reg.RecordWritten(entry.TaskID, WrittenFile{
			Path:      target,
			Bytes:     written,
			SHA256:    sha,
			WrittenAt: time.Now().UTC().Format(time.RFC3339Nano),
		})
	}
	h.audit.Log(AuditEvent{
		Event: "put_blob", Path: target, SHA256: sha,
		Bytes: written, TaskID: entry.TaskID, PeerShortID: peer,
		Overwrite: entry.Overwrite,
	})
	w.WriteHeader(http.StatusOK)
}

func sha256OfFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func randSuffix() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
