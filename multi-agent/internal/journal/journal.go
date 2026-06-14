package journal

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/yourorg/multi-agent/internal/executor"
)

// LLMRunner is the minimal subset of pkg/agentbackend.LLMRunner that
// the journal needs. Defined locally so internal/journal doesn't
// import pkg/agentbackend (preserves dependency direction; the
// slave-agent main wires the concrete agentbackend.LLM into here).
type LLMRunner interface {
	Run(ctx context.Context, prompt string) (string, error)
}

type Config struct {
	Dir string
	// LLM merges capability-change events into CURRENT_STATE.md.
	// Previously the journal shelled out to a hardcoded CLI binary
	// using Claude-flavoured flags ('--print'), which silently failed
	// on codex slaves. Now the slave-agent main injects whichever
	// backend.LLM() the configured agent provides so the protocol
	// matches the kind.
	LLM LLMRunner
}

type Journal struct {
	cfg Config
	mu  sync.Mutex
}

func New(cfg Config) (*Journal, error) {
	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, err
	}
	return &Journal{cfg: cfg}, nil
}

func (j *Journal) Record(ctx context.Context, t executor.Task, r executor.Result) error {
	if r.CapabilityChange == "" {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()

	csPath := filepath.Join(j.cfg.Dir, "CURRENT_STATE.md")
	current, _ := os.ReadFile(csPath)

	merged, mergeErr := j.callLLM(ctx, string(current), t, r.CapabilityChange)
	histLine := j.histLine(t, r.CapabilityChange, mergeErr)
	j.appendHistory(histLine)

	if mergeErr != nil {
		return nil // history.md already records the failure
	}
	return atomicWrite(csPath, []byte(merged))
}

func (j *Journal) callLLM(ctx context.Context, currentDoc string, t executor.Task, change string) (string, error) {
	prompt := fmt.Sprintf(
		"Current CURRENT_STATE.md:\n%s\n\nJust executed task %s (skill=%s) with capability impact:\n%s\n\nOutput the updated CURRENT_STATE.md in full. Group with H2 (## Tools, ## MCP Servers, ## Mounted Resources, ## Credentials). Only modify affected sections. Be terse.",
		currentDoc, t.ID, t.Skill, change,
	)
	if j.cfg.LLM == nil {
		return "", fmt.Errorf("journal: no LLMRunner configured")
	}
	return j.cfg.LLM.Run(ctx, prompt)
}

func (j *Journal) histLine(t executor.Task, change string, mergeErr error) string {
	ts := time.Now().UTC().Format(time.RFC3339)
	if mergeErr != nil {
		return fmt.Sprintf("| %s | %s | %s | [merge failed: %v] %s |\n", ts, t.ID, t.Skill, mergeErr, change)
	}
	return fmt.Sprintf("| %s | %s | %s | %s |\n", ts, t.ID, t.Skill, change)
}

func (j *Journal) appendHistory(line string) {
	p := filepath.Join(j.cfg.Dir, "history.md")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	io.WriteString(f, line)
}

func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// suppress unused
var _ = io.Discard
