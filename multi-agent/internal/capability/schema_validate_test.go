package capability

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestValidateArgsAcceptsValidObject(t *testing.T) {
	schema := raw(`{"type":"object","properties":{"n":{"type":"integer"},"output_dir":{"type":"string"}},"required":["n"],"additionalProperties":false}`)
	err := ValidateArgs(schema, map[string]interface{}{"n": float64(7), "output_dir": "/tmp/out"})
	require.NoError(t, err)
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

func TestValidateArgsRejectsWrongPrimitiveType(t *testing.T) {
	schema := raw(`{"type":"object","properties":{"n":{"type":"integer"},"enabled":{"type":"boolean"}},"required":["n","enabled"],"additionalProperties":false}`)
	err := ValidateArgs(schema, map[string]interface{}{"n": "7", "enabled": true})
	require.Error(t, err)
	require.Contains(t, err.Error(), "argument n must be integer")
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
