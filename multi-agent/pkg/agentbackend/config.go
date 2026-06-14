package agentbackend

// Config is the single per-backend carrier shape consumed by every
// builder registered with RegisterBuilder. Driver and slave both
// build this from cfg.Agent.* (see internal/{driver,config}/config.go).
//
// Backend-specific fields previously lived in separate ClaudeConfig /
// CodexConfig structs; that arrangement multiplied schema work each
// time a backend was added. The flat shape means a new backend
// (e.g. opencode) only adds a sub-package — no Config edit here.
// Fixes issue #15.
type Config struct {
	Kind      Kind     `yaml:"kind"`
	Bin       string   `yaml:"bin"`
	WorkDir   string   `yaml:"workdir"`
	ExtraArgs []string `yaml:"extra_args"`
}

// Deprecated: ClaudeConfig is a type alias for Config, retained as a
// transitional shim for callers still using the per-backend type name
// (notably the master path which has not migrated to the unified
// shape — see [[master_path_frozen]] + issue #15 master follow-up).
// Remove when those callers migrate. The alias is field-compatible
// with the old struct (Bin, WorkDir, ExtraArgs).
type ClaudeConfig = Config

// Deprecated: same as ClaudeConfig — transitional alias for the
// codex name.
type CodexConfig = Config
