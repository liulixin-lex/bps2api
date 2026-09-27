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

	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestExcelBPSMessagesActualRouteOnOff(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, enabled := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("bps=%t/stream=%t", enabled, stream), func(t *testing.T) {
				wire := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_messages\",\"model\":\"gpt-5.6-sol\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_messages\",\"model\":\"gpt-5.6-sol\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"messages result\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":2,\"input_tokens_details\":{\"cached_tokens\":3}}}}\n\n"
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}}
				svc := openAIClientToolsTestService(upstream)
				account := excelAccount()
				account.Extra["openai_excel_bps"] = enabled
				body := []byte(fmt.Sprintf(`{"model":"claude-fable-5","max_tokens":256,"messages":[{"role":"user","content":"hello"}],"stream":%t}`, stream))
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
				result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "messages-thread", "gpt-5.6-sol")
				require.NoError(t, err)
				require.NotNil(t, result)
				if enabled {
					require.Equal(t, basispoints.ResponsesURL, upstream.lastReq.URL.String())
					require.Equal(t, "/basispoints/api/responses", result.UpstreamEndpoint)
					require.Equal(t, "basispoints", rec.Header().Get("X-Codex2API-Upstream"))
				} else {
					require.NotEqual(t, "bps.openai.com", upstream.lastReq.URL.Host)
					require.Contains(t, upstream.lastReq.URL.Path, "/responses")
				}
				require.Equal(t, "gpt-5.6-sol", gjson.GetBytes(upstream.lastBody, "model").String())
				require.Equal(t, "claude-fable-5", result.Model)
				require.Equal(t, "gpt-5.6-sol", result.BillingModel)
				require.Equal(t, "gpt-5.6-sol", result.UpstreamModel)
				require.Equal(t, 10, result.Usage.InputTokens)
				require.Equal(t, 2, result.Usage.OutputTokens)
				require.Equal(t, 3, result.Usage.CacheReadInputTokens)
				require.Equal(t, http.StatusOK, rec.Code)
				require.Contains(t, rec.Body.String(), "messages result")
				require.NotContains(t, rec.Body.String(), "event: response.")
				if stream {
					require.Contains(t, rec.Body.String(), "event: message_start")
					require.Contains(t, rec.Body.String(), "event: message_stop")
					require.Contains(t, rec.Body.String(), `"model":"claude-fable-5"`)
					require.Contains(t, rec.Body.String(), `"cache_read_input_tokens":3`)
					require.NotContains(t, rec.Body.String(), "[DONE]")
				} else {
					require.Equal(t, "message", gjson.GetBytes(rec.Body.Bytes(), "type").String())
					require.Equal(t, "claude-fable-5", gjson.GetBytes(rec.Body.Bytes(), "model").String())
					require.Equal(t, int64(7), gjson.GetBytes(rec.Body.Bytes(), "usage.input_tokens").Int())
					require.Equal(t, int64(3), gjson.GetBytes(rec.Body.Bytes(), "usage.cache_read_input_tokens").Int())
				}
			})
		}
	}
}

func TestExcelBPSMessagesRejectionStaysAnthropicAndBPS(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"PRIVATE_UPSTREAM"}}`))}}
			svc := openAIClientToolsTestService(upstream)
			body := []byte(fmt.Sprintf(`{"model":"gpt-5.6-sol","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"stream":%t}`, stream))
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
			_, err := svc.ForwardAsAnthropic(context.Background(), c, excelAccount(), body, "", "")
			require.Error(t, err)
			var failover *UpstreamFailoverError
			require.NotErrorAs(t, err, &failover)
			require.Equal(t, "bps.openai.com", upstream.lastReq.URL.Host)
			require.Equal(t, http.StatusForbidden, rec.Code)
			require.Equal(t, "error", gjson.GetBytes(rec.Body.Bytes(), "type").String())
			require.Equal(t, "permission_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
			require.NotContains(t, rec.Body.String(), "PRIVATE_UPSTREAM")
			require.True(t, IsResponseCommitted(c))
		})
	}
}

func TestExcelBPSMessagesTerminalOnlyAndFailureWriter(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			var output strings.Builder
			writer := newExcelBPSMessagesWriter(&output, "client-model")
			raw := `{"type":"response.completed","response":{"id":"resp_terminal","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"terminal answer"}]}],"usage":{"input_tokens":4,"output_tokens":2}}}`
			if failure {
				raw = `{"type":"response.failed","response":{"status":"failed","error":{"type":"server_error","code":"basispoints_stream_incomplete","message":"stream failed"}}}`
			}
			_, err := writer.WriteString("data: " + raw + "\n\n")
			require.NoError(t, err)
			if failure {
				require.Contains(t, output.String(), "event: error")
				require.NotContains(t, output.String(), "message_stop")
				require.NotContains(t, output.String(), "end_turn")
			} else {
				require.Contains(t, output.String(), "message_start")
				require.Contains(t, output.String(), "terminal answer")
				require.Contains(t, output.String(), "message_stop")
				require.Less(t, strings.Index(output.String(), "message_start"), strings.Index(output.String(), "content_block"))
			}
			snapshot := output.String()
			_, err = writer.WriteString("data: " + raw + "\n\n")
			require.NoError(t, err)
			require.Equal(t, snapshot, output.String(), "terminal must be emitted once")
		})
	}
}

