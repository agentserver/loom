package executor

import "context"

type Task struct {
	ID            string
	Skill         string
	Prompt        string
	SystemContext string
	TimeoutSec    int

	ParentSessionID   string
	ParentAgentID     string
	ParentDisplayName string
}

type Result struct {
	Summary          string
	CapabilityChange string // empty = no change
	SessionID        string // backend session/thread id (chat / chat_resume only)
	AwaitingUser     *AskUserPayload
	// WrappedOutput, when non-empty, is the structured kind-marker envelope
	// (`{"kind":"final"|"awaiting_user", "session_id":..., ...}`) that the
	// dispatcher builds for chat-skill results. The poller forwards it to
	// agentserver as the task `result` so the driver can read the session id
	// + kind from `info.Result` even when the observer relay is unavailable.
	// Empty for non-chat skills; the poller falls back to Summary then.
	WrappedOutput string
}

// AskUserPayload mirrors humanloop.Payload but lives here so chat-skill
// callers in this package can build a Result without importing humanloop.
type AskUserPayload struct {
	Kind     string   `json:"kind"` // "ask_user" | "request_permission"
	Question string   `json:"question,omitempty"`
	Options  []string `json:"options,omitempty"`
	Context  string   `json:"context,omitempty"`
	Intent   string   `json:"intent,omitempty"`
	Target   string   `json:"target,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

type Sink interface {
	Write(eventType, data string)
	Close()
}

type Executor interface {
	Run(ctx context.Context, t Task, sink Sink) (Result, error)
}
