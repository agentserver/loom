package commander

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

var errPathOutsideRoot = errors.New("path outside session root")

// ListFiles returns a lazy, non-recursive listing rooted at the session cwd.
func (h *Handler) ListFiles(ctx context.Context, sessionID, rel string) (FileListResult, error) {
	root, target, cleanRel, err := h.sessionFileTarget(ctx, sessionID, rel)
	if err != nil {
		return FileListResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return FileListResult{}, err
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return FileListResult{}, err
	}

	out := FileListResult{Root: root, Path: cleanRel, Entries: make([]FileEntry, 0, len(entries))}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return FileListResult{}, err
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		kind := "file"
		if entry.IsDir() {
			kind = "dir"
		}
		childRel := filepath.ToSlash(filepath.Join(cleanRel, entry.Name()))
		if cleanRel == "." {
			childRel = entry.Name()
		}
		out.Entries = append(out.Entries, FileEntry{
			Name:    entry.Name(),
			Path:    childRel,
			Kind:    kind,
			Size:    info.Size(),
			ModTime: info.ModTime().UTC().Format(time.RFC3339Nano),
		})
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		if out.Entries[i].Kind != out.Entries[j].Kind {
			return out.Entries[i].Kind == "dir"
		}
		left := strings.ToLower(out.Entries[i].Name)
		right := strings.ToLower(out.Entries[j].Name)
		if left != right {
			return left < right
		}
		return out.Entries[i].Name < out.Entries[j].Name
	})
	return out, nil
}

// ReadFile returns a bounded read-only preview for one file under the session cwd.
func (h *Handler) ReadFile(ctx context.Context, sessionID, rel string) (FileReadResult, error) {
	_, target, cleanRel, err := h.sessionFileTarget(ctx, sessionID, rel)
	if err != nil {
		return FileReadResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return FileReadResult{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return FileReadResult{}, err
	}
	if info.IsDir() {
		return FileReadResult{}, fmt.Errorf("path is a directory: %s", cleanRel)
	}

	res := FileReadResult{Path: cleanRel, Size: info.Size()}
	if info.Size() > MaxFilePreviewBytes {
		res.TooLarge = true
		return res, nil
	}
	if err := ctx.Err(); err != nil {
		return FileReadResult{}, err
	}
	body, err := os.ReadFile(target)
	if err != nil {
		return FileReadResult{}, err
	}

	res.MIME = http.DetectContentType(body)
	if bytes.IndexByte(body, 0) >= 0 || !utf8.Valid(body) {
		res.Binary = true
		return res, nil
	}
	res.Content = string(body)
	return res, nil
}

func (h *Handler) sessionFileTarget(ctx context.Context, sessionID, rel string) (root, target, cleanRel string, err error) {
	if h == nil || h.Backend == nil {
		return "", "", "", errors.New("backend unavailable")
	}
	sess, _, err := h.Backend.GetSession(ctx, sessionID)
	if err != nil {
		return "", "", "", err
	}
	if sess.WorkingDir == "" {
		return "", "", "", errors.New("session working directory unknown")
	}

	cleanRel = filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(cleanRel) {
		return "", "", "", errPathOutsideRoot
	}
	if cleanRel == "" {
		cleanRel = "."
	}

	root, err = filepath.Abs(sess.WorkingDir)
	if err != nil {
		return "", "", "", err
	}
	target, err = filepath.Abs(filepath.Join(root, cleanRel))
	if err != nil {
		return "", "", "", err
	}
	if !pathWithinRoot(root, target) {
		return "", "", "", errPathOutsideRoot
	}

	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", "", err
	}
	realTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", "", "", err
	}
	if !pathWithinRoot(realRoot, realTarget) {
		return "", "", "", errPathOutsideRoot
	}
	return root, target, filepath.ToSlash(cleanRel), nil
}

func pathWithinRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel))
}
