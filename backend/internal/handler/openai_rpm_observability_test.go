package handler

import (
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestRuntimeRPMExhaustionAttribution(t *testing.T) {
	for _, staleUpstream := range []bool{false, true} {
		t.Run(strconv.FormatBool(staleUpstream), func(t *testing.T) {
			c, w := newGinContextForEndpoint(t, EndpointResponses)
			if staleUpstream {
				c.Set(service.OpsUpstreamStatusCodeKey, http.StatusBadGateway)
			}
			admission := openAIRPMAdmission{exhausted: true, resetAt: time.Now().Add(20 * time.Second)}
			err := admission.selectionError(service.ErrNoAvailableAccounts)
			admission.retryAfter(c, err)
			classification := classifySelectionFailureError(err, noAccountErrorClassification{})
			phase, limited, owner, source := classifyOpsErrorLog(c, classification.ErrType, classification.Message, "", classification.Status)
			t.Logf("OBSERVED rpm_exhausted stale_upstream=%t status=%d phase=%s business_limited=%t owner=%s source=%s retry_after=%s", staleUpstream, classification.Status, phase, limited, owner, source, w.Header().Get("Retry-After"))
			require.Equal(t, http.StatusTooManyRequests, classification.Status)
			seconds, parseErr := strconv.Atoi(w.Header().Get("Retry-After"))
			require.NoError(t, parseErr)
			require.GreaterOrEqual(t, seconds, 1)
			require.LessOrEqual(t, seconds, 20)
			require.Equal(t, "routing", phase)
			require.True(t, limited)
			require.Equal(t, "platform", owner)
			require.Equal(t, "gateway", source)
		})
	}
}

func TestRuntimeRPMForwardExhaustionAttribution(t *testing.T) {
	for _, anthropic := range []bool{false, true} {
		t.Run(strconv.FormatBool(anthropic), func(t *testing.T) {
			c, w := newGinContextForEndpoint(t, EndpointResponses)
			h := &OpenAIGatewayHandler{}
			require.True(t, h.handleOpenAIRPMForwardError(c, service.ErrOpenAIRPMExhausted, false, anthropic))
			classification := classifySelectionFailureError(service.ErrOpenAIRPMExhausted, noAccountErrorClassification{})
			phase, limited, owner, source := classifyOpsErrorLog(c, classification.ErrType, classification.Message, "", w.Code)
			t.Logf("OBSERVED forward_rpm_exhausted anthropic=%t status=%d phase=%s business_limited=%t owner=%s source=%s", anthropic, w.Code, phase, limited, owner, source)
			require.Equal(t, http.StatusTooManyRequests, w.Code)
			require.NotEmpty(t, w.Header().Get("Retry-After"))
			require.Equal(t, "routing", phase)
			require.True(t, limited)
			require.Equal(t, "gateway", source)
		})
	}
}

func TestRuntimeRPMDoesNotMaskOtherFailures(t *testing.T) {
	for _, failure := range []error{nil, errors.New("upstream rate limit"), service.ErrOpenAIRPMUnavailable} {
		c, w := newGinContextForEndpoint(t, EndpointResponses)
		admission := openAIRPMAdmission{exhausted: true}
		admission.retryAfter(c, failure)
		require.False(t, isOpsRoutingCapacityLimited(c))
		require.Empty(t, w.Header().Get("Retry-After"))
	}
	c, _ := newGinContextForEndpoint(t, EndpointResponses)
	c.Set(service.OpsUpstreamStatusCodeKey, http.StatusTooManyRequests)
	phase, limited, _, source := classifyOpsErrorLog(c, "rate_limit_error", "upstream rate limit", "", http.StatusTooManyRequests)
	t.Logf("OBSERVED actual_upstream_429 phase=%s business_limited=%t source=%s", phase, limited, source)
	require.Equal(t, "upstream", phase)
	require.False(t, limited)
	require.Equal(t, "upstream_http", source)
}
