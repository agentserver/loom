package commander

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	errFileRequest     = errors.New("commander: invalid file request")
	errPathOutsideRoot = errors.New("path outside session root")
)

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
		return FileListResult{}, fileRequestError(err)
	}

	out := FileListResult{Root: root, Path: cleanRel, Entries: make([]FileEntry, 0, len(entries))}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return FileListResult{}, err
		}
		info, err := entry.Info()
		if err != nil {
			return FileListResult{}, fileRequestError(err)
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
		return FileReadResult{}, fileRequestError(err)
	}
	if err := rejectNonPreviewFile(info, cleanRel); err != nil {
		return FileReadResult{}, fileRequestError(err)
	}

	f, err := os.Open(target)
	if err != nil {
		return FileReadResult{}, fileRequestError(err)
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return FileReadResult{}, fileRequestError(err)
	}
	if err := rejectNonPreviewFile(info, cleanRel); err != nil {
		return FileReadResult{}, fileRequestError(err)
	}
	res := FileReadResult{Path: cleanRel, Size: info.Size()}
	if err := ctx.Err(); err != nil {
		return FileReadResult{}, err
	}
	body, err := io.ReadAll(io.LimitReader(f, MaxFilePreviewBytes+1))
	if err != nil {
		return FileReadResult{}, fileRequestError(err)
	}
	observed := int64(len(body))
	if observed > res.Size {
		res.Size = observed
	}
	if observed > MaxFilePreviewBytes || res.Size > MaxFilePreviewBytes {
		if res.Size < MaxFilePreviewBytes+1 {
			res.Size = MaxFilePreviewBytes + 1
		}
		res.TooLarge = true
		return res, nil
	}

	res.MIME = http.DetectContentType(body)
	if bytes.IndexByte(body, 0) >= 0 || !utf8.Valid(body) {
		res.Binary = true
		return res, nil
	}
	res.Content = string(body)
	return res, nil
}

func rejectNonPreviewFile(info os.FileInfo, cleanRel string) error {
	if info.IsDir() {
		return fmt.Errorf("path is a directory: %s", cleanRel)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("path is not a regular file: %s", cleanRel)
	}
	return nil
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
		return "", "", "", fileRequestError(errors.New("session working directory unknown"))
	}

	cleanRel = filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(cleanRel) {
		return "", "", "", fileRequestError(errPathOutsideRoot)
	}
	if cleanRel == "" {
		cleanRel = "."
	}

	root, err = filepath.Abs(sess.WorkingDir)
	if err != nil {
		return "", "", "", fileRequestError(err)
	}
	target, err = filepath.Abs(filepath.Join(root, cleanRel))
	if err != nil {
		return "", "", "", fileRequestError(err)
	}
	if !pathWithinRoot(root, target) {
		return "", "", "", fileRequestError(errPathOutsideRoot)
	}

	// EvalSymlinks gives static containment before opening. The trust boundary
	// here is the session working directory: callers are owner-scoped, but a
	// process that can concurrently write this root can still win a TOCTOU race.
	// Closing that would require descriptor/openat-based containment, which is
	// outside this cross-platform helper's current scope.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", "", fileRequestError(err)
	}
	realTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", "", "", fileRequestError(err)
	}
	if !pathWithinRoot(realRoot, realTarget) {
		return "", "", "", fileRequestError(errPathOutsideRoot)
	}
	return root, target, filepath.ToSlash(cleanRel), nil
}

func fileRequestError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errFileRequest) {
		return err
	}
	return fileRequestWrap{err: err}
}

type fileRequestWrap struct {
	err error
}

func (e fileRequestWrap) Error() string {
	return e.err.Error()
}

func (e fileRequestWrap) Unwrap() error {
	return e.err
}

func (e fileRequestWrap) Is(target error) bool {
	return target == errFileRequest
}

func pathWithinRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel))
}
