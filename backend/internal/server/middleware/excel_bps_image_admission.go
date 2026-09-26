package middleware

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const (
	bpsImageMaxBodyBytes   = 64 << 20
	bpsImageBudgetBytes    = 512 << 20
	bpsImageBodyMultiplier = 8
	bpsImageMinBodyBytes   = 1 << 20
	bpsImageMaxRequests    = 32
)

type excelBPSImageSettingsReader interface {
	GetExcelBPSImageRelaySettings(context.Context) (service.ExcelBPSImageRelaySettings, error)
}

// Reservations cover processing copies, not measured RSS. Decode leases last
// only until handoff; processing leases cover retained bodies and scheduler/
// upstream waits until the entire request returns. No body queue.
type bpsImageAdmissionBudget struct {
	mu          sync.Mutex
	bytes       int64
	requests    int
	maxBytes    int64
	maxRequests int
	changed     chan struct{}
}

type bpsImageReservation struct {
	budget   *bpsImageAdmissionBudget
	weight   int64
	released bool
}

func (b *bpsImageAdmissionBudget) reserve(weight int64) (*bpsImageReservation, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reserveLocked(weight)
}

func (b *bpsImageAdmissionBudget) reserveLocked(weight int64) (*bpsImageReservation, bool) {
	maxBytes, maxRequests := b.maxBytes, b.maxRequests
	if maxBytes == 0 {
		maxBytes = bpsImageBudgetBytes
	}
	if maxRequests == 0 {
		maxRequests = bpsImageMaxRequests
	}
	if weight < 0 || b.requests >= maxRequests || weight > maxBytes-b.bytes {
		return nil, false
	}
	b.bytes += weight
	b.requests++
	return &bpsImageReservation{budget: b, weight: weight}, true
}

// Waiters already hold a processing lease, so their number is bounded by the
// active-request limit. No request body is read or buffered while waiting.
// A short wait absorbs decode microbursts without increasing peak memory.
func (b *bpsImageAdmissionBudget) reserveWaiting(ctx context.Context, weight int64, wait time.Duration) (*bpsImageReservation, bool) {
	deadline := time.Now().Add(wait)
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		b.mu.Lock()
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			b.mu.Unlock()
			return nil, false
		}
		if lease, ok := b.reserveLocked(weight); ok {
			b.mu.Unlock()
			return lease, true
		}
		if b.changed == nil {
			b.changed = make(chan struct{})
		}
		changed := b.changed
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, false
		case <-timer.C:
			return nil, false
		case <-changed:
		}
	}
}

func (b *bpsImageAdmissionBudget) acquire(weight int64) (func(), bool) {
	r, ok := b.reserve(weight)
	if !ok {
		return nil, false
	}
	return r.release, true
}

func (r *bpsImageReservation) resize(weight int64) bool {
	b := r.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	limit := b.maxBytes
	if limit == 0 {
		limit = bpsImageBudgetBytes
	}
	if r.released || weight < 0 || weight-r.weight > limit-b.bytes {
		return false
	}
	b.bytes += weight - r.weight
	r.weight = weight
	return true
}

func (r *bpsImageReservation) release() {
	b := r.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.released {
		return
	}
	b.bytes -= r.weight
	b.requests--
	r.released = true
	if b.changed != nil {
		close(b.changed)
		b.changed = nil
	}
}

func (b *bpsImageAdmissionBudget) snapshot() (int64, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bytes, b.requests
}

