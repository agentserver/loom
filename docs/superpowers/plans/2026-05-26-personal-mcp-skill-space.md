# Personal MCP & Skill Space (observer extension) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把"用户私有 MCP/skill 仓"以子模块形态折进 observer-server：复用 agent token 鉴权、共享 `*sql.DB`、自动跟 observer workspace 绑定；slug 用户级全局唯一（pip 心智），每个 workspace 装一个版本，任何 workspace 都能 push 出新版本（带 `created_in_workspace` provenance）；MCP 与 skill 同收。

**Architecture:** 三层。底层 `internal/mcpmarket/{manifest,pack}` 是 marketplace 与 userspace 共享的基础（manifest 校验 + JCS 规范化 + 确定性 tar.gz）；中层 `internal/userspace/{store,blob,skillpack}` 是业务实现，挂在 observer 的 sqlite 文件上但自有表族（避免污染 observer 现有业务表）；HTTP 路由 `/api/userspace/*` 由 `observerweb` 在 mux 上挂，鉴权走 observer 现有的 agent token 中间件，自动解出 `workspace_id`。CLI `cmd/mcp-userspace` 是纯 HTTP 客户端。

**Tech Stack:** Go 1.x；testify/require；标准库 `database/sql` + `modernc.org/sqlite`；`archive/tar` + `compress/gzip`（确定性打包）；`gopkg.in/yaml.v3`（CLI 配置 + SKILL.md frontmatter）；`encoding/json` + RFC 8785 JCS（manifest 规范化）；现有 `internal/observerstore`、`internal/observerweb`、`internal/buildspec`。

**Working directory for all commands:** `multi-agent/`（Go module 在子目录里；`cd multi-agent` 一次即可）。

**Spec:** `docs/superpowers/specs/2026-05-25-personal-mcp-skill-space-design.md`

---

## File Map

**Create:**
- `multi-agent/internal/mcpmarket/manifest/manifest.go` — `Manifest` struct + `Parse` + `Validate`
- `multi-agent/internal/mcpmarket/manifest/jcs.go` — RFC 8785 JCS canonicalizer
- `multi-agent/internal/mcpmarket/manifest/manifest_test.go`
- `multi-agent/internal/mcpmarket/pack/pack.go` — `WriteTarball` + `ReadTarball` 确定性打包
- `multi-agent/internal/mcpmarket/pack/pack_test.go`
- `multi-agent/internal/userspace/schema.sql` — userspace 表族
- `multi-agent/internal/userspace/migrate.go` — `Migrate(db)` apply schema
- `multi-agent/internal/userspace/store.go` — packages / versions / installations CRUD
- `multi-agent/internal/userspace/store_test.go`
- `multi-agent/internal/userspace/blob.go` — fs sha256 寻址 + refcount
- `multi-agent/internal/userspace/blob_test.go`
- `multi-agent/internal/userspace/skillpack.go` — SKILL.md frontmatter + install scope
- `multi-agent/internal/userspace/skillpack_test.go`
- `multi-agent/internal/userspace/api.go` — chi handlers
- `multi-agent/internal/userspace/api_test.go`
- `multi-agent/internal/userspace/routes.go` — exported `MountRoutes(mux, store, blob, blobRoot)`
- `multi-agent/cmd/mcp-userspace/main.go` — CLI 入口 + subcommand dispatch
- `multi-agent/cmd/mcp-userspace/client.go` — HTTP wrapper
- `multi-agent/cmd/mcp-userspace/config.go` — `~/.mcp-userspace/config.yaml` 解析
- `multi-agent/cmd/mcp-userspace/cmd_*.go` — 一个子命令一个文件（push / pull / search / install / list / yank / login）
- `skills/userspace-publish/SKILL.md` — driver 内 authoring 引导

**Modify:**
- `multi-agent/internal/observerstore/store.go` — add `func (s *Store) DB() *sql.DB`
- `multi-agent/internal/observerweb/server.go` — call `userspace.MountRoutes(mux, ...)` at startup
- `multi-agent/cmd/observer-server/main.go` — call `userspace.Migrate(db)` + `userspace.NewBlobStore(blobRoot)` + pass to web

**Out of scope（推迟到 v1.1）：**
- `internal/mcpmarket/scanner` —— spec §10.3 信息性扫描；不阻塞 install，本期 CLI 占位 "no scan_report"
- `internal/userspace/promote` + `mcp-userspace promote` —— 依赖 marketplace 已落
- Embedding 检索 —— 本期 search 只走 FTS5；spec §3.1 的 `userspace_pkg_embed_*` 表本期不建（schema migration 留扩展位）

---

## Task 1: `internal/mcpmarket/manifest` — manifest schema + JCS

**Goal:** 建共享的 manifest 类型 + 校验 + JCS 规范化。userspace 的 push handler 与 marketplace 未来都用同一份。

**Files:**
- Create: `multi-agent/internal/mcpmarket/manifest/manifest.go`
- Create: `multi-agent/internal/mcpmarket/manifest/jcs.go`
- Create: `multi-agent/internal/mcpmarket/manifest/manifest_test.go`

### Steps

- [ ] **Step 1.1: 写 Manifest struct + 字段（manifest.go 骨架）**

```go
package manifest

import (
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
	SchemaVersion int             `json:"schema_version"`
	Kind          Kind            `json:"kind"`
	Slug          string          `json:"slug"`
	Version       string          `json:"version"`
	// NOTE: tarball_sha256 deliberately NOT in the manifest for v1. The server
	// computes the hash of received bytes server-side and stores it in
	// VersionRow.TarballSHA256. Putting it in the manifest creates a
	// chicken-and-egg with deterministic packing (manifest is inside the tar
	// it claims to hash). Future versions with signatures will need to
	// reintroduce it using a "hash excluding the manifest entry" scheme.
	SpecRef       string          `json:"spec_ref,omitempty"`  // kind=mcp
	CardRef       string          `json:"card_ref"`
	CasesRef      string          `json:"cases_ref,omitempty"` // kind=mcp
	Software      Software        `json:"software"`
	Hardware      Hardware        `json:"hardware"`
	SLAHint       SLAHint         `json:"sla_hint"`
	Tags          []string        `json:"tags"`
	License       string          `json:"license"`
	CreatedAt     string          `json:"created_at"`
	SkillMeta     *SkillMeta      `json:"skill_meta,omitempty"` // kind=skill
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

var slugPattern = regexp.MustCompile(`^[a-z0-9_]+$`)
var semverPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$`)

// Parse decodes raw bytes into a Manifest using strict JSON (unknown fields rejected).
func Parse(data []byte) (*Manifest, error) {
	if len(data) > 64*1024 {
		return nil, fmt.Errorf("manifest: too large (%d bytes; max 65536)", len(data))
	}
	dec := json.NewDecoder(bytesReader(data))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	return &m, nil
}

// Validate runs structural rules. Callers should also verify TarballSHA256
// against the actual tarball bytes (Validate cannot do that).
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
	if m.Kind == KindMCP {
		if m.SpecRef == "" {
			return errors.New("manifest: spec_ref required when kind=mcp")
		}
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

// for testing
func bytesReader(data []byte) *bytesBuf { return &bytesBuf{b: data} }

type bytesBuf struct {
	b []byte
	i int
}

func (r *bytesBuf) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, errEOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}

var errEOF = errors.New("EOF")
```

Note: use `bytes.NewReader` from stdlib instead of the local `bytesBuf` if you prefer; the local helper avoids extra imports. Standard library is cleaner — feel free to swap.

- [ ] **Step 1.2: 加 jcs.go — RFC 8785 canonicalizer**

```go
package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Canonicalize produces RFC 8785 JCS form: keys in codepoint-sorted order,
// no whitespace, ECMA-262 number toString, JSON string escapes per RFC 8259.
// Input must be valid JSON; returns canonical bytes.
func Canonicalize(in []byte) ([]byte, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(in))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("jcs: decode: %w", err)
	}
	var buf bytes.Buffer
	if err := writeJCS(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeJCS(w *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		w.WriteString("null")
	case bool:
		if x {
			w.WriteString("true")
		} else {
			w.WriteString("false")
		}
	case string:
		writeJCSString(w, x)
	case json.Number:
		return writeJCSNumber(w, string(x))
	case []any:
		w.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				w.WriteByte(',')
			}
			if err := writeJCS(w, e); err != nil {
				return err
			}
		}
		w.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys) // codepoint order = lex order for UTF-8
		w.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				w.WriteByte(',')
			}
			writeJCSString(w, k)
			w.WriteByte(':')
			if err := writeJCS(w, x[k]); err != nil {
				return err
			}
		}
		w.WriteByte('}')
	default:
		return fmt.Errorf("jcs: unsupported type %T", v)
	}
	return nil
}

func writeJCSString(w *bytes.Buffer, s string) {
	w.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			w.WriteString(`\"`)
		case '\\':
			w.WriteString(`\\`)
		case '\b':
			w.WriteString(`\b`)
		case '\f':
			w.WriteString(`\f`)
		case '\n':
			w.WriteString(`\n`)
		case '\r':
			w.WriteString(`\r`)
		case '\t':
			w.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(w, `\u%04x`, r)
			} else {
				w.WriteRune(r)
			}
		}
	}
	w.WriteByte('"')
}

