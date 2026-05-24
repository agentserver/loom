package agentbackend

type Config struct {
	Kind   Kind         `yaml:"kind"`
	Claude ClaudeConfig `yaml:"-"`
	Codex  CodexConfig  `yaml:"-"`
}

type ClaudeConfig struct {
	Bin       string
	WorkDir   string
	ExtraArgs []string
}

type CodexConfig struct {
	Bin       string
	WorkDir   string
	ExtraArgs []string
}
