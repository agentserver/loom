package driver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/agentserver/agentserver/pkg/agentsdk"
)

const defaultInlineMaxBytes = 4096

// fileCacheRoot returns the directory where read_slave_file caches blobs.
// Mirrors resolveAuditPath's directory choice so all driver-local files share a parent.
func (t *Tools) fileCacheRoot() (string, error) {
	dir := ""
	if t.cfg != nil {
		dir = t.cfg.DriverDefaults.AuditLogDir
	}
	if dir == "" {
		u, err := user.Current()
		if err != nil {
			return "", err
		}
		shortID := ""
		if t.cfg != nil {
			shortID = t.cfg.Credentials.ShortID
		}
		dir = filepath.Join(u.HomeDir, ".cache", "multi-agent", shortID)
	}
	root := filepath.Join(dir, "file-cache")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return root, nil
}

// cacheBytes writes payload to <root>/<sha256> atomically and registers it in FileRegistry.
// Returns (sha, abs path).
func (t *Tools) cacheBytes(payload []byte) (string, string, error) {
	sum := sha256.Sum256(payload)
	sha := hex.EncodeToString(sum[:])
	root, err := t.fileCacheRoot()
	if err != nil {
		return "", "", err
	}
	abs := filepath.Join(root, sha)
	if _, statErr := os.Stat(abs); os.IsNotExist(statErr) {
		tmp, err := os.CreateTemp(root, "incoming-*")
		if err != nil {
			return "", "", err
		}
		if _, err := tmp.Write(payload); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return "", "", err
		}
		tmp.Close()
		if err := os.Rename(tmp.Name(), abs); err != nil {
			os.Remove(tmp.Name())
			return "", "", err
		}
	}
	if _, _, _, err := t.reg.RegisterFile(abs); err != nil {
		return "", "", err
	}
	return sha, abs, nil
}

type readSlaveFileTool struct{ t *Tools }