func writeJCSNumber(w *bytes.Buffer, raw string) error {
	// ECMA-262 toString: try int first, fall back to float.
	if i, err := strconv.ParseInt(raw, 10, 64); err == nil {
		w.WriteString(strconv.FormatInt(i, 10))
		return nil
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fmt.Errorf("jcs: bad number %q: %w", raw, err)
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	// Normalize 1e+10 → 1e10 etc. for JCS deterministic output.
	s = strings.ReplaceAll(s, "e+0", "e")
	s = strings.ReplaceAll(s, "e+", "e")
	s = strings.ReplaceAll(s, "e-0", "e-")
	w.WriteString(s)
	return nil
}
```

- [ ] **Step 1.3: 写测试 — manifest_test.go**

```go
package manifest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func validManifest() string {
	return `{
		"schema_version": 1,
		"kind": "mcp",
		"slug": "wedding_almanac",
		"version": "1.0.0",
		"spec_ref": "spec.json",
		"card_ref": "capability_card.md",
		"cases_ref": "tests/cases.json",
		"software": {"python": ">=3.10", "packages": []},
		"hardware": {"min_ram_mb": 128, "gpu_class": null, "network_egress": []},
		"sla_hint": {"latency_p99_ms": 800, "warmup_ms": 0},
		"tags": ["a", "b"],
		"license": "MIT",
		"created_at": "2026-05-26T00:00:00Z"
	}`
}

func TestParse_ValidMCPManifest(t *testing.T) {
	m, err := Parse([]byte(validManifest()))
	require.NoError(t, err)
	require.NoError(t, m.Validate())
	require.Equal(t, KindMCP, m.Kind)
	require.Equal(t, "wedding_almanac", m.Slug)
}

func TestParse_RejectsUnknownFields(t *testing.T) {
	raw := strings.Replace(validManifest(), `"license": "MIT"`, `"license": "MIT", "evil_field": 1`, 1)
	_, err := Parse([]byte(raw))
	require.Error(t, err)
}

func TestValidate_RejectsBadSlug(t *testing.T) {
	m, err := Parse([]byte(strings.Replace(validManifest(), `"wedding_almanac"`, `"Wedding-Almanac"`, 1)))
	require.NoError(t, err)
	require.ErrorContains(t, m.Validate(), "slug must match")
}

func TestValidate_RejectsSkillMissingMCPFields(t *testing.T) {
	raw := strings.Replace(validManifest(), `"kind": "mcp"`, `"kind": "skill"`, 1)
	raw = strings.Replace(raw, `"spec_ref": "spec.json",`, "", 1)
	raw = strings.Replace(raw, `"cases_ref": "tests/cases.json",`, "", 1)
	m, err := Parse([]byte(raw))
	require.NoError(t, err)
	require.NoError(t, m.Validate(), "skill without spec_ref/cases_ref must be valid")
}

func TestValidate_MCPRequiresSpecRef(t *testing.T) {
	raw := strings.Replace(validManifest(), `"spec_ref": "spec.json",`, "", 1)
	m, err := Parse([]byte(raw))
	require.NoError(t, err)
	require.ErrorContains(t, m.Validate(), "spec_ref required when kind=mcp")
}

func TestCanonicalize_KeyOrderStable(t *testing.T) {
	a := []byte(`{"b":2,"a":1,"c":{"y":2,"x":1}}`)
	b := []byte(`{"a":1,"c":{"x":1,"y":2},"b":2}`)
	ca, err := Canonicalize(a)
	require.NoError(t, err)
	cb, err := Canonicalize(b)
	require.NoError(t, err)
	require.Equal(t, ca, cb)
	require.Equal(t, `{"a":1,"b":2,"c":{"x":1,"y":2}}`, string(ca))
}

func TestCanonicalize_StripsWhitespace(t *testing.T) {
	out, err := Canonicalize([]byte("{\n  \"x\": 1\n}"))
	require.NoError(t, err)
	require.Equal(t, `{"x":1}`, string(out))
}

func TestCanonicalize_NumberToString(t *testing.T) {
	out, err := Canonicalize([]byte(`{"i":128,"f":1.5}`))
	require.NoError(t, err)
	require.Equal(t, `{"f":1.5,"i":128}`, string(out))
}
```

- [ ] **Step 1.4: 跑测**

```bash
cd multi-agent
go test ./internal/mcpmarket/manifest/... -v
```

Expected: all PASS.

- [ ] **Step 1.5: Commit**

```bash
cd multi-agent
git add internal/mcpmarket/manifest/
git commit -m "feat(mcpmarket/manifest): shared manifest schema + JCS canonicalizer

- Manifest struct covers both kind=mcp and kind=skill
- Parse rejects unknown fields (strict); 64KiB cap
- Validate enforces slug regex, semver, sha256 length, kind-specific refs,
  tag/package count limits
- Canonicalize implements RFC 8785 JCS: key codepoint sort, no whitespace,
  ECMA-262 number toString, RFC 8259 string escapes"
```

---

## Task 2: `internal/mcpmarket/pack` — 确定性 tar.gz

**Goal:** 唯一的 pack/unpack 实现：USTAR 格式 + mtime=0 + 字典序 + 权限标准化；解包时 zip-slip 防护。

**Files:**
- Create: `multi-agent/internal/mcpmarket/pack/pack.go`
- Create: `multi-agent/internal/mcpmarket/pack/pack_test.go`

### Steps

- [ ] **Step 2.1: 写 pack.go**

```go
package pack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Limits — enforced by both pack and unpack.
const (
	MaxCompressedBytes   = 10 * 1024 * 1024 // 10 MiB
	MaxUncompressedBytes = 50 * 1024 * 1024 // 50 MiB
	MaxFileBytes         = 5 * 1024 * 1024  // 5 MiB
	MaxFileCount         = 1024
)

// File is one entry to pack. Path is the relative path inside the
// tarball-prefix directory; Content is its bytes.
type File struct {
	Path    string
	Content []byte
	Mode    os.FileMode // 0644 for files, 0755 for dirs (auto-coerced)
}

// WriteTarball serializes files into a deterministic .tar.gz.
// All entries are placed under "<prefix>/" inside the archive.
// Returns the compressed bytes and their sha256 hex digest.
func WriteTarball(prefix string, files []File) ([]byte, string, error) {
	if prefix == "" {
		return nil, "", errors.New("pack: prefix required")
	}
	if len(files) > MaxFileCount {
		return nil, "", fmt.Errorf("pack: too many files (%d > %d)", len(files), MaxFileCount)
	}

	// Sort by path (codepoint) for determinism.
	sorted := append([]File(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	// Validate paths: no leading slash, no .., utf-8 only.
	seen := map[string]bool{}
	var totalUncompressed int64
	for _, f := range sorted {
		if err := checkRelPath(f.Path); err != nil {
			return nil, "", err
		}
		if len(f.Content) > MaxFileBytes {
			return nil, "", fmt.Errorf("pack: file %q exceeds %d bytes", f.Path, MaxFileBytes)
		}
		if seen[f.Path] {
			return nil, "", fmt.Errorf("pack: duplicate entry %q", f.Path)
		}
		seen[f.Path] = true
		totalUncompressed += int64(len(f.Content))
	}
	if totalUncompressed > MaxUncompressedBytes {
		return nil, "", fmt.Errorf("pack: total uncompressed %d exceeds %d", totalUncompressed, MaxUncompressedBytes)
	}

	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, "", err
	}
	gz.Header.OS = 255 // deterministic
	gz.Header.ModTime = epoch()
	tw := tar.NewWriter(gz)

	// Tar entries: each file. We do NOT emit explicit directory entries; tar
	// readers don't need them and skipping them removes another non-determinism.
	for _, f := range sorted {
		entryName := prefix + "/" + f.Path
		if len(entryName) > 100 {
			return nil, "", fmt.Errorf("pack: path too long for USTAR: %q", entryName)
		}
		hdr := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     entryName,
			Size:     int64(len(f.Content)),
			Mode:     0o644,
			ModTime:  epoch(),
			Uid:      0,
			Gid:      0,
			Uname:    "",
			Gname:    "",
			Format:   tar.FormatUSTAR,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, "", err
		}
		if _, err := tw.Write(f.Content); err != nil {
			return nil, "", err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, "", err
	}
	if err := gz.Close(); err != nil {
		return nil, "", err
	}

	out := buf.Bytes()
	if len(out) > MaxCompressedBytes {
		return nil, "", fmt.Errorf("pack: compressed %d exceeds %d", len(out), MaxCompressedBytes)
	}

	h := sha256.Sum256(out)
	return out, hex.EncodeToString(h[:]), nil
}

// ReadTarball reverses WriteTarball with zip-slip / symlink / size protection.
// Returns the prefix directory name and the file list.
func ReadTarball(data []byte) (prefix string, files []File, err error) {
	if len(data) > MaxCompressedBytes {
		return "", nil, fmt.Errorf("unpack: compressed %d exceeds %d", len(data), MaxCompressedBytes)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return "", nil, fmt.Errorf("unpack: gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", nil, fmt.Errorf("unpack: tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			return "", nil, fmt.Errorf("unpack: entry %q has unsupported type %c", hdr.Name, hdr.Typeflag)
		}
		// Path must be "<prefix>/<rest>" with no absolute, no ".."
		name := path.Clean(hdr.Name)
		if name != hdr.Name {
			return "", nil, fmt.Errorf("unpack: non-canonical path %q", hdr.Name)
		}
		if strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
			return "", nil, fmt.Errorf("unpack: unsafe path %q", hdr.Name)
		}
		parts := strings.SplitN(name, "/", 2)
		if len(parts) != 2 {
			return "", nil, fmt.Errorf("unpack: entry %q missing prefix dir", hdr.Name)
		}
		if prefix == "" {
			prefix = parts[0]
		} else if prefix != parts[0] {
			return "", nil, fmt.Errorf("unpack: multiple prefixes (%q and %q)", prefix, parts[0])
		}

		if hdr.Size > MaxFileBytes {
			return "", nil, fmt.Errorf("unpack: file %q size %d exceeds %d", hdr.Name, hdr.Size, MaxFileBytes)
		}
		total += hdr.Size
		if total > MaxUncompressedBytes {
			return "", nil, fmt.Errorf("unpack: total uncompressed exceeds %d", MaxUncompressedBytes)
		}
		if len(files) >= MaxFileCount {
			return "", nil, fmt.Errorf("unpack: too many files (>%d)", MaxFileCount)
		}

		body, err := io.ReadAll(io.LimitReader(tr, MaxFileBytes+1))
		if err != nil {
			return "", nil, fmt.Errorf("unpack: read %q: %w", hdr.Name, err)
		}
		if int64(len(body)) != hdr.Size {
			return "", nil, fmt.Errorf("unpack: short read on %q", hdr.Name)
		}
		files = append(files, File{Path: parts[1], Content: body, Mode: 0o644})
	}
	if prefix == "" {
		return "", nil, errors.New("unpack: empty archive")
	}
	return prefix, files, nil
}

// FilesFromDir reads a directory tree as []File ready for WriteTarball.
// Skips entries whose names start with ".".
func FilesFromDir(root string) ([]File, error) {
	var out []File
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && p != root {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out = append(out, File{Path: rel, Content: body, Mode: 0o644})
		return nil
	})
	return out, err
}

func checkRelPath(p string) error {
	if p == "" {
		return errors.New("pack: empty path")
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("pack: absolute path %q", p)
	}
	if strings.Contains(p, "..") {
		return fmt.Errorf("pack: %q contains ..", p)
	}
	clean := path.Clean(p)
	if clean != p {
		return fmt.Errorf("pack: non-canonical path %q (expected %q)", p, clean)
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("pack: path %q contains control char", p)
		}
	}
	return nil
}

// epoch returns a frozen UTC zero time. tar headers and gzip ModTime use it
// so the same input bytes produce the same archive bytes.
func epoch() time.Time { return time.Unix(0, 0).UTC() }
```

Don't forget `import "time"` at the top.

- [ ] **Step 2.2: 写测试 — pack_test.go**

```go
package pack

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWriteTarball_Deterministic(t *testing.T) {
	files := []File{
		{Path: "b.txt", Content: []byte("two")},
		{Path: "a.txt", Content: []byte("one")},
		{Path: "sub/c.txt", Content: []byte("three")},
	}
	var firstBytes []byte
	var firstHash string
	for i := 0; i < 10; i++ {
		out, hash, err := WriteTarball("pkg-foo-1.0.0", files)
		require.NoError(t, err)
		if i == 0 {
			firstBytes = out
			firstHash = hash
		} else {
			require.True(t, bytes.Equal(out, firstBytes), "run %d differs from run 0", i)
			require.Equal(t, firstHash, hash)
		}
	}
}

func TestWriteThenRead_RoundTrip(t *testing.T) {
	in := []File{
		{Path: "manifest.json", Content: []byte(`{"x":1}`)},
		{Path: "src/server.py", Content: []byte("print('hi')")},
	}
	tgz, _, err := WriteTarball("pkg-foo-1.0.0", in)
	require.NoError(t, err)
	prefix, out, err := ReadTarball(tgz)
	require.NoError(t, err)
	require.Equal(t, "pkg-foo-1.0.0", prefix)
	require.Len(t, out, 2)
	// Paths after read are sorted by tar order, which equals our written order.
	require.Equal(t, "manifest.json", out[0].Path)
	require.Equal(t, "src/server.py", out[1].Path)
	require.Equal(t, in[0].Content, out[0].Content)
}

func TestReadTarball_RejectsZipSlip(t *testing.T) {
	files := []File{{Path: "../escape.txt", Content: []byte("nope")}}
	// Bypass pack's validation by hand-crafting (use a separate test or fixture)
	// — here we just verify pack itself rejects on write.
	_, _, err := WriteTarball("pkg", files)
	require.Error(t, err)
	require.Contains(t, err.Error(), "..")
}

func TestReadTarball_RejectsAbsolutePath(t *testing.T) {
	files := []File{{Path: "/etc/passwd", Content: []byte("x")}}
	_, _, err := WriteTarball("pkg", files)
	require.Error(t, err)
}

