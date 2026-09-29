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

func TestBPSProviderRecoveryClassification(t *testing.T) {
	for _, tc := range []struct {
		name, raw, header string
		status            int
		retry             bool
		delay             time.Duration
	}{
		{"HTTP throttle", "{}", "0", 429, true, 250 * time.Millisecond},
		{"SSE wait milliseconds", "{\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"Please try again in 800ms.\"}}", "", 200, true, 800 * time.Millisecond},
		{"SSE wait seconds", "{\"error\":{\"code\":\"rate_limit_error\",\"retry_after_seconds\":1.5}}", "", 200, true, 1500 * time.Millisecond},
		{"HTTP header dominates", "{\"error\":{\"code\":\"rate_limit_error\",\"retry_after_ms\":600}}", "2", 429, true, 2 * time.Second},
		{"HTTP explicit quota", "{\"error\":{\"code\":\"insufficient_quota\"}}", "0", 429, false, 0},
		{"SSE explicit quota", "{\"response\":{\"error\":{\"type\":\"usage_limit_reached\"}}}", "", 200, false, 0},
		{"quota overrides503", "{\"error\":{\"code\":\"insufficient_quota\"}}", "0", 503, false, 0},
		{"quota message", "{\"error\":{\"message\":\"You exceeded your current quota, please check your plan.\"}}", "0", 429, false, 0},
		{"SSE status503", "{\"error\":{\"status_code\":503}}", "", 200, true, 250 * time.Millisecond},
		{"SSE status401", "{\"error\":{\"status_code\":401,\"code\":\"rate_limit_error\"}}", "", 200, false, 0},
		{"auth overrides header", "{\"error\":{\"code\":\"rate_limit_error\"}}", "0", 401, false, 0},
		{"policy overrides header", "{\"error\":{\"code\":\"rate_limit_error\"}}", "0", 403, false, 0},
		{"unknown provider error", "{\"error\":{\"code\":\"arbitrary_error\",\"type\":\"server_error\"}}", "", 200, false, 0},
		{"local protocol repair separate", "{\"response\":{\"error\":{\"code\":\"basispoints_protocol_error\",\"type\":\"server_error\"}}}", "", 200, false, 0},
		{"negative hint", "{\"error\":{\"code\":\"rate_limit_error\",\"retry_after_ms\":-1}}", "", 200, false, 0},
		{"overflow hint", "{\"error\":{\"code\":\"rate_limit_error\",\"retry_after_seconds\":1e99}}", "", 200, false, 0},
		{"malformed header", "{}", "bad", 503, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := excelBPSClassifyProviderFailure([]byte(tc.raw), tc.status, tc.header, time.Now())
			require.Equal(t, tc.retry, f.retry)
			if tc.retry {
				require.Equal(t, tc.delay, f.delay)
			}
		})
	}
}

func TestBPSProviderRecoverySafetyBoundaries(t *testing.T) {
	text := incidentBPSFrame("response.output_text.delta", map[string]any{"delta": "already delivered"})
	for _, tc := range []struct {
		name, wire string
		budget     time.Duration
		calls      int
	}{
		{"after text", text + bpsProviderRecoveryFailure("error", "rate_limit_error"), 30 * time.Second, 1},
		{"quota", bpsProviderRecoveryFailure("response.failed", "insufficient_quota"), 30 * time.Second, 1},
		{"credential", bpsProviderRecoveryFailure("error", "invalid_api_key"), 30 * time.Second, 1},
		{"policy", bpsProviderRecoveryFailure("error", "permission_denied"), 30 * time.Second, 1},
		{"unknown", bpsProviderRecoveryFailure("error", "arbitrary_error"), 30 * time.Second, 1},
		{"budget too short", bpsProviderRecoveryFailure("error", "rate_limit_error"), 50 * time.Millisecond, 1},
		{"bounded attempts", bpsProviderRecoveryFailure("response.failed", "rate_limit_error"), 30 * time.Second, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{responses: []*http.Response{}}
			for i := 0; i < 4; i++ {
				upstream.responses = append(upstream.responses, &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.wire))})
			}
			svc := openAIClientToolsTestService(upstream)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			timeouts := config.ExcelBPSTimeoutConfig{MaxAttempts: 3, RecoveryInitialDelayMilliseconds: 1}
			r := newExcelBPSRecovery(timeouts)
			r.budget = tc.budget
			_, err := svc.forwardExcelBPSAttempt(context.Background(), c, excelAccount(), []byte("{\"model\":\"gpt-6-sol\",\"stream\":true,\"input\":\"test\"}"), time.Now(), timeouts, 0, r)
			require.Error(t, err)
			require.Len(t, upstream.requests, tc.calls)
			require.Equal(t, 1, strings.Count(rec.Body.String(), "event: "+map[bool]string{true: "response.failed", false: "error"}[strings.Contains(tc.wire, "response.failed")]), "one final terminal, never duplicate attempts")
			if tc.name == "after text" {
				require.Equal(t, 1, strings.Count(rec.Body.String(), "already delivered"))
			}
		})
	}
}