func (r *readSlaveFileTool) Name() string { return "read_slave_file" }
func (r *readSlaveFileTool) Description() string {
	return "Read a file from a selected slave through the file skill. Bytes are cached in the driver's blob store; the LLM receives a handle plus inline content only if small and utf-8."
}
func (r *readSlaveFileTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
        "target_agent_id":{"type":"string"},
        "target_display_name":{"type":"string"},
        "path":{"type":"string"},
        "offset":{"type":"integer","minimum":0},
        "length":{"type":"integer","minimum":1},
        "encoding":{"type":"string","enum":["utf-8","base64"]},
        "inline_max_bytes":{"type":"integer","minimum":0}
    },"required":["path"],"additionalProperties":false}`)
}
func (r *readSlaveFileTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		TargetAgentID     string `json:"target_agent_id"`
		TargetDisplayName string `json:"target_display_name"`
		Path              string `json:"path"`
		Offset            int64  `json:"offset,omitempty"`
		Length            int64  `json:"length,omitempty"`
		Encoding          string `json:"encoding,omitempty"`
		InlineMaxBytes    *int64 `json:"inline_max_bytes,omitempty"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
	}
	if args.Path == "" {
		return nil, &MCPToolError{Message: "path is required"}
	}
	card, err := r.t.resolveAvailableAgent(ctx, args.TargetAgentID, args.TargetDisplayName)
	if err != nil {
		return nil, err
	}
	if !hasSkill(card, "file") {
		return nil, &MCPToolError{Message: "target " + card.DisplayName + " does not advertise file"}
	}
	prompt := map[string]interface{}{"op": "read", "path": args.Path}
	if args.Offset > 0 {
		prompt["offset"] = args.Offset
	}
	if args.Length > 0 {
		prompt["length"] = args.Length
	}
	if args.Encoding != "" {
		prompt["encoding"] = args.Encoding
	}
	pb, _ := json.Marshal(prompt)
	resp, err := r.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID: card.AgentID, Skill: "file", Prompt: string(pb),
	})
	if err != nil {
		return nil, &MCPToolError{Message: "delegate file read: " + err.Error()}
	}
	waitOut, err := r.t.waitDelegatedTask(ctx, resp.TaskID, 0)
	if err != nil {
		return nil, err
	}
	// waitDelegatedTask wraps the slave summary as a JSON-encoded string in "output".
	var wrap struct {
		TaskID string          `json:"task_id"`
		Status string          `json:"status"`
		Output string          `json:"output"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(waitOut, &wrap); err != nil {
		return nil, &MCPToolError{Message: "parse task output: " + err.Error()}
	}
	var slaveRes struct {
		Path     string `json:"path"`
		Bytes    int64  `json:"bytes"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
		EOF      bool   `json:"eof"`
	}
	if err := json.Unmarshal([]byte(wrap.Output), &slaveRes); err != nil {
		return nil, &MCPToolError{Message: "parse slave file result: " + err.Error()}
	}

	// Decode payload, cache, register.
	var payload []byte
	switch slaveRes.Encoding {
	case "utf-8":
		payload = []byte(slaveRes.Content)
	case "base64":
		payload, err = base64.StdEncoding.DecodeString(slaveRes.Content)
		if err != nil {
			return nil, &MCPToolError{Message: "slave returned invalid base64: " + err.Error()}
		}
	default:
		return nil, &MCPToolError{Message: "slave returned unknown encoding: " + slaveRes.Encoding}
	}
	sha, cachePath, err := r.t.cacheBytes(payload)
	if err != nil {
		return nil, &MCPToolError{Message: "cache slave bytes: " + err.Error()}
	}
	shortID := cardShortID(card)
	r.t.audit.Log(AuditEvent{
		Event: "register_read", Path: slaveRes.Path, SHA256: sha,
		Bytes: int64(len(payload)), PeerShortID: shortID, TaskID: resp.TaskID,
	})

	inlineMax := int64(defaultInlineMaxBytes)
	if args.InlineMaxBytes != nil {
		inlineMax = *args.InlineMaxBytes
	}
	out := map[string]interface{}{
		"task_id":             resp.TaskID,
		"target_display_name": card.DisplayName,
		"slave_path":          slaveRes.Path,
		"size":                slaveRes.Bytes,
		"encoding":            slaveRes.Encoding,
		"sha256":              sha,
		"blob_handle":         "sha256:" + sha,
		"cache_path":          cachePath,
		"eof":                 slaveRes.EOF,
	}
	if slaveRes.Encoding == "utf-8" && slaveRes.Bytes <= inlineMax {
		out["content"] = slaveRes.Content
	}
	return json.Marshal(out)
}

type writeSlaveFileTool struct{ t *Tools }

