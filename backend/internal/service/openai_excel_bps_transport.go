package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/mihomo"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"github.com/Wei-Shaw/sub2api/internal/util/transportdiag"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

var errExcelBPSProxyUnavailable = errors.New("BPS proxy unavailable")

// Keep the cause available to diagnostics without exposing a supplier URL in
// the error string if a caller logs the returned error.
type excelBPSAcquisitionFailure struct{ cause error }

func (e *excelBPSAcquisitionFailure) Error() string { return errExcelBPSProxyUnavailable.Error() }
func (e *excelBPSAcquisitionFailure) Unwrap() []error {
	return []error{errExcelBPSProxyUnavailable, e.cause}
}

type excelBPSLease interface {
	Release()
	ReportFailure()
	ReportStreamFailure()
	ReportSuccess()
	ReportUpstreamFailure()
}

type excelBPSAcquire func(context.Context, string, ...string) (string, excelBPSLease, error)

func acquireExcelBPSProxy(ctx context.Context, scope string, excluded ...string) (string, excelBPSLease, error) {
	acquire := mihomo.AcquireBPSLease
	if strings.HasPrefix(scope, "transient:") {
		acquire = mihomo.AcquireBPSTransientLease
	}
	lease, err := acquire(ctx, scope, excluded...)
	if err != nil {
		return "", nil, err
	}
	return lease.ProxyURL, lease, nil
}

// excelBPSAcquireFor routes the managed session to the pool the account chose.
// Both pools share binding/scoring semantics, so callers keep one acquire shape.
func (s *OpenAIGatewayService) excelBPSAcquireFor(account *Account) excelBPSAcquire {
	if account.ExcelBPSProxySource() == ExcelBPSProxySourceIPPool {
		return s.acquireExcelBPSIPPoolProxy
	}
	return acquireExcelBPSProxy
}

// The background warm worker refreshes IP-management membership with a short
// TTL. Requests only consume the shared pool and never query the proxy database.
const excelBPSIPPoolRefreshTTL = 15 * time.Second

func (s *OpenAIGatewayService) acquireExcelBPSIPPoolProxy(ctx context.Context, scope string, excluded ...string) (string, excelBPSLease, error) {
	acquire := mihomo.AcquireBPSStaticLease
	if strings.HasPrefix(scope, "transient:") {
		acquire = mihomo.AcquireBPSStaticTransientLease
	}
	lease, err := acquire(ctx, scope, excluded...)
	if err != nil {
		return "", nil, err
	}
	return lease.ProxyURL, lease, nil
}

func (s *OpenAIGatewayService) refreshExcelBPSIPPool(ctx context.Context) {
	if s.proxyRepo == nil {
		return
	}
	now := time.Now()
	s.excelBPSIPPoolMu.Lock()
	fresh := now.Before(s.excelBPSIPPoolSyncedAt.Add(excelBPSIPPoolRefreshTTL))
	if !fresh {
		s.excelBPSIPPoolSyncedAt = now
	}
	s.excelBPSIPPoolMu.Unlock()
	if fresh {
		return
	}
	proxies, err := s.proxyRepo.ListActive(ctx)
	if err != nil {
		// Keep the last pushed membership on a transient listing error; health
		// state and cooldowns still gate the exits that remain in the pool.
		logger.FromContext(ctx).Warn("excel_bps.ip_pool_refresh_failed", zap.Error(err))
		return
	}
	urls := make([]string, 0, len(proxies))
	for i := range proxies {
		if proxies[i].IsExpired(now) {
			continue
		}
		urls = append(urls, proxies[i].URL())
	}
	mihomo.SetBPSStaticProxies(urls)
}

// Missing trace is not evidence of safety. Standard net/http emits GetConn
// before dialing and GotConn before handing a connection to request writing.
// Keep evidence for the whole attempt, including any internal reconnects.
type excelBPSWriteEvidence struct{ transportdiag.Trace }

func (e *excelBPSWriteEvidence) request(req *http.Request) *http.Request {
	req = e.Request(req)
	if req.Body != nil {
		req.Body = &excelBPSTrackedBody{ReadCloser: req.Body, mark: e.MarkBodyRead}
	}
	if getBody := req.GetBody; getBody != nil {
		req.GetBody = func() (io.ReadCloser, error) {
			body, err := getBody()
			if err != nil {
				return nil, err
			}
			return &excelBPSTrackedBody{ReadCloser: body, mark: e.MarkBodyRead}, nil
		}
	}
	return req
}
func (e *excelBPSWriteEvidence) unsent() bool { return e.DefinitelyUnsent() }

