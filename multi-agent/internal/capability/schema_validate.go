package capability

import (
	"encoding/json"
	"fmt"
	"math"
)

type liteSchema struct {
	Type                 string                     `json:"type"`
	Properties           map[string]json.RawMessage `json:"properties"`
	Required             []string                   `json:"required"`
	AdditionalProperties interface{}                `json:"additionalProperties"`
}

func ValidateArgs(schema json.RawMessage, args map[string]interface{}) error {
	if len(schema) == 0 || string(schema) == "null" {
		return nil
	}

	var root liteSchema
	if err := json.Unmarshal(schema, &root); err != nil {
		return fmt.Errorf("invalid schema: %w", err)
	}
	if root.Type != "" && root.Type != "object" {
		return fmt.Errorf("schema top-level type must be object")
	}
	if root.Type == "" && len(root.Properties) == 0 && len(root.Required) == 0 && root.AdditionalProperties == nil {
		return nil
	}

	for _, name := range root.Required {
		if _, ok := args[name]; !ok {
			return fmt.Errorf("missing required argument %s", name)
		}
	}

	allowAdditional := allowsAdditionalProperties(root.AdditionalProperties)
	for name, value := range args {
		propertySchema, known := root.Properties[name]
		if !known {
			if allowAdditional {
				continue
			}
			return fmt.Errorf("unknown argument %s", name)
		}
		if err := validateValue(name, propertySchema, value); err != nil {
			return err
		}
	}

	return nil
}

func allowsAdditionalProperties(value interface{}) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case map[string]interface{}:
		return len(typed) > 0
	default:
		return false
	}
}

func validateValue(name string, schema json.RawMessage, value interface{}) error {
	if len(schema) == 0 || string(schema) == "null" {
		return nil
	}

	var property struct {
		Type interface{} `json:"type"`
	}
	if err := json.Unmarshal(schema, &property); err != nil {
		return fmt.Errorf("invalid schema for argument %s: %w", name, err)
	}

	types := schemaTypes(property.Type)
	if len(types) == 0 {
		return nil
	}
	for _, schemaType := range types {
		if matchesType(schemaType, value) {
			return nil
		}
	}
	return fmt.Errorf("argument %s must be %s", name, types[0])
}

func schemaTypes(value interface{}) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []interface{}:
		types := make([]string, 0, len(typed))
		for _, item := range typed {
			if s, ok := item.(string); ok {
				types = append(types, s)
			}
		}
		return types
	default:
		return nil
	}
}

func matchesType(schemaType string, value interface{}) bool {
	switch schemaType {
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		return isNumber(value)
	case "integer":
		return isInteger(value)
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "object":
		return isObject(value)
	case "array":
		return isArray(value)
	default:
		return true
	}
}

func isNumber(value interface{}) bool {
	switch value.(type) {
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	default:
		return false
	}
}

func isInteger(value interface{}) bool {
	switch typed := value.(type) {
	case float64:
		return math.Trunc(typed) == typed
	case float32:
		return math.Trunc(float64(typed)) == float64(typed)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	default:
		return false
	}
}

func isObject(value interface{}) bool {
	_, ok := value.(map[string]interface{})
	return ok
}

func isArray(value interface{}) bool {
	_, ok := value.([]interface{})
	return ok
}
