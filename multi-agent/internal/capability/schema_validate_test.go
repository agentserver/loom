package capability

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestFlatNamesDeduplicatesPreservingFirstOccurrenceAndSkipsEmpty(t *testing.T) {
	tools := []MCPToolDescriptor{
		{Name: "render", Server: "srv-a"},
		{Name: ""},
		{Name: "plan", Server: "srv-b"},
		{Name: "render", Server: "srv-c"},
	}

	require.Equal(t, []string{"render", "plan"}, FlatNames(tools))
}

func TestWithServerFillsOnlyMissingServer(t *testing.T) {
	tools := []MCPToolDescriptor{
		{Name: "render"},
		{Name: "plan", Server: "existing"},
	}

	got := WithServer("default", tools)

	require.Equal(t, "default", got[0].Server)
	require.Equal(t, "existing", got[1].Server)
	require.Empty(t, tools[0].Server)
}

func TestFindToolMatchesServerAndName(t *testing.T) {
	tools := []MCPToolDescriptor{
		{Server: "srv-a", Name: "render", Description: "wrong server"},
		{Server: "srv-b", Name: "render", Description: "match"},
	}

	got, ok := FindTool(tools, "srv-b", "render")
	require.True(t, ok)
	require.Equal(t, "match", got.Description)

	_, ok = FindTool(tools, "srv-c", "render")
	require.False(t, ok)
}

func TestValidateArgsAcceptsValidObject(t *testing.T) {
	schema := raw(`{"type":"object","properties":{"n":{"type":"integer"},"output_dir":{"type":"string"}},"required":["n"],"additionalProperties":false}`)
	err := ValidateArgs(schema, map[string]interface{}{"n": float64(7), "output_dir": "/tmp/out"})
	require.NoError(t, err)
}

func TestValidateArgsEmptyAndNullSchemaAllowArbitraryArgs(t *testing.T) {
	args := map[string]interface{}{"unknown": "ok", "n": float64(1)}

	require.NoError(t, ValidateArgs(nil, args))
	require.NoError(t, ValidateArgs(raw(`null`), args))
}

func TestValidateArgsRejectsTopLevelNonObjectSchema(t *testing.T) {
	err := ValidateArgs(raw(`{"type":"string"}`), map[string]interface{}{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "schema top-level type must be object")
}

func TestValidateArgsRejectsMissingRequired(t *testing.T) {
	schema := raw(`{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"],"additionalProperties":false}`)
	err := ValidateArgs(schema, map[string]interface{}{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing required argument n")
}

func TestValidateArgsRejectsUnknownArgumentByDefault(t *testing.T) {
	schema := raw(`{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"],"additionalProperties":false}`)
	err := ValidateArgs(schema, map[string]interface{}{"n": float64(7), "put_url_128": "http://x"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown argument put_url_128")
}

func TestValidateArgsAllowsAdditionalPropertiesWhenExplicit(t *testing.T) {
	schema := raw(`{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"],"additionalProperties":true}`)
	err := ValidateArgs(schema, map[string]interface{}{"n": float64(7), "extra": "ok"})
	require.NoError(t, err)
}

func TestValidateArgsAllowsAdditionalPropertiesWithNonEmptySchemaObject(t *testing.T) {
	schema := raw(`{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"],"additionalProperties":{"type":"string"}}`)
	err := ValidateArgs(schema, map[string]interface{}{"n": float64(7), "extra": "ok"})
	require.NoError(t, err)
}

func TestValidateArgsRejectsWrongPrimitiveType(t *testing.T) {
	schema := raw(`{"type":"object","properties":{"n":{"type":"integer"},"enabled":{"type":"boolean"}},"required":["n","enabled"],"additionalProperties":false}`)
	err := ValidateArgs(schema, map[string]interface{}{"n": "7", "enabled": true})
	require.Error(t, err)
	require.Contains(t, err.Error(), "argument n must be integer")
}

func TestValidateArgsEnforcesNumberObjectAndArrayTypes(t *testing.T) {
	schema := raw(`{"type":"object","properties":{"score":{"type":"number"},"meta":{"type":"object"},"items":{"type":"array"}},"required":["score","meta","items"],"additionalProperties":false}`)

	err := ValidateArgs(schema, map[string]interface{}{
		"score": float64(9.5),
		"meta":  map[string]interface{}{"kind": "demo"},
		"items": []interface{}{"a", "b"},
	})
	require.NoError(t, err)

	err = ValidateArgs(schema, map[string]interface{}{
		"score": "9.5",
		"meta":  map[string]interface{}{"kind": "demo"},
		"items": []interface{}{"a", "b"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "argument score must be number")

	err = ValidateArgs(schema, map[string]interface{}{
		"score": float64(9.5),
		"meta":  []interface{}{"not-object"},
		"items": []interface{}{"a", "b"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "argument meta must be object")

	err = ValidateArgs(schema, map[string]interface{}{
		"score": float64(9.5),
		"meta":  map[string]interface{}{"kind": "demo"},
		"items": "not-array",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "argument items must be array")
}

func TestValidateArgsAcceptsTopLevelTypeArray(t *testing.T) {
	schema := raw(`{"type":["object"],"properties":{"name":{"type":"string"}},"required":["name"],"additionalProperties":false}`)
	err := ValidateArgs(schema, map[string]interface{}{"name": "render"})
	require.NoError(t, err)
}

func TestValidateArgsPropertyStringOrNullRejectsNumberAndAcceptsNil(t *testing.T) {
	schema := raw(`{"type":"object","properties":{"name":{"type":["string","null"]}},"required":["name"],"additionalProperties":false}`)

	err := ValidateArgs(schema, map[string]interface{}{"name": float64(123)})
	require.Error(t, err)
	require.Contains(t, err.Error(), "argument name must be string")

	err = ValidateArgs(schema, map[string]interface{}{"name": nil})
	require.NoError(t, err)
}

func TestValidateArgsRejectsUnsupportedPropertyType(t *testing.T) {
	schema := raw(`{"type":"object","properties":{"name":{"type":"strng"}},"required":["name"],"additionalProperties":false}`)
	err := ValidateArgs(schema, map[string]interface{}{"name": "render"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "argument name must be strng")
}

func TestExtractFromAgentCardSupportsStructuredAndFlatTools(t *testing.T) {
	card := json.RawMessage(`{
        "tools":["legacy"],
        "mcp_tools":[{"server":"srv","name":"render","input_schema":{"type":"object"}}]
    }`)
	desc, flat := ExtractFromAgentCard(card)
	require.Equal(t, []string{"legacy"}, flat)
	require.Len(t, desc, 1)
	require.Equal(t, "srv", desc[0].Server)
	require.Equal(t, "render", desc[0].Name)
}
