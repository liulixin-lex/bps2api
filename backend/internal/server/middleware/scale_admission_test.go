package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestScaleAdmissionLargeTextRetainsMemoryBudget(t *testing.T) {
	initMiddlewareTestLogger(t)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan int, 1)
	r := admissionRouter(config.ImageRelayAdmissionConfig{ProcessingBudgetBytes: 8 << 20, MaxConcurrentRequests: 4}, func(c *gin.Context) {
		if c.GetHeader("Hold") == "true" {
			close(entered)
			<-release
		}
		c.Status(http.StatusNoContent)
	})
	large := "{\"input\":\"" + strings.Repeat("x", (1<<20)-12) + "\"}"
	require.Len(t, large, 1<<20)
	request := func(body string, hold bool) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		if hold {
			req.Header.Set("Hold", "true")
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	go func() { done <- request(large, true) }()
	defer func() { close(release); require.Equal(t, http.StatusNoContent, <-done) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("initial large body did not reach handler")
	}
	require.Equal(t, http.StatusServiceUnavailable, request(large, false), "large non-image histories retain memory during scheduler/upstream waits too")
	require.Equal(t, http.StatusNoContent, request("{\"input\":\"small text still works\"}", false), "bounded large bodies must not consume small-text headroom")
}
