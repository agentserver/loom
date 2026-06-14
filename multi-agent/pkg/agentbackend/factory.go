package agentbackend

import (
	"fmt"
	"sort"
)

// Builder constructs a Backend from a flat Config. Backend packages
// register a Builder via RegisterBuilder() in their init().
type Builder func(Config, []string) (Backend, error)

var builders = make(map[Kind]Builder)

// RegisterBuilder installs a Builder for kind. Duplicate registration
// for the same kind panics — that almost always means two backend
// packages tried to claim the same name during init().
func RegisterBuilder(kind Kind, b Builder) {
	if kind == "" {
		panic("agentbackend: RegisterBuilder called with empty kind")
	}
	if _, dup := builders[kind]; dup {
		panic("agentbackend: duplicate RegisterBuilder for " + string(kind))
	}
	builders[kind] = b
}

// RegisteredKinds returns sorted names of every kind currently
// registered. LoadConfig in driver+slave use this to surface a
// helpful error when YAML specifies an unknown agent.kind.
func RegisteredKinds() []string {
	out := make([]string, 0, len(builders))
	for k := range builders {
		out = append(out, string(k))
	}
	sort.Strings(out)
	return out
}

// New builds the Backend for cfg.Kind. Empty kind is rejected (no
// implicit default — see §issue-15). Unknown kind reports the
// available set so the operator can spot a missing import.
func New(cfg Config, env []string) (Backend, error) {
	if cfg.Kind == "" {
		return nil, fmt.Errorf("agentbackend: kind is required; one of %v", RegisteredKinds())
	}
	b, ok := builders[cfg.Kind]
	if !ok {
		return nil, fmt.Errorf("agentbackend: unknown kind %q; registered: %v", cfg.Kind, RegisteredKinds())
	}
	return b(cfg, env)
}