func (w *writeSlaveFileTool) Name() string { return "write_slave_file" }
func (w *writeSlaveFileTool) Description() string {
	return "Write bytes to a path on a selected slave through the file skill. Exactly one of content / source_blob / source_path must be set."
}
func (w *writeSlaveFileTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
        "target_agent_id":{"type":"string"},
        "target_display_name":{"type":"string"},
        "path":{"type":"string"},
        "content":{"type":"string"},
        "source_blob":{"type":"string"},
        "source_path":{"type":"string"},
        "encoding":{"type":"string","enum":["utf-8","base64"]},
        "mode":{"type":"string","enum":["overwrite","append","create_new","patch"]},
        "mkdir":{"type":"boolean"},
        "offset":{"type":"integer","minimum":0}
    },"required":["path"],"additionalProperties":false}`)
}
func (w *writeSlaveFileTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		TargetAgentID     string  `json:"target_agent_id"`
		TargetDisplayName string  `json:"target_display_name"`
		Path              string  `json:"path"`
		Content           *string `json:"content,omitempty"`
		SourceBlob        string  `json:"source_blob,omitempty"`
		SourcePath        string  `json:"source_path,omitempty"`
		Encoding          string  `json:"encoding,omitempty"`
		Mode              string  `json:"mode,omitempty"`
		Mkdir             bool    `json:"mkdir,omitempty"`
		Offset            *int64  `json:"offset,omitempty"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
	}
	if args.Path == "" {
		return nil, &MCPToolError{Message: "path is required"}
	}
	sources := 0
	if args.Content != nil {
		sources++
	}
	if args.SourceBlob != "" {
		sources++
	}
	if args.SourcePath != "" {
		sources++
	}
	if sources != 1 {
		return nil, &MCPToolError{Message: "exactly one of content / source_blob / source_path must be set"}
	}
	mode := args.Mode
	if mode == "" {
		mode = "overwrite"
	}
	if mode != "patch" && args.Offset != nil && *args.Offset != 0 {
		return nil, &MCPToolError{Message: "offset is only valid with mode=patch"}
	}
	card, err := w.t.resolveAvailableAgent(ctx, args.TargetAgentID, args.TargetDisplayName)
	if err != nil {
		return nil, err
	}
	if !hasSkill(card, "file") {
		return nil, &MCPToolError{Message: "target " + card.DisplayName + " does not advertise file"}
	}

	var (
		slaveContent  string
		slaveEncoding string
		sourceLabel   string
	)
	switch {
	case args.Content != nil:
		if int64(len(*args.Content)) > defaultInlineMaxBytes {
			return nil, &MCPToolError{Message: fmt.Sprintf(
				"inline content exceeds inline_max_bytes (%d > %d); use source_blob or source_path",
				len(*args.Content), defaultInlineMaxBytes)}
		}
		slaveContent = *args.Content
		slaveEncoding = args.Encoding
		if slaveEncoding == "" {
			slaveEncoding = "utf-8"
		}
		sourceLabel = "content"
	case args.SourceBlob != "":
		sha := strings.TrimPrefix(args.SourceBlob, "sha256:")
		path, ok := w.t.reg.LookupBlob(sha)
		if !ok {
			return nil, &MCPToolError{Message: "source_blob " + args.SourceBlob + " not in driver FileRegistry"}
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, &MCPToolError{Message: "read source_blob: " + err.Error()}
		}
		slaveContent = base64.StdEncoding.EncodeToString(body)
		slaveEncoding = "base64"
		sourceLabel = "source_blob:" + args.SourceBlob
	case args.SourcePath != "":
		body, err := os.ReadFile(args.SourcePath)
		if err != nil {
			return nil, &MCPToolError{Message: "read source_path: " + err.Error()}
		}
		if _, _, _, err := w.t.reg.RegisterFile(args.SourcePath); err != nil {
			return nil, &MCPToolError{Message: "register source_path: " + err.Error()}
		}
		slaveContent = base64.StdEncoding.EncodeToString(body)
		slaveEncoding = "base64"
		sourceLabel = "source_path:" + args.SourcePath
	}

	prompt := map[string]interface{}{
		"op":       "write",
		"path":     args.Path,
		"content":  slaveContent,
		"encoding": slaveEncoding,
		"mode":     mode,
	}
	if args.Mkdir {
		prompt["mkdir"] = true
	}
	if mode == "patch" && args.Offset != nil {
		prompt["offset"] = *args.Offset
	}
	pb, _ := json.Marshal(prompt)
	resp, err := w.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID: card.AgentID, Skill: "file", Prompt: string(pb),
	})
	if err != nil {
		return nil, &MCPToolError{Message: "delegate file write: " + err.Error()}
	}
	waitOut, err := w.t.waitDelegatedTask(ctx, resp.TaskID, 0)
	if err != nil {
		return nil, err
	}
	var wrap struct {
		Output string `json:"output"`
	}
	json.Unmarshal(waitOut, &wrap)
	var slaveRes struct {
		Path         string `json:"path"`
		BytesWritten int64  `json:"bytes_written"`
		Mode         string `json:"mode"`
		Offset       *int64 `json:"offset,omitempty"`
	}
	json.Unmarshal([]byte(wrap.Output), &slaveRes)

	out := map[string]interface{}{
		"task_id":             resp.TaskID,
		"target_display_name": card.DisplayName,
		"slave_path":          slaveRes.Path,
		"bytes_written":       slaveRes.BytesWritten,
		"mode":                slaveRes.Mode,
		"source":              sourceLabel,
	}
	if slaveRes.Offset != nil {
		out["offset"] = *slaveRes.Offset
	}
	return json.Marshal(out)
}
