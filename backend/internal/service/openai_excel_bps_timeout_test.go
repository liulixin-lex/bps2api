package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestExcelBPSTimeoutsDoNotReplay(t *testing.T) {
	for _, tt := range []struct {
		name    string
		limits  config.ExcelBPSTimeoutConfig
		stream  bool
		emit    bool
		compact bool
	}{
		{"idle JSON", config.ExcelBPSTimeoutConfig{IdleSeconds: 1}, false, false, false},
		{"idle SSE", config.ExcelBPSTimeoutConfig{IdleSeconds: 1}, true, true, false},
		{"first output", config.ExcelBPSTimeoutConfig{FirstOutputSeconds: 1}, true, true, false},
		{"total", config.ExcelBPSTimeoutConfig{TotalSeconds: 1}, false, false, false},
		{"idle compact SSE", config.ExcelBPSTimeoutConfig{IdleSeconds: 1}, false, false, true},
		{"first output compact SSE", config.ExcelBPSTimeoutConfig{FirstOutputSeconds: 1}, false, false, true},
		{"total compact SSE", config.ExcelBPSTimeoutConfig{TotalSeconds: 1}, false, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reader, writer := io.Pipe()
			defer writer.Close()
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: reader}}
			svc := openAIClientToolsTestService(upstream)
			svc.cfg.Gateway.ExcelBPSTimeouts = tt.limits
			if tt.emit {
				done := make(chan struct{})
				go func() {
					defer close(done)
					_, _ = io.WriteString(writer, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"test\",\"output\":[]}}\n\n")
				}()
				defer func() { reader.Close(); <-done }()
			}
			body := []byte(`{"model":"gpt-6-astra","input":"hello","stream":false}`)
			if tt.stream {
				body = []byte(`{"model":"gpt-6-astra","input":"hello","stream":true}`)
			}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			var keepalive *openAICompactSSEKeepalive
			if tt.compact {
				c.Request = httptest.NewRequest("POST", "/v1/responses/compact", nil)
				MarkOpenAICompactClientStream(c)
				stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
				defer stop()
				value, exists := c.Get(openAICompactSSEKeepaliveKey)
				require.True(t, exists)
				keepalive = value.(*openAICompactSSEKeepalive)
				require.True(t, keepalive.beat())
			}
			start := time.Now()
			result, err := svc.Forward(context.Background(), c, excelAccount(), body)
			require.Error(t, err)
			require.NotNil(t, result)
			require.False(t, result.ClientDisconnect)
			require.False(t, result.SucceededForScheduling())
			require.Len(t, upstream.requests, 1)
			require.Less(t, time.Since(start), 5*time.Second)
			require.Contains(t, rec.Body.String(), "basispoints_stream_timeout")
			if tt.compact || (tt.stream && tt.emit) {
				require.Equal(t, 200, rec.Code)
				require.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.failed\n"))
				require.NotContains(t, rec.Body.String(), "{\"error\":")
			} else {
				require.Equal(t, 504, rec.Code)
			}
			if keepalive != nil {
				require.False(t, keepalive.beat(), "timeout must stop the compact heartbeat")
			}
		})
	}
}

func TestExcelBPSRawActivityKeepsBufferedBridgeAlive(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	pump := newOpenAISSEReadPump(reader, 1024)
	defer pump.Close()
	activity := &excelBPSActivityBody{ReadCloser: io.NopCloser(strings.NewReader("upstream bytes"))}
	// Simulate data consumed by a bridge before a downstream event is emitted.
	_, err := io.ReadAll(activity)
	require.NoError(t, err)
	pump.upstreamActivity = &activity.lastRead
	pump.lastRead.Store(time.Now().Add(-time.Second).UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.False(t, pump.Next(ctx, 200*time.Millisecond, nil, nil))
	require.ErrorIs(t, pump.Err(), context.DeadlineExceeded, "must count raw upstream activity")
}
