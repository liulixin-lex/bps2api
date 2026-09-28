package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func incidentBPSFrame(kind string, fields map[string]any) string {
	fields["type"] = kind
	raw, _ := json.Marshal(fields)
	return "event: " + kind + "\ndata: " + string(raw) + "\n\n"
}

func incidentBPSToolResponse(id, code string) string {
	args, _ := json.Marshal(map[string]any{"summary": "Run tool", "code": code, "extended_summary": "", "references": []any{}, "destructive": false})
	return incidentBPSFrame("response.completed", map[string]any{"response": map[string]any{
		"id": id, "status": "completed", "model": "gpt-5.6-sol", "output": []any{map[string]any{
			"type": "function_call", "id": "fc_" + id, "call_id": "call_" + id,
			"name": "run_officejs", "arguments": string(args),
		}},
	}})
}

func TestIncidentBPSProtocolRecoveryBoundaries(t *testing.T) {
	metadata := incidentBPSFrame("response.created", map[string]any{"response": map[string]any{"id": "resp_rejected", "status": "in_progress"}})
	bad := incidentBPSToolResponse("resp_rejected", "{malformed")
	good := incidentBPSToolResponse("resp_corrected", `{"name":"exec","input":"print('exact')"}`)
	textDelta := incidentBPSFrame("response.output_text.delta", map[string]any{"delta": "already delivered"})
	providerFailure := incidentBPSFrame("response.failed", map[string]any{"response": map[string]any{"status": "failed", "error": map[string]any{"code": "server_is_overloaded", "message": "busy"}}})
	for _, tc := range []struct {
		name, first, second string
		stream, success     bool
		calls               int
	}{
		{"recover stream", metadata + bad, good, true, true, 2},
		{"recover production sized initialization", incidentBPSFrame("response.created", map[string]any{"response": map[string]any{"id": "resp_rejected", "instructions": strings.Repeat("x", 95<<10)}}) + incidentBPSFrame("response.in_progress", map[string]any{"response": map[string]any{"id": "resp_rejected", "instructions": strings.Repeat("x", 95<<10)}}) + bad, good, true, true, 2},
		{"recover after buffered reasoning", metadata + incidentBPSFrame("response.output_item.added", map[string]any{"item": map[string]any{"type": "reasoning", "id": "reason_hidden"}}) + bad, good, true, true, 2},
		{"recover JSON", metadata + bad, good, false, true, 2},
		{"unmarked source cannot change operation", metadata + incidentBPSToolResponse("resp_rejected", "const r = await tools.exec_command({cmd: 'true'}); text(r);"), good, true, false, 2},
		{"no source regeneration after text", metadata + textDelta + incidentBPSToolResponse("resp_rejected", "text(await tools.exec_command({cmd: 'true'}));"), good, true, false, 1},
		{"one retry maximum", metadata + bad, bad, true, false, 2},
		{"no retry after text", metadata + textDelta + bad, good, true, false, 1},
		{"recover before buffered text is delivered", textDelta + bad, good, false, true, 2},
		{"no retry for provider failure", providerFailure, good, true, false, 1},
		{"provider JSON attribution", providerFailure, good, false, false, 1},
		{"no retry on metadata overflow", incidentBPSFrame("response.created", map[string]any{"padding": strings.Repeat("x", 270<<10)}) + bad, good, true, false, 1},
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
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			body := []byte(fmt.Sprintf(`{"model":"gpt-5.6-sol","stream":%t,"instructions":"retain original instruction","input":"run it","tools":[{"type":"custom","name":"exec"}]}`, tc.stream))
			_, err := svc.Forward(ctx, c, excelAccount(), body)
			require.Equal(t, tc.calls, len(upstream.requests))
			require.True(t, first.closed)
			if tc.success {
				require.NoError(t, err, rec.Body.String())
				require.NotContains(t, rec.Body.String(), "resp_rejected")
				require.NotContains(t, rec.Body.String(), "basispoints_protocol_error")
				require.Contains(t, rec.Body.String(), "resp_corrected")
				require.Contains(t, string(upstream.bodies[1]), "retain original instruction")
				require.Contains(t, string(upstream.bodies[1]), "Transport correction")
				require.Equal(t, upstream.requests[0].Context().Value(httpUpstreamProfileContextKey{}), upstream.requests[1].Context().Value(httpUpstreamProfileContextKey{}))
			} else {
				require.Error(t, err)
				if tc.name == "unmarked source cannot change operation" {
					require.Contains(t, rec.Body.String(), "changed an operation")
					require.NotContains(t, rec.Body.String(), "custom_tool_call")
				}
				if tc.name == "provider JSON attribution" {
					require.Contains(t, rec.Body.String(), "server_is_overloaded")
					require.NotContains(t, rec.Body.String(), "basispoints_protocol_error")
				}
			}
		})
	}
}