func TestBPSProviderRecoveryReleasesLeaseAndCountsRPM(t *testing.T) {
	svc := openAIClientToolsTestService(nil)
	account := excelAccount()
	account.Extra["openai_excel_bps_mihomo"] = true
	account.Extra["base_rpm"] = 100
	cache := &openAIRPMTestCache{counts: map[int64]int{}}
	svc.rpmCache = cache
	var leases []*bpsTestLease
	var bodies []*passthroughCloseTrackingReadCloser
	acquire := func(context.Context, string, ...string) (string, excelBPSLease, error) {
		if len(leases) > 0 {
			require.Equal(t, 1, leases[len(leases)-1].releases)
			require.True(t, bodies[len(bodies)-1].closed)
		}
		lease := &bpsTestLease{}
		leases = append(leases, lease)
		return "http://127.0.0.1:19000", lease, nil
	}
	calls := 0
	svc.httpUpstream = &nativeAttachmentUpstream{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		calls++
		wire := bpsProviderRecoveryFailure("error", "rate_limit_error")
		if calls == 3 {
			wire = bpsProviderRecoverySuccess()
		}
		body := &passthroughCloseTrackingReadCloser{Reader: strings.NewReader(wire)}
		bodies = append(bodies, body)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: body}, nil
	}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	timeouts := config.ExcelBPSTimeoutConfig{MaxAttempts: 3, RecoveryInitialDelayMilliseconds: 1}
	_, err := svc.forwardExcelBPSAttemptWithAcquire(context.Background(), c, account, []byte("{\"model\":\"gpt-6-sol\",\"stream\":true,\"input\":\"test\"}"), time.Now(), timeouts, 0, newExcelBPSRecovery(timeouts), acquire)
	require.NoError(t, err)
	require.Equal(t, 3, calls)
	require.Equal(t, 3, cache.counts[account.ID])
	require.Equal(t, 3, c.GetInt("excel_bps_upstream_attempt"))
	for i, lease := range leases {
		require.Equal(t, 1, lease.releases)
		require.True(t, bodies[i].closed)
	}
	_, finalError := c.Get(OpsUpstreamStatusCodeKey)
	require.False(t, finalError)
	_, streamError := GetOpsStreamError(c)
	require.False(t, streamError)
}

func TestBPSProviderRecoveryCancellationDuringWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := &passthroughCloseTrackingReadCloser{Reader: strings.NewReader(bpsProviderRecoveryFailure("error", "rate_limit_error"))}
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Retry-After": {"2"}}, Body: body}}
	svc := openAIClientToolsTestService(upstream)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	_, err := svc.Forward(ctx, c, excelAccount(), []byte("{\"model\":\"gpt-6-sol\",\"stream\":true,\"input\":\"test\"}"))
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), time.Second)
	require.Len(t, upstream.requests, 1)
	require.True(t, body.closed)
	require.Empty(t, rec.Body.String())
}

func TestBPSProviderRecoveryRespectsProviderDelay(t *testing.T) {
	for _, kind := range []string{"error", "response.failed"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			var sent []time.Time
			svc := openAIClientToolsTestService(nil)
			svc.httpUpstream = &nativeAttachmentUpstream{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
				calls++
				sent = append(sent, time.Now())
				wire := bpsProviderRecoverySuccess()
				if calls == 1 {
					wire = strings.ReplaceAll(bpsProviderRecoveryFailure(kind, "rate_limit_error"), "152ms", "400ms")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}, nil
			}}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			_, err := svc.Forward(context.Background(), c, excelAccount(), []byte(fmt.Sprintf("{\"model\":\"gpt-6-sol\",\"stream\":%t,\"input\":\"test\"}", true)))
			require.NoError(t, err)
			require.Len(t, sent, 2)
			require.GreaterOrEqual(t, sent[1].Sub(sent[0]), 400*time.Millisecond)
		})
	}
}