func TestWriteTarball_RejectsOversizedFile(t *testing.T) {
	big := make([]byte, MaxFileBytes+1)
	_, _, err := WriteTarball("pkg", []File{{Path: "big.bin", Content: big}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds")
}

func TestWriteTarball_PrefixDirRequired(t *testing.T) {
	_, _, err := WriteTarball("", []File{{Path: "x.txt", Content: []byte("y")}})
	require.Error(t, err)
}

func TestWriteTarball_RejectsDuplicate(t *testing.T) {
	_, _, err := WriteTarball("pkg", []File{
		{Path: "x.txt", Content: []byte("a")},
		{Path: "x.txt", Content: []byte("b")},
	})
	require.Error(t, err)
}
```

- [ ] **Step 2.3: 跑测**

```bash
cd multi-agent
go test ./internal/mcpmarket/pack/... -v
```

Expected: all PASS. Determinism test passes 10 iterations.

- [ ] **Step 2.4: Commit**

```bash
cd multi-agent
git add internal/mcpmarket/pack/
git commit -m "feat(mcpmarket/pack): deterministic tar.gz + zip-slip-safe unpack

USTAR format, mtime=0, sorted entries, mode normalized, gzip OS=unknown.
Size limits enforced (10MiB compressed / 50MiB uncompressed / 5MiB per file
/ 1024 files). ReadTarball rejects non-regular entries, absolute paths,
.., non-canonical paths, multiple prefixes."
```

---

## Task 3: `observerstore.Store.DB()` + `internal/userspace` schema + Migrate

**Goal:** 让 userspace 子包能借到 observer 的 `*sql.DB`；建 schema + migrate 函数；observer-server 启动调一次。

**Files:**
- Modify: `multi-agent/internal/observerstore/store.go` — add `func (s *Store) DB() *sql.DB`
- Create: `multi-agent/internal/userspace/schema.sql`
- Create: `multi-agent/internal/userspace/migrate.go`
- Create: `multi-agent/internal/userspace/migrate_test.go`

### Steps

- [ ] **Step 3.1: 在 observerstore/store.go 暴露 DB()**

找到 `Store` 类型定义（通常在 store.go 顶部），追加：

```go
// DB returns the underlying *sql.DB so sibling packages (internal/userspace,
// future internal/marketplace) can attach their own tables to the same
// SQLite file. Callers MUST keep their table names in their own namespace
// (e.g. userspace_*) — do NOT query observer's business tables (events,
// tasks, artifacts, agents, workspaces) via this handle.
func (s *Store) DB() *sql.DB { return s.db }
```

This change has no behavioral impact on observer; it's purely a getter.

- [ ] **Step 3.2: 写 schema.sql**

```sql
-- userspace tables. Namespace prefix: userspace_*.
-- Shares the SQLite file with observerstore but owns its own table family.

CREATE TABLE IF NOT EXISTS userspace_packages (
    slug         TEXT PRIMARY KEY,
    kind         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    tags_json    TEXT NOT NULL DEFAULT '[]',
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS userspace_blobs (
    sha256       TEXT PRIMARY KEY,
    size_bytes   INTEGER NOT NULL,
    blob_path    TEXT NOT NULL,
    refcount     INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS userspace_package_versions (
    slug                  TEXT NOT NULL REFERENCES userspace_packages(slug),
    version               TEXT NOT NULL,
    created_in_workspace  TEXT NOT NULL,
    created_by_agent_id   TEXT NOT NULL,
    manifest_json         TEXT NOT NULL,
    spec_json             TEXT,
    card_md               TEXT NOT NULL,
    tarball_sha256        TEXT NOT NULL,
    blob_sha256           TEXT NOT NULL REFERENCES userspace_blobs(sha256),
    status                TEXT NOT NULL DEFAULT 'ready',
    created_at            TEXT NOT NULL,
    PRIMARY KEY (slug, version)
);
CREATE INDEX IF NOT EXISTS idx_uspv_workspace ON userspace_package_versions(created_in_workspace);

CREATE TABLE IF NOT EXISTS userspace_workspace_installations (
    workspace_id          TEXT NOT NULL REFERENCES workspaces(id),
    slug                  TEXT NOT NULL REFERENCES userspace_packages(slug),
    installed_version     TEXT NOT NULL,
    installed_at          TEXT NOT NULL,
    installed_by_agent_id TEXT NOT NULL,
    PRIMARY KEY (workspace_id, slug),
    FOREIGN KEY (slug, installed_version) REFERENCES userspace_package_versions(slug, version)
);

-- FTS5 for search. content='userspace_package_versions' makes it auto-track,
-- but we'll populate it ourselves on insert to keep control.
CREATE VIRTUAL TABLE IF NOT EXISTS userspace_pkg_fts USING fts5(
    slug, description, card_md
);
```

- [ ] **Step 3.3: 写 migrate.go**

```go
package userspace

import (
	"database/sql"
	_ "embed"
)

//go:embed schema.sql
var schemaSQL string

// Migrate creates userspace tables if missing. Idempotent — safe to call
// on every observer-server startup. Does NOT touch observer's business tables.
func Migrate(db *sql.DB) error {
	_, err := db.Exec(schemaSQL)
	return err
}
```

- [ ] **Step 3.4: 写 migrate_test.go**

```go
package userspace

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	// userspace schema references workspaces(id); create a stub.
	_, err = db.Exec(`CREATE TABLE workspaces (id TEXT PRIMARY KEY)`)
	require.NoError(t, err)
	require.NoError(t, Migrate(db))
	return db
}

func TestMigrate_Idempotent(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, Migrate(db)) // second run
	require.NoError(t, Migrate(db)) // third run
}

func TestMigrate_CreatesAllTables(t *testing.T) {
	db := newTestDB(t)
	for _, table := range []string{
		"userspace_packages",
		"userspace_package_versions",
		"userspace_workspace_installations",
		"userspace_blobs",
		"userspace_pkg_fts",
	} {
		var name string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE name=?`, table).Scan(&name)
		require.NoError(t, err, "table %s missing", table)
	}
}
```

- [ ] **Step 3.5: 跑测**

```bash
cd multi-agent
go test ./internal/userspace/... -v
go test ./internal/observerstore/... -v   # DB() addition shouldn't break anything
```

Expected: all PASS.

- [ ] **Step 3.6: Commit**

```bash
cd multi-agent
git add internal/observerstore/store.go internal/userspace/
git commit -m "feat(userspace): schema migrate + observerstore.Store.DB() accessor

- Store.DB() returns the underlying *sql.DB for sibling packages to attach
  their own table family (here: userspace_*). Documented invariant: callers
  must stay in their own namespace, not query observer's business tables.
- userspace/schema.sql defines packages / package_versions / installations
  / blobs / FTS5 search table; all PKs prefixed userspace_.
- Migrate(db) is idempotent and safe at every observer-server startup."
```

---

## Task 4: `internal/userspace/store.go` — packages / versions / installations CRUD

**Goal:** 所有 userspace 业务表的 CRUD 都集中在一个 Store 类型；不直接暴露 `*sql.DB`，避免 api / cli 误碰 observer 业务表。

**Files:**
- Create: `multi-agent/internal/userspace/store.go`
- Create: `multi-agent/internal/userspace/store_test.go` (extend the existing one created in Task 3)

### Steps

- [ ] **Step 4.1: 写 store.go 类型与构造**

```go
package userspace

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Store wraps the userspace tables. It deliberately does NOT expose the
// raw *sql.DB so callers can't accidentally read observer's business tables.
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// PackageRow mirrors userspace_packages.
type PackageRow struct {
	Slug        string
	Kind        string
	Description string
	Tags        []string
	CreatedAt   string
	UpdatedAt   string
}

// VersionRow mirrors userspace_package_versions.
type VersionRow struct {
	Slug               string
	Version            string
	CreatedInWorkspace string
	CreatedByAgentID   string
	ManifestJSON       []byte
	SpecJSON           []byte // may be nil for kind=skill
	CardMD             string
	TarballSHA256      string
	BlobSHA256         string
	Status             string
	CreatedAt          string
}

// InstallationRow mirrors userspace_workspace_installations.
type InstallationRow struct {
	WorkspaceID       string
	Slug              string
	InstalledVersion  string
	InstalledAt       string
	InstalledByAgent  string
}

// PackageView is the search/list output shape; description comes from the
// owning package, installed_version from the requesting workspace.
type PackageView struct {
	Slug             string   `json:"slug"`
	Kind             string   `json:"kind"`
	Description      string   `json:"description"`
	Tags             []string `json:"tags"`
	LatestVersion    string   `json:"latest_version"`
	InstalledVersion string   `json:"installed_version,omitempty"` // for caller's workspace
}
```

- [ ] **Step 4.2: 写 UpsertPackage + GetPackage**

```go
// UpsertPackage inserts a new row or updates description/tags/updated_at.
// Kind is INSERT-only — once set for a slug, conflict updates do not change it
// (caller responsible for rejecting kind mismatch upstream).
func (s *Store) UpsertPackage(p PackageRow) error {
	if p.Slug == "" {
		return errors.New("userspace: slug required")
	}
	tagsJSON, err := json.Marshal(p.Tags)
	if err != nil {
		return err
	}
	if p.Tags == nil {
		tagsJSON = []byte("[]")
	}
	now := nowUTC()
	_, err = s.db.Exec(`
		INSERT INTO userspace_packages(slug, kind, description, tags_json, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(slug) DO UPDATE SET
		    description = excluded.description,
		    tags_json   = excluded.tags_json,
		    updated_at  = excluded.updated_at`,
		p.Slug, p.Kind, p.Description, string(tagsJSON), now, now)
	return err
}

// GetPackage returns the package row or (nil, nil) if not found.
func (s *Store) GetPackage(slug string) (*PackageRow, error) {
	var p PackageRow
	var tagsJSON string
	err := s.db.QueryRow(`
		SELECT slug, kind, description, tags_json, created_at, updated_at
		  FROM userspace_packages WHERE slug=?`, slug,
	).Scan(&p.Slug, &p.Kind, &p.Description, &tagsJSON, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(tagsJSON), &p.Tags); err != nil {
		return nil, fmt.Errorf("userspace: parse tags_json: %w", err)
	}
	return &p, nil
}
```

- [ ] **Step 4.3: 写 InsertVersion + ListVersions + GetVersion**

```go
// InsertVersion inserts a new version row. The caller must have already
// inserted/UpsertPackage(slug, kind) and incremented the blob refcount.
// Conflict on (slug, version) returns ErrVersionExists.
var ErrVersionExists = errors.New("userspace: version already exists")

func (s *Store) InsertVersion(v VersionRow) error {
	if v.Slug == "" || v.Version == "" {
		return errors.New("userspace: slug + version required")
	}
	if v.Status == "" {
		v.Status = "ready"
	}
	v.CreatedAt = nowUTC()
	res, err := s.db.Exec(`
		INSERT OR IGNORE INTO userspace_package_versions
		  (slug, version, created_in_workspace, created_by_agent_id,
		   manifest_json, spec_json, card_md, tarball_sha256, blob_sha256,
		   status, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		v.Slug, v.Version, v.CreatedInWorkspace, v.CreatedByAgentID,
		string(v.ManifestJSON), nullIfEmpty(v.SpecJSON), v.CardMD,
		v.TarballSHA256, v.BlobSHA256, v.Status, v.CreatedAt)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrVersionExists
	}
	// Mirror into FTS5.
	_, err = s.db.Exec(`
		INSERT INTO userspace_pkg_fts(slug, description, card_md)
		VALUES(?, ?, ?)`,
		v.Slug, "" /*description tracked via packages*/, v.CardMD)
	return err
}

func (s *Store) GetVersion(slug, version string) (*VersionRow, error) {
	var v VersionRow
	var specJSON sql.NullString
	err := s.db.QueryRow(`
		SELECT slug, version, created_in_workspace, created_by_agent_id,
		       manifest_json, spec_json, card_md, tarball_sha256, blob_sha256,
		       status, created_at
		  FROM userspace_package_versions WHERE slug=? AND version=?`,
		slug, version,
	).Scan(&v.Slug, &v.Version, &v.CreatedInWorkspace, &v.CreatedByAgentID,
		&v.ManifestJSON, &specJSON, &v.CardMD, &v.TarballSHA256, &v.BlobSHA256,
		&v.Status, &v.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if specJSON.Valid {
		v.SpecJSON = []byte(specJSON.String)
	}
	return &v, nil
}

// ListVersions returns all versions for a slug, newest first by created_at.
func (s *Store) ListVersions(slug string) ([]VersionRow, error) {
	rows, err := s.db.Query(`
		SELECT slug, version, created_in_workspace, created_by_agent_id,
		       tarball_sha256, blob_sha256, status, created_at
		  FROM userspace_package_versions WHERE slug=?
		 ORDER BY created_at DESC`, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VersionRow
	for rows.Next() {
		var v VersionRow
		if err := rows.Scan(&v.Slug, &v.Version, &v.CreatedInWorkspace,
			&v.CreatedByAgentID, &v.TarballSHA256, &v.BlobSHA256,
			&v.Status, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// YankVersion soft-deletes a version (search hides it; installs unaffected).
func (s *Store) YankVersion(slug, version string) error {
	res, err := s.db.Exec(
		`UPDATE userspace_package_versions SET status='yanked'
		 WHERE slug=? AND version=? AND status='ready'`, slug, version)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func nullIfEmpty(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}
```

- [ ] **Step 4.4: 写 UpsertInstallation + GetInstallation + ListInstallations + DeleteInstallation**

```go
// UpsertInstallation sets the workspace's currently-installed version for slug.
// Replaces any previous installed_version. The (slug, version) row must exist.
func (s *Store) UpsertInstallation(in InstallationRow) error {
	in.InstalledAt = nowUTC()
	_, err := s.db.Exec(`
		INSERT INTO userspace_workspace_installations
		  (workspace_id, slug, installed_version, installed_at, installed_by_agent_id)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id, slug) DO UPDATE SET
		    installed_version     = excluded.installed_version,
		    installed_at          = excluded.installed_at,
		    installed_by_agent_id = excluded.installed_by_agent_id`,
		in.WorkspaceID, in.Slug, in.InstalledVersion,
		in.InstalledAt, in.InstalledByAgent)
	return err
}

// GetInstallation returns this workspace's installed version of slug, or
// ("", false, nil) if not installed.
func (s *Store) GetInstallation(workspaceID, slug string) (string, bool, error) {
	var v string
	err := s.db.QueryRow(`
		SELECT installed_version FROM userspace_workspace_installations
		 WHERE workspace_id=? AND slug=?`, workspaceID, slug).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// ListInstallations returns all packages installed in the given workspace.
func (s *Store) ListInstallations(workspaceID string) ([]InstallationRow, error) {
	rows, err := s.db.Query(`
		SELECT workspace_id, slug, installed_version, installed_at, installed_by_agent_id
		  FROM userspace_workspace_installations
		 WHERE workspace_id=?
		 ORDER BY installed_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InstallationRow
	for rows.Next() {
		var in InstallationRow
		if err := rows.Scan(&in.WorkspaceID, &in.Slug, &in.InstalledVersion,
			&in.InstalledAt, &in.InstalledByAgent); err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

func (s *Store) DeleteInstallation(workspaceID, slug string) error {
	_, err := s.db.Exec(
		`DELETE FROM userspace_workspace_installations
		 WHERE workspace_id=? AND slug=?`, workspaceID, slug)
	return err
}
```

- [ ] **Step 4.5: 写 SearchPackages + ListPackages (FTS5)**

```go
// SearchPackages runs the FTS5 query and returns up to limit results, each
// joined with the latest version + caller's installed_version (if any).
// q="" lists all packages.
func (s *Store) SearchPackages(q, workspaceID, kindFilter string, limit int) ([]PackageView, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	args := []any{}
	where := []string{}
	from := "userspace_packages p"
	if q != "" {
		from = `userspace_pkg_fts f JOIN userspace_packages p ON p.slug = f.slug`
		where = append(where, `f.userspace_pkg_fts MATCH ?`)
		args = append(args, q)
	}
	if kindFilter != "" && kindFilter != "all" {
		where = append(where, `p.kind = ?`)
		args = append(args, kindFilter)
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = "WHERE " + joinAnd(where)
	}
	query := fmt.Sprintf(`
		SELECT p.slug, p.kind, p.description, p.tags_json,
		       COALESCE((SELECT version FROM userspace_package_versions v
		                  WHERE v.slug=p.slug AND v.status='ready'
		                  ORDER BY v.created_at DESC LIMIT 1), '') AS latest_version,
		       COALESCE((SELECT installed_version FROM userspace_workspace_installations i
		                  WHERE i.workspace_id=? AND i.slug=p.slug), '') AS installed_version
		  FROM %s %s
		 ORDER BY p.updated_at DESC
		 LIMIT ?`, from, whereSQL)
	finalArgs := append([]any{workspaceID}, args...)
	finalArgs = append(finalArgs, limit)
	rows, err := s.db.Query(query, finalArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PackageView
	for rows.Next() {
		var pv PackageView
		var tagsJSON string
		if err := rows.Scan(&pv.Slug, &pv.Kind, &pv.Description, &tagsJSON,
			&pv.LatestVersion, &pv.InstalledVersion); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(tagsJSON), &pv.Tags)
		out = append(out, pv)
	}
	return out, rows.Err()
}

func joinAnd(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " AND "
		}
		out += p
	}
	return out
}
```

- [ ] **Step 4.6: 写测试 store_test.go**

Append to the file from Task 3:

```go
func TestUpsertPackage_FirstWriterSetsKind(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	require.NoError(t, s.UpsertPackage(PackageRow{Slug: "foo", Kind: "mcp", Description: "first"}))
	require.NoError(t, s.UpsertPackage(PackageRow{Slug: "foo", Kind: "mcp", Description: "second"}))
	p, err := s.GetPackage("foo")
	require.NoError(t, err)
	require.Equal(t, "mcp", p.Kind)
	require.Equal(t, "second", p.Description)
}

func TestInsertVersion_ConflictReturnsErrVersionExists(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	require.NoError(t, db.Exec(`INSERT INTO workspaces(id) VALUES('ws-a')`).Err)
	require.NoError(t, s.UpsertPackage(PackageRow{Slug: "foo", Kind: "mcp"}))
	require.NoError(t, db.Exec(`INSERT INTO userspace_blobs(sha256,size_bytes,blob_path,created_at) VALUES('h1',10,'p1',?)`, nowUTC()).Err)
	v := VersionRow{Slug: "foo", Version: "1.0.0", CreatedInWorkspace: "ws-a", CreatedByAgentID: "a1",
		ManifestJSON: []byte(`{}`), CardMD: "card", TarballSHA256: "h1", BlobSHA256: "h1"}
	require.NoError(t, s.InsertVersion(v))
	require.ErrorIs(t, s.InsertVersion(v), ErrVersionExists)
}

func TestInstallation_RoundTrip(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	_, _ = db.Exec(`INSERT INTO workspaces(id) VALUES('ws-a'),('ws-b')`)
	require.NoError(t, s.UpsertPackage(PackageRow{Slug: "foo", Kind: "mcp"}))
	_, _ = db.Exec(`INSERT INTO userspace_blobs(sha256,size_bytes,blob_path,created_at) VALUES('h1',10,'p1',?)`, nowUTC())
	require.NoError(t, s.InsertVersion(VersionRow{Slug: "foo", Version: "1.0.0", CreatedInWorkspace: "ws-a", CreatedByAgentID: "a1",
		ManifestJSON: []byte(`{}`), CardMD: "x", TarballSHA256: "h1", BlobSHA256: "h1"}))
	require.NoError(t, s.UpsertInstallation(InstallationRow{WorkspaceID: "ws-b", Slug: "foo", InstalledVersion: "1.0.0", InstalledByAgent: "a-b"}))
	v, ok, err := s.GetInstallation("ws-b", "foo")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "1.0.0", v)
	// ws-a has no install of foo
	_, ok2, _ := s.GetInstallation("ws-a", "foo")
	require.False(t, ok2)
}

func TestSearchPackages_FTSFindsByCardMD(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	_, _ = db.Exec(`INSERT INTO workspaces(id) VALUES('ws-a')`)
	_, _ = db.Exec(`INSERT INTO userspace_blobs(sha256,size_bytes,blob_path,created_at) VALUES('h1',10,'p1',?)`, nowUTC())
	require.NoError(t, s.UpsertPackage(PackageRow{Slug: "invoice_extract", Kind: "mcp", Description: "PDF tables"}))
	require.NoError(t, s.InsertVersion(VersionRow{Slug: "invoice_extract", Version: "1.0.0",
		CreatedInWorkspace: "ws-a", CreatedByAgentID: "x", ManifestJSON: []byte(`{}`),
		CardMD: "extracts invoice tables from pdf", TarballSHA256: "h1", BlobSHA256: "h1"}))
	results, err := s.SearchPackages("invoice", "ws-a", "mcp", 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "invoice_extract", results[0].Slug)
	require.Equal(t, "1.0.0", results[0].LatestVersion)
	require.Equal(t, "", results[0].InstalledVersion)
}
```

(`db.Exec(...).Err` doesn't compile — use `_, err := db.Exec(...); require.NoError(t, err)` pattern. Adjust on commit.)

- [ ] **Step 4.7: 跑测**

```bash
cd multi-agent
go test ./internal/userspace/... -v
```

Expected: all PASS.

- [ ] **Step 4.8: Commit**

```bash
cd multi-agent
git add internal/userspace/
git commit -m "feat(userspace): store CRUD — packages, versions, installations, FTS5 search

Store wraps *sql.DB privately so callers can't reach into observer's tables.
Versions are append-only with ErrVersionExists on (slug, version) conflict.
Installations are upsert-keyed by (workspace_id, slug). SearchPackages joins
FTS5 results with the latest ready version + the caller's installed_version
column (empty if not installed in caller's workspace)."
```

---

## Task 5: `internal/userspace/blob.go` — fs sha256 store + refcount

**Goal:** 大 blob 走文件系统（不内联 SQLite），sha256 内容寻址，跨 slug 跨版本去重；refcount 由 InsertVersion / DeleteVersion / yank-when-deleted 更新。

**Files:**
- Create: `multi-agent/internal/userspace/blob.go`
- Create: `multi-agent/internal/userspace/blob_test.go`

### Steps

- [ ] **Step 5.1: 写 blob.go**

```go
package userspace

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// BlobStore is sha256-addressed file storage with refcount in SQLite.
// Two callers writing the same bytes increment the same refcount.
// On refcount → 0 the row stays (with refcount=0) but the file is removed.
// (Lazy table cleanup can be added later.)
type BlobStore struct {
	db   *sql.DB
	root string
}

func NewBlobStore(db *sql.DB, root string) (*BlobStore, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &BlobStore{db: db, root: root}, nil
}

// Put writes content (if not already present) and increments refcount.
// Returns the sha256 hex digest.
func (b *BlobStore) Put(content []byte) (string, error) {
	sum := sha256.Sum256(content)
	hexsum := hex.EncodeToString(sum[:])
	path := b.pathFor(hexsum)

	// Check existing row.
	var existing int
	err := b.db.QueryRow(`SELECT refcount FROM userspace_blobs WHERE sha256=?`, hexsum).Scan(&existing)
	switch {
	case err == sql.ErrNoRows:
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return "", err
		}
		_, err = b.db.Exec(`
			INSERT INTO userspace_blobs(sha256, size_bytes, blob_path, refcount, created_at)
			VALUES(?, ?, ?, 1, ?)`,
			hexsum, len(content), filepath.Join(blobShard(hexsum), hexsum), nowUTC())
		if err != nil {
			os.Remove(path)
			return "", err
		}
		return hexsum, nil
	case err != nil:
		return "", err
	default:
		_, err = b.db.Exec(`UPDATE userspace_blobs SET refcount = refcount + 1 WHERE sha256=?`, hexsum)
		return hexsum, err
	}
}

// Open returns a ReadCloser for the blob; not found → (nil, ErrBlobNotFound).
var ErrBlobNotFound = errors.New("userspace: blob not found")

func (b *BlobStore) Open(sha256hex string) (io.ReadCloser, int64, error) {
	var sz int64
	err := b.db.QueryRow(`SELECT size_bytes FROM userspace_blobs WHERE sha256=? AND refcount > 0`,
		sha256hex).Scan(&sz)
	if err == sql.ErrNoRows {
		return nil, 0, ErrBlobNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(b.pathFor(sha256hex))
	if err != nil {
		return nil, 0, err
	}
	return f, sz, nil
}

// Release decrements refcount. On zero, the file is unlinked; the row stays
// (refcount=0) so the next Put with the same content can recreate the file
// without losing the audit trail.
func (b *BlobStore) Release(sha256hex string) error {
	_, err := b.db.Exec(
		`UPDATE userspace_blobs SET refcount = refcount - 1
		   WHERE sha256=? AND refcount > 0`, sha256hex)
	if err != nil {
		return err
	}
	var cnt int
	err = b.db.QueryRow(`SELECT refcount FROM userspace_blobs WHERE sha256=?`, sha256hex).Scan(&cnt)
	if err != nil {
		return err
	}
	if cnt == 0 {
		return os.Remove(b.pathFor(sha256hex))
	}
	return nil
}

func (b *BlobStore) pathFor(hexsum string) string {
	return filepath.Join(b.root, blobShard(hexsum), hexsum)
}

func blobShard(hexsum string) string {
	if len(hexsum) < 2 {
		return "_"
	}
	return hexsum[:2]
}

// Helper for verifying caller-claimed digest matches actual content.
func ComputeSHA256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// Sanity: never silently truncate giant blobs.
const _ = fmt.Sprintf("") // ensure fmt referenced
```

(Remove the `_ = fmt.Sprintf` line; it's a no-op placeholder in case the file doesn't otherwise use fmt. Use `errors.New` only — drop the `fmt` import if unused.)

- [ ] **Step 5.2: 写 blob_test.go**

```go
package userspace

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBlobStore_PutOpenRoundTrip(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()
	b, err := NewBlobStore(db, root)
	require.NoError(t, err)
	sum, err := b.Put([]byte("hello"))
	require.NoError(t, err)
	require.Equal(t, ComputeSHA256Hex([]byte("hello")), sum)

	rc, sz, err := b.Open(sum)
	require.NoError(t, err)
	defer rc.Close()
	require.Equal(t, int64(5), sz)
	body, _ := io.ReadAll(rc)
	require.Equal(t, "hello", string(body))
}

func TestBlobStore_DedupIncrementsRefcount(t *testing.T) {
	db := newTestDB(t)
	b, _ := NewBlobStore(db, t.TempDir())
	sum, _ := b.Put([]byte("dup"))
	_, _ = b.Put([]byte("dup"))
	var rc int
	require.NoError(t, db.QueryRow(`SELECT refcount FROM userspace_blobs WHERE sha256=?`, sum).Scan(&rc))
	require.Equal(t, 2, rc)
}

func TestBlobStore_ReleaseToZeroRemovesFile(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()
	b, _ := NewBlobStore(db, root)
	sum, _ := b.Put([]byte("temp"))
	require.NoError(t, b.Release(sum))
	_, err := os.Stat(filepath.Join(root, blobShard(sum), sum))
	require.True(t, os.IsNotExist(err))
	// row stays with refcount=0
	var rc int
	require.NoError(t, db.QueryRow(`SELECT refcount FROM userspace_blobs WHERE sha256=?`, sum).Scan(&rc))
	require.Equal(t, 0, rc)
}

func TestBlobStore_OpenZeroRefcountFails(t *testing.T) {
	db := newTestDB(t)
	b, _ := NewBlobStore(db, t.TempDir())
	sum, _ := b.Put([]byte("x"))
	_ = b.Release(sum)
	_, _, err := b.Open(sum)
	require.ErrorIs(t, err, ErrBlobNotFound)
}
```

- [ ] **Step 5.3: 跑测 + commit**

```bash
cd multi-agent
go test ./internal/userspace/... -v
git add internal/userspace/blob.go internal/userspace/blob_test.go
git commit -m "feat(userspace/blob): sha256-addressed fs blob store with refcount

Two-level sharding (sha256[:2]/sha256). Put dedups by content hash and
increments refcount. Release decrements; when refcount reaches 0 the file
is unlinked but the row stays (refcount=0) so future Put with same content
can recreate cleanly without losing audit trail."
```

---

## Task 6: `internal/userspace/skillpack.go` — SKILL.md frontmatter + install scope

**Goal:** kind=skill 包安装时不调 register_slave_mcp，只是把 `skill/` 子树拷到 `~/.claude/skills/<name>/` (user scope) 或 `<project>/.claude/skills/<name>/` (project scope)。本任务实现：(a) 解 SKILL.md 头部 yaml frontmatter；(b) 复制目录到 scope 路径。

**Files:**
- Create: `multi-agent/internal/userspace/skillpack.go`
- Create: `multi-agent/internal/userspace/skillpack_test.go`

### Steps

- [ ] **Step 6.1: 写 skillpack.go**

```go
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

// InstallSkill copies the contents of an unpacked "skill/" subtree into
// <skillsDir>/<name>/, where name is taken from frontmatter. Refuses to
// clobber an existing dir unless overwrite=true.
//
// files is the slice from pack.ReadTarball, filtered to entries whose Path
// starts with "skill/".
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
	// Drop the SKILL.md
	if err := os.WriteFile(filepath.Join(dest, "SKILL.md"), fmBytes, 0o644); err != nil {
		return "", err
	}
	// Drop everything else under skill/<rest> → <dest>/<rest>
	for _, f := range others {
		rel := strings.TrimPrefix(f.Path, "skill/")
		if rel == f.Path { // not under skill/ — ignore
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

// SkillFile is a path/content tuple. Matches the shape of pack.File but
// avoids the cross-package dependency here (api wires them together).
type SkillFile struct {
	Path    string
	Content []byte
}
```

- [ ] **Step 6.2: 写 skillpack_test.go**

```go
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
```

- [ ] **Step 6.3: 跑测 + commit**

```bash
cd multi-agent
go test ./internal/userspace/... -v
git add internal/userspace/skillpack.go internal/userspace/skillpack_test.go
git commit -m "feat(userspace/skillpack): SKILL.md frontmatter + install to user/project scope

ParseSkillFrontmatter expects a leading '---' YAML block with name field
required. InstallSkill drops the skill/ subtree under <skillsDir>/<name>/,
refusing to clobber unless overwrite=true. ScopeUser resolves via os.UserHomeDir;
ScopeProject takes an explicit projectRoot."
```

---

## Task 7: `internal/userspace/api.go` + `routes.go` — HTTP handlers

**Goal:** 把所有 `/api/userspace/*` 路由实现起来，挂到 observer 现有 mux 上，鉴权走 observer 的 agent token middleware（已经把 workspace_id + agent_id 注入 request context）。

**Files:**
- Create: `multi-agent/internal/userspace/api.go` — handlers
- Create: `multi-agent/internal/userspace/routes.go` — `MountRoutes(mux, deps...)`
- Create: `multi-agent/internal/userspace/api_test.go`
- Modify: `multi-agent/internal/observerweb/server.go` — call MountRoutes at construction
- Modify: `multi-agent/cmd/observer-server/main.go` — wire userspace.Migrate + NewBlobStore + pass to web

### Steps

- [ ] **Step 7.1: 先确认 observer 的 agent-context 暴露方式**

```bash
cd multi-agent
grep -n "WorkspaceID\|AgentID\|context\.WithValue\|authedAgent" internal/observerweb/server.go | head -30
```

The existing observerweb has some way to surface the authenticated agent's identity inside handlers. Read the file and identify the helper (likely something like `agentFromCtx(r)` returning `observerstore.Agent`). Use that helper directly in api.go below — do not reinvent token validation.

If no such helper exists yet, add a tiny one in observerweb that wraps the existing token lookup so userspace handlers can call `observerweb.AgentFromRequest(r) (workspaceID, agentID string, ok bool)`. Keep it in observerweb to preserve the rule "userspace doesn't touch observer business data directly".

- [ ] **Step 7.2: 写 api.go — types + push handler**

```go
package userspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/yourorg/multi-agent/internal/mcpmarket/manifest"
	"github.com/yourorg/multi-agent/internal/mcpmarket/pack"
)

// AgentResolver returns the workspace_id and agent_id authenticated by the
// observer agent-token middleware. observerweb provides the concrete impl.
type AgentResolver func(r *http.Request) (workspaceID, agentID string, ok bool)

// Handler holds wired-up dependencies for all /api/userspace/* routes.
type Handler struct {
	Store    *Store
	Blobs    *BlobStore
	Resolver AgentResolver
}

// PushResponse is the body returned by POST /api/userspace/packages.
type PushResponse struct {
	Slug          string `json:"slug"`
	Version       string `json:"version"`
	BlobSHA256    string `json:"blob_sha256"`
	Dedup         bool   `json:"dedup"`
}

func (h *Handler) push(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	wsID, agentID, ok := h.Resolver(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Parse multipart: fields "manifest" (JSON) + "tarball" (binary).
	r.Body = http.MaxBytesReader(w, r.Body, pack.MaxCompressedBytes+1<<16)
	if err := r.ParseMultipartForm(pack.MaxCompressedBytes + 1<<16); err != nil {
		http.Error(w, "bad multipart: "+err.Error(), http.StatusBadRequest)
		return
	}
	manifestRaw := r.FormValue("manifest")
	if manifestRaw == "" {
		http.Error(w, "missing 'manifest' form field", http.StatusBadRequest)
		return
	}
	if len(manifestRaw) > 64*1024 {
		http.Error(w, "manifest too large", http.StatusRequestEntityTooLarge)
		return
	}
	mfp, err := manifest.Parse([]byte(manifestRaw))
	if err != nil {
		http.Error(w, "manifest parse: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := mfp.Validate(); err != nil {
		http.Error(w, "manifest invalid: "+err.Error(), http.StatusBadRequest)
		return
	}

	tarFile, _, err := r.FormFile("tarball")
	if err != nil {
		http.Error(w, "missing 'tarball' file field", http.StatusBadRequest)
		return
	}
	defer tarFile.Close()
	tarBytes, err := io.ReadAll(io.LimitReader(tarFile, pack.MaxCompressedBytes+1))
	if err != nil || len(tarBytes) > pack.MaxCompressedBytes {
		http.Error(w, "tarball read/oversize", http.StatusRequestEntityTooLarge)
		return
	}
	// Server is the source of truth for the tarball hash (manifest no longer
	// carries it in v1 — see Task 1 NOTE).
	actual := ComputeSHA256Hex(tarBytes)
	// Unpack to validate structure + extract spec.json / card_md.
	prefix, files, err := pack.ReadTarball(tarBytes)
	if err != nil {
		http.Error(w, "unpack: "+err.Error(), http.StatusBadRequest)
		return
	}
	expectedPrefix := fmt.Sprintf("mcp-package-%s-%s", mfp.Slug, mfp.Version)
	if prefix != expectedPrefix {
		http.Error(w, fmt.Sprintf("prefix mismatch: got %q want %q", prefix, expectedPrefix), http.StatusBadRequest)
		return
	}
	specJSON, cardMD, err := extractRefs(files, mfp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// kind consistency: same slug must not flip kind.
	if existing, _ := h.Store.GetPackage(mfp.Slug); existing != nil && existing.Kind != string(mfp.Kind) {
		http.Error(w, fmt.Sprintf("kind mismatch: slug %s already registered as %s", mfp.Slug, existing.Kind), http.StatusBadRequest)
		return
	}

	// Refcount-aware blob put. dedup=true means existing rowcount went up.
	existingRefcount := 0
	_ = h.Store.db.QueryRow(`SELECT refcount FROM userspace_blobs WHERE sha256=?`, actual).Scan(&existingRefcount)
	dedup := existingRefcount > 0
	if _, err := h.Blobs.Put(tarBytes); err != nil {
		http.Error(w, "blob put: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Upsert package + insert version + auto-install for this workspace.
	if err := h.Store.UpsertPackage(PackageRow{
		Slug: mfp.Slug, Kind: string(mfp.Kind),
		Description: "", // description is held in card_md / future field; v1 leaves empty
		Tags:        mfp.Tags,
	}); err != nil {
		http.Error(w, "upsert pkg: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.Store.InsertVersion(VersionRow{
		Slug: mfp.Slug, Version: mfp.Version,
		CreatedInWorkspace: wsID, CreatedByAgentID: agentID,
		ManifestJSON:  []byte(manifestRaw),
		SpecJSON:      specJSON,
		CardMD:        cardMD,
		TarballSHA256: actual, BlobSHA256: actual,
	}); err != nil {
		if errors.Is(err, ErrVersionExists) {
			http.Error(w, "version already exists", http.StatusConflict)
			return
		}
		http.Error(w, "insert version: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.Store.UpsertInstallation(InstallationRow{
		WorkspaceID: wsID, Slug: mfp.Slug,
		InstalledVersion: mfp.Version, InstalledByAgent: agentID,
	}); err != nil {
		http.Error(w, "upsert installation: "+err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("userspace: push slug=%s version=%s ws=%s agent=%s dedup=%v",
		mfp.Slug, mfp.Version, wsID, agentID, dedup)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(PushResponse{
		Slug: mfp.Slug, Version: mfp.Version, BlobSHA256: actual, Dedup: dedup,
	})
}

// extractRefs pulls spec.json + card_md out of the unpacked file list.
func extractRefs(files []pack.File, mfp *manifest.Manifest) (specJSON []byte, cardMD string, err error) {
	byPath := map[string]pack.File{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	card, ok := byPath[mfp.CardRef]
	if !ok {
		return nil, "", fmt.Errorf("card_ref %q not in tarball", mfp.CardRef)
	}
	if len(card.Content) > 16*1024 {
		return nil, "", errors.New("card_md > 16 KiB")
	}
	cardMD = string(card.Content)
	if mfp.Kind == manifest.KindMCP {
		spec, ok := byPath[mfp.SpecRef]
		if !ok {
			return nil, "", fmt.Errorf("spec_ref %q not in tarball", mfp.SpecRef)
		}
		if len(spec.Content) > 32*1024 {
			return nil, "", errors.New("spec.json > 32 KiB")
		}
		specJSON = spec.Content
	}
	return specJSON, cardMD, nil
}

// helper unused yet but useful for handler tests
func split2(s, sep string) (string, string, bool) {
	i := strings.Index(s, sep)
	if i < 0 {
		return "", "", false
	}
	return s[:i], s[i+len(sep):], true
}
```

- [ ] **Step 7.3: 写 read handlers in api.go (search, get package, get version, source.tar.gz)**

Append to `api.go`:

```go
func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	wsID, _, ok := h.Resolver(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	q := r.URL.Query().Get("q")
	kind := r.URL.Query().Get("kind")
	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}
	results, err := h.Store.SearchPackages(q, wsID, kind, limit)
	if err != nil {
		log.Printf("userspace: search error: %v", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if results == nil {
		results = []PackageView{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"results":     results,
		"search_mode": "fts5",
	})
}

// /api/userspace/packages/{slug}
func (h *Handler) getPackage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	slug := strings.TrimPrefix(r.URL.Path, "/api/userspace/packages/")
	if slug == "" || strings.Contains(slug, "/") {
		// Not a bare slug — fall through to versions handler if present.
		h.getPackageOrVersion(w, r)
		return
	}
	pkg, err := h.Store.GetPackage(slug)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if pkg == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	versions, err := h.Store.ListVersions(slug)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"package":  pkg,
		"versions": versions,
	})
}

// /api/userspace/packages/{slug}/versions/{ver}[/source.tar.gz]
func (h *Handler) getPackageOrVersion(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/userspace/packages/")
	parts := strings.SplitN(rest, "/", 4)
	if len(parts) < 3 || parts[1] != "versions" {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	slug, ver := parts[0], parts[2]
	v, err := h.Store.GetVersion(slug, ver)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if v == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if len(parts) == 4 && parts[3] == "source.tar.gz" {
		rc, sz, err := h.Blobs.Open(v.BlobSHA256)
		if err != nil {
			http.Error(w, "blob missing", http.StatusGone)
			return
		}
		defer rc.Close()
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", sz))
		w.Header().Set("ETag", `"`+v.TarballSHA256+`"`)
		_, _ = io.Copy(w, rc)
		return
	}
	// Metadata only.
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"slug":                 v.Slug,
		"version":              v.Version,
		"created_in_workspace": v.CreatedInWorkspace,
		"created_by_agent_id":  v.CreatedByAgentID,
		"manifest":             json.RawMessage(v.ManifestJSON),
		"card_md":              v.CardMD,
		"tarball_sha256":       v.TarballSHA256,
		"status":               v.Status,
	})
}
```

- [ ] **Step 7.4: 写 install/uninstall/yank handlers + listPackages**

```go
// POST /api/userspace/workspaces/{ws}/installations/{slug}  body={"version":"x.y.z"}
func (h *Handler) installVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	wsID, agentID, ok := h.Resolver(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/userspace/workspaces/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) != 3 || parts[1] != "installations" {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	pathWS, slug := parts[0], parts[2]
	if pathWS != wsID {
		http.Error(w, "cross-workspace write not allowed", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodDelete {
		if err := h.Store.DeleteInstallation(wsID, slug); err != nil {
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Version == "" {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	v, err := h.Store.GetVersion(slug, body.Version)
	if err != nil || v == nil {
		http.Error(w, "version not found", http.StatusNotFound)
		return
	}
	if err := h.Store.UpsertInstallation(InstallationRow{
		WorkspaceID: wsID, Slug: slug,
		InstalledVersion: body.Version, InstalledByAgent: agentID,
	}); err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/userspace/packages/{slug}/yank/{ver}
func (h *Handler) yankVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, _, ok := h.Resolver(r); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/userspace/packages/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) != 3 || parts[1] != "yank" {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	slug, ver := parts[0], parts[2]
	if err := h.Store.YankVersion(slug, ver); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "not found or already yanked", http.StatusNotFound)
			return
		}
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// /api/userspace/packages  (list; supports ?workspace=mine|all&kind=mcp|skill|all)
func (h *Handler) listPackages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	wsID, _, ok := h.Resolver(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	scope := r.URL.Query().Get("workspace")
	if scope == "" {
		scope = "mine"
	}
	kind := r.URL.Query().Get("kind")
	results, err := h.Store.SearchPackages("", wsID, kind, 100)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if scope == "mine" {
		filtered := results[:0]
		for _, p := range results {
			if p.InstalledVersion != "" {
				filtered = append(filtered, p)
			}
		}
		results = filtered
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"packages": results})
}
```

Don't forget to import `database/sql` for `sql.ErrNoRows`.

- [ ] **Step 7.5: 写 routes.go — MountRoutes**

```go
package userspace

import "net/http"

// MountRoutes wires every /api/userspace/* path onto the given mux.
// Call once at observer-server startup.
func MountRoutes(mux *http.ServeMux, h *Handler) {
	mux.HandleFunc("POST /api/userspace/packages", h.push)
	mux.HandleFunc("GET /api/userspace/search", h.search)
	mux.HandleFunc("GET /api/userspace/packages", h.listPackages)
	mux.HandleFunc("GET /api/userspace/packages/", h.getPackage) // matches /api/userspace/packages/{slug}[/...]
	mux.HandleFunc("POST /api/userspace/packages/", h.routePackagePost) // yank + others
	mux.HandleFunc("POST /api/userspace/workspaces/", h.installVersion)
	mux.HandleFunc("DELETE /api/userspace/workspaces/", h.installVersion)
}

// routePackagePost dispatches POST /api/userspace/packages/{slug}/yank/{ver}
// (no other POST endpoints under /packages/ in v1).
func (h *Handler) routePackagePost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !strings.Contains(r.URL.Path, "/yank/") {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	h.yankVersion(w, r)
}
```

If the project's Go version is < 1.22, `mux.HandleFunc("POST /...")` method-prefix syntax won't compile — fall back to inspecting `r.Method` inside each handler (the handlers above already do). Check `go version` first; if < 1.22, drop the method prefixes and rely on per-handler method checks.

- [ ] **Step 7.6: 写 api_test.go**

```go
package userspace

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/mcpmarket/manifest"
	"github.com/yourorg/multi-agent/internal/mcpmarket/pack"
)

func newTestHandler(t *testing.T) (*Handler, *httptest.Server) {
	t.Helper()
	db := newTestDB(t)
	store := NewStore(db)
	blobs, err := NewBlobStore(db, t.TempDir())
	require.NoError(t, err)
	// Fixed-identity resolver
	resolver := func(r *http.Request) (string, string, bool) {
		ws := r.Header.Get("X-Test-WS")
		ag := r.Header.Get("X-Test-Agent")
		return ws, ag, ws != "" && ag != ""
	}
	h := &Handler{Store: store, Blobs: blobs, Resolver: resolver}
	mux := http.NewServeMux()
	// Register handlers WITHOUT method-prefix syntax for portability.
	mux.HandleFunc("/api/userspace/packages", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			h.push(w, r)
		} else {
			h.listPackages(w, r)
		}
	})
	mux.HandleFunc("/api/userspace/search", h.search)
	mux.HandleFunc("/api/userspace/packages/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			h.routePackagePost(w, r)
		} else {
			h.getPackage(w, r)
		}
	})
	mux.HandleFunc("/api/userspace/workspaces/", h.installVersion)
	return h, httptest.NewServer(mux)
}

func buildPushBody(t *testing.T, kind manifest.Kind, slug, version string) (*bytes.Buffer, string) {
	t.Helper()
	// minimal valid tarball
	manifestContent := []byte(`# card`)
	files := []pack.File{
		{Path: "capability_card.md", Content: manifestContent},
	}
	if kind == manifest.KindMCP {
		files = append(files,
			pack.File{Path: "spec.json", Content: []byte(`{"name":"x","version":1}`)},
			pack.File{Path: "tests/cases.json", Content: []byte(`{}`)},
		)
	} else {
		files = append(files, pack.File{Path: "skill/SKILL.md", Content: []byte("---\nname: " + slug + "\n---\nbody\n")})
	}
	prefix := "mcp-package-" + slug + "-" + version
	tarBytes, sha, err := pack.WriteTarball(prefix, files)
	require.NoError(t, err)

	m := &manifest.Manifest{
		SchemaVersion: 1, Kind: kind, Slug: slug, Version: version,
		TarballSHA256: sha, CardRef: "capability_card.md",
		SpecRef: "spec.json", CasesRef: "tests/cases.json",
		Software: manifest.Software{Packages: []string{}},
		Hardware: manifest.Hardware{NetworkEgress: []string{}},
		Tags:     []string{}, License: "MIT", CreatedAt: "2026-05-26T00:00:00Z",
	}
	if kind == manifest.KindSkill {
		m.SpecRef = ""
		m.CasesRef = ""
	}
	mfJSON, err := json.Marshal(m)
	require.NoError(t, err)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	require.NoError(t, mw.WriteField("manifest", string(mfJSON)))
	fw, err := mw.CreateFormFile("tarball", "pkg.tar.gz")
	require.NoError(t, err)
	_, err = fw.Write(tarBytes)
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	return &body, mw.FormDataContentType()
}

func TestAPI_PushHappyPath(t *testing.T) {
	_, srv := newTestHandler(t)
	defer srv.Close()
	_, err := srv.Client().PostForm(srv.URL, nil)
	_ = err
	body, ct := buildPushBody(t, manifest.KindMCP, "foo", "1.0.0")
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/api/userspace/packages", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-Test-WS", "ws-a")
	req.Header.Set("X-Test-Agent", "agent-1")
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode, "body=%s", readBody(resp))
	var pr PushResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&pr))
	require.Equal(t, "foo", pr.Slug)
	require.Equal(t, "1.0.0", pr.Version)
	require.False(t, pr.Dedup)
}

func TestAPI_PushRejectsKindMismatch(t *testing.T) {
	_, srv := newTestHandler(t)
	defer srv.Close()
	doPush(t, srv, manifest.KindMCP, "foo", "1.0.0", "ws-a", "ag", 200)
	body, ct := buildPushBody(t, manifest.KindSkill, "foo", "2.0.0")
	resp := postMultipart(t, srv, "/api/userspace/packages", body, ct, "ws-a", "ag")
	require.Equal(t, 400, resp.StatusCode)
	require.Contains(t, readBody(resp), "kind mismatch")
}

func TestAPI_PushVersionConflict(t *testing.T) {
	_, srv := newTestHandler(t)
	defer srv.Close()
	doPush(t, srv, manifest.KindMCP, "foo", "1.0.0", "ws-a", "ag", 200)
	doPush(t, srv, manifest.KindMCP, "foo", "1.0.0", "ws-a", "ag", 409)
}

func TestAPI_InstallCrossWorkspaceForbidden(t *testing.T) {
	_, srv := newTestHandler(t)
	defer srv.Close()
	doPush(t, srv, manifest.KindMCP, "foo", "1.0.0", "ws-a", "ag", 200)
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/api/userspace/workspaces/ws-OTHER/installations/foo",
		bytes.NewReader([]byte(`{"version":"1.0.0"}`)))
	req.Header.Set("X-Test-WS", "ws-a") // caller is ws-a, path says ws-OTHER
	req.Header.Set("X-Test-Agent", "ag")
	resp, _ := srv.Client().Do(req)
	require.Equal(t, 403, resp.StatusCode)
}

func TestAPI_Search_HappyPath(t *testing.T) {
	_, srv := newTestHandler(t)
	defer srv.Close()
	doPush(t, srv, manifest.KindMCP, "invoice_extract", "1.0.0", "ws-a", "ag", 200)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/userspace/search?q=invoice", nil)
	req.Header.Set("X-Test-WS", "ws-a")
	req.Header.Set("X-Test-Agent", "ag")
	resp, _ := srv.Client().Do(req)
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Results []PackageView `json:"results"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Len(t, out.Results, 1)
	require.Equal(t, "invoice_extract", out.Results[0].Slug)
}

// helpers
func doPush(t *testing.T, srv *httptest.Server, k manifest.Kind, slug, ver, ws, ag string, wantStatus int) {
	t.Helper()
	body, ct := buildPushBody(t, k, slug, ver)
	resp := postMultipart(t, srv, "/api/userspace/packages", body, ct, ws, ag)
	require.Equal(t, wantStatus, resp.StatusCode, "body=%s", readBody(resp))
}
func postMultipart(t *testing.T, srv *httptest.Server, path string, body io.Reader, ct, ws, ag string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+path, body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-Test-WS", ws)
	req.Header.Set("X-Test-Agent", ag)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	return resp
}
func readBody(r *http.Response) string {
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	return string(b)
}
```

- [ ] **Step 7.7: 修改 observerweb/server.go 暴露 AgentResolver + 调 MountRoutes**

Open `internal/observerweb/server.go`. Find where the handler/router is constructed (in `New(...)`). Two adjustments:

1. **Expose a public helper** `AgentFromRequest(r *http.Request) (workspaceID, agentID string, ok bool)` that runs the existing token validation. If the file already uses `bearerToken(...)` + `ValidateToken(...)` inline in each handler, extract that into a helper:

```go
// AgentFromRequest validates the Bearer token (if any) and returns the
// authenticated agent's workspace_id + agent_id. ok=false means anonymous
// or invalid. Designed for sibling packages (internal/userspace) that mount
// routes on the same mux.
func AgentFromRequest(s observerstore.StoreReader, r *http.Request) (string, string, bool) {
	tok, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		return "", "", false
	}
	agent, ok, err := s.ValidateToken(tok)
	if err != nil || !ok {
		return "", "", false
	}
	return agent.WorkspaceID, agent.ID, true
}
```

`StoreReader` is a small interface that just promises `ValidateToken`; create it in observerweb if not already present. The existing handlers in server.go can be refactored to call this helper too (DRY), but that's optional.

2. **Mount userspace routes in New(...)**:

```go
// at the end of constructing the mux, before returning the handler:
import "github.com/yourorg/multi-agent/internal/userspace"
import "net/http"

if usHandler != nil {
	userspace.MountRoutes(mux, usHandler)
}
```

`usHandler` is a new optional parameter passed into observerweb. Add `WithUserspaceHandler` option or new constructor signature; choose the path that matches existing options style in this file.

If observerweb today has `func New(store StoreReader) *handler`, change to `func New(store StoreReader, opts ...Option) *handler` and add an `Option func(*handler)` mechanism. If you prefer not to refactor today, just pass `usHandler` as an extra param `New(store, usHandler)` and update the call site in cmd/observer-server.

- [ ] **Step 7.8: 修改 cmd/observer-server/main.go**

```go
// After the existing `st, err := observerstore.New(...)`:
import "path/filepath"
import "github.com/yourorg/multi-agent/internal/userspace"

if err := userspace.Migrate(st.DB()); err != nil {
	log.Fatalf("userspace migrate: %v", err)
}
blobRoot := filepath.Join(filepath.Dir(cfg.DBPath), "userspace-blobs")
blobs, err := userspace.NewBlobStore(st.DB(), blobRoot)
if err != nil {
	log.Fatalf("userspace blob store: %v", err)
}
usHandler := &userspace.Handler{
	Store: userspace.NewStore(st.DB()),
	Blobs: blobs,
	Resolver: func(r *http.Request) (string, string, bool) {
		return observerweb.AgentFromRequest(st, r)
	},
}
// Pass usHandler to observerweb.New(...)
```

The `AgentFromRequest` reference here closes over `st`; the function signature you create in Step 7.7 must accept the store (or however you wired it).

- [ ] **Step 7.9: 跑测**

```bash
cd multi-agent
go test ./... 2>&1 | tail -10
```

Expected: all PASS.

- [ ] **Step 7.10: Commit**

```bash
cd multi-agent
git add internal/userspace/ internal/observerweb/server.go cmd/observer-server/main.go
git commit -m "feat(userspace): HTTP routes mounted on observer-server

- /api/userspace/packages (push, list)
- /api/userspace/search
- /api/userspace/packages/{slug}[/versions/{ver}[/source.tar.gz]]
- /api/userspace/packages/{slug}/yank/{ver}
- /api/userspace/workspaces/{ws}/installations/{slug} (post/delete)
- Auth via observerweb.AgentFromRequest helper (one place validates tokens).
- Cross-workspace writes return 403; same-slug kind flip returns 400.
- Push auto-installs the new version in the calling workspace."
```

---

## Task 8: `cmd/mcp-userspace` CLI

**Goal:** Pure HTTP client. Subcommands: `login push search pull install list yank`. Config at `~/.mcp-userspace/config.yaml` (observer URL + token). Token can also come from `--token` flag or the agent's existing `~/.loom/<agent>/observer.token` file.

**Files:**
- Create: `multi-agent/cmd/mcp-userspace/main.go`
- Create: `multi-agent/cmd/mcp-userspace/client.go`
- Create: `multi-agent/cmd/mcp-userspace/config.go`
- Create: `multi-agent/cmd/mcp-userspace/cmd_login.go`, `cmd_push.go`, `cmd_search.go`, `cmd_pull.go`, `cmd_install.go`, `cmd_list.go`, `cmd_yank.go`

### Steps

- [ ] **Step 8.1: 写 main.go (subcommand dispatch)**

```go
package main

import (
	"fmt"
	"os"
)

const usage = `mcp-userspace — push/pull/install personal MCP & skill packages

Usage:
  mcp-userspace login --url URL --token TOK     Save config to ~/.mcp-userspace/config.yaml
  mcp-userspace push <dir> [--slug X] [--bump-patch|--bump-minor|--bump-major]
  mcp-userspace search "query" [--kind mcp|skill|all] [--limit N]
  mcp-userspace list [--workspace mine|all]
  mcp-userspace pull <slug>[@<ver>] [--out <dir>]
  mcp-userspace install <slug>[@<ver>] [--as mcp|skill] [--scope user|project]
  mcp-userspace yank <slug> <ver>

Configuration:
  Reads ~/.mcp-userspace/config.yaml; overridable per-invocation with
  --url and --token.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "login":
		runLogin(os.Args[2:])
	case "push":
		runPush(os.Args[2:])
	case "search":
		runSearch(os.Args[2:])
	case "list":
		runList(os.Args[2:])
	case "pull":
		runPull(os.Args[2:])
	case "install":
		runInstall(os.Args[2:])
	case "yank":
		runYank(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Println(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}
```

- [ ] **Step 8.2: 写 config.go**

```go
package main

import (
	"errors"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	URL   string `yaml:"url"`
	Token string `yaml:"token"`
}

func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".mcp-userspace", "config.yaml"), nil
}

func loadConfig() (Config, error) {
	p, err := configPath()
	if err != nil {
		return Config{}, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, errors.New("no config — run `mcp-userspace login` first")
		}
		return Config{}, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return Config{}, err
	}
	if c.URL == "" || c.Token == "" {
		return Config{}, errors.New("config missing url or token")
	}
	return c, nil
}

func saveConfig(c Config) error {
	p, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}
```

- [ ] **Step 8.3: 写 client.go (thin HTTP wrapper)**

```go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	cfg  Config
	http *http.Client
}

func newClient(cfg Config) *Client {
	return &Client{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) do(method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequest(method, strings.TrimRight(c.cfg.URL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return c.http.Do(req)
}

func (c *Client) Push(tarball, manifestJSON []byte) (map[string]any, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("manifest", string(manifestJSON)); err != nil {
		return nil, err
	}
	fw, err := mw.CreateFormFile("tarball", "pkg.tar.gz")
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(tarball); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	resp, err := c.do(http.MethodPost, "/api/userspace/packages", &body, mw.FormDataContentType())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeOrErr(resp)
}

func (c *Client) Search(q, kind string, limit int) (map[string]any, error) {
	u := fmt.Sprintf("/api/userspace/search?q=%s&kind=%s&limit=%d",
		url.QueryEscape(q), url.QueryEscape(kind), limit)
	resp, err := c.do(http.MethodGet, u, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeOrErr(resp)
}

func (c *Client) List(scope, kind string) (map[string]any, error) {
	u := fmt.Sprintf("/api/userspace/packages?workspace=%s&kind=%s",
		url.QueryEscape(scope), url.QueryEscape(kind))
	resp, err := c.do(http.MethodGet, u, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeOrErr(resp)
}

func (c *Client) PullTarball(slug, ver string) ([]byte, error) {
	resp, err := c.do(http.MethodGet,
		fmt.Sprintf("/api/userspace/packages/%s/versions/%s/source.tar.gz", slug, ver),
		nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("pull HTTP %d: %s", resp.StatusCode, body)
	}
	return io.ReadAll(resp.Body)
}

func (c *Client) Yank(slug, ver string) error {
	resp, err := c.do(http.MethodPost,
		fmt.Sprintf("/api/userspace/packages/%s/yank/%s", slug, ver), nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 204 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("yank HTTP %d: %s", resp.StatusCode, body)
	}
	return nil
}

func decodeOrErr(resp *http.Response) (map[string]any, error) {
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode: %w; body=%s", err, body)
	}
	return out, nil
}
```

- [ ] **Step 8.4: 写 cmd_login.go / cmd_push.go / cmd_search.go**

```go
// cmd_login.go
package main

import (
	"flag"
	"fmt"
	"os"
)

func runLogin(args []string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	url := fs.String("url", "", "observer URL, e.g. http://localhost:18091")
	tok := fs.String("token", "", "observer agent token (Bearer)")
	fs.Parse(args)
	if *url == "" || *tok == "" {
		fmt.Fprintln(os.Stderr, "--url and --token required")
		os.Exit(2)
	}
	if err := saveConfig(Config{URL: *url, Token: *tok}); err != nil {
		fmt.Fprintln(os.Stderr, "save:", err)
		os.Exit(1)
	}
	fmt.Println("saved to ~/.mcp-userspace/config.yaml")
}
```

```go
// cmd_push.go
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yourorg/multi-agent/internal/mcpmarket/manifest"
	"github.com/yourorg/multi-agent/internal/mcpmarket/pack"
)

func runPush(args []string) {
	fs := flag.NewFlagSet("push", flag.ExitOnError)
	slugFlag := fs.String("slug", "", "override slug (default = directory basename)")
	bumpMinor := fs.Bool("bump-minor", false, "auto-bump minor before push")
	bumpPatch := fs.Bool("bump-patch", false, "auto-bump patch before push")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mcp-userspace push <dir>")
		os.Exit(2)
	}
	dir := fs.Arg(0)

	cfg, err := loadConfig()
	failIf(err)

	// Detect kind from filesystem.
	kind := manifest.KindMCP
	if _, err := os.Stat(filepath.Join(dir, "skill", "SKILL.md")); err == nil {
		kind = manifest.KindSkill
	}

	// Read manifest.json if present; else synthesize a default.
	mfPath := filepath.Join(dir, "manifest.json")
	var m *manifest.Manifest
	if b, err := os.ReadFile(mfPath); err == nil {
		m, err = manifest.Parse(b)
		failIf(err)
	} else {
		slug := *slugFlag
		if slug == "" {
			slug = strings.ToLower(filepath.Base(dir))
		}
		m = &manifest.Manifest{
			SchemaVersion: manifest.SchemaVersion, Kind: kind,
			Slug: slug, Version: "0.1.0",
			CardRef:  "capability_card.md",
			Software: manifest.Software{Packages: []string{}},
			Hardware: manifest.Hardware{NetworkEgress: []string{}},
			Tags:     []string{}, License: "MIT",
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		if kind == manifest.KindMCP {
			m.SpecRef = "spec.json"
			m.CasesRef = "tests/cases.json"
		}
	}
	if *bumpMinor || *bumpPatch {
		m.Version = bumpVersion(m.Version, *bumpMinor)
	}
	if *slugFlag != "" {
		m.Slug = *slugFlag
	}

	// Pack directory; strip any pre-existing manifest.json (we write the
	// authoritative one). No hashing needed — server computes sha of bytes.
	files, err := pack.FilesFromDir(dir)
	failIf(err)
	filtered := files[:0]
	for _, f := range files {
		if f.Path == "manifest.json" {
			continue
		}
		filtered = append(filtered, f)
	}
	mfBytes, _ := json.Marshal(m)
	withMF := append([]pack.File{{Path: "manifest.json", Content: mfBytes}}, filtered...)
	finalTar, _, err := pack.WriteTarball("mcp-package-"+m.Slug+"-"+m.Version, withMF)
	failIf(err)

	client := newClient(cfg)
	resp, err := client.Push(finalTar, mfBytes)
	failIf(err)
	fmt.Printf("pushed %s@%s (dedup=%v, blob=%s)\n",
		resp["slug"], resp["version"], resp["dedup"], resp["blob_sha256"])
}

func bumpVersion(v string, minor bool) string {
	var maj, min, pat int
	_, err := fmt.Sscanf(v, "%d.%d.%d", &maj, &min, &pat)
	if err != nil {
		return "0.1.0"
	}
	if minor {
		min++
		pat = 0
	} else {
		pat++
	}
	return fmt.Sprintf("%d.%d.%d", maj, min, pat)
}

func failIf(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
```

Per Task 1 NOTE: `tarball_sha256` is NOT in the manifest in v1. The chicken-and-egg with deterministic packing is sidestepped by trusting the server-computed sha. Client doesn't compute it; server returns `blob_sha256` in the push response.

- [ ] **Step 8.5: 写 cmd_search.go / cmd_list.go**

```go
// cmd_search.go
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func runSearch(args []string) {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	kind := fs.String("kind", "all", "mcp|skill|all")
	limit := fs.Int("limit", 20, "max results")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mcp-userspace search \"query\"")
		os.Exit(2)
	}
	cfg, err := loadConfig()
	failIf(err)
	resp, err := newClient(cfg).Search(fs.Arg(0), *kind, *limit)
	failIf(err)
	b, _ := json.MarshalIndent(resp, "", "  ")
	fmt.Println(string(b))
}
```

```go
// cmd_list.go
package main

import (
	"encoding/json"
	"flag"
	"fmt"
)

func runList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	scope := fs.String("workspace", "mine", "mine|all")
	kind := fs.String("kind", "all", "mcp|skill|all")
	fs.Parse(args)
	cfg, err := loadConfig()
	failIf(err)
	resp, err := newClient(cfg).List(*scope, *kind)
	failIf(err)
	b, _ := json.MarshalIndent(resp, "", "  ")
	fmt.Println(string(b))
}
```

- [ ] **Step 8.6: 写 cmd_pull.go / cmd_install.go / cmd_yank.go**

```go
// cmd_pull.go
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yourorg/multi-agent/internal/mcpmarket/pack"
)

func runPull(args []string) {
	fs := flag.NewFlagSet("pull", flag.ExitOnError)
	out := fs.String("out", "", "extract to this directory (default: ./<slug>)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mcp-userspace pull <slug>[@<ver>]")
		os.Exit(2)
	}
	slug, ver := parseSlugVer(fs.Arg(0))
	cfg, err := loadConfig()
	failIf(err)
	client := newClient(cfg)
	if ver == "" {
		// Look up latest via list endpoint
		fmt.Fprintln(os.Stderr, "version required for v1; try `mcp-userspace search <slug>` to see available")
		os.Exit(2)
	}
	tarball, err := client.PullTarball(slug, ver)
	failIf(err)
	dest := *out
	if dest == "" {
		dest = slug
	}
	prefix, files, err := pack.ReadTarball(tarball)
	failIf(err)
	_ = prefix
	failIf(os.MkdirAll(dest, 0o755))
	for _, f := range files {
		full := filepath.Join(dest, f.Path)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		failIf(os.WriteFile(full, f.Content, 0o644))
	}
	fmt.Printf("extracted %s@%s to %s\n", slug, ver, dest)
}

func parseSlugVer(s string) (slug, ver string) {
	i := strings.Index(s, "@")
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i+1:]
}
```

```go
// cmd_install.go
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/yourorg/multi-agent/internal/mcpmarket/pack"
	"github.com/yourorg/multi-agent/internal/userspace"
)

func runInstall(args []string) {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	as := fs.String("as", "", "mcp|skill (auto-detect if empty)")
	scope := fs.String("scope", "user", "user|project (skill only)")
	projectRoot := fs.String("project-root", ".", "for --scope=project")
	overwrite := fs.Bool("overwrite", false, "overwrite existing install")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mcp-userspace install <slug>@<ver>")
		os.Exit(2)
	}
	slug, ver := parseSlugVer(fs.Arg(0))
	if ver == "" {
		fmt.Fprintln(os.Stderr, "version required: <slug>@<ver>")
		os.Exit(2)
	}
	cfg, err := loadConfig()
	failIf(err)
	tarball, err := newClient(cfg).PullTarball(slug, ver)
	failIf(err)
	_, files, err := pack.ReadTarball(tarball)
	failIf(err)

	// Detect kind.
	kind := *as
	if kind == "" {
		for _, f := range files {
			if f.Path == "skill/SKILL.md" {
				kind = "skill"
				break
			}
		}
		if kind == "" {
			kind = "mcp"
		}
	}

	switch kind {
	case "skill":
		skillFiles := make([]userspace.SkillFile, 0, len(files))
		for _, f := range files {
			skillFiles = append(skillFiles, userspace.SkillFile{Path: f.Path, Content: f.Content})
		}
		dir, err := userspace.ResolveSkillDir(userspace.InstallScope(*scope), *projectRoot)
		failIf(err)
		dest, err := userspace.InstallSkill(skillFiles, dir, *overwrite)
		failIf(err)
		fmt.Printf("skill installed to %s\n", dest)
	case "mcp":
		// v1: copy tarball contents into ./generated_mcp/<slug>/ so the user can
		// run scaffold-mcp-server + mcp-acceptance + register_slave_mcp manually.
		dest := "generated_mcp/" + slug
		failIf(os.MkdirAll(dest, 0o755))
		for _, f := range files {
			full := dest + "/" + f.Path
			_ = os.MkdirAll(parentDir(full), 0o755)
			failIf(os.WriteFile(full, f.Content, 0o644))
		}
		fmt.Printf("mcp package extracted to %s — next: scaffold-mcp-server / mcp-acceptance / register_slave_mcp\n", dest)
	default:
		fmt.Fprintln(os.Stderr, "unknown --as:", kind)
		os.Exit(2)
	}
}

func parentDir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}
```

```go
// cmd_yank.go
package main

