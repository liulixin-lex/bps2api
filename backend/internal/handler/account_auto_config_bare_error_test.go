package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAutoConfigBareChatSSEErrorDoesNotPromote(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		wantSuccess   bool
	}{
		{"bare error", "{\"error\":{\"type\":\"rate_limit_error\",\"message\":\"limited\"}}", false},
		{"error string", "{\"error\":\"upstream unavailable\"}", false},
		{"null error", "{\"error\":null,\"choices\":[{\"finish_reason\":\"stop\"}]}", true},
		{"error mentioned in text", "{\"choices\":[{\"delta\":{\"content\":\"error\"},\"finish_reason\":\"stop\"}]}", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			ops := service.NewOpsService(nil, nil, &config.Config{}, nil, nil, nil, nil, nil, nil, nil, nil)
			var got []service.AccountConcurrencyResult
			ops.SetAutoConfigObserver(func(r service.AccountConcurrencyResult) { got = append(got, r) })
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(ops))
			router.POST("/v1/chat/completions", func(c *gin.Context) {
				c.Set(opsAccountIDKey, int64(7))
				c.Set(opsStreamKey, true)
				c.Header("Content-Type", "text/event-stream")
				body := "data: " + tc.payload + "\n\ndata: [DONE]\n\n"
				// Split frames across writes as a real upstream stream can do.
				for _, chunk := range []string{body[:11], body[11:]} {
					_, err := c.Writer.WriteString(chunk)
					require.NoError(t, err)
				}
			})
			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
			require.Len(t, got, 1)
			require.Equal(t, tc.wantSuccess, got[0].Success)
		})
	}
}
