package mcpserver

import (
	"github.com/teyfix/bloblang-lsp/internal/help"
	"github.com/teyfix/bloblang-lsp/internal/mapping"
)

func object(properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
}

func textSchema(maximum int) map[string]any {
	return map[string]any{"type": "string", "maxLength": maximum}
}

func pathSchema() map[string]any {
	schema := textSchema(4096)
	schema["minLength"] = 1
	return schema
}

func importsSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": textSchema(mapping.MaxRequestBytes), "maxProperties": 128}
}

func exclusiveRequired(names ...string) []any {
	out := make([]any, 0, len(names))
	for _, name := range names {
		out = append(out, map[string]any{"required": []string{name}})
	}
	return out
}

func runSchema() map[string]any {
	schema := object(map[string]any{
		"source":      textSchema(mapping.MaxRequestBytes),
		"mappingPath": pathSchema(),
		// A raw JSON value must stay unconstrained: []byte schema inference would
		// advertise a base64 string and exclude ordinary objects, arrays and null.
		"input":      map[string]any{},
		"inputPath":  pathSchema(),
		"samplePath": pathSchema(),
		"root":       map[string]any{},
		"metadata":   map[string]any{"type": "object", "additionalProperties": true},
		"env":        map[string]any{"type": "object", "additionalProperties": textSchema(mapping.MaxRequestBytes)},
		"imports":    importsSchema(),
		"timeoutMs":  map[string]any{"type": "integer", "minimum": 1, "maximum": 5000},
	})
	schema["allOf"] = []any{
		map[string]any{"oneOf": exclusiveRequired("source", "mappingPath")},
		map[string]any{"oneOf": append(exclusiveRequired("input", "inputPath", "samplePath"),
			map[string]any{"not": map[string]any{"anyOf": exclusiveRequired("input", "inputPath", "samplePath")}})},
	}
	return schema
}

func sourceSchema(format bool) map[string]any {
	properties := map[string]any{
		"source":      textSchema(mapping.MaxRequestBytes),
		"mappingPath": pathSchema(),
		"filename":    pathSchema(),
		"imports":     importsSchema(),
	}
	if format {
		properties["tabSize"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 16}
	}
	schema := object(properties)
	schema["oneOf"] = exclusiveRequired("source", "mappingPath")
	return schema
}

func searchSchema() map[string]any {
	query := object(map[string]any{"id": textSchema(help.MaxIDBytes), "text": textSchema(help.MaxQueryBytes)})
	query["required"] = []string{"id", "text"}
	schema := object(map[string]any{
		"queries":           map[string]any{"type": "array", "items": query, "minItems": 1, "maxItems": help.MaxQueries},
		"limitPerQuery":     map[string]any{"type": "integer", "minimum": 1, "maximum": help.MaxLimitPerQuery},
		"includeDeprecated": map[string]any{"type": "boolean"},
	})
	schema["required"] = []string{"queries"}
	return schema
}

func docsSchema() map[string]any {
	schema := object(map[string]any{
		"ids":               map[string]any{"type": "array", "items": textSchema(help.MaxIDBytes), "minItems": 1, "maxItems": help.MaxDocumentIDs},
		"includeDeprecated": map[string]any{"type": "boolean"},
	})
	schema["required"] = []string{"ids"}
	return schema
}
