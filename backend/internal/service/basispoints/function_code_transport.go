package basispoints

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

const functionCodeTransportPrefix = "codex2api.function_code/"

// Executable text stays in a single native string rather than nested JSON.
// A declared code field takes precedence; command fields must be unambiguous.
func supportsFunctionCodeTransport(name, kind string, parameters any) bool {
	if kind != "function" || name == "" || strings.ContainsAny(name, "/\\") || strings.IndexFunc(name, unicode.IsSpace) >= 0 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return false
	}
	return functionCodeTransportField(parameters) != ""
}

func functionCodeTransportField(parameters any) string {
	schema, _ := parameters.(object)
	if schema["type"] != "object" {
		return ""
	}
	properties, _ := schema["properties"].(object)
	isString := func(name string) bool {
		field, _ := properties[name].(object)
		return field["type"] == "string"
	}
	if isString("code") {
		return "code"
	}
	selected := ""
	for _, name := range []string{"cmd", "command", "script"} {
		if isString(name) {
			if selected != "" {
				return ""
			}
			selected = name
		}
	}
	return selected
}

// Keep executable text in the native code string. extended_summary carries only
// the other JSON arguments; the server serializes the final client arguments.
// No source text is parsed, repaired, evaluated or treated as another tool call.
func (b *Bridge) functionCodeTransportEnvelope(arguments object) (object, bool, error) {
	summary, ok := arguments["summary"].(string)
	if !ok || !strings.HasPrefix(summary, functionCodeTransportPrefix) {
		return nil, false, nil
	}
	name := strings.TrimPrefix(summary, functionCodeTransportPrefix)
	info, allowed := b.tools[name]
	if !allowed || !supportsFunctionCodeTransport(name, info.Kind, info.Parameters) {
		return nil, true, fmt.Errorf("basispoints function code transport requires an exact catalog function with a declared string source parameter")
	}
	code, codeOK := arguments["code"].(string)
	metadata, metadataOK := arguments["extended_summary"].(string)
	if !codeOK || !metadataOK {
		return nil, true, fmt.Errorf("basispoints function code transport requires string code and JSON arguments in extended_summary")
	}
	if len(code) > maxEnvelopeBytes || len(metadata) > maxEnvelopeBytes-len(code) {
		return nil, true, fmt.Errorf("basispoints function code transport exceeds the size limit")
	}
	var args object
	if decode([]byte(metadata), &args) != nil || args == nil {
		return nil, true, fmt.Errorf("basispoints function code transport extended_summary must contain one JSON object")
	}
	field := functionCodeTransportField(info.Parameters)
	if _, exists := args[field]; exists {
		return nil, true, fmt.Errorf("basispoints function code transport must not duplicate %s in extended_summary", field)
	}
	args[field] = code
	encoded, err := json.Marshal(args)
	if err != nil || len(encoded) > maxEnvelopeBytes {
		return nil, true, fmt.Errorf("basispoints function code transport arguments exceed the size limit")
	}
	return object{"name": name, "arguments": args}, true, nil
}

func encodeFunctionCodeTransport(name string, args object, field string) (object, error) {
	metadata := make(object, len(args))
	for key, value := range args {
		if key != field {
			metadata[key] = value
		}
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("basispoints history function metadata cannot be serialized")
	}
	return object{
		"summary": functionCodeTransportPrefix + name, "code": args[field],
		"extended_summary": string(encoded), "destructive": false, "references": []any{},
	}, nil
}
