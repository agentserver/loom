// Package evalconfig parses the paper-v3 eval-runner default config.
//
// The only purpose of this package right now is to make the "master path is
// frozen" guarantee a property an automated test can assert on. The default
// config lives at dev/configs/eval-default.yaml and is referenced by the
// project README. See the "Historical note — BuildMCPExecutor was never
// implemented" callout in README.md / README.en.md and the v3 plan in
// docs/intermediate/.
//
// We deliberately keep the schema small (just the routing block) so this
// package can grow alongside the eval-runner without re-litigating the
// freeze every time another field is added.
package evalconfig

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Routing captures the eval-runner's dispatch policy.
//
// Mode is the routing strategy; for the v3 cycle it must be "driver-first".
// AllowMaster is the hard latch — when false, any attempt to dispatch through
// cmd/master-agent must be refused by the caller. The freeze rules out the
// master path entirely; AllowMaster exists so a misconfigured operator config
// fails loudly instead of silently re-enabling it.
type Routing struct {
	Mode        string `yaml:"mode"`
	AllowMaster bool   `yaml:"allow_master"`
}

// Config is the top-level eval-runner config.
type Config struct {
	Routing Routing `yaml:"routing"`
}

// Load parses a YAML file from disk.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("evalconfig: read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("evalconfig: parse %s: %w", path, err)
	}
	return &cfg, nil
}
