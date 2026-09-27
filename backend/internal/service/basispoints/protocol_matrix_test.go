package basispoints

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Official Codex view_image emits application/octet-stream, not image/png.
func TestProtocolMatrixCodexOctetStreamImages(t *testing.T) {
	var jpg, gifData bytes.Buffer
	require.NoError(t, jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 2, 3)), nil))
	require.NoError(t, gif.Encode(&gifData, image.NewRGBA(image.Rect(0, 0, 2, 3)), nil))
	webp, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	require.NoError(t, err)
	for mime, data := range map[string][]byte{"image/png": relayTestPNG(t), "image/jpeg": jpg.Bytes(), "image/gif": gifData.Bytes(), "image/webp": webp} {
		for _, declared := range []string{mime, "application/octet-stream"} {
			t.Run(mime+"/"+declared, func(t *testing.T) {
				decoded, contentType, err := decodeTestRelayImage(t, "data:"+declared+";base64,"+base64.StdEncoding.EncodeToString(data))
				require.NoError(t, err)
				require.Equal(t, mime, contentType)
				require.Equal(t, data, decoded)
			})
		}
	}
	for _, uri := range []string{"data:application/octet-stream;base64,bm90LWFuLWltYWdl", "data:application/octet-stream;base64,!", "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(relayTestPNG(t))} {
		_, _, err := decodeTestRelayImage(t, uri)
		require.Error(t, err)
	}
}

func TestProtocolMatrixImageHistoryCapacity(t *testing.T) {
	for _, count := range []int{2, 25} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			r, err := newTestImageRelay(t, "https://images.example")
			require.NoError(t, err)
			data := relayTestPNG(t)
			if count == 2 {
				data = append(data, make([]byte, (17<<20)-len(data))...)
			}
			uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
			parts := make([]any, count)
			for i := range parts {
				parts[i] = object{"type": "input_image", "image_url": uri, "detail": "original"}
			}
			raw, err := json.Marshal(object{"model": "gpt-5.6-sol", "input": []any{object{"role": "user", "content": parts}}})
			require.NoError(t, err)
			result, err := r.Rewrite(raw, "test")
			require.NoError(t, err)
			require.Len(t, r.entries, 1)
			require.Equal(t, len(data), r.bytes)
			require.Zero(t, r.reservedEntries)
			require.Zero(t, r.reservedBytes)
			var source object
			require.NoError(t, decode(result, &source))
			content := source["input"].([]any)[0].(object)["content"].([]any)
			require.Len(t, content, count)
			first := content[0].(object)["image_url"]
			for _, p := range content {
				require.Equal(t, first, p.(object)["image_url"])
				require.Equal(t, "original", p.(object)["detail"])
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, first.(string), nil))
			require.Equal(t, 200, w.Code)
			require.Equal(t, data, w.Body.Bytes())
		})
	}
}

func TestProtocolMatrixOfficeReferencedSource(t *testing.T) {
	for _, field := range []string{"code", "cmd", "command", "script", "input"} {
		for _, ref := range []string{"#/$defs/source", "#/definitions/source", "#/$defs/a~1b~0c"} {
			t.Run(field+ref, func(t *testing.T) {
				schema := object{"type": "object", "properties": object{field: object{"$ref": ref}}, "$defs": object{"source": object{"type": []any{"string", "null"}}, "a/b~c": object{"type": "string"}}, "definitions": object{"source": object{"anyOf": []any{object{"type": "null"}, object{"type": "string"}}}}}
				source := testSource()
				source["tools"] = []any{object{"type": "function", "name": "office_apply", "parameters": schema}}
				_, b := mustPrepare(t, source, "office", nil)
				code := "const r = ctx.workbook.worksheets.getItem(\"预算\").getRange(\"A1:B2\");\r\nr.values=[[1,\"=A1*2\"],[null,false]]; await ctx.sync();\n"
				metadata := object{"sheetId": "预算", "ranges": []any{"A1:B2", "D4"}, "cellStyles": object{"numberFormat": "#,##0.00;[Red](#,##0.00)", "fontWeight": "bold"}, "enabled": false, "note": nil, "serial": json.Number("9007199254740993")}
				encoded, err := json.Marshal(metadata)
				require.NoError(t, err)
				call, err := b.translateCall(functionCodeTestNative(t, "office_apply", code, string(encoded)))
				require.NoError(t, err)
				args := functionCodeTestArguments(t, call)
				metadata[field] = code
				require.Equal(t, metadata, args)
				replay, err := b.rebuildNativeHistoryCall(call)
				require.NoError(t, err)
				again, err := b.translateCall(replay)
				require.NoError(t, err)
				require.Equal(t, args, functionCodeTestArguments(t, again))
			})
		}
	}
	for _, field := range []object{{"$ref": "https://example.invalid/schema"}, {"$ref": "file:///private"}, {"$ref": "#/$defs/cycle"}, {"$ref": "#/$defs/missing"}, {"$ref": "#/$defs/number"}} {
		schema := object{"type": "object", "properties": object{"code": field}, "$defs": object{"cycle": object{"$ref": "#/$defs/cycle"}, "number": object{"type": "number"}}}
		require.Empty(t, functionCodeTransportField(schema))
	}
}

