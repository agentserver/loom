# multiagent skill TODO

## 补全 write_paths 的使用说明

**现状**：`references/slave-skills.md` 和 `references/task-contract.md` 都没说清 `submit_task` 的 `write_paths` 实际走法，首次使用会踩坑。

**需要补全的内容**：

1. `write_paths` 仅支持 `skill="chat"`；`skill="bash"` 直接报错 `"skill bash takes JSON-only prompts; read_paths/write_paths cannot be conveyed"`。
2. `submit_task` 返回的 `manifest.writes[].put_url` 需要 slave 上的 Claude **主动** PUT 文件上去 —— slave-agent 不会自动上传。
3. PUT 必须带 `Authorization: Bearer <token>`，token 在 slave 工作目录的 `observer.token` 文件中。
4. 默认 slave Claude 权限不允许 `curl`，需要先用 `update_slave_claude_permissions` 放行 `Bash(curl *)` 和 `Read(.../observer.token)`。
5. prompt 必须显式指示 slave Claude 使用 manifest 的 `put_url` 并附带 token，否则它只会确认文件存在而不上传。

**建议**：在 `references/` 下加一节 "File Transfer via write_paths" 或新增 `references/write-paths.md`，附完整 `curl PUT` 示例。

**可工作示例 prompt**：

```
Upload ./slave.log to the write manifest's put_url. Read the bearer token from
<workdir>/observer.token and pass it via -H "Authorization: Bearer $(cat ...)".

curl -sS -X PUT --data-binary @./slave.log \
  -H "Authorization: Bearer $(cat <workdir>/observer.token)" \
  <put_url> -w "\nHTTP %{http_code}\n"
```

成功后 `wait_task` 的 `written_files` 会带 `path / bytes / sha256 / written_at`。
