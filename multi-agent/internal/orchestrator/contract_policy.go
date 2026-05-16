package orchestrator

import (
	"fmt"

	"github.com/agentserver/agentserver/pkg/agentsdk"
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

func ValidateWithContractPolicyAndAgents(nodes []planner.Node, policy contract.ExecutionPolicy, agents []agentsdk.AgentCard) error {
	if err := ValidateWithContractPolicy(nodes, policy); err != nil {
		return err
	}
	return validateContractTargets(nodes, policy, agents)
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

func ValidateAppendWithContractPolicyAndAgents(existing []planner.Node, appended []planner.Node, policy contract.ExecutionPolicy, agents []agentsdk.AgentCard) error {
	if err := ValidateAppendWithContractPolicy(existing, appended, policy); err != nil {
		return err
	}
	return validateContractTargets(appended, policy, agents)
}

func validatePlanForContract(nodes []planner.Node, tc contract.TaskContract, agents ...[]agentsdk.AgentCard) error {
	if tc.Version != 0 {
		if len(agents) > 0 {
			return ValidateWithContractPolicyAndAgents(nodes, tc.ExecutionPolicy, agents[0])
		}
		return ValidateWithContractPolicy(nodes, tc.ExecutionPolicy)
	}
	return Validate(nodes)
}

func validateAppendPlanForContract(existing []planner.Node, appended []planner.Node, tc contract.TaskContract, agents ...[]agentsdk.AgentCard) error {
	if tc.Version != 0 {
		if len(agents) > 0 {
			return ValidateAppendWithContractPolicyAndAgents(existing, appended, tc.ExecutionPolicy, agents[0])
		}
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

func validateContractTargets(nodes []planner.Node, policy contract.ExecutionPolicy, agents []agentsdk.AgentCard) error {
	allowedTargets := make(map[string]bool, len(policy.AllowedTargets))
	for _, target := range policy.AllowedTargets {
		if target != "" {
			allowedTargets[target] = true
		}
	}
	availableTargets := make(map[string]bool, len(agents))
	for _, agent := range agents {
		if agent.AgentID != "" {
			availableTargets[agent.AgentID] = true
		}
	}
	for _, n := range nodes {
		if n.TargetID == "" {
			return fmt.Errorf("node %s target_id is required by contract policy", n.ID)
		}
		if len(allowedTargets) > 0 && !allowedTargets[n.TargetID] {
			return fmt.Errorf("node %s target %s is not allowed by contract policy", n.ID, n.TargetID)
		}
		if !availableTargets[n.TargetID] {
			return fmt.Errorf("node %s target %s is not in current resource snapshot", n.ID, n.TargetID)
		}
	}
	return nil
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
