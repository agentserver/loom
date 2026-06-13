# Slave 安全修复 (审查报告 §1.4) — 设计文档

- **日期**：2026-06-13
- **来源**：`docs/review-2026-06-13.md` §1.4（CRITICAL #14–#19）
- **范围**：slave 端 RCE / SSRF / 数据泄漏一线之隔的 5 个 bug：FileExecutor jail、HTTP MCP 资源耗尽、permissions patch 校验、driver `source_path` 二级 RCE 通道、`/files/put` 大小+TOCTOU+fsync
- **不在范围**：mcpmarket 签名（#19 → [issue #13](https://github.com/agentserver/loom/issues/13)，独立大工作）；master 路径（[[master_path_frozen]]）

## 目标不变量

修复后下列五条必须始终成立：

1. **slave FileExecutor 不能跳出 WorkDir** — 拒绝绝对路径不在 jail 内 + 拒绝 symlink 跳出 root。LLM 故意构造 `../../../etc/shadow` 写不出去。
2. **HTTP MCP server 不能阻塞/OOM slave** — 30s timeout + 16 MiB response cap。慢/恶意 server 失败 graceful，不挂住 dispatcher / 不耗尽内存。
3. **permissions patch 不能从 LLM 漏过去** — `patch.allow_add` / `deny_add` / `remove` / `ask` 字段名通过 schema 白名单；任意键被拒；`*` 通配符被拒；强制 refresh 成功才提交。
4. **driver 不能被 LLM 当"读任意本地文件 → 推 slave"的二级 RCE 通道** — `write_slave_file.source_path` 必须落在 driver `WorkDir` 或 cfg-declared `SourcePathReadRoots` 内。
5. **/files/put 不能被攻击者灌大文件 / TOCTOU 改 target / 断电丢 rename** — 1 GiB body cap + `O_CREATE|O_EXCL` tmp + parent dir fsync。

## 变更摘要

### Bug #14 — FileExecutor jail

**位置**：`internal/executor/file.go:83-92` (`resolvePath`) + `:102, 209` (read/write/stat 入口)

**问题**：`resolvePath` 对绝对路径直通；不解 symlink；不校验落在 WorkDir 内。slave 上任何 file 工具可读写 `/etc/shadow` / `/root/.ssh/authorized_keys`。

**修复**：

```go
// 新增 assertInJail (file.go 内部)，被 doRead/doWrite/doStat 调用前置：
//
//   abs := e.resolvePath(req.Path)
//   if err := e.assertInJail(abs); err != nil {
//       return Result{}, err
//   }
//
// resolvePath 不再无脑接受绝对路径；jail check 在外层。
//
// assertInJail：
//   - 用 filepath.EvalSymlinks 解；对不存在的 leaf，对 parent 解后拼回
//   - filepath.Rel(workDir, real)，若 .. 开头或 == ".." → reject
//   - WorkDir 在 FileExecutor 构造时缓存为绝对路径（cfg.WorkDir / os.Getwd）

func (e *FileExecutor) assertInJail(abs string) error {
    // EvalSymlinks fails on non-existent file; handle by evaluating
    // the longest existing prefix and rejoining the remaining tail.
    real, err := resolveExistingPrefix(abs)
    if err != nil {
        return fmt.Errorf("resolve %s: %w", abs, err)
    }
    rel, err := filepath.Rel(e.workDir, real)
    if err != nil {
        return fmt.Errorf("path %s outside jail %s: %w", abs, e.workDir, err)
    }
    if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
        return fmt.Errorf("path %s escapes jail %s (rel=%s)", abs, e.workDir, rel)
    }
    return nil
}
```

`FileExecutor` 增 `workDir string` (init 时 `filepath.Abs(cfg.WorkDir)` 或 fallback `os.Getwd()`)。如 cfg.WorkDir 为空 + Getwd 失败 → 构造失败（fail-fast 比 silently allow everything 安全）。

### Bug #15 — HTTP MCP 资源耗尽

**位置**：`internal/executor/mcp.go:38-43` (NewMCPExecutor) + ~270 (HTTP call site)

**问题**：`httpCli: &http.Client{}` 无 timeout；`io.ReadAll(resp.Body)` 无上限。

**修复**：

```go
const (
    mcpHTTPTimeout       = 30 * time.Second
    mcpMaxResponseBytes  = 16 * 1024 * 1024 // 16 MiB
)

// NewMCPExecutor:
httpCli: &http.Client{Timeout: mcpHTTPTimeout},

// 在调用 io.ReadAll 处:
body, err := io.ReadAll(io.LimitReader(resp.Body, mcpMaxResponseBytes+1))
if err != nil { ... }
if int64(len(body)) > mcpMaxResponseBytes {
    return result, fmt.Errorf("mcp HTTP response > %d bytes; refusing to buffer", mcpMaxResponseBytes)
}
```

