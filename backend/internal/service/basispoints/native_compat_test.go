package basispoints

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNativeCompatCustomInputWrappers(t *testing.T) {
	const source = "text(\"native \\n \\u0041 世界\");"
	for _, direct := range []bool{false, true} {
		t.Run(map[bool]string{false: "envelope", true: "direct"}[direct], func(t *testing.T) {
			raw, err := json.Marshal(object{"model": "gpt-5.6-sol", "input": "test", "tools": []any{object{"type": "custom", "name": "exec"}}})
			require.NoError(t, err)
			_, b, err := Prepare(raw, "native-compat", nil)
			require.NoError(t, err)
			for _, field := range []string{"input", "arguments", "args"} {
				envelope := object{"name": "exec", field: object{"input": source}}
				encoded, err := json.Marshal(envelope)
				require.NoError(t, err)
				native := object{"type": "function_call", "name": "run_officejs", "id": "fc_test", "call_id": "call_test", "arguments": object{"code": string(encoded)}}
				if direct {
					native["name"] = "exec"
					arguments, err := json.Marshal(object{"input": source})
					require.NoError(t, err)
					native["arguments"] = string(arguments)
				}
				call, err := b.translateCall(native)
				require.NoError(t, err, field)
				require.Equal(t, "custom_tool_call", call["type"])
				require.Equal(t, source, call["input"])
				require.Equal(t, "exec", call["name"])
			}
		})
	}
}

func TestNativeCompatCustomWrappersRejectAmbiguity(t *testing.T) {
	for _, input := range []any{
		object{"input": "one", "extra": "two"}, object{"code": "ambiguous"},
		object{"input": object{"input": "too deep"}}, object{"input": 7}, []any{"one"},
	} {
		b := &Bridge{tools: map[string]tool{"exec": {Name: "exec", Kind: "custom"}}}
		_, err := b.finishClientToolCall(object{"call_id": "call_test"}, b.tools["exec"], object{"name": "exec", "input": input}, false)
		require.Error(t, err)
	}
	const rawJSONSource = "{\"input\":\"leave this raw text alone\"}"
	b := &Bridge{tools: map[string]tool{"exec": {Name: "exec", Kind: "custom"}}}
	call, err := b.finishClientToolCall(object{"call_id": "call_test"}, b.tools["exec"], object{"name": "exec", "input": rawJSONSource}, false)
	require.NoError(t, err)
	require.Equal(t, rawJSONSource, call["input"])
}

func TestNativeCompatEmptyCatalogInstructions(t *testing.T) {
	for _, none := range []bool{false, true} {
		body := object{"model": "gpt-5.6-sol", "input": "test"}
		if none {
			body["tool_choice"] = "none"
			body["tools"] = []any{object{"type": "function", "name": "declared_but_disabled"}}
		}
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		wire, _, err := Prepare(raw, "empty-catalog", nil)
		require.NoError(t, err)
		require.Contains(t, string(wire), "No client tools were declared. Return assistant text only.")
		require.Contains(t, string(wire), "Do not call run_officejs, skills")
	}
}
