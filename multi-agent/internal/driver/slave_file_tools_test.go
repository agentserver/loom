package driver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/stretchr/testify/require"
)

func TestReadSlaveFile_CachesAndRegistersAndReturnsHandle(t *testing.T) {
	var delegated agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-a", DisplayName: "slave-a", Status: "available",
				Card: json.RawMessage(`{"skills":["file"],"short_id":"sa"}`),
			}}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			// slave returns hello\n (6 bytes, utf-8)
			return &agentsdk.TaskInfo{
				TaskID: "task-1", Status: "completed",
				Result: json.RawMessage(`"{\"path\":\"/abs/in.txt\",\"bytes\":6,\"encoding\":\"utf-8\",\"content\":\"hello\\n\",\"eof\":true}"`),
			}, nil
		},
	}
	tools := newTestTools(t, sdk)
	tool := toolByName(t, tools, "read_slave_file")
	raw, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-a","path":"in.txt"}`))
	require.NoError(t, err)
	require.Equal(t, "file", delegated.Skill)
	require.JSONEq(t, `{"op":"read","path":"in.txt"}`, delegated.Prompt)

	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &out))
	require.Equal(t, "/abs/in.txt", out["slave_path"])
	require.EqualValues(t, 6, out["size"])
	require.Equal(t, "hello\n", out["content"]) // inline (≤ 4 KiB, utf-8)
	sha, _ := out["sha256"].(string)
	require.NotEmpty(t, sha)
	require.Equal(t, "sha256:"+sha, out["blob_handle"])

	// Cache file exists with that sha as filename.
	cachePath, _ := out["cache_path"].(string)
	require.FileExists(t, cachePath)
	body, _ := os.ReadFile(cachePath)
	require.Equal(t, "hello\n", string(body))

	// FileRegistry has it.
	path, ok := tools.reg.LookupBlob(sha)
	require.True(t, ok)
	require.Equal(t, cachePath, path)
}

func TestReadSlaveFile_OmitsContentWhenLargerThanInlineCap(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-a", DisplayName: "slave-a", Status: "available",
				Card: json.RawMessage(`{"skills":["file"],"short_id":"sa"}`),
			}}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{
				TaskID: "task-1", Status: "completed",
				Result: json.RawMessage(`"{\"path\":\"/abs/big.txt\",\"bytes\":10,\"encoding\":\"utf-8\",\"content\":\"0123456789\",\"eof\":true}"`),
			}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "read_slave_file")
	raw, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-a","path":"big.txt","inline_max_bytes":4}`))
	require.NoError(t, err)
	var out map[string]interface{}
	json.Unmarshal(raw, &out)
	_, hasContent := out["content"]
	require.False(t, hasContent, "content must be omitted when size > inline_max_bytes")
}

func TestReadSlaveFile_OmitsContentForBase64EvenWhenSmall(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-a", DisplayName: "slave-a", Status: "available",
				Card: json.RawMessage(`{"skills":["file"],"short_id":"sa"}`),
			}}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			// 3-byte binary, base64 = "AP9C"
			return &agentsdk.TaskInfo{
				TaskID: "task-1", Status: "completed",
				Result: json.RawMessage(`"{\"path\":\"/abs/bin\",\"bytes\":3,\"encoding\":\"base64\",\"content\":\"AP9C\",\"eof\":true}"`),
			}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "read_slave_file")
	raw, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-a","path":"bin","encoding":"base64"}`))
	require.NoError(t, err)
	var out map[string]interface{}
	json.Unmarshal(raw, &out)
	_, hasContent := out["content"]
	require.False(t, hasContent, "base64 reads never inline content")
}

func TestReadSlaveFile_RejectsMissingFileSkill(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-a", DisplayName: "slave-a", Status: "available",
				Card: json.RawMessage(`{"skills":["chat"]}`),
			}}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "read_slave_file")
	_, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-a","path":"in.txt"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not advertise file")
}