func TestBPSProviderRecoveryPermanentQuotaIsFinal(t *testing.T) {
	for _, raw := range []string{
		"{\"error\":{\"type\":\"usage_limit_reached\",\"resets_in_seconds\":7200}}",
		fmt.Sprintf("{\"error\":{\"type\":\"usage_limit_reached\",\"resets_at\":%d}}", time.Now().Add(2*time.Hour).Unix()),
		"{\"error\":{\"code\":\"insufficient_quota\"}}",
	} {
		upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {"0"}}, Body: io.NopCloser(strings.NewReader(raw))}}
		svc := openAIClientToolsTestService(upstream)
		account := excelAccount()
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
		_, err := svc.Forward(context.Background(), c, account, []byte("{\"model\":\"gpt-6-sol\",\"input\":\"test\"}"))
		require.Error(t, err)
		var failover *UpstreamFailoverError
		require.NotErrorAs(t, err, &failover)
		require.Len(t, upstream.requests, 1)
		require.Equal(t, 429, rec.Code)
		require.True(t, IsResponseCommitted(c))
		require.True(t, account.IsSchedulable())
		require.Nil(t, account.RateLimitResetAt)
	}
}

func TestBPSProviderRecoveryChatAndMessages(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/messages"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", path, stream), func(t *testing.T) {
				good := incidentBPSFrame("response.completed", map[string]any{"response": map[string]any{"id": "resp_protocol_recovered", "status": "completed", "model": "gpt-6-sol", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "recovered answer"}}}}}})
				upstream := &httpUpstreamRecorder{responses: []*http.Response{
					{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(bpsProviderRecoveryFailure("error", "rate_limit_error")))},
					{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(good))},
				}}
				svc := openAIClientToolsTestService(upstream)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest("POST", path, nil)
				body := []byte(fmt.Sprintf("{\"model\":\"gpt-6-sol\",\"stream\":%t,\"max_tokens\":64,\"messages\":[{\"role\":\"user\",\"content\":\"test\"}]}", stream))
				var result *OpenAIForwardResult
				var err error
				if path == "/v1/messages" {
					result, err = svc.ForwardAsAnthropic(context.Background(), c, excelAccount(), body, "", "")
				} else {
					result, err = svc.ForwardAsChatCompletions(context.Background(), c, excelAccount(), body, "", "")
				}
				require.NoError(t, err)
				require.Equal(t, "gpt-6-sol", result.UpstreamModel)
				require.Len(t, upstream.requests, 2)
				for _, req := range upstream.requests {
					require.Equal(t, "bps.openai.com", req.URL.Host)
				}
				require.Contains(t, rec.Body.String(), "recovered answer")
				require.NotContains(t, rec.Body.String(), "rate_limit_error")
				require.NotContains(t, rec.Body.String(), "resp_failed")
			})
		}
	}
}

func TestBPSProviderRecoveryOutputRetainsCallerDeadline(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	r := newExcelBPSRecovery(config.ExcelBPSTimeoutConfig{MaxAttempts: 2})
	r.budget = 10 * time.Millisecond
	require.True(t, r.consumeRepair())
	child, closeChild := r.withDeadline(parent)
	defer closeChild()
	r.acceptOutput()
	<-child.Done()
	require.ErrorIs(t, child.Err(), context.DeadlineExceeded)
	require.ErrorIs(t, context.Cause(child), context.DeadlineExceeded)
	require.False(t, r.consumeRepair(), "healthy output never replenishes the expired recovery budget")
}

func TestBPSProviderRecoveryMultipleRetryAfterHints(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	for _, values := range [][]string{{"0.1", "0.7"}, {now.Add(2 * time.Second).Format(http.TimeFormat), "0.5"}, {"0.5", now.Add(2 * time.Second).Format(http.TimeFormat)}} {
		header := http.Header{"Retry-After": values}
		value := excelBPSRetryAfterValue(header)
		delay, ok := excelBPSHTTPRetryDelay(429, value, now)
		require.True(t, ok)
		if len(values[0]) < 10 && len(values[1]) < 10 {
			require.Equal(t, 700*time.Millisecond, delay)
		} else {
			require.Equal(t, 2*time.Second, delay)
		}
		require.NotContains(t, excelBPSRetryAfterHeader(value), "\n")
	}
	for _, value := range []string{"NaN", "Inf", "-0.5", "1\n-1"} {
		_, ok := excelBPSHTTPRetryDelay(429, value, now)
		require.False(t, ok)
	}
	r := newExcelBPSRecovery(config.ExcelBPSTimeoutConfig{RecoveryMaxDelaySeconds: 1})
	delay, ok := excelBPSHTTPRetryDelay(429, "0.5\n1.5", now)
	require.True(t, ok)
	_, _, reserved := r.reserve(now, delay)
	require.False(t, reserved)
}