可选 config knobs（如 cfg 有 MCP 段）：`cfg.MCP.HTTPTimeoutSec` + `cfg.MCP.MaxResponseBytes`，默认上述值。如 cfg 没有 MCP 段就直接 hardcode。

### Bug #16 — permissions patch 字段校验

**位置**：`cmd/slave-agent/permissions_executor.go:38-43` (Patch 调用前)

**问题**：`agentbackend.Patch` 把 `req.Patch` 透传 `store.Patch`，patch 内容零校验。LLM 可发 `{"allow_add":["*"],"remove":["*"]}` 或任意字段。

**修复**：在 `Patch` 调用前加 `validatePermissionsPatch(req.Patch)`：

```go
// validatePermissionsPatch enforces a strict allowlist of patch fields
// and rejects '*' wildcards. LLM-supplied patches that try to broaden
// access via unknown fields or wildcards are rejected before reaching
// the persistence layer.
func validatePermissionsPatch(p agentbackend.Patch) error {
    // (a) Known top-level fields only
    // (b) Each value list must be []string, no '*'
    // (c) No 'remove' entries that match '*'
    // Implementation depends on agentbackend.Patch struct shape — read it first.
}
```

**注意**：`agentbackend.Patch` 的具体字段需 implementer 读 `pkg/agentbackend/` 后确认。最常见 shape 大概是 `AllowAdd []string`, `DenyAdd []string`, `Remove []string`, `AskAdd []string`。Plan task 里给 implementer 实际 shape 让他实现。

`switch req.Op { case "patch": ... }` 路径加：

```go
case "patch":
    if err := validatePermissionsPatch(req.Patch); err != nil {
        return executor.Result{}, fmt.Errorf("permissions patch rejected: %w", err)
    }
    state, err = e.store.Patch(ctx, req.Patch)
    if err == nil && e.refresh != nil {
        err = e.refresh(ctx, "permission update")
    }
```

### Bug #17 — `source_path` 二级 RCE 通道

**位置**：`internal/driver/slave_file_tools.go:314-324`

**问题**：`write_slave_file` 的 `source_path` 是 driver 本地路径，`os.ReadFile(args.SourcePath)` 没任何校验。LLM 让 driver `read /etc/shadow` → base64 推到任何 slave。

**修复**：加 `assertSafeSourcePath` 调用：

```go
// 新 helper 在 internal/driver/safe_paths.go：
//
// AssertReadableSource enforces that source_path is either:
//   (a) under cfg.DriverDefaults.WorkDir (after Abs + EvalSymlinks)
//   (b) in cfg.DriverDefaults.SourcePathReadRoots (driver-configured allowlist)
//
// Default WorkDir is cwd (where driver-agent runs). Operators who need
// to ingest files from elsewhere add SourcePathReadRoots in YAML.

func AssertReadableSource(p string, workDir string, allowedRoots []string) error {
    abs, err := filepath.Abs(p)
    if err != nil {
        return err
    }
    realAbs, err := resolveExistingPrefix(abs)
    if err != nil {
        return err
    }
    for _, root := range append([]string{workDir}, allowedRoots...) {
        if root == "" {
            continue
        }
        rootAbs, err := filepath.Abs(root)
        if err != nil {
            continue
        }
        rel, err := filepath.Rel(rootAbs, realAbs)
        if err != nil {
            continue
        }
        if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
            return nil
        }
    }
    return fmt.Errorf("source_path %s outside driver workdir and allowed roots", p)
}
```

`slave_file_tools.go` 内：

```go
case args.SourcePath != "":
    if err := AssertReadableSource(args.SourcePath, w.t.cfg.DriverDefaults.WorkDir, w.t.cfg.DriverDefaults.SourcePathReadRoots); err != nil {
        return nil, &MCPToolError{Message: err.Error()}
    }
    body, err := os.ReadFile(args.SourcePath)
    // ... rest unchanged
```

`internal/driver/config.go` `DriverDefaults` struct 加：

```go
SourcePathReadRoots []string `yaml:"source_path_read_roots,omitempty"`
```

`WorkDir` field — 如果 DriverDefaults 已有就用，没有就用 cfg-level WorkDir 或 process cwd（implementer 读 config.go 后定）。

### Bug #18 — `/files/put` body cap + TOCTOU + parent fsync

**位置**：`internal/driver/files_handler.go:222-283`

**问题**：
- `io.Copy(mw, r.Body)` 无 size cap → 攻击者灌爆磁盘
- `OpenFile(tmpName, O_CREATE|O_WRONLY|O_TRUNC, 0o644)` — `O_TRUNC` 允许同名文件被截断；同时 TOCTOU 与 `os.Stat(target)` 之间
- `os.Rename` 后没 parent dir fsync — 断电后 rename 在 inode 上成功但 dirent 没刷

**修复**：

