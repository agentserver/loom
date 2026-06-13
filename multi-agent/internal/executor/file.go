package executor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"
)

const fileMaxReadBytes = 8 * 1024 * 1024 // 8 MiB hard cap per read.

// resolveExistingPrefix returns filepath.EvalSymlinks(p) if p exists.
// If p doesn't exist, it evaluates the longest existing prefix and
// rejoins the non-existent tail unchanged. This lets jail checks work
// even when the caller is about to *create* a file at p.
func resolveExistingPrefix(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	cur := abs
	var tail []string
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			if len(tail) == 0 {
				return real, nil
			}
			parts := append([]string{real}, reverse(tail)...)
			return filepath.Join(parts...), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent, leaf := filepath.Split(cur)
		parent = filepath.Clean(parent)
		if parent == cur {
			return abs, nil
		}
		tail = append(tail, leaf)
		cur = parent
	}
}

func reverse(s []string) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[len(s)-1-i] = v
	}
	return out
}

type FileConfig struct {
	WorkDir string
}

type FileExecutor struct{ cfg FileConfig }

func NewFileExecutor(cfg FileConfig) *FileExecutor { return &FileExecutor{cfg: cfg} }

type fileRequest struct {
	Op       string `json:"op"`
	Path     string `json:"path"`
	Offset   int64  `json:"offset,omitempty"`
	Length   int64  `json:"length,omitempty"`
	Encoding string `json:"encoding,omitempty"`
	Content  string `json:"content,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Mkdir    bool   `json:"mkdir,omitempty"`
}

type FileReadResult struct {
	Path     string `json:"path"`
	Bytes    int64  `json:"bytes"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
	EOF      bool   `json:"eof"`
}

type FileWriteResult struct {
	Path         string `json:"path"`
	BytesWritten int64  `json:"bytes_written"`
	Mode         string `json:"mode"`
	Offset       *int64 `json:"offset,omitempty"`
}

type FileStatResult struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Size   int64  `json:"size,omitempty"`
	Mode   string `json:"mode,omitempty"`
	IsDir  bool   `json:"is_dir,omitempty"`
	MTime  string `json:"mtime,omitempty"`
}

func (e *FileExecutor) Run(ctx context.Context, t Task, sink Sink) (Result, error) {
	defer sink.Close()
	var req fileRequest
	if err := json.Unmarshal([]byte(t.Prompt), &req); err != nil {
		return Result{}, fmt.Errorf("file prompt must be JSON: %w", err)
	}
	if req.Path == "" {
		return Result{}, errors.New("file path is required")
	}
	abs := e.resolvePath(req.Path)
	switch req.Op {
	case "read":
		return e.doRead(req, abs, sink)
	case "write":
		return e.doWrite(req, abs, sink)
	case "stat":
		return e.doStat(req, abs, sink)
	default:
		return Result{}, fmt.Errorf("unknown file op %q", req.Op)
	}
}

func (e *FileExecutor) resolvePath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	base := e.cfg.WorkDir
	if base == "" {
		base, _ = os.Getwd()
	}
	return filepath.Join(base, p)
}

