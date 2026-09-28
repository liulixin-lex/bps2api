package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestExcelBPSHTTPRetryBounds(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	for _, tc := range []struct {
		status int
		header string
		retry  bool
	}{
		{502, "", true}, {503, "0", true}, {504, "2", true}, {503, "3", false},
		{503, "-1", false}, {503, "nonsense", false}, {503, now.Add(time.Second).Format(http.TimeFormat), true},
		{503, now.Add(time.Minute).Format(http.TimeFormat), false}, {403, "", false}, {401, "", false}, {429, "0", false},
	} {
		delay, retry := excelBPSHTTPRetryDelay(tc.status, tc.header, now)
		require.Equal(t, tc.retry, retry)
		if retry {
			require.GreaterOrEqual(t, delay, 250*time.Millisecond)
			require.LessOrEqual(t, delay, 2*time.Second)
		}
	}
}

func TestExcelBPSExplicitTransientHTTPRecovery(t *testing.T) {
	good := incidentBPSFrame("response.completed", map[string]any{"response": map[string]any{"id": "resp_http_recovered", "status": "completed", "model": "gpt-5.6-sol", "output": []any{}}})
	for _, tc := range []struct {
		name    string
		status  int
		header  string
		second  int
		calls   int
		success bool
	}{
		{"recover 502", 502, "0", 200, 2, true}, {"recover 503", 503, "0", 200, 2, true}, {"recover 504", 504, "0", 200, 2, true},
		{"one retry only", 503, "0", 503, 2, false}, {"respect backoff", 503, "30", 200, 1, false},
		{"permission denied", 403, "", 200, 1, false}, {"rate limited", 429, "", 200, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := &passthroughCloseTrackingReadCloser{Reader: strings.NewReader("{}")}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				{StatusCode: tc.status, Header: http.Header{"Retry-After": []string{tc.header}}, Body: first},
				{StatusCode: tc.second, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(good))},
			}}
			svc := openAIClientToolsTestService(upstream)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			_, err := svc.Forward(context.Background(), c, excelAccount(), []byte(`{"model":"gpt-5.6-sol","stream":false,"input":"test"}`))
			require.Len(t, upstream.requests, tc.calls)
			require.True(t, first.closed)
			if tc.success {
				require.NoError(t, err)
				require.Contains(t, w.Body.String(), "resp_http_recovered")
				require.Empty(t, w.Header().Get("Retry-After"))
			} else {
				require.Error(t, err)
				if tc.status == http.StatusTooManyRequests {
					var failover *UpstreamFailoverError
					require.True(t, errors.As(err, &failover))
					require.Equal(t, ExcelBPSRateLimitedReason, failover.Reason)
					require.False(t, IsResponseCommitted(c), "the handler owns the final 429 or account switch")
					return
				}
				require.Equal(t, tc.status, w.Code)
				if tc.calls == 1 {
					require.Equal(t, tc.header, w.Header().Get("Retry-After"))
				}
			}
		})
	}
}

func TestExcelBPSHTTPRetryCancellation(t *testing.T) {
	upstream := &httpUpstreamRecorder{responses: []*http.Response{{StatusCode: 503, Header: http.Header{"Retry-After": []string{"2"}}, Body: io.NopCloser(strings.NewReader("{}"))}}}
	svc := openAIClientToolsTestService(upstream)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := svc.Forward(ctx, c, excelAccount(), []byte(`{"model":"gpt-5.6-sol","input":"test"}`))
	require.Error(t, err)
	require.Len(t, upstream.requests, 1, "cancellation must never replay a request")
}

func TestExcelBPSRetryAfterHeaderValidation(t *testing.T) {
	for _, value := range []string{"-1", "bogus", "10\r\nX-Fake: 1", "4294967296"} {
		require.Empty(t, excelBPSRetryAfterHeader(value))
	}
	require.Equal(t, "60", excelBPSRetryAfterHeader(" 60 "))
}
