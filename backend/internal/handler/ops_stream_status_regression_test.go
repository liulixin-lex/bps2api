package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpsStreamingFailureKeepsLogicalGatewayStatus(t *testing.T) {
	for _, tc := range []struct {
		code    string
		want    int
		limited bool
	}{
		{gatewayConcurrencyLimitCode, 429, true},
		{gatewayQueueFullCode, 429, true},
		{"basispoints_image_request_busy", 503, false},
		{"basispoints_stream_timeout", 504, false},
		{"basispoints_protocol_error", 502, false},
	} {
		t.Run(tc.code, func(t *testing.T) {
			setupOpsErrorLogTestQueue(t, 2)
			gin.SetMode(gin.TestMode)
			ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(ops))
			router.POST("/v1/responses", func(c *gin.Context) {
				c.Data(200, "text/event-stream", []byte(fmt.Sprintf("event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":%q,\"message\":\"request failed\"}}}\n\n", tc.code)))
			})
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
			require.Equal(t, 200, recorder.Code, "committed SSE headers cannot change")
			require.Equal(t, int64(1), OpsErrorLogQueueLength())
			job := <-opsErrorLogQueue
			require.Equal(t, tc.want, job.entry.StatusCode)
			require.Equal(t, tc.limited, job.entry.IsBusinessLimited)
		})
	}
}
