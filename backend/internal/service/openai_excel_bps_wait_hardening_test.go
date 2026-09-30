//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type bpsRecoveryHardeningWaitWriter struct {
	*httptest.ResponseRecorder
	onFlush func()
}

func (w *bpsRecoveryHardeningWaitWriter) Flush() {
	w.ResponseRecorder.Flush()
	if w.onFlush != nil {
		w.onFlush()
	}
}

type bpsRecoveryHardeningWaitBody struct {
	io.Reader
	closed chan struct{}
	once   sync.Once
}

func (b *bpsRecoveryHardeningWaitBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

func bpsRecoveryHardeningWaitContext(ctx context.Context, onFlush func()) (context.Context, *gin.Context, *bpsRecoveryHardeningWaitWriter) {
	w := &bpsRecoveryHardeningWaitWriter{ResponseRecorder: httptest.NewRecorder(), onFlush: onFlush}
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	state := &excelBPSKeepalive{c: c, next: time.Now().Add(-time.Second), interval: time.Hour}
	c.Set("excel_bps_request_keepalive", state)
	return excelBPSWithKeepalive(ctx, c, true), c, w
}

func TestBPSRecoveryHardeningWaitCancellation(t *testing.T) {
	for _, delay := range []time.Duration{0, time.Hour} {
		t.Run(delay.String(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			ctx, _, w := bpsRecoveryHardeningWaitContext(ctx, nil)
			cancel()
			require.ErrorIs(t, excelBPSWaitRecoveryDelay(ctx, delay), context.Canceled)
			require.Empty(t, w.Body.String())
			require.False(t, w.Flushed)
		})
	}
}

func TestBPSRecoveryHardeningWaitHeartbeatDue(t *testing.T) {
	for _, operation := range []string{"delay", "upstream"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			ctx, c, w := bpsRecoveryHardeningWaitContext(ctx, cancel)
			state := excelBPSKeepaliveState(ctx)
			before := time.Now()
			var err error
			if operation == "delay" {
				err = excelBPSWaitRecoveryDelay(ctx, time.Hour)
			} else {
				finished := make(chan struct{})
				_, err = excelBPSDoWithKeepalive(ctx, c.Request, func(req *http.Request) (*http.Response, error) {
					defer close(finished)
					<-req.Context().Done()
					return nil, req.Context().Err()
				})
				select {
				case <-finished:
				case <-time.After(2 * time.Second):
					t.Fatal("upstream worker did not stop after heartbeat cancellation")
				}
			}
			require.ErrorIs(t, err, context.Canceled)
			require.Equal(t, ": keepalive\n\n", w.Body.String())
			require.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
			require.Equal(t, "no-cache", w.Header().Get("Cache-Control"))
			require.Equal(t, "no", w.Header().Get("X-Accel-Buffering"))
			require.True(t, w.Flushed)
			require.False(t, state.next.Before(before.Add(state.interval)))
		})
	}
}

func TestBPSRecoveryHardeningWaitNoStateDoesNotWriteSSE(t *testing.T) {
	ctx := context.Background()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request = req
	ctx = excelBPSWithKeepalive(ctx, c, false)
	require.Nil(t, excelBPSKeepaliveState(ctx))
	require.NoError(t, excelBPSWaitRecoveryDelay(ctx, time.Nanosecond))
	expected := &http.Response{StatusCode: http.StatusNoContent}
	called := false
	got, err := excelBPSDoWithKeepalive(ctx, req, func(gotReq *http.Request) (*http.Response, error) {
		called = true
		require.Same(t, req, gotReq, "without state the upstream call remains synchronous")
		return expected, nil
	})
	require.NoError(t, err)
	require.True(t, called)
	require.Same(t, expected, got)
	require.Empty(t, w.Body.String())
	require.Empty(t, w.Header().Get("Content-Type"))
	require.False(t, w.Flushed)
}

func TestBPSRecoveryHardeningWaitWithoutKeepaliveDoesNotWriteSSE(t *testing.T) {
	parent, c, w := bpsRecoveryHardeningWaitContext(context.Background(), nil)
	state := excelBPSKeepaliveState(parent)
	next := state.next
	ctx := excelBPSWithoutKeepalive(parent)
	require.Nil(t, excelBPSKeepaliveState(ctx))
	require.Same(t, state, excelBPSKeepaliveState(parent))
	require.NoError(t, excelBPSWaitRecoveryDelay(ctx, time.Nanosecond))
	require.NoError(t, excelBPSWriteKeepalive(ctx))
	got, err := excelBPSDoWithKeepalive(ctx, c.Request, func(req *http.Request) (*http.Response, error) {
		require.Same(t, c.Request, req)
		return &http.Response{StatusCode: http.StatusNoContent}, nil
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, got.StatusCode)
	require.Equal(t, next, state.next)
	require.Empty(t, w.Body.String())
	require.Empty(t, w.Header().Get("Content-Type"))
	require.False(t, w.Flushed)
}

func TestBPSRecoveryHardeningWaitLateResponseClosesBodyAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx, c, _ := bpsRecoveryHardeningWaitContext(ctx, nil)
	excelBPSKeepaliveState(ctx).next = time.Now().Add(time.Hour)
	started := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	body := &bpsRecoveryHardeningWaitBody{Reader: strings.NewReader("late"), closed: make(chan struct{})}
	cancellerDone := make(chan struct{})
	go func() {
		defer close(cancellerDone)
		select {
		case <-started:
			cancel()
		case <-ctx.Done():
		}
	}()
	response, err := excelBPSDoWithKeepalive(ctx, c.Request, func(req *http.Request) (*http.Response, error) {
		close(started)
		<-release
		return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
	})
	<-cancellerDone
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, response)
	close(release)
	select {
	case <-body.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("response returned after cancellation leaked its body")
	}
}

