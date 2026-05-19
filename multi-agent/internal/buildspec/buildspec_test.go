package buildspec

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseJSON_NormalizesDefaultsAndLists(t *testing.T) {
	raw := `{
        "name":" lesson_builder ",
        "description":"build lesson docs",
        "tools":[{"name":"render","description":"render doc","args_schema":{"type":"object"},"result_description":"doc path"}],
        "allowed_packages":["python-docx","python-docx"],
        "compose_servers":["textbook","textbook"]
    }`

	spec, err := ParseJSON(raw)
	require.NoError(t, err)
	require.Equal(t, "lesson_builder", spec.Name)
	require.Equal(t, 1, spec.Version)
	require.Equal(t, 1, spec.Iteration)
	require.Equal(t, 3, spec.MaxIterations)
	require.Equal(t, []string{"python-docx"}, spec.AllowedPackages)
	require.Equal(t, []string{"textbook"}, spec.ComposeServers)

	canonical, err := MarshalCanonical(spec)
	require.NoError(t, err)
	require.JSONEq(t, `{
        "name":"lesson_builder",
        "description":"build lesson docs",
        "tools":[{"name":"render","description":"render doc","args_schema":{"type":"object"},"result_description":"doc path"}],
        "hints":"",
        "allowed_packages":["python-docx"],
        "compose_servers":["textbook"],
        "version":1,
        "iteration":1,
        "max_iterations":3
    }`, canonical)
}

func TestParseJSON_RejectsNaturalLanguage(t *testing.T) {
	_, err := ParseJSON("please build a reusable MCP server")
	require.Error(t, err)
	require.ErrorContains(t, err, "malformed mcp build spec")
}

func TestParseJSON_RejectsTrailingProse(t *testing.T) {
	raw := `{
		"name":"lesson_builder",
		"description":"build lesson docs",
		"tools":[{"name":"render","description":"render doc","args_schema":{"type":"object"},"result_description":"doc path"}]
	}
	extra prose`

	_, err := ParseJSON(raw)
	require.Error(t, err)
	require.ErrorContains(t, err, "malformed mcp build spec")
}

func TestValidate_RejectsInvalidNameAndMissingTools(t *testing.T) {
	err := Validate(Spec{Name: "Bad-Name", Version: 1, Iteration: 1, MaxIterations: 3})
	require.ErrorContains(t, err, "invalid name")

	err = Validate(Spec{Name: "ok", Version: 1, Iteration: 1, MaxIterations: 3})
	require.ErrorContains(t, err, "tools must have at least 1 entry")
}

func TestValidate_RequiresPriorPathForUpgrade(t *testing.T) {
	spec := Spec{
		Name: "lesson_builder", Description: "d", Version: 2, Iteration: 1, MaxIterations: 3,
		Tools: []ToolSpec{{Name: "render", Description: "d", ArgsSchema: json.RawMessage(`{"type":"object"}`), ResultDescription: "r"}},
	}
	err := Validate(spec)
	require.ErrorContains(t, err, "prior_path required")
}
