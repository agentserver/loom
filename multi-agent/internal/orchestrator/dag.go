package orchestrator

import (
	"fmt"
	"regexp"

	"github.com/yourorg/multi-agent/internal/planner"
)

const MaxNodes = 100

func Validate(nodes []planner.Node) error {
	if len(nodes) == 0 {
		return fmt.Errorf("plan empty")
	}
	if len(nodes) > MaxNodes {
		return fmt.Errorf("plan too large: %d nodes (max %d)", len(nodes), MaxNodes)
	}
	seen := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		if n.ID == "" {
			return fmt.Errorf("node with empty id")
		}
		if seen[n.ID] {
			return fmt.Errorf("duplicate node id: %s", n.ID)
		}
		seen[n.ID] = true
	}
	for _, n := range nodes {
		for _, dep := range n.DependsOn {
			if !seen[dep] {
				return fmt.Errorf("dangling dep: %s -> %s", n.ID, dep)
			}
		}
	}
	return detectCycle(nodes)
}

// detectCycle uses Kahn's topological sort.
func detectCycle(nodes []planner.Node) error {
	indeg := make(map[string]int, len(nodes))
	for _, n := range nodes {
		indeg[n.ID] = 0
	}
	for _, n := range nodes {
		for range n.DependsOn {
			indeg[n.ID]++
		}
	}
	var queue []string
	for id, d := range indeg {
		if d == 0 {
			queue = append(queue, id)
		}
	}
	visited := 0
	rev := make(map[string][]string)
	for _, n := range nodes {
		for _, dep := range n.DependsOn {
			rev[dep] = append(rev[dep], n.ID)
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		for _, downstream := range rev[id] {
			indeg[downstream]--
			if indeg[downstream] == 0 {
				queue = append(queue, downstream)
			}
		}
	}
	if visited != len(nodes) {
		return fmt.Errorf("cycle detected (visited %d of %d nodes)", visited, len(nodes))
	}
	return nil
}

type FinishedNode struct {
	NodeID string
	Status string // "completed" | "failed" | "skipped"
	Output string
	Error  string
}

type Scheduler struct {
	nodes      []planner.Node
	nodeByID   map[string]planner.Node
	rev        map[string][]string
	maxConc    int
	inFlight   map[string]bool
	finished   map[string]FinishedNode
	pending    map[string]bool
	failedDeps map[string]bool
}

func NewScheduler(nodes []planner.Node, maxConc int) *Scheduler {
	if maxConc <= 0 {
		maxConc = 1
	}
	s := &Scheduler{
		nodes:      nodes,
		nodeByID:   make(map[string]planner.Node, len(nodes)),
		rev:        make(map[string][]string),
		maxConc:    maxConc,
		inFlight:   make(map[string]bool),
		finished:   make(map[string]FinishedNode),
		pending:    make(map[string]bool),
		failedDeps: make(map[string]bool),
	}
	for _, n := range nodes {
		s.nodeByID[n.ID] = n
		for _, dep := range n.DependsOn {
			s.rev[dep] = append(s.rev[dep], n.ID)
		}
		if len(n.DependsOn) == 0 {
			s.pending[n.ID] = true
		}
	}
	return s
}

func (s *Scheduler) Ready() []planner.Node {
	free := s.maxConc - len(s.inFlight)
	if free <= 0 {
		return nil
	}
	var out []planner.Node
	for id := range s.pending {
		if free == 0 {
			break
		}
		out = append(out, s.nodeByID[id])
		free--
	}
	return out
}

func (s *Scheduler) MarkDispatched(nodeID string) {
	delete(s.pending, nodeID)
	s.inFlight[nodeID] = true
}

func (s *Scheduler) Report(nodeID, status, output, errMsg string) {
	delete(s.inFlight, nodeID)
	s.finished[nodeID] = FinishedNode{NodeID: nodeID, Status: status, Output: output, Error: errMsg}
	if status != "completed" {
		return
	}
	for _, downstream := range s.rev[nodeID] {
		if _, done := s.finished[downstream]; done {
			continue
		}
		if s.failedDeps[downstream] {
			continue
		}
		ready := true
		for _, dep := range s.nodeByID[downstream].DependsOn {
			f, ok := s.finished[dep]
			if !ok || f.Status != "completed" {
				ready = false
				break
			}
		}
		if ready {
			s.pending[downstream] = true
		}
	}
}

func (s *Scheduler) MarkDownstreamSkipped(failedID string) {
	var stack []string
	stack = append(stack, s.rev[failedID]...)
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, done := s.finished[id]; done {
			continue
		}
		s.failedDeps[id] = true
		delete(s.pending, id)
		s.finished[id] = FinishedNode{NodeID: id, Status: "skipped", Error: fmt.Sprintf("upstream %s failed/skipped", failedID)}
		stack = append(stack, s.rev[id]...)
	}
}

func (s *Scheduler) MarkSuperseded(nodeID, reason string) []FinishedNode {
	var out []FinishedNode
	mark := func(id string) {
		if _, done := s.finished[id]; done {
			return
		}
		delete(s.pending, id)
		delete(s.inFlight, id)
		fn := FinishedNode{NodeID: id, Status: "skipped", Error: reason}
		s.finished[id] = fn
		out = append(out, fn)
	}

	mark(nodeID)
	stack := append([]string{}, s.rev[nodeID]...)
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		mark(id)
		stack = append(stack, s.rev[id]...)
	}
	return out
}

func (s *Scheduler) Done() bool {
	return len(s.pending) == 0 && len(s.inFlight) == 0
}

func (s *Scheduler) AllFinished() []FinishedNode {
	out := make([]FinishedNode, 0, len(s.finished))
	for _, n := range s.nodes {
		if f, ok := s.finished[n.ID]; ok {
			out = append(out, f)
		}
	}
	return out
}

// Append adds new nodes to a running scheduler. Used by runFanout's
// phase-boundary handler when re-planning after a build_mcp completes.
//
// Caller is responsible for unique node ids; Append errors if any new node
// shares an id with an existing one. depends_on may reference either
// already-appended ids or pre-existing ids (including completed ones).
func (s *Scheduler) Append(nodes []planner.Node) error {
	for _, n := range nodes {
		if _, exists := s.nodeByID[n.ID]; exists {
			return fmt.Errorf("Scheduler.Append: duplicate id %q", n.ID)
		}
	}
	for _, n := range nodes {
		s.nodes = append(s.nodes, n)
		s.nodeByID[n.ID] = n
		for _, d := range n.DependsOn {
			s.rev[d] = append(s.rev[d], n.ID)
		}
		// Only add to pending if all dependencies are already complete
		ready := true
		for _, dep := range n.DependsOn {
			f, ok := s.finished[dep]
			if !ok || f.Status != "completed" {
				ready = false
				break
			}
		}
		if ready {
			s.pending[n.ID] = true
		}
	}
	return nil
}

var renderRe = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_-]+)\.output\s*\}\}`)

func Render(template string, outputs map[string]string) (string, error) {
	var firstErr error
	out := renderRe.ReplaceAllStringFunc(template, func(match string) string {
		sub := renderRe.FindStringSubmatch(match)
		id := sub[1]
		v, ok := outputs[id]
		if !ok {
			if firstErr == nil {
				firstErr = fmt.Errorf("template references missing node output: %s", id)
			}
			return match
		}
		return v
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}
