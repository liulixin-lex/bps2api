package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var excelBPSReplay basispoints.ReplayCache

func (s *OpenAIGatewayService) disableExcelBPSOn403(ctx context.Context, account *Account) bool {
	if !account.IsExcelBPSAutoDisableOn403Enabled() {
		return false
	}
	repo, ok := s.accountRepo.(AccountExcelBPSRepository)
	if !ok {
		return false
	}
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()
	changed, err := repo.DisableExcelBPSOn403(stateCtx, account)
	if err != nil {
		// Do not log upstream bodies, credentials or database query arguments.
		logger.LegacyPrintf("service.openai_excel_bps", "auto-disable failed: account_id=%d error_type=%T", account.ID, err)
		return false
	}
	if changed {
		logger.LegacyPrintf("service.openai_excel_bps", "paused Excel BPS routing after upstream HTTP 403: account_id=%d", account.ID)
	}
	return changed
}

func (s *OpenAIGatewayService) excelBPSImageRelay(ctx context.Context) (*basispoints.ImageRelay, error) {
	settings, err := s.settingService.GetExcelBPSImageRelaySettings(ctx)
	if err != nil || !settings.Enabled {
		return nil, err
	}
	s.excelBPSImagesMu.Lock()
	defer s.excelBPSImagesMu.Unlock()
	if s.excelBPSImages == nil {
		dataDir := strings.TrimSpace(os.Getenv("DATA_DIR"))
		if dataDir == "" {
			dataDir = "./data"
		}
		var limits config.ImageRelayCacheConfig
		if s.cfg != nil {
			limits = s.cfg.Gateway.ImageRelayCache
		}
		limits = limits.WithDefaults()
		s.excelBPSImages, err = basispoints.NewImageRelay(settings.BaseURL, filepath.Join(dataDir, "bps-images"), basispoints.ImageRelayOptions{
			MaxEntries: limits.MaxEntries, MaxBytes: limits.MaxBytes, MaxDownloads: limits.MaxDownloads,
			DownloadTimeout: time.Duration(limits.DownloadTimeoutSeconds) * time.Second,
		})
	} else {
		err = s.excelBPSImages.SetPublicOrigin(settings.BaseURL)
	}
	return s.excelBPSImages, err
}

func (s *OpenAIGatewayService) CloseExcelBPSImages() error {
	if s == nil {
		return nil
	}
	s.excelBPSImagesMu.Lock()
	defer s.excelBPSImagesMu.Unlock()
	return s.excelBPSImages.Close()
}