func TestExcelBPSMessagesFailedStreamHasNoSuccessfulStop(t *testing.T) {
	wire := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_failure\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"partial\"}\n\n" +
		"data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_failure\",\"status\":\"failed\",\"error\":{\"code\":\"upstream_failure\",\"message\":\"failed\"}}}\n\n"
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}}
	svc := openAIClientToolsTestService(upstream)
	body := []byte(`{"model":"gpt-5.6-sol","max_tokens":32,"messages":[{"role":"user","content":"hi"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	result, err := svc.ForwardAsAnthropic(context.Background(), c, excelAccount(), body, "", "")
	require.Error(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), "event: error")
	require.NotContains(t, rec.Body.String(), "event: response.failed")
	require.NotContains(t, rec.Body.String(), "event: message_stop")
}

func TestExcelBPSMessagesOtherProviderSwitchKeepsNativeRoute(t *testing.T) {
	wire := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_native\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"native result\"}]}],\"usage\":{\"input_tokens\":2,\"output_tokens\":2}}}\n\n"
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}}
	svc := openAIClientToolsTestService(upstream)
	account := excelAccount()
	account.Platform = PlatformGrok
	account.Type = AccountTypeAPIKey
	account.Credentials = map[string]any{"api_key": "grok-test-key", "base_url": "https://xai.test/v1"}
	require.False(t, account.IsExcelBPSEnabledForModel("grok-4"))
	body := []byte(`{"model":"grok-4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	_, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	require.NotEqual(t, "bps.openai.com", upstream.lastReq.URL.Host)
	require.NotContains(t, rec.Body.String(), "event: response.")
	require.Contains(t, rec.Body.String(), "native result")
}

func TestExcelBPSMessagesIncompleteWithoutTokenLimitIsError(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			wire := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_incomplete\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
				"data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_incomplete\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"content_filter\"},\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"partial\"}]}],\"usage\":{\"input_tokens\":2,\"output_tokens\":2}}}\n\n"
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}}
			svc := openAIClientToolsTestService(upstream)
			body := []byte(fmt.Sprintf(`{"model":"gpt-5.6-sol","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":%t}`, stream))
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
			_, err := svc.ForwardAsAnthropic(context.Background(), c, excelAccount(), body, "", "")
			require.Error(t, err)
			if stream {
				require.Contains(t, rec.Body.String(), "event: error")
				require.NotContains(t, rec.Body.String(), "event: message_stop")
			} else {
				require.Equal(t, http.StatusBadGateway, rec.Code)
				require.Equal(t, "error", gjson.GetBytes(rec.Body.Bytes(), "type").String())
			}
		})
	}
}

func TestExcelBPSMessagesMaxOutputTokensIsSuccessfulStop(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			wire := "data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_tokens\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"truncated\"}]}],\"usage\":{\"input_tokens\":2,\"output_tokens\":2}}}\n\n"
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}}
			svc := openAIClientToolsTestService(upstream)
			body := []byte(fmt.Sprintf(`{"model":"gpt-5.6-sol","max_tokens":2,"messages":[{"role":"user","content":"hello"}],"stream":%t}`, stream))
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
			result, err := svc.ForwardAsAnthropic(context.Background(), c, excelAccount(), body, "", "")
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Contains(t, rec.Body.String(), "truncated")
			if stream {
				require.Contains(t, rec.Body.String(), `"stop_reason":"max_tokens"`)
				require.Contains(t, rec.Body.String(), "event: message_stop")
				require.NotContains(t, rec.Body.String(), "event: error")
			} else {
				require.Equal(t, "max_tokens", gjson.GetBytes(rec.Body.Bytes(), "stop_reason").String())
			}
		})
	}
}

func TestExcelBPSMessagesPrematureEOFIsError(t *testing.T) {
	wire := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_eof\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"partial\"}\n\n"
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}}
	svc := openAIClientToolsTestService(upstream)
	body := []byte(`{"model":"gpt-5.6-sol","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	_, err := svc.ForwardAsAnthropic(context.Background(), c, excelAccount(), body, "", "")
	require.Error(t, err)
	require.Contains(t, rec.Body.String(), "event: error")
	require.NotContains(t, rec.Body.String(), "event: response.failed")
	require.NotContains(t, rec.Body.String(), "event: message_stop")
}
