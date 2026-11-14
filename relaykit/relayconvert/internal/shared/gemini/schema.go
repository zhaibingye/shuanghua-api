package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/internal/convdiag"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
)

var geminiOpenAPISchemaAllowedFields = map[string]struct{}{
	"anyOf":            {},
	"default":          {},
	"description":      {},
	"enum":             {},
	"example":          {},
	"format":           {},
	"items":            {},
	"maxItems":         {},
	"maxLength":        {},
	"maxProperties":    {},
	"maximum":          {},
	"minItems":         {},
	"minLength":        {},
	"minProperties":    {},
	"minimum":          {},
	"nullable":         {},
	"pattern":          {},
	"properties":       {},
	"propertyOrdering": {},
	"required":         {},
	"title":            {},
	"type":             {},
}

const geminiFunctionSchemaMaxDepth = 64

// CleanFunctionParametersJSON projects function parameters to typed Schema.
func CleanFunctionParametersJSON(parameters json.RawMessage) (json.RawMessage, error) {
	_, schema, _, err := FunctionParameters(parameters, false)
	if err != nil || schema == nil {
		return nil, err
	}
	return kitutil.Marshal(schema)
}

func geminiSchemaType(value ojson.Result) string {
	switch name := strings.ToUpper(strings.TrimSpace(value.Str)); name {
	case "OBJECT", "ARRAY", "STRING", "INTEGER", "NUMBER", "BOOLEAN", "NULL":
		return name
	default:
		return ""
	}
}

// nullableUnion lowers an explicit type-or-null union. Only annotations can
// be merged from the outer schema because other siblings may constrain null.
func nullableUnion(schema map[string]ojson.Result, keyword string) map[string]ojson.Result {
	branches := schema[keyword].Array()
	if len(branches) != 2 {
		return nil
	}
	nullBranch, valueBranch := branches[0].Map(), branches[1].Map()
	if geminiSchemaType(nullBranch["type"]) != "NULL" {
		nullBranch, valueBranch = valueBranch, nullBranch
	}
	valueType := geminiSchemaType(valueBranch["type"])
	if len(nullBranch) != 1 || geminiSchemaType(nullBranch["type"]) != "NULL" || valueType == "" || valueType == "NULL" {
		return nil
	}
	// A nullable value branch could overlap the null branch in oneOf.
	if valueBranch["nullable"].Type == ojson.True || valueBranch["anyOf"].Exists() || valueBranch["oneOf"].Exists() {
		return nil
	}
	lowered := maps.Clone(valueBranch)
	for key := range schema {
		switch key {
		case keyword, "$schema":
		case "title", "description", "default", "example":
			lowered[key] = schema[key]
		default:
			return nil
		}
	}
	lowered["nullable"] = ojson.Result{Type: ojson.True, Raw: "true"}
	return lowered
}

func projectSchema(schema ojson.Result, depth int, losses map[string]struct{}) map[string]any {
	if !schema.IsObject() {
		if schema.Type == ojson.True || schema.Type == ojson.False {
			losses["boolean schemas"] = struct{}{}
		} else {
			losses["invalid schema"] = struct{}{}
		}
		return map[string]any{}
	}
	object := schema.Map()
	for _, keyword := range []string{"anyOf", "oneOf"} {
		if lowered := nullableUnion(object, keyword); lowered != nil {
			object = lowered
			break
		}
	}

	typed := make(map[string]any, len(object))
	for key, value := range object {
		if _, allowed := geminiOpenAPISchemaAllowedFields[key]; !allowed {
			// true is the JSON Schema default. Developer API's typed Schema
			// has no additionalProperties field for other values.
			if key != "$schema" && (key != "additionalProperties" || value.Type != ojson.True) {
				losses[key] = struct{}{}
			}
			continue
		}
		switch key {
		case "type":
			// Projected after the other fields to account for sibling constraints.
		case "properties", "items", "anyOf":
			if depth >= geminiFunctionSchemaMaxDepth {
				losses[fmt.Sprintf("nesting deeper than %d levels", geminiFunctionSchemaMaxDepth)] = struct{}{}
				continue
			}
			switch key {
			case "properties":
				if !value.IsObject() {
					losses[key] = struct{}{}
					continue
				}
				converted := make(map[string]any)
				var order []string
				for name, property := range value.All() {
					if _, exists := converted[name.Str]; !exists {
						order = append(order, name.Str)
					}
					converted[name.Str] = projectSchema(property, depth+1, losses)
				}
				typed[key] = converted
				if !object["propertyOrdering"].Exists() && len(order) > 1 {
					typed["propertyOrdering"] = order
				}
			case "items":
				if value.IsArray() {
					losses["tuple items"] = struct{}{}
					tuple := value.Array()
					if len(tuple) == 0 {
						continue
					}
					value = tuple[0]
				}
				typed[key] = projectSchema(value, depth+1, losses)
			case "anyOf":
				if !value.IsArray() {
					losses[key] = struct{}{}
					continue
				}
				branches := value.Array()
				converted := make([]any, len(branches))
				for i, branch := range branches {
					converted[i] = projectSchema(branch, depth+1, losses)
					if geminiSchemaType(branch.Get("type")) == "NULL" {
						// Prefer raw JSON Schema when the union cannot use nullable.
						losses["complex nullable unions"] = struct{}{}
					}
				}
				typed[key] = converted
			}
		case "enum":
			if !value.IsArray() || slices.ContainsFunc(value.Array(), func(element ojson.Result) bool {
				return element.Type != ojson.String
			}) {
				losses["non-string enum values"] = struct{}{}
				continue
			}
			typed[key] = json.RawMessage(value.Raw)
		default:
			if _, isBound := geminiSchemaInt64Fields[key]; isBound && value.Type == ojson.Number {
				// ProtoJSON accepts quoted numeric literals for int64 fields.
				// Keep the literal to avoid rounding large bounds via float64.
				typed[key] = value.Raw
				continue
			}
			typed[key] = json.RawMessage(value.Raw)
		}
	}
	projectSchemaType(object, typed, losses)
	return typed
}

