package orchestrator

import (
	"regexp"

	"github.com/yourorg/multi-agent/internal/orchestration"
	"github.com/yourorg/multi-agent/internal/planner"
)

const MaxNodes = orchestration.MaxNodes

type FinishedNode = orchestration.FinishedNode
type Scheduler = orchestration.Scheduler

var renderRe = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_-]+)\.output((?:\.[A-Za-z0-9_-]+)*)\s*\}\}`)

func Validate(nodes []planner.Node) error {
	return orchestration.Validate(nodes)
}

func NewScheduler(nodes []planner.Node, maxConc int) *Scheduler {
	return orchestration.NewScheduler(nodes, maxConc)
}

func Render(template string, outputs map[string]string) (string, error) {
	return orchestration.Render(template, outputs)
}