// ServeExcelBPSImage allows the upstream to retrieve an unguessable temporary URL.
func (s *OpenAIGatewayService) ServeExcelBPSImage(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Referrer-Policy", "no-referrer")
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		c.Header("Allow", "GET, HEAD")
		c.Status(http.StatusMethodNotAllowed)
		return
	}
	// Public capability downloads must reject malformed tokens before making
	// settings queries. These requests carry no client authentication budget.
	token, ok := strings.CutPrefix(c.Request.URL.Path, basispoints.ImageRelayPath)
	if !ok || len(token) != 43 {
		http.NotFound(c.Writer, c.Request)
		return
	}
	if decoded, err := base64.RawURLEncoding.Strict().DecodeString(token); err != nil || len(decoded) != 32 {
		http.NotFound(c.Writer, c.Request)
		return
	}
	// Only the owning instance consults live settings. Unknown capabilities must
	// remain 404 so a reverse proxy can try the previous instance during drain.
	s.excelBPSImagesMu.Lock()
	ownedRelay := s.excelBPSImages
	s.excelBPSImagesMu.Unlock()
	if !ownedRelay.HasToken(token) {
		http.NotFound(c.Writer, c.Request)
		return
	}
	relay, err := s.excelBPSImageRelay(c.Request.Context())
	if err != nil {
		if !ownedRelay.HasToken(token) {
			http.NotFound(c.Writer, c.Request)
			return
		}
		// Settings failures are retryable only for images owned by this instance.
		// Do not serve bytes without checking enabled/revocation settings.
		c.Header("Retry-After", "1")
		http.Error(c.Writer, "Image relay settings are temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	relay.ServeHTTP(c.Writer, c.Request)
}

func excelBPSAccountID(account *Account, accessToken string) string {
	if accountID := strings.TrimSpace(account.GetChatGPTAccountID()); accountID != "" {
		return accountID
	}
	claims, err := openai.DecodeIDToken(accessToken)
	if err != nil || claims.OpenAIAuth == nil {
		return ""
	}
	return strings.TrimSpace(claims.OpenAIAuth.ChatGPTAccountID)
}

func newExcelBPSRequest(ctx context.Context, body []byte, token, accountID string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, basispoints.ResponsesURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = http.Header{
		"Authorization": {"Bearer " + token}, "Chatgpt-Account-Id": {accountID}, "X-Openai-Account-Id": {accountID},
		"X-Basispoints-Auth-Mode": {"chatgpt"}, "Content-Type": {"application/json"}, "Accept": {"text/event-stream"},
		"Origin": {"https://bps.openai.com"}, "User-Agent": {"Mozilla/5.0"},
		"X-Openai-Internal-Basispoints-Client-Product":       {"basispoints-excel-plugin"},
		"X-Openai-Internal-Basispoints-Client-Agent-Profile": {"excel"},
	}
	return req, nil
}

// BPS deliberately bypasses Codex ticket/cookie injection and OAuth plugins:
// only the selected account's bearer and ChatGPT account ID belong on this host.
func (s *OpenAIGatewayService) forwardExcelBPS(ctx context.Context, c *gin.Context, account *Account, body []byte, start time.Time) (*OpenAIForwardResult, error) {
	var timeouts config.ExcelBPSTimeoutConfig
	if s.cfg != nil {
		timeouts = s.cfg.Gateway.ExcelBPSTimeouts
	}
	if timeouts.TotalSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeoutCause(ctx, time.Duration(timeouts.TotalSeconds)*time.Second, errExcelBPSRequestTimeout)
		defer cancel()
	}
	return s.forwardExcelBPSAttempt(ctx, c, account, body, start, timeouts, 0)
}

