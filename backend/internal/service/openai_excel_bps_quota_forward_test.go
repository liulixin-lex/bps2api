//go:build unit

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
)

func bpsQuotaForwardContext() (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	return c, rec
}

func bpsQuotaForwardHTTPFailure(model, group, retryAfter string) *http.Response {
	raw := fmt.Sprintf("{\"error\":{\"code\":\"rate_limit_exceeded\",\"type\":\"tokens\",\"message\":\"Rate limit reached for %s in organization org-%s on tokens per min (TPM): Limit 100, Used 100, Requested 5.\"}}", model, group)
	return &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": {retryAfter}},
		Body:       io.NopCloser(strings.NewReader(raw)),
	}
}

func TestBPSRecoveryHardeningQuotaForwardEmbeddedRetryAfterAtAttemptLimit(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			failure := incidentBPSFrame("error", map[string]any{"error": map[string]any{
				"code":    "rate_limit_exceeded",
				"type":    "tokens",
				"message": "Rate limit reached for gpt-6-sol in organization org-embedded on tokens per min (TPM): Limit 100, Used 100, Requested 5. Please try again in 4ms.",
				"headers": map[string]any{"Retry-After": "1", "Retry-After-Ms": "4"},
			}})
			first := &passthroughCloseTrackingReadCloser{Reader: strings.NewReader(failure)}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				{StatusCode: http.StatusOK, Header: http.Header{}, Body: first},
				{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(bpsProviderRecoverySuccess()))},
			}}
			svc := openAIClientToolsTestService(upstream)
			svc.cfg.Gateway.ExcelBPSTimeouts.MaxAttempts = 1
			c, rec := bpsQuotaForwardContext()
			_, err := svc.Forward(context.Background(), c, excelAccount(), []byte(fmt.Sprintf("{\"model\":\"gpt-6-sol\",\"stream\":%t,\"input\":\"test\"}", stream)))
			t.Logf("OBSERVED embedded_retry_after stream=%t sends=%d header=%q status=%d error=%v", stream, len(upstream.requests), rec.Header().Get("Retry-After"), rec.Code, err)
			require.Error(t, err)
			require.Len(t, upstream.requests, 1, "attempt exhaustion must not dispatch the queued success")
			require.True(t, first.closed)
			require.Equal(t, "1", rec.Header().Get("Retry-After"), "terminal headers retain the longest embedded provider hint")
			require.NotContains(t, rec.Body.String(), "resp_recovered")
		})
	}
}

func TestBPSRecoveryHardeningQuotaForwardSameAccountModelGate(t *testing.T) {
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		bpsQuotaForwardHTTPFailure("gpt-6-sol", "same-account", "6"),
		{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(bpsProviderRecoverySuccess()))},
	}}
	svc := openAIClientToolsTestService(upstream)
	svc.cfg.Gateway.ExcelBPSTimeouts.MaxAttempts = 1
	account := excelAccount()
	request := []byte("{\"model\":\"gpt-6-sol\",\"stream\":false,\"input\":\"test\"}")
	first, _ := bpsQuotaForwardContext()
	_, err := svc.Forward(context.Background(), first, account, request)
	var firstFailure *UpstreamFailoverError
	require.ErrorAs(t, err, &firstFailure)
	require.Equal(t, http.StatusTooManyRequests, firstFailure.ClientStatusCode)
	require.Equal(t, "6", firstFailure.ResponseHeaders.Get("Retry-After"))
	require.Len(t, upstream.requests, 1)

	secondAccount := *account
	require.Equal(t, account.ID, secondAccount.ID, "a fresh account snapshot has the same quota subject")
	second, rec := bpsQuotaForwardContext()
	_, err = svc.Forward(context.Background(), second, &secondAccount, request)
	t.Logf("OBSERVED same_account_model sends=%d error=%v", len(upstream.requests), err)
	var secondFailure *UpstreamFailoverError
	require.ErrorAs(t, err, &secondFailure)
	require.Equal(t, http.StatusTooManyRequests, secondFailure.ClientStatusCode)
	require.NotEmpty(t, secondFailure.ResponseHeaders.Get("Retry-After"))
	require.Len(t, upstream.requests, 1, "a live six-second provider gate must reject before another send")
	require.Len(t, upstream.responses, 1)
	require.NotContains(t, rec.Body.String(), "resp_recovered")
}

