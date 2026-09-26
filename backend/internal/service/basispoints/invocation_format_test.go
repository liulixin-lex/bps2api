package basispoints

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestFormattedSingleInvocationPreservesArgumentsAndReplay(t *testing.T) {
	const invocation = `await functions.shell({"command":"echo 中文","number":9007199254740993});`
	quoted, err := json.Marshal(invocation)
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{
		"```javascript\n" + invocation + "\n```",
		"```js\r\n" + invocation + "\r\n```",
		"```\n" + invocation + "\n```",
		string(quoted),
		"```json\n" + string(quoted) + "\n```",
	} {
		source := testSource()
		source["tools"] = []any{object{"type": "namespace", "name": "functions", "tools": []any{object{"type": "function", "name": "shell"}}}}
		cache := new(ReplayCache)
		_, bridge := mustPrepare(t, source, "formatted-invocation", cache)
		native := nativeCall(object{})
		native["arguments"] = object{"code": code}
		call, err := bridge.translateCall(native)
		if err != nil {
			t.Fatalf("wrapper rejected: %v", err)
		}
		var args object
		if err := decode([]byte(text(call["arguments"])), &args); err != nil {
			t.Fatal(err)
		}
		if call["name"] != "shell" || call["namespace"] != "functions" || args["number"] != json.Number("9007199254740993") || args["command"] != "echo 中文" {
			t.Fatalf("wrapper changed arguments: %#v", call)
		}
		if !reflect.DeepEqual(cache.get(bridge.scope, text(call["call_id"])), native) {
			t.Fatal("replay must preserve original native call")
		}
	}
}

func TestFormattedInvocationStillRejectsProgramsAndBatches(t *testing.T) {
	catalog := map[string]tool{"shell": {Name: "shell", Kind: "function"}}
	for _, code := range []string{
		"```js\nshell({}); shell({});\n```",
		"```js\nshell({});\n``` trailing",
		"```js\nconst args = {}; shell(args);\n```",
		"```js\nshell({\"command\": other()});\n```",
		"```js\nunknown({});\n```",
		"```python\nshell({});\n```",
		`"shell({}); shell({});"`,
	} {
		if result, ok := recoverTransportEnvelope(code, catalog); ok {
			t.Fatalf("unsafe formatted invocation accepted: %#v", result)
		}
	}
}
