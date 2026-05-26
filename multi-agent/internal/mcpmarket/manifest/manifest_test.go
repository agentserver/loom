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

func TestValidate_AcceptsSkillWithoutMCPFields(t *testing.T) {
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