// One shared handler covers all aliases. A compressed request reserves the
// conservative maximum ONLY while reading/decoding. After bounded decoding its
// actual size is charged to a separate processing budget before decode release.
func ExcelBPSImageAdmission(settings excelBPSImageSettingsReader, configuredMax int64, options ...config.ImageRelayAdmissionConfig) gin.HandlerFunc {
	var cfg config.ImageRelayAdmissionConfig
	if len(options) > 0 {
		cfg = options[0]
	}
	cfg = cfg.WithDefaults()
	decode := &bpsImageAdmissionBudget{maxBytes: cfg.DecodeBudgetBytes, maxRequests: cfg.DecodeMaxConcurrent}
	processing := &bpsImageAdmissionBudget{maxBytes: cfg.ProcessingBudgetBytes, maxRequests: cfg.MaxConcurrentRequests}
	maxBody := int64(bpsImageMaxBodyBytes)
	if configuredMax > 0 && configuredMax < maxBody {
		maxBody = configuredMax
	}
	return func(c *gin.Context) {
		if settings == nil || !bpsImageAdmissionRoute(c) {
			c.Next()
			return
		}
		key, ok := GetAPIKeyFromContext(c)
		if !ok || key == nil || (key.Group != nil && key.Group.Platform != service.PlatformOpenAI && key.Group.Platform != service.PlatformComposite) {
			c.Next()
			return
		}
		relay, err := settings.GetExcelBPSImageRelaySettings(c.Request.Context())
		if err != nil {
			bpsImageAdmissionError(c, 503, "basispoints_image_settings_unavailable", "Image relay settings are unavailable")
			return
		}
		if !relay.Enabled {
			c.Next()
			return
		}
		wireLength, encoding := c.Request.ContentLength, c.GetHeader("Content-Encoding")
		if wireLength > maxBody {
			bpsImageAdmissionError(c, 413, "basispoints_image_body_too_large", "Request body exceeds the image relay ingress limit")
			return
		}
		reject := func(reason string, decoded, requested int64) {
			db, dn := decode.snapshot()
			pb, pn := processing.snapshot()
			logger.FromContext(c.Request.Context()).Warn("image relay admission rejected",
				zap.String("component", "gateway.image_relay_admission"), zap.String("reason", reason),
				zap.String("encoding", encoding), zap.Int64("wire_bytes", wireLength), zap.Int64("decoded_bytes", decoded),
				zap.Int64("requested_bytes", requested), zap.Int64("decode_bytes", db), zap.Int("decode_active", dn),
				zap.Int64("processing_bytes", pb), zap.Int("processing_active", pn),
				zap.Int64("processing_limit_bytes", cfg.ProcessingBudgetBytes), zap.Int("active_limit", cfg.MaxConcurrentRequests))
			bpsImageAdmissionError(c, 503, "basispoints_image_request_busy", "Image relay request capacity is busy; retry later")
		}
		// Reject a full process before reading more bodies or using a decoder.
		processLease, ok := processing.reserve(bpsImageMinBodyBytes * bpsImageBodyMultiplier)
		if !ok {
			_, active := processing.snapshot()
			reason := "processing_bytes"
			if active >= cfg.MaxConcurrentRequests {
				reason = "active_requests"
			}
			reject(reason, -1, bpsImageMinBodyBytes*bpsImageBodyMultiplier)
			return
		}
		defer processLease.release()
		decodeLease, ok := decode.reserveWaiting(c.Request.Context(), config.ImageRelayDecodeReservationBytes, time.Duration(cfg.DecodeWaitMilliseconds)*time.Millisecond)
		if !ok {
			if c.Request.Context().Err() != nil {
				c.Abort()
				return
			}
			reject("decode_capacity", -1, config.ImageRelayDecodeReservationBytes)
			return
		}
		defer decodeLease.release()
		started := time.Now()
		body, err := readBPSAdmissionBody(c, maxBody, time.Duration(cfg.BodyReadTimeoutSeconds)*time.Second)
		if err != nil {
			var tooLarge *http.MaxBytesError
			var timeout net.Error
			switch {
			case errors.As(err, &tooLarge):
				bpsImageAdmissionError(c, 413, "basispoints_image_body_too_large", "Request body exceeds the image relay ingress limit")
			case errors.Is(err, context.Canceled):
				c.Abort()
				return
			case errors.As(err, &timeout) && timeout.Timeout():
				bpsImageAdmissionError(c, 408, "basispoints_image_body_timeout", "Request body read timed out")
			default:
				bpsImageAdmissionError(c, 400, "basispoints_image_body_invalid", "Invalid or unsupported request body encoding")
			}
			return
		}
		decoded := int64(len(body))
		weight := max(decoded, int64(bpsImageMinBodyBytes)) * bpsImageBodyMultiplier
		if !processLease.resize(weight) {
			reject("processing_bytes", decoded, weight)
			return
		}
		// Downstream retains body slices for retries/capture: hold the actual-body
		// lease through c.Next, never just until response headers arrive.
		c.Request.Body = httputil.NewPrereadBody(body)
		c.Request.ContentLength = decoded
		c.Request.Header.Del("Content-Encoding")
		c.Request.Header.Del("Content-Length")
		decodeLease.release()
		logger.FromContext(c.Request.Context()).Debug("image relay admission accepted",
			zap.String("component", "gateway.image_relay_admission"), zap.String("encoding", encoding),
			zap.Int64("wire_bytes", wireLength), zap.Int64("decoded_bytes", decoded),
			zap.Int64("reserved_bytes", weight), zap.Duration("decode_duration", time.Since(started)))
		c.Next()
	}
}

func readBPSAdmissionBody(c *gin.Context, limit int64, timeout time.Duration) ([]byte, error) {
	controller := http.NewResponseController(c.Writer)
	originalBody := c.Request.Body
	deadlineSet := false
	defer func() {
		// Close while the read deadline is still in force. Clearing it first
		// lets net/http drain a stalled upload while writing the 408 response.
		if originalBody != nil {
			_ = originalBody.Close()
		}
		if deadlineSet {
			_ = controller.SetReadDeadline(time.Time{})
		}
	}()
	// Network response writers expose deadlines through Unwrap; in-memory
	// test writers legitimately return ErrNotSupported.
	if err := controller.SetReadDeadline(time.Now().Add(timeout)); err == nil {
		deadlineSet = true
	} else if !errors.Is(err, http.ErrNotSupported) {
		return nil, err
	}
	if err := c.Request.Context().Err(); err != nil {
		return nil, err
	}
	body, err := httputil.ReadRequestBodyBounded(c.Writer, c.Request, limit)
	if err != nil {
		return nil, err
	}
	if err = c.Request.Context().Err(); err != nil {
		return nil, err
	}
	return body, nil
}

func bpsImageAdmissionRoute(c *gin.Context) bool {
	if c.Request.Method != http.MethodPost {
		return false
	}
	switch c.FullPath() {
	case "/responses", "/responses/*subpath", "/v1/responses", "/v1/responses/*subpath", "/backend-api/codex/responses", "/backend-api/codex/responses/*subpath", "/chat/completions", "/v1/chat/completions", "/v1/messages":
		return true
	}
	return false
}

func bpsImageAdmissionError(c *gin.Context, status int, code, message string) {
	errorType := "server_error"
	if status == 413 || status == 400 || status == 408 {
		errorType = "invalid_request_error"
	}
	if status == 503 {
		c.Header("Retry-After", "1")
	}
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"type": errorType, "code": code, "message": message}})
}
