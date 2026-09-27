package basispoints

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompatFunctionCodeMarkerForExactCustomTool(t *testing.T) {
	source := testSource()
	source["tools"] = []any{object{"type": "custom", "name": "functions.exec"}}
	_, bridge := mustPrepare(t, source, "scope", nil)
	raw := "const x = await tools.echo({text: \"quoted\"}); text(x);\n"
	call, err := bridge.translateCall(functionCodeTestNative(t, "functions.exec", raw, "{}"))
	if err != nil {
		t.Fatal(err)
	}
	if call["type"] != "custom_tool_call" || call["name"] != "functions.exec" || call["input"] != raw {
		t.Fatalf("transport changed: %#v", call)
	}
	if _, err := bridge.translateCall(functionCodeTestNative(t, "functions.exec", raw, `{"timeout":1}`)); err == nil {
		t.Fatal("must not silently discard metadata")
	}
}

func TestCompatNullableSourceField(t *testing.T) {
	source := testSource()
	source["tools"] = []any{object{"type": "function", "name": "shell", "parameters": object{"type": "object", "properties": object{"cmd": object{"type": []any{"string", "null"}}}}}}
	_, bridge := mustPrepare(t, source, "scope", nil)
	call, err := bridge.translateCall(functionCodeTestNative(t, "shell", "echo hello", "{}"))
	if err != nil {
		t.Fatal(err)
	}
	if functionCodeTestArguments(t, call)["cmd"] != "echo hello" {
		t.Fatal("source changed")
	}
}

func TestCompatDeclaredStringInputTransport(t *testing.T) {
	source := testSource()
	source["tools"] = []any{object{"type": "function", "name": "exec", "parameters": object{"type": "object", "properties": object{"input": object{"type": "string"}}, "required": []any{"input"}}}}
	wire, bridge := mustPrepare(t, source, "scope", nil)
	encoded, _ := json.Marshal(wire)
	if !strings.Contains(string(encoded), functionCodeTransportPrefix+"exec") {
		t.Fatal("catalog does not teach the declared transport")
	}
	raw := "const matches = ALL_TOOLS.filter(x => /GetTaskContext/.test(x.name)); text(matches);"
	call, err := bridge.translateCall(functionCodeTestNative(t, "exec", raw, "{}"))
	if err != nil {
		t.Fatal(err)
	}
	if call["type"] != "function_call" || functionCodeTestArguments(t, call)["input"] != raw {
		t.Fatal("declared string input changed")
	}
}

func TestCompatCustomHistoryUsesDeclaredRawTransport(t *testing.T) {
	source := testSource()
	source["tools"] = []any{object{"type": "custom", "name": "functions.exec"}}
	_, bridge := mustPrepare(t, source, "scope", nil)
	code := "const r = await tools.echo({text:'quote'}); text(r);"
	restored, err := bridge.rebuildNativeHistoryCall(object{"type": "custom_tool_call", "call_id": "call_restore", "name": "functions.exec", "input": code})
	if err != nil {
		t.Fatal(err)
	}
	var args object
	if err = json.Unmarshal([]byte(text(restored["arguments"])), &args); err != nil {
		t.Fatal(err)
	}
	if args["summary"] != customTransportPrefix+"functions.exec" || args["code"] != code {
		t.Fatal("custom history still teaches JSON/function transport for raw source")
	}
}

func TestCompatSafeContentAliases(t *testing.T) {
	for _, part := range []object{
		{"type": "reasoning_text", "text": "preserve exact context"},
		{"type": "summary_text", "text": "preserve exact context"},
		{"type": "image_url", "image_url": object{"url": "https://example.com/image.png", "detail": "high"}},
	} {
		t.Run(text(part["type"]), func(t *testing.T) {
			source := testSource()
			source["input"] = []any{object{"role": "user", "content": []any{part}}}
			raw, _ := json.Marshal(source)
			prepared, _, err := Prepare(raw, "scope", nil)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(prepared), "preserve exact context") && !strings.Contains(string(prepared), "https://example.com/image.png") {
				t.Fatal("content lost")
			}
		})
	}
}

