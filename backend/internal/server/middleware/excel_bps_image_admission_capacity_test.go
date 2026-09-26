package middleware

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
)

func admissionRouter(cfg config.ImageRelayAdmissionConfig, next gin.HandlerFunc) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyAPIKey), &service.APIKey{Group: &service.Group{Platform: service.PlatformOpenAI}})
		c.Next()
	})
	r.Use(ExcelBPSImageAdmission(bpsImageTestSettings{enabled: true}, 64<<20, cfg))
	r.POST("/v1/responses", next)
	return r
}

func TestExcelBPSImageAdmissionDecodeAndProcessingAreIndependent(t *testing.T) {
	initMiddlewareTestLogger(t)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	enc, err := zstd.NewWriter(nil)
	require.NoError(t, err)
	payload := enc.EncodeAll([]byte(`{"input":"hello"}`), nil)
	require.NoError(t, enc.Close())
	r := admissionRouter(config.ImageRelayAdmissionConfig{}, func(c *gin.Context) {
		b, err := httputil.ReadRequestBodyWithPrealloc(c.Request)
		if err != nil || string(b) != `{"input":"hello"}` {
			c.Status(400)
			return
		}
		if c.GetHeader("Hold") == "1" {
			close(entered)
			<-release
		}
		c.Status(200)
	})
	request := func(hold bool) int {
		req := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(payload))
		req.Header.Set("Content-Encoding", "zstd")
		if hold {
			req.Header.Set("Hold", "1")
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	go func() { defer close(done); request(true) }()
	defer func() { close(release); <-done }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first request stuck")
	}
	for i := 0; i < 10; i++ {
		require.Equal(t, 200, request(false), "long stream must not retain decoder budget")
	}
}

type admissionBlockedBody struct {
	entered chan struct{}
	release chan struct{}
	sent    bool
}

func (b *admissionBlockedBody) Read(p []byte) (int, error) {
	if b.sent {
		return 0, io.EOF
	}
	b.sent = true
	close(b.entered)
	<-b.release
	return copy(p, "test"), nil
}
func (*admissionBlockedBody) Close() error { return nil }

func TestExcelBPSImageAdmissionRejectsWhileDecodingWithoutReading(t *testing.T) {
	sink := initMiddlewareTestLogger(t)
	body := &admissionBlockedBody{entered: make(chan struct{}), release: make(chan struct{})}
	r := admissionRouter(config.ImageRelayAdmissionConfig{}, func(c *gin.Context) { c.Status(200) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/responses", body))
	}()
	defer func() { close(body.release); <-done }()
	select {
	case <-body.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("decoder did not enter")
	}
	var reads atomic.Int32
	req := httptest.NewRequest("POST", "/v1/responses", &bpsImageCountingBody{reads: &reads, reader: strings.NewReader("test")})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, 503, w.Code)
	require.Zero(t, reads.Load())
	events := sink.list()
	require.NotEmpty(t, events)
	require.Equal(t, "decode_capacity", events[len(events)-1].Fields["reason"])
}

func TestExcelBPSImageAdmissionBadEncodingReleasesBudget(t *testing.T) {
	r := admissionRouter(config.ImageRelayAdmissionConfig{MaxConcurrentRequests: 1}, func(c *gin.Context) { c.Status(200) })
	for _, encoding := range []string{"zstd", "gzip", "unsupported", ""} {
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader("test"))
		req.Header.Set("Content-Encoding", encoding)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if encoding == "" {
			require.Equal(t, 200, w.Code)
		} else {
			require.Equal(t, 400, w.Code)
		}
	}
}

func TestExcelBPSImageAdmissionProcessingHandoffIsBounded(t *testing.T) {
	b := &bpsImageAdmissionBudget{maxBytes: 16 << 20, maxRequests: 3}
	first, ok := b.reserve(8 << 20)
	require.True(t, ok)
	second, ok := b.reserve(8 << 20)
	require.True(t, ok)
	require.False(t, second.resize(16<<20))
	require.Equal(t, int64(8<<20), second.weight)
	first.release()
	require.True(t, second.resize(16<<20))
	second.release()
	second.release()
	require.False(t, second.resize(0))
	used, n := b.snapshot()
	require.Zero(t, used)
	require.Zero(t, n)
}

func TestExcelBPSImageAdmissionDecodeWaitReusesReleasedSlot(t *testing.T) {
	b := &bpsImageAdmissionBudget{maxBytes: 8, maxRequests: 1}
	held, ok := b.reserve(8)
	require.True(t, ok)
	defer held.release()
	result := make(chan *bpsImageReservation, 1)
	go func() {
		lease, _ := b.reserveWaiting(context.Background(), 8, time.Second)
		result <- lease
	}()
	require.Eventually(t, func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.changed != nil
	}, time.Second, time.Millisecond)
	held.release()
	select {
	case lease := <-result:
		require.NotNil(t, lease, "a released slot must wake a waiting request")
		lease.release()
	case <-time.After(2 * time.Second):
		t.Fatal("waiting request was not released")
	}
	used, count := b.snapshot()
	require.Zero(t, used)
	require.Zero(t, count)
}

func TestExcelBPSImageAdmissionDecodeWaitTimeoutAndCancel(t *testing.T) {
	b := &bpsImageAdmissionBudget{maxBytes: 8, maxRequests: 1}
	held, ok := b.reserve(8)
	require.True(t, ok)
	defer held.release()
	lease, ok := b.reserveWaiting(context.Background(), 8, 10*time.Millisecond)
	require.False(t, ok)
	require.Nil(t, lease)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	lease, ok = b.reserveWaiting(ctx, 8, time.Second)
	require.False(t, ok)
	require.Nil(t, lease)
	used, count := b.snapshot()
	require.Equal(t, int64(8), used)
	require.Equal(t, 1, count)
	held.release()
	lease, ok = b.reserveWaiting(context.Background(), 8, time.Second)
	require.True(t, ok, "timed-out and canceled waiters must not consume capacity")
	lease.release()
}

func TestExcelBPSImageAdmissionBodyReadDeadline(t *testing.T) {
	r := admissionRouter(config.ImageRelayAdmissionConfig{BodyReadTimeoutSeconds: 1}, func(c *gin.Context) { c.Status(200) })
	s := httptest.NewServer(r)
	defer s.Close()
	conn, err := net.Dial("tcp", s.Listener.Addr().String())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	_, err = io.WriteString(conn, "POST /v1/responses HTTP/1.1\r\nHost: localhost\r\nContent-Length: 10\r\n\r\na")
	require.NoError(t, err)
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 408, resp.StatusCode)
	// A timed-out uploader cannot retain the sole decoder lease.
	resp2, err := s.Client().Post(s.URL+"/v1/responses", "application/json", strings.NewReader("test"))
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	require.Equal(t, 200, resp2.StatusCode)
}
