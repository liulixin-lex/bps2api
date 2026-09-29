package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

type excelBPSKeepaliveContextKey struct{}

// Only the request goroutine writes this state or the downstream writer.
// Converter repair goroutines explicitly remove the context value; their
// scanner's request goroutine already sends keepalives while they work.
type excelBPSKeepalive struct {
	c        *gin.Context
	next     time.Time
	interval time.Duration
}

func excelBPSWithKeepalive(ctx context.Context, c *gin.Context, stream bool) context.Context {
	if !stream || isOpenAIResponsesCompactPath(c) {
		return ctx
	}
	if existing, ok := c.Get("excel_bps_request_keepalive"); ok {
		if state, ok := existing.(*excelBPSKeepalive); ok {
			return context.WithValue(ctx, excelBPSKeepaliveContextKey{}, state)
		}
	}
	state := &excelBPSKeepalive{c: c, interval: 15 * time.Second, next: time.Now().Add(15 * time.Second)}
	c.Set("excel_bps_request_keepalive", state)
	return context.WithValue(ctx, excelBPSKeepaliveContextKey{}, state)
}

func excelBPSWithoutKeepalive(ctx context.Context) context.Context {
	return context.WithValue(ctx, excelBPSKeepaliveContextKey{}, (*excelBPSKeepalive)(nil))
}

func excelBPSKeepaliveState(ctx context.Context) *excelBPSKeepalive {
	state, _ := ctx.Value(excelBPSKeepaliveContextKey{}).(*excelBPSKeepalive)
	return state
}

func excelBPSKeepaliveDelay(ctx context.Context) time.Duration {
	if state := excelBPSKeepaliveState(ctx); state != nil {
		if delay := time.Until(state.next); delay > 0 {
			return delay
		}
		return time.Nanosecond
	}
	return 15 * time.Second
}

func excelBPSWriteKeepalive(ctx context.Context) error {
	state := excelBPSKeepaliveState(ctx)
	if state == nil || time.Now().Before(state.next) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	state.c.Header("Content-Type", "text/event-stream")
	state.c.Header("Cache-Control", "no-cache")
	state.c.Header("X-Accel-Buffering", "no")
	if _, err := io.WriteString(state.c.Writer, ": keepalive\n\n"); err != nil {
		return err
	}
	state.c.Writer.Flush()
	state.next = time.Now().Add(state.interval)
	return nil
}

// Backoff and quota waits do not spend another send. A caller-owned deadline
// bounds the wait; only this goroutine may emit optional SSE comments.
func excelBPSWaitRecoveryDelay(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	done := time.NewTimer(delay)
	defer done.Stop()
	if excelBPSKeepaliveState(ctx) == nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done.C:
			return nil
		}
	}
	heartbeat := time.NewTimer(excelBPSKeepaliveDelay(ctx))
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done.C:
			return nil
		case <-heartbeat.C:
			if err := excelBPSWriteKeepalive(ctx); err != nil {
				return err
			}
			heartbeat.Reset(excelBPSKeepaliveDelay(ctx))
		}
	}
}

// Worker owns only upstream Do. It never accesses Gin or a downstream writer.
// An unbuffered handoff closes late bodies if cancellation wins the race.
func excelBPSDoWithKeepalive(ctx context.Context, req *http.Request, do func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if excelBPSKeepaliveState(ctx) == nil {
		return do(req)
	}
	workerCtx, cancel := context.WithCancel(req.Context())
	type result struct {
		response *http.Response
		err      error
	}
	ready := make(chan result)
	go func() {
		response, err := do(req.Clone(workerCtx))
		select {
		case ready <- result{response, err}:
		case <-workerCtx.Done():
			if response != nil && response.Body != nil {
				_ = response.Body.Close()
			}
		}
	}()
	heartbeat := time.NewTimer(excelBPSKeepaliveDelay(ctx))
	defer heartbeat.Stop()
	for {
		select {
		case value := <-ready:
			if err := ctx.Err(); err != nil {
				cancel()
				if value.response != nil && value.response.Body != nil {
					_ = value.response.Body.Close()
				}
				return nil, err
			}
			if value.response != nil && value.response.Body != nil {
				value.response.Body = &excelBPSRecoveryBody{ReadCloser: value.response.Body, cancel: cancel}
			} else {
				cancel()
			}
			return value.response, value.err
		case <-ctx.Done():
			cancel()
			return nil, ctx.Err()
		case <-heartbeat.C:
			if err := excelBPSWriteKeepalive(ctx); err != nil {
				cancel()
				return nil, err
			}
			heartbeat.Reset(excelBPSKeepaliveDelay(ctx))
		}
	}
}

type excelBPSQuotaWaitError struct{ retryAfter string }

func (e *excelBPSQuotaWaitError) Error() string {
	return "excel BPS model quota is cooling down beyond the recovery delay limit"
}

func (s *OpenAIGatewayService) observeExcelBPSProviderFailure(ctx context.Context, account *Account, model string, failure excelBPSProviderFailure) {
	if s != nil && account != nil && !isQualityObservation(ctx) {
		s.excelBPSQuotaGate.observe(account.RPMAccountID(), model, failure, time.Now())
	}
}