func projectSchemaType(source map[string]ojson.Result, typed map[string]any, losses map[string]struct{}) {
	value, exists := source["type"]
	if !exists {
		return
	}
	if name := geminiSchemaType(value); name != "" {
		typed["type"] = name
		return
	}
	if !value.IsArray() {
		losses["invalid type"] = struct{}{}
		return
	}
	typeNames := value.Array()
	if len(typeNames) == 2 && !source["enum"].Exists() && !source["anyOf"].Exists() && !source["oneOf"].Exists() {
		first, second := geminiSchemaType(typeNames[0]), geminiSchemaType(typeNames[1])
		if first == "NULL" {
			first, second = second, first
		}
		if first != "" && first != "NULL" && second == "NULL" {
			typed["type"] = first
			typed["nullable"] = true
			return
		}
	}
	losses["type arrays"] = struct{}{}
	if len(typed) != 0 || len(typeNames) == 0 || source["oneOf"].Exists() {
		return
	}
	// Use anyOf as a fallback for bare type arrays on typed-only routes.
	branches := make([]any, 0, len(typeNames))
	for _, value := range typeNames {
		name := geminiSchemaType(value)
		if name == "" {
			return
		}
		branches = append(branches, map[string]any{"type": name})
	}
	typed["anyOf"] = branches
}

// FunctionParameters selects the declaration field that can carry the schema.
// Unconstrained empty parameter lists are omitted.
func FunctionParameters(schema json.RawMessage, jsonSchema bool) (field string, value any, misfits []string, err error) {
	if len(schema) == 0 {
		return "parameters", nil, nil, nil
	}
	if !kitutil.Valid(schema) {
		return "", nil, nil, fmt.Errorf("invalid JSON schema")
	}
	decoded := ojson.ParseBytes(schema)
	if decoded.Type == ojson.Null {
		return "parameters", nil, nil, nil
	}
	typed, misfits := typedSchema(decoded)
	object := decoded.Map()
	properties := object["properties"]
	emptyObject := properties.IsObject() && len(properties.Map()) == 0
	if emptyObject {
		for key, item := range object {
			switch key {
			case "type", "properties":
				continue
			case "required":
				if item.IsArray() && len(item.Array()) == 0 {
					continue
				}
			}
			if _, allowed := geminiOpenAPISchemaAllowedFields[key]; allowed {
				misfits = append(misfits, "constraints on an empty object")
				break
			}
		}
	}
	if len(misfits) > 0 && jsonSchema {
		// Keep the client's bytes and property order.
		return "parametersJsonSchema", schema, nil, nil
	}
	if emptyObject {
		return "parameters", nil, misfits, nil
	}
	return "parameters", typed, misfits, nil
}

// ApplyResponseSchema chooses between responseSchema and responseJsonSchema
// using the same projection as function parameters.
func ApplyResponseSchema(ctx context.Context, config *dto.GeminiChatGenerationConfig, schema json.RawMessage, path string, jsonSchema bool) error {
	if !kitutil.Valid(schema) {
		return fmt.Errorf("%s: invalid JSON schema", path)
	}
	config.ResponseMimeType = "application/json"
	config.ResponseSchema = nil
	config.ResponseJsonSchema = nil
	decoded := ojson.ParseBytes(schema)
	if decoded.Type == ojson.Null {
		return nil
	}
	typed, misfits := typedSchema(decoded)
	if len(misfits) > 0 && jsonSchema {
		config.ResponseJsonSchema = schema
		return nil
	}
	config.ResponseSchema = typed
	if len(misfits) > 0 {
		convdiag.Add(ctx, types.ConversionDiagnostic{
			Code:     "gemini_schema_keyword_dropped",
			Path:     path,
			Message:  fmt.Sprintf("Gemini response schema cannot carry %s", strings.Join(misfits, ", ")),
			Severity: types.ConversionDiagnosticWarning,
		})
	}
	return nil
}