func TestReadSlaveFile_AuditsRegisterReadWithSlaveShortID(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-a", DisplayName: "slave-a", Status: "available",
				Card: json.RawMessage(`{"skills":["file"],"short_id":"sa"}`),
			}}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{
				TaskID: "task-1", Status: "completed",
				Result: json.RawMessage(`"{\"path\":\"/abs/in.txt\",\"bytes\":2,\"encoding\":\"utf-8\",\"content\":\"hi\",\"eof\":true}"`),
			}, nil
		},
	}
	tools := newTestTools(t, sdk)
	tool := toolByName(t, tools, "read_slave_file")
	_, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-a","path":"in.txt"}`))
	require.NoError(t, err)

	dir := tools.cfg.DriverDefaults.AuditLogDir
	require.NotEmpty(t, dir, "test helper must set AuditLogDir")
	body, err := os.ReadFile(filepath.Join(dir, "audit.log"))
	require.NoError(t, err)
	require.Contains(t, string(body), `"event":"register_read"`)
	require.Contains(t, string(body), `"peer_short_id":"sa"`)
}

func newAvailableFileSlaveSDK(t *testing.T, slaveResult string, captured *agentsdk.DelegateTaskRequest) *fakeSDK {
	return &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-b", DisplayName: "slave-b", Status: "available",
				Card: json.RawMessage(`{"skills":["file"],"short_id":"sb"}`),
			}}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			*captured = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-w1"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{
				TaskID: "task-w1", Status: "completed",
				Result: json.RawMessage(`"` + jsonEscape(slaveResult) + `"`),
			}, nil
		},
	}
}

func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

func TestWriteSlaveFile_InlineContent(t *testing.T) {
	var captured agentsdk.DelegateTaskRequest
	sdk := newAvailableFileSlaveSDK(t,
		`{"path":"/abs/out.txt","bytes_written":5,"mode":"overwrite"}`, &captured)
	tool := toolByName(t, newTestTools(t, sdk), "write_slave_file")
	raw, err := tool.Call(context.Background(),
		json.RawMessage(`{"target_display_name":"slave-b","path":"out.txt","content":"hello"}`))
	require.NoError(t, err)
	require.Equal(t, "file", captured.Skill)
	require.JSONEq(t,
		`{"op":"write","path":"out.txt","content":"hello","encoding":"utf-8","mode":"overwrite"}`,
		captured.Prompt)
	var out map[string]interface{}
	json.Unmarshal(raw, &out)
	require.Equal(t, "content", out["source"])
	require.EqualValues(t, 5, out["bytes_written"])
}

