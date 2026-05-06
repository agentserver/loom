package orchestrator

import (
	"fmt"
	"regexp"

	"github.com/yourorg/salve_agent/internal/planner"
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
