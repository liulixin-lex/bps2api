package service

import (
	"context"
	"fmt"
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

func bpsProviderRecoverySuccess() string {
	return incidentBPSFrame("response.completed", map[string]any{"response": map[string]any{"id": "resp_recovered", "model": "gpt-6-sol", "status": "completed", "output": []any{}}})
}

func bpsProviderRecoveryFailure(kind, code string) string {
	failure := map[string]any{"code": code, "message": "Please try again in 152ms."}
	if kind == "error" {
		return incidentBPSFrame(kind, map[string]any{"error": failure})
	}
	return incidentBPSFrame(kind, map[string]any{"response": map[string]any{"id": "resp_failed", "status": "failed", "error": failure, "output": []any{}}})
}

func TestBPSProviderRecoverySSE(t *testing.T) {
	for _, kind := range []string{"error", "response.failed"} {
		for _, code := range []string{"rate_limit_exceeded", "rate_limit_error", "bad_gateway", "server_is_overloaded", "service_unavailable", "gateway_timeout"} {
			for _, stream := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", kind, code, stream), func(t *testing.T) {
					first := &passthroughCloseTrackingReadCloser{Reader: strings.NewReader(bpsProviderRecoveryFailure(kind, code))}
					upstream := &httpUpstreamRecorder{responses: []*http.Response{
						{StatusCode: 200, Header: http.Header{}, Body: first},
						{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(bpsProviderRecoverySuccess()))},
					}}
					svc := openAIClientToolsTestService(upstream)
					svc.cfg.Gateway.ExcelBPSTimeouts.MaxAttempts = 3
					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
					result, err := svc.Forward(context.Background(), c, excelAccount(), []byte(fmt.Sprintf(`{"model":"gpt-6-sol","stream":%t,"input":"test"}`, stream)))
					t.Logf("OBSERVED kind=%s code=%s calls=%d http=%d error=%v output=%s", kind, code, len(upstream.requests), recorder.Code, err, recorder.Body.String())
					require.NoError(t, err)
					require.Len(t, upstream.requests, 2)
					require.True(t, first.closed)
					require.Equal(t, "response.completed", result.UpstreamTerminalEvent)
					require.NotContains(t, recorder.Body.String(), "resp_failed")
					require.NotContains(t, recorder.Body.String(), code)
					require.Contains(t, recorder.Body.String(), "resp_recovered")
				})
			}
		}
	}
}

func TestBPSProviderRecoveryHTTP429(t *testing.T) {
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: 429, Header: http.Header{"Retry-After": {"0"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"rate_limit_exceeded"}}`))},
		{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(bpsProviderRecoverySuccess()))},
	}}
	svc := openAIClientToolsTestService(upstream)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	_, err := svc.Forward(context.Background(), c, excelAccount(), []byte(`{"model":"gpt-6-sol","input":"test"}`))
	t.Logf("OBSERVED HTTP429 calls=%d error=%v output=%s", len(upstream.requests), err, recorder.Body.String())
	require.NoError(t, err)
	require.Len(t, upstream.requests, 2)
	require.Contains(t, recorder.Body.String(), "resp_recovered")
	_, cooled := svc.excelBPSCooldownUntil.Load(excelAccount().ID)
	require.False(t, cooled, "a recovered short throttle must not leave the account cooled")
}

func TestBPSProviderRecoveryKeepsEncryptedRepair(t *testing.T) {
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(excelBPSInvalidCiphertext))},
		{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))},
		{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(bpsProviderRecoverySuccess()))},
	}}
	svc := openAIClientToolsTestService(upstream)
	svc.cfg.Gateway.ExcelBPSTimeouts.MaxAttempts = 3
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	_, err := svc.Forward(context.Background(), c, excelAccount(), excelBPSEncryptedHistoryRequest(false))
	require.NoError(t, err)
	require.Len(t, upstream.requests, 3)
	for i := 1; i < 3; i++ {
		t.Logf("OBSERVED attempt=%d encrypted_present=%t", i+1, strings.Contains(string(upstream.bodies[i]), "encrypted_content"))
		require.NotContains(t, string(upstream.bodies[i]), "encrypted_content", "repair must survive every later transport recovery")
	}
}

func TestBPSProviderRecoveryHealthyStreamOutlivesBudget(t *testing.T) {
	for _, kind := range []string{"response.output_text.delta", "response.reasoning_summary_text.delta"} {
		t.Run(kind, func(t *testing.T) { testBPSRecoveredLongStream(t, kind) })
	}
}

func testBPSRecoveredLongStream(t *testing.T, kind string) {
	svc := openAIClientToolsTestService(nil)
	calls := 0
	svc.httpUpstream = &nativeAttachmentUpstream{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}
		reader, writer := io.Pipe()
		go func() {
			defer func() { _ = writer.Close() }()
			_, _ = io.WriteString(writer, incidentBPSFrame(kind, map[string]any{"delta": "healthy output"}))
			select {
			case <-req.Context().Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
			_, _ = io.WriteString(writer, bpsProviderRecoverySuccess())
		}()
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: reader}, nil
	}}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	timeouts := config.ExcelBPSTimeoutConfig{MaxAttempts: 2, RecoveryInitialDelayMilliseconds: 1}
	recovery := newExcelBPSRecovery(timeouts)
	recovery.budget = 350 * time.Millisecond
	_, err := svc.forwardExcelBPSAttempt(context.Background(), c, excelAccount(), []byte(`{"model":"gpt-6-sol","stream":true,"input":"test"}`), time.Now(), timeouts, 0, recovery)
	t.Logf("OBSERVED long_recovered_stream calls=%d error=%v output=%s", calls, err, recorder.Body.String())
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Contains(t, recorder.Body.String(), "resp_recovered")
}
