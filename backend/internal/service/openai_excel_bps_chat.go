package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
)

type excelBPSChatContextKey struct{}
type excelBPSChatRequest struct {
	OriginalModel, BillingModel, UpstreamModel string
	IncludeUsage                               bool
}

func excelBPSChatFromContext(ctx context.Context) *excelBPSChatRequest {
	request, _ := ctx.Value(excelBPSChatContextKey{}).(*excelBPSChatRequest)
	return request
}

// Only the downstream wire format changes. The existing BPS loop owns timeout,
// retry-before-output, usage, image scope and authoritative tool validation.
type excelBPSChatWriter struct {
	output  io.StringWriter
	state   *apicompat.ResponsesEventToChatState
	pending string
	done    bool
}

func newExcelBPSChatWriter(output io.StringWriter, model string, includeUsage bool) *excelBPSChatWriter {
	state := apicompat.NewResponsesEventToChatState()
	state.Model = model
	state.IncludeUsage = includeUsage
	return &excelBPSChatWriter{output: output, state: state}
}
func (w *excelBPSChatWriter) WriteString(value string) (int, error) {
	if w.done {
		return len(value), nil
	}
	w.pending += value
	for {
		line, rest, ok := strings.Cut(w.pending, "\n")
		if !ok {
			break
		}
		w.pending = rest
		if strings.HasPrefix(line, ":") {
			if _, err := w.output.WriteString(line + "\n\n"); err != nil {
				return 0, err
			}
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var event apicompat.ResponsesStreamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return 0, fmt.Errorf("invalid BPS Chat event")
		}
		if event.Type == "response.failed" || event.Type == "error" {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(payload), &fields); err != nil {
				return 0, err
			}
			raw := fields["error"]
			if event.Type == "response.failed" {
				var response map[string]json.RawMessage
				if err := json.Unmarshal(fields["response"], &response); err != nil {
					return 0, err
				}
				raw = response["error"]
			}
			if len(raw) == 0 || string(raw) == "null" {
				raw = json.RawMessage("{\"type\":\"upstream_error\",\"message\":\"BPS response failed\"}")
			}
			encoded, err := json.Marshal(map[string]json.RawMessage{"error": raw})
			if err != nil {
				return 0, err
			}
			if _, err = w.output.WriteString("data: " + string(encoded) + "\n\ndata: [DONE]\n\n"); err != nil {
				return 0, err
			}
			w.done = true
			w.pending = ""
			return len(value), nil
		}
		chunks := apicompat.ResponsesEventToChatChunks(&event, w.state)
		for _, chunk := range chunks {
			encoded, err := apicompat.ChatChunkToSSE(chunk)
			if err != nil {
				return 0, err
			}
			if _, err = w.output.WriteString(encoded); err != nil {
				return 0, err
			}
		}
		if event.Type == "response.completed" || event.Type == "response.incomplete" {
			if _, err := w.output.WriteString("data: [DONE]\n\n"); err != nil {
				return 0, err
			}
			w.done = true
			w.pending = ""
			return len(value), nil
		}
	}
	if len(w.pending) > 16<<20 {
		return 0, fmt.Errorf("BPS Chat event exceeds size limit")
	}
	return len(value), nil
}

func writeExcelBPSStreamFailure(c *gin.Context, chat *excelBPSChatRequest, output io.StringWriter, status int, code, message string) {
	if chat == nil {
		writeOpenAICompactSSEFailureMessage(c, status, code, message)
		return
	}
	event, _ := json.Marshal(gin.H{"type": "response.failed", "response": gin.H{"status": "failed", "error": gin.H{"type": "server_error", "code": code, "message": message}}})
	_, _ = output.WriteString("data: " + string(event) + "\n\n")
	c.Writer.Flush()
}
