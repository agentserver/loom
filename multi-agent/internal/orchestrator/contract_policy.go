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
	limit := policy.DAGNodeLimit()
	if len(nodes) > limit {
		return fmt.Errorf("plan too large for contract: %d nodes (max %d)", len(nodes), limit)
	}
	if !policy.AllowBuildMCP {
		for _, n := range nodes {
			if n.Skill == "build_mcp" {
				return fmt.Errorf("build_mcp node %s rejected by contract policy", n.ID)
			}
		}
	}
	return nil
}
