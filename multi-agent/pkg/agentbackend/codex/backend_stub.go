// backend_stub.go — temporary exported constructor; will be deleted in Task 14
// when the full Backend type is assembled.
package codex

import "github.com/yourorg/multi-agent/pkg/agentbackend"

func New(cfg agentbackend.CodexConfig, env []string) *executor { return newExecutor(cfg, env) }
