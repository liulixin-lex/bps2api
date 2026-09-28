package basispoints

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestContentErrorsIdentifyPathAndKnownType(t *testing.T) {
	for _, field := range []string{"content", "function_call_output", "custom_tool_call_output"} {
		for _, kind := range []string{"input_file", "input_audio", "image_url", "image", "encrypted_content"} {
			t.Run(field+"/"+kind, func(t *testing.T) {
				source := testSource()
				parts := []any{object{"type": "input_text", "text": "private-text"}, object{"type": kind, "data": "private-payload", "url": "https://private-url.example/image"}}
				input := []any{message("user", "inspect")}
				path := "input[1].content[1]"
				if field != "content" {
					call := object{"type": "function_call", "call_id": "call_diagnostic", "name": "inspect", "arguments": `{}`}
					if field == "custom_tool_call_output" {
						call = object{"type": "custom_tool_call", "call_id": "call_diagnostic", "name": "inspect", "input": "diagnostic"}
					}
					input = append(input, call, object{"type": field, "call_id": "call_diagnostic", "output": parts})
					path = "input[2].output[1]"
				} else {
					input = append(input, object{"role": "user", "content": parts})
				}
				source["input"] = input
				raw, err := json.Marshal(source)
				if err != nil {
					t.Fatal(err)
				}
				_, _, err = Prepare(raw, "scope", nil)
				if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "type="+kind) || strings.Contains(err.Error(), "private-") {
					t.Fatalf("content error lacks safe structural diagnostics: %v", err)
				}
			})
		}
	}
}

func TestContentDiagnosticsDoNotEchoUntrustedTypeValues(t *testing.T) {
	for _, tc := range []struct {
		part any
		kind string
	}{
		{nil, "non_object"},
		{"private-payload", "non_object"},
		{object{}, "missing"},
		{object{"type": "private-type"}, "unknown"},
		{object{"type": "input_file\nprivate-log-injection"}, "unknown"},
		{object{"type": object{"private-key": "private-value"}}, "non_string"},
	} {
		source := testSource()
		source["input"] = []any{object{"role": "user", "content": []any{tc.part}}}
		raw, err := json.Marshal(source)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = Prepare(raw, "scope", nil)
		if err == nil || !strings.Contains(err.Error(), "input[0].content[0]") || !strings.Contains(err.Error(), "type="+tc.kind) || strings.Contains(err.Error(), "private-") {
			t.Fatalf("missing path or untrusted data echoed: %v", err)
		}
	}
}

func TestImageValidationErrorIncludesPathWithoutURL(t *testing.T) {
	source := testSource()
	source["input"] = []any{message("user", "diagnostic"), object{"role": "user", "content": []any{object{"type": "input_image", "image_url": "http://private-url.example/image?private-token"}}}}
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = Prepare(raw, "scope", nil)
	if err == nil || !strings.Contains(err.Error(), "path=input[1].content[0]") || !strings.Contains(err.Error(), "HTTPS") || strings.Contains(err.Error(), "private-") {
		t.Fatalf("image error lacks a safe path: %v", err)
	}
}

func TestUnknownContentLongHistoryReportsSafeStructure(t *testing.T) {
	// This is a synthetic regression at the reported position, not a captured
	// production payload. Its unknown type must remain unsupported.
	source := testSource()
	input := make([]any, 0, 62)
	for i := 0; i < 61; i++ {
		input = append(input, message("user", "history"))
	}
	input = append(input, object{"type": "agent_message", "author": "/root/worker", "recipient": "/root", "content": []any{
		object{"type": "input_text", "text": "Message Type: MESSAGE\nPayload:\n"},
		object{"type": "private-unsupported-type", "encrypted_content": "private-ciphertext", "content": object{"private-nested-key": "private-text"}, "private-key": "private-value"},
	}})
	source["input"] = input
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, omitEncrypted := range []bool{false, true} {
		candidate := raw
		if omitEncrypted {
			candidate, err = StripEncryptedContent(raw)
			if err != nil {
				t.Fatal(err)
			}
			if string(candidate) != string(raw) {
				t.Fatal("unknown content must not be guessed to be encrypted and omitted")
			}
		}
		body, _, err := Prepare(candidate, "scope", nil)
		var contentErr *ContentValidationError
		if !errors.As(err, &contentErr) || body != nil {
			t.Fatalf("unknown content must fail before forwarding: %v", err)
		}
		t.Logf("omit_encrypted=%t error=%s", omitEncrypted, err)
		for _, want := range []string{"path=input[61].content[1]", "type=unknown", "shape=object{type:string,content:object,encrypted_content:string,other_fields:1}"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("missing safe diagnostic %q: %v", want, err)
			}
		}
		if strings.Contains(err.Error(), "private-") {
			t.Fatal("diagnostic leaked type, content, or unknown field names")
		}
	}
}

func TestContentStructureDiagnosticsAreBoundedAndValueFree(t *testing.T) {
	for _, tc := range []struct {
		part any
		want string
	}{
		{nil, "null"},
		{"private-text", "string"},
		{true, "boolean"},
		{json.Number("9007199254740993"), "number"},
		{[]any{object{"private-key": "private-text"}}, "array"},
		{object{}, "object{}"},
		{object{"type": object{"private-key": "private-value"}, "text": []any{"private-text"}, "source": nil}, "object{type:object,text:array,source:null}"},
		{object{"type": strings.Repeat("private-type\n", 1<<16), "image_url": "https://private-url.example/?private-token", "detail": "private-detail", strings.Repeat("private-key\n", 1<<16): "private-value"}, "object{type:string,image_url:string,detail:string,other_fields:1}"},
		{object{"type": "provider_image", "source": object{"private-key": "private-value"}}, "object{type:string,source:object}"},
	} {
		b := &Bridge{}
		err := b.validateHistoryContent([]any{tc.part}, 0, "content")
		if err == nil || !strings.Contains(err.Error(), "shape="+tc.want) {
			t.Errorf("missing bounded structure %q: %v", tc.want, err)
			continue
		}
		if len(err.Error()) > 512 || strings.Contains(err.Error(), "private-") || strings.ContainsAny(err.Error(), "\n\r") {
			t.Fatal("diagnostic is not bounded and value-free")
		}
		if part, ok := tc.part.(object); ok && text(part["type"]) == "provider_image" && !strings.Contains(err.Error(), "type=provider_image") {
			t.Fatal("known provider_image alias was reported as unknown")
		}
	}
}
