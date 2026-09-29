package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// One state is shared by HTTP recovery, stream regeneration and tool repair.
// Its clock starts at the first failure, so a healthy long reasoning request is
// unaffected. Retried attempts share a removable pre-output deadline.
type excelBPSRecovery struct {
	mu           sync.Mutex
	remaining    int
	retries      int
	deadline     time.Time
	budget       time.Duration
	initialDelay time.Duration
	maxDelay     time.Duration
	timers       []*time.Timer
	started      time.Time
	accounts     map[int64]struct{}
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
	return &excelBPSRecovery{remaining: maxAttempts - 1, budget: budget, initialDelay: initialDelay, maxDelay: maxDelay, started: time.Now()}
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

// A recovery deadline bounds pre-output work. Unlike context.WithDeadline, its
// timer can be disarmed once a healthy stream produces semantic output. Parent
// cancellation and the independent total request deadline remain in force.
func (r *excelBPSRecovery) withDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	r.mu.Lock()
	retryCtx, cancel := context.WithCancelCause(ctx)
	timer := time.AfterFunc(time.Until(r.deadline), func() { cancel(errExcelBPSRequestTimeout) })
	r.timers = append(r.timers, timer)
	r.mu.Unlock()
	return retryCtx, func() { timer.Stop(); cancel(context.Canceled) }
}

func (r *excelBPSRecovery) acceptOutput() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, timer := range r.timers {
		timer.Stop()
	}
	r.timers = nil
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
	// Spread simultaneous recoveries while never shortening a provider hint.
	delay += time.Duration(float64(delay) * rand.Float64() / 4)
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

func (r *excelBPSRecovery) begin(ctx context.Context, minimumDelay time.Duration, beforeWait ...func()) (context.Context, context.CancelFunc, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, false, err
	}
	delay, _, ok := r.reserve(time.Now(), minimumDelay)
	if !ok {
		return nil, nil, false, nil
	}
	for _, closeAttempt := range beforeWait {
		closeAttempt()
	}
	retryCtx, cancel := r.withDeadline(ctx)
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
	if delay, ok := excelBPSParseRetryAfter(value, time.Now()); ok {
		return strconv.FormatInt(int64(math.Ceil(delay.Seconds())), 10)
	}
	return ""
}

// Header.Values preserves repeated date-valued headers (commas belong to the
// date itself). A newline is internal-only and is never forwarded downstream.
func excelBPSRetryAfterValue(header http.Header) string {
	return strings.Join(header.Values("Retry-After"), "\n")
}

func excelBPSParseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	var longest time.Duration
	valid := false
	for _, part := range strings.Split(value, "\n") {
		part = strings.TrimSpace(part)
		var delay time.Duration
		if seconds, err := strconv.ParseFloat(part, 64); err == nil && !math.IsInf(seconds, 0) && !math.IsNaN(seconds) && seconds >= 0 && seconds <= math.MaxUint32 {
			delay = time.Duration(seconds * float64(time.Second))
		} else if date, err := http.ParseTime(part); err == nil {
			delay = date.Sub(now)
			if delay < 0 {
				delay = 0
			}
		} else {
			return 0, false
		}
		if delay > longest {
			longest = delay
		}
		valid = true
	}
	return longest, valid
}

// Long/malformed Retry-After values are not used for recovery. The shared
// recovery state applies the configured maximum delay and time budget.
func excelBPSHTTPRetryDelay(status int, retryAfter string, now time.Time) (time.Duration, bool) {
	if status != http.StatusTooManyRequests && status != http.StatusRequestTimeout && status != http.StatusInternalServerError && status != http.StatusBadGateway && status != http.StatusServiceUnavailable && status != http.StatusGatewayTimeout {
		return 0, false
	}
	if retryAfter = strings.TrimSpace(retryAfter); retryAfter == "" {
		return 250 * time.Millisecond, true
	}
	if delay, ok := excelBPSParseRetryAfter(retryAfter, now); ok {
		if delay < 250*time.Millisecond {
			delay = 250 * time.Millisecond
		}
		return delay, true
	}
	return 0, false
}

type excelBPSProviderFailure struct {
	status    int
	code      string
	delay     time.Duration
	retry     bool
	permanent bool
}

// Preserve transient repair failures through the tool bridge without turning
// them into a protocol-repair prompt. Only the outer output guard may replay.
type excelBPSProviderRetryError struct {
	failure      excelBPSProviderFailure
	retryAfter   string
	usagePayload []byte
}

func (e *excelBPSProviderRetryError) Error() string {
	return fmt.Sprintf("excel BPS correction returned HTTP %d", e.failure.status)
}

