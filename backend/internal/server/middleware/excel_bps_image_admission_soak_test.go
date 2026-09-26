package middleware

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Opt-in; no real upstream, credentials or billing. Exercise the actual HTTP
// ingress while another compressed request remains streaming for the whole run.
func TestExcelBPSImageAdmissionSoak(t *testing.T) {
	duration, err := time.ParseDuration(os.Getenv("SUB2API_ADMISSION_SOAK_DURATION"))
	if err != nil || duration <= 0 {
		t.Skip("set SUB2API_ADMISSION_SOAK_DURATION, e.g. 30m")
	}
	if duration > time.Hour {
		t.Fatal("soak duration must not exceed 1h")
	}
	payload := []byte(`{"input":"bounded compressed request","stream":true}`)
	encoder, err := zstd.NewWriter(nil)
	require.NoError(t, err)
	compressed := encoder.EncodeAll(payload, nil)
	require.NoError(t, encoder.Close())
	held := make(chan struct{})
	r := admissionRouter(config.ImageRelayAdmissionConfig{}, func(c *gin.Context) {
		body, err := httputil.ReadRequestBodyWithPrealloc(c.Request)
		if err != nil || !bytes.Equal(body, payload) {
			c.Status(400)
			return
		}
		if c.GetHeader("Hold") == "true" {
			c.Header("Content-Type", "text/event-stream")
			_, _ = c.Writer.WriteString(": ready\n\n")
			c.Writer.Flush()
			close(held)
			<-c.Request.Context().Done()
			return
		}
		c.Status(204)
	})
	nop := zap.NewNop()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.ServeHTTP(w, req.WithContext(logger.IntoContext(req.Context(), nop)))
	}))
	defer s.Close()
	client := s.Client()
	holdCtx, stopHold := context.WithCancel(context.Background())
	defer stopHold()
	req, err := http.NewRequestWithContext(holdCtx, "POST", s.URL+"/v1/responses", bytes.NewReader(compressed))
	require.NoError(t, err)
	req.Header.Set("Content-Encoding", "zstd")
	req.Header.Set("Hold", "true")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	<-held
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	var ok, busy, unexpected atomic.Int64
	var wg sync.WaitGroup
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
				wire := payload
				if worker%2 == 0 {
					wire = compressed
				}
				request, e := http.NewRequestWithContext(ctx, "POST", s.URL+"/v1/responses", bytes.NewReader(wire))
				if e != nil {
					unexpected.Add(1)
					return
				}
				if worker%2 == 0 {
					request.Header.Set("Content-Encoding", "zstd")
				}
				response, e := client.Do(request)
				if e != nil {
					if ctx.Err() == nil {
						unexpected.Add(1)
					}
					return
				}
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				switch response.StatusCode {
				case 204:
					ok.Add(1)
				case 503:
					busy.Add(1)
				default:
					unexpected.Add(1)
				}
			}
		}(worker)
	}
	wg.Wait()
	stopHold()
	_ = resp.Body.Close()
	runtime.GC()
	runtime.ReadMemStats(&after)
	fmt.Printf("ADMISSION_SOAK elapsed=%s success=%d bounded_busy=%d unexpected=%d heap_before=%d heap_after=%d\n", time.Since(start).Round(time.Millisecond), ok.Load(), busy.Load(), unexpected.Load(), before.HeapAlloc, after.HeapAlloc)
	require.Positive(t, ok.Load())
	require.Zero(t, busy.Load(), "small concurrent requests must pass while a stream stays open")
	require.Zero(t, unexpected.Load())
}