```go
const maxPutBytes = 1 << 30 // 1 GiB

func (h *FilesHandler) handlePut(w http.ResponseWriter, r *http.Request, peer string) {
    // ... existing token consume + parent check ...

    // 1) body cap
    body := http.MaxBytesReader(w, r.Body, maxPutBytes)
    defer body.Close()

    // 2) O_CREATE|O_EXCL prevents same-name tmp clobber race
    tmpName := fmt.Sprintf("%s.tmp.%s", target, randSuffix())
    out, err := os.OpenFile(tmpName, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
    if err != nil {
        // distinguish body-too-large from real errors
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }

    hasher := sha256.New()
    mw := io.MultiWriter(out, hasher)
    written, copyErr := io.Copy(mw, body)
    if copyErr != nil {
        out.Close()
        os.Remove(tmpName)
        // MaxBytesReader returns *http.MaxBytesError on overflow
        var maxErr *http.MaxBytesError
        if errors.As(copyErr, &maxErr) {
            http.Error(w, fmt.Sprintf("body exceeds %d bytes", maxPutBytes), http.StatusRequestEntityTooLarge)
            return
        }
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

    // 3) parent fsync so the new dirent survives crash
    if parentFd, err := os.Open(parent); err == nil {
        _ = parentFd.Sync() // best-effort; on filesystems where Sync isn't supported this is OK to ignore
        parentFd.Close()
    }

    // ... existing audit + RecordWritten ...
}
```

`tmp` 文件模式从 `0o644` 改 `0o600` 顺手收紧（tmp 不该被 world-readable）。

## 测试策略

| # | 测试 | 包 | 覆盖 |
|---|---|---|---|
| 1 | `TestFileExecutor_RejectsAbsolutePathOutsideJail` | executor | #14 absolute |
| 2 | `TestFileExecutor_RejectsSymlinkLeapingOutOfJail` | executor | #14 symlink |
| 3 | `TestFileExecutor_AcceptsRelativeInsideJail` | executor | #14 happy path |
| 4 | `TestFileExecutor_AcceptsAbsoluteInsideJail` | executor | #14 happy path absolute |
| 5 | `TestMCPExecutor_HTTPTimeoutFiresIn30s` | executor | #15 timeout |
| 6 | `TestMCPExecutor_HTTPResponseSizeCapEnforced` | executor | #15 size cap |
| 7 | `TestPermissionsPatch_RejectsUnknownField` | slave-agent | #16 unknown field |
| 8 | `TestPermissionsPatch_RejectsStarWildcard` | slave-agent | #16 wildcard |
| 9 | `TestPermissionsPatch_AcceptsKnownPatch` | slave-agent | #16 happy path |
| 10 | `TestWriteSlaveFile_SourcePathOutsideJailRejected` | driver | #17 jail enforcement |
| 11 | `TestWriteSlaveFile_SourcePathInsideJailAccepted` | driver | #17 happy path |
| 12 | `TestWriteSlaveFile_SourcePathInExtraAllowedRoot` | driver | #17 SourcePathReadRoots |
| 13 | `TestFilesHandler_PutBodyOverMaxBytesRejected` | driver | #18 size cap |
| 14 | `TestFilesHandler_PutWithExclTmpDoesNotClobber` | driver | #18 EXCL |

回归：`go test ./internal/executor/... ./cmd/slave-agent/... ./internal/driver/... -race -count=1`.

## 兼容性

| 变更 | 影响 |
|---|---|
| FileExecutor jail | 行为变化：以前 `read /etc/foo` 可能 OK；现在 jail-strict。**这是 intended fix**。如果有操作员的合法 setup 依赖 read-anywhere，加 `cfg.WorkDir` 指到一个更宽的 root 即可。 |
| MCP HTTP 30s timeout / 16 MiB cap | 行为变化：超慢/超大 server fail。OK — 这些都是异常情况。可后续加 cfg knob。 |
| Permissions patch 白名单 | 行为变化：用未知字段 / `*` 通配符的 patch 现在 fail。LLM 触达不到 → operator 也应该 strict。 |
| `source_path` jail | 行为变化：LLM 不能 read driver 任意文件。Operator 用 `source_path_read_roots` YAML opt-in 额外 root。 |
| handlePut 1 GiB body cap | 行为变化：> 1 GiB 拒。极端用例 follow-up cfg。 |
| handlePut tmp 0o600 | Tmp 文件不再 world-readable。Hardening。 |
| `O_EXCL` tmp | 同名 tmp 抢占的攻击 (本地 user `touch /tmp/foo.tmp.xxx`) 现在拒。Hardening。 |
| Parent dir fsync | 写入持久化。No observable behavior change. |

## 不变项 / 反目标

- 不实现 mcpmarket 签名（#19 → issue #13）
- 不动 `cmd/master-agent/`、`internal/orchestrator/`、`internal/orchestration/`（[[master_path_frozen]]）
- 不重构 FileExecutor / MCPExecutor / FilesHandler 的类结构
- 不动 transport / agentsdk / observer
- 不引入新二进制依赖（全 stdlib）