func (s *OpenAIGatewayService) forwardExcelBPSAttempt(ctx context.Context, c *gin.Context, account *Account, body []byte, start time.Time, timeouts config.ExcelBPSTimeoutConfig, attempt int) (*OpenAIForwardResult, error) {
	// Record the selected provider before any local rewrite/validation. This
	// keeps request errors (including image-shape validation) attributable to
	// the BPS channel instead of the generic /v1/responses fallback label.
	SetActualOpenAIUpstreamEndpoint(c, "/basispoints/api/responses")
	c.Header("X-Codex2API-Upstream", "basispoints")
	chat := excelBPSChatFromContext(ctx)
	messages := excelBPSMessagesFromContext(ctx)
	var downstream io.StringWriter = c.Writer
	if chat != nil {
		downstream = newExcelBPSChatWriter(c.Writer, chat.OriginalModel, chat.IncludeUsage)
	} else if messages != nil {
		downstream = newExcelBPSMessagesWriter(c.Writer, messages.OriginalModel)
	}
	// The normal sjson helpers and protocol/image rewrites allocate new output.
	// Retain the immutable ingress slice for the bounded retry; cloning a
	// base64-heavy request here adds another full body per concurrent request.
	originalBody := body
	fail := func(status int, code, message string) (*OpenAIForwardResult, error) {
		// A compact keepalive may already have committed SSE headers. Otherwise
		// finish a single JSON response so the handler cannot append another error.
		committed := StopOpenAICompactSSEKeepaliveCommitted(c)
		MarkResponseCommitted(c)
		if committed {
			writeExcelBPSStreamFailure(c, chat, downstream, status, code, message)
		} else {
			if status == http.StatusServiceUnavailable && c.Writer.Header().Get("Retry-After") == "" {
				c.Header("Retry-After", "1")
			}
			writeExcelBPSJSONError(c, messages, status, code, message)
		}
		return nil, fmt.Errorf("excel BPS: %s", code)
	}
	// A denied BPS account remains a BPS account until the administrator
	// disables the switch. Honor the pause without silently selecting Codex.
	if _, paused := account.Extra[OpenAIExcelBPSPausedOn403AtExtraKey]; paused {
		return fail(http.StatusForbidden, "basispoints_routing_paused", "Excel BPS routing is paused after an upstream permission rejection; review the account permission and re-enable BPS to resume")
	}
	originalModel := gjson.GetBytes(body, "model").String()
	model := account.GetMappedModel(originalModel)
	if chat != nil {
		originalModel, model = chat.OriginalModel, chat.UpstreamModel
	} else if messages != nil {
		originalModel, model = messages.OriginalModel, messages.UpstreamModel
	}
	stream := gjson.GetBytes(body, "stream").Bool()
	var err error
	body, err = sjson.SetBytes(body, "model", model)
	if err != nil {
		return fail(400, "basispoints_request_invalid", "Invalid model request")
	}
	identity, _ := resolveOpenAIWSExecutionScope(c, body, getAPIKeyIDFromContext(c))
	if identity != "" {
		body, err = sjson.SetBytes(body, "prompt_cache_key", identity)
		if err != nil {
			return nil, err
		}
	}
	if isOpenAIResponsesCompactPath(c) {
		var request map[string]any
		if err = json.Unmarshal(body, &request); err != nil {
			return fail(400, "basispoints_request_invalid", "Invalid compact request")
		}
		var input []any
		switch v := request["input"].(type) {
		case []any:
			input = v
		case string:
			input = []any{map[string]any{"role": "user", "content": v}}
		default:
			return fail(400, "basispoints_request_invalid", "Compact requires input")
		}
		request["input"] = append(input, map[string]any{"type": "compaction_trigger"})
		request["tool_choice"] = "none"
		body, err = json.Marshal(request)
		if err != nil {
			return nil, err
		}
	}
	scope := fmt.Sprintf("account:%d/key:%d/thread:%s", account.ID, getAPIKeyIDFromContext(c), identity)
	relay, err := s.excelBPSImageRelay(ctx)
	if err != nil {
		return fail(503, "basispoints_image_relay_unavailable", err.Error())
	}
	body, err = relay.Rewrite(body, scope)
	if err != nil {
		if errors.Is(err, basispoints.ErrImageRelayFull) {
			return fail(503, "basispoints_image_relay_full", err.Error())
		}
		if errors.Is(err, basispoints.ErrImageRelayStorage) {
			return fail(503, "basispoints_image_relay_unavailable", err.Error())
		}
		return fail(400, "basispoints_request_invalid", err.Error())
	}
	if ignored := basispoints.IgnoredParameterNames(body); len(ignored) > 0 {
		// Names only: expose inherited upstream capability limits without logging
		// client values or claiming that the omitted controls were enforced.
		c.Header("X-BPS-Ignored-Parameters", strings.Join(ignored, ","))
	}
	upstreamBody, bridge, err := basispoints.Prepare(body, scope, &excelBPSReplay)
	if err != nil {
		return fail(400, "basispoints_request_invalid", err.Error())
	}
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return fail(502, "basispoints_auth_unavailable", "Account OAuth credential is unavailable")
	}
	accountID := excelBPSAccountID(account, token)
	if accountID == "" {
		return fail(400, "basispoints_account_id_missing", "Excel BPS requires chatgpt_account_id")
	}
	requestCtx := WithHTTPUpstreamRedirectsDisabled(WithHTTPUpstreamProfile(ctx, HTTPUpstreamProfileLongStream))
	req, err := newExcelBPSRequest(requestCtx, upstreamBody, token, accountID)
	if err != nil {
		return nil, err
	}
	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	SetOpsUpstreamModel(c, model)
	sent := time.Now()
	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(sent).Milliseconds())
	if err != nil {
		if errors.Is(context.Cause(ctx), errExcelBPSRequestTimeout) {
			return fail(504, "basispoints_request_timeout", "Excel BPS request timed out; request was not replayed")
		}
		return fail(502, "basispoints_transport_error", "Excel BPS connection failed; request was not replayed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
		if resp.StatusCode == http.StatusTooManyRequests && s.rateLimitService != nil {
			stateCtx, cancel := openAIAccountStateContext(ctx)
			s.rateLimitService.handle429Cooldown(stateCtx, account, resp.Header, raw)
			cancel()
		}
		// Preserve the original rejection for Ops without exposing it to clients.
		// BPS errors can echo request fields, so redact before storing diagnostics.
		upstreamMessage := fmt.Sprintf("Excel BPS returned HTTP %d", resp.StatusCode)
		upstreamDetail := ""
		if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
			safeBody := excelBPSSanitizeErrorBody(string(raw), token, account)
			maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
			if maxBytes <= 0 {
				maxBytes = 2048
			}
			upstreamDetail, _ = sanitizeErrorBodyForStorage(safeBody, maxBytes)
			if message := strings.TrimSpace(extractUpstreamErrorMessage([]byte(safeBody))); message != "" {
				upstreamMessage = truncateString(message, 2048)
			}
		}
		setOpsUpstreamError(c, resp.StatusCode, upstreamMessage, upstreamDetail)
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
			ProxyID: opsUpstreamProxyID(account), ProxyName: opsUpstreamProxyName(account),
			UpstreamStatusCode: resp.StatusCode, UpstreamRequestID: resp.Header.Get("x-request-id"),
			UpstreamURL: basispoints.ResponsesURL, Kind: "http_error",
			Message: upstreamMessage, Detail: upstreamDetail, UpstreamResponseBody: upstreamDetail,
		})
		code := gjson.GetBytes(raw, "error.code").String()
		// Only an explicit transient HTTP rejection, before any downstream
		// bytes, may be retried once. Never replay network failures, permission
		// errors, rate limits, or partially delivered streams.
		if delay, retry := excelBPSHTTPRetryDelay(resp.StatusCode, resp.Header.Get("Retry-After"), time.Now()); retry && attempt == 0 && !c.Writer.Written() && ctx.Err() == nil {
			_ = resp.Body.Close()
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				if errors.Is(context.Cause(ctx), errExcelBPSRequestTimeout) {
					return fail(504, "basispoints_request_timeout", "Excel BPS request timed out; request was not replayed")
				}
				return nil, ctx.Err()
			case <-timer.C:
			}
			return s.forwardExcelBPSAttempt(ctx, c, account, originalBody, start, timeouts, attempt+1)
		}
		if retryAfter := excelBPSRetryAfterHeader(resp.Header.Get("Retry-After")); retryAfter != "" && !c.Writer.Written() {
			c.Header("Retry-After", retryAfter)
		}
		if code == "basispoints_model_access_changed" {
			return fail(resp.StatusCode, code, "This model is not available on the account's Excel BPS endpoint")
		}
		message := "Excel BPS rejected this request; account scheduling was not changed"
		if resp.StatusCode == http.StatusTooManyRequests {
			message = "Excel BPS rate limit exceeded; request was not replayed"
		}
		if resp.StatusCode == http.StatusForbidden && s.disableExcelBPSOn403(ctx, account) {
			message = "Excel BPS rejected this request; BPS routing was paused for this account while its saved setting was preserved; request was not replayed"
		}
		return fail(resp.StatusCode, "basispoints_upstream_error", message)
	}
	activity := &excelBPSActivityBody{ReadCloser: resp.Body}
	activity.lastRead.Store(time.Now().UnixNano())
	// BPS and Codex share quota. Refresh at the HTTP boundary even if the client
	// disconnects or a later stream/protocol error prevents normal completion.
	s.UpdateCodexUsageSnapshotFromHeaders(ctx, account.ID, resp.Header)
	converted := bridge.Stream(activity)
	defer func() { _ = converted.Close() }()
	// The bridge sees the body after group policy mapping. Keep the original
	// client effort for usage display, and the BPS-normalized effort for billing.
	requestedEffort := coalesceRequestedReasoningEffort(RequestedReasoningEffortFromContext(ctx), &bridge.RequestedEffort)
	result := &OpenAIForwardResult{Model: originalModel, UpstreamModel: model, UpstreamEndpoint: "/basispoints/api/responses", Stream: stream, ReasoningEffort: &bridge.Effort, RequestedReasoningEffort: requestedEffort, RequestID: resp.Header.Get("x-request-id")}
	if chat != nil {
		result.BillingModel = chat.BillingModel
	} else if messages != nil {
		result.BillingModel = messages.BillingModel
	}
	if stream {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("X-Accel-Buffering", "no")
	}
	scanner := newOpenAISSEReadPump(converted, 16<<20)
	scanner.upstreamActivity = &activity.lastRead
	if timeouts.FirstOutputSeconds > 0 {
		scanner.firstOutputDeadline = sent.Add(time.Duration(timeouts.FirstOutputSeconds) * time.Second)
	}
	defer scanner.Close()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	keepalive := func() {
		if stream && ctx.Err() == nil {
			_, _ = downstream.WriteString(": keepalive\n\n")
			c.Writer.Flush()
		}
	}
	// Non-streaming clients need a bounded fallback when the provider emits
	// deltas but an explicit terminal snapshot with no output. Tools still use
	// only the authoritative terminal validated by the BPS bridge.
	var bufferedText *excelBPSBufferedOutput
	if !stream {
		bufferedText = newExcelBPSBufferedOutput()
	}
	// Converter resource failures are upstream protocol failures, not client
	// disconnects. They must never trigger replay or a successful terminal.
	downstreamFailure := func(writeErr error) (*OpenAIForwardResult, error) {
		result.Duration = time.Since(start)
		if !errors.Is(writeErr, apicompat.ErrChatStreamMetadataLimit) && !errors.Is(writeErr, errExcelBPSMessagesConversion) {
			result.ClientDisconnect = true
			return result, writeErr
		}
		result.streamReadIncomplete = true
		result.UpstreamTerminalEvent = "response.failed"
		compactCommitted := StopOpenAICompactSSEKeepaliveCommitted(c)
		MarkResponseCommitted(c)
		const code = "basispoints_response_metadata_limit"
		const message = "Excel BPS response metadata exceeds the stream resource limit; request was not replayed"
		MarkOpsStreamError(c, code, message, http.StatusBadGateway)
		if compactCommitted || c.Writer.Written() {
			writeExcelBPSStreamFailure(c, chat, downstream, http.StatusBadGateway, code, message)
		} else {
			c.Header("Content-Type", "application/json")
			writeExcelBPSJSONError(c, messages, http.StatusBadGateway, code, message)
		}
		return result, fmt.Errorf("excel BPS response conversion failed: %w", writeErr)
	}
	var completed []byte
	terminal := ""
	terminalCode := "basispoints_upstream_error"
	// Hold only initial metadata so a failed local translation can be regenerated
	// once without exposing duplicate response IDs or replaying client tool calls.
	var pending strings.Builder
	outputCommitted := false
	cacheCreationAsInput := account.IsExcelBPSCacheCreationAsInputEnabled()
	for scanner.Next(ctx, time.Duration(timeouts.IdleSeconds)*time.Second, heartbeat.C, keepalive) {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			payload := []byte(strings.TrimPrefix(line, "data: "))
			kind := gjson.GetBytes(payload, "type").String()
			protocolMessage := gjson.GetBytes(payload, "response.error.message").String()
			// Retry only a local transport failure before any client output.
			// No client tool has been dispatched; provider refusals are separate.
			correctable := excelBPSCorrectableProtocolError(protocolMessage)
			if correctable && kind == "response.failed" && gjson.GetBytes(payload, "response.error.code").String() == "basispoints_protocol_error" && attempt == 0 && !outputCommitted && ctx.Err() == nil {
				instructions := gjson.GetBytes(originalBody, "instructions").String()
				instructions += "\nTransport correction: the previous tool response was rejected before any client output or tool dispatch. Follow the exact catalog transport. For CUSTOM raw input, summary MUST be codex2api.custom/CATALOG_NAME and code MUST contain the raw string, never an object of arguments. For a declared FUNCTION_CODE tool, summary MUST be codex2api.function_code/CATALOG_NAME and extended_summary MUST contain the other arguments as JSON. Use the exact catalog name including its namespace. For ordinary FUNCTION, serialize one valid JSON envelope with escaped strings. Do not repeat the rejected wrapper or use a prose summary for raw source. Never invent tool names or execute wrapper code. Do not call native skills, workbook or connector tools. If the client catalog is empty, return assistant text only."
				corrected, correctionErr := sjson.SetBytes(originalBody, "instructions", instructions)
				if correctionErr == nil {
					scanner.Close()
					_ = converted.Close()
					_ = resp.Body.Close()
					logger.LegacyPrintf("service.openai_excel_bps", "retrying pre-output protocol failure once: account_id=%d", account.ID)
					appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
						Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
						UpstreamStatusCode: http.StatusBadGateway, UpstreamRequestID: resp.Header.Get("x-request-id"),
						Kind: "protocol_retry", Message: "Local BPS tool translation rejected before output; one correction attempt",
					})
					return s.forwardExcelBPSAttempt(ctx, c, account, corrected, start, timeouts, attempt+1)
				}
			}
			clientOutput := openAIStreamDataStartsClientOutput(string(payload), kind)
			// Reasoning metadata may precede an invalid native call. Keep it in
			// the existing bounded buffer until actual text/tool output starts.
			if strings.HasPrefix(kind, "response.reasoning") || ((kind == "response.output_item.added" || kind == "response.output_item.done") && gjson.GetBytes(payload, "item.type").String() == "reasoning") {
				clientOutput = false
			}
			// Non-streaming responses have delivered nothing until terminal.
			if (stream && clientOutput) || openAIStreamEventTypeIsTerminal(kind) {
				outputCommitted = true
			}
			s.parseSSEUsageBytes(payload, &result.Usage)
			if bufferedText != nil && (kind == "response.output_text.delta" || kind == "response.reasoning_summary_text.delta" || kind == "response.output_text.done" || kind == "response.reasoning_summary_text.done") {
				var event apicompat.ResponsesStreamEvent
				if err = json.Unmarshal(payload, &event); err == nil {
					err = bufferedText.Observe(&event)
				}
				if err != nil {
					result.Duration = time.Since(start)
					result.streamReadIncomplete = true
					_, failure := fail(http.StatusBadGateway, "basispoints_output_invalid", "Excel BPS buffered output is inconsistent or exceeds the response size limit")
					return result, failure
				}
			}
			if cacheCreationAsInput {
				payload, err = excelBPSDownstreamUsage(payload)
				if err != nil {
					return fail(http.StatusBadGateway, "basispoints_usage_invalid", "Excel BPS usage could not be normalized")
				}
				line = "data: " + string(payload)
			}
			if result.FirstTokenMs == nil && (kind == "response.output_text.delta" || kind == "response.output_item.added") {
				scanner.firstOutputDeadline = time.Time{}
				ms := int(time.Since(start).Milliseconds())
				result.FirstTokenMs = &ms
			}
			switch kind {
			case "response.completed", "response.failed", "response.incomplete", "error":
				terminal = kind
				if kind == "response.incomplete" {
					terminalCode = "response_incomplete"
				} else if code := gjson.GetBytes(payload, "response.error.code").String(); code != "" {
					terminalCode = code
				}
				completed = []byte(gjson.GetBytes(payload, "response").Raw)
				result.ResponseID = gjson.GetBytes(payload, "response.id").String()
				result.UpstreamResponseModel = gjson.GetBytes(payload, "response.model").String()
			}
		}
		if stream {
			if !outputCommitted {
				if _, err = pending.WriteString(line + "\n"); err != nil {
					return result, err
				}
				// Production response.created/in_progress events each carry ~95 KiB
				// of provider metadata. A 64 KiB cap disabled correction before
				// any text or tool output. Keep both events within a bounded cap.
				if pending.Len() < 256<<10 {
					continue
				}
				// Bound metadata memory and permanently disarm recovery when flushed.
				outputCommitted = true
				line = ""
			} else {
				line += "\n"
			}
			if pending.Len() > 0 {
				if _, err = downstream.WriteString(pending.String()); err != nil {
					return downstreamFailure(err)
				}
				pending.Reset()
			}
			if _, err = downstream.WriteString(line); err != nil {
				return downstreamFailure(err)
			}
			if line == "\n" || line == "" {
				c.Writer.Flush()
			}
		}
	}
	result.Duration = time.Since(start)
	result.UpstreamTerminalEvent = terminal
	err = scanner.Err()
	// A clean premature EOF before any visible output can be regenerated once.
	// This shares the HTTP/protocol budget and original deadline. Never replay
	// network errors, cancellation, flushed metadata, or delivered tool/text.
	if terminal == "" && attempt == 0 && !outputCommitted && ctx.Err() == nil &&
		(err == nil || errors.Is(err, io.ErrUnexpectedEOF)) {
		scanner.Close()
		_ = converted.Close()
		_ = resp.Body.Close()
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
			UpstreamStatusCode: resp.StatusCode, UpstreamRequestID: resp.Header.Get("x-request-id"),
			Kind: "pre_output_eof_retry", Message: "BPS stream ended before output; one recovery attempt",
		})
		timer := time.NewTimer(250 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			// Preserve the timeout cause for the shared terminal handling below.
			// An internal deadline is not a client disconnect or an empty 200.
			err = ctx.Err()
			result.Duration = time.Since(start)
		case <-timer.C:
			return s.forwardExcelBPSAttempt(ctx, c, account, originalBody, start, timeouts, attempt+1)
		}
	}
	// If no correction was taken, preserve the original SSE contract on EOF or
	// timeout: metadata already received is followed by one terminal SSE error.
	if stream && pending.Len() > 0 && !errors.Is(ctx.Err(), context.Canceled) {
		if _, writeErr := downstream.WriteString(pending.String()); writeErr != nil {
			return downstreamFailure(writeErr)
		}
		pending.Reset()
	}
	if err != nil || terminal == "" {
		if errors.Is(err, errOpenAISSEIdle) || errors.Is(err, errOpenAISSEFirstOutput) || errors.Is(context.Cause(ctx), errExcelBPSRequestTimeout) {
			// Compact clients can have SSE headers committed by the keepalive
			// even when the forwarded body uses stream=false. Stop it before
			// deciding the error format, just as the HTTP error path does.
			compactCommitted := StopOpenAICompactSSEKeepaliveCommitted(c)
			MarkResponseCommitted(c)
			result.streamReadIncomplete = true
			const code = "basispoints_stream_timeout"
			const message = "Excel BPS stream timed out; request was not replayed"
			if compactCommitted || (stream && c.Writer.Written()) {
				writeExcelBPSStreamFailure(c, chat, downstream, 504, code, message)
			} else {
				c.Header("Content-Type", "application/json")
				writeExcelBPSJSONError(c, messages, 504, code, message)
			}
			return result, fmt.Errorf("excel BPS stream timeout: %w", err)
		}
		if ctx.Err() != nil {
			result.ClientDisconnect = true
			return result, ctx.Err()
		}
		result.streamReadIncomplete = true
		MarkResponseCommitted(c)
		if stream {
			_, _ = downstream.WriteString("event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"basispoints_stream_incomplete\",\"message\":\"Upstream stream ended before completion\"}}}\n\n")
			c.Writer.Flush()
		} else {
			writeExcelBPSJSONError(c, messages, 502, "basispoints_stream_incomplete", "Excel BPS stream ended before completion")
		}
		return result, fmt.Errorf("excel BPS stream incomplete")
	}
	if bufferedText != nil && bufferedText.HasContent() && (terminal == "response.completed" || terminal == "response.incomplete") && gjson.GetBytes(completed, "output").IsArray() && len(gjson.GetBytes(completed, "output").Array()) == 0 {
		completed, err = sjson.SetBytes(completed, "output", bufferedText.BuildOutput(gjson.GetBytes(completed, "status").String()))
		if err != nil {
			_, failure := fail(http.StatusBadGateway, "basispoints_output_invalid", "Excel BPS final output could not be assembled")
			return result, failure
		}
	}
	messagesMaxTokens := messages != nil && terminal == "response.incomplete" && excelBPSIncompleteReason(completed) == "max_output_tokens"
	if terminal == "response.incomplete" && !messagesMaxTokens {
		reason := excelBPSIncompleteReason(completed)
		message := "Excel BPS response incomplete: " + reason
		detail, _ := json.Marshal(map[string]any{"event": terminal, "reason": reason,
			"duration_ms": result.Duration.Milliseconds(), "output_committed": outputCommitted})
		setOpsUpstreamError(c, resp.StatusCode, message, string(detail))
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
			UpstreamStatusCode: resp.StatusCode, UpstreamRequestID: resp.Header.Get("x-request-id"),
			Kind: "response_incomplete", Message: message, Detail: string(detail),
		})
	}
	if terminal != "response.completed" {
		MarkResponseCommitted(c)
	}
	if !stream {
		if messages != nil && terminal == "response.incomplete" {
			reason := excelBPSIncompleteReason(completed)
			if reason != "max_output_tokens" {
				writeExcelBPSJSONError(c, messages, http.StatusBadGateway, "basispoints_response_incomplete", "Excel BPS did not complete the response")
				return result, fmt.Errorf("excel BPS Messages incomplete: %s", reason)
			}
		}
		if terminal == "response.completed" || (terminal == "response.incomplete" && gjson.ParseBytes(completed).IsObject()) {
			// An explicit incomplete response is a native Responses terminal,
			// including its partial answer, usage and incomplete_details. Keep
			// that contract instead of replacing it with a generic 502 body.
			if chat != nil {
				var response apicompat.ResponsesResponse
				if err := json.Unmarshal(completed, &response); err != nil {
					return fail(502, "basispoints_chat_invalid", "Invalid BPS final response")
				}
				c.JSON(http.StatusOK, apicompat.ResponsesToChatCompletions(&response, chat.OriginalModel))
			} else if messages != nil {
				if err := writeExcelBPSMessagesResponse(c, completed, messages); err != nil {
					return fail(502, "basispoints_messages_invalid", "Invalid BPS final response")
				}
			} else {
				c.Data(http.StatusOK, "application/json", completed)
			}
		} else {
			writeExcelBPSJSONError(c, messages, 502, terminalCode, "Excel BPS did not complete the response")
		}
	}
	if terminal != "response.completed" && !messagesMaxTokens {
		return result, fmt.Errorf("excel BPS terminal: %s", terminal)
	}
	s.bindHTTPResponseAccount(ctx, c, account, result.ResponseID)
	return result, nil
}

