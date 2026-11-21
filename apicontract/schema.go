package apicontract

import (
	"encoding/json"
	"reflect"
	"strings"
	"time"
)

func embeddedStruct(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Kind() == reflect.Struct
}

// Schema describes a JSON response from its Go representation. Summary payloads
// use finite structs and maps, avoiding duplicate hand-maintained field lists.
func Schema[T any]() map[string]any { return schema(reflect.TypeFor[T]()) }
func schema(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeFor[time.Time]() {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	if t == reflect.TypeFor[json.RawMessage]() {
		return map[string]any{}
	}
	switch t.Kind() {
	case reflect.Struct:
		properties := map[string]any{}
		required := []string{}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			// encoding/json promotes the exported fields of an unexported
			// embedded struct; every other unexported field is skipped.
			if !field.IsExported() && !(field.Anonymous && embeddedStruct(field.Type)) {
				continue
			}
			tag := strings.Split(field.Tag.Get("json"), ",")
			if tag[0] == "-" {
				continue
			}
			child := schema(field.Type)
			if field.Anonymous && tag[0] == "" {
				if nested, ok := child["properties"].(map[string]any); ok {
					for name, value := range nested {
						properties[name] = value
					}
				}
				if names, ok := child["required"].([]string); ok {
					required = append(required, names...)
				}
				continue
			}
			name := tag[0]
			if name == "" {
				name = field.Name
			}
			properties[name] = child
			optional := false
			for _, flag := range tag[1:] {
				optional = optional || flag == "omitempty" || flag == "omitzero"
			}
			if !optional {
				required = append(required, name)
			}
		}
		return map[string]any{"type": "object", "properties": properties, "required": required}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": schema(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": schema(t.Elem())}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.String:
		return map[string]any{"type": "string"}
	default:
		return map[string]any{}
	}
}
