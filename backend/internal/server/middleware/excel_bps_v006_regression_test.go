package middleware

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestV006ImageDetectionMatchesRelay(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		want       bool
	}{
		{"escaped type", `{"input":[{"content":[{"type":"input_\u0069mage","image_url":"data:image/png;base64,AAAA"}]}]}`, true},
		{"escaped URL", `{"input":[{"content":[{"type":"input_image","image_url":"\u0064ata:image/png;base64,AAAA"}]}]}`, true},
		{"case insensitive URL", `{"input":[{"content":[{"type":"input_image","image_url":"DATA:image/png;base64,AAAA"}]}]}`, true},
		{"unrelated markers", `{"metadata":{"type":"input_image","x":"data:fake"},"input":"hello"}`, false},
		{"last input wins", `{"input":[{"content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}],"input":"hello"}`, false},
		{"last image URL wins", `{"input":[{"content":[{"type":"input_image","image_url":"https://example.test/a.png","image_url":"data:image/png;base64,AAAA"}]}]}`, true},
		{"case sensitive field", `{"Input":[{"content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`, false},
		{"invalid JSON conservative", `{"input":`, true},
		{"tool output", `{"input":[{"type":"custom_tool_call_output","output":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`, true},
	} {
		t.Run(tt.name, func(t *testing.T) { require.Equal(t, tt.want, bpsImageRequestNeedsRelay([]byte(tt.body))) })
	}
}

func TestV006TextPassesWhenImageCapacityIsFull(t *testing.T) {
	initMiddlewareTestLogger(t)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyAPIKey), &service.APIKey{Group: &service.Group{Platform: service.PlatformOpenAI}})
		c.Next()
	})
	r.Use(ExcelBPSImageAdmission(bpsImageTestSettings{enabled: true}, 64<<20, config.ImageRelayAdmissionConfig{MaxConcurrentRequests: 1, ProcessingBudgetBytes: 8 << 20}))
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	r.POST("/v1/responses", func(c *gin.Context) {
		if c.GetHeader("Hold") == "yes" {
			close(entered)
			<-release
		}
		c.Status(200)
	})
	defer func() { close(release); <-done }()
	go func() {
		defer close(done)
		q := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"input":[{"content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`))
		q.Header.Set("Hold", "yes")
		r.ServeHTTP(httptest.NewRecorder(), q)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("image request did not reach handler")
	}
	for _, body := range []string{`{"input":"hello"}`, `{"input":[{"content":[{"type":"input_image","image_url":"https://example.test/a.png"}]}]}`} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
		require.Equal(t, 200, w.Code, w.Body.String())
	}
}
