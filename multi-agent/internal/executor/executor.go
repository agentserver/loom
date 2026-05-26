package executor

import "context"

type Task struct {
	ID            string
	Skill         string
	Prompt        string
	SystemContext string
	TimeoutSec    int
}

type Result struct {
	Summary          string
	CapabilityChange string // empty = no change
	SessionID        string // backend session/thread id (chat / chat_resume only)
	AwaitingUser     *AskUserPayload
}

// AskUserPayload mirrors humanloop.Payload but lives here so chat-skill
// callers in this package can build a Result without importing humanloop.
type AskUserPayload struct {
	Kind     string   `json:"kind"`              // "ask_user" | "request_permission"
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