func TestWriteSlaveFile_RejectsZeroOrMultipleSources(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-b", DisplayName: "slave-b", Status: "available",
				Card: json.RawMessage(`{"skills":["file"]}`),
			}}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "write_slave_file")
	// zero
	_, err := tool.Call(context.Background(),
		json.RawMessage(`{"target_display_name":"slave-b","path":"x"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "exactly one")
	// multiple
	_, err = tool.Call(context.Background(),
		json.RawMessage(`{"target_display_name":"slave-b","path":"x","content":"a","source_path":"/p"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "exactly one")
}

func TestWriteSlaveFile_RejectsOffsetWithoutPatch(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-b", DisplayName: "slave-b", Status: "available",
				Card: json.RawMessage(`{"skills":["file"]}`),
			}}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "write_slave_file")
	_, err := tool.Call(context.Background(), json.RawMessage(
		`{"target_display_name":"slave-b","path":"x","content":"a","mode":"overwrite","offset":5}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "offset")
}

func TestWriteSlaveFile_RejectsLargeInlineContent(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-b", DisplayName: "slave-b", Status: "available",
				Card: json.RawMessage(`{"skills":["file"]}`),
			}}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "write_slave_file")
	big := strings.Repeat("a", 5000) // > 4 KiB default
	args, _ := json.Marshal(map[string]string{
		"target_display_name": "slave-b", "path": "x", "content": big,
	})
	_, err := tool.Call(context.Background(), args)
	require.Error(t, err)
	require.Contains(t, err.Error(), "inline_max_bytes")
}

func TestWriteSlaveFile_SourceBlobLooksUpAndSendsBase64(t *testing.T) {
	var captured agentsdk.DelegateTaskRequest
	sdk := newAvailableFileSlaveSDK(t,
		`{"path":"/abs/out.bin","bytes_written":3,"mode":"overwrite"}`, &captured)
	tools := newTestTools(t, sdk)
	// Seed a blob in the registry by writing a temp file and registering it.
	tmp := filepath.Join(t.TempDir(), "src")
	require.NoError(t, os.WriteFile(tmp, []byte{0x00, 0xff, 0x42}, 0o644))
	sha, _, _, err := tools.reg.RegisterFile(tmp)
	require.NoError(t, err)

	tool := toolByName(t, tools, "write_slave_file")
	raw, err := tool.Call(context.Background(), json.RawMessage(
		`{"target_display_name":"slave-b","path":"out.bin","source_blob":"sha256:`+sha+`"}`))
	require.NoError(t, err)

	// The forwarded prompt must use encoding=base64 regardless of what caller passed.
	var slavePrompt map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(captured.Prompt), &slavePrompt))
	require.Equal(t, "base64", slavePrompt["encoding"])
	decoded, _ := base64.StdEncoding.DecodeString(slavePrompt["content"].(string))
	require.Equal(t, []byte{0x00, 0xff, 0x42}, decoded)

	var out map[string]interface{}
	json.Unmarshal(raw, &out)
	require.Equal(t, "source_blob:sha256:"+sha, out["source"])
}

func TestWriteSlaveFile_SourceBlobUnknownSha(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-b", DisplayName: "slave-b", Status: "available",
				Card: json.RawMessage(`{"skills":["file"]}`),
			}}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "write_slave_file")
	_, err := tool.Call(context.Background(), json.RawMessage(
		`{"target_display_name":"slave-b","path":"x","source_blob":"sha256:deadbeef"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "source_blob")
}

func TestWriteSlaveFile_SourcePathRegistersAndUploads(t *testing.T) {
	var captured agentsdk.DelegateTaskRequest
	sdk := newAvailableFileSlaveSDK(t,
		`{"path":"/abs/out","bytes_written":4,"mode":"overwrite"}`, &captured)
	tools := newTestTools(t, sdk)
	// §1.4 #17: source_path must resolve inside DriverDefaults.WorkDir; the
	// helper test tools point WorkDir at AuditLogDir so place the temp file
	// there too.
	workDir := tools.cfg.DriverDefaults.WorkDir
	require.NotEmpty(t, workDir, "test helper must set WorkDir for source_path jail")
	src := filepath.Join(workDir, "src")
	require.NoError(t, os.WriteFile(src, []byte("ABCD"), 0o644))
	tool := toolByName(t, tools, "write_slave_file")
	args, _ := json.Marshal(map[string]string{
		"target_display_name": "slave-b", "path": "out", "source_path": src,
	})
	raw, err := tool.Call(context.Background(), args)
	require.NoError(t, err)
	var slavePrompt map[string]interface{}
	json.Unmarshal([]byte(captured.Prompt), &slavePrompt)
	require.Equal(t, "base64", slavePrompt["encoding"])
	decoded, _ := base64.StdEncoding.DecodeString(slavePrompt["content"].(string))
	require.Equal(t, []byte("ABCD"), decoded)

	var out map[string]interface{}
	json.Unmarshal(raw, &out)
	require.Equal(t, "source_path:"+src, out["source"])

	// Confirm the file was registered.
	sum := sha256.Sum256([]byte("ABCD"))
	_, ok := tools.reg.LookupBlob(hex.EncodeToString(sum[:]))
	require.True(t, ok)
}

// TestWriteSlaveFile_SourcePathRejectsOutsideJail pins §1.4 #17: a
// driver-local source_path that resolves outside WorkDir and any
// configured SourcePathReadRoots must be rejected before os.ReadFile.
func TestWriteSlaveFile_SourcePathRejectsOutsideJail(t *testing.T) {
	var delegated bool
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-b", DisplayName: "slave-b", Status: "available",
				Card: json.RawMessage(`{"skills":["file"],"short_id":"sb"}`),
			}}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = true
			return &agentsdk.DelegateTaskResponse{TaskID: "task-w1"}, nil
		},
	}
	tools := newTestTools(t, sdk)
	// "Sensitive" file lives in a SEPARATE tempdir (simulating /etc/shadow).
	outside := t.TempDir()
	sensitive := filepath.Join(outside, "shadow")
	require.NoError(t, os.WriteFile(sensitive, []byte("root:x:0:0"), 0o600))

	tool := toolByName(t, tools, "write_slave_file")
	args, _ := json.Marshal(map[string]string{
		"target_display_name": "slave-b", "path": "out", "source_path": sensitive,
	})
	_, err := tool.Call(context.Background(), args)
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "outside")
	require.False(t, delegated, "validation must pre-empt the upload")
}