func TestBPSRecoveryHardeningQuotaForwardLearnsSharedGroupFromTwoAccounts(t *testing.T) {
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		bpsQuotaForwardHTTPFailure("gpt-6-sol", "shared-group", "0"),
		bpsQuotaForwardHTTPFailure("gpt-6-sol", "shared-group", "6"),
		{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(bpsProviderRecoverySuccess()))},
	}}
	svc := openAIClientToolsTestService(upstream)
	svc.cfg.Gateway.ExcelBPSTimeouts.MaxAttempts = 1
	firstAccount := excelAccount()
	secondAccount := excelAccount()
	secondAccount.ID = firstAccount.ID + 1
	secondAccount.Credentials["chatgpt_account_id"] = "test-account-second"
	require.NotEqual(t, firstAccount.ID, secondAccount.ID)
	request := []byte("{\"model\":\"gpt-6-sol\",\"stream\":false,\"input\":\"test\"}")
	for i, account := range []*Account{firstAccount, secondAccount} {
		c, _ := bpsQuotaForwardContext()
		_, err := svc.Forward(context.Background(), c, account, request)
		var failure *UpstreamFailoverError
		require.ErrorAs(t, err, &failure)
		require.Equal(t, http.StatusTooManyRequests, failure.ClientStatusCode)
		require.Len(t, upstream.requests, i+1, "an unknown account must reach the provider before its group is learned")
	}

	third, rec := bpsQuotaForwardContext()
	_, err := svc.Forward(context.Background(), third, firstAccount, request)
	t.Logf("OBSERVED learned_group first_account=%d second_account=%d sends=%d error=%v", firstAccount.ID, secondAccount.ID, len(upstream.requests), err)
	var failure *UpstreamFailoverError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, http.StatusTooManyRequests, failure.ClientStatusCode)
	require.NotEmpty(t, failure.ResponseHeaders.Get("Retry-After"))
	require.Len(t, upstream.requests, 2, "the second account's long hint must gate the first account only after both observed the same group")
	require.Len(t, upstream.responses, 1)
	require.NotContains(t, rec.Body.String(), "resp_recovered")
}

func TestBPSRecoveryHardeningQuotaForwardDoesNotReplayCommittedOutput(t *testing.T) {
	failure := incidentBPSFrame("error", map[string]any{"error": map[string]any{
		"code":    "rate_limit_exceeded",
		"type":    "tokens",
		"message": "Rate limit reached for gpt-6-sol in organization org-visible on tokens per min (TPM): Limit 100, Used 100, Requested 5.",
		"headers": map[string]any{"Retry-After": "1"},
	}})
	for _, tc := range []struct {
		name, prefix, marker string
	}{
		{"text", incidentBPSFrame("response.output_text.delta", map[string]any{"delta": "already delivered"}), "already delivered"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := &passthroughCloseTrackingReadCloser{Reader: strings.NewReader(tc.prefix + failure)}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				{StatusCode: http.StatusOK, Header: http.Header{}, Body: first},
				{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(bpsProviderRecoverySuccess()))},
			}}
			svc := openAIClientToolsTestService(upstream)
			svc.cfg.Gateway.ExcelBPSTimeouts.MaxAttempts = 3
			c, rec := bpsQuotaForwardContext()
			request := []byte("{\"model\":\"gpt-6-sol\",\"stream\":true,\"input\":\"test\",\"tools\":[{\"type\":\"function\",\"name\":\"test_tool\",\"parameters\":{\"type\":\"object\",\"properties\":{}}}]}")
			_, err := svc.Forward(context.Background(), c, excelAccount(), request)
			t.Logf("OBSERVED visible=%s sends=%d error=%v output=%s", tc.name, len(upstream.requests), err, rec.Body.String())
			require.Error(t, err)
			require.Len(t, upstream.requests, 1, "delivered text forbids replay despite spare attempts")
			require.True(t, first.closed)
			require.Contains(t, rec.Body.String(), tc.marker)
			require.NotContains(t, rec.Body.String(), "resp_recovered")
			require.Equal(t, 1, strings.Count(rec.Body.String(), "event: error"), "the visible stream has exactly one failure terminal")
		})
	}
}

func TestBPSRecoveryHardeningQuotaForwardBufferedToolMetadataAllowsRecovery(t *testing.T) {
	tool := map[string]any{"type": "function_call", "id": "fc_visible", "call_id": "call_visible", "name": "test_tool", "arguments": "{}"}
	prefix := incidentBPSFrame("response.output_item.added", map[string]any{"output_index": 0, "item": tool}) + incidentBPSFrame("response.output_item.done", map[string]any{"output_index": 0, "item": tool})
	failure := incidentBPSFrame("error", map[string]any{"error": map[string]any{
		"code":    "rate_limit_exceeded",
		"type":    "tokens",
		"message": "Rate limit reached for gpt-6-sol in organization org-buffered on tokens per min (TPM): Limit 100, Used 100, Requested 5.",
		"headers": map[string]any{"Retry-After": "0"},
	}})
	first := &passthroughCloseTrackingReadCloser{Reader: strings.NewReader(prefix + failure)}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: http.StatusOK, Header: http.Header{}, Body: first},
		{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(bpsProviderRecoverySuccess()))},
	}}
	svc := openAIClientToolsTestService(upstream)
	svc.cfg.Gateway.ExcelBPSTimeouts.MaxAttempts = 3
	svc.cfg.Gateway.ExcelBPSTimeouts.RecoveryInitialDelayMilliseconds = 1
	c, rec := bpsQuotaForwardContext()
	request := []byte("{\"model\":\"gpt-6-sol\",\"stream\":true,\"input\":\"test\",\"tools\":[{\"type\":\"function\",\"name\":\"test_tool\",\"parameters\":{\"type\":\"object\",\"properties\":{}}}]}")
	_, err := svc.Forward(context.Background(), c, excelAccount(), request)
	t.Logf("OBSERVED buffered_tool sends=%d error=%v output=%s", len(upstream.requests), err, rec.Body.String())
	require.NoError(t, err)
	require.Len(t, upstream.requests, 2, "unvalidated tool metadata has not dispatched a client call")
	require.True(t, first.closed)
	require.NotContains(t, rec.Body.String(), "call_visible")
	require.NotContains(t, rec.Body.String(), "fc_visible")
	require.NotContains(t, rec.Body.String(), "rate_limit_exceeded")
	require.Contains(t, rec.Body.String(), "resp_recovered")
}

