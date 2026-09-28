package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// One state is shared by HTTP recovery, stream regeneration and tool repair.
// Its clock starts at the first failure, so a healthy long reasoning request is
// unaffected. A retried attempt inherits the remaining recovery deadline.
type excelBPSRecovery struct {
	mu           sync.Mutex
	remaining    int
	retries      int
	deadline     time.Time
	budget       time.Duration
	initialDelay time.Duration
	maxDelay     time.Duration
}

func newExcelBPSRecovery(c config.ExcelBPSTimeoutConfig) *excelBPSRecovery {
	maxAttempts := c.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	budget := time.Duration(c.RecoveryBudgetSeconds) * time.Second
	if budget <= 0 {
		budget = 30 * time.Second
	}
	initialDelay := time.Duration(c.RecoveryInitialDelayMilliseconds) * time.Millisecond
	if initialDelay <= 0 {
		initialDelay = 250 * time.Millisecond
	}
	maxDelay := time.Duration(c.RecoveryMaxDelaySeconds) * time.Second
	if maxDelay <= 0 {
		maxDelay = 5 * time.Second
	}
	if maxDelay < initialDelay {
		maxDelay = initialDelay
	}
	return &excelBPSRecovery{remaining: maxAttempts - 1, budget: budget, initialDelay: initialDelay, maxDelay: maxDelay}
}

func (r *excelBPSRecovery) consumeRepair() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Tool/protocol repair is another outbound attempt. Charge it to the same
	// envelope as HTTP and stream retries so MaxAttempts bounds every send.
	if r.remaining <= 0 {
		return false
	}
	now := time.Now()
	if r.deadline.IsZero() {
		r.deadline = now.Add(r.budget)
	}
	if !now.Before(r.deadline) {
		return false
	}
	r.remaining--
	return true
}

// withDeadline is used after a repair reservation. Keep this context alive
// until the returned response body is consumed or closed.
func (r *excelBPSRecovery) withDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	r.mu.Lock()
	deadline := r.deadline
	r.mu.Unlock()
	return context.WithDeadlineCause(ctx, deadline, errExcelBPSRequestTimeout)
}

type excelBPSRecoveryBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *excelBPSRecoveryBody) Close() error {
	b.cancel()
	return b.ReadCloser.Close()
}

// reserve bounds provider Retry-After values by the configured recovery delay
// and total budget. A delay outside those bounds declines recovery and is
// preserved on the final error response.
func (r *excelBPSRecovery) reserve(now time.Time, minimumDelay time.Duration) (time.Duration, time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.remaining <= 0 {
		return 0, time.Time{}, false
	}
	if r.deadline.IsZero() {
		r.deadline = now.Add(r.budget)
	}
	delay := r.initialDelay
	for i := 0; i < r.retries && delay < r.maxDelay; i++ {
		delay *= 2
	}
	if delay > r.maxDelay {
		delay = r.maxDelay
	}
	if minimumDelay > delay {
		delay = minimumDelay
	}
	if delay > r.maxDelay || !now.Add(delay).Before(r.deadline) {
		return 0, time.Time{}, false
	}
	r.remaining--
	r.retries++
	return delay, r.deadline, true
}

func (r *excelBPSRecovery) begin(ctx context.Context, minimumDelay time.Duration) (context.Context, context.CancelFunc, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, false, err
	}
	delay, deadline, ok := r.reserve(time.Now(), minimumDelay)
	if !ok {
		return nil, nil, false, nil
	}
	retryCtx, cancel := context.WithDeadlineCause(ctx, deadline, errExcelBPSRequestTimeout)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-retryCtx.Done():
		return retryCtx, cancel, true, retryCtx.Err()
	case <-timer.C:
		return retryCtx, cancel, true, nil
	}
}

func excelBPSRetryableStreamError(err error) bool {
	if err == nil || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) || errors.Is(err, errOpenAISSEIdle) || errors.Is(err, errOpenAISSEFirstOutput) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError)
}

