package basispoints

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

const functionCodeTransportPrefix = "codex2api.function_code/"

// Executable text stays in a single native string rather than nested JSON.
// A declared code field takes precedence; command fields must be unambiguous.
func supportsFunctionCodeTransport(name, kind string, parameters any) bool {
	if kind != "function" || !validSourceTransportName(name) {
		return false
	}
	return functionCodeTransportField(parameters) != ""
}

func validSourceTransportName(name string) bool {
	return name != "" && !strings.ContainsAny(name, "/\\") && strings.IndexFunc(name, unicode.IsSpace) < 0 && strings.IndexFunc(name, unicode.IsControl) < 0
}

func functionCodeTransportField(parameters any) string {
	schema, _ := parameters.(object)
	if schema["type"] != "object" {
		return ""
	}
	properties, _ := schema["properties"].(object)
	isString := func(name string) bool {
		field, _ := properties[name].(object)
		return schemaAcceptsSourceString(field, schema, 0)
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
	// Some Responses clients wrap a raw custom tool as a function whose
	// declared input is a string. Preserve that exact contract and payload.
	if selected == "" && isString("input") {
		return "input"
	}
	return selected
}

// Nullable string contracts remain valid when the supplied source is a string.
// Only explicit string branches qualify, including bounded in-document references.
func schemaAcceptsSourceString(schema, root object, depth int) bool {
	if depth > 16 {
		return false
	}
	// A nested resource changes local reference scope. Explicit string branches
	// remain recognizable, but descendants must not resolve against the outer
	// document after anyOf/oneOf traversal has hidden that resource boundary.
	if schema["$id"] != nil {
		root = nil
	}
	if ref, ok := schema["$ref"].(string); ok {
		// No network, files or dynamic references; cycles stop at the depth cap.
		if !strings.HasPrefix(ref, "#/") || schema["$id"] != nil {
			return false
		}
		var node any = root
		for index, component := range strings.Split(ref[2:], "/") {
			component = strings.ReplaceAll(strings.ReplaceAll(component, "~1", "/"), "~0", "~")
			switch fields := node.(type) {
			case object:
				// The document root may have an ID. Only a nested resource ID
				// changes resolution scope and requires a full schema resolver.
				if fields["$id"] != nil && index > 0 {
					return false
				}
				node = fields[component]
			case []any:
				position, err := strconv.Atoi(component)
				if err != nil || position < 0 || position >= len(fields) || strconv.Itoa(position) != component {
					return false
				}
				node = fields[position]
			default:
				return false
			}
		}
		child, _ := node.(object)
		return schemaAcceptsSourceString(child, root, depth+1)
	}
	if schema["type"] == "string" {
		return true
	}
	if types, ok := schema["type"].([]any); ok {
		for _, kind := range types {
			if kind == "string" {
				return true
			}
		}
	}
	for _, keyword := range []string{"anyOf", "oneOf"} {
		branches, _ := schema[keyword].([]any)
		for _, branch := range branches {
			child, _ := branch.(object)
			if schemaAcceptsSourceString(child, root, depth+1) {
				return true
			}
		}
	}
	return false
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
	if !validSourceTransportName(name) || !allowed || (info.Kind != "custom" && !supportsFunctionCodeTransport(name, info.Kind, info.Parameters)) {
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
	// The marker identifies an exact registered tool, so a custom tool's raw
	// source is unambiguous even if the model chose the function-code spelling.
	// Nonempty metadata must never disappear during this lossless normalization.
	if info.Kind == "custom" {
		if len(args) != 0 {
			return nil, true, fmt.Errorf("basispoints custom source transport cannot discard function metadata")
		}
		return object{"name": name, "input": code}, true, nil
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
