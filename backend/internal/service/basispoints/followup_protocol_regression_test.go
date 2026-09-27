package basispoints

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestFollowupProtocolRejectsTranslatedItemIDCollision(t *testing.T) {
	source := testSource()
	source["tools"] = []any{object{"type": "custom", "name": "patch"}, object{"type": "function", "name": "read"}}
	_, bridge := mustPrepare(t, source, "followup-identities", nil)
	// Original IDs differ, but custom normalization derives its public ID
	// from call_id. A second item must not claim that rewritten identity.
	response := object{"type": "response.completed", "response": object{"status": "completed", "output": []any{
		object{"type": "custom_tool_call", "id": "ctc_native", "call_id": "call_patch", "name": "patch", "input": "exact patch source"},
		object{"type": "function_call", "id": "ctc_" + fingerprint("call_patch"), "call_id": "call_read", "name": "read", "arguments": "{}"},
	}}}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	body := bridge.Stream(io.NopCloser(strings.NewReader("data: " + string(raw) + "\n\n")))
	defer func() { _ = body.Close() }()
	result, err := io.ReadAll(body)
	if err != nil || !strings.Contains(string(result), "basispoints_protocol_error") || strings.Contains(string(result), "event: response.completed") || strings.Contains(string(result), "event: response.output_item.added") {
		t.Fatalf("colliding rewritten IDs must fail before any callable item is emitted: %s; read error=%v", result, err)
	}
}

func TestFollowupProtocolNestedSourceResourceDoesNotEscapeScope(t *testing.T) {
	for _, keyword := range []string{"anyOf", "oneOf"} {
		t.Run(keyword, func(t *testing.T) {
			schema := object{"type": "object", "properties": object{"code": object{
				"$id": "nested-source.json", "$defs": object{"source": object{"type": "number"}},
				keyword: []any{object{"$ref": "#/$defs/source"}},
			}}, "$defs": object{"source": object{"type": "string"}}}
			if got := functionCodeTransportField(schema); got != "" {
				t.Fatalf("nested resource number contract incorrectly resolved against outer string schema: %q", got)
			}
		})
	}
}

func TestFollowupProtocolExplicitStringResourceRemainsSupported(t *testing.T) {
	for _, field := range []object{
		{"$id": "nested-source.json", "type": "string"},
		{"$id": "nested-source.json", "anyOf": []any{object{"type": "null"}, object{"type": "string"}}},
	} {
		schema := object{"type": "object", "properties": object{"code": field}}
		if got := functionCodeTransportField(schema); got != "code" {
			t.Fatalf("explicit string source contract changed: %q", got)
		}
	}
}