func TestBPSRecoveryHardeningWaitReturnedBodyContextLivesUntilClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx, c, _ := bpsRecoveryHardeningWaitContext(ctx, nil)
	excelBPSKeepaliveState(ctx).next = time.Now().Add(time.Hour)
	workerContext := make(chan context.Context, 1)
	body := &bpsRecoveryHardeningWaitBody{Reader: strings.NewReader("complete body"), closed: make(chan struct{})}
	response, err := excelBPSDoWithKeepalive(ctx, c.Request, func(req *http.Request) (*http.Response, error) {
		workerContext <- req.Context()
		return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
	})
	require.NoError(t, err)
	require.NotNil(t, response)
	defer response.Body.Close()
	upstream := <-workerContext
	require.NoError(t, upstream.Err(), "returning headers must not cancel response-body reads")
	data, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, "complete body", string(data))
	require.NoError(t, upstream.Err())
	require.NoError(t, response.Body.Close())
	require.ErrorIs(t, upstream.Err(), context.Canceled)
	select {
	case <-body.closed:
	default:
		t.Fatal("closing response did not close the underlying body")
	}
	require.NoError(t, c.Request.Context().Err(), "closing worker must not cancel the original request context")
}

type bpsRecoveryHardeningWaitContextBody struct {
	ctx    context.Context
	closed chan struct{}
	once   sync.Once
}

func (b *bpsRecoveryHardeningWaitContextBody) Read([]byte) (int, error) {
	select {
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	case <-b.closed:
		return 0, io.ErrClosedPipe
	}
}

func (b *bpsRecoveryHardeningWaitContextBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

func TestBPSRecoveryHardeningWaitQuotaBudgetContinuesThroughForward(t *testing.T) {
	for _, phase := range []string{"headers", "body"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			account := excelAccount()
			svc := openAIClientToolsTestService(nil)
			svc.cfg.Gateway.ExcelBPSTimeouts.MaxAttempts = 1
			svc.cfg.Gateway.ExcelBPSTimeouts.RecoveryBudgetSeconds = 1
			var sends atomic.Int32
			outbound := make(chan context.Context, 1)
			workerDone := make(chan struct{})
			body := &bpsRecoveryHardeningWaitContextBody{closed: make(chan struct{})}
			defer body.Close()
			svc.httpUpstream = &nativeAttachmentUpstream{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
				if sends.Add(1) != 1 {
					return nil, context.Canceled
				}
				defer close(workerDone)
				outbound <- req.Context()
				if phase == "headers" {
					<-req.Context().Done()
					return nil, req.Context().Err()
				}
				body.ctx = req.Context()
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}, nil
			}}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			// Seed feedback from a prior rejection without spending this request's
			// only send. The gate must start a budget that survives the wait.
			svc.excelBPSQuotaGate.observe(account.RPMAccountID(), "gpt-6-sol", excelBPSProviderFailure{
				status: http.StatusTooManyRequests, retry: true, delay: 250 * time.Millisecond,
			}, time.Now())
			_, err := svc.Forward(ctx, c, account, []byte("{\"model\":\"gpt-6-sol\",\"stream\":true,\"input\":\"test\"}"))
			t.Logf("OBSERVED quota_budget phase=%s sends=%d status=%d error=%v output=%s", phase, sends.Load(), rec.Code, err, rec.Body.String())
			require.Error(t, err)
			require.Equal(t, int32(1), sends.Load(), "cooldown waits spend no send and timeout must not replay")
			require.Equal(t, http.StatusGatewayTimeout, rec.Code, "the recovery deadline is a 504, not a transport 502")
			require.NoError(t, ctx.Err(), "the recovery deadline must expire before the caller's independent deadline")
			wantCode := "basispoints_request_timeout"
			if phase == "body" {
				wantCode = "basispoints_stream_timeout"
			}
			require.Contains(t, rec.Body.String(), wantCode)
			require.NotContains(t, rec.Body.String(), "basispoints_transport_error")
			select {
			case requestCtx := <-outbound:
				require.Error(t, requestCtx.Err(), "the upstream context must be canceled when the budget expires")
			default:
				t.Fatal("the request never reached the upstream after its cooldown")
			}
			select {
			case <-workerDone:
			case <-time.After(time.Second):
				t.Fatal("upstream worker did not finish after recovery timeout")
			}
			if phase == "body" {
				select {
				case <-body.closed:
				default:
					t.Fatal("timed-out response body was not closed")
				}
			}
		})
	}
}
