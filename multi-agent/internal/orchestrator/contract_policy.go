package orchestrator

import (
	"fmt"

	"github.com/yourorg/multi-agent/internal/contract"
	"github.com/yourorg/multi-agent/internal/planner"
)

func contractPolicyFromPrompt(prompt string) (contract.TaskContract, string, error) {
	tc, body, ok, err := contract.DecodeEnvelope(prompt)
	if err != nil {
		return contract.TaskContract{}, "", err
	}
	if !ok {
		return contract.TaskContract{}, prompt, nil
	}
	if !tc.ExecutionPolicy.AllowsMaster() {
		return contract.TaskContract{}, "", fmt.Errorf("contract execution_policy.allow_master is false")
	}
	return tc, body, nil
}

func ValidateWithContractPolicy(nodes []planner.Node, policy contract.ExecutionPolicy) error {
	if err := Validate(nodes); err != nil {
		return err
	}
	if err := validateContractNodeLimit(len(nodes), policy); err != nil {
		return err
	}
	if err := validateContractDepth(nodes, policy); err != nil {
		return err
	}
	if !policy.AllowBuildMCP {
		for _, n := range nodes {
			if n.Skill == "build_mcp" || n.Kind == "build_mcp" {
				return fmt.Errorf("build_mcp node %s rejected by contract policy", n.ID)
			}
		}
	}
	return nil
}

func ValidateAppendWithContractPolicy(existing []planner.Node, appended []planner.Node, policy contract.ExecutionPolicy) error {
	if len(appended) == 0 {
		return fmt.Errorf("plan empty")
	}
	combined := make([]planner.Node, 0, len(existing)+len(appended))
	combined = append(combined, existing...)
	combined = append(combined, appended...)
	if err := Validate(combined); err != nil {
		return err
	}
	if err := validateContractNodeLimit(len(combined), policy); err != nil {
		return err
	}
	if err := validateContractDepth(combined, policy); err != nil {
		return err
	}
	if !policy.AllowBuildMCP {
		for _, n := range appended {
			if n.Skill == "build_mcp" || n.Kind == "build_mcp" {
				return fmt.Errorf("build_mcp node %s rejected by contract policy", n.ID)
			}
		}
	}
	return nil
}

func validatePlanForContract(nodes []planner.Node, tc contract.TaskContract) error {
	if tc.Version != 0 {
		return ValidateWithContractPolicy(nodes, tc.ExecutionPolicy)
	}
	return Validate(nodes)
}

func validateAppendPlanForContract(existing []planner.Node, appended []planner.Node, tc contract.TaskContract) error {
	if tc.Version != 0 {
		return ValidateAppendWithContractPolicy(existing, appended, tc.ExecutionPolicy)
	}
	if len(appended) == 0 {
		return fmt.Errorf("plan empty")
	}
	combined := make([]planner.Node, 0, len(existing)+len(appended))
	combined = append(combined, existing...)
	combined = append(combined, appended...)
	return Validate(combined)
}

func validateContractNodeLimit(nodeCount int, policy contract.ExecutionPolicy) error {
	limit := policy.DAGNodeLimit()
	if nodeCount > limit {
		return fmt.Errorf("plan too large for contract: %d nodes (max %d)", nodeCount, limit)
	}
	return nil
}

func validateContractDepth(nodes []planner.Node, policy contract.ExecutionPolicy) error {
	maxDepth := planDepth(nodes)
	limit := policy.DepthLimit()
	if maxDepth > limit {
		return fmt.Errorf("plan depth exceeds contract: %d (max %d)", maxDepth, limit)
	}
	return nil
}

func planDepth(nodes []planner.Node) int {
	byID := make(map[string]planner.Node, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	memo := make(map[string]int, len(nodes))
	var depth func(string) int
	depth = func(id string) int {
		if d, ok := memo[id]; ok {
			return d
		}
		n := byID[id]
		maxDepDepth := 0
		for _, dep := range n.DependsOn {
			if depDepth := depth(dep); depDepth > maxDepDepth {
				maxDepDepth = depDepth
			}
		}
		memo[id] = maxDepDepth + 1
		return memo[id]
	}
	maxDepth := 0
	for _, n := range nodes {
		if d := depth(n.ID); d > maxDepth {
			maxDepth = d
		}
	}
	return maxDepth
}

func effectiveFanoutConcurrency(configMax int, policy contract.ExecutionPolicy, hasContract bool) int {
	if !hasContract {
		return configMax
	}
	limit := policy.ConcurrencyLimit()
	if configMax < limit {
		return configMax
	}
	return limit
}
