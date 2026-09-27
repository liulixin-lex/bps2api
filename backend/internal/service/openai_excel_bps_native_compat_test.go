package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNativeCompatBPSPreOutputEOFSafety(t *testing.T) {
	metadata := incidentBPSFrame("response.created", map[string]any{"response": map[string]any{"id": "resp_discarded", "status": "in_progress"}})
	text := incidentBPSFrame("response.output_text.delta", map[string]any{"delta": "already visible"})
	good := incidentBPSFrame("response.completed", map[string]any{"response": map[string]any{"id": "resp_recovered", "status": "completed", "output": []any{}}})
	for _, tc := range []struct {
		name, first, second string
		stream, success     bool
		calls               int
	}{
		{"metadata EOF", metadata, good, true, true, 2},
		{"empty EOF", "", good, true, true, 2},
		{"undelivered JSON text", metadata + text, good, false, true, 2},
		{"streamed text cannot replay", metadata + text, good, true, false, 1},
		{"bounded one retry", metadata, metadata, true, false, 2},
		{"flushed metadata cannot replay", incidentBPSFrame("response.created", map[string]any{"padding": strings.Repeat("x", 270<<10)}), good, true, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := &passthroughCloseTrackingReadCloser{Reader: strings.NewReader(tc.first)}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				{StatusCode: 200, Header: http.Header{}, Body: first},
				{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.second))},
			}}
			svc := openAIClientToolsTestService(upstream)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			body, err := json.Marshal(map[string]any{"model": "gpt-5.6-sol", "input": "keep exact input", "stream": tc.stream})
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, err = svc.Forward(ctx, c, excelAccount(), body)
			require.Len(t, upstream.requests, tc.calls)
			require.True(t, first.closed)
			if tc.success {
				require.NoError(t, err)
				require.NotContains(t, rec.Body.String(), "resp_discarded")
				require.Contains(t, rec.Body.String(), "resp_recovered")
				require.JSONEq(t, string(upstream.bodies[0]), string(upstream.bodies[1]))
			} else {
				require.Error(t, err)
				require.NotContains(t, rec.Body.String(), "resp_recovered")
			}
		})
	}
}

func TestNativeCompatBPSIncompleteJSONPreservesResponse(t *testing.T) {
	for _, stream := range []bool{false, true} {
		wire := incidentBPSFrame("response.incomplete", map[string]any{"response": map[string]any{
			"id": "resp_partial", "status": "incomplete", "incomplete_details": map[string]any{"reason": "max_output_tokens"},
			"output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "partial answer"}}}},
		}})
		upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}}
		svc := openAIClientToolsTestService(upstream)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		body, err := json.Marshal(map[string]any{"model": "gpt-5.6-sol", "input": "test", "stream": stream})
		require.NoError(t, err)
		result, err := svc.Forward(context.Background(), c, excelAccount(), body)
		require.Error(t, err, "an incomplete answer must not be recorded as completed")
		require.Equal(t, "response.incomplete", result.UpstreamTerminalEvent)
		require.Len(t, upstream.requests, 1, "provider incomplete/refusal must not regenerate")
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Body.String(), "max_output_tokens")
		require.Contains(t, rec.Body.String(), "partial answer")
		if !stream {
			require.Equal(t, "incomplete", gjson.Get(rec.Body.String(), "status").String())
			require.Equal(t, "resp_partial", gjson.Get(rec.Body.String(), "id").String())
		}
	}
}

func TestNativeCompatBPSCustomCorrectionIsBounded(t *testing.T) {
	badEnvelope, err := json.Marshal(map[string]any{"name": "exec", "input": 7})
	require.NoError(t, err)
	goodEnvelope, err := json.Marshal(map[string]any{"name": "exec", "input": "text(1)"})
	require.NoError(t, err)
	bad, good := incidentBPSToolResponse("resp_bad", string(badEnvelope)), incidentBPSToolResponse("resp_good", string(goodEnvelope))
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(bad))},
		{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(good))},
	}}
	svc := openAIClientToolsTestService(upstream)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	body, err := json.Marshal(map[string]any{"model": "gpt-5.6-sol", "input": "test", "stream": true, "tools": []any{map[string]any{"type": "custom", "name": "exec"}}})
	require.NoError(t, err)
	_, err = svc.Forward(context.Background(), c, excelAccount(), body)
	require.NoError(t, err)
	require.Len(t, upstream.requests, 2)
	require.NotContains(t, rec.Body.String(), "resp_bad")
	require.Contains(t, rec.Body.String(), "text(1)")
}

func TestNativeCompatBPSNoCatalogRejectsAndCorrectsNativeTools(t *testing.T) {
	bad := incidentBPSFrame("response.completed", map[string]any{"response": map[string]any{
		"id": "resp_native", "status": "completed", "output": []any{map[string]any{
			"id": "fc_native", "call_id": "call_native", "type": "function_call", "name": "list_skills", "arguments": "{}",
		}},
	}})
	good := incidentBPSFrame("response.completed", map[string]any{"response": map[string]any{
		"id": "resp_corrected", "status": "completed", "output": []any{map[string]any{
			"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "answer without tools"}},
		}},
	}})
	for _, visible := range []bool{false, true} {
		wire := bad
		if visible {
			wire = incidentBPSFrame("response.output_text.delta", map[string]any{"delta": "visible answer"}) + wire
		}
		upstream := &httpUpstreamRecorder{responses: []*http.Response{
			{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))},
			{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(good))},
		}}
		svc := openAIClientToolsTestService(upstream)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		body, err := json.Marshal(map[string]any{"model": "gpt-5.6-sol", "input": "test", "stream": true})
		require.NoError(t, err)
		_, err = svc.Forward(context.Background(), c, excelAccount(), body)
		require.NotContains(t, rec.Body.String(), "list_skills", "undeclared native tools must never reach the client")
		if visible {
			require.Error(t, err)
			require.Len(t, upstream.requests, 1)
		} else {
			require.NoError(t, err)
			require.Len(t, upstream.requests, 2)
			require.Contains(t, rec.Body.String(), "answer without tools")
			require.Contains(t, string(upstream.bodies[1]), "catalog is empty, return assistant text only")
		}
	}
}

func TestNativeCompatBPSIncompleteReasonPrivacy(t *testing.T) {
	for _, tc := range []struct{ reason, want string }{
		{"max_output_tokens", "max_output_tokens"}, {"content_filter", "content_filter"},
		{"model_context_window_exceeded", "model_context_window_exceeded"},
		{"", "unspecified"}, {"private provider details", "unrecognized"},
	} {
		body, err := json.Marshal(map[string]any{"incomplete_details": map[string]any{"reason": tc.reason}})
		require.NoError(t, err)
		require.Equal(t, tc.want, excelBPSIncompleteReason(body))
	}
}

func TestNativeCompatBPSRecoverySharesBudgetAndCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		responses := []*http.Response{{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}}
		if !cancelled {
			responses = append([]*http.Response{{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("busy"))}}, responses...)
		}
		upstream := &httpUpstreamRecorder{responses: responses}
		svc := openAIClientToolsTestService(upstream)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		body, err := json.Marshal(map[string]any{"model": "gpt-5.6-sol", "input": "test", "stream": true})
		require.NoError(t, err)
		timeout := 3 * time.Second
		if cancelled {
			timeout = 30 * time.Millisecond
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		_, err = svc.Forward(ctx, c, excelAccount(), body)
		require.Error(t, err)
		if cancelled {
			require.Len(t, upstream.requests, 1)
		} else {
			require.Len(t, upstream.requests, 2, "HTTP and stream recovery must share one retry budget")
		}
	}
}
