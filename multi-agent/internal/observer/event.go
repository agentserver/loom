package observer

import (
	"encoding/json"

	"github.com/yourorg/multi-agent/internal/capability"
)

const (
	RoleDriver = "driver"
	RoleMaster = "master"
	RoleSlave  = "slave"
)

const (
	EventDriverTaskSubmitted = "driver_task_submitted"
	EventDriverTaskStatus    = "driver_task_status"

	EventMasterTaskReceived            = "master_task_received"
	EventMasterPlanCreated             = "master_plan_created"
	EventMasterSubtaskDispatched       = "master_subtask_dispatched"
	EventMasterSubtaskDone             = "master_subtask_done"
	EventMasterTaskCompleted           = "master_task_completed"
	EventMasterTaskFailed              = "master_task_failed"
	EventMasterMCPReplan               = "master_mcp_replan"
	EventMasterMCPCallValidationFailed = "master_mcp_call_validation_failed"
	EventMasterRequiredNodeFailed      = "master_required_node_failed"

	EventSlaveTaskStarted   = "slave_task_started"
	EventSlaveTaskCompleted = "slave_task_completed"
	EventSlaveTaskFailed    = "slave_task_failed"
	EventMCPServerCreated   = "mcp_server_created"
	EventMCPServerBlocked   = "mcp_server_blocked"
)

type Event struct {
	EventID            string                         `json:"event_id,omitempty"`
	TS                 string                         `json:"ts,omitempty"`
	WorkspaceID        string                         `json:"workspace_id"`
	AgentID            string                         `json:"agent_id"`
	AgentRole          string                         `json:"agent_role"`
	Type               string                         `json:"type"`
	TaskID             string                         `json:"task_id"`
	ParentTaskID       string                         `json:"parent_task_id,omitempty"`
	SubtaskID          string                         `json:"subtask_id,omitempty"`
	ChildTaskID        string                         `json:"child_task_id,omitempty"`
	Summary            string                         `json:"summary,omitempty"`
	SubtaskSummary     string                         `json:"subtask_summary,omitempty"`
	Status             string                         `json:"status,omitempty"`
	TargetAgentID      string                         `json:"target_agent_id,omitempty"`
	TargetRole         string                         `json:"target_role,omitempty"`
	MCPServerName      string                         `json:"mcp_server_name,omitempty"`
	MCPTools           []string                       `json:"mcp_tools,omitempty"`
	MCPToolDescriptors []capability.MCPToolDescriptor `json:"mcp_tool_descriptors,omitempty"`
	Payload            json.RawMessage                `json:"payload,omitempty"`
}