func (e *FileExecutor) doRead(req fileRequest, abs string, sink Sink) (Result, error) {
	enc := req.Encoding
	if enc == "" {
		enc = "utf-8"
	}
	if enc != "utf-8" && enc != "base64" {
		return Result{}, fmt.Errorf("encoding must be utf-8 or base64, got %q", enc)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Result{}, fmt.Errorf("stat %s: %w", abs, err)
	}
	if info.IsDir() {
		return Result{}, fmt.Errorf("read target is a directory: %s", abs)
	}
	size := info.Size()
	if req.Offset < 0 {
		return Result{}, fmt.Errorf("offset must be >= 0")
	}
	remaining := size - req.Offset
	if remaining < 0 {
		remaining = 0
	}
	want := remaining
	if req.Length > 0 && req.Length < want {
		want = req.Length
	}
	if want > fileMaxReadBytes {
		return Result{}, fmt.Errorf("read of %d bytes exceeds %d cap; chunk via offset/length", want, fileMaxReadBytes)
	}
	buf := make([]byte, want)
	if want > 0 {
		f, err := os.Open(abs)
		if err != nil {
			return Result{}, err
		}
		n, err := f.ReadAt(buf, req.Offset)
		f.Close()
		// ReadAt may return io.EOF when fewer than len(buf) bytes are available;
		// a short read is fine, but a zero-byte read with a real error is not.
		if err != nil && n == 0 {
			return Result{}, err
		}
		buf = buf[:n]
	}
	content := ""
	switch enc {
	case "utf-8":
		if !utf8.Valid(buf) {
			return Result{}, fmt.Errorf("content is not valid utf-8; retry with encoding=base64")
		}
		content = string(buf)
	case "base64":
		content = base64.StdEncoding.EncodeToString(buf)
	}
	result := FileReadResult{
		Path:     abs,
		Bytes:    int64(len(buf)),
		Encoding: enc,
		Content:  content,
		EOF:      req.Offset+int64(len(buf)) >= size,
	}
	body, _ := json.Marshal(result)
	sink.Write("chunk", string(body))
	return Result{Summary: string(body)}, nil
}

func (e *FileExecutor) doWrite(req fileRequest, abs string, sink Sink) (Result, error) {
	enc := req.Encoding
	if enc == "" {
		enc = "utf-8"
	}
	if enc != "utf-8" && enc != "base64" {
		return Result{}, fmt.Errorf("encoding must be utf-8 or base64, got %q", enc)
	}
	mode := req.Mode
	if mode == "" {
		mode = "overwrite"
	}
	switch mode {
	case "overwrite", "append", "create_new", "patch":
	default:
		return Result{}, fmt.Errorf("mode must be overwrite|append|create_new|patch, got %q", mode)
	}
	if mode != "patch" && req.Offset != 0 {
		return Result{}, fmt.Errorf("offset is only valid with mode=patch")
	}
	if req.Offset < 0 {
		return Result{}, fmt.Errorf("offset must be >= 0")
	}

	var bytesPayload []byte
	switch enc {
	case "utf-8":
		bytesPayload = []byte(req.Content)
	case "base64":
		decoded, err := base64.StdEncoding.DecodeString(req.Content)
		if err != nil {
			return Result{}, fmt.Errorf("base64 decode: %w", err)
		}
		bytesPayload = decoded
	}

	if req.Mkdir {
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return Result{}, err
		}
	}

	var (
		f   *os.File
		err error
	)
	switch mode {
	case "overwrite":
		f, err = os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	case "append":
		f, err = os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	case "create_new":
		f, err = os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	case "patch":
		f, err = os.OpenFile(abs, os.O_WRONLY|os.O_CREATE, 0o644)
	}
	if err != nil {
		return Result{}, err
	}
	defer f.Close()

	var n int
	if mode == "patch" {
		n, err = f.WriteAt(bytesPayload, req.Offset)
	} else {
		n, err = f.Write(bytesPayload)
	}
	if err != nil {
		return Result{}, err
	}
	result := FileWriteResult{
		Path:         abs,
		BytesWritten: int64(n),
		Mode:         mode,
	}
	if mode == "patch" {
		off := req.Offset
		result.Offset = &off
	}
	body, _ := json.Marshal(result)
	sink.Write("chunk", string(body))
	return Result{Summary: string(body)}, nil
}

func (e *FileExecutor) doStat(req fileRequest, abs string, sink Sink) (Result, error) {
	info, err := os.Stat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		result := FileStatResult{Path: abs, Exists: false}
		body, _ := json.Marshal(result)
		sink.Write("chunk", string(body))
		return Result{Summary: string(body)}, nil
	}
	if err != nil {
		return Result{}, err
	}
	result := FileStatResult{
		Path:   abs,
		Exists: true,
		Size:   info.Size(),
		Mode:   fmt.Sprintf("%#o", info.Mode().Perm()),
		IsDir:  info.IsDir(),
		MTime:  info.ModTime().UTC().Format(time.RFC3339Nano),
	}
	body, _ := json.Marshal(result)
	sink.Write("chunk", string(body))
	return Result{Summary: string(body)}, nil
}
