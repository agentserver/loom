package commanderhub

import (
	"encoding/json"

	"github.com/yourorg/multi-agent/internal/commander"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func isTerminalEnvelope(env commander.Envelope) bool {
	switch env.Type {
	case "command_result", "error":
		return true
	case "event":
		var ep commander.EventPayload
		if err := json.Unmarshal(env.Payload, &ep); err != nil {
			return false
		}
		if ep.EventKind != "status" {
			return false
		}
		switch ep.StatusCode {
		case agentbackend.StatusAwaitingApproval, agentbackend.StatusDone, agentbackend.StatusError:
			return true
		default:
			return false
		}
	default:
		return false
	}
}
