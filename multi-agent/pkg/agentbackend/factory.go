package agentbackend

import "fmt"

// Builder is a function that creates a Backend from a Config.
type Builder func(Config, []string) (Backend, error)

var builders = make(map[Kind]Builder)

// RegisterBuilder registers a Builder for a given Kind.
// This is called by init() functions in subpackages like claude.
func RegisterBuilder(kind Kind, builder Builder) {
	builders[kind] = builder
}

// New creates a Backend based on the Config Kind.
// It defaults to Claude if no Kind is specified.
func New(cfg Config, env []string) (Backend, error) {
	kind := cfg.Kind
	if kind == "" {
		kind = KindClaude
	}
	builder, ok := builders[kind]
	if !ok {
		return nil, fmt.Errorf("agentbackend: unknown kind %q", kind)
	}
	return builder(cfg, env)
}
