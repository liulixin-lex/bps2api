package service

import (
	"context"
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

func requireExcelBPSModelAccessFailover(t *testing.T, err error, c *gin.Context, model string) *UpstreamFailoverError {
	t.Helper()
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Equal(t, http.StatusForbidden, failover.StatusCode)
	require.Equal(t, http.StatusForbidden, failover.ClientStatusCode)
	require.Equal(t, ExcelBPSModelAccessChangedReason, failover.Reason)
	require.Equal(t, model, failover.RequiredExcelBPSUpstreamModel)
	require.True(t, failover.ShouldRetryNextAccount())
	require.False(t, failover.RetryableOnSameAccount)
	require.False(t, failover.ShouldReportAccountScheduleFailure())
	require.False(t, failover.IsCredentialFailure())
	require.Empty(t, failover.ResponseBody)
	require.False(t, c.Writer.Written())
	require.False(t, IsResponseCommitted(c))
	return failover
}

func TestExcelBPSModelAccessFailoverPreservesMappedModelAndCommitBoundary(t *testing.T) {
	for _, committed := range []bool{false, true} {
		for _, retryAfter := range []string{"30", "PRIVATE_INVALID_HEADER"} {
			t.Run(fmt.Sprintf("committed=%t/retry=%s", committed, retryAfter), func(t *testing.T) {
				upstream := &httpUpstreamRecorder{resp: &http.Response{
					StatusCode: http.StatusForbidden,
					Header:     http.Header{"Retry-After": {retryAfter}},
					Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"basispoints_model_access_changed","message":"PRIVATE_UPSTREAM"}}`)),
				}}
				svc := openAIClientToolsTestService(upstream)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				if committed {
					_, err := c.Writer.WriteString(": keepalive\n\n")
					require.NoError(t, err)
					c.Writer.Flush()
				}
				account := excelAccount()
				account.Credentials["model_mapping"] = map[string]any{"client-model": "gpt-6-sol"}
				_, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"client-model","input":"x","stream":true}`))
				require.Error(t, err)
				require.Len(t, upstream.requests, 1, "never retry the denied account")
				require.Equal(t, "gpt-6-sol", gjson.GetBytes(upstream.lastBody, "model").String())
				if committed {
					var failover *UpstreamFailoverError
					require.NotErrorAs(t, err, &failover)
					require.Equal(t, http.StatusOK, rec.Code)
					require.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.failed"))
					require.Contains(t, rec.Body.String(), `"code":"basispoints_model_access_changed"`)
					require.True(t, IsResponseCommitted(c))
				} else {
					failover := requireExcelBPSModelAccessFailover(t, err, c, "gpt-6-sol")
					wantHeader := ""
					if retryAfter == "30" {
						wantHeader = retryAfter
					}
					require.Equal(t, wantHeader, failover.ResponseHeaders.Get("Retry-After"))
					require.Empty(t, rec.Body.String())
				}
				require.NotContains(t, rec.Body.String(), "PRIVATE_")
				require.True(t, account.IsExcelBPSEnabled())
				require.True(t, account.IsSchedulable())
			})
		}
	}
}