// Reserve the final send for a known same-model BPS alternative. This only
// consults the existing request group after a failure. Metadata snapshots are
// only candidates: hydrate and recheck them through the ordinary scheduling
// gates before giving up the current account's final recovery send.
func (s *OpenAIGatewayService) excelBPSReserveAccountSwitch(ctx context.Context, c *gin.Context, current *Account, originalModel, upstreamModel string, recovery *excelBPSRecovery) bool {
	recovery.mu.Lock()
	lastSend := recovery.remaining == 1
	excluded := make(map[int64]struct{}, len(recovery.accounts)+1)
	for id := range recovery.accounts {
		excluded[id] = struct{}{}
	}
	excluded[current.ID] = struct{}{}
	recovery.mu.Unlock()
	if !lastSend || (s.accountRepo == nil && s.schedulerSnapshot == nil) {
		return false
	}
	value, ok := c.Get("api_key")
	if !ok {
		return false
	}
	key, ok := value.(*APIKey)
	if !ok || key == nil {
		return false
	}
	accounts, err := s.listSchedulableAccounts(ctx, key.GroupID, PlatformOpenAI)
	if err != nil {
		return false
	}
	req := OpenAIAccountScheduleRequest{
		GroupID: key.GroupID, Platform: PlatformOpenAI, RequestedModel: originalModel,
		RequiredTransport: OpenAIUpstreamTransportAny, RequiredCapability: OpenAIEndpointCapabilityResponses,
		RequireCompact: isOpenAIResponsesCompactPath(c), RequirePrivacySet: s.openAIGroupRequiresPrivacySet(ctx, key.GroupID),
	}
	if excelBPSChatFromContext(ctx) != nil || excelBPSMessagesFromContext(ctx) != nil {
		req.RequiredCapability = OpenAIEndpointCapabilityChatCompletions
	}
	if req.RequireCompact {
		req.RequiredCapability = OpenAIEndpointCapabilityResponsesCompact
	}
	scheduler := &defaultOpenAIAccountScheduler{service: s}
	for i := range accounts {
		candidate := &accounts[i]
		if _, tried := excluded[candidate.ID]; tried {
			continue
		}
		if candidate.Platform != PlatformOpenAI || candidate.Type != AccountTypeOAuth || !candidate.IsSchedulable() {
			continue
		}
		candidate = s.resolveFreshSchedulableOpenAIAccount(ctx, candidate, key.GroupID, PlatformOpenAI, originalModel, req.RequireCompact, req.RequiredCapability)
		if candidate == nil {
			continue
		}
		candidate = s.recheckSelectedOpenAIAccountFromDB(ctx, candidate, key.GroupID, PlatformOpenAI, originalModel, req.RequireCompact, req.RequiredCapability)
		if candidate == nil || candidate.Type != AccountTypeOAuth {
			continue
		}
		if candidate.GetMappedModel(originalModel) != upstreamModel || !candidate.IsExcelBPSConfiguredForUpstreamModel(upstreamModel) {
			continue
		}
		if _, paused := candidate.Extra[OpenAIExcelBPSPausedOn403AtExtraKey]; paused {
			continue
		}
		if compatible, _ := scheduler.isAccountRequestCompatibleReason(ctx, candidate, req); !compatible {
			continue
		}
		if eligible, _, err := s.OpenAIRPMSchedulable(ctx, candidate, false); err != nil || !eligible {
			continue
		}
		return true
	}
	return false
}

var excelBPSRetryHint = regexp.MustCompile(`(?i)(?:try again in|retry after)\s+([0-9]+(?:\.[0-9]+)?)\s*(milliseconds?|ms|seconds?|s)\b`)

