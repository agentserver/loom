# Slave 安全修复 §1.4 — 测试证据

- **日期**：2026-06-13
- **分支**：`worktree-fix-slave-security-1.4` @ HEAD `1a0157f`
- **范围**：5 个 CRITICAL 修复 (#14–#18)，所有都是 LLM-controlled-input 拒绝路径
- **不在范围**：#19 mcpmarket signing（→ [issue #13](https://github.com/agentserver/loom/issues/13)）；master 路径（[[master_path_frozen]]）

## 为什么 unit + 单包 race 足够 (而非 host-local e2e)

每个 fix 都是**单进程内的 input validation / resource bounding**，攻击向量是 LLM 投递的恶意 JSON / 文件路径 / HTTP body — 不跨服务，不依赖网络拓扑。相比之下 §1.3 是 driver↔observer↔slave 三跨 control plane，需要本机灰度跑 prod_test 才能验。

这里走"对抗 unit + race 全量 + 二进制 sanity"路径：

1. **每个 fix 都用 RED→GREEN** 演示 — 拒绝路径有具体 `t.Run` 触达
2. **完整仓库 race 跑过** — 无任何包退化
3. **driver/slave 二进制能正常起** — flag 解析未被破坏

## 提交序列

```
1a0157f fix(driver): /files/put body cap + O_EXCL tmp + parent fsync (Bug #18)
05de678 fix(driver): write_slave_file source_path requires workdir jail (Bug #17)
317fe42 fix(slave-agent): permissions patch rejects '*' wildcard + empty entries (Bug #16)
a5433e3 fix(executor): HTTP MCP gets 30s timeout + 16 MiB cap (Bug #15)
2dd0d3d fix(executor): FileExecutor jails file ops to WorkDir (Bug #14)
bc3295c feat: resolveExistingPrefix helper (jail for not-yet-existing leaf)
d5020c3 docs: implementation plan for slave security §1.4 fixes
971b7be docs: spec for slave security §1.4 fixes (#14-#18)
```

`git diff --stat master..HEAD -- 'cmd/master-agent/**' 'internal/orchestrator/**' 'internal/orchestration/**'` → **empty**（master 冻结遵守）

## 每个 fix 的对抗测试

### #14 FileExecutor jail — `internal/executor/file.go`

| 测试 | 覆盖 |
|---|---|
| `TestFileExecutor_RejectsAbsolutePathOutsideJail` | LLM 投 `/etc/shadow` |
| `TestFileExecutor_RejectsSymlinkLeapingOutOfJail` | jail 内创建 symlink → 外，仍拒 |
| `TestFileExecutor_AcceptsRelativeInsideJail` | 正常 path 仍 OK |
| `TestFileExecutor_AcceptsReadWhenWorkDirIsSymlink` | macOS `/tmp→/private/tmp` 不误杀 |

WorkDir 在 `NewFileExecutor` 经 `filepath.Abs` + `filepath.EvalSymlinks` 缓存；`assertInJail` 用 `resolveExistingPrefix` 解 candidate 再 `filepath.Rel` 比对。比较的两边都已解 symlink，避免参考系不一致。

### #15 HTTP MCP 资源耗尽 — `internal/executor/mcp.go`

| 测试 | 覆盖 |
|---|---|
| `TestMCPExecutor_HTTPTimeoutFiresIn30s` | 慢/挂死 server (real timer, 30s wall) |
| `TestMCPExecutor_HTTPResponseSizeCapEnforced` | 16 MiB+1 body → 拒 |

`mcpHTTPTimeout = 30s` 写进 `http.Client.Timeout`；`io.LimitReader(resp.Body, mcpMaxResponseBytes+1)` 后判 `> mcpMaxResponseBytes` (16 MiB) 拒。

### #16 permissions patch 校验 — `cmd/slave-agent/permissions_executor.go`

| 测试 | 覆盖 |
|---|---|
| `TestPermissionsPatch_RejectsStarWildcard` (5 subtests) | `*` in presets/allow_add/allow_remove/deny_add/deny_remove 全拒 |
| `TestPermissionsPatch_RejectsEmptyEntry` | 空字符串 / 仅空白 → 拒 |
| `TestPermissionsPatch_AcceptsValidPatch` | 正常 patch + refresh 触发 |

`validatePermissionsPatch` 在 `store.Patch` 之前拦截；任何拒绝路径 store.Patch 都不会被调用（用 `fakePermsStore.patchSeen` 验证）。

### #17 source_path 二级 RCE — `internal/driver/slave_file_tools.go`

| 测试 | 覆盖 |
|---|---|
| `TestWriteSlaveFile_SourcePathRejectsOutsideJail` | LLM 让 driver 读 `/etc/shadow` 推到 slave → 拒 |
| `TestWriteSlaveFile_SourcePathAcceptsInsideJail` | WorkDir 内的文件正常 |
| `TestWriteSlaveFile_SourcePathAcceptsExtraReadRoot` | operator opt-in 的额外 root 生效 |

`AssertReadableSource(p, workDir, allowedRoots)` 在 `safe_paths.go`；config 新增 `DriverDefaults.WorkDir`（YAML: `workdir`）+ `DriverDefaults.SourcePathReadRoots`（YAML: `source_path_read_roots`）。原 `TestWriteSlaveFile_SourcePathRegistersAndUploads` 重写为 jail-aware (把测试文件放 WorkDir 下)，避免锁死 buggy 行为。

### #18 /files/put hardening — `internal/driver/files_handler.go`

| 测试 | 覆盖 |
|---|---|
| `TestFilesHandler_Put_BodyOverMaxBytesRejected` | 过 cap → 413，target / tmp 都不留 |
| 既有 `TestFilesHandler_Put_Atomic` 等 4 个 | 无回归 |

4 修复：
1. `http.MaxBytesReader(w, r.Body, maxPutBytes)` (default `1<<30`)
2. tmp 改 `O_CREATE|O_WRONLY|O_EXCL` (从 `O_TRUNC`)
3. tmp mode `0o600` (从 `0o644`)
4. rename 后 best-effort `parent.Sync()`

O_EXCL 没单独 unit-test（需 monkey-patch 8-byte 随机 suffix 才能强碰撞，测试基础设施大于改动本身）；写进 commit message + code review 兜底。

## 全量回归

```bash
$ go build ./...                  # clean
$ go vet ./...                    # clean
$ go test ./... -race -count=1    # all OK
```

**关键包**：

| 包 | 时间 |
|---|---|
| `internal/executor` | 34.6s (HTTP timeout test 跑了 30s wall, intended) |
| `internal/driver` | 1.96s |
| `cmd/slave-agent` | 1.38s |

整仓 50+ 包全过，无 skip / no race 警告。

## 二进制 sanity

```bash
$ go build -o /tmp/bin-smoke-1.4/driver-agent ./cmd/driver-agent  # OK
$ go build -o /tmp/bin-smoke-1.4/slave-agent  ./cmd/slave-agent   # OK
$ /tmp/bin-smoke-1.4/driver-agent
driver-agent — bridges Claude Code to the multi-agent workspace.
Usage:
  driver-agent register   --config /path/to/driver.yaml
  driver-agent serve-mcp  --config /path/to/driver.yaml
```

flag parse 路径未被破坏。

## 后续工作

- mcpmarket signing (#19) — issue #13，独立 PR
- §1.5 planner injection — 下一个 worktree