import (
	"flag"
	"fmt"
	"os"
)

func runYank(args []string) {
	fs := flag.NewFlagSet("yank", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: mcp-userspace yank <slug> <ver>")
		os.Exit(2)
	}
	cfg, err := loadConfig()
	failIf(err)
	if err := newClient(cfg).Yank(fs.Arg(0), fs.Arg(1)); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Println("yanked")
}
```

- [ ] **Step 8.7: Build + smoke**

```bash
cd multi-agent
go build ./cmd/mcp-userspace
./mcp-userspace --help
```

Expected: help output prints; binary builds.

- [ ] **Step 8.8: Commit**

```bash
cd multi-agent
git add cmd/mcp-userspace/
git commit -m "feat(cmd/mcp-userspace): CLI — login/push/search/list/pull/install/yank

Pure HTTP client; config at ~/.mcp-userspace/config.yaml (url + Bearer token).
Push reads dir → packs deterministic tar.gz → multipart POST. Install splits
by kind: skill copies to ~/.claude/skills/<name>/ (or project scope);
mcp extracts to ./generated_mcp/<slug>/ where the user then runs
scaffold-mcp-server / mcp-acceptance / register_slave_mcp manually."
```

---

## Task 9: Local-gray e2e (per memory [[e2e_required_for_features_and_fixes]])

**Goal:** Build the new observer-server with userspace, start it, use the CLI to push + install a real package across two workspaces, verify storage + scope.

**Files:** None to create — this is a verification task. Output recorded in commit message of the smoke script.

### Steps

- [ ] **Step 9.1: Build binaries to /tmp/e2e/**

```bash
cd /root/multi-agent/multi-agent
mkdir -p /tmp/e2e-userspace
CGO_ENABLED=0 go build -o /tmp/e2e-userspace/observer-server ./cmd/observer-server
CGO_ENABLED=0 go build -o /tmp/e2e-userspace/mcp-userspace ./cmd/mcp-userspace
ls -la /tmp/e2e-userspace/
```

- [ ] **Step 9.2: Write observer yaml + start it on :18092**

```bash
cat >/tmp/e2e-userspace/observer.yaml <<'YAML'
listen_addr: ":18092"
db_path: /tmp/e2e-userspace/observer.db
api_keys:
  - id: ak-e2e
    key: e2e-userspace-key
    note: "userspace local-gray"