type excelBPSTrackedBody struct {
	io.ReadCloser
	mark func()
}

func (b *excelBPSTrackedBody) Read(p []byte) (int, error) {
	if len(p) > 0 {
		b.mark()
	}
	return b.ReadCloser.Read(p)
}

// At most one extra model attempt, on another healthy managed exit, and only
// before HTTP could have written anything. The caller owns the returned lease
// through response closure. Static account proxies retain their old behavior.
func (s *OpenAIGatewayService) doExcelBPSRequest(ctx context.Context, c *gin.Context, account *Account, scope string, body []byte, token, accountID string, acquire excelBPSAcquire, recovery ...*excelBPSRecovery) (*http.Response, excelBPSLease, string, error) {
	managed := account.IsExcelBPSMihomoEnabled()
	var excluded []string
	proxy := ""
	if account.Proxy != nil {
		proxy = account.Proxy.URL()
	}
	var lastError error
	var retryCancel context.CancelFunc
	var quotaCancel context.CancelFunc
	handedOff := false
	defer func() {
		if retryCancel != nil && !handedOff {
			retryCancel()
		}
		if quotaCancel != nil && !handedOff {
			quotaCancel()
		}
	}()
	for attempt := 1; attempt <= 2; attempt++ {
		if attempt > 1 && len(recovery) > 0 && recovery[0] != nil {
			if !recovery[0].consumeRepair() {
				return nil, nil, proxy, lastError
			}
			ctx, retryCancel = recovery[0].withDeadline(ctx)
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, proxy, err
		}
		var recoveryState *excelBPSRecovery
		if len(recovery) > 0 {
			recoveryState = recovery[0]
		}
		sendCtx, cancelSend, sendErr := s.excelBPSQuotaSendContext(ctx, account, gjson.GetBytes(body, "model").String(), recoveryState)
		if sendErr != nil {
			return nil, nil, proxy, sendErr
		}
		quotaCancel = cancelSend
		var lease excelBPSLease
		if managed {
			var err error
			proxy, lease, err = acquire(sendCtx, scope, excluded...)
			if err != nil {
				if errors.Is(context.Cause(sendCtx), errExcelBPSRequestTimeout) {
					return nil, nil, proxy, errExcelBPSRequestTimeout
				}
				if ctx.Err() != nil {
					return nil, nil, proxy, ctx.Err()
				}
				err = &excelBPSAcquisitionFailure{cause: err}
				recordExcelBPSTransportFailure(ctx, c, account, scope, proxy, err, "proxy_acquisition", attempt, false)
				return nil, nil, proxy, err
			}
		}
		req, err := newExcelBPSRequest(sendCtx, body, token, accountID)
		if err != nil {
			if lease != nil {
				lease.Release()
			}
			return nil, nil, proxy, err
		}
		// Admission reserves the first slot; every actual BPS send, including
		// transport retries, acquires a slot immediately before Do.
		evidence := &excelBPSWriteEvidence{}
		resp, err := s.sendExcelBPSWithQuota(c, account, gjson.GetBytes(body, "model").String(), evidence.request(req), proxy, recoveryState)
		if err != nil && errors.Is(context.Cause(sendCtx), errExcelBPSRequestTimeout) {
			err = errExcelBPSRequestTimeout
		}
		var quotaWait *excelBPSQuotaWaitError
		if err != nil && (IsOpenAIRPMError(err) || errors.As(err, &quotaWait) || errors.Is(err, errExcelBPSRequestTimeout)) {
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			if lease != nil {
				lease.Release()
			}
			cancelSend()
			return nil, nil, proxy, err
		}
		if err == nil {
			if resp != nil && resp.Body != nil {
				resp.Body = &excelBPSRecoveryBody{ReadCloser: resp.Body, cancel: func() {
					cancelSend()
					if retryCancel != nil {
						retryCancel()
					}
				}}
				handedOff = true
			}
			return resp, lease, proxy, nil
		}
		lastError = err
		cancelSend()
		// Even an unusual response+error result makes replay unsafe.
		retry := managed && attempt == 1 && resp == nil && evidence.unsent() && ctx.Err() == nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, errExcelBPSRequestTimeout)
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if lease != nil {
			if ctx.Err() == nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				lease.ReportFailure()
			}
			lease.Release()
		}
		recordExcelBPSTransportFailure(ctx, c, account, scope, proxy, err, "transport", attempt, retry, evidence)
		if !retry {
			return nil, nil, proxy, err
		}
		excluded = append(excluded, proxy)
	}
	return nil, nil, proxy, errExcelBPSProxyUnavailable
}

