package basispoints

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func choiceSource(choice any) object {
	source := testSource()
	source["tool_choice"] = choice
	source["tools"] = []any{
		object{"type": "function", "name": "read", "parameters": object{"type": "object"}},
		object{"type": "custom", "name": "patch"},
		object{"type": "namespace", "name": "client", "tools": []any{
			object{"type": "function", "name": "read", "parameters": object{"type": "object"}},
			object{"type": "custom", "name": "patch"},
		}},
	}
	return source
}

func choiceResponse(names ...string) object {
	output := make([]any, 0, len(names))
	for _, name := range names {
		envelope := object{"name": name, "arguments": object{}}
		if strings.HasSuffix(name, "patch") {
			envelope = object{"name": name, "input": "*** Begin Patch\n*** End Patch"}
		}
		call := nativeCall(envelope)
		call["id"], call["call_id"] = "fc_"+name, "call_"+name
		output = append(output, call)
	}
	return object{"id": "resp_choice", "status": "completed", "output": output}
}

func TestToolChoicePreservesSelectionAndRequiredSemantics(t *testing.T) {
	cases := []struct {
		name     string
		choice   any
		allowed  []string
		denied   []string
		required bool
		single   bool
	}{
		{"auto", "auto", []string{"read", "patch", "client.read"}, nil, false, false},
		{"required", "required", []string{"read", "patch", "client.read"}, nil, true, false},
		{"function", object{"type": "function", "name": "read"}, []string{"read"}, []string{"patch", "client.read"}, true, true},
		{"custom", object{"type": "custom", "name": "patch"}, []string{"patch"}, []string{"read", "client.patch"}, true, true},
		{"qualified", object{"type": "function", "name": "client.read"}, []string{"client.read"}, []string{"read"}, true, true},
		{"namespace_field", object{"type": "custom", "name": "patch", "namespace": "client"}, []string{"client.patch"}, []string{"patch", "client.read"}, true, true},
		{"namespace", object{"type": "namespace", "name": "client"}, []string{"client.read", "client.patch"}, []string{"read", "patch"}, true, false},
		{"allowed_auto", object{"type": "allowed_tools", "mode": "auto", "tools": []any{object{"type": "function", "name": "read"}, object{"type": "custom", "name": "patch", "namespace": "client"}}}, []string{"read", "client.patch"}, []string{"patch", "client.read"}, false, false},
		{"allowed_required", object{"type": "allowed_tools", "mode": "required", "tools": []any{object{"type": "function", "name": "read"}}}, []string{"read"}, []string{"patch", "client.read"}, true, false},
		{"none", "none", nil, []string{"read", "patch"}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cache := new(ReplayCache)
			body, b := mustPrepare(t, choiceSource(tc.choice), "choice", cache)
			if _, exists := body["tool_choice"]; exists {
				t.Fatal("client tool_choice must not leak to native BPS wire")
			}
			for _, name := range tc.allowed {
				response := choiceResponse(name)
				if err := b.translateResponse(response); err != nil {
					t.Errorf("allowed %s: %v", name, err)
				}
			}
			for _, name := range tc.denied {
				response := choiceResponse(name)
				before, _ := json.Marshal(response)
				if err := b.translateResponse(response); err == nil {
					t.Errorf("forbidden tool %s was dispatched", name)
				}
				after, _ := json.Marshal(response)
				if string(before) != string(after) || cache.get("choice", "call_"+name) != nil {
					t.Fatal("invalid batch mutated output or committed replay")
				}
			}
			if err := b.translateResponse(choiceResponse()); (err != nil) != tc.required {
				t.Errorf("empty completion required=%v: %v", tc.required, err)
			}
			if tc.single {
				response := choiceResponse(tc.allowed[0])
				first := response["output"].([]any)[0].(object)
				second := object{}
				for key, value := range first {
					second[key] = value
				}
				second["id"], second["call_id"] = "fc_second", "call_second"
				response["output"] = []any{first, second}
				if err := b.translateResponse(response); err == nil {
					t.Fatal("forced tool accepted multiple calls")
				}
			}
		})
	}
}

func TestToolChoiceRejectsInvalidOrUnavailableSelections(t *testing.T) {
	for _, choice := range []any{
		"unknown", true, []any{}, object{},
		object{"type": "function", "name": "missing"},
		object{"type": "custom", "name": "read"},
		object{"type": "function", "name": "read", "namespace": 42},
		object{"type": "function", "name": " read"},
		object{"type": "function", "name": "read", "unexpected_constraint": true},
		object{"type": "namespace", "name": "missing"},
		object{"type": "web_search"},
		object{"type": "allowed_tools", "mode": "required", "tools": []any{}},
		object{"type": "allowed_tools", "mode": "none", "tools": []any{object{"type": "function", "name": "read"}}},
		object{"type": "allowed_tools", "mode": "auto", "tools": []any{object{"type": "web_search"}}},
	} {
		raw, _ := json.Marshal(choiceSource(choice))
		if _, _, err := Prepare(raw, "", nil, PrepareOptions{OmitUnsupportedTools: true}); err == nil || !strings.Contains(err.Error(), "tool_choice") {
			t.Errorf("choice %v: expected explicit tool_choice error, got %v", choice, err)
		}
	}
	for _, tools := range []any{nil, []any{object{"type": "web_search"}}} {
		source := choiceSource("required")
		source["tools"] = tools
		raw, _ := json.Marshal(source)
		if _, _, err := Prepare(raw, "", nil, PrepareOptions{OmitUnsupportedTools: true}); err == nil {
			t.Fatal("required accepted no callable client tools")
		}
	}
}

