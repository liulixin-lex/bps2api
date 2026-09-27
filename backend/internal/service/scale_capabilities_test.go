package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestScaleBPSIgnoredParametersAreObservable(t *testing.T) {
	good := incidentBPSFrame("response.completed", map[string]any{"response": map[string]any{"id": "resp_capabilities", "status": "completed", "output": []any{}}})
	for _, stream := range []bool{false, true} {
		upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(good))}}
		svc := openAIClientToolsTestService(upstream)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
		body, err := json.Marshal(map[string]any{"model": "gpt-5.6-sol", "stream": stream, "input": "test", "max_output_tokens": 128, "temperature": 0.5})
		require.NoError(t, err)
		_, err = svc.Forward(context.Background(), c, excelAccount(), body)
		require.NoError(t, err)
		require.Equal(t, "max_output_tokens,temperature", w.Header().Get("X-BPS-Ignored-Parameters"))
		require.NotContains(t, w.Header().Get("X-BPS-Ignored-Parameters"), "128")
	}
}
