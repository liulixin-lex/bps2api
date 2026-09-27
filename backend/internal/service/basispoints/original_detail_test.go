package basispoints

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// Original image detail must survive messages, tool history and inline relay.
func TestOriginalDetailCompatibility(t *testing.T) {
	for _, kind := range []string{"message", "function_call_output", "custom_tool_call_output"} {
		for _, alias := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/responses", true: "/chat-alias"}[alias], func(t *testing.T) {
				image := object{"type": "input_image", "image_url": "https://images.example/screenshot.png?sig=a%2Fb", "detail": "original"}
				if alias {
					image = object{"type": "image_url", "image_url": object{"url": "https://images.example/screenshot.png?sig=a%2Fb", "detail": "original"}}
				}
				parts := []any{object{"type": "input_text", "text": "preserve screenshot coordinates"}, image}
				item := object{"type": "message", "role": "user", "content": parts}
				input := []any{item}
				if kind != "message" {
					callType := "function_call"
					call := object{"type": callType, "call_id": "call_view", "name": "view_image", "arguments": "{}"}
					if kind == "custom_tool_call_output" {
						call["type"] = "custom_tool_call"
						delete(call, "arguments")
						call["input"] = "view"
					}
					item = object{"type": kind, "call_id": "call_view", "output": parts}
					input = []any{call, item}
				}
				source := testSource()
				source["input"] = input
				toolKind := "function"
				if kind == "custom_tool_call_output" {
					toolKind = "custom"
				}
				source["tools"] = []any{object{"type": toolKind, "name": "view_image", "parameters": object{"type": "object"}}}
				wire, _ := mustPrepare(t, source, "scope", nil)
				encoded, _ := json.Marshal(wire)
				var found []object
				var visit func(any)
				visit = func(v any) {
					switch x := v.(type) {
					case map[string]any:
						if x["type"] == "input_image" {
							found = append(found, x)
						}
						for _, c := range x {
							visit(c)
						}
					case []any:
						for _, c := range x {
							visit(c)
						}
					}
				}
				var parsed any
				if err := json.Unmarshal(encoded, &parsed); err != nil {
					t.Fatal(err)
				}
				visit(parsed)
				expected := object{"type": "input_image", "image_url": "https://images.example/screenshot.png?sig=a%2Fb", "detail": "original"}
				if len(found) != 1 || !reflect.DeepEqual(found[0], expected) {
					t.Fatalf("image detail or URL changed: %#v", found)
				}
			})
		}
	}
}

func TestOriginalDetailInlineRelayPreservesBytes(t *testing.T) {
	data := relayTestPNG(t)
	relay, err := newTestImageRelay(t, "https://images.example")
	if err != nil {
		t.Fatal(err)
	}
	source := testSource()
	source["input"] = []any{object{"role": "user", "content": []any{object{"type": "input_image", "image_url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(data), "detail": "original"}}}}
	raw, _ := json.Marshal(source)
	out, err := relay.Rewrite(raw, "scope")
	if err != nil {
		t.Fatal(err)
	}
	prepared, _, err := Prepare(out, "scope", nil)
	if err != nil {
		t.Fatal(err)
	}
	var wire object
	if err = decode(prepared, &wire); err != nil {
		t.Fatal(err)
	}
	var image object
	for _, value := range mustTestValue[[]any](t, wire["input"]) {
		item := mustTestValue[object](t, value)
		if content, ok := item["content"].([]any); ok {
			for _, v := range content {
				part, ok := v.(map[string]any)
				if ok && part["type"] == "input_image" {
					image = part
				}
			}
		}
	}
	if image == nil || image["detail"] != "original" {
		t.Fatal("original detail lost")
	}
	response := httptest.NewRecorder()
	relay.ServeHTTP(response, httptest.NewRequest(http.MethodGet, text(image["image_url"]), nil))
	if response.Code != 200 || !bytes.Equal(response.Body.Bytes(), data) {
		t.Fatal("relay altered original bytes")
	}
}
