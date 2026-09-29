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

	"github.com/Wei-Shaw/sub2api/internal/config"
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
		{502, "", true}, {503, "0", true}, {504, "2", true}, {503, "3", true},
		{503, "-1", false}, {503, "nonsense", false}, {503, now.Add(time.Second).Format(http.TimeFormat), true},
		{503, now.Add(time.Minute).Format(http.TimeFormat), true}, {403, "", false}, {401, "", false}, {429, "0", true},
	} {
		delay, retry := excelBPSHTTPRetryDelay(tc.status, tc.header, now)
		require.Equal(t, tc.retry, retry)
		if retry {
			require.GreaterOrEqual(t, delay, 250*time.Millisecond)
			require.LessOrEqual(t, delay, time.Minute)
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
		{"permission denied", 403, "", 200, 1, false}, {"rate limited", 429, "", 200, 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := &passthroughCloseTrackingReadCloser{Reader: strings.NewReader("{}")}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				{StatusCode: tc.status, Header: http.Header{"Retry-After": []string{tc.header}}, Body: first},
				{StatusCode: tc.second, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(good))},
			}}
			svc := openAIClientToolsTestService(upstream)
			svc.cfg.Gateway.ExcelBPSTimeouts.MaxAttempts = 2
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

func TestExcelBPSExplicitTransientHTTPRecoveryUsesBoundedAttempts(t *testing.T) {
	good := incidentBPSFrame("response.completed", map[string]any{"response": map[string]any{"id": "resp_http_recovered_after_two_failures", "status": "completed", "model": "gpt-5.6-sol", "output": []any{}}})
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: http.StatusBadGateway, Header: http.Header{"Retry-After": []string{"0"}}, Body: io.NopCloser(strings.NewReader("{}"))},
		{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"Retry-After": []string{"0"}}, Body: io.NopCloser(strings.NewReader("{}"))},
		{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(good))},
	}}
	svc := openAIClientToolsTestService(upstream)
	svc.cfg.Gateway.ExcelBPSTimeouts.MaxAttempts = 3
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	_, err := svc.Forward(context.Background(), c, excelAccount(), []byte(`{"model":"gpt-5.6-sol","stream":false,"input":"test"}`))
	require.NoError(t, err)
	require.Len(t, upstream.requests, 3)
	require.Contains(t, w.Body.String(), "resp_http_recovered_after_two_failures")
}

func TestExcelBPSPreOutputEOFRecoveryUsesBoundedAttempts(t *testing.T) {
	good := incidentBPSFrame("response.completed", map[string]any{"response": map[string]any{"id": "resp_eof_recovered", "status": "completed", "model": "gpt-5.6-sol", "output": []any{}}})
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))},
		{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))},
		{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(good))},
	}}
	svc := openAIClientToolsTestService(upstream)
	svc.cfg.Gateway.ExcelBPSTimeouts.MaxAttempts = 3
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	_, err := svc.Forward(context.Background(), c, excelAccount(), []byte(`{"model":"gpt-5.6-sol","stream":false,"input":"test"}`))
	require.NoError(t, err)
	require.Len(t, upstream.requests, 3)
	require.Contains(t, w.Body.String(), "resp_eof_recovered")
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

func TestExcelBPSRecoveryRepairSharesDeadlineAndAttempts(t *testing.T) {
	r := newExcelBPSRecovery(config.ExcelBPSTimeoutConfig{MaxAttempts: 3, RecoveryBudgetSeconds: 1})
	require.True(t, r.consumeRepair())
	ctx, cancel := r.withDeadline(context.Background())
	defer cancel()
	deadline := r.deadline
	_, inheritedDeadline := ctx.Deadline()
	require.False(t, inheritedDeadline, "recovery timer must be removable without removing caller cancellation")
	require.WithinDuration(t, time.Now().Add(time.Second), deadline, 100*time.Millisecond)
	_, nextDeadline, ok := r.reserve(time.Now(), 0)
	require.True(t, ok)
	require.Equal(t, deadline, nextDeadline, "HTTP recovery cannot restart the repair deadline")
	require.False(t, r.consumeRepair(), "all extra sends consume the same count")
	r.remaining = 1
	r.deadline = time.Now().Add(-time.Second)
	require.False(t, r.consumeRepair(), "expired recovery cannot start another repair")
}

func TestExcelBPSHTTPRetryRespectsConfiguredDelay(t *testing.T) {
	for _, limit := range []int{2, 5} {
		r := newExcelBPSRecovery(config.ExcelBPSTimeoutConfig{RecoveryMaxDelaySeconds: limit})
		delay, ok := excelBPSHTTPRetryDelay(503, "3", time.Now())
		require.True(t, ok)
		_, _, reserved := r.reserve(time.Now(), delay)
		require.Equal(t, limit >= 3, reserved, "the configured delay replaces the legacy hardcoded two-second limit")
	}
}
