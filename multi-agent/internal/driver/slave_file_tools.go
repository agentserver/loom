package driver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"

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