// HTTP status is sufficient for transient transport errors; an HTTP 200 SSE
// terminal needs a recognized provider code or explicit error status. Quota,
// credential and policy failures always override a transient status or hint.
func excelBPSClassifyProviderFailure(raw []byte, status int, retryAfter string, now time.Time) excelBPSProviderFailure {
	failure := excelBPSProviderFailure{status: status}
	envelope := gjson.ParseBytes(raw)
	detail := envelope.Get("response.error")
	if !detail.IsObject() {
		detail = envelope.Get("error")
	}
	if !detail.IsObject() {
		detail = envelope
	}
	failure.code = strings.ToLower(strings.TrimSpace(detail.Get("code").String()))
	errorType := strings.ToLower(strings.TrimSpace(detail.Get("type").String()))
	message := strings.ToLower(strings.TrimSpace(detail.Get("message").String()))
	for _, value := range []string{failure.code, errorType} {
		switch value {
		case "insufficient_quota", "quota_exceeded", "usage_limit_reached", "billing_hard_limit_reached", "billing_not_active":
			if failure.status < 400 {
				failure.status = http.StatusTooManyRequests
			}
			failure.permanent = true
		case "invalid_api_key", "token_revoked", "invalid_authentication", "authentication_error":
			if failure.status < 400 {
				failure.status = http.StatusUnauthorized
			}
			failure.permanent = true
		case "permission_denied", "access_denied", "usage_policy", "content_policy_violation", "policy_violation":
			if failure.status < 400 {
				failure.status = http.StatusForbidden
			}
			failure.permanent = true
		}
	}
	for _, prefix := range []string{"you exceeded your current quota", "your quota has been exceeded", "insufficient quota", "insufficient balance", "usage limit reached", "you have reached your usage limit", "usage is not included", "request blocked by usage policy"} {
		if strings.HasPrefix(message, prefix) {
			failure.permanent = true
		}
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		failure.permanent = true
	}
	if failure.permanent {
		return failure
	}
	if status < 400 {
		for _, path := range []string{"status_code", "http_status", "status"} {
			for _, object := range []gjson.Result{detail, envelope} {
				if value := int(object.Get(path).Int()); value >= 400 && value <= 599 {
					failure.status = value
				}
			}
		}
		if failure.status < 400 {
			values := []string{failure.code}
			if failure.code == "" {
				values = append(values, errorType)
			}
			for _, value := range values {
				switch value {
				case "rate_limit_exceeded", "rate_limit_error", "rate_limited":
					failure.status = http.StatusTooManyRequests
				case "server_error", "internal_server_error":
					failure.status = http.StatusInternalServerError
				case "bad_gateway":
					failure.status = http.StatusBadGateway
				case "server_is_overloaded", "overloaded_error", "service_unavailable", "temporarily_unavailable":
					failure.status = http.StatusServiceUnavailable
				case "gateway_timeout", "request_timeout":
					failure.status = http.StatusGatewayTimeout
				}
			}
		}
	}
	if failure.status == http.StatusUnauthorized || failure.status == http.StatusForbidden {
		failure.permanent = true
		return failure
	}
	delay, retry := excelBPSHTTPRetryDelay(failure.status, retryAfter, now)
	if !retry {
		return failure
	}
	// A longer valid hint wins. Values outside the bounded envelope decline
	// recovery instead of being clamped and retried earlier than the provider.
	for _, object := range []gjson.Result{detail, envelope} {
		// Some provider SSE errors carry response headers inside error.headers.
		// Honor their longest delay just like the outer HTTP Retry-After instead
		// of trusting a shorter prose hint in the same error.
		validHeaders := true
		if headers := object.Get("headers"); headers.IsObject() {
			headers.ForEach(func(key, value gjson.Result) bool {
				var candidate time.Duration
				var valid bool
				switch strings.ToLower(strings.TrimSpace(key.String())) {
				case "retry-after":
					candidate, valid = excelBPSParseRetryAfter(value.String(), now)
				case "retry-after-ms":
					n, err := strconv.ParseFloat(value.String(), 64)
					valid = err == nil && !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && n <= float64((24*time.Hour)/time.Millisecond)
					if valid {
						candidate = time.Duration(n * float64(time.Millisecond))
					}
				default:
					return true
				}
				if !valid {
					validHeaders = false
					return false
				}
				if candidate > delay {
					delay = candidate
				}
				return true
			})
		}
		if !validHeaders {
			return failure
		}
		for _, hint := range []struct {
			name string
			unit time.Duration
		}{{"retry_after_ms", time.Millisecond}, {"retry_after_seconds", time.Second}, {"retry_after", time.Second}} {
			value := object.Get(hint.name)
			if !value.Exists() {
				continue
			}
			n, err := strconv.ParseFloat(value.String(), 64)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > float64((24*time.Hour)/hint.unit) {
				return failure
			}
			if candidate := time.Duration(n * float64(hint.unit)); candidate > delay {
				delay = candidate
			}
		}
	}
	if match := excelBPSRetryHint.FindStringSubmatch(message); len(match) > 0 {
		n, err := strconv.ParseFloat(match[1], 64)
		unit := time.Second
		if strings.HasPrefix(strings.ToLower(match[2]), "m") {
			unit = time.Millisecond
		}
		if err != nil || n > float64((24*time.Hour)/unit) {
			return failure
		}
		if candidate := time.Duration(n * float64(unit)); candidate > delay {
			delay = candidate
		}
	}
	failure.delay, failure.retry = delay, true
	return failure
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