YAML
rm -f /tmp/e2e-userspace/observer.db
/tmp/e2e-userspace/observer-server -config /tmp/e2e-userspace/observer.yaml > /tmp/e2e-userspace/server.log 2>&1 &
echo $! > /tmp/e2e-userspace/server.pid
sleep 1
ss -tlnp 2>/dev/null | grep 18092 && tail /tmp/e2e-userspace/server.log
```

Expected: process listening on :18092; log shows "loaded 1 api_keys" and userspace migration succeeded (verify by `sqlite3 /tmp/e2e-userspace/observer.db ".tables"` shows `userspace_*` tables alongside observer's).

- [ ] **Step 9.3: Register two agents in two workspaces (curl)**

```bash
# ws-personal agent
TOK_PERSONAL=$(curl -sS -X POST http://localhost:18092/api/agents/register \
  -H "Authorization: Bearer e2e-userspace-key" -H "Content-Type: application/json" \
  -d '{"agent_id":"driver-personal","role":"driver","workspace_id":"ws-personal","workspace_name":"Personal"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')
echo "personal token: ${TOK_PERSONAL:0:8}..."

# ws-work agent
TOK_WORK=$(curl -sS -X POST http://localhost:18092/api/agents/register \
  -H "Authorization: Bearer e2e-userspace-key" -H "Content-Type: application/json" \
  -d '{"agent_id":"driver-work","role":"driver","workspace_id":"ws-work","workspace_name":"Work"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')