func TestCompatSingleLiteralInvocation(t *testing.T) {
	source := testSource()
	source["tools"] = []any{object{"type": "function", "name": "shell", "parameters": object{"type": "object"}}, object{"type": "custom", "name": "exec"}}
	_, b := mustPrepare(t, source, "scope", nil)
	for _, code := range []string{
		"await shell({cmd:'echo hello', nested:{items:[true,false,null,9007199254740993,],},});",
		"shell({cmd: 'hello', cwd: '/tmp'})",
		"exec('text(await tools.echo({value:1}));')",
	} {
		envelope, ok := recoverTransportEnvelope(code, b.tools)
		if !ok {
			t.Fatalf("literal rejected: %q", code)
		}
		if _, err := b.finishClientToolCall(object{"call_id": "fixture"}, b.tools[text(envelope["name"])], envelope, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, code := range []string{
		"shell({cmd: run()})", "shell({cmd: secret})", "shell({...other})", "shell({get cmd(){return 'x'}})",
		"shell({cmd:'first',cmd:'second'})", "shell({cmd:1+2})", "shell({cmd:true.value})",
		"shell({cmd:'x'});shell({cmd:'y'})", "shell({cmd:'x'}) trailing", "missing({cmd:'x'})",
		"shell({__proto__:{cmd:'x'}})", "shell({cmd:NaN})", "shell({cmd:undefined})",
	} {
		if _, ok := recoverTransportEnvelope(code, b.tools); ok {
			t.Fatalf("expression or ambiguous call accepted: %q", code)
		}
	}
}

func TestCompatImageAliasesUseAdmissionAndRelay(t *testing.T) {
	data := relayTestPNG(t)
	relay, err := newTestImageRelay(t, "https://images.example")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"input_image", "image_url"} {
		source := testSource()
		source["input"] = []any{object{"role": "user", "content": []any{object{"type": kind, "image_url": object{"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(data), "detail": "high"}}}}}
		raw, _ := json.Marshal(source)
		if !RequestHasInlineImages(raw) {
			t.Fatal("image alias bypasses resource admission")
		}
		out, err := relay.Rewrite(raw, "scope")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = Prepare(out, "scope", nil); err != nil {
			t.Fatal(err)
		}
		var rewritten object
		if err := decode(out, &rewritten); err != nil {
			t.Fatal(err)
		}
		items := mustTestValue[[]any](t, rewritten["input"])
		item := mustTestValue[object](t, items[0])
		parts := mustTestValue[[]any](t, item["content"])
		part := mustTestValue[object](t, parts[0])
		if part["type"] != "input_image" || part["detail"] != "high" {
			t.Fatal("image semantics lost")
		}
		response := httptest.NewRecorder()
		relay.ServeHTTP(response, httptest.NewRequest(http.MethodGet, text(part["image_url"]), nil))
		if response.Code != 200 || !bytes.Equal(response.Body.Bytes(), data) {
			t.Fatal("relay changed image bytes")
		}
	}
}

func TestCompatFunctionCodeRejectsInvalidCustomMarkerNames(t *testing.T) {
	for _, name := range []string{"bad/name", "bad name", "bad\nname", ""} {
		b := &Bridge{tools: map[string]tool{name: {Name: name, Kind: "custom"}}}
		_, matched, err := b.functionCodeTransportEnvelope(object{"summary": functionCodeTransportPrefix + name, "code": "raw", "extended_summary": "{}"})
		if !matched || err == nil {
			t.Fatalf("invalid marker name accepted: %q", name)
		}
	}
}

func TestCompatLiteralPreservesEscapesAndLargeNumbers(t *testing.T) {
	catalog := map[string]tool{"shell": {Name: "shell", Kind: "function"}}
	code := "shell({cmd:'say \"hello\"\\npath\\\\next\\tend', n:9007199254740993,})"
	envelope, ok := recoverTransportEnvelope(code, catalog)
	if !ok {
		t.Fatal("literal rejected")
	}
	args := mustTestValue[object](t, envelope["arguments"])
	if args["cmd"] != "say \"hello\"\npath\\next\tend" || args["n"] != json.Number("9007199254740993") {
		t.Fatalf("literal changed: %#v", args)
	}
}
