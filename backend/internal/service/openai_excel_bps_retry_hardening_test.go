//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
)

func TestBPSRecoveryHardeningHTTP2Classification(t *testing.T) {
	for _, code := range []http2.ErrCode{
		http2.ErrCodeNo, http2.ErrCodeProtocol, http2.ErrCodeInternal,
		http2.ErrCodeFlowControl, http2.ErrCodeSettingsTimeout, http2.ErrCodeStreamClosed,
		http2.ErrCodeFrameSize, http2.ErrCodeRefusedStream, http2.ErrCodeCancel,
		http2.ErrCodeCompression, http2.ErrCodeConnect, http2.ErrCodeEnhanceYourCalm,
		http2.ErrCodeInadequateSecurity, http2.ErrCodeHTTP11Required, http2.ErrCode(255),
	} {
		stream := http2.StreamError{StreamID: 1, Code: code}
		goaway := http2.GoAwayError{LastStreamID: 1, ErrCode: code}
		for _, kind := range []struct {
			name           string
			value, pointer error
			want           bool
		}{
			{"stream", stream, &stream, code == http2.ErrCodeRefusedStream || code == http2.ErrCodeInternal},
			{"goaway", goaway, &goaway, code == http2.ErrCodeNo || code == http2.ErrCodeInternal},
		} {
			for _, form := range []struct {
				name string
				err  error
			}{
				{"value", kind.value}, {"pointer", kind.pointer},
				{"wrapped_value", fmt.Errorf("read failed: %w", kind.value)},
				{"wrapped_pointer", fmt.Errorf("read failed: %w", kind.pointer)},
			} {
				t.Run(kind.name+"/"+code.String()+"/"+form.name, func(t *testing.T) {
					got := excelBPSRetryableStreamError(form.err)
					t.Logf("OBSERVED error_type=%T retry=%v", form.err, got)
					require.Equal(t, kind.want, got)
				})
			}
		}
	}
}

func TestBPSRecoveryHardeningCancellationAndNetworkClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, true}, {"EOF", io.EOF, true}, {"unexpected_EOF", io.ErrUnexpectedEOF, true},
		{"reset", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, true},
		{"attempt_deadline", context.DeadlineExceeded, true},
		{"cancel", context.Canceled, false},
		{"wrapped_cancel", &net.OpError{Op: "read", Net: "tcp", Err: context.Canceled}, false},
		{"joined_cancel_EOF", errors.Join(context.Canceled, io.EOF), false},
		{"stream_cancel_cause", http2.StreamError{Code: http2.ErrCodeInternal, Cause: context.Canceled}, false},
		{"stream_wrapped_cancel_cause", &http2.StreamError{Code: http2.ErrCodeRefusedStream, Cause: fmt.Errorf("stopped: %w", context.Canceled)}, false},
		{"protocol_in_network_wrapper", &net.OpError{Op: "read", Net: "tcp", Err: http2.StreamError{Code: http2.ErrCodeProtocol}}, false},
		{"joined_protocol_EOF", errors.Join(http2.StreamError{Code: http2.ErrCodeProtocol}, io.EOF), false},
		{"plain_text_is_not_a_type", errors.New("stream error: stream ID 1; INTERNAL_ERROR"), false},
		{"arbitrary_error", errors.New("failed"), false},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, excelBPSRetryableStreamError(tc.err)) })
	}
}

type bpsHardeningFaultReader struct {
	err         error
	beforeError func()
}

func (r *bpsHardeningFaultReader) Read([]byte) (int, error) {
	if r.beforeError != nil {
		r.beforeError()
		r.beforeError = nil
	}
	return 0, r.err
}