echo "work token: ${TOK_WORK:0:8}..."
```

- [ ] **Step 9.4: Login CLI as ws-personal + push a fixture mcp package**

```bash
/tmp/e2e-userspace/mcp-userspace login --url http://localhost:18092 --token "$TOK_PERSONAL"
mkdir -p /tmp/e2e-userspace/fixture-mcp/{src,tests}
cat >/tmp/e2e-userspace/fixture-mcp/spec.json <<'JSON'
{"name":"hello","version":1,"tools":[{"name":"hi","description":"says hi"}]}
JSON
cat >/tmp/e2e-userspace/fixture-mcp/src/server.py <<'PY'
print("hello mcp")
PY
cat >/tmp/e2e-userspace/fixture-mcp/capability_card.md <<'MD'
Says hi. Toy package for e2e.
MD
cat >/tmp/e2e-userspace/fixture-mcp/tests/cases.json <<'JSON'
{"cases":[]}
JSON
/tmp/e2e-userspace/mcp-userspace push /tmp/e2e-userspace/fixture-mcp --slug hello
```

Expected: stdout `pushed hello@0.1.0 (dedup=false, blob=<sha>)`.

- [ ] **Step 9.5: Search + list from ws-work (different token)**

```bash
/tmp/e2e-userspace/mcp-userspace login --url http://localhost:18092 --token "$TOK_WORK"
/tmp/e2e-userspace/mcp-userspace search "hello"
/tmp/e2e-userspace/mcp-userspace list --workspace mine   # should be empty (ws-work hasn't installed it)
/tmp/e2e-userspace/mcp-userspace list --workspace all    # should show hello
```

- [ ] **Step 9.6: Pull + install in ws-work**

```bash
mkdir -p /tmp/e2e-userspace/work-cwd && cd /tmp/e2e-userspace/work-cwd
/tmp/e2e-userspace/mcp-userspace install hello@0.1.0 --as mcp
ls generated_mcp/hello/
# Now ws-work has it installed
/tmp/e2e-userspace/mcp-userspace list --workspace mine
```

Expected: `generated_mcp/hello/` contains `spec.json`, `src/server.py`, etc. `list --workspace mine` now shows `hello` with `installed_version: "0.1.0"`.

- [ ] **Step 9.7: Skill package round-trip**

```bash
mkdir -p /tmp/e2e-userspace/fixture-skill/skill
cat >/tmp/e2e-userspace/fixture-skill/skill/SKILL.md <<'MD'
---
name: e2e-skill
description: test skill
---

