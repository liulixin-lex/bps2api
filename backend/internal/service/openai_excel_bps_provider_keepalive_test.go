package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBPSProviderRecoveryHTTPAfterKeepalive(t *testing.T) {
	for _, status := range []int{429, 502, 503, 504} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c, rec := newPassthroughKeepaliveTestContext(t)
			stop := startOpenAISSEKeepalive(c, time.Hour)
			defer stop()
			value, ok := c.Get(openAICompactSSEKeepaliveKey)
			require.True(t, ok)
			require.True(t, value.(*openAICompactSSEKeepalive).beat())
			require.True(t, StopOpenAICompactSSEKeepaliveCommitted(c))
			require.True(t, c.Writer.Written())
			require.False(t, openAIStreamClientOutputStarted(c, false))
			require.False(t, IsResponseCommitted(c))
			first := &passthroughCloseTrackingReadCloser{Reader: strings.NewReader(`{"error":{"message":"temporarily unavailable"}}`)}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				{StatusCode: status, Header: http.Header{"Retry-After": {"0"}}, Body: first},
				{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(bpsProviderRecoverySuccess()))},
			}}
			svc := openAIClientToolsTestService(upstream)
			result, err := svc.Forward(context.Background(), c, excelAccount(), []byte(`{"model":"gpt-6-sol","stream":true,"input":"test"}`))
			t.Logf("OBSERVED HTTP%d after_keepalive calls=%d error=%v output=%s", status, len(upstream.requests), err, rec.Body.String())
			require.NoError(t, err)
			require.Len(t, upstream.requests, 2)
			require.True(t, first.closed)
			require.Equal(t, "response.completed", result.UpstreamTerminalEvent)
			require.Contains(t, rec.Body.String(), ": keepalive\n\n")
			require.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.completed"))
			require.NotContains(t, rec.Body.String(), "basispoints_upstream_error")
		})
	}
}

func TestBPSProviderRecoveryHTTPDoesNotReplayCommittedOutput(t *testing.T) {
	for _, status := range []int{429, 503} {
		for _, output := range []string{"text", "tool", "terminal marker"} {
			t.Run(fmt.Sprintf("%d/%s", status, output), func(t *testing.T) {
				c, rec := newPassthroughKeepaliveTestContext(t)
				stop := startOpenAISSEKeepalive(c, time.Hour)
				defer stop()
				value, ok := c.Get(openAICompactSSEKeepaliveKey)
				require.True(t, ok)
				require.True(t, value.(*openAICompactSSEKeepalive).beat())
				require.True(t, StopOpenAICompactSSEKeepaliveCommitted(c))
				switch output {
				case "text":
					_, err := c.Writer.WriteString(incidentBPSFrame("response.output_text.delta", map[string]any{"delta": "already delivered"}))
					require.NoError(t, err)
				case "tool":
					_, err := c.Writer.WriteString(incidentBPSFrame("response.output_item.added", map[string]any{"item": map[string]any{"type": "function_call", "call_id": "already_dispatched", "name": "test", "arguments": "{}"}}))
					require.NoError(t, err)
				case "terminal marker":
					MarkResponseCommitted(c)
				}
				upstream := &httpUpstreamRecorder{responses: []*http.Response{
					{StatusCode: status, Header: http.Header{"Retry-After": {"0"}}, Body: io.NopCloser(strings.NewReader(`{}`))},
					{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(bpsProviderRecoverySuccess()))},
				}}
				svc := openAIClientToolsTestService(upstream)
				_, err := svc.Forward(context.Background(), c, excelAccount(), []byte(`{"model":"gpt-6-sol","stream":true,"input":"test"}`))
				require.Error(t, err)
				require.Len(t, upstream.requests, 1, "delivered text, tools and committed terminal state forbid recovery")
				require.NotContains(t, rec.Body.String(), "resp_recovered")
			})
		}
	}
}
