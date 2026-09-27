package service

import (
	"fmt"
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

func TestFollowupBPSChatMetadataCardinality(t *testing.T) {
	var output strings.Builder
	writer := newExcelBPSChatWriter(&output, "gpt-5.6-sol", false)
	var lastError error
	for index := 0; index < 5000; index++ {
		_, lastError = writer.WriteString(incidentBPSFrame("response.output_text.delta", map[string]any{
			"item_id": fmt.Sprintf("msg_%d", index), "output_index": index, "content_index": 0, "delta": "x",
		}))
		if lastError != nil {
			break
		}
	}
	require.Error(t, lastError, "distinct streamed text identities must have an aggregate metadata bound")
	require.Contains(t, lastError.Error(), "metadata")
	require.NotContains(t, output.String(), "data: [DONE]")
	_, err := writer.WriteString(incidentBPSFrame("response.completed", map[string]any{"response": map[string]any{"status": "completed", "output": []any{}}}))
	require.Error(t, err, "metadata failure must remain sticky")
	require.NotContains(t, output.String(), "data: [DONE]", "a resource failure must not become success on a later terminal")
}

func TestFollowupBPSChatMetadataIdentityBytes(t *testing.T) {
	var output strings.Builder
	writer := newExcelBPSChatWriter(&output, "gpt-5.6-sol", false)
	_, err := writer.WriteString(incidentBPSFrame("response.output_text.delta", map[string]any{
		"item_id": strings.Repeat("i", 600<<10), "output_index": 0, "delta": "must not emit",
	}))
	require.Error(t, err, "retained identity strings must be bounded independently of entry count")
	require.Empty(t, output.String(), "the event that exceeds the limit must not emit content")
}

func TestFollowupNativeChatMetadataFailure(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(fmt.Sprintf("started=%t", started), func(t *testing.T) {
			wire := ""
			if started {
				wire += incidentBPSFrame("response.output_text.delta", map[string]any{"item_id": "msg_0", "output_index": 0, "delta": strings.Repeat("answer ", 64)})
			}
			wire += incidentBPSFrame("response.output_text.delta", map[string]any{"item_id": strings.Repeat("i", 600<<10), "output_index": 1, "delta": "must not emit"})
			wire += incidentBPSFrame("response.completed", map[string]any{"response": map[string]any{"status": "completed", "output": []any{}}})
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			result, err := svc.handleChatStreamingResponse(response, c, &Account{ID: 1, Platform: PlatformOpenAI}, "gpt-5.6-sol", "gpt-5.6-sol", "gpt-5.6-sol", time.Now(), 0)
			require.Error(t, err)
			require.NotNil(t, result)
			require.False(t, result.SucceededForScheduling(), "failed conversion is not a successful generation")
			require.Contains(t, err.Error(), "metadata")
			require.Contains(t, rec.Body.String(), "response_metadata_limit")
			require.Contains(t, rec.Body.String(), "upstream_error")
			require.NotContains(t, rec.Body.String(), "must not emit")
			require.NotContains(t, rec.Body.String(), "\"finish_reason\":\"stop\"")
			if started {
				require.Equal(t, http.StatusOK, rec.Code)
				require.Equal(t, 1, strings.Count(rec.Body.String(), "data: [DONE]"))
			} else {
				require.Equal(t, http.StatusBadGateway, rec.Code)
				require.NotContains(t, rec.Body.String(), "data: [DONE]")
			}
		})
	}
}
