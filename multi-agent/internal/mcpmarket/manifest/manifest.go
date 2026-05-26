package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

const SchemaVersion = 1

// Kind enumerates supported package kinds.
type Kind string

const (
	KindMCP   Kind = "mcp"
	KindSkill Kind = "skill"
)

// Manifest is the on-disk manifest.json embedded in every package tarball.
// userspace + future marketplace both use this struct; JCS canonicalization
// produces the byte-stable form used for signing / hashing where applicable.
type Manifest struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          Kind   `json:"kind"`
	Slug          string `json:"slug"`
	Version       string `json:"version"`
	// NOTE: tarball_sha256 deliberately NOT in the manifest for v1. The server
	// computes the hash of received bytes and stores it in VersionRow. Putting
	// it in the manifest creates a chicken-and-egg with deterministic packing
	// (manifest is inside the tar it would hash). Future signing work will
	// reintroduce it using a "hash excluding the manifest entry" scheme.
	SpecRef   string     `json:"spec_ref,omitempty"`  // required when kind=mcp
	CardRef   string     `json:"card_ref"`
	CasesRef  string     `json:"cases_ref,omitempty"` // typically present when kind=mcp
	Software  Software   `json:"software"`
	Hardware  Hardware   `json:"hardware"`
	SLAHint   SLAHint    `json:"sla_hint"`
	Tags      []string   `json:"tags"`
	License   string     `json:"license"`
	CreatedAt string     `json:"created_at"`
	SkillMeta *SkillMeta `json:"skill_meta,omitempty"` // kind=skill
}

type Software struct {
	Python   string   `json:"python,omitempty"`
	Packages []string `json:"packages"`
}

type Hardware struct {
	MinRAMMB      int      `json:"min_ram_mb"`
	GPUClass      *string  `json:"gpu_class"`
	NetworkEgress []string `json:"network_egress"`
}

type SLAHint struct {
	LatencyP99MS int `json:"latency_p99_ms"`
	WarmupMS     int `json:"warmup_ms"`
}

type SkillMeta struct {
	InstallScopeHint string   `json:"install_scope_hint,omitempty"` // "user" | "project"
	DependsOnSkills  []string `json:"depends_on_skills,omitempty"`
}

var (
	slugPattern   = regexp.MustCompile(`^[a-z0-9_]+$`)
	semverPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$`)
)

// Parse decodes raw bytes into a Manifest using strict JSON (unknown fields rejected).
func Parse(data []byte) (*Manifest, error) {
	if len(data) > 64*1024 {
		return nil, fmt.Errorf("manifest: too large (%d bytes; max 65536)", len(data))
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	return &m, nil
}

// Validate runs structural rules. Callers should also verify the package
// tarball's actual sha256 server-side (Validate cannot do that).
func (m *Manifest) Validate() error {
	if m.SchemaVersion != SchemaVersion {
		return fmt.Errorf("manifest: unsupported schema_version %d", m.SchemaVersion)
	}
	switch m.Kind {
	case KindMCP, KindSkill:
	default:
		return fmt.Errorf("manifest: kind must be mcp or skill (got %q)", m.Kind)
	}
	if !slugPattern.MatchString(m.Slug) {
		return errors.New("manifest: slug must match ^[a-z0-9_]+$")
	}
	if len(m.Slug) == 0 || len(m.Slug) > 64 {
		return errors.New("manifest: slug length 1..64")
	}
	if !semverPattern.MatchString(m.Version) {
		return fmt.Errorf("manifest: version %q not semver", m.Version)
	}
	if m.CardRef == "" {
		return errors.New("manifest: card_ref required")
	}
	if m.Kind == KindMCP && m.SpecRef == "" {
		return errors.New("manifest: spec_ref required when kind=mcp")
	}
	if len(m.Tags) > 32 {
		return errors.New("manifest: too many tags (max 32)")
	}
	for i, t := range m.Tags {
		if len(t) > 64 {
			return fmt.Errorf("manifest: tags[%d] too long (max 64)", i)
		}
	}
	if len(m.Software.Packages) > 64 {
		return errors.New("manifest: too many software.packages (max 64)")
	}
	if len(m.Hardware.NetworkEgress) > 32 {
		return errors.New("manifest: too many hardware.network_egress (max 32)")
	}
	return nil
}
