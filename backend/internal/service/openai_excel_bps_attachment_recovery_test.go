package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestExcelBPSAttachmentRecoveryReleasesOwnedLeaseBeforeAcquire(t *testing.T) {
	for _, failure := range []string{"http", "eof", "transport"} {
		t.Run(failure, func(t *testing.T) {
			svc := openAIClientToolsTestService(nil)
			enableNativeAttachments(svc)
			account := excelAccount()
			account.Extra["openai_excel_bps_mihomo"] = true
			var leases []*bpsTestLease
			acquire := func(context.Context, string, ...string) (string, excelBPSLease, error) {
				if len(leases) > 0 {
					require.Equal(t, 1, leases[len(leases)-1].releases, "the real attachment owner must release before the retry reacquires its single-slot exit")
				}
				lease := &bpsTestLease{}
				leases = append(leases, lease)
				return "http://127.0.0.1:19000", lease, nil
			}
			uploads, responses := 0, 0
			svc.httpUpstream = &nativeAttachmentUpstream{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
				require.Zero(t, leases[len(leases)-1].releases, "a live request retains the actual exit lease")
				if req.URL.String() == basispoints.AttachmentsURL {
					uploads++
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"openai_file_id":"file-lease-recovery"}`))}, nil
				}
				responses++
				if responses == 1 {
					switch failure {
					case "http":
						return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
					case "eof":
						return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
					case "transport":
						return nil, errors.New("temporary connection reset")
					}
				}
				wire := incidentBPSFrame("response.completed", map[string]any{"response": map[string]any{"id": "resp_attachment_recovered", "status": "completed", "model": "gpt-6-astra", "output": []any{}}})
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}, nil
			}}
			body, _ := nativeGatewayBody(t)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			c.Request.Header.Set("session_id", "attachment-lease-"+failure)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			timeouts := config.ExcelBPSTimeoutConfig{MaxAttempts: 2, RecoveryInitialDelayMilliseconds: 1, RecoveryBudgetSeconds: 2}
			_, err := svc.forwardExcelBPSAttemptWithAcquire(ctx, c, account, body, time.Now(), timeouts, 0, newExcelBPSRecovery(timeouts), acquire)
			require.NoError(t, err)
			require.Contains(t, recorder.Body.String(), "resp_attachment_recovered")
			require.Equal(t, 1, uploads, "the retry reuses the completed attachment upload")
			require.Equal(t, 2, responses)
			require.Len(t, leases, 2)
			for _, lease := range leases {
				require.Equal(t, 1, lease.releases, "recursion and deferred cleanup must release ownership only once")
			}
		})
	}
}
