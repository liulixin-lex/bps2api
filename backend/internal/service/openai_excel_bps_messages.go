package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

var errExcelBPSMessagesConversion = errors.New("BPS Messages conversion failed")

const excelBPSMessagesMaxWireBytes = 64 << 20
const excelBPSMessagesMaxEvents = 65536

type excelBPSMessagesContextKey struct{}
type excelBPSMessagesRequest struct {
	OriginalModel, BillingModel, UpstreamModel string
}

func excelBPSMessagesFromContext(ctx context.Context) *excelBPSMessagesRequest {
	request, _ := ctx.Value(excelBPSMessagesContextKey{}).(*excelBPSMessagesRequest)
	return request
}

// The shared BPS forwarder retains ownership of retries, image admission,
// validated tools, usage and timeouts. Only its downstream event format changes.
type excelBPSMessagesWriter struct {
	output        io.StringWriter
	state         *apicompat.ResponsesEventToAnthropicState
	pending       string
	done          bool
	wireBytes     int
	events        int
	conversionErr error
}

func newExcelBPSMessagesWriter(output io.StringWriter, model string) *excelBPSMessagesWriter {
	state := apicompat.NewResponsesEventToAnthropicState()
	state.Model = model
	return &excelBPSMessagesWriter{output: output, state: state}
}

func (w *excelBPSMessagesWriter) WriteString(value string) (int, error) {
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
			if _, err := w.output.WriteString("event: ping\ndata: {\"type\":\"ping\"}\n\n"); err != nil {
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
			w.pending = ""
			w.conversionErr = fmt.Errorf("%w: invalid event", errExcelBPSMessagesConversion)
			return 0, w.conversionErr
		}
		incompleteFailure := event.Type == "response.incomplete" &&
			gjson.Get(payload, "response.incomplete_details.reason").String() != "max_output_tokens"
		if event.Type == "response.failed" || event.Type == "error" || incompleteFailure {
			rawError := gjson.Get(payload, "error")
			if event.Type == "response.failed" {
				rawError = gjson.Get(payload, "response.error")
			}
			kind := excelBPSAnthropicErrorType(rawError.Get("type").String())
			message := rawError.Get("message").String()
			if message == "" {
				message = "Excel BPS did not complete the response"
			}
			if _, err := w.output.WriteString(buildAnthropicStreamErrorSSE(kind, message)); err != nil {
				return 0, err
			}
			w.done, w.pending = true, ""
			return len(value), nil
		}
		// This converter retains text/tool metadata for terminal reconciliation.
		// Bound aggregate input as well as individual framing before adding to
		// that state; unlike a frame limit this covers arbitrarily many deltas.
		if w.conversionErr != nil {
			w.pending = ""
			return 0, w.conversionErr
		}
		if len(payload) > excelBPSMessagesMaxWireBytes-w.wireBytes || w.events >= excelBPSMessagesMaxEvents {
			w.pending = ""
			w.conversionErr = fmt.Errorf("%w: aggregate stream limit", errExcelBPSMessagesConversion)
			return 0, w.conversionErr
		}
		w.wireBytes += len(payload)
		w.events++
		// Some BPS streams contain only a terminal. Anthropic requires
		// message_start before every content/delta/stop sequence.
		if !w.state.MessageStartSent && event.Type != "response.created" {
			created := apicompat.ResponsesStreamEvent{Type: "response.created", Response: event.Response}
			if err := w.writeEvents(apicompat.ResponsesEventToAnthropicEvents(&created, w.state)); err != nil {
				return 0, err
			}
		}
		if err := w.writeEvents(apicompat.ResponsesEventToAnthropicEvents(&event, w.state)); err != nil {
			return 0, err
		}
		if event.Type == "response.completed" || event.Type == "response.incomplete" {
			w.done, w.pending = true, ""
			return len(value), nil
		}
	}
	if len(w.pending) > 16<<20 {
		w.pending = ""
		w.conversionErr = fmt.Errorf("%w: event size limit", errExcelBPSMessagesConversion)
		return 0, w.conversionErr
	}
	return len(value), nil
}

func (w *excelBPSMessagesWriter) writeEvents(events []apicompat.AnthropicStreamEvent) error {
	for _, event := range events {
		encoded, err := apicompat.ResponsesAnthropicEventToSSE(event)
		if err != nil {
			return err
		}
		if _, err = w.output.WriteString(encoded); err != nil {
			return err
		}
	}
	return nil
}

func excelBPSAnthropicErrorType(kind string) string {
	switch kind {
	case "invalid_request_error", "authentication_error", "permission_error", "not_found_error", "rate_limit_error":
		return kind
	case "service_unavailable_error":
		return "overloaded_error"
	default:
		return "api_error"
	}
}

func writeExcelBPSJSONError(c *gin.Context, messages *excelBPSMessagesRequest, status int, code, message string) {
	if messages != nil {
		c.Header("Content-Type", "application/json")
		writeAnthropicError(c, status, excelBPSAnthropicErrorType(excelBPSErrorType(status)), message)
		return
	}
	c.JSON(status, gin.H{"error": gin.H{"type": excelBPSErrorType(status), "code": code, "message": message}})
}

func writeExcelBPSMessagesResponse(c *gin.Context, completed []byte, messages *excelBPSMessagesRequest) error {
	var response apicompat.ResponsesResponse
	if err := json.Unmarshal(completed, &response); err != nil {
		return err
	}
	c.JSON(http.StatusOK, apicompat.ResponsesToAnthropic(&response, messages.OriginalModel))
	return nil
}