func excelBPSRetryableTransportError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, errExcelBPSProxyUnavailable) {
		return false
	}
	if IsOpenAIRPMError(err) {
		return false
	}
	// The caller checks its own context separately: a transport deadline can be
	// a recoverable response-header timeout while the request remains active.
	return true
}

// Preserve a provider's valid backoff on a terminal rejection, so clients do
// not immediately hammer an overloaded endpoint after our retry budget ends.
func excelBPSRetryAfterHeader(value string) string {
	value = strings.TrimSpace(value)
	if _, err := strconv.ParseUint(value, 10, 32); err == nil {
		return value
	}
	if _, err := http.ParseTime(value); err == nil {
		return value
	}
	return ""
}

// Long/malformed Retry-After values are not used for recovery. The shared
// recovery state applies the configured maximum delay and time budget.
func excelBPSHTTPRetryDelay(status int, retryAfter string, now time.Time) (time.Duration, bool) {
	if status != http.StatusRequestTimeout && status != http.StatusInternalServerError && status != http.StatusBadGateway && status != http.StatusServiceUnavailable && status != http.StatusGatewayTimeout {
		return 0, false
	}
	if retryAfter = strings.TrimSpace(retryAfter); retryAfter == "" {
		return 250 * time.Millisecond, true
	}
	if seconds, err := strconv.ParseUint(retryAfter, 10, 32); err == nil {
		delay := time.Duration(seconds) * time.Second
		if delay < 250*time.Millisecond {
			delay = 250 * time.Millisecond
		}
		return delay, true
	}
	if deadline, err := http.ParseTime(retryAfter); err == nil {
		delay := deadline.Sub(now)
		if delay < 250*time.Millisecond {
			delay = 250 * time.Millisecond
		}
		return delay, true
	}
	return 0, false
}

// These errors arise in local transport translation before client dispatch.
// Never regenerate provider refusals, structured answers or arbitrary failures.
func excelBPSCorrectableProtocolError(message string) bool {
	if strings.HasPrefix(message, "basispoints tool transport correction failed: basispoints source regeneration required:") {
		return true
	}
	for _, prefix := range []string{
		"basispoints function code transport requires an exact catalog function",
		"basispoints function code transport requires string code",
		"basispoints function code transport extended_summary must contain",
		"basispoints raw transport requires a declared custom tool",
		"basispoints returned a tool outside the client's catalog",
		"basispoints custom tool input must be a string",
		"basispoints direct custom tool input must be a string",
		"basispoints direct custom function wrapper requires one string input",
		"basispoints returned an unsupported native tool; no tool was executed",
	} {
		if strings.HasPrefix(message, prefix) {
			return true
		}
	}
	return strings.HasPrefix(message, "basispoints tool transport code must contain one JSON client-tool envelope;") &&
		(strings.Contains(message, "format=json_object;") || strings.Contains(message, "format=json_string;") || strings.Contains(message, "format=text_or_code;") || strings.Contains(message, "format=markdown;")) &&
		strings.Contains(message, "json_failure=") && !strings.Contains(message, "json_failure=trailing_data")
}

// A broken JSON-shaped wrapper has no operation to preserve. Let the existing
// one-shot pre-output request regeneration handle it before spending the shared
// budget on a continuation that would have to invent an operation.
func excelBPSSourceRegenerationRequired(response map[string]any) bool {
	output, _ := response["output"].([]any)
	for _, raw := range output {
		item, _ := raw.(map[string]any)
		if item["name"] != "run_officejs" && item["name"] != "functions.run_officejs" {
			continue
		}
		var arguments map[string]any
		switch value := item["arguments"].(type) {
		case string:
			if json.Unmarshal([]byte(value), &arguments) != nil {
				continue
			}
		case map[string]any:
			arguments = value
		}
		code, _ := arguments["code"].(string)
		code = strings.TrimSpace(code)
		if strings.HasPrefix(code, "{") && !json.Valid([]byte(code)) {
			return true
		}
	}
	return false
}