// Waiting learns no capacity and consumes neither an attempt nor an RPM slot.
// The caller owns cancel through the actual response body, so a newly started
// recovery budget also bounds the subsequent pre-output send and stream.
func (s *OpenAIGatewayService) excelBPSQuotaSendContext(ctx context.Context, account *Account, model string, recovery *excelBPSRecovery) (context.Context, context.CancelFunc, error) {
	noop := func() {}
	if err := ctx.Err(); err != nil {
		return ctx, noop, err
	}
	if s == nil || account == nil || recovery == nil || isQualityObservation(ctx) {
		return ctx, noop, nil
	}
	delay := s.excelBPSQuotaGate.delay(account.RPMAccountID(), model, time.Now())
	recovery.mu.Lock()
	if delay > 0 && recovery.deadline.IsZero() {
		recovery.deadline = time.Now().Add(recovery.budget)
	}
	active := !recovery.deadline.IsZero()
	deadline := recovery.deadline
	maxDelay := recovery.maxDelay
	recovery.mu.Unlock()
	if !active {
		return ctx, noop, nil
	}
	if !time.Now().Before(deadline) {
		return ctx, noop, errExcelBPSRequestTimeout
	}
	sendCtx, cancel := recovery.withDeadline(ctx)
	for delay > 0 {
		if delay > maxDelay {
			cancel()
			return ctx, noop, &excelBPSQuotaWaitError{retryAfter: strconv.FormatInt(int64((delay+time.Second-1)/time.Second), 10)}
		}
		// First sends waiting on a peer's 429 also need positive jitter. Waiting
		// changes neither the send count nor either recovery cap.
		room := min(delay/4, maxDelay-delay, time.Until(deadline)-delay-time.Nanosecond)
		if room > 0 {
			delay += time.Duration(rand.Int64N(int64(room))) + time.Nanosecond
		}
		if err := excelBPSWaitRecoveryDelay(sendCtx, delay); err != nil {
			cause := context.Cause(sendCtx)
			cancel()
			if errors.Is(cause, errExcelBPSRequestTimeout) {
				return ctx, noop, errExcelBPSRequestTimeout
			}
			return ctx, noop, err
		}
		delay = s.excelBPSQuotaGate.delay(account.RPMAccountID(), model, time.Now())
	}
	if err := sendCtx.Err(); err != nil {
		cause := context.Cause(sendCtx)
		cancel()
		if errors.Is(cause, errExcelBPSRequestTimeout) {
			return ctx, noop, errExcelBPSRequestTimeout
		}
		return ctx, noop, err
	}
	return sendCtx, cancel, nil
}

func excelBPSPreserveSendGateError(err error) error {
	var quota *excelBPSQuotaWaitError
	if errors.As(err, &quota) {
		return &excelBPSProviderRetryError{modelScoped: true, failure: excelBPSProviderFailure{status: http.StatusTooManyRequests, retryAfter: quota.retryAfter}, retryAfter: quota.retryAfter}
	}
	if errors.Is(err, errExcelBPSRequestTimeout) {
		return &excelBPSProviderRetryError{failure: excelBPSProviderFailure{status: http.StatusGatewayTimeout}}
	}
	return err
}

func excelBPSRecoveryBudgetExpired(recovery *excelBPSRecovery) bool {
	if recovery == nil {
		return false
	}
	recovery.mu.Lock()
	defer recovery.mu.Unlock()
	return !recovery.deadline.IsZero() && !time.Now().Before(recovery.deadline) && len(recovery.timers) > 0
}

func excelBPSRecoveryDiagnostics(recovery *excelBPSRecovery, outputCommitted bool, terminal string, err error) string {
	switch terminal {
	case "", "error", "response.failed", "response.completed", "response.incomplete":
	default:
		terminal = "other"
	}
	recovery.mu.Lock()
	remaining := recovery.budget
	if !recovery.deadline.IsZero() {
		remaining = time.Until(recovery.deadline)
		if remaining < 0 {
			remaining = 0
		}
	}
	detail, _ := json.Marshal(map[string]any{"output_committed": outputCommitted, "remaining_attempts": recovery.remaining, "budget_remaining_ms": remaining.Milliseconds(), "terminal": terminal, "error_type": fmt.Sprintf("%T", err)})
	recovery.mu.Unlock()
	return string(detail)
}

// All BPS Responses sends, including converter repairs, pass this final gate.
// RPM and the attempt diagnostic are charged only when Do will actually start.
func (s *OpenAIGatewayService) sendExcelBPSWithQuota(c *gin.Context, account *Account, model string, req *http.Request, proxy string, recovery *excelBPSRecovery) (*http.Response, error) {
	// The request already descends from the caller context and may add per-send
	// httptrace hooks. Derive from it so the final gate never erases evidence.
	sendCtx, cancel, err := s.excelBPSQuotaSendContext(req.Context(), account, model, recovery)
	if err != nil {
		return nil, err
	}
	handedOff := false
	defer func() {
		if !handedOff {
			cancel()
		}
	}()
	if err := s.acquireOpenAIRPMForSend(sendCtx, account); err != nil {
		return nil, err
	}
	c.Set("excel_bps_upstream_attempt", c.GetInt("excel_bps_upstream_attempt")+1)
	response, err := excelBPSDoWithKeepalive(sendCtx, req.WithContext(sendCtx), func(outbound *http.Request) (*http.Response, error) {
		return s.httpUpstream.Do(outbound, proxy, account.ID, account.Concurrency)
	})
	if err != nil && errors.Is(context.Cause(sendCtx), errExcelBPSRequestTimeout) {
		err = errExcelBPSRequestTimeout
	}
	if err == nil && response != nil && response.Body != nil {
		response.Body = &excelBPSRecoveryBody{ReadCloser: response.Body, cancel: cancel}
		handedOff = true
	}
	return response, err
}
