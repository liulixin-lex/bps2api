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
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var excelBPSReplay basispoints.ReplayCache
var excelBPSCatalog basispoints.CatalogCache

// BPS uses the account's OAuth credentials, so authentication failures must
// update the same scheduling state as ordinary OpenAI requests. Keep arbitrary
// BPS error text (which may echo request data) out of persisted account reasons.
func (s *OpenAIGatewayService) handleExcelBPSUnauthorized(ctx context.Context, account *Account, status int, headers http.Header, raw []byte) {
	if status != http.StatusUnauthorized || s.rateLimitService == nil || isQualityObservation(ctx) {
		return
	}
	fields := map[string]string{"message": "Excel BPS authentication failed"}
	code := extractUpstreamErrorCode(raw)
	if code == "token_invalidated" || code == "token_revoked" {
		fields["code"] = code
	}
	authError := map[string]any{"error": fields}
	if gjson.GetBytes(raw, "detail").String() == "Unauthorized" {
		authError["detail"] = "Unauthorized"
	}
	body, _ := json.Marshal(authError)
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()
	s.rateLimitService.HandleUpstreamError(stateCtx, account, status, headers, body)
}

func (s *OpenAIGatewayService) moveExcelBPSOn403(ctx context.Context, account *Account) bool {
	target, enabled := account.ExcelBPS403GroupTarget()
	if !enabled || isQualityObservation(ctx) {
		return false
	}
	repo, ok := s.accountRepo.(AccountExcelBPSGroupRepository)
	if !ok {
		return false
	}
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()
	changed, err := repo.MoveExcelBPSOn403(stateCtx, account)
	if err != nil {
		logger.LegacyPrintf("service.openai_excel_bps", "automatic group action failed: account_id=%d error_type=%T", account.ID, err)
		return false
	}
	if changed {
		logger.LegacyPrintf("service.openai_excel_bps", "automatically updated groups after upstream HTTP 403: account_id=%d target_group_id=%d", account.ID, target)
	}
	return changed
}

func (s *OpenAIGatewayService) disableExcelBPSOn403(ctx context.Context, account *Account) bool {
	if !account.IsExcelBPSAutoDisableOn403Enabled() || isQualityObservation(ctx) {
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
	if err != nil {
		return nil, err
	}
	return s.excelBPSImageRelayForSettings(settings)
}

func (s *OpenAIGatewayService) excelBPSImageRelayForSettings(settings ExcelBPSImageRelaySettings) (*basispoints.ImageRelay, error) {
	if !settings.Enabled || settings.Mode == ExcelBPSImageModeNative {
		return nil, nil
	}
	var err error
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
	}
	if err == nil {
		err = s.excelBPSImages.Configure(settings.BaseURL, settings.Limits)
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
	// Handler account switching must not reset the service retry envelope.
	// Gin's keys are request-scoped; recursive attempts keep this same object.
	recovery := newExcelBPSRecovery(timeouts)
	if stored, ok := c.Get("excel_bps_shared_recovery"); ok {
		if existing, ok := stored.(*excelBPSRecovery); ok {
			recovery = existing
			if !recovery.consumeRepair() {
				status := c.GetInt("excel_bps_recovery_last_status")
				if status < 400 {
					status = http.StatusServiceUnavailable
				}
				const code = "basispoints_recovery_exhausted"
				const message = "Excel BPS automatic recovery attempt or time budget was exhausted"
				committed := StopOpenAICompactSSEKeepaliveCommitted(c) || c.Writer.Written()
				MarkResponseCommitted(c)
				if committed {
					chat := excelBPSChatFromContext(ctx)
					var downstream io.StringWriter = c.Writer
					if chat != nil {
						downstream = newExcelBPSChatWriter(c.Writer, chat.OriginalModel, chat.IncludeUsage)
					} else if messages := excelBPSMessagesFromContext(ctx); messages != nil {
						downstream = newExcelBPSMessagesWriter(c.Writer, messages.OriginalModel)
					}
					writeExcelBPSStreamFailure(c, chat, downstream, status, code, message)
				} else {
					writeExcelBPSJSONError(c, excelBPSMessagesFromContext(ctx), status, code, message)
				}
				return nil, fmt.Errorf("excel BPS: %s", code)
			}
			var cancel context.CancelFunc
			ctx, cancel = recovery.withDeadline(ctx)
			defer cancel()
		}
	} else {
		c.Set("excel_bps_shared_recovery", recovery)
	}
	recovery.mu.Lock()
	if recovery.accounts == nil {
		recovery.accounts = make(map[int64]struct{})
	}
	recovery.accounts[account.ID] = struct{}{}
	recovery.mu.Unlock()
	if timeouts.TotalSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadlineCause(ctx, recovery.started.Add(time.Duration(timeouts.TotalSeconds)*time.Second), errExcelBPSRequestTimeout)
		defer cancel()
	}
	return s.forwardExcelBPSAttempt(ctx, c, account, body, start, timeouts, 0, recovery)
}

func (s *OpenAIGatewayService) forwardExcelBPSAttempt(ctx context.Context, c *gin.Context, account *Account, body []byte, start time.Time, timeouts config.ExcelBPSTimeoutConfig, attempt int, recovery *excelBPSRecovery) (*OpenAIForwardResult, error) {
	return s.forwardExcelBPSAttemptWithAcquire(ctx, c, account, body, start, timeouts, attempt, recovery, s.excelBPSAcquireFor(account))
}