// Preserve a useful failure reason without logging provider-controlled text or
// copying a potentially huge response body containing conversation metadata.
func excelBPSIncompleteReason(response []byte) string {
	reason := gjson.GetBytes(response, "incomplete_details.reason").String()
	switch reason {
	case "max_output_tokens", "max_tokens", "content_filter", "model_context_window_exceeded":
		return reason
	case "":
		return "unspecified"
	default:
		return "unrecognized"
	}
}

var excelBPSBearerPattern = regexp.MustCompile(`(?i)\bBearer\s+[^\s"',;<>]+`)
var excelBPSURLCredentialsPattern = regexp.MustCompile(`(https?://)[^/\s@]+@`)
var excelBPSImageCapabilityPattern = regexp.MustCompile(`/api/bps-images/[A-Za-z0-9_-]+`)

func excelBPSSanitizeErrorBody(raw, token string, account *Account) string {
	if !json.Valid([]byte(raw)) {
		return ""
	}
	secrets := append([]string{token}, excelBPSAccountSecrets(account)...)
	fields := make(map[string]string)
	for _, key := range []string{"message", "code", "type", "param"} {
		value := gjson.Get(raw, "error."+key)
		if value.Type != gjson.String {
			continue
		}
		clean := value.String()
		for _, secret := range secrets {
			if secret != "" {
				clean = strings.ReplaceAll(clean, secret, "[redacted]")
			}
		}
		clean = excelBPSBearerPattern.ReplaceAllString(clean, "Bearer [redacted]")
		clean = excelBPSURLCredentialsPattern.ReplaceAllString(clean, "${1}[redacted]@")
		clean = excelBPSImageCapabilityPattern.ReplaceAllString(clean, "/api/bps-images/[redacted]")
		clean = sanitizeUpstreamErrorMessage(clean)
		fields[key] = truncateString(logredact.RedactText(clean, "authorization", "api_key", "apikey", "token", "secret", "key", "cookie", "ticket", "recovery_ticket"), 2048)
	}
	encoded, _ := json.Marshal(map[string]any{"error": fields})
	return string(encoded)
}

func excelBPSAccountSecrets(account *Account) []string {
	var secrets []string
	for _, key := range []string{"access_token", "refresh_token", "id_token", "api_key", "session_key", "cookie"} {
		if value := account.GetCredential(key); value != "" {
			secrets = append(secrets, value)
		}
	}
	if account.Proxy != nil && account.Proxy.Password != "" {
		secrets = append(secrets, account.Proxy.Password)
	}
	return secrets
}
