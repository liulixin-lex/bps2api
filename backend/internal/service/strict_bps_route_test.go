package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Exercise the real forwarding entrypoints: an optional native capability
// must not override an administrator's channel choice.
func TestStrictBPSRouteSwitch(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		for _, chat := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("enabled=%t/chat=%t/stream=%t", enabled, chat, stream), func(t *testing.T) {
					wire := incidentBPSFrame("response.completed", map[string]any{"response": map[string]any{
						"id": "resp_route", "status": "completed", "model": "gpt-5.6-sol",
						"output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "route_ok"}}}},
					}})
					upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}}
					svc := openAIClientToolsTestService(upstream)
					account := excelAccount()
					account.Extra["openai_excel_bps"] = enabled
					body := []byte(fmt.Sprintf(`{"model":"gpt-5.6-sol","stream":%t,"input":"route test","text":{"verbosity":"high"}}`, stream))
					path := "/v1/responses"
					if chat {
						path = "/v1/chat/completions"
						body = []byte(fmt.Sprintf(`{"model":"gpt-5.6-sol","stream":%t,"messages":[{"role":"user","content":"route test"}],"reasoning_effort":"minimal"}`, stream))
					}
					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
					var err error
					if chat {
						_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
					} else {
						_, err = svc.Forward(context.Background(), c, account, body)
					}
					require.NoError(t, err)
					require.Len(t, upstream.requests, 1)
					if enabled {
						require.Equal(t, "https://bps.openai.com/basispoints/api/responses", upstream.lastReq.URL.String())
						require.Equal(t, "/basispoints/api/responses", GetActualOpenAIUpstreamEndpoint(c))
						require.Equal(t, "basispoints", recorder.Header().Get("X-Codex2API-Upstream"))
					} else {
						require.NotEqual(t, "bps.openai.com", upstream.lastReq.URL.Host)
					}
					require.Empty(t, recorder.Header().Get("X-Codex2API-Basispoints-Bypass"))
					t.Logf("enabled=%t chat=%t stream=%t upstream=%s status=%d", enabled, chat, stream, upstream.lastReq.URL, recorder.Code)
				})
			}
		}
	}
}

func TestStrictBPSLocalErrorsNeverInvokeNative(t *testing.T) {
	for name, body := range map[string]string{
		"forced tool":         `{"model":"gpt-5.6-sol","input":"test","tool_choice":"required"}`,
		"stored history":      `{"model":"gpt-5.6-sol","input":"test","previous_response_id":"resp_native"}`,
		"unsupported content": `{"model":"gpt-5.6-sol","input":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"fixture"}}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{}
			svc := openAIClientToolsTestService(upstream)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
			_, err := svc.Forward(context.Background(), c, excelAccount(), []byte(body))
			require.Error(t, err)
			require.Empty(t, upstream.requests)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Equal(t, "/basispoints/api/responses", GetActualOpenAIUpstreamEndpoint(c))
			require.Contains(t, recorder.Body.String(), "basispoints_request_invalid")
			require.Equal(t, "basispoints", recorder.Header().Get("X-Codex2API-Upstream"))
			t.Logf("case=%s network_calls=%d endpoint=%s status=%d", name, len(upstream.requests), GetActualOpenAIUpstreamEndpoint(c), recorder.Code)
		})
	}
}

func TestStrictBPSAuxiliaryEndpointsNeverInvokeNative(t *testing.T) {
	for _, endpoint := range []string{"/v1/images/generations", "/v1/alpha/search"} {
		t.Run(endpoint, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{}
			svc := openAIClientToolsTestService(upstream)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			body := []byte(`{"model":"gpt-5.6-sol","prompt":"test"}`)
			c.Request = httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
			var err error
			if endpoint == "/v1/alpha/search" {
				_, err = svc.ForwardAlphaSearch(context.Background(), c, excelAccount(), body)
			} else {
				_, err = svc.ForwardImages(context.Background(), c, excelAccount(), body, &OpenAIImagesRequest{Model: "gpt-5.6-sol", Endpoint: endpoint}, "")
			}
			require.Error(t, err)
			require.Empty(t, upstream.requests)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Equal(t, "/basispoints/api/responses", GetActualOpenAIUpstreamEndpoint(c))
			require.Contains(t, recorder.Body.String(), "basispoints_endpoint_unsupported")
		})
	}
}
