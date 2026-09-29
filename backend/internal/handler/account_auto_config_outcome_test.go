package handler

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAutoConfigRealRequestOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, body     string
		status         int
		stream, cancel bool
		want           int
		success        bool
	}{
		{"json error at 200", "{\"error\":{\"message\":\"limited\"}}", 200, false, false, 1, false},
		{"incomplete at 200", "{\"status\":\"incomplete\"}", 200, false, false, 1, false},
		{"error followed by done", "event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"failed\"}}\n\ndata: [DONE]\n\n", 200, true, false, 1, false},
		{"empty at 200", "{}", 200, false, false, 0, false},
		{"queued at 200", "{\"status\":\"queued\"}", 200, false, false, 0, false},
		{"in progress at 200", "{\"status\":\"in_progress\"}", 200, false, false, 0, false},
		{"json success", `{"id":"resp_ok","object":"response","status":"completed","output":[{"type":"message"}]}`, 200, false, false, 1, true},
		{"upstream 429", `{"error":"limited"}`, 429, false, false, 1, false},
		{"complete stream", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", 200, true, false, 1, true},
		{"stream error at 200", "event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"failed\"}}\n\n", 200, true, false, 1, false},
		{"truncated stream", "data: {\"type\":\"response.created\"}\n\n", 200, true, false, 1, false},
		{"cancelled", "data: [DONE]\n\n", 200, true, true, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			ops := service.NewOpsService(nil, nil, &config.Config{}, nil, nil, nil, nil, nil, nil, nil, nil)
			got := []service.AccountConcurrencyResult{}
			ops.SetAutoConfigObserver(func(r service.AccountConcurrencyResult) { got = append(got, r) })
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(ops))
			router.POST("/v1/responses", func(c *gin.Context) {
				c.Set(opsAccountIDKey, int64(7))
				c.Set(opsStreamKey, tc.stream)
				if tc.cancel {
					ctx, cancel := context.WithCancel(c.Request.Context())
					cancel()
					c.Request = c.Request.WithContext(ctx)
				}
				content := "application/json"
				if tc.stream {
					content = "text/event-stream"
				}
				c.Data(tc.status, content, []byte(tc.body))
			})
			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
			require.Len(t, got, tc.want)
			if tc.want > 0 {
				require.Equal(t, tc.success, got[0].Success)
				require.Equal(t, int64(7), got[0].AccountID)
			}
		})
	}
}
func TestAutoConfigRetryDeduplicatesFailedAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ops := service.NewOpsService(nil, nil, &config.Config{}, nil, nil, nil, nil, nil, nil, nil, nil)
	got := []service.AccountConcurrencyResult{}
	ops.SetAutoConfigObserver(func(r service.AccountConcurrencyResult) { got = append(got, r) })
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/responses", func(c *gin.Context) {
		c.Set(opsAccountIDKey, int64(8))
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{AccountID: 7}, {AccountID: 7}})
		c.JSON(200, gin.H{"id": "resp_ok", "object": "response", "status": "completed", "output": []gin.H{{"type": "message"}}})
	})
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	require.Len(t, got, 2)
	require.Equal(t, int64(7), got[0].AccountID)
	require.False(t, got[0].Success)
	require.True(t, got[1].Success)
}
func TestAutoConfigSplitSSEAndWriterReuse(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	w := acquireOpsCaptureWriter(c.Writer)
	for _, chunk := range []string{"data: {\"type\":\"message_", "stop\"}\n", "\n"} {
		_, err := w.WriteString(chunk)
		require.NoError(t, err)
	}
	state, _ := w.lockActive()
	require.True(t, state.terminalSuccess)
	state.mu.RUnlock()
	releaseOpsCaptureWriter(w)
	fresh := acquireOpsCaptureWriter(c.Writer)
	defer releaseOpsCaptureWriter(fresh)
	state, _ = fresh.lockActive()
	require.False(t, state.terminalSuccess)
	state.mu.RUnlock()
}

func TestAutoConfigJSONRequiresProtocolCompletion(t *testing.T) {
	for _, tc := range []struct {
		name, path, body  string
		completed, failed bool
	}{
		{"responses completed", "/v1/responses", "{\"id\":\"resp_ok\",\"object\":\"response\",\"status\":\"completed\",\"output\":[{\"type\":\"message\"}]}", true, false},
		{"responses queued", "/v1/responses", "{\"id\":\"resp_wait\",\"object\":\"response\",\"status\":\"queued\",\"output\":[]}", false, false},
		{"responses empty", "/v1/responses", "{}", false, false},
		{"responses incomplete", "/v1/responses", "{\"status\":\"incomplete\"}", false, true},
		{"chat completed", "/v1/chat/completions", "{\"id\":\"chat_ok\",\"object\":\"chat.completion\",\"choices\":[{\"finish_reason\":\"stop\",\"message\":{\"role\":\"assistant\",\"content\":\"ok\"}}]}", true, false},
		{"chat tool completed", "/v1/chat/completions", "{\"id\":\"chat_tool\",\"object\":\"chat.completion\",\"choices\":[{\"finish_reason\":\"tool_calls\",\"message\":{\"role\":\"assistant\",\"content\":null,\"tool_calls\":[{\"id\":\"tool\"}]}}]}", true, false},
		{"chat unfinished", "/v1/chat/completions", "{\"id\":\"chat_wait\",\"object\":\"chat.completion\",\"choices\":[{\"finish_reason\":null,\"message\":{\"role\":\"assistant\",\"content\":\"partial\"}}]}", false, false},
		{"messages completed", "/v1/messages", "{\"id\":\"msg_ok\",\"type\":\"message\",\"role\":\"assistant\",\"stop_reason\":\"end_turn\",\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}", true, false},
		{"messages unfinished", "/v1/messages", "{\"id\":\"msg_wait\",\"type\":\"message\",\"role\":\"assistant\",\"stop_reason\":null,\"content\":[{\"type\":\"text\",\"text\":\"partial\"}]}", false, false},
		{"gemini completed", "/v1beta/models/gemini:generateContent", "{\"candidates\":[{\"finishReason\":\"STOP\",\"content\":{\"parts\":[{\"text\":\"ok\"}]}}]}", true, false},
		{"gemini blocked", "/v1beta/models/gemini:generateContent", "{\"promptFeedback\":{\"blockReason\":\"SAFETY\"}}", false, true},
		{"gemini unfinished", "/v1beta/models/gemini:generateContent", "{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial\"}]}}]}", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			completed, failed := autoConfigJSONOutcome(tc.path, []byte(tc.body))
			require.Equal(t, tc.completed, completed)
			require.Equal(t, tc.failed, failed)
		})
	}
}
