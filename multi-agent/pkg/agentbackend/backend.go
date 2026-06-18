package agentbackend

import (
	"context"
	"errors"
	"time"

	"github.com/yourorg/multi-agent/internal/executor"
)

type Kind string

const (
	KindClaude   Kind = "claude"
	KindCodex    Kind = "codex"
	KindOpencode Kind = "opencode"
)

type SessionOrigin string

const (
	SessionOriginUser      SessionOrigin = "user"
	SessionOriginSubagent  SessionOrigin = "subagent"
	SessionOriginAgentTask SessionOrigin = "agent_task"
	SessionOriginUnknown   SessionOrigin = "unknown"
)

// Re-exports so callers can depend on agentbackend only.
type (
	Task   = executor.Task
	Sink   = executor.Sink
	Result = executor.Result
)

type Backend interface {
	Kind() Kind
	Run(ctx context.Context, t Task, sink Sink) (Result, error)
	RunResume(ctx context.Context, sessionID, answer string, sink Sink) (Result, error)
	LLM() LLMRunner
	Permissions() PermissionsStore
	Detect(ctx context.Context) error

	// ListSessions returns descriptors for every session this backend
	// has persisted on disk. Empty list (with nil error) when the
	// backend has no session storage directory or it is empty.
	// Implementations must not shell out to the backend CLI.
	// Individual unparseable session entries are skipped silently;
	// a hard error is returned only when the storage location itself
	// can't be read (e.g. permission denied on a directory we expected).
	ListSessions(ctx context.Context) ([]Session, error)

	// GetSession returns the descriptor plus full message history of
	// one session. Returns ErrSessionNotFound when id is unknown to
	// this backend. Like ListSessions, no subprocess invocation.
	GetSession(ctx context.Context, id string) (Session, []SessionMessage, error)
}

type LLMRunner interface {
	Run(ctx context.Context, prompt string) (string, error)
}

type PermissionsStore interface {
	Get(ctx context.Context) (State, error)
	Patch(ctx context.Context, p Patch) (State, error)
}

type State struct {
	Backend Kind     `json:"backend"`
	Path    string   `json:"path"`
	Mode    string   `json:"mode,omitempty"`
	Allow   []string `json:"allow,omitempty"`
	Deny    []string `json:"deny,omitempty"`
}

type Patch struct {
	Presets     []string `json:"presets,omitempty"`
	AllowAdd    []string `json:"allow_add,omitempty"`
	AllowRemove []string `json:"allow_remove,omitempty"`
	DenyAdd     []string `json:"deny_add,omitempty"`
	DenyRemove  []string `json:"deny_remove,omitempty"`
	Mode        string   `json:"mode,omitempty"`
}

// SessionPreviewMaxBytes caps session list text fields such as Preview and
// Title so a session-list UI can render one line per row without unbounded
// growth from verbose output.
const SessionPreviewMaxBytes = 256

// Session is a backend-agnostic descriptor of a conversation thread
// persisted by an agent CLI (claude / codex / opencode). Authoritative
// storage lives in the backend's own files; this struct is the
// interchange shape consumed by daemon / web layers via
// Backend.ListSessions / GetSession.
type Session struct {
	// ID is the backend-native session identifier (claude session uuid,
	// codex thread uuid, opencode session id). Stable across reads and
	// used directly by RunResume.
	ID string

	// Kind names the backend that owns this session.
	Kind Kind

	// WorkingDir is the cwd the session was originally created with,
	// as recorded by the backend itself. May be empty when unknown.
	WorkingDir string

	// Title is a short human-readable name for the session. Backends set it to
	// the first user prompt when available. UIs may fall back to Preview or ID.
	Title string

	// Origin classifies whether this session was started directly by the user
	// or spawned as a subagent/sidechain by another session.
	Origin SessionOrigin

	// ParentID links subagent sessions back to the session that spawned them.
	// Empty for user sessions or when the backend does not expose the parent.
	ParentID string

	// ParentAgentID is the stable agent instance ID that owns ParentID.
	// Empty when ParentID is empty.
	ParentAgentID string

	// ParentDisplayName is the denormalized display name for the parent agent.
	// It lets callers label a parent even when that agent is offline.
	ParentDisplayName string

	// AgentName and AgentRole carry backend-provided subagent labels when
	// available. They are empty for normal user sessions.
	AgentName string
	AgentRole string

	// StartedAt is when the first message in the session was recorded.
	// Zero value means unknown.
	StartedAt time.Time

	// UpdatedAt is when the most recent message was appended. Zero value
	// means unknown.
	UpdatedAt time.Time

	// MessageCount is the total messages in the session.
	MessageCount int

	// Preview is a short snippet from the most recent assistant message,
	// capped at SessionPreviewMaxBytes.
	Preview string

	// ActiveWorker is true when the daemon currently has a hot worker cached
	// for this session. It is transport/runtime metadata, not persisted backend
	// session state.
	ActiveWorker bool `json:"active_worker,omitempty"`
}

// SessionMessage is one turn in a session. Roles map to claude / codex /
// opencode conventions: "user", "assistant", "system", "tool".
type SessionMessage struct {
	Role string    `json:"role"`
	Text string    `json:"text"`
	Ts   time.Time `json:"ts"`
}

// SessionWorker is a long-lived per-session worker owned by a backend. Backends
// opt into commander hot-session reuse by also implementing SessionWorkerBackend.
type SessionWorker interface {
	Run(ctx context.Context, prompt string, sink Sink) (Result, error)
	Close() error
}

// SessionWorkerBackend is implemented by backends that can safely keep a
// session hot across turns. Unsupported backends should not implement it; the
// commander handler will keep using RunResume.
type SessionWorkerBackend interface {
	NewSessionWorker(ctx context.Context, session Session) (SessionWorker, error)
}

// HealthySessionWorker may be implemented by SessionWorker to let commander
// discard stale workers before sending another user turn.
type HealthySessionWorker interface {
	Healthy() bool
}

// ErrSessionNotFound signals GetSession was called with an id that does
// not exist in this backend's persistence.
var ErrSessionNotFound = errors.New("agentbackend: session not found")

// ErrSessionWorkerUnavailable tells callers that a backend cannot provide a
// hot worker for this session/turn. Commander falls back to RunResume.
var ErrSessionWorkerUnavailable = errors.New("agentbackend: session worker unavailable")