func TestBPSProviderRecoveryToolRepairTransientFailures(t *testing.T) {
	for _, kind := range []string{"http429", "http502", "http503", "http504", "error", "response.failed"} {
		t.Run(kind, func(t *testing.T) {
			status := 200
			wire := bpsProviderRecoveryFailure(kind, "rate_limit_error")
			if strings.HasPrefix(kind, "http") {
				_, err := fmt.Sscanf(kind, "http%d", &status)
				require.NoError(t, err)
				wire = "{\"error\":{\"code\":\"rate_limit_error\"}}"
			}
			first := excelBPSRepairWire(t, "repair_attempt", "Run", "text(42);")
			good := excelBPSRepairWire(t, "recovered", "codex2api.custom/functions.exec", "text(42);")
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(first))},
				{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))},
				{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(good))},
			}}
			svc := openAIClientToolsTestService(upstream)
			svc.cfg.Gateway.ExcelBPSTimeouts.MaxAttempts = 3
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			body := []byte("{\"model\":\"gpt-5.6-sol\",\"stream\":true,\"input\":\"test\",\"tools\":[{\"type\":\"namespace\",\"name\":\"functions\",\"tools\":[{\"type\":\"custom\",\"name\":\"exec\"}]}]}")
			result, err := svc.Forward(context.Background(), c, excelAccount(), body)
			require.NoError(t, err)
			require.Len(t, upstream.requests, 3)
			require.Contains(t, rec.Body.String(), "text(42);")
			require.NotContains(t, rec.Body.String(), "basispoints_protocol_error")
			require.NotContains(t, rec.Body.String(), "repair_attempt")
			require.Equal(t, 20, result.Usage.InputTokens, "both original and recovered generated responses retain metered usage")
			require.Equal(t, 3, c.GetInt("excel_bps_upstream_attempt"))
		})
	}
}

func TestBPSProviderRecoveryToolRepairNeverReplaysVisibleOutput(t *testing.T) {
	wire := incidentBPSFrame("response.output_text.delta", map[string]any{"delta": "already delivered"}) + excelBPSRepairWire(t, "must_not_dispatch", "Run", "text(42);")
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))},
		{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))},
	}}
	svc := openAIClientToolsTestService(upstream)
	svc.cfg.Gateway.ExcelBPSTimeouts.MaxAttempts = 3
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	_, err := svc.Forward(context.Background(), c, excelAccount(), []byte("{\"model\":\"gpt-5.6-sol\",\"stream\":true,\"input\":\"test\",\"tools\":[{\"type\":\"namespace\",\"name\":\"functions\",\"tools\":[{\"type\":\"custom\",\"name\":\"exec\"}]}]}"))
	require.Error(t, err)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, 1, strings.Count(rec.Body.String(), "already delivered"))
	require.NotContains(t, rec.Body.String(), "custom_tool_call_input")
}

func TestBPSProviderRecoveryAccountSwitchSharesTotalBudget(t *testing.T) {
	for _, succeeds := range []bool{true, false} {
		t.Run(fmt.Sprint(succeeds), func(t *testing.T) {
			first, second := excelAccount(), excelAccount()
			second.ID++
			svc := openAIClientToolsTestService(nil)
			svc.cfg.Gateway.ExcelBPSTimeouts.MaxAttempts = 3
			svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: []Account{*first, *second}}
			var sent []int64
			svc.httpUpstream = &nativeAttachmentUpstream{do: func(req *http.Request, _ string, id int64, _ int) (*http.Response, error) {
				sent = append(sent, id)
				if id == second.ID && succeeds {
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(bpsProviderRecoverySuccess()))}, nil
				}
				return &http.Response{StatusCode: 429, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{\"error\":{\"code\":\"rate_limit_error\"}}"))}, nil
			}}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			c.Set("api_key", &APIKey{ID: 1})
			body := []byte("{\"model\":\"gpt-6-sol\",\"input\":\"test\"}")
			_, err := svc.Forward(context.Background(), c, first, body)
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			require.Len(t, sent, 2)
			_, err = svc.Forward(context.Background(), c, second, body)
			if succeeds {
				require.NoError(t, err)
				require.Contains(t, rec.Body.String(), "resp_recovered")
			} else {
				require.Error(t, err)
				_, err = svc.Forward(context.Background(), c, first, body)
				require.Error(t, err)
				require.True(t, IsResponseCommitted(c))
			}
			require.Equal(t, []int64{first.ID, first.ID, second.ID}, sent, "the alternate account gets the last send without multiplying the envelope")
		})
	}
}
