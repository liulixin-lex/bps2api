package basispoints

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestScaleProtocolNamespacedDirectCalls(t *testing.T) {
	for _, name := range []string{"read", "run_officejs", "update_plan"} {
		for _, kind := range []string{"function", "custom"} {
			t.Run(kind+"/"+name, func(t *testing.T) {
				source := testSource()
				source["tools"] = []any{object{"type": "function", "name": name}, object{"type": "namespace", "name": "client", "tools": []any{object{"type": kind, "name": name}}}}
				_, bridge := mustPrepare(t, source, "scale-namespace", nil)
				native := object{"type": kind + "_call", "id": "fc_native", "call_id": "call_native", "name": name, "namespace": "client", "arguments": "{}", "input": "exact source\r\n中文"}
				if kind == "custom" {
					native["type"] = "custom_tool_call"
				}
				result, err := bridge.translateCall(native)
				if err != nil || result["namespace"] != "client" || result["name"] != name || (kind == "custom" && result["input"] != native["input"]) {
					t.Fatalf("separate namespace must select its exact catalog tool: result=%v err=%v", result, err)
				}
				native["namespace"] = "unknown"
				if _, err := bridge.translateCall(native); err == nil {
					t.Fatal("unknown explicit namespace resolved to another tool")
				}
			})
		}
	}
}

func TestScaleProtocolCatalogDepth(t *testing.T) {
	var tools any = []any{object{"type": "function", "name": "read"}}
	for i := 0; i < 64; i++ {
		tools = []any{object{"type": "namespace", "name": "nested", "tools": tools}}
	}
	source := testSource()
	source["tools"] = tools
	raw, _ := json.Marshal(source)
	if _, _, err := Prepare(raw, "scale-depth", nil); err == nil || !strings.Contains(err.Error(), "namespace depth") {
		t.Fatalf("deep catalogs must fail with a bounded namespace error: %v", err)
	}
	if reason := NativeFallbackReason(raw); reason != "tool_namespace_depth" {
		t.Fatalf("native routing should preserve deep catalog, got %q", reason)
	}
}

func TestScaleProtocolTerminalIntegrity(t *testing.T) {
	call := func(id, callID string) object {
		return object{"type": "function_call", "id": id, "call_id": callID, "name": "read", "arguments": "{}"}
	}
	cases := map[string]object{
		"missing_response":  {"type": "response.completed"},
		"missing_output":    {"type": "response.completed", "response": object{"status": "completed"}},
		"duplicate_call_id": {"type": "response.completed", "response": object{"status": "completed", "output": []any{call("fc_1", "call_1"), call("fc_2", "call_1")}}},
		"duplicate_item_id": {"type": "response.completed", "response": object{"status": "completed", "output": []any{call("fc_1", "call_1"), call("fc_1", "call_2")}}},
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			source := testSource()
			source["tools"] = []any{object{"type": "function", "name": "read"}}
			_, bridge := mustPrepare(t, source, "scale-terminal", nil)
			encoded, _ := json.Marshal(payload)
			body := bridge.Stream(io.NopCloser(strings.NewReader("data: " + string(encoded) + "\n\n")))
			defer body.Close()
			result, err := io.ReadAll(body)
			if err != nil || !strings.Contains(string(result), "basispoints_protocol_error") || strings.Contains(string(result), "event: response.completed") || strings.Contains(string(result), "event: response.output_item.added") {
				t.Fatalf("malformed completion must not emit callable items or success: %s %v", result, err)
			}
		})
	}
}

func TestScaleProtocolAdditionalNativeHistory(t *testing.T) {
	for _, kind := range []string{"local_shell_call", "local_shell_call_output", "shell_call", "shell_call_output", "apply_patch_call", "apply_patch_call_output"} {
		raw, _ := json.Marshal(object{"input": []any{object{"type": kind, "call_id": "call_previous"}}})
		if reason := NativeFallbackReason(raw); reason != "native_history" {
			t.Errorf("%s must remain on native route, got %q", kind, reason)
		}
	}
	raw, _ := json.Marshal(object{"input": []any{object{"role": "assistant", "content": []any{object{"type": "output_audio", "audio_url": "data:audio/wav;base64,AA=="}}}}})
	if reason := NativeFallbackReason(raw); reason != "native_media" {
		t.Errorf("output audio must remain on native route, got %q", reason)
	}
}

func TestScaleProtocolSourceJSONPointerSchemas(t *testing.T) {
	cases := map[string]object{
		"array_pointer":           {"type": "object", "properties": object{"code": object{"$ref": "#/anyOf/0/properties/source"}}, "anyOf": []any{object{"type": "object", "properties": object{"source": object{"type": "string"}}}}},
		"root_id_reference_chain": {"$id": "https://schemas.invalid/source.json", "type": "object", "properties": object{"code": object{"$ref": "#/$defs/alias"}}, "$defs": object{"alias": object{"$ref": "#/$defs/source"}, "source": object{"type": "string"}}},
	}
	for name, schema := range cases {
		t.Run(name, func(t *testing.T) {
			if field := functionCodeTransportField(schema); field != "code" {
				t.Fatalf("local string source reference not recognized: %q", field)
			}
		})
	}
}
