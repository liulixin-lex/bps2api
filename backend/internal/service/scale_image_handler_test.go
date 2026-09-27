package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type scaleImageHandlerSettings struct {
	SettingRepository
	calls int
	err   error
}

func (r *scaleImageHandlerSettings) GetMultiple(context.Context, []string) (map[string]string, error) {
	r.calls++
	return nil, r.err
}

func TestScaleImageHandlerTransientSettingsFailureIsRetryable(t *testing.T) {
	repo := &scaleImageHandlerSettings{err: errors.New("private database failure")}
	svc := &OpenAIGatewayService{settingService: NewSettingService(repo, &config.Config{})}
	relay, err := basispoints.NewImageRelay("https://images.example", t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, relay.Close()) })
	svc.excelBPSImages = relay
	var pngBytes bytes.Buffer
	require.NoError(t, png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 2, 3))))
	body, err := json.Marshal(map[string]any{"input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes.Bytes())}}}}})
	require.NoError(t, err)
	rewritten, err := relay.Rewrite(body, "test-owned-image")
	require.NoError(t, err)
	imageURL := gjson.GetBytes(rewritten, "input.0.content.0.image_url").String()
	require.Contains(t, imageURL, basispoints.ImageRelayPath)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, imageURL, nil)
	svc.ServeExcelBPSImage(c)
	c.Writer.WriteHeaderNow()
	require.Equal(t, http.StatusServiceUnavailable, w.Code, "transient settings failure must not tell upstream that a retained image is missing")
	require.Equal(t, "1", w.Header().Get("Retry-After"))
	require.Equal(t, "private, no-store", w.Header().Get("Cache-Control"))
	require.NotContains(t, w.Body.String(), "private database failure")
	require.Equal(t, 1, repo.calls)
}

func TestScaleImageHandlerInvalidCapabilitiesSkipDatabase(t *testing.T) {
	for _, tc := range []struct {
		name, method, path string
		status             int
	}{
		{"short", http.MethodGet, basispoints.ImageRelayPath + "short", http.StatusNotFound},
		{"bad-alphabet", http.MethodGet, basispoints.ImageRelayPath + strings.Repeat("!", 43), http.StatusNotFound},
		{"wrong-path", http.MethodGet, "/unrelated/" + strings.Repeat("A", 43), http.StatusNotFound},
		{"valid-but-not-owned", http.MethodGet, basispoints.ImageRelayPath + strings.Repeat("A", 43), http.StatusNotFound},
		{"method", http.MethodPost, basispoints.ImageRelayPath + strings.Repeat("A", 43), http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &scaleImageHandlerSettings{err: errors.New("unavailable")}
			svc := &OpenAIGatewayService{settingService: NewSettingService(repo, &config.Config{})}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(tc.method, tc.path, nil)
			svc.ServeExcelBPSImage(c)
			c.Writer.WriteHeaderNow()
			require.Equal(t, tc.status, w.Code)
			require.Zero(t, repo.calls, "invalid public download probes must not consume database capacity")
		})
	}
}
