package basispoints

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIncidentForcedClientToolChoiceStaysOnBPS(t *testing.T) {
	for _, choice := range []string{
		`"required"`, `{"type":"function","name":"exec"}`,
		`{"type":"custom","name":"patch"}`, `{"type":"allowed_tools","mode":"required","tools":[]}`,
	} {
		body := []byte(`{"model":"gpt-5.6-sol","tool_choice":` + choice + `,"input":"test"}`)
		if got := NativeFallbackReason(body); got != "" {
			t.Errorf("choice=%s unexpectedly requires native capability: %q", choice, got)
		}
	}
	for _, choice := range []string{`"auto"`, `"none"`, `null`} {
		if got := NativeFallbackReason([]byte(`{"tool_choice":` + choice + `}`)); got != "" {
			t.Errorf("compatible choice=%s unexpectedly bypassed BPS: %s", choice, got)
		}
	}
}

func TestIncidentCustomStringArgumentsPreserveExactInput(t *testing.T) {
	source := testSource()
	source["tools"] = []any{object{"type": "custom", "name": "patch"}}
	_, b := mustPrepare(t, source, "incident", new(ReplayCache))
	input := "*** Begin Patch\nquoted \"value\" \\path\t中文\r\n*** End Patch"
	for _, field := range []string{"input", "args", "arguments"} {
		envelope, _ := json.Marshal(object{"name": "patch", field: input})
		native := object{"type": "function_call", "name": "run_officejs", "call_id": "call_" + field, "arguments": object{"code": string(envelope)}}
		call, err := b.translateCall(native)
		if err != nil || call["input"] != input || call["type"] != "custom_tool_call" {
			t.Errorf("field=%s exact custom input was not preserved: %v", field, err)
		}
	}
	for _, invalid := range []object{
		// A sole string input is now losslessly normalized; additional data
		// must still be rejected rather than silently discarded.
		{"name": "patch", "arguments": object{"input": input, "extra": true}},
		{"name": "patch", "input": input, "arguments": input},
		{"name": "patch", "args": input, "arguments": input},
	} {
		envelope, _ := json.Marshal(invalid)
		if _, err := b.translateCall(object{"type": "function_call", "name": "run_officejs", "call_id": "bad", "arguments": object{"code": string(envelope)}}); err == nil {
			t.Fatal("accepted ambiguous or non-text custom input")
		}
	}
}

func TestIncidentRawCommandTransportRoundTrip(t *testing.T) {
	source := testSource()
	source["tools"] = []any{object{"type": "function", "name": "exec_command", "parameters": object{
		"type": "object", "properties": object{"cmd": object{"type": "string"}, "workdir": object{"type": "string"}},
	}}}
	body, b := mustPrepare(t, source, "incident", new(ReplayCache))
	bodyJSON, _ := json.Marshal(body)
	if !strings.Contains(string(bodyJSON), "codex2api.function_cmd/exec_command") {
		t.Fatal("shell command was not advertised with lossless raw transport")
	}
	input := "python3 - <<'PY'\nprint(\"C:\\\\work\")\nPY\n"
	call, err := b.translateCall(object{"type": "function_call", "name": "run_officejs", "call_id": "cmd", "arguments": object{
		"summary": "codex2api.function_code/exec_command", "code": input, "extended_summary": `{"workdir":"/tmp"}`,
	}})
	if err != nil {
		t.Fatal(err)
	}
	var args object
	if json.Unmarshal([]byte(text(call["arguments"])), &args) != nil || args["cmd"] != input || args["workdir"] != "/tmp" || args["code"] != nil {
		t.Fatal("raw command was changed or mapped to the wrong field")
	}
	replay, err := b.rebuildNativeHistoryCall(call)
	if err != nil {
		t.Fatal(err)
	}
	var outer object
	_ = json.Unmarshal([]byte(text(replay["arguments"])), &outer)
	if outer["code"] != input || outer["summary"] != "codex2api.function_cmd/exec_command" {
		t.Fatal("raw command history lost its exact transport")
	}
}
