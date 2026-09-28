package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestFollowupBPSErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		status int
		kind   string
	}{
		{400, "invalid_request_error"}, {401, "authentication_error"},
		{403, "permission_error"}, {404, "not_found_error"},
		{429, "rate_limit_error"}, {500, "server_error"},
		{502, "server_error"}, {503, "service_unavailable_error"}, {504, "server_error"},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: tc.status, Header: http.Header{"Retry-After": {"30"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"PRIVATE_UPSTREAM"}}`))}}
			svc := openAIClientToolsTestService(upstream)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			_, err := svc.Forward(context.Background(), c, excelAccount(), []byte(`{"model":"gpt-5.6-sol","input":"test"}`))
			require.Error(t, err)
			if tc.status == http.StatusTooManyRequests {
				requireExcelBPSRateLimitFailover(t, err, c)
				var failover *UpstreamFailoverError
				require.ErrorAs(t, err, &failover)
				require.Equal(t, "30", failover.ResponseHeaders.Get("Retry-After"))
				require.Equal(t, "gpt-5.6-sol", failover.RequiredExcelBPSUpstreamModel)
				require.Empty(t, w.Body.String())
			} else {
				require.Equal(t, tc.status, w.Code)
				require.Equal(t, tc.kind, gjson.GetBytes(w.Body.Bytes(), "error.type").String())
				require.Equal(t, "basispoints_upstream_error", gjson.GetBytes(w.Body.Bytes(), "error.code").String())
				require.Equal(t, "30", w.Header().Get("Retry-After"))
			}
			require.NotContains(t, w.Body.String(), "PRIVATE_UPSTREAM")
			require.Len(t, upstream.requests, 1, "classification must not introduce request replay")
		})
	}
}

func TestFollowupBPSStreamErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		status int
		kind   string
	}{
		{400, "invalid_request_error"}, {429, "rate_limit_error"},
		{503, "service_unavailable_error"}, {504, "server_error"},
	} {
		for _, chat := range []bool{false, true} {
			t.Run(fmt.Sprintf("status=%d/chat=%t", tc.status, chat), func(t *testing.T) {
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				_, err := c.Writer.WriteString(": keepalive\n\n")
				require.NoError(t, err)
				c.Writer.Flush()
				var request *excelBPSChatRequest
				var output io.StringWriter = c.Writer
				if chat {
					request = &excelBPSChatRequest{OriginalModel: "gpt-5.6-sol"}
					output = newExcelBPSChatWriter(c.Writer, request.OriginalModel, false)
				}
				writeExcelBPSStreamFailure(c, request, output, tc.status, "basispoints_upstream_error", "Public failure")
				require.Equal(t, http.StatusOK, w.Code, "committed status must not be rewritten")
				var payload string
				for _, line := range strings.Split(w.Body.String(), "\n") {
					if strings.HasPrefix(line, "data: {") {
						payload = strings.TrimPrefix(line, "data: ")
					}
				}
				path := "response.error"
				if chat {
					path = "error"
					require.Equal(t, 1, strings.Count(w.Body.String(), "data: [DONE]"))
				} else {
					require.Equal(t, "response.failed", gjson.Get(payload, "type").String())
					require.True(t, strings.HasPrefix(gjson.Get(payload, "response.id").String(), "resp_"))
					require.True(t, gjson.Get(payload, "response.created_at").Exists())
				}
				require.Equal(t, tc.kind, gjson.Get(payload, path+".type").String())
				require.Equal(t, "basispoints_upstream_error", gjson.Get(payload, path+".code").String())
				streamError, exists := GetOpsStreamError(c)
				require.True(t, exists, "failed Chat streams must remain visible in Ops")
				require.Equal(t, "basispoints_upstream_error", streamError.ErrType)
			})
		}
	}
}

func TestFollowupBPSLocalUnavailableRetryHint(t *testing.T) {
	upstream := &httpUpstreamRecorder{}
	svc := openAIClientToolsTestService(upstream)
	svc.settingService = NewSettingService(&excelBPSImageSettingsRepo{err: fmt.Errorf("PRIVATE_DATABASE")}, &config.Config{})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	_, err := svc.Forward(context.Background(), c, excelAccount(), []byte(`{"model":"gpt-5.6-sol","input":"test"}`))
	require.Error(t, err)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Equal(t, "service_unavailable_error", gjson.GetBytes(w.Body.Bytes(), "error.type").String())
	require.Equal(t, "1", w.Header().Get("Retry-After"))
	require.NotContains(t, w.Body.String(), "PRIVATE_DATABASE")
	require.Empty(t, upstream.requests)
}