func TestBPSRecoveryHardeningQuotaForwardCompletedToolDoesNotReplayAfterDisconnect(t *testing.T) {
	terminal := excelBPSRepairWire(t, "quota_committed", "codex2api.custom/functions.exec", "text(42);")
	first := &passthroughCloseTrackingReadCloser{Reader: io.MultiReader(
		strings.NewReader(terminal), passthroughErrReadCloser{err: io.ErrUnexpectedEOF},
	)}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: http.StatusOK, Header: http.Header{}, Body: first},
		{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(bpsProviderRecoverySuccess()))},
	}}
	svc := openAIClientToolsTestService(upstream)
	svc.cfg.Gateway.ExcelBPSTimeouts.MaxAttempts = 3
	c, rec := bpsQuotaForwardContext()
	request := []byte("{\"model\":\"gpt-5.6-sol\",\"stream\":true,\"input\":\"test\",\"tools\":[{\"type\":\"namespace\",\"name\":\"functions\",\"tools\":[{\"type\":\"custom\",\"name\":\"exec\"}]}]}")
	_, err := svc.Forward(context.Background(), c, excelAccount(), request)
	t.Logf("OBSERVED completed_tool_disconnect sends=%d error=%v output=%s", len(upstream.requests), err, rec.Body.String())
	require.Len(t, upstream.requests, 1, "a validated completed tool batch must not regenerate after disconnect")
	require.True(t, first.closed)
	require.Contains(t, rec.Body.String(), "call_quota_committed")
	require.Contains(t, rec.Body.String(), "custom_tool_call")
	require.Contains(t, rec.Body.String(), "text(42);")
	require.NotContains(t, rec.Body.String(), "resp_recovered")
}

// This public Forward fixture also runs unchanged against the pristine source.
func TestBPSRecoveryHardeningQuotaForwardRepairFailurePreservesUsage(t *testing.T) {
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		bpsCompletionResponse(200, "data: {\"type\": \"response.completed\", \"response\": {\"id\": \"original\", \"status\": \"completed\", \"usage\": {\"input_tokens\": 5, \"output_tokens\": 1}, \"output\": [{\"id\": \"fc_original\", \"call_id\": \"call_original\", \"type\": \"function_call\", \"name\": \"run_officejs\", \"arguments\": {\"code\": \"{\\\"name\\\": \\\"missing\\\", \\\"arguments\\\": {\\\"cmd\\\": \\\"pwd\\\"}}\"}}]}}\n\n"),
		bpsQuotaForwardHTTPFailure("gpt-6-astra", "repair", "6"),
	}}
	svc := openAIClientToolsTestService(upstream)
	c, rec := bpsQuotaForwardContext()
	result, err := svc.Forward(context.Background(), c, excelAccount(), []byte("{\"model\": \"gpt-6-astra\", \"stream\": false, \"input\": \"run pwd\", \"tools\": [{\"type\": \"function\", \"name\": \"shell\", \"parameters\": {\"type\": \"object\", \"required\": [\"cmd\"], \"properties\": {\"cmd\": {\"type\": \"string\"}}}}]}"))
	require.Error(t, err)
	require.NotNil(t, result)
	t.Logf("OBSERVED rejected_repair sends=%d status=%d input=%d output=%d", len(upstream.requests), rec.Code, result.Usage.InputTokens, result.Usage.OutputTokens)
	require.EqualValues(t, 5, result.Usage.InputTokens)
	require.EqualValues(t, 1, result.Usage.OutputTokens)
	require.Len(t, upstream.requests, 2, "the six-second provider hint must prevent replay")
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Equal(t, "6", rec.Header().Get("Retry-After"))
	require.NotContains(t, rec.Body.String(), "fc_original", "unvalidated tools must never be dispatched")
}
