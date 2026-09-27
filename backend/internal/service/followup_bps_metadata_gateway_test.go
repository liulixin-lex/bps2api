package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestFollowupBPSMetadataFailureGateway(t *testing.T) {
	for _, visible := range []bool{false, true} {
		t.Run(fmt.Sprint(visible), func(t *testing.T) {
			wire := ""
			if visible {
				wire = incidentBPSFrame("response.output_text.delta", map[string]any{"item_id": "msg_good", "delta": "delivered exactly once"})
			}
			wire += incidentBPSFrame("response.output_text.delta", map[string]any{"item_id": strings.Repeat("i", 600<<10), "output_index": 1, "delta": "must not emit"}) +
				incidentBPSFrame("response.completed", map[string]any{"response": map[string]any{"status": "completed", "output": []any{}}})
			reader := &passthroughCloseTrackingReadCloser{Reader: strings.NewReader(wire)}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: reader}}
			svc := openAIClientToolsTestService(upstream)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			result, err := svc.ForwardAsChatCompletions(context.Background(), c, excelAccount(), []byte(`{"model":"gpt-5.6-sol","stream":true,"messages":[{"role":"user","content":"hello"}]}`), "", "")
			require.Error(t, err)
			require.NotNil(t, result)
			require.False(t, result.ClientDisconnect)
			require.True(t, result.streamReadIncomplete)
			require.True(t, IsResponseCommitted(c))
			require.True(t, reader.closed, "failed conversion must close the upstream")
			require.Len(t, upstream.requests, 1, "resource failure must not replay")
			ops, ok := GetOpsStreamError(c)
			require.True(t, ok)
			require.Equal(t, "basispoints_response_metadata_limit", ops.ErrType)
			require.NotContains(t, rec.Body.String(), "must not emit")
			require.NotContains(t, rec.Body.String(), `"finish_reason":"stop"`)
			if visible {
				require.Equal(t, http.StatusOK, rec.Code)
				require.Equal(t, 1, strings.Count(rec.Body.String(), "delivered exactly once"))
				require.Equal(t, 1, strings.Count(rec.Body.String(), "basispoints_response_metadata_limit"))
				require.Equal(t, 1, strings.Count(rec.Body.String(), "data: [DONE]"))
			} else {
				require.Equal(t, http.StatusBadGateway, rec.Code)
				require.Equal(t, "basispoints_response_metadata_limit", gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
				require.Equal(t, "server_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
			}
		})
	}
}

func TestFollowupBPSMetadataPendingFramesDiscarded(t *testing.T) {
	var out strings.Builder
	w := newExcelBPSChatWriter(&out, "gpt-5.6-sol", false)
	_, err := w.WriteString(incidentBPSFrame("response.output_text.delta", map[string]any{"item_id": strings.Repeat("i", 600<<10), "delta": "rejected"}) +
		incidentBPSFrame("response.output_text.delta", map[string]any{"delta": "pending data"}))
	require.Error(t, err)
	_, err = w.WriteString(incidentBPSFrame("response.failed", map[string]any{"response": map[string]any{"error": map[string]any{"code": "explicit_failure"}}}))
	require.NoError(t, err)
	require.NotContains(t, out.String(), "pending data")
	require.Equal(t, 1, strings.Count(out.String(), "explicit_failure"))
	require.Equal(t, 1, strings.Count(out.String(), "data: [DONE]"))
}