func TestBPSRecoveryHardeningHTTP2Forward(t *testing.T) {
	metadata := incidentBPSFrame("response.created", map[string]any{"response": map[string]any{"id": "resp_attempt_failed"}})
	text := incidentBPSFrame("response.output_text.delta", map[string]any{"delta": "already delivered"})
	for _, tc := range []struct {
		name    string
		fault   error
		visible bool
		calls   int
	}{
		{"internal_before_output", http2.StreamError{StreamID: 1, Code: http2.ErrCodeInternal}, false, 2},
		{"wrapped_pointer_before_output", fmt.Errorf("read: %w", &http2.StreamError{StreamID: 1, Code: http2.ErrCodeInternal}), false, 2},
		{"refused_before_output", http2.StreamError{StreamID: 1, Code: http2.ErrCodeRefusedStream}, false, 2},
		{"graceful_goaway_before_output", http2.GoAwayError{LastStreamID: 1, ErrCode: http2.ErrCodeNo}, false, 2},
		{"internal_goaway_before_output", fmt.Errorf("read: %w", &http2.GoAwayError{LastStreamID: 1, ErrCode: http2.ErrCodeInternal}), false, 2},
		{"cancel_before_output", http2.StreamError{StreamID: 1, Code: http2.ErrCodeCancel}, false, 1},
		{"protocol_before_output", http2.StreamError{StreamID: 1, Code: http2.ErrCodeProtocol}, false, 1},
		{"protocol_goaway_before_output", http2.GoAwayError{LastStreamID: 1, ErrCode: http2.ErrCodeProtocol}, false, 1},
		{"internal_after_text", http2.StreamError{StreamID: 1, Code: http2.ErrCodeInternal}, true, 1},
		{"refused_after_text", http2.StreamError{StreamID: 1, Code: http2.ErrCodeRefusedStream}, true, 1},
		{"goaway_after_text", http2.GoAwayError{LastStreamID: 1, ErrCode: http2.ErrCodeNo}, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefix := metadata
			if tc.visible {
				prefix += text
			}
			first := &passthroughCloseTrackingReadCloser{Reader: io.MultiReader(strings.NewReader(prefix), &bpsHardeningFaultReader{err: tc.fault})}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				{StatusCode: 200, Header: http.Header{}, Body: first},
				{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(bpsProviderRecoverySuccess()))},
			}}
			svc := openAIClientToolsTestService(upstream)
			svc.cfg.Gateway.ExcelBPSTimeouts.MaxAttempts = 3
			svc.cfg.Gateway.ExcelBPSTimeouts.RecoveryInitialDelayMilliseconds = 1
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			_, err := svc.Forward(context.Background(), c, excelAccount(), []byte(`{"model":"gpt-6-sol","stream":true,"input":"test"}`))
			t.Logf("OBSERVED fault=%T visible=%v sends=%d error=%v", tc.fault, tc.visible, len(upstream.requests), err)
			require.Len(t, upstream.requests, tc.calls)
			require.True(t, first.closed)
			if tc.calls == 2 {
				require.NoError(t, err)
				require.Contains(t, rec.Body.String(), "resp_recovered")
				require.NotContains(t, rec.Body.String(), "resp_attempt_failed")
			} else {
				require.Error(t, err)
				require.NotContains(t, rec.Body.String(), "resp_recovered")
			}
			if tc.visible {
				require.Equal(t, 1, strings.Count(rec.Body.String(), "already delivered"))
			}
		})
	}
}

func TestBPSRecoveryHardeningBudgetAndRPM(t *testing.T) {
	for _, tc := range []struct {
		name             string
		limit, wantCalls int
		succeed, cancel  bool
		budget           time.Duration
	}{
		{"third_send_recovers", 100, 3, true, false, 30 * time.Second},
		{"three_total_not_three_retries", 100, 3, false, false, 30 * time.Second},
		{"RPM_hard_ceiling", 2, 2, false, false, 30 * time.Second},
		{"recovery_budget_too_short", 100, 1, false, false, time.Millisecond},
		{"client_cancel", 100, 1, false, true, 30 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			account := excelAccount()
			account.Extra["base_rpm"] = tc.limit
			rpm := &openAIRPMTestCache{counts: map[int64]int{}}
			svc := openAIClientToolsTestService(nil)
			svc.rpmCache = rpm
			calls := 0
			svc.httpUpstream = &nativeAttachmentUpstream{do: func(*http.Request, string, int64, int) (*http.Response, error) {
				calls++
				if calls == 3 && tc.succeed {
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(bpsProviderRecoverySuccess()))}, nil
				}
				fault := &bpsHardeningFaultReader{err: http2.StreamError{StreamID: 1, Code: http2.ErrCodeInternal}}
				if tc.cancel {
					fault.beforeError = cancel
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(fault)}, nil
			}}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
			timeouts := config.ExcelBPSTimeoutConfig{MaxAttempts: 3, RecoveryInitialDelayMilliseconds: 5}
			recovery := newExcelBPSRecovery(timeouts)
			recovery.budget = tc.budget
			_, err := svc.forwardExcelBPSAttempt(ctx, c, account, []byte(`{"model":"gpt-6-sol","stream":true,"input":"test"}`), time.Now(), timeouts, 0, recovery)
			t.Logf("OBSERVED sends=%d RPM=%d remaining=%d error=%v", calls, rpm.counts[account.ID], recovery.remaining, err)
			require.Equal(t, tc.wantCalls, calls)
			require.Equal(t, tc.wantCalls, rpm.counts[account.ID])
			require.Equal(t, tc.wantCalls, c.GetInt("excel_bps_upstream_attempt"))
			if tc.succeed {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			if tc.cancel {
				require.ErrorIs(t, err, context.Canceled)
			}
		})
	}
}

func TestBPSRecoveryHardeningHTTP2TransportClassification(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		retry bool
	}{
		{"refused", http2.StreamError{Code: http2.ErrCodeRefusedStream}, true},
		{"internal", &http2.StreamError{Code: http2.ErrCodeInternal}, true},
		{"protocol", http2.StreamError{Code: http2.ErrCodeProtocol}, false},
		{"cancel", &http2.StreamError{Code: http2.ErrCodeCancel}, false},
		{"cancel_cause", http2.StreamError{Code: http2.ErrCodeInternal, Cause: context.Canceled}, false},
		{"goaway_no_error", http2.GoAwayError{ErrCode: http2.ErrCodeNo}, true},
		{"goaway_protocol", &http2.GoAwayError{ErrCode: http2.ErrCodeProtocol}, false},
		{"joined_cancel", errors.Join(io.EOF, context.Canceled), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.retry, excelBPSRetryableTransportError(tc.err)) })
	}
}