Body.
MD
cat >/tmp/e2e-userspace/fixture-skill/capability_card.md <<'MD'
Toy skill.
MD
/tmp/e2e-userspace/mcp-userspace login --url http://localhost:18092 --token "$TOK_PERSONAL"
/tmp/e2e-userspace/mcp-userspace push /tmp/e2e-userspace/fixture-skill --slug e2e_skill

# Install in ws-work
/tmp/e2e-userspace/mcp-userspace login --url http://localhost:18092 --token "$TOK_WORK"
/tmp/e2e-userspace/mcp-userspace install e2e_skill@0.1.0 --as skill --scope project --project-root /tmp/e2e-userspace/work-cwd
ls /tmp/e2e-userspace/work-cwd/.claude/skills/e2e-skill/
```

Expected: skill dir contains `SKILL.md` and nothing leaked elsewhere.

- [ ] **Step 9.8: Cross-workspace write 403**

```bash
# ws-work token trying to write into ws-personal installations → expect 403
curl -sS -X POST -w "\nHTTP %{http_code}\n" \
  -H "Authorization: Bearer $TOK_WORK" \
  -H "Content-Type: application/json" \
  -d '{"version":"0.1.0"}' \
  http://localhost:18092/api/userspace/workspaces/ws-personal/installations/hello