func TestToolChoiceRequiresToolInCurrentResponseAndRetainsOtherHistory(t *testing.T) {
	source := choiceSource(object{"type": "function", "name": "read"})
	source["input"] = []any{
		object{"type": "custom_tool_call", "name": "patch", "call_id": "old_patch", "input": "old source"},
		object{"type": "custom_tool_call_output", "call_id": "old_patch", "output": "done"},
	}
	body, b := mustPrepare(t, source, "", nil)
	encoded, _ := json.Marshal(body)
	if !strings.Contains(string(encoded), "old source") {
		t.Fatal("non-selected tool history was lost")
	}
	if err := b.translateResponse(choiceResponse()); err == nil {
		t.Fatal("history satisfied current required call")
	}
	if err := b.translateResponse(choiceResponse("read")); err != nil {
		t.Fatal(err)
	}
}

func TestToolChoiceStreamingRejectsTextOnlyAndWrongTool(t *testing.T) {
	for _, response := range []object{
		{"status": "completed", "output": []any{object{"type": "message", "role": "assistant", "content": []any{object{"type": "output_text", "text": "done"}}}}},
		choiceResponse("patch"),
	} {
		_, b := mustPrepare(t, choiceSource(object{"type": "function", "name": "read"}), "", new(ReplayCache))
		stream := b.Stream(io.NopCloser(strings.NewReader(sse(object{"type": "response.completed", "response": response}))))
		got, err := io.ReadAll(stream)
		_ = stream.Close()
		if err != nil || !strings.Contains(string(got), "response.failed") || strings.Contains(string(got), "response.completed") || strings.Contains(string(got), "response.function_call_arguments") || strings.Contains(string(got), "response.custom_tool_call_input") {
			t.Fatalf("invalid forced completion escaped validation: %s, %v", got, err)
		}
	}
}

func TestToolChoiceBatchIsAtomicAndCannotRepairToAnotherOperation(t *testing.T) {
	cache := new(ReplayCache)
	_, b := mustPrepare(t, choiceSource(object{"type": "allowed_tools", "mode": "required", "tools": []any{object{"type": "function", "name": "read"}}}), "atomic", cache)
	response := choiceResponse("read", "patch")
	repairs := 0
	err := b.translateCompleted(context.Background(), response, func(context.Context, map[string]any, error) (map[string]any, error) {
		repairs++
		return choiceResponse("read"), nil
	})
	if err == nil || repairs != 0 || cache.get("atomic", "call_read") != nil || cache.get("atomic", "call_patch") != nil {
		t.Fatalf("selection violation changed operations or committed partial batch: repairs=%d error=%v", repairs, err)
	}
	first := response["output"].([]any)[0].(object)
	if first["name"] != "run_officejs" {
		t.Fatal("rejected batch changed original output")
	}
}

func TestToolChoiceAllowsMultipleRequiredCallsAndHonorsParallelLimit(t *testing.T) {
	source := choiceSource("required")
	_, b := mustPrepare(t, source, "", nil)
	if err := b.translateResponse(choiceResponse("read", "patch")); err != nil {
		t.Fatal(err)
	}
	source["parallel_tool_calls"] = false
	_, b = mustPrepare(t, source, "", nil)
	if err := b.translateResponse(choiceResponse("read", "patch")); err == nil {
		t.Fatal("required bypassed parallel_tool_calls=false")
	}
}

func TestToolChoiceIncludesDiscoveredToolsAndChecksDirectCalls(t *testing.T) {
	source := testSource()
	source["tool_choice"] = object{"type": "function", "name": "read", "namespace": "client"}
	source["input"] = []any{
		object{"type": "additional_tools", "tools": choiceSource(nil)["tools"]},
		message("user", "Read the data"),
	}
	_, b := mustPrepare(t, source, "", nil)
	for _, namespace := range []string{"client", ""} {
		response := object{"output": []any{object{"type": "function_call", "id": "fc_direct", "call_id": "call_direct", "name": "read", "namespace": namespace, "arguments": `{}`}}}
		if err := b.translateResponse(response); (err == nil) != (namespace == "client") {
			t.Fatalf("direct call namespace %q violated selection: %v", namespace, err)
		}
	}
}
