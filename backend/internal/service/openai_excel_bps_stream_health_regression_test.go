package service

import (
	"context"
	"errors"
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
	"golang.org/x/net/http2"
)

type bpsAuditStreamLease struct {
	releases, streamFailures, connectFailures, upstreamFailures, successes int
}

func (l *bpsAuditStreamLease) Release()               { l.releases++ }
func (l *bpsAuditStreamLease) ReportStreamFailure()   { l.streamFailures++ }
func (l *bpsAuditStreamLease) ReportFailure()         { l.connectFailures++ }
func (l *bpsAuditStreamLease) ReportUpstreamFailure() { l.upstreamFailures++ }
func (l *bpsAuditStreamLease) ReportSuccess()         { l.successes++ }

type bpsAuditErrorReader struct{ err error }

func (b bpsAuditErrorReader) Read([]byte) (int, error) { return 0, b.err }

func TestBPSAuditStreamFailureInvalidatesExitBeforeRecovery(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, failure := range []struct {
			name string
			err  error
		}{
			{"clean_eof", io.EOF},
			{"unexpected_eof", io.ErrUnexpectedEOF},
			{"http2_internal", http2.StreamError{StreamID: 1, Code: http2.ErrCodeInternal}},
		} {
			t.Run(fmt.Sprintf("%s/stream_%t", failure.name, stream), func(t *testing.T) {
				svc := openAIClientToolsTestService(nil)
				account := excelAccount()
				account.Extra["openai_excel_bps_mihomo"] = true
				bad, good := &bpsAuditStreamLease{}, &bpsAuditStreamLease{}
				acquire := func(context.Context, string, ...string) (string, excelBPSLease, error) {
					if bad.streamFailures == 0 {
						return "http://127.0.0.1:19000", bad, nil
					}
					require.Equal(t, 1, bad.releases, "failed owner must release before replacement acquisition")
					return "http://127.0.0.1:19001", good, nil
				}
				calls := 0
				svc.httpUpstream = &bpsTestUpstream{send: func(req *http.Request, proxy string) (*http.Response, error) {
					calls++
					_ = req.Body.Close()
					var body io.Reader = bpsAuditErrorReader{err: failure.err}
					if proxy == "http://127.0.0.1:19001" {
						body = strings.NewReader(bpsProviderRecoverySuccess())
					}
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(body)}, nil
				}}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
				timeouts := config.ExcelBPSTimeoutConfig{MaxAttempts: 3, RecoveryInitialDelayMilliseconds: 1}
				body := []byte(fmt.Sprintf(`{"model":"gpt-6-sol","stream":%t,"input":"test"}`, stream))
				result, err := svc.forwardExcelBPSAttemptWithAcquire(context.Background(), c, account, body, time.Now(), timeouts, 0, newExcelBPSRecovery(timeouts), acquire)
				t.Logf("calls=%d bad_stream_failures=%d bad_releases=%d good_successes=%d error=%v", calls, bad.streamFailures, bad.releases, good.successes, err)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, 2, calls)
				require.Equal(t, 1, bad.streamFailures)
				require.Equal(t, 1, bad.releases)
				require.Equal(t, 1, good.releases)
				require.Equal(t, 1, good.successes)
			})
		}
	}
}

func TestBPSAuditStreamHealthBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, wire   string
		readErr      error
		cancel       bool
		wantFailures int
	}{
		{name: "exhausted_clean_eof", readErr: io.EOF, wantFailures: 1},
		{name: "after_visible_text", wire: incidentBPSFrame("response.output_text.delta", map[string]any{"delta": "visible"}), wantFailures: 1},
		{name: "provider_throttle", wire: bpsProviderRecoveryFailure("error", "rate_limit_error")},
		{name: "provider_permission", wire: bpsProviderRecoveryFailure("response.failed", "permission_denied")},
		{name: "provider_quota", wire: bpsProviderRecoveryFailure("response.failed", "insufficient_quota")},
		{name: "client_cancel", cancel: true},
		{name: "first_output_timeout", readErr: errOpenAISSEFirstOutput},
		{name: "idle_timeout", readErr: errOpenAISSEIdle},
		{name: "transport_deadline", readErr: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := openAIClientToolsTestService(nil)
			account := excelAccount()
			account.Extra["openai_excel_bps_mihomo"] = true
			lease := &bpsAuditStreamLease{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			acquire := func(context.Context, string, ...string) (string, excelBPSLease, error) {
				return "http://127.0.0.1:19000", lease, nil
			}
			calls := 0
			svc.httpUpstream = &bpsTestUpstream{send: func(req *http.Request, _ string) (*http.Response, error) {
				calls++
				_ = req.Body.Close()
					body := io.NopCloser(strings.NewReader(tc.wire))
				if tc.readErr != nil {
					body = io.NopCloser(bpsAuditErrorReader{err: tc.readErr})
				}
				if tc.cancel {
					body = &bpsCancelBody{cancel: cancel}
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: body}, nil
			}}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
			timeouts := config.ExcelBPSTimeoutConfig{MaxAttempts: 1, RecoveryInitialDelayMilliseconds: 1}
			_, err := svc.forwardExcelBPSAttemptWithAcquire(ctx, c, account, []byte(`{"model":"gpt-6-sol","stream":true,"input":"test"}`), time.Now(), timeouts, 0, newExcelBPSRecovery(timeouts), acquire)
			t.Logf("calls=%d stream_failures=%d connect_failures=%d upstream_failures=%d releases=%d error=%v", calls, lease.streamFailures, lease.connectFailures, lease.upstreamFailures, lease.releases, err)
			require.Error(t, err)
			require.Equal(t, 1, calls)
			require.Equal(t, tc.wantFailures, lease.streamFailures)
			require.Zero(t, lease.connectFailures)
			require.Zero(t, lease.upstreamFailures)
			require.Equal(t, 1, lease.releases)
			if tc.cancel {
				require.True(t, errors.Is(err, context.Canceled))
			}
		})
	}
}