func TestProtocolMatrixOfficeJSONArguments(t *testing.T) {
	source := testSource()
	source["tools"] = []any{object{"type": "function", "name": "write_range", "parameters": object{"type": "object"}}}
	_, b := mustPrepare(t, source, "office", nil)
	args := object{"sheetId": "预算", "writes": []any{object{"cell": "A1", "value": json.Number("9007199254740993"), "note": nil}, object{"cell": "B1", "formula": "=IF(A1>0,\"中文\",\"\")", "cellStyles": object{"fontWeight": "bold", "borders": []any{object{"sides": []any{"bottom"}, "style": "double", "weight": "thin"}}}}}, "includeStyles": false}
	code, err := json.Marshal(object{"name": "write_range", "arguments": args})
	require.NoError(t, err)
	call, err := b.translateCall(object{"type": "function_call", "name": "run_officejs", "call_id": "office_call", "arguments": object{"code": string(code)}})
	require.NoError(t, err)
	require.Equal(t, args, functionCodeTestArguments(t, call))
}

func TestProtocolMatrixNativeCapabilityRouting(t *testing.T) {
	cases := map[string]string{
		"{\"tools\":[{\"type\":\"function\",\"name\":\"work\",\"async\":true}]}":                                                          "async_tool",
		"{\"tools\":[{\"type\":\"shell\"}]}":                                                                                              "shell",
		"{\"input\":[{\"type\":\"mcp_tool_call_output\",\"output\":[]}]}":                                                                 "native_history",
		"{\"tools\":[{\"type\":\"web_search\"}]}":                                                                                         "web_search",
		"{\"tools\":[{\"type\":\"file_search\",\"vector_store_ids\":[\"vs_x\"]}]}":                                                        "file_search",
		"{\"tools\":[{\"type\":\"code_interpreter\"}]}":                                                                                   "code_interpreter",
		"{\"tools\":[{\"type\":\"tool_search\"}]}":                                                                                        "tool_search",
		"{\"input\":[{\"type\":\"tool_search_output\",\"tools\":[]}]}":                                                                    "native_history",
		"{\"input\":[{\"type\":\"configuration_update\",\"reasoning\":{\"effort\":\"high\"}}]}":                                           "native_history",
		"{\"reasoning\":{\"mode\":\"pro\"}}":                                                                                              "reasoning_mode",
		"{\"previous_response_id\":\"resp_123\"}":                                                                                         "previous_response_id",
		"{\"input\":[{\"role\":\"user\",\"content\":[{\"type\":\"input_image\",\"file_id\":\"file_x\"}]}]}":                               "image_file_id",
		"{\"input\":[{\"role\":\"user\",\"content\":[{\"type\":\"input_audio\",\"audio_url\":\"data:audio/wav;base64,AAAA\"}]}]}":         "native_media",
		"{\"input\":[{\"type\":\"function_call_output\",\"output\":[{\"type\":\"input_file\",\"file_id\":\"file_x\"}]}]}":                 "native_media",
		"{\"input\":[{\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"input_audio and file_id are literal text\"}]}]}": "",
		"{\"input\":[{\"role\":\"user\",\"content\":[{\"type\":\"input_image\",\"detail\":\"original\",\"image_url\":\"data:application/octet-stream;base64,AAAA\"}]}]}": "",
	}
	for body, want := range cases {
		t.Run(strings.ReplaceAll(want, "/", "_"), func(t *testing.T) {
			before := []byte(body)
			require.Equal(t, want, NativeFallbackReason(before))
			require.Equal(t, body, string(before))
		})
	}
}
