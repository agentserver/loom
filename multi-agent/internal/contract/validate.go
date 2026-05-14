package contract

import (
	"fmt"
	"strings"
)

func (tc *TaskContract) ApplyDefaults() {
	if tc.Version == 0 {
		tc.Version = Version
	}
	if tc.ExecutionPolicy.Routing == "" {
		tc.ExecutionPolicy.Routing = RoutingDirectFirst
	}
	if tc.ExecutionPolicy.AllowMaster == nil {
		tc.ExecutionPolicy.AllowMaster = Bool(true)
	}
	if !tc.ExecutionPolicy.AllowCodeArtifacts {
		tc.ExecutionPolicy.AllowCodeArtifacts = true
	}
	if tc.ExecutionPolicy.CodePersistence == "" {
		tc.ExecutionPolicy.CodePersistence = CodePersistenceObserverArtifactStore
	}
	if tc.ExecutionPolicy.ExposeCodeToUser == "" {
		tc.ExecutionPolicy.ExposeCodeToUser = ExposeCodeOnRequest
	}
	if tc.ExecutionPolicy.WriteMode == "" {
		tc.ExecutionPolicy.WriteMode = WriteModeArtifactOnly
	}
	if tc.ExecutionPolicy.MaxDAGNodes == 0 {
		tc.ExecutionPolicy.MaxDAGNodes = 6
	}
	if tc.ExecutionPolicy.MaxDepth == 0 {
		tc.ExecutionPolicy.MaxDepth = 3
	}
	if tc.ExecutionPolicy.MaxConcurrency == 0 {
		tc.ExecutionPolicy.MaxConcurrency = 3
	}
}

func (tc TaskContract) Validate() error {
	if tc.Version != Version {
		return fmt.Errorf("unsupported contract version: %d", tc.Version)
	}
	if strings.TrimSpace(tc.Intent.Goal) == "" {
		return fmt.Errorf("intent.goal is required")
	}
	if len(tc.Intent.SuccessCriteria) == 0 {
		return fmt.Errorf("intent.success_criteria is required")
	}
	for i, wt := range tc.DataContract.WriteTargets {
		if wt.Type == "" {
			return fmt.Errorf("data_contract.write_targets[%d].type is required", i)
		}
		if wt.Type != WriteTargetArtifact {
			return fmt.Errorf("data_contract.write_targets[%d].type %q is not supported", i, wt.Type)
		}
		if wt.Kind == "" {
			return fmt.Errorf("data_contract.write_targets[%d].kind is required", i)
		}
		if wt.Name == "" {
			return fmt.Errorf("data_contract.write_targets[%d].name is required", i)
		}
	}
	if err := validatePolicy(tc.ExecutionPolicy); err != nil {
		return err
	}
	if !tc.ExecutionPolicy.AllowBuildMCP {
		for _, s := range tc.CapabilityRequirements.Skills {
			if s == "build_mcp" {
				return fmt.Errorf("build_mcp requested but execution_policy.allow_build_mcp is false")
			}
		}
	}
	return nil
}

func Bool(v bool) *bool {
	return &v
}

func (p ExecutionPolicy) AllowsMaster() bool {
	return p.AllowMaster == nil || *p.AllowMaster
}

func validatePolicy(p ExecutionPolicy) error {
	switch p.Routing {
	case RoutingDirectFirst, RoutingMasterOnly:
	default:
		return fmt.Errorf("execution_policy.routing %q is not supported", p.Routing)
	}
	if p.CodePersistence != CodePersistenceObserverArtifactStore {
		return fmt.Errorf("execution_policy.code_persistence %q is not supported", p.CodePersistence)
	}
	if p.ExposeCodeToUser != ExposeCodeOnRequest {
		return fmt.Errorf("execution_policy.expose_code_to_user %q is not supported", p.ExposeCodeToUser)
	}
	switch p.WriteMode {
	case WriteModeArtifactOnly, WriteModePatch, WriteModeRepoCommit:
	default:
		return fmt.Errorf("execution_policy.write_mode %q is not supported", p.WriteMode)
	}
	if p.WriteMode == WriteModeRepoCommit && !p.RequireUserApprovalForRepoWrites {
		return fmt.Errorf("repo_commit requires execution_policy.require_user_approval_for_repo_writes")
	}
	if p.MaxDAGNodes < 1 {
		return fmt.Errorf("execution_policy.max_dag_nodes must be >= 1")
	}
	if p.MaxDepth < 1 {
		return fmt.Errorf("execution_policy.max_depth must be >= 1")
	}
	if p.MaxConcurrency < 1 {
		return fmt.Errorf("execution_policy.max_concurrency must be >= 1")
	}
	return nil
}
