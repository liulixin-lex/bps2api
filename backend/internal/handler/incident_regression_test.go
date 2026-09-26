package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestIncidentIncompleteStreamIsRecorded(t *testing.T) {
	for _, frame := range []string{
		"event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n",
		"data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"content_filter\"}}}\n\n",
	} {
		setupOpsErrorLogTestQueue(t, 2)
		gin.SetMode(gin.TestMode)
		ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
		router := gin.New()
		router.Use(OpsErrorLoggerMiddleware(ops))
		router.POST("/v1/responses", func(c *gin.Context) {
			c.Header("Content-Type", "text/event-stream")
			for _, b := range []byte(frame) {
				_, _ = c.Writer.Write([]byte{b})
			}
		})
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
		require.Equal(t, 200, rec.Code)
		require.Equal(t, int64(1), OpsErrorLogQueueLength(), "incomplete SSE must not look like a successful request")
		entry := (<-opsErrorLogQueue).entry
		require.Equal(t, 502, entry.StatusCode)
		require.Contains(t, entry.ErrorMessage, "incomplete")
	}
}

func TestIncidentProtocolFailureBelongsToGateway(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	_, _, _, source := classifyOpsErrorLog(c, "upstream_error", "invalid tool envelope", "basispoints_protocol_error", 502)
	require.Equal(t, "gateway", source)
}

func TestIncidentOverloadRetriesHaveBoundedJitter(t *testing.T) {
	err := &service.UpstreamFailoverError{StatusCode: 503, RequestScopedTransient: true}
	seen := make(map[time.Duration]bool)
	for i := 0; i < 32; i++ {
		delay := sameAccountRetryDelayFor(err, 1)
		require.GreaterOrEqual(t, delay, 500*time.Millisecond)
		require.LessOrEqual(t, delay, 750*time.Millisecond)
		seen[delay] = true
	}
	require.Greater(t, len(seen), 1, "synchronized clients must not all retry at exactly 500ms")
	require.LessOrEqual(t, sameAccountRetryDelayFor(err, 100), 8*time.Second)
	err.SameAccountRetryDelay = 4 * time.Second
	require.Equal(t, 4*time.Second, sameAccountRetryDelayFor(err, 1), "explicit provider delay wins")
}