// TestWriteSlaveFile_SourcePathAcceptsInsideJail covers the happy path
// when source_path resolves inside DriverDefaults.WorkDir.
func TestWriteSlaveFile_SourcePathAcceptsInsideJail(t *testing.T) {
	var captured agentsdk.DelegateTaskRequest
	sdk := newAvailableFileSlaveSDK(t,
		`{"path":"/abs/out","bytes_written":2,"mode":"overwrite"}`, &captured)
	tools := newTestTools(t, sdk)
	workDir := tools.cfg.DriverDefaults.WorkDir
	require.NotEmpty(t, workDir)
	src := filepath.Join(workDir, "inside-src")
	require.NoError(t, os.WriteFile(src, []byte("OK"), 0o644))

	tool := toolByName(t, tools, "write_slave_file")
	args, _ := json.Marshal(map[string]string{
		"target_display_name": "slave-b", "path": "out", "source_path": src,
	})
	_, err := tool.Call(context.Background(), args)
	require.NoError(t, err)
	var slavePrompt map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(captured.Prompt), &slavePrompt))
	decoded, _ := base64.StdEncoding.DecodeString(slavePrompt["content"].(string))
	require.Equal(t, []byte("OK"), decoded)
}

// TestWriteSlaveFile_SourcePathAcceptsExtraReadRoot pins the operator
// opt-in: SourcePathReadRoots adds extra dirs beyond WorkDir.
func TestWriteSlaveFile_SourcePathAcceptsExtraReadRoot(t *testing.T) {
	var captured agentsdk.DelegateTaskRequest
	sdk := newAvailableFileSlaveSDK(t,
		`{"path":"/abs/out","bytes_written":3,"mode":"overwrite"}`, &captured)
	tools := newTestTools(t, sdk)
	extraRoot := t.TempDir()
	tools.cfg.DriverDefaults.SourcePathReadRoots = []string{extraRoot}
	src := filepath.Join(extraRoot, "extra-src")
	require.NoError(t, os.WriteFile(src, []byte("EXT"), 0o644))

	tool := toolByName(t, tools, "write_slave_file")
	args, _ := json.Marshal(map[string]string{
		"target_display_name": "slave-b", "path": "out", "source_path": src,
	})
	_, err := tool.Call(context.Background(), args)
	require.NoError(t, err)
	var slavePrompt map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(captured.Prompt), &slavePrompt))
	decoded, _ := base64.StdEncoding.DecodeString(slavePrompt["content"].(string))
	require.Equal(t, []byte("EXT"), decoded)
}

func TestWriteSlaveFile_RejectsMissingFileSkill(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-b", DisplayName: "slave-b", Status: "available",
				Card: json.RawMessage(`{"skills":["chat"]}`),
			}}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "write_slave_file")
	_, err := tool.Call(context.Background(), json.RawMessage(
		`{"target_display_name":"slave-b","path":"x","content":"hi"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not advertise file")
}

func TestStatSlaveFile_PassesThroughResult(t *testing.T) {
	var captured agentsdk.DelegateTaskRequest
	sdk := newAvailableFileSlaveSDK(t,
		`{"path":"/abs/f","exists":true,"size":42,"mode":"0644","is_dir":false,"mtime":"2026-05-21T10:00:00Z"}`,
		&captured)
	tool := toolByName(t, newTestTools(t, sdk), "stat_slave_file")
	raw, err := tool.Call(context.Background(),
		json.RawMessage(`{"target_display_name":"slave-b","path":"f"}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"op":"stat","path":"f"}`, captured.Prompt)
	var out map[string]interface{}
	json.Unmarshal(raw, &out)
	require.Equal(t, true, out["exists"])
	require.EqualValues(t, 42, out["size"])
	require.Equal(t, "/abs/f", out["slave_path"])
	require.Equal(t, "task-w1", out["task_id"])
}

func TestStatSlaveFile_RejectsMissingFileSkill(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-b", DisplayName: "slave-b", Status: "available",
				Card: json.RawMessage(`{"skills":["chat"]}`),
			}}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "stat_slave_file")
	_, err := tool.Call(context.Background(),
		json.RawMessage(`{"target_display_name":"slave-b","path":"f"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not advertise file")
}
