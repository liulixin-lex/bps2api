package basispoints

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type scaleSmallSendBufferListener struct{ net.Listener }

func (l scaleSmallSendBufferListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if tcp, ok := c.(*net.TCPConn); ok {
		_ = tcp.SetWriteBuffer(4096)
	}
	return c, err
}

// Production middleware wrappers and Gin preserve this same Unwrap chain.
type scaleImageGinUnwrapper struct{ gin.ResponseWriter }

func (w scaleImageGinUnwrapper) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// This fixture deliberately never reads the first TCP connection. A16MiB body
// exceeds both tiny socket buffers and proves that actual blocked writes stop;
// an in-memory recorder would not exercise the network deadline.
func TestScaleImageSlowConsumerReleasesSlotAndRetiredQuota(t *testing.T) {
	r, err := newTestImageRelay(t, "https://images.example")
	require.NoError(t, err)
	r.downloads = make(chan struct{}, 1)
	large := make([]byte, 16<<20)
	copy(large, relayTestPNG(t))
	bigOut, err := r.Rewrite(relayTestRequest(t, large), "slow-reader")
	require.NoError(t, err)
	bigPath := strings.TrimPrefix(relayTestURL(t, bigOut), r.baseURL)
	bigToken := strings.TrimPrefix(bigPath, ImageRelayPath)
	smallData := relayTestPNG(t)
	smallOut, err := r.Rewrite(relayTestRequest(t, smallData), "healthy-reader")
	require.NoError(t, err)
	smallPath := strings.TrimPrefix(relayTestURL(t, smallOut), r.baseURL)
	returned := make(chan struct{})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Writer = scaleImageGinUnwrapper{scaleImageGinUnwrapper{c.Writer}}
		c.Next()
	})
	router.GET(ImageRelayPath+":token", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 500*time.Millisecond)
		defer cancel()
		r.ServeHTTP(c.Writer, c.Request.WithContext(ctx))
		if c.Request.URL.Path == bigPath {
			close(returned)
		}
	})
	server := httptest.NewUnstartedServer(router)
	server.Listener = scaleSmallSendBufferListener{server.Listener}
	server.Start()
	defer server.Close()
	client, err := net.Dial("tcp", server.Listener.Addr().String())
	require.NoError(t, err)
	defer client.Close()
	if tcp, ok := client.(*net.TCPConn); ok {
		require.NoError(t, tcp.SetReadBuffer(1024))
	}
	_, err = fmt.Fprintf(client, "GET %s HTTP/1.1\r\nHost: images.example\r\nConnection: keep-alive\r\n\r\n", bigPath)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.entries[bigToken] != nil && r.entries[bigToken].readers == 1
	}, time.Second, time.Millisecond, "download never entered its real network write")
	r.mu.Lock()
	old := r.entries[bigToken]
	old.expires = time.Now().Add(-time.Second)
	r.pruneLocked(time.Now())
	retired, retainedBytes := r.retiredEntries, r.bytes
	r.mu.Unlock()
	require.Equal(t, 1, retired)
	require.Equal(t, len(large)+len(smallData), retainedBytes)
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("slow consumer pinned its download slot after request deadline")
	}
	r.mu.Lock()
	retired, retainedBytes = r.retiredEntries, r.bytes
	r.mu.Unlock()
	require.Zero(t, retired)
	require.Equal(t, len(smallData), retainedBytes)
	require.Empty(t, r.downloads)
	_, err = os.Stat(old.path)
	require.True(t, os.IsNotExist(err), "expired file must be removed when interrupted download finishes")
	httpClient := server.Client()
	httpClient.Timeout = 2 * time.Second
	response, err := httpClient.Get(server.URL + smallPath)
	require.NoError(t, err)
	defer response.Body.Close()
	got, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, "image/png", response.Header.Get("Content-Type"))
	require.Equal(t, smallData, got)
}

type scaleCancelDeadlineWriter struct {
	mu        sync.Mutex
	header    http.Header
	deadlines []time.Time
	updates   chan time.Time
	entered   chan struct{}
	once      sync.Once
}

func (w *scaleCancelDeadlineWriter) Header() http.Header { return w.header }
func (w *scaleCancelDeadlineWriter) WriteHeader(int)     {}
func (w *scaleCancelDeadlineWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	w.deadlines = append(w.deadlines, deadline)
	w.mu.Unlock()
	w.updates <- deadline
	return nil
}
func (w *scaleCancelDeadlineWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	stop := time.NewTimer(3 * time.Second)
	defer stop.Stop()
	for {
		select {
		case deadline := <-w.updates:
			if !deadline.IsZero() && !deadline.After(time.Now()) {
				return 0, os.ErrDeadlineExceeded
			}
		case <-stop.C:
			return 0, fmt.Errorf("test writer was not interrupted")
		}
	}
}

func TestScaleImageCancelledDownloadClearsDeadline(t *testing.T) {
	r, err := newTestImageRelay(t, "https://images.example")
	require.NoError(t, err)
	out, err := r.Rewrite(relayTestRequest(t, relayTestPNG(t)), "scope")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &scaleCancelDeadlineWriter{header: make(http.Header), updates: make(chan time.Time, 8), entered: make(chan struct{})}
	finished := make(chan struct{})
	imageURL := relayTestURL(t, out)
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(4 * time.Second):
			t.Error("download goroutine failed to finish")
		}
	})
	go func() {
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, imageURL, nil).WithContext(ctx))
		close(finished)
	}()
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("write did not start")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt blocked write")
	}
	w.mu.Lock()
	deadlines := append([]time.Time(nil), w.deadlines...)
	w.mu.Unlock()
	require.GreaterOrEqual(t, len(deadlines), 3)
	require.False(t, deadlines[0].IsZero())
	require.True(t, deadlines[len(deadlines)-1].IsZero(), "must clear deadline only after cancellation callback finishes")
	require.Empty(t, r.downloads)
	for _, img := range r.entries {
		require.Zero(t, img.readers)
	}

	// Deadline-free in-memory writers still preserve ordinary byte delivery.
	plain := httptest.NewRecorder()
	r.ServeHTTP(plain, httptest.NewRequest(http.MethodGet, relayTestURL(t, out), nil))
	require.Equal(t, http.StatusOK, plain.Code)
	require.True(t, bytes.Equal(relayTestPNG(t), plain.Body.Bytes()))
}