func (s *OpenAIGatewayService) forwardExcelBPSAttemptWithAcquire(ctx context.Context, c *gin.Context, account *Account, body []byte, start time.Time, timeouts config.ExcelBPSTimeoutConfig, attempt int, recovery *excelBPSRecovery, acquire excelBPSAcquire) (*OpenAIForwardResult, error) {
	// Record the selected provider before any local rewrite/validation. This
	// keeps request errors (including image-shape validation) attributable to
	// the BPS channel instead of the generic /v1/responses fallback label.
	SetActualOpenAIUpstreamEndpoint(c, "/basispoints/api/responses")
	c.Header("X-Codex2API-Upstream", "basispoints")
	c.Writer.Header().Del("X-Codex2API-Basispoints-Bypass")
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
	fail := func(status int, code, message string, param ...string) (*OpenAIForwardResult, error) {
		// A compact keepalive may already have committed SSE headers. Otherwise
		// finish a single JSON response so the handler cannot append another error.
		committed := StopOpenAICompactSSEKeepaliveCommitted(c) || c.Writer.Written()
		MarkResponseCommitted(c)
		if committed {
			writeExcelBPSStreamFailure(c, chat, downstream, status, code, message, param...)
		} else {
			if status == http.StatusServiceUnavailable && c.Writer.Header().Get("Retry-After") == "" {
				c.Header("Retry-After", "1")
			}
			writeExcelBPSJSONError(c, messages, status, code, message, param...)
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
	ctx = excelBPSWithKeepalive(ctx, c, stream)
	clientCanceled := func() (*OpenAIForwardResult, error) {
		StopOpenAICompactSSEKeepaliveCommitted(c)
		MarkResponseCommitted(c)
		MarkOpsClientCancellation(c, stream)
		// No response or metered usage exists before headers; do not create a usage row.
		return nil, context.Canceled
	}
	// No output exists yet, so the handler may replay the request on another
	// account within its switch budget unless the client is already gone.
	failoverRateLimited := func(retryAfter string, modelScoped ...bool) (*OpenAIForwardResult, error) {
		c.Set("excel_bps_recovery_last_status", http.StatusTooManyRequests)
		retryAfter = excelBPSRetryAfterHeader(retryAfter)
		if !isQualityObservation(ctx) && (len(modelScoped) == 0 || !modelScoped[0]) {
			s.coolDownExcelBPS(ctx, account, retryAfter)
		}
		if isExcelBPSClientCancellation(c, ctx.Err()) {
			return clientCanceled()
		}
		failover := newExcelBPSRateLimitedFailoverError(retryAfter)
		failover.RequiredExcelBPSUpstreamModel = model
		return nil, failover
	}
	// Attachment ownership is distinct from the borrowed Responses lease. Release
	// the real owner before recursion so the next attempt can acquire the exit.
	releaseAttachment := func() {}
	defer func() { releaseAttachment() }()
	// Call only after closing the failed response and releasing its proxy lease.
	// No response IDs, text or client tool calls may have been delivered yet.
	retryRequest := func(nextBody []byte, minimumDelay time.Duration, cleanup ...func()) (*OpenAIForwardResult, error, bool) {
		retryCtx, cancel, reserved, retryErr := recovery.begin(ctx, minimumDelay, func() {
			for _, closeAttempt := range cleanup {
				closeAttempt()
			}
			releaseAttachment()
		})
		if !reserved {
			if excelBPSRecoveryBudgetExpired(recovery) {
				result, failure := fail(504, "basispoints_request_timeout", "Excel BPS recovery time budget was exhausted")
				return result, failure, true
			}
			return nil, retryErr, false
		}
		defer cancel()
		if retryErr != nil {
			if errors.Is(context.Cause(retryCtx), errExcelBPSRequestTimeout) {
				result, failure := fail(504, "basispoints_request_timeout", "Excel BPS recovery time budget was exhausted")
				return result, failure, true
			}
			result, failure := clientCanceled()
			return result, failure, true
		}
		result, failure := s.forwardExcelBPSAttemptWithAcquire(retryCtx, c, account, nextBody, start, timeouts, attempt+1, recovery, acquire)
		return result, failure, true
	}
	var err error
	body, err = sjson.SetBytes(body, "model", model)
	if err != nil {
		return fail(400, "basispoints_request_invalid", "Invalid model request")
	}
	identity, transient := resolveExcelBPSIdentity(c, body, getAPIKeyIDFromContext(c), account.IsExcelBPSMihomoEnabled())
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
	if transient {
		scope = "transient:" + scope
	}
	imageSettings, err := s.settingService.GetExcelBPSImageRelaySettings(ctx)
	if err != nil {
		if isExcelBPSClientCancellation(c, err) {
			return clientCanceled()
		}
		return fail(503, "basispoints_image_settings_unavailable", "Excel BPS image settings are unavailable")
	}
	if !imageSettings.Enabled && account.IsExcelBPSIgnoreImagesEnabled() {
		body, err = basispoints.StripInputImages(body)
		if err != nil {
			return fail(400, "basispoints_request_invalid", err.Error())
		}
	}
	if account.IsExcelBPSIgnoreEncryptedContentEnabled() {
		body, err = basispoints.StripEncryptedContent(body)
		if err != nil {
			return fail(400, "basispoints_request_invalid", err.Error())
		}
	}
	var images *basispoints.NativeImages
	if imageSettings.Enabled && imageSettings.Mode == ExcelBPSImageModeNative {
		images, err = basispoints.PrepareNativeImagesWithLimit(body, imageSettings.Limits.MaxImages)
		if err == nil {
			body, err = images.Body()
		}
	} else {
		var relay *basispoints.ImageRelay
		relay, err = s.excelBPSImageRelayForSettings(imageSettings)
		if err != nil {
			return fail(503, "basispoints_image_relay_unavailable", "Excel BPS image relay is unavailable")
		}
		body, err = relay.Rewrite(body, scope)
	}
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
	// Validate the complete request before uploading any attachments. The native
	// plan contains valid placeholder IDs until all local protocol checks pass.
	var replay *basispoints.ReplayCache
	var catalog *basispoints.CatalogCache
	if identity != "" {
		replay, catalog = &excelBPSReplay, &excelBPSCatalog
	}
	var upstreamBody []byte
	var bridge *basispoints.Bridge
	if images != nil {
		upstreamBody, bridge, err = images.PrepareWithCatalog(scope, replay, catalog)
	} else {
		upstreamBody, bridge, err = basispoints.PrepareWithCatalog(body, scope, replay, catalog)
	}
	if err != nil {
		var contentErr *basispoints.ContentValidationError
		if errors.As(err, &contentErr) {
			return fail(400, "basispoints_request_invalid", err.Error(), contentErr.Path)
		}
		return fail(400, "basispoints_request_invalid", err.Error())
	}
	if omitted := bridge.OmittedHostedToolTypes(); len(omitted) > 0 {
		c.Header("X-BPS-Omitted-Hosted-Tools", strings.Join(omitted, ","))
	}

	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		if isExcelBPSClientCancellation(c, err) {
			return clientCanceled()
		}
		return fail(502, "basispoints_auth_unavailable", "Account OAuth credential is unavailable")
	}
	accountID := excelBPSAccountID(account, token)
	if accountID == "" {
		return fail(400, "basispoints_account_id_missing", "Excel BPS requires chatgpt_account_id")
	}
	requestAcquire := acquire
	attachmentProxy := ""
	if account.Proxy != nil {
		attachmentProxy = account.Proxy.URL()
	}
	if images != nil && images.HasImages() && account.IsExcelBPSMihomoEnabled() {
		var lease excelBPSLease
		// Pin the attachment upload and the Responses request to the same exit,
		// whichever pool the account chose.
		attachmentProxy, lease, err = requestAcquire(ctx, scope)
		if err != nil {
			if isExcelBPSClientCancellation(c, err) {
				return clientCanceled()
			}
			return fail(503, "basispoints_proxy_unavailable", "No healthy BPS session proxy is available; retry later")
		}
		releaseAttachment = sync.OnceFunc(lease.Release)
		requestAcquire = pinnedExcelBPSAcquire(attachmentProxy, lease)
	}
	if images != nil && images.HasImages() {
		attachmentScope := ""
		if identity != "" {
			attachmentScope = scope + "\x00" + accountID + "\x00" + token
		}
		body, err = images.Upload(ctx, &s.excelBPSAttachments, attachmentScope, func(uploadCtx context.Context, img basispoints.InlineAttachment) (string, error) {
			SetActualOpenAIUpstreamEndpoint(c, "/basispoints/api/attachments")
			return s.uploadExcelBPSAttachment(uploadCtx, account, token, accountID, attachmentProxy, img)
		})
		if err != nil {
			if IsOpenAIRPMError(err) {
				return nil, err
			}
			if isExcelBPSClientCancellation(c, err) {
				return clientCanceled()
			}
			status, code := http.StatusBadGateway, "basispoints_attachment_error"
			var uploadError *excelBPSAttachmentError
			if errors.As(err, &uploadError) {
				status = uploadError.status
			}
			if errors.Is(err, basispoints.ErrAttachmentBusy) {
				status = http.StatusServiceUnavailable
			}
			if status == http.StatusTooManyRequests && uploadError != nil {
				appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
					Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
					ProxyID: opsUpstreamProxyID(account), ProxyName: opsUpstreamProxyName(account),
					UpstreamStatusCode: status, UpstreamURL: basispoints.AttachmentsURL, Kind: "failover",
					Message: "Excel BPS attachment upload was rate limited",
				})
				return failoverRateLimited(uploadError.retryAfter)
			}
			setOpsUpstreamError(c, status, "Excel BPS attachment upload failed", "")
			if status == http.StatusUnauthorized {
				return fail(status, code, "Excel BPS attachment authentication failed; request was not replayed")
			}
			return fail(status, code, "Excel BPS attachment upload failed; request was not replayed and account scheduling was not changed")
		}
		upstreamBody, bridge, err = bridge.Reprepare(body)
		if err != nil {
			return fail(400, "basispoints_request_invalid", err.Error())
		}
	}
	requestCtx := WithHTTPUpstreamRedirectsDisabled(WithHTTPUpstreamProfile(ctx, HTTPUpstreamProfileExcelBPS))

	SetActualOpenAIUpstreamEndpoint(c, "/basispoints/api/responses")
	SetOpsUpstreamModel(c, model)
	sent := time.Now()
	resp, lease, proxyURL, err := s.doExcelBPSRequest(requestCtx, c, account, scope, upstreamBody, token, accountID, requestAcquire, recovery)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(sent).Milliseconds())
	if err == nil && resp == nil {
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		if errors.Is(err, errExcelBPSRequestTimeout) || errors.Is(context.Cause(ctx), errExcelBPSRequestTimeout) {
			return fail(504, "basispoints_request_timeout", "Excel BPS request timed out; request was not replayed")
		}
		var quotaWait *excelBPSQuotaWaitError
		if errors.As(err, &quotaWait) {
			return failoverRateLimited(quotaWait.retryAfter, true)
		}
		if isExcelBPSClientCancellation(c, err) {
			return clientCanceled()
		}
		if IsOpenAIRPMError(err) {
			return nil, err
		}
		if errors.Is(err, errExcelBPSProxyUnavailable) {
			return fail(503, "basispoints_proxy_unavailable", "No healthy BPS session proxy is available; retry later")
		}
		if excelBPSRetryableTransportError(err) && ctx.Err() == nil && !IsResponseCommitted(c) {
			if result, failure, retried := retryRequest(originalBody, 0); retried {
				return result, failure
			}
		}
		return fail(502, "basispoints_transport_error", "Excel BPS connection failed after bounded automatic recovery")
	}
	// A first send may have waited on a peer's quota feedback and started the
	// shared budget inside transport. Carry that same deadline into stream work
	// without using the response-owned cancellation as the next retry's parent.
	recovery.mu.Lock()
	quotaBudgetActive := !recovery.deadline.IsZero()
	recovery.mu.Unlock()
	if quotaBudgetActive {
		var cancel context.CancelFunc
		ctx, cancel = recovery.withDeadline(ctx)
		defer cancel()
		requestCtx = WithHTTPUpstreamRedirectsDisabled(WithHTTPUpstreamProfile(ctx, HTTPUpstreamProfileExcelBPS))
	}
	// Release before a recursive recovery. Holding the previous lease while
	// acquiring its replacement can exhaust a single-slot proxy pool.
	releaseLease := func() {
		if lease != nil {
			lease.Release()
			lease = nil
		}
	}
	defer releaseLease()
	defer func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()
	if resp.StatusCode == http.StatusBadRequest {
		// Read and close before retrying: the HTTP body owns the account's
		// concurrency slot. Keep the proxy lease for the exact same exit.
		const maxRejectionBytes = 512 << 10
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxRejectionBytes+1))
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(raw))
		if retryBody, retry := prepareExcelBPSInvalidEncryptedRetry(upstreamBody, raw); retry && attempt == 0 && readErr == nil && len(raw) <= maxRejectionBytes && ctx.Err() == nil && recovery.consumeRepair() {
			// Persist the same narrow repair on the ingress request as well: a
			// later HTTP/SSE retry must not restore the rejected ciphertext or
			// feed the already translated tool catalog through Prepare twice.
			if repairedOriginal, changed := prepareExcelBPSInvalidEncryptedRetry(originalBody, raw); changed {
				originalBody = repairedOriginal
			}
			var cancel context.CancelFunc
			requestCtx, cancel = recovery.withDeadline(requestCtx)
			defer cancel()
			ctx = requestCtx
			retryReq, retryErr := newExcelBPSRequest(requestCtx, retryBody, token, accountID)
			if retryErr != nil {
				return fail(502, "basispoints_transport_error", "Excel BPS recovery request could not be prepared")
			}
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
				Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
				ProxyID: opsUpstreamProxyID(account), ProxyName: opsUpstreamProxyName(account),
				UpstreamStatusCode: resp.StatusCode, UpstreamRequestID: resp.Header.Get("x-request-id"),
				UpstreamURL: basispoints.ResponsesURL, Kind: "invalid_encrypted_content_retry",
				Message: "Excel BPS rejected encrypted reasoning; retrying once without opaque reasoning on the same route",
			})
			logger.LegacyPrintf("service.openai_excel_bps", "retrying invalid encrypted reasoning once: account_id=%d", account.ID)
			// Do not re-enter proxy acquisition or transport retries after sending.
			resp, err = s.sendExcelBPSWithQuota(c, account, model, retryReq, proxyURL, recovery)
			SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(sent).Milliseconds())
			if err != nil {
				if errors.Is(err, errExcelBPSRequestTimeout) || errors.Is(context.Cause(requestCtx), errExcelBPSRequestTimeout) {
					return fail(504, "basispoints_request_timeout", "Excel BPS recovery time budget was exhausted")
				}
				var quotaWait *excelBPSQuotaWaitError
				if errors.As(err, &quotaWait) {
					return failoverRateLimited(quotaWait.retryAfter, true)
				}
				if IsOpenAIRPMError(err) {
					return nil, err
				}
				if isExcelBPSClientCancellation(c, err) {
					return clientCanceled()
				}
				if lease != nil && ctx.Err() == nil {
					lease.ReportFailure()
				}
				releaseLease()
				if excelBPSRetryableTransportError(err) && ctx.Err() == nil {
					if result, failure, retried := retryRequest(originalBody, 0); retried {
						return result, failure
					}
				}
				return fail(502, "basispoints_transport_error", "Excel BPS recovery connection failed; request was not replayed again")
			}
			upstreamBody = retryBody
			attempt++ // The shared budget was charged before the encrypted repair.
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if lease != nil && resp.StatusCode >= 500 && ctx.Err() == nil {
			lease.ReportUpstreamFailure()
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
		// BPS throttles its own endpoint. A BPS 429 must not write Codex
		// quota/cooldown state; it cools only the BPS route and fails over.
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
		code := gjson.GetBytes(raw, "error.code").String()
		providerFailure := excelBPSClassifyProviderFailure(raw, resp.StatusCode, excelBPSRetryAfterValue(resp.Header), time.Now())
		s.observeExcelBPSProviderFailure(ctx, account, model, providerFailure)
		modelAccessDenied := resp.StatusCode == http.StatusForbidden && code == string(ExcelBPSModelAccessChangedReason)
		// A failover attempt is only an event; the handler records the final state.
		kind := "failover"
		if resp.StatusCode != http.StatusTooManyRequests && !modelAccessDenied {
			kind = "http_error"
		}
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
			ProxyID: opsUpstreamProxyID(account), ProxyName: opsUpstreamProxyName(account),
			UpstreamStatusCode: resp.StatusCode, UpstreamRequestID: resp.Header.Get("x-request-id"),
			UpstreamURL: basispoints.ResponsesURL, Kind: kind,
			Message: upstreamMessage, Detail: upstreamDetail, UpstreamResponseBody: upstreamDetail,
		})
		// Recover a short throttle before cooling this route or returning its
		// failure. Every new send consumes the same HTTP/SSE/repair envelope.
		reserveSwitch := resp.StatusCode == http.StatusTooManyRequests && !providerFailure.permanent && s.excelBPSReserveAccountSwitch(ctx, c, account, originalModel, model, recovery)
		// Keepalive comments commit HTTP headers without delivering model output.
		// Only semantic output or an explicitly committed terminal forbids replay.
		if providerFailure.retry && !reserveSwitch && !openAIStreamClientOutputStarted(c, IsResponseCommitted(c)) && ctx.Err() == nil {
			_ = resp.Body.Close()
			releaseLease()
			if result, failure, retried := retryRequest(originalBody, providerFailure.delay); retried {
				return result, failure
			}
		}
		if resp.StatusCode == http.StatusTooManyRequests && !providerFailure.permanent {
			return failoverRateLimited(providerFailure.retryAfter, providerFailure.quotaModel == model && providerFailure.quotaGroupHash != "")
		}
		if !modelAccessDenied {
			setOpsUpstreamError(c, resp.StatusCode, upstreamMessage, upstreamDetail)
		}
		if resp.StatusCode == http.StatusUnauthorized {
			s.handleExcelBPSUnauthorized(ctx, account, resp.StatusCode, resp.Header, raw)
			if retryAfter := providerFailure.retryAfter; retryAfter != "" && !c.Writer.Written() {
				c.Header("Retry-After", retryAfter)
			}
			return fail(resp.StatusCode, "basispoints_upstream_error", "Excel BPS authentication failed; request was not replayed")
		}
		if modelAccessDenied {
			if isExcelBPSClientCancellation(c, ctx.Err()) {
				return clientCanceled()
			}
			if !StopOpenAICompactSSEKeepaliveCommitted(c) && !c.Writer.Written() && !IsResponseCommitted(c) {
				return nil, newExcelBPSModelAccessChangedFailoverError(model, providerFailure.retryAfter)
			}
			return fail(resp.StatusCode, code, excelBPSModelAccessChangedClientMessage)
		}
		if retryAfter := providerFailure.retryAfter; retryAfter != "" && !c.Writer.Written() {
			c.Header("Retry-After", retryAfter)
		}
		if code == "basispoints_model_access_changed" {
			return fail(resp.StatusCode, code, "This model is not available on the account's Excel BPS endpoint")
		}
		message := "Excel BPS rejected this request; account scheduling was not changed"
		errorCode := "basispoints_upstream_error"
		if resp.StatusCode == http.StatusBadRequest && isExcelBPSInvalidEncryptedContent(raw) {
			errorCode = "invalid_encrypted_content"
			message = "Excel BPS could not verify encrypted conversation state; resend the original plaintext history or start a new conversation"
		}
		if resp.StatusCode == http.StatusForbidden {
			// Apply group routing before the independent BPS route is paused.
			moved := s.moveExcelBPSOn403(ctx, account)
			disabled := s.disableExcelBPSOn403(ctx, account)
			switch {
			case moved && disabled:
				message = "Excel BPS rejected this request; BPS routing was paused while its saved setting was preserved and account groups were updated; request was not replayed"
			case disabled:
				message = "Excel BPS rejected this request; BPS routing was paused while its saved setting was preserved for this account; request was not replayed"
			case moved:
				message = "Excel BPS rejected this request; account groups were automatically updated; request was not replayed"
			}
		}
		return fail(resp.StatusCode, errorCode, message)
	}
	activity := &excelBPSActivityBody{ReadCloser: resp.Body}
	activity.lastRead.Store(time.Now().UnixNano())
	// BPS and Codex share quota. Refresh at the HTTP boundary even if the client
	// disconnects or a later stream/protocol error prevents normal completion.
	s.UpdateCodexUsageSnapshotFromHeaders(ctx, account.ID, resp.Header)
	// Tool repair, HTTP rejection and EOF regeneration share one recovery
	// budget. It survives recursive attempts and prevents a request from
	// spending one repair on every fresh stream.
	converted := bridge.StreamWithPreOutputRepairs(requestCtx, activity, func(repairCtx context.Context, failed map[string]any, validation error) (map[string]any, error) {
		repairCtx = excelBPSWithoutKeepalive(repairCtx)
		preserveFailure := func(failure excelBPSProviderFailure, header http.Header) error {
			usagePayload, _ := json.Marshal(map[string]any{"type": "response.failed", "response": map[string]any{"usage": failed["usage"]}})
			return basispoints.PreserveError(&excelBPSProviderRetryError{failure: failure, retryAfter: failure.retryAfter, usagePayload: usagePayload})
		}
		// A peer's model cooldown may block correction after the first response
		// was billed. Preserve that usage along with the model-scoped failure.
		preserveGateFailure := func(err error) error {
			failure := excelBPSPreserveSendGateError(err)
			var provider *excelBPSProviderRetryError
			if errors.As(failure, &provider) {
				provider.usagePayload, _ = json.Marshal(map[string]any{"type": "response.failed", "response": map[string]any{"usage": failed["usage"]}})
			}
			return basispoints.PreserveError(failure)
		}
		if excelBPSSourceRegenerationRequired(failed) {
			return nil, fmt.Errorf("basispoints source regeneration required: %w", validation)
		}
		if !recovery.consumeRepair() {
			return nil, fmt.Errorf("excel BPS shared recovery budget exhausted")
		}
		repairCtx, cancel := recovery.withDeadline(repairCtx)
		defer cancel()
		correctedBody, err := basispoints.BuildToolRepairRequest(upstreamBody, failed, validation)
		if err != nil {
			return nil, err
		}
		repairReq, err := newExcelBPSRequest(repairCtx, correctedBody, token, accountID)
		if err != nil {
			return nil, err
		}
		repairResp, err := s.sendExcelBPSWithQuota(c, account, model, repairReq, proxyURL, recovery)
		if err != nil {
			if repairResp != nil && repairResp.Body != nil {
				_ = repairResp.Body.Close()
			}
			var quotaWait *excelBPSQuotaWaitError
			if errors.As(err, &quotaWait) || errors.Is(err, errExcelBPSRequestTimeout) || IsOpenAIRPMError(err) {
				return nil, preserveGateFailure(err)
			}
			if errors.Is(context.Cause(repairCtx), errExcelBPSRequestTimeout) {
				return nil, preserveGateFailure(errExcelBPSRequestTimeout)
			}
			if repairCtx.Err() != nil {
				return nil, repairCtx.Err()
			}
			return nil, preserveFailure(excelBPSProviderFailure{status: http.StatusBadGateway, retry: excelBPSRetryableTransportError(err)}, nil)
		}
		defer func() { _ = repairResp.Body.Close() }()
		stop := context.AfterFunc(repairCtx, func() { _ = repairResp.Body.Close() })
		defer stop()
		if repairResp.StatusCode < 200 || repairResp.StatusCode >= 300 {
			raw, _ := io.ReadAll(io.LimitReader(repairResp.Body, 512<<10))
			providerFailure := excelBPSClassifyProviderFailure(raw, repairResp.StatusCode, excelBPSRetryAfterValue(repairResp.Header), time.Now())
			s.observeExcelBPSProviderFailure(repairCtx, account, model, providerFailure)
			if providerFailure.retry {
				return nil, preserveFailure(providerFailure, repairResp.Header)
			}
			if repairResp.StatusCode == http.StatusTooManyRequests {
				// Output was already accepted: cool the route, never replay the request.
				s.coolDownExcelBPS(repairCtx, account, providerFailure.retryAfter)
			}
			s.handleExcelBPSUnauthorized(repairCtx, account, repairResp.StatusCode, repairResp.Header, raw)
			if repairResp.StatusCode == http.StatusForbidden && gjson.GetBytes(raw, "error.code").String() != "basispoints_model_access_changed" {
				s.moveExcelBPSOn403(repairCtx, account)
				s.disableExcelBPSOn403(repairCtx, account)
			}
			return nil, fmt.Errorf("excel BPS correction returned HTTP %d", repairResp.StatusCode)
		}
		s.UpdateCodexUsageSnapshotFromHeaders(repairCtx, account.ID, repairResp.Header)
		upstreamBody = correctedBody
		repairedResponse, readErr := basispoints.ReadToolRepairResponse(&excelBPSRepairActivityBody{ReadCloser: repairResp.Body, activity: activity})
		if readErr != nil {
			raw, _ := json.Marshal(repairedResponse)
			providerFailure := excelBPSClassifyProviderFailure(raw, repairResp.StatusCode, excelBPSRetryAfterValue(repairResp.Header), time.Now())
			s.observeExcelBPSProviderFailure(repairCtx, account, model, providerFailure)
			if providerFailure.retry {
				return nil, preserveFailure(providerFailure, repairResp.Header)
			}
		}
		return repairedResponse, readErr
	}, func(repairCtx context.Context) (io.ReadCloser, error) {
		repairCtx = excelBPSWithoutKeepalive(repairCtx)
		if !recovery.consumeRepair() {
			return nil, fmt.Errorf("excel BPS shared recovery budget exhausted")
		}
		repairCtx, cancel := recovery.withDeadline(repairCtx)
		handedOff := false
		defer func() {
			if !handedOff {
				cancel()
			}
		}()
		repairBody, err := basispoints.RepairRequest(upstreamBody)
		if err != nil {
			return nil, err
		}
		retry, err := newExcelBPSRequest(repairCtx, repairBody, token, accountID)
		if err != nil {
			return nil, err
		}
		repaired, err := s.sendExcelBPSWithQuota(c, account, model, retry, proxyURL, recovery)
		if err != nil {
			if repaired != nil && repaired.Body != nil {
				_ = repaired.Body.Close()
			}
			var quotaWait *excelBPSQuotaWaitError
			if errors.As(err, &quotaWait) || errors.Is(err, errExcelBPSRequestTimeout) || IsOpenAIRPMError(err) {
				return nil, basispoints.PreserveError(excelBPSPreserveSendGateError(err))
			}
			if errors.Is(context.Cause(repairCtx), errExcelBPSRequestTimeout) {
				return nil, basispoints.PreserveError(excelBPSPreserveSendGateError(errExcelBPSRequestTimeout))
			}
			if repairCtx.Err() != nil {
				return nil, repairCtx.Err()
			}
			return nil, basispoints.PreserveError(&excelBPSProviderRetryError{failure: excelBPSProviderFailure{status: http.StatusBadGateway, retry: excelBPSRetryableTransportError(err)}})
		}
		if repaired.StatusCode < 200 || repaired.StatusCode >= 300 {
			raw, _ := io.ReadAll(io.LimitReader(repaired.Body, 512<<10))
			_ = repaired.Body.Close()
			providerFailure := excelBPSClassifyProviderFailure(raw, repaired.StatusCode, excelBPSRetryAfterValue(repaired.Header), time.Now())
			s.observeExcelBPSProviderFailure(repairCtx, account, model, providerFailure)
			if providerFailure.retry {
				return nil, basispoints.PreserveError(&excelBPSProviderRetryError{failure: providerFailure, retryAfter: providerFailure.retryAfter})
			}
			if repaired.StatusCode == http.StatusTooManyRequests {
				s.coolDownExcelBPS(repairCtx, account, providerFailure.retryAfter)
			}
			s.handleExcelBPSUnauthorized(repairCtx, account, repaired.StatusCode, repaired.Header, raw)
			if repaired.StatusCode == http.StatusForbidden && gjson.GetBytes(raw, "error.code").String() != "basispoints_model_access_changed" {
				s.moveExcelBPSOn403(repairCtx, account)
				s.disableExcelBPSOn403(repairCtx, account)
			}
			return nil, fmt.Errorf("excel BPS tool correction returned HTTP %d", repaired.StatusCode)
		}
		s.UpdateCodexUsageSnapshotFromHeaders(repairCtx, account.ID, repaired.Header)
		handedOff = true
		return &excelBPSRepairActivityBody{ReadCloser: &excelBPSRecoveryBody{ReadCloser: repaired.Body, cancel: cancel}, activity: activity}, nil
	})
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
	heartbeat := time.NewTimer(excelBPSKeepaliveDelay(ctx))
	defer heartbeat.Stop()
	keepalive := func() {
		_ = excelBPSWriteKeepalive(ctx)
		heartbeat.Reset(excelBPSKeepaliveDelay(ctx))
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
			if isExcelBPSClientCancellation(c, c.Request.Context().Err()) {
				MarkOpsClientCancellation(c, stream)
			}
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
	terminalStatus := http.StatusBadGateway
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
			if kind == "error" || kind == "response.failed" {
				providerFailure := excelBPSClassifyProviderFailure(payload, resp.StatusCode, excelBPSRetryAfterValue(resp.Header), time.Now())
				s.observeExcelBPSProviderFailure(ctx, account, model, providerFailure)
				if providerFailure.status == http.StatusTooManyRequests && !providerFailure.permanent && !outputCommitted && ctx.Err() == nil && s.excelBPSReserveAccountSwitch(ctx, c, account, originalModel, model, recovery) {
					return failoverRateLimited(providerFailure.retryAfter, providerFailure.quotaModel == model && providerFailure.quotaGroupHash != "")
				}
				// Inspect the provider terminal BEFORE it can commit downstream
				// state. Metadata and keepalives alone do not dispatch a tool.
				if providerFailure.retry && !outputCommitted && ctx.Err() == nil {
					if result, failure, retried := retryRequest(originalBody, providerFailure.delay, func() {
						scanner.Close()
						_ = converted.Close()
						_ = resp.Body.Close()
						releaseLease()
						appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
							Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
							UpstreamStatusCode: providerFailure.status, UpstreamRequestID: resp.Header.Get("x-request-id"),
							UpstreamURL: basispoints.ResponsesURL, Kind: "pre_output_provider_retry",
							Message: "BPS transient provider terminal before output; trying bounded recovery", Detail: excelBPSRecoveryDiagnostics(recovery, outputCommitted, kind, nil),
						})
					}); retried {
						return result, failure
					}
				}
				if providerFailure.retryAfter != "" && !c.Writer.Written() {
					c.Header("Retry-After", providerFailure.retryAfter)
				}
				if providerFailure.status >= 400 {
					terminalStatus = providerFailure.status
				}
				if providerFailure.code != "" {
					terminalCode = providerFailure.code
				}
			}
			protocolMessage := gjson.GetBytes(payload, "response.error.message").String()
			// Retry only a local transport failure before any client output.
			// No client tool has been dispatched; provider refusals are separate.
			correctable := excelBPSCorrectableProtocolError(protocolMessage)
			if correctable && kind == "response.failed" && gjson.GetBytes(payload, "response.error.code").String() == "basispoints_protocol_error" && !outputCommitted && ctx.Err() == nil {
				instructions := gjson.GetBytes(originalBody, "instructions").String()
				instructions += "\nTransport correction: the previous tool response was rejected before any client output or tool dispatch. Follow the exact catalog transport. For CUSTOM raw input, summary MUST be codex2api.custom/CATALOG_NAME and code MUST contain the raw string, never an object of arguments. For a declared FUNCTION_CODE tool, summary MUST be codex2api.function_code/CATALOG_NAME and extended_summary MUST contain the other arguments as JSON. Use the exact catalog name including its namespace. For ordinary FUNCTION, serialize one valid JSON envelope with escaped strings. Do not repeat the rejected wrapper or use a prose summary for raw source. Never invent tool names or execute wrapper code. Do not call native skills, workbook or connector tools. If the client catalog is empty, return assistant text only."
				corrected, correctionErr := sjson.SetBytes(originalBody, "instructions", instructions)
				if correctionErr == nil {
					scanner.Close()
					_ = converted.Close()
					_ = resp.Body.Close()
					releaseLease()
					logger.LegacyPrintf("service.openai_excel_bps", "retrying pre-output protocol failure once: account_id=%d", account.ID)
					appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
						Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
						UpstreamStatusCode: http.StatusBadGateway, UpstreamRequestID: resp.Header.Get("x-request-id"),
						Kind: "protocol_retry", Message: "Local BPS tool translation rejected before output; one correction attempt",
					})
					if result, failure, retried := retryRequest(corrected, 0); retried {
						return result, failure
					}
				}
			}
			clientOutput := openAIStreamDataStartsClientOutput(string(payload), kind)
			// Reasoning metadata may precede an invalid native call. Keep it in
			// the existing bounded buffer until actual text/tool output starts.
			if strings.HasPrefix(kind, "response.reasoning") || ((kind == "response.output_item.added" || kind == "response.output_item.done") && gjson.GetBytes(payload, "item.type").String() == "reasoning") {
				clientOutput = false
			}
			if clientOutput || openAIStreamDataStartsVisibleOutput(string(payload), kind) || kind == "response.completed" {
				recovery.acceptOutput()
			}
			// Non-streaming responses have delivered nothing until terminal.
			if (stream && clientOutput) || openAIStreamEventTypeIsTerminal(kind) {
				outputCommitted = true
				c.Set("excel_bps_output_committed", true)
				recovery.acceptOutput()
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
				if kind == "error" || kind == "response.failed" {
					message := "Excel BPS returned an upstream terminal failure"
					setOpsUpstreamError(c, terminalStatus, message, "")
					MarkOpsStreamError(c, terminalCode, message, terminalStatus)
				}
				if kind == "response.completed" && lease != nil {
					lease.ReportSuccess()
				}
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
				c.Set("excel_bps_output_committed", true)
				recovery.acceptOutput()
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
	if IsOpenAIRPMError(err) {
		return nil, err
	}
	var repairFailure *excelBPSProviderRetryError
	if errors.As(err, &repairFailure) {
		usagePayload := repairFailure.usagePayload
		if len(usagePayload) == 0 {
			usagePayload = basispoints.PreservedErrorUsage(err)
		}
		s.parseSSEUsageBytes(usagePayload, &result.Usage)
		if repairFailure.failure.retry && !outputCommitted && ctx.Err() == nil {
			if recovered, failure, retried := retryRequest(originalBody, repairFailure.failure.delay, func() {
				scanner.Close()
				_ = converted.Close()
				_ = resp.Body.Close()
				releaseLease()
				appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Platform: account.Platform, AccountID: account.ID, AccountName: account.Name, UpstreamStatusCode: repairFailure.failure.status, UpstreamURL: basispoints.ResponsesURL, Kind: "pre_output_repair_retry", Message: repairFailure.Error()})
			}); retried {
				if recovered != nil {
					recovered.Usage.InputTokens += result.Usage.InputTokens
					recovered.Usage.OutputTokens += result.Usage.OutputTokens
					recovered.Usage.ImageInputTokens += result.Usage.ImageInputTokens
					recovered.Usage.ImageOutputTokens += result.Usage.ImageOutputTokens
					recovered.Usage.ImageCacheReadTokens += result.Usage.ImageCacheReadTokens
					recovered.Usage.CacheCreationInputTokens += result.Usage.CacheCreationInputTokens
					recovered.Usage.CacheReadInputTokens += result.Usage.CacheReadInputTokens
				}
				return recovered, failure
			}
		}
		if repairFailure.failure.status == http.StatusTooManyRequests && !repairFailure.modelScoped && (repairFailure.failure.quotaGroupHash == "" || repairFailure.failure.quotaModel != model) {
			s.coolDownExcelBPS(ctx, account, repairFailure.retryAfter)
		}
		if retryAfter := repairFailure.retryAfter; retryAfter != "" && !c.Writer.Written() {
			c.Header("Retry-After", retryAfter)
		}
		result.streamReadIncomplete = true
		result.UpstreamTerminalEvent = "response.failed"
		setOpsUpstreamError(c, repairFailure.failure.status, repairFailure.Error(), "")
		MarkOpsStreamError(c, "basispoints_upstream_error", repairFailure.Error(), repairFailure.failure.status)
		_, failure := fail(repairFailure.failure.status, "basispoints_upstream_error", repairFailure.Error())
		return result, failure
	}
	// Before any visible output, retry clean EOF, EOF, idle/first-output and
	// network read failures within the shared attempt/time budget. Never replay
	// cancellation, flushed metadata, or delivered tool/text.
	if terminal == "" && !outputCommitted && ctx.Err() == nil && excelBPSRetryableStreamError(err) {
		// Report the broken exit before releasing its ownership. Otherwise the
		// same session can reacquire it, and exhausted recovery loses feedback
		// when releaseLease clears lease. Local time budgets do not prove that
		// the proxy failed; provider terminals and cancellation stay separate.
		if lease != nil && !errors.Is(err, errOpenAISSEIdle) && !errors.Is(err, errOpenAISSEFirstOutput) && !errors.Is(err, context.DeadlineExceeded) {
			lease.ReportStreamFailure()
		}
		scanner.Close()
		_ = converted.Close()
		_ = resp.Body.Close()
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
			UpstreamStatusCode: resp.StatusCode, UpstreamRequestID: resp.Header.Get("x-request-id"),
			Kind: "pre_output_stream_retry", Message: "BPS stream failed before output; trying bounded recovery", Detail: excelBPSRecoveryDiagnostics(recovery, outputCommitted, terminal, err),
		})
		releaseLease()
		if result, failure, retried := retryRequest(originalBody, 0); retried {
			return result, failure
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
		if errors.Is(err, errOpenAISSEIdle) || errors.Is(err, errOpenAISSEFirstOutput) || errors.Is(context.Cause(ctx), errExcelBPSRequestTimeout) || (!outputCommitted && excelBPSRecoveryBudgetExpired(recovery)) {
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
			if isExcelBPSClientCancellation(c, ctx.Err()) {
				MarkOpsClientCancellation(c, stream)
			}
			result.ClientDisconnect = true
			return result, ctx.Err()
		}
		if lease != nil {
			lease.ReportStreamFailure()
		}
		recordExcelBPSTransportFailure(ctx, c, account, scope, proxyURL, err, "stream", c.GetInt("excel_bps_upstream_attempt"), false)
		MarkOpsStreamError(c, "basispoints_stream_incomplete", "Excel BPS stream ended before completion", http.StatusBadGateway)
		result.streamReadIncomplete = true
		MarkResponseCommitted(c)
		if stream {
			writeExcelBPSStreamFailure(c, chat, downstream, http.StatusBadGateway, "basispoints_stream_incomplete", "Upstream stream ended before completion")
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
			writeExcelBPSJSONError(c, messages, terminalStatus, terminalCode, "Excel BPS did not complete the response")
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
var excelBPSAttachmentIDPattern = regexp.MustCompile(`\bfile-[A-Za-z0-9_-]+`)
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
		clean = excelBPSAttachmentIDPattern.ReplaceAllString(clean, "file-[redacted]")
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

// Explicit conversation identity remains sticky. Anonymous requests get a
// request-local identity reused across internal retries, never across callers.
func resolveExcelBPSIdentity(c *gin.Context, body []byte, apiKeyID int64, managed bool) (string, bool) {
	if identity, _ := resolveOpenAIWSExecutionScope(c, body, apiKeyID); identity != "" {
		return identity, false
	}
	if !managed {
		return "", false
	}
	const key = "excel_bps_transient_identity"
	if identity := c.GetString(key); identity != "" {
		return identity, true
	}
	identity := uuid.NewString()
	c.Set(key, identity)
	return identity, true
}

// Keep the stream scanner's byte-idle clock alive while a bounded pre-output
// correction is reading. The same total deadline still limits every attempt.
type excelBPSRepairActivityBody struct {
	io.ReadCloser
	activity *excelBPSActivityBody
}

func (b *excelBPSRepairActivityBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.activity.lastRead.Store(time.Now().UnixNano())
	}
	return n, err
}
