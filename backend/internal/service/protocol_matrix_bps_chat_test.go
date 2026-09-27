package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestProtocolMatrixBPSChatIngress(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, kind := range []string{"text", "text_delta", "function", "incomplete", "failed"} {
			t.Run(fmt.Sprintf("%s/stream=%t", kind, stream), func(t *testing.T) {
				args := "{\"cell\":\"A1\",\"value\":9007199254740993}"
				output := []any{map[string]any{"type": "message", "id": "msg_chat", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "BPS_CHAT_OK"}}}}
				terminal := "response.completed"
				status := "completed"
				if kind == "function" {
					envelope, _ := json.Marshal(map[string]any{"name": "write_range", "arguments": json.RawMessage(args)})
					outer, _ := json.Marshal(map[string]any{"code": string(envelope), "summary": "Apply declared client write"})
					output = []any{map[string]any{"type": "function_call", "id": "fc_chat", "call_id": "call_chat", "name": "run_officejs", "arguments": string(outer)}}
				}
				if kind == "incomplete" {
					terminal = "response.incomplete"
					status = "incomplete"
				}
				if kind == "failed" {
					terminal = "response.failed"
					status = "failed"
					output = nil
				}
				response := map[string]any{"id": "resp_bps_chat", "status": status, "model": "gpt-5.6-sol", "output": output, "usage": map[string]any{"input_tokens": 10, "output_tokens": 2}}
				if kind == "incomplete" {
					response["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
				}
				if kind == "failed" {
					response["error"] = map[string]any{"code": "upstream_unavailable", "message": "Provider unavailable"}
				}
				encoded, _ := json.Marshal(map[string]any{"type": terminal, "response": response})
				wire := "data: " + string(encoded) + "\n\n"
				if kind == "text_delta" {
					wire = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"BPS_CHAT_OK\"}\n\n" + wire
				}
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}}
				svc := openAIClientToolsTestService(upstream)
				body := []byte(fmt.Sprintf("{\"model\":\"gpt-5.6-sol\",\"stream\":%t,\"messages\":[{\"role\":\"developer\",\"content\":\"Keep the exact values\"},{\"role\":\"user\",\"content\":[{\"type\":\"image_url\",\"image_url\":{\"url\":\"https://images.example/screen.png\",\"detail\":\"original\"}}]}],\"tools\":[{\"type\":\"function\",\"function\":{\"name\":\"write_range\",\"parameters\":{\"type\":\"object\"}}}]}", stream))
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
				result, err := svc.ForwardAsChatCompletions(context.Background(), c, excelAccount(), body, "", "")
				require.NotNil(t, upstream.lastReq)
				require.Equal(t, "bps.openai.com", upstream.lastReq.URL.Host)
				require.Contains(t, string(upstream.lastBody), "\"detail\":\"original\"")
				require.Contains(t, string(upstream.lastBody), "Keep the exact values")
				require.NotNil(t, result)
				require.Equal(t, "/basispoints/api/responses", result.UpstreamEndpoint)
				require.Equal(t, 10, result.Usage.InputTokens)
				if kind == "failed" || kind == "incomplete" {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				content := rec.Body.String()
				require.NotContains(t, content, "run_officejs")
				require.NotContains(t, content, "codex2api.function_code")
				if stream {
					require.Contains(t, content, "data: [DONE]")
					require.Equal(t, 1, strings.Count(content, "data: [DONE]"))
					require.NotContains(t, content, "event: response.")
					if kind == "failed" {
						require.Contains(t, content, "\"error\"")
						require.NotContains(t, content, "\"finish_reason\":\"stop\"")
					} else {
						require.Contains(t, content, "chat.completion.chunk")
					}
				} else {
					if kind == "failed" {
						require.Equal(t, 502, rec.Code)
						require.True(t, gjson.Get(content, "error").Exists())
					} else {
						require.Equal(t, "chat.completion", gjson.Get(content, "object").String())
					}
				}
				if kind == "function" {
					require.Contains(t, content, "write_range")
					require.Contains(t, content, "9007199254740993")
					require.Contains(t, content, "tool_calls")
				}
				if kind == "incomplete" {
					require.Contains(t, content, "\"finish_reason\":\"length\"")
				}
				if kind == "text" || kind == "text_delta" {
					require.Contains(t, content, "BPS_CHAT_OK")
					require.Equal(t, 1, strings.Count(content, "BPS_CHAT_OK"))
				}
			})
		}
	}
}
