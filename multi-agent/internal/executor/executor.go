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
}

type Sink interface {
	Write(eventType, data string)
	Close()
}

type Executor interface {
	Run(ctx context.Context, t Task, sink Sink) (Result, error)
}