```

Expected: HTTP 403 + "cross-workspace write not allowed".

- [ ] **Step 9.9: Cleanup**

```bash
kill "$(cat /tmp/e2e-userspace/server.pid)" 2>/dev/null
sleep 1
ss -tlnp 2>/dev/null | grep 18092 || echo "port free"
rm -rf /tmp/e2e-userspace/
```

- [ ] **Step 9.10: Commit (no code changes — but document via a test or a HISTORY note)**

The smoke is verification, not new code. **Do NOT add a commit** for this step alone. If you want a record, add a top-level `tests/e2e/userspace_smoke.md` describing the steps, or skip and rely on the plan's existence.

Optional:

```bash
mkdir -p multi-agent/tests/e2e
cat > multi-agent/tests/e2e/userspace_smoke.md <<'EOF'
# userspace local-gray smoke

Pre-merge verification recorded 2026-05-26.
[paste outputs of steps 9.4 / 9.5 / 9.7 / 9.8]
EOF
git add multi-agent/tests/e2e/userspace_smoke.md
git commit -m "test(e2e): local-gray smoke record for userspace v1"
```

---

## Task 10: `skills/userspace-publish/SKILL.md` — driver-side authoring guide

**Goal:** Tell Claude (running inside driver) "when the user finishes a new MCP/skill in this driver, here's how to push it to their personal space."

**Files:**
- Create: `skills/userspace-publish/SKILL.md`

### Steps

- [ ] **Step 10.1: Write the skill markdown**

```markdown
---
name: userspace-publish
description: Use when the user has finished iterating on an MCP server or skill inside this driver and wants to save it to their personal observer-backed space for use on other devices or workspaces.
---

# userspace-publish

Push a freshly-built MCP package or skill to the user's personal space hosted on observer-server.

## When to use

- User says "save this to my space", "I want to use this on my laptop too", "publish this skill/MCP", "push it to userspace"
- After a `register_slave_mcp` succeeds AND the user expresses intent to reuse

Do NOT auto-push without the user asking. This skill is opt-in.

## Preconditions

- `mcp-userspace` CLI is on PATH (built from `cmd/mcp-userspace/`)
- `~/.mcp-userspace/config.yaml` has the observer URL + this agent's token (run `mcp-userspace login --url ... --token ...` once)
- The package directory contains either `spec.json` + `src/server.py` (kind=mcp) or `skill/SKILL.md` (kind=skill)
- `capability_card.md` exists in the directory — write one if missing

## Steps

1. Confirm with user which package they mean (path + slug).
2. If `capability_card.md` is missing, draft a 3-5 sentence description focused on what the package DOES (not how it's implemented) and offer it for review.
3. Run `mcp-userspace push <dir> --slug <slug>` — if first push, omits `--bump-*`; if updating, ask whether minor or patch bump.
4. Read the response. If `dedup=true`, tell the user the exact same bytes were already there (someone pushed identical content before).
5. Tell the user how to install on another device:
   `mcp-userspace login --url <observer-url> --token <that-device-token>; mcp-userspace install <slug>@<ver>`

## Failure modes

- **HTTP 409 version already exists**: user must bump version. Ask which axis (patch / minor) and re-run with `--bump-*`.
- **HTTP 400 kind mismatch**: a different slug already holds this name as the other kind (mcp vs skill). Suggest renaming.
- **HTTP 401**: token expired or wrong. Re-run `mcp-userspace login` with a fresh token from the observer.
- **HTTP 413**: package too large. Help user trim (the limits are in spec §8: 10 MiB compressed, 5 MiB per file).

## What NOT to do

- Do not push without explicit user request.
- Do not silently bump versions; always confirm bump axis.
- Do not write or modify files under `~/.mcp-userspace/` directly — always go through the CLI.
```

- [ ] **Step 10.2: Commit**

```bash
git add skills/userspace-publish/SKILL.md
git commit -m "feat(skills): userspace-publish authoring skill for driver-side Claude"
```

---

## Self-review checklist

After all tasks land:

1. **Whole-repo green**: `cd multi-agent && go test ./...` 0 FAILs
2. **No stale references**: `grep -rn "user_token\|device_token\|owner_user_id\|cmd/mcp-userspace-cli" multi-agent/` → 0 hits
3. **observerweb didn't grow in surprising ways**: business-table handlers unchanged
4. **userspace can't reach observer business tables**: `grep -rnE "FROM (events|tasks|subtasks|artifacts|writes|mcp_servers)" multi-agent/internal/userspace/` → 0 hits
5. **Smoke executed**: §9 steps run + outputs recorded
6. **CLI binary < 20 MiB** (sanity): `ls -la /tmp/e2e-userspace/mcp-userspace`
7. **Memory honored**: e2e per [[e2e_required_for_features_and_fixes]] ran before any merge

If anything is missing, fix inline before invoking finishing-a-development-branch.

---

## Follow-up (out of this plan)

- **v1.1** `internal/mcpmarket/scanner` + scan_report surfaced in CLI install (informational, --strict to block on high)
- **v1.1** `internal/userspace/promote` + `mcp-userspace promote` — requires marketplace landed + publisher key local
- **v1.1** Embedding-backed search alongside FTS5 (currently FTS5 only)
- **v1.2** `mcp-userspace sync` polling latest versions across user's workspaces
- **v1.2** Web UI for browsing personal space (mount under observerweb dashboard)
