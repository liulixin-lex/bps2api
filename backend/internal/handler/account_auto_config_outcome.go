package handler

import (
	"bytes"
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
	"time"
)

func autoConfigSuccessfulFrame(frame []byte) bool {
	_, data := parseOpsSSEFrameEnvelope(frame)
	if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		return true
	}
	if !bytes.Contains(data, []byte("response.completed")) && !bytes.Contains(data, []byte("message_stop")) && !bytes.Contains(data, []byte("finishReason")) {
		return false
	}
	var v struct {
		Type     string `json:"type"`
		Response struct {
			Status string `json:"status"`
		} `json:"response"`
		Candidates []struct {
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
	}
	if json.Unmarshal(data, &v) != nil {
		return false
	}
	if v.Type == "message_stop" || v.Type == "response.completed" && (v.Response.Status == "" || v.Response.Status == "completed") {
		return true
	}
	for _, c := range v.Candidates {
		if c.FinishReason == "STOP" || c.FinishReason == "MAX_TOKENS" {
			return true
		}
	}
	return false
}
func observeAutoConfigRequest(c *gin.Context, ops *service.OpsService, w *opsCaptureWriter, started time.Time) {
	if ops == nil || c.Request.Method != http.MethodPost || isCountTokensRequest(c) {
		return
	}
	path := c.Request.URL.Path
	if !strings.HasSuffix(path, "/responses") && !strings.HasSuffix(path, "/messages") && !strings.HasSuffix(path, "/chat/completions") && !strings.Contains(path, ":generateContent") && !strings.Contains(path, ":streamGenerateContent") {
		return
	}
	failed := map[int64]bool{}
	if v, ok := c.Get(service.OpsUpstreamErrorsKey); ok {
		if events, ok := v.([]*service.OpsUpstreamErrorEvent); ok {
			for _, e := range events {
				if e != nil && e.AccountID > 0 {
					failed[e.AccountID] = true
				}
			}
		}
	}
	for id := range failed {
		ops.ObserveConcurrencyResult(service.AccountConcurrencyResult{AccountID: id, StartedAt: started})
	}
	id := c.GetInt64(opsAccountIDKey)
	if id <= 0 || failed[id] {
		return
	}
	if c.Request.Context().Err() != nil || c.Writer.Status() == 499 {
		return
	}
	state, _ := w.lockActive()
	if state == nil {
		return
	}
	terminalError, completed := state.terminalFound, state.terminalSuccess
	// Successful non-stream JSON lives in the bounded frame probe, while
	// explicit HTTP error responses are retained in buf. An uninspectable
	// oversized response is not evidence for increasing concurrency.
	body := state.buf.Bytes()
	if len(body) == 0 && !state.frameTruncated {
		body = state.probe
	}
	jsonCompleted, jsonFailed := autoConfigJSONOutcome(path, body)
	terminalError = terminalError || jsonFailed

	state.mu.RUnlock()
	success := c.Writer.Status() >= 200 && c.Writer.Status() < 300 && len(c.Errors) == 0 && !terminalError && len(service.GetOpsStreamErrors(c)) == 0
	streaming := c.GetBool(opsStreamKey) || strings.Contains(c.Writer.Header().Get("Content-Type"), "text/event-stream")
	if !streaming && !jsonCompleted && success {
		return
	}
	if streaming && !completed {
		success = false
	}
	ops.ObserveConcurrencyResult(service.AccountConcurrencyResult{AccountID: id, StartedAt: started, Success: success})
}

// A transport 200 or parseable JSON object is not a generation completion.
// Unknown/queued payloads do not contribute a sample; explicit failures reset it.
func autoConfigJSONOutcome(path string, body []byte) (completed, failed bool) {
	var value struct {
		ID, Object, Type, Role, Status string
		StopReason                     string `json:"stop_reason"`
		Error                          json.RawMessage
		Output                         []json.RawMessage
		Content                        []json.RawMessage
		Choices                        []struct {
			FinishReason string `json:"finish_reason"`
			Message      *struct {
				Role         string
				Content      json.RawMessage
				ToolCalls    []json.RawMessage `json:"tool_calls"`
				FunctionCall json.RawMessage   `json:"function_call"`
			}
		}
		Candidates []struct {
			FinishReason string
			Content      struct{ Parts []json.RawMessage }
		}
		PromptFeedback struct{ BlockReason string }
	}
	if json.Unmarshal(body, &value) != nil {
		return false, false
	}
	if len(value.Error) > 0 && string(value.Error) != "null" || value.Status == "failed" || value.Status == "incomplete" {
		return false, true
	}
	switch {
	case strings.HasSuffix(path, "/responses"):
		return value.ID != "" && value.Object == "response" && value.Status == "completed" && len(value.Output) > 0, false
	case strings.HasSuffix(path, "/chat/completions"):
		if value.ID == "" || value.Object != "chat.completion" || len(value.Choices) == 0 {
			return false, false
		}
		for _, choice := range value.Choices {
			switch choice.FinishReason {
			case "stop", "length", "tool_calls", "function_call":
			default:
				return false, false
			}
			if choice.Message == nil || choice.Message.Role != "assistant" {
				return false, false
			}
			if (len(choice.Message.Content) == 0 || string(choice.Message.Content) == "null") && len(choice.Message.ToolCalls) == 0 && len(choice.Message.FunctionCall) == 0 {
				return false, false
			}
		}
		return true, false
	case strings.HasSuffix(path, "/messages"):
		if value.ID == "" || value.Type != "message" || value.Role != "assistant" || len(value.Content) == 0 {
			return false, false
		}
		switch value.StopReason {
		case "end_turn", "max_tokens", "stop_sequence", "tool_use":
			return true, false
		}
	case strings.Contains(path, ":generateContent"), strings.Contains(path, ":streamGenerateContent"):
		if value.PromptFeedback.BlockReason != "" {
			return false, true
		}
		if len(value.Candidates) == 0 {
			return false, false
		}
		for _, candidate := range value.Candidates {
			if (candidate.FinishReason != "STOP" && candidate.FinishReason != "MAX_TOKENS") || len(candidate.Content.Parts) == 0 {
				return false, false
			}
		}
		return true, false
	}
	return false, false
}
