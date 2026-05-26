package userspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// SkillFrontmatter is the YAML block at the top of SKILL.md (between two
// "---" lines). All fields optional except name.
type SkillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// ParseSkillFrontmatter reads the leading `---\n...\n---\n` block of skill_md.
// Returns the parsed struct + the body bytes (everything after the closing ---).
func ParseSkillFrontmatter(skillMD []byte) (SkillFrontmatter, []byte, error) {
	s := string(skillMD)
	const sep = "---\n"
	if !strings.HasPrefix(s, sep) {
		return SkillFrontmatter{}, nil, errors.New("skillpack: SKILL.md must begin with ---")
	}
	rest := s[len(sep):]
	end := strings.Index(rest, "\n"+sep)
	if end < 0 {
		return SkillFrontmatter{}, nil, errors.New("skillpack: closing --- not found")
	}
	yamlPart := rest[:end+1] // include trailing newline before ---
	body := rest[end+1+len(sep):]
	var fm SkillFrontmatter
	if err := yaml.Unmarshal([]byte(yamlPart), &fm); err != nil {
		return SkillFrontmatter{}, nil, fmt.Errorf("skillpack: parse frontmatter: %w", err)
	}
	if fm.Name == "" {
		return SkillFrontmatter{}, nil, errors.New("skillpack: name field required in frontmatter")
	}
	return fm, []byte(body), nil
}

// InstallScope is "user" (~/.claude/skills/) or "project" (<cwd>/.claude/skills/).
type InstallScope string

const (
	ScopeUser    InstallScope = "user"
	ScopeProject InstallScope = "project"
)

// ResolveSkillDir returns the absolute directory under which to drop the skill.
// projectRoot is only consulted for ScopeProject.
func ResolveSkillDir(scope InstallScope, projectRoot string) (string, error) {
	switch scope {
	case ScopeUser:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".claude", "skills"), nil
	case ScopeProject:
		if projectRoot == "" {
			return "", errors.New("skillpack: project scope requires non-empty projectRoot")
		}
		return filepath.Join(projectRoot, ".claude", "skills"), nil
	default:
		return "", fmt.Errorf("skillpack: unknown scope %q", scope)
	}
}

// SkillFile is a path/content tuple matching the shape of pack.File without
// the cross-package dependency.
type SkillFile struct {
	Path    string
	Content []byte
}

// InstallSkill copies the contents of an unpacked "skill/" subtree into
// <skillsDir>/<name>/, where name is taken from frontmatter. Refuses to
// clobber an existing dir unless overwrite=true.
func InstallSkill(files []SkillFile, skillsDir string, overwrite bool) (string, error) {
	var fmBytes []byte
	var others []SkillFile
	for _, f := range files {
		if f.Path == "skill/SKILL.md" {
			fmBytes = f.Content
		} else {
			others = append(others, f)
		}
	}
	if fmBytes == nil {
		return "", errors.New("skillpack: missing skill/SKILL.md in archive")
	}
	fm, _, err := ParseSkillFrontmatter(fmBytes)
	if err != nil {
		return "", err
	}
	dest := filepath.Join(skillsDir, fm.Name)
	if _, err := os.Stat(dest); err == nil {
		if !overwrite {
			return "", fmt.Errorf("skillpack: %s already exists (use --overwrite)", dest)
		}
		if err := os.RemoveAll(dest); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dest, "SKILL.md"), fmBytes, 0o644); err != nil {
		return "", err
	}
	for _, f := range others {
		rel := strings.TrimPrefix(f.Path, "skill/")
		if rel == f.Path {
			continue
		}
		fullPath := filepath.Join(dest, rel)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			return "", err
		}
		mode := fs.FileMode(0o644)
		if err := os.WriteFile(fullPath, f.Content, mode); err != nil {
			return "", err
		}
	}
	return dest, nil
}