func typedSchema(schema ojson.Result) (map[string]any, []string) {
	found := make(map[string]struct{})
	typed := projectSchema(schema, 0, found)
	misfits := make([]string, 0, len(found))
	for misfit := range found {
		misfits = append(misfits, misfit)
	}
	slices.Sort(misfits)
	return typed, misfits
}

func OpenAIToolChoiceToConfig(toolChoice any) *dto.ToolConfig {
	if toolChoice == nil {
		return nil
	}

	if toolChoiceStr, ok := toolChoice.(string); ok {
		config := &dto.ToolConfig{
			FunctionCallingConfig: &dto.FunctionCallingConfig{},
		}
		switch toolChoiceStr {
		case "auto":
			config.FunctionCallingConfig.Mode = "AUTO"
		case "none":
			config.FunctionCallingConfig.Mode = "NONE"
		case "required":
			config.FunctionCallingConfig.Mode = "ANY"
		default:
			config.FunctionCallingConfig.Mode = "AUTO"
		}
		return config
	}

	if toolChoiceMap, ok := toolChoice.(map[string]interface{}); ok {
		if toolChoiceMap["type"] == "function" {
			config := &dto.ToolConfig{
				FunctionCallingConfig: &dto.FunctionCallingConfig{
					Mode: "ANY",
				},
			}
			if function, ok := toolChoiceMap["function"].(map[string]interface{}); ok {
				if name, ok := function["name"].(string); ok && name != "" {
					config.FunctionCallingConfig.AllowedFunctionNames = []string{name}
				}
			}
			return config
		}
		return nil
	}

	return nil
}

// geminiSchemaInt64Fields are Gemini Schema bounds typed int64, which the
// proto JSON encoding may send as strings.
var geminiSchemaInt64Fields = map[string]struct{}{
	"maxItems":      {},
	"minItems":      {},
	"maxLength":     {},
	"minLength":     {},
	"maxProperties": {},
	"minProperties": {},
}

// OpenAPISchemaToJSONSchema rewrites a schema in Gemini's OpenAPI subset as
// JSON Schema: type names are lowercased, nullable becomes a "null" type,
// example becomes examples, and int64 bounds sent as strings become numbers.
// It also returns the Gemini-only keywords JSON Schema cannot express, which
// are dropped (propertyOrdering).
func OpenAPISchemaToJSONSchema(schema any) (any, []string) {
	dropped := make(map[string]struct{})
	converted := openAPISchemaToJSONSchema(schema, 0, dropped)
	keywords := make([]string, 0, len(dropped))
	for keyword := range dropped {
		keywords = append(keywords, keyword)
	}
	slices.Sort(keywords)
	return converted, keywords
}

func openAPISchemaToJSONSchema(schema any, depth int, dropped map[string]struct{}) any {
	value, ok := schema.(map[string]any)
	if !ok || depth >= geminiFunctionSchemaMaxDepth {
		return schema
	}
	converted := make(map[string]any, len(value))
	nullable := false
	for key, item := range value {
		switch key {
		case "type":
			typeName, isString := item.(string)
			if !isString {
				converted[key] = item
				continue
			}
			typeName = strings.ToLower(strings.TrimSpace(typeName))
			if typeName != "" && typeName != "type_unspecified" {
				converted[key] = typeName
			}
		case "nullable":
			nullable, _ = item.(bool)
		case "propertyOrdering":
			dropped[key] = struct{}{}
		case "example":
			if _, exists := value["examples"]; exists {
				dropped[key] = struct{}{}
				continue
			}
			converted["examples"] = []any{item}
		case "properties":
			properties, isMap := item.(map[string]any)
			if !isMap {
				converted[key] = item
				continue
			}
			convertedProperties := make(map[string]any, len(properties))
			for name, property := range properties {
				convertedProperties[name] = openAPISchemaToJSONSchema(property, depth+1, dropped)
			}
			converted[key] = convertedProperties
		case "items":
			converted[key] = openAPISchemaToJSONSchema(item, depth+1, dropped)
		case "anyOf":
			variants, isArray := item.([]any)
			if !isArray {
				converted[key] = item
				continue
			}
			convertedVariants := make([]any, len(variants))
			for index, variant := range variants {
				convertedVariants[index] = openAPISchemaToJSONSchema(variant, depth+1, dropped)
			}
			converted[key] = convertedVariants
		default:
			if _, isInt64 := geminiSchemaInt64Fields[key]; isInt64 {
				if text, isString := item.(string); isString {
					if number, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64); err == nil {
						converted[key] = number
						continue
					}
				}
			}
			converted[key] = item
		}
	}
	if !nullable {
		return converted
	}
	switch typed := converted["type"].(type) {
	case string:
		converted["type"] = []any{typed, "null"}
	default:
		if variants, isArray := converted["anyOf"].([]any); isArray {
			converted["anyOf"] = append(variants, map[string]any{"type": "null"})
		}
	}
	if values, isArray := converted["enum"].([]any); isArray && !slices.Contains(values, nil) {
		converted["enum"] = append(values, nil)
	}
	return converted
}