func excelBPSLocalProxyPort(proxy string) int {
	u, err := url.Parse(proxy)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil {
		return 0
	}
	port, _ := strconv.Atoi(u.Port())
	if port < 19000 || port >= 23096 {
		return 0
	}
	return port
}

func recordExcelBPSTransportFailure(ctx context.Context, c *gin.Context, account *Account, scope, proxy string, err error, stage string, attempt int, retry bool, evidence ...*excelBPSWriteEvidence) {
	if isExcelBPSClientCancellation(c, err) {
		logger.FromContext(ctx).Info("excel_bps.client_canceled",
			zap.Int64("account_id", account.ID), zap.String("stage", stage))
		return
	}
	kind := transportdiag.Classify(err)
	if errors.Is(err, errExcelBPSProxyUnavailable) {
		kind = "proxy_unavailable"
	}
	digest := sha256.Sum256([]byte(scope))
	sessionHash := hex.EncodeToString(digest[:8])
	port := 0
	if account.IsExcelBPSMihomoEnabled() {
		port = excelBPSLocalProxyPort(proxy)
	}
	diagnostics := map[string]any{
		"error_kind": kind, "error_type": fmt.Sprintf("%T", err),
		"proxy_port": port, "session_hash": sessionHash, "attempt": attempt, "retry_before_send": retry,
		"output_committed": c.GetBool("excel_bps_output_committed"),
	}
	if stored, ok := c.Get("excel_bps_shared_recovery"); ok {
		if recovery, ok := stored.(*excelBPSRecovery); ok {
			recovery.mu.Lock()
			diagnostics["remaining_attempts"] = recovery.remaining
			remaining := recovery.budget
			if !recovery.deadline.IsZero() {
				remaining = time.Until(recovery.deadline)
				if remaining < 0 {
					remaining = 0
				}
			}
			diagnostics["budget_remaining_ms"] = remaining.Milliseconds()
			recovery.mu.Unlock()
		}
	}
	if len(evidence) > 0 && evidence[0] != nil {
		diagnostics["transport"] = evidence[0].Snapshot()
	}
	var acquisition *mihomo.BPSAcquireError
	if errors.As(err, &acquisition) {
		diagnostics["acquisition_reason"] = acquisition.Reason
		diagnostics["candidates_checked"] = acquisition.Candidates
	}
	detail, _ := json.Marshal(diagnostics)
	message := "Excel BPS " + stage + " failed: " + kind
	// Keep UI client errors generic; persist only explicitly safe diagnostics.
	if !retry {
		setOpsUpstreamError(c, 0, message, string(detail))
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform: account.Platform, AccountID: account.ID,
		UpstreamURL: basispoints.ResponsesURL, Kind: "request_error", Stage: stage,
		Scope: "excel_bps", Reason: kind, Message: message, Detail: string(detail),
	})
	logger.FromContext(ctx).Warn("excel_bps.transport_failed",
		zap.Int64("account_id", account.ID), zap.String("stage", stage),
		zap.String("error_kind", kind), zap.String("error_type", fmt.Sprintf("%T", err)),
		zap.Int("proxy_port", port), zap.String("session_hash", sessionHash),
		zap.Int("attempt", attempt), zap.Bool("retry_before_send", retry),
		zap.Any("transport", diagnostics["transport"]), zap.Any("acquisition_reason", diagnostics["acquisition_reason"]), zap.Any("candidates_checked", diagnostics["candidates_checked"]))
}

// Attachment requests own their lease through upload, generation and correction.
// A borrowed lease reports health but cannot release the caller's ownership.
type excelBPSBorrowedLease struct{ excelBPSLease }

func (excelBPSBorrowedLease) Release() {}
func pinnedExcelBPSAcquire(proxy string, lease excelBPSLease) excelBPSAcquire {
	return func(ctx context.Context, _ string, excluded ...string) (string, excelBPSLease, error) {
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		// An attachment has already been sent on this exit. Never move this request
		// to another node, even when the later Responses request was not sent.
		if len(excluded) != 0 {
			return "", nil, errExcelBPSProxyUnavailable
		}
		return proxy, excelBPSBorrowedLease{lease}, nil
	}
}
