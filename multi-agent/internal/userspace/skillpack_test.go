package userspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseFrontmatter_Happy(t *testing.T) {
	md := []byte("---\nname: foo\ndescription: bar\n---\nbody here\n")
	fm, body, err := ParseSkillFrontmatter(md)
	require.NoError(t, err)
	require.Equal(t, "foo", fm.Name)
	require.Equal(t, "bar", fm.Description)
	require.Equal(t, "body here\n", string(body))
}

func TestParseFrontmatter_MissingName(t *testing.T) {
	_, _, err := ParseSkillFrontmatter([]byte("---\ndescription: bar\n---\nx"))
	require.ErrorContains(t, err, "name field required")
}

func TestParseFrontmatter_NoOpeningSep(t *testing.T) {
	_, _, err := ParseSkillFrontmatter([]byte("name: foo\n"))
	require.ErrorContains(t, err, "must begin with ---")
}

func TestResolveSkillDir(t *testing.T) {
	u, err := ResolveSkillDir(ScopeUser, "")
	require.NoError(t, err)
	require.Contains(t, u, ".claude/skills")

	p, err := ResolveSkillDir(ScopeProject, "/tmp/myproj")
	require.NoError(t, err)
	require.Equal(t, "/tmp/myproj/.claude/skills", p)

	_, err = ResolveSkillDir(ScopeProject, "")
	require.Error(t, err)
}

func TestInstallSkill_DropsTreeUnderName(t *testing.T) {
	files := []SkillFile{
		{Path: "skill/SKILL.md", Content: []byte("---\nname: hello\n---\nbody\n")},
		{Path: "skill/scripts/run.sh", Content: []byte("#!/bin/bash")},
		{Path: "skill/data/info.txt", Content: []byte("data")},
	}
	root := t.TempDir()
	dest, err := InstallSkill(files, root, false)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "hello"), dest)
	require.FileExists(t, filepath.Join(dest, "SKILL.md"))
	require.FileExists(t, filepath.Join(dest, "scripts/run.sh"))
	require.FileExists(t, filepath.Join(dest, "data/info.txt"))
}

func TestInstallSkill_RefusesOverwriteByDefault(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "hello"), 0o755))
	files := []SkillFile{{Path: "skill/SKILL.md", Content: []byte("---\nname: hello\n---\n")}}
	_, err := InstallSkill(files, root, false)
	require.ErrorContains(t, err, "already exists")
}

func TestInstallSkill_OverwriteWipesOld(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "hello")
	require.NoError(t, os.MkdirAll(old, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(old, "stale.txt"), []byte("x"), 0o644))
	files := []SkillFile{{Path: "skill/SKILL.md", Content: []byte("---\nname: hello\n---\n")}}
	_, err := InstallSkill(files, root, true)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(old, "stale.txt"))
	require.True(t, os.IsNotExist(err), "old file must be wiped on overwrite")
}
