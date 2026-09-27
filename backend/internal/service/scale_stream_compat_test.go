package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func scaleStreamText(wire string) string {
	var out strings.Builder
	for _, line := range strings.Split(wire, "\n") {
		if strings.HasPrefix(line, "data: ") && line != "data: [DONE]" {
			out.WriteString(gjson.Get(strings.TrimPrefix(line, "data: "), "choices.0.delta.content").String())
		}
	}
	return out.String()
}

func TestScaleStreamBPSChatTextRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, delta, first, second string
		done                       bool
	}{
		{"terminal suffix", "Hello", "Hello world", "", false},
		{"second terminal message", "first", "first", "second", false},
		{"complete snapshot is not repeated", "same", "same", "", false},
		{"text done supplies suffix", "Hello", "Hello world", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			writer := newExcelBPSChatWriter(&out, "gpt-5.6-sol", true)
			delta := incidentBPSFrame("response.output_text.delta", map[string]any{"output_index": 0, "content_index": 0, "item_id": "msg_1", "delta": tc.delta})
			_, err := writer.WriteString(delta)
			require.NoError(t, err)
			if tc.done {
				_, err = writer.WriteString(incidentBPSFrame("response.output_text.done", map[string]any{"output_index": 0, "content_index": 0, "item_id": "msg_1", "text": tc.first}))
				require.NoError(t, err)
			}
			output := []any{map[string]any{"id": "msg_1", "type": "message", "content": []any{map[string]any{"type": "output_text", "text": tc.first}}}}
			if tc.second != "" {
				output = append(output, map[string]any{"id": "msg_2", "type": "message", "content": []any{map[string]any{"type": "output_text", "text": tc.second}}})
			}
			if tc.done {
				output = []any{}
			}
			terminal := incidentBPSFrame("response.completed", map[string]any{"response": map[string]any{"id": "resp_recover", "status": "completed", "output": output}})
			for i := 0; i < len(terminal); i += 7 {
				_, err = writer.WriteString(terminal[i:min(i+7, len(terminal))])
				require.NoError(t, err)
			}
			require.Equal(t, tc.first+tc.second, scaleStreamText(out.String()))
			require.Equal(t, 1, strings.Count(out.String(), "data: [DONE]"))
		})
	}
}

func TestScaleStreamBPSBufferedDeltaRecovery(t *testing.T) {
	for _, chat := range []bool{false, true} {
		for _, incomplete := range []bool{false, true} {
			t.Run(fmt.Sprintf("chat=%t/incomplete=%t", chat, incomplete), func(t *testing.T) {
				status, kind := "completed", "response.completed"
				if incomplete {
					status, kind = "incomplete", "response.incomplete"
				}
				response := map[string]any{"id": "resp_delta", "status": status, "output": []any{}, "usage": map[string]any{"input_tokens": 4, "output_tokens": 2}}
				if incomplete {
					response["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
				}
				wire := incidentBPSFrame("response.output_text.delta", map[string]any{"item_id": "msg_1", "delta": "exact "}) + incidentBPSFrame("response.output_text.delta", map[string]any{"item_id": "msg_1", "delta": "answer"}) + incidentBPSFrame(kind, map[string]any{"response": response})
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}}
				svc := openAIClientToolsTestService(upstream)
				request := map[string]any{"model": "gpt-5.6-sol", "stream": false, "input": "hello"}
				path := "/v1/responses"
				if chat {
					delete(request, "input")
					request["messages"] = []any{map[string]any{"role": "user", "content": "hello"}}
					path = "/v1/chat/completions"
				}
				body, err := json.Marshal(request)
				require.NoError(t, err)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest("POST", path, bytes.NewReader(body))
				if chat {
					_, err = svc.ForwardAsChatCompletions(context.Background(), c, excelAccount(), body, "", "")
				} else {
					_, err = svc.Forward(context.Background(), c, excelAccount(), body)
				}
				if incomplete {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				require.Contains(t, rec.Body.String(), "exact answer")
				require.Len(t, upstream.requests, 1, "partial answers must never be regenerated after terminal")
			})
		}
	}
}

type scaleStreamCancelBody struct {
	io.ReadCloser
	started chan struct{}
	start   sync.Once
	closed  atomic.Bool
}

func (b *scaleStreamCancelBody) Read(p []byte) (int, error) {
	b.start.Do(func() { close(b.started) })
	return b.ReadCloser.Read(p)
}
func (b *scaleStreamCancelBody) Close() error { b.closed.Store(true); return b.ReadCloser.Close() }

func TestScaleStreamBPSConcurrentCancellation(t *testing.T) {
	const count = 24
	var group sync.WaitGroup
	errors := make(chan string, count)
	for i := 0; i < count; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			reader, writer := io.Pipe()
			defer writer.Close()
			upstreamBody := &scaleStreamCancelBody{ReadCloser: reader, started: make(chan struct{})}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: upstreamBody}}
			svc := openAIClientToolsTestService(upstream)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			go func() {
				select {
				case <-upstreamBody.started:
					cancel()
				case <-ctx.Done():
				}
			}()
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
			result, err := svc.forwardExcelBPS(ctx, c, excelAccount(), []byte("{\"model\":\"gpt-5.6-sol\",\"stream\":true,\"input\":\"hello\"}"), time.Now())
			if err == nil || result == nil || !result.ClientDisconnect || !upstreamBody.closed.Load() || len(upstream.requests) != 1 {
				errors <- fmt.Sprintf("cancel cleanup mismatch: err=%v result=%+v closed=%t calls=%d", err, result, upstreamBody.closed.Load(), len(upstream.requests))
			}
		}()
	}
	done := make(chan struct{})
	go func() { group.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent cancellations did not release streams")
	}
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

func TestScaleStreamBPSRetryKeepsOriginalBytes(t *testing.T) {
	body := []byte("{ \"model\": \"gpt-5.6-sol\", \"stream\": false, \"input\": \"keep whitespace  中文\", \"metadata\": {\"large\":9007199254740993} }")
	original := append([]byte(nil), body...)
	good := incidentBPSFrame("response.completed", map[string]any{"response": map[string]any{"id": "resp_exact", "status": "completed", "output": []any{}}})
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("busy"))},
		{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(good))},
	}}
	svc := openAIClientToolsTestService(upstream)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
	_, err := svc.Forward(context.Background(), c, excelAccount(), body)
	require.NoError(t, err)
	require.Equal(t, original, body, "retry preparation must never mutate the caller's ingress bytes")
	require.Len(t, upstream.bodies, 2)
	require.JSONEq(t, string(upstream.bodies[0]), string(upstream.bodies[1]))
}

func TestScaleStreamBPSChatMissingTerminalItemID(t *testing.T) {
	for _, knownID := range []bool{false, true} {
		t.Run(fmt.Sprint(knownID), func(t *testing.T) {
			var out strings.Builder
			writer := newExcelBPSChatWriter(&out, "gpt-5.6-sol", false)
			delta := map[string]any{"output_index": 0, "content_index": 0, "delta": "exact"}
			if knownID {
				delta["item_id"] = "msg_known"
			}
			_, err := writer.WriteString(incidentBPSFrame("response.output_text.delta", delta))
			require.NoError(t, err)
			_, err = writer.WriteString(incidentBPSFrame("response.completed", map[string]any{"response": map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "exact suffix"}}}}}}))
			require.NoError(t, err)
			require.Equal(t, "exact suffix", scaleStreamText(out.String()), "same indices without terminal item_id must not duplicate identified deltas")
		})
	}
}

func TestScaleStreamBPSBufferedItemBoundaries(t *testing.T) {
	events := []map[string]any{
		{"type": "response.reasoning_summary_text.delta", "output_index": 0, "summary_index": 1, "item_id": "reason_one", "delta": "summary-one"},
		{"type": "response.reasoning_summary_text.delta", "output_index": 0, "summary_index": 0, "item_id": "reason_one", "delta": "summary-zero"},
		{"type": "response.output_text.delta", "output_index": 1, "content_index": 1, "item_id": "msg_one", "delta": "second part"},
		{"type": "response.output_text.delta", "output_index": 2, "content_index": 0, "item_id": "msg_two", "delta": "another message"},
		{"type": "response.output_text.delta", "output_index": 1, "content_index": 0, "item_id": "msg_one", "delta": "first part"},
	}
	var wire strings.Builder
	for _, event := range events {
		wire.WriteString(incidentBPSFrame(event["type"].(string), event))
	}
	wire.WriteString(incidentBPSFrame("response.completed", map[string]any{"response": map[string]any{"status": "completed", "output": []any{}}}))
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire.String()))}}
	svc := openAIClientToolsTestService(upstream)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	_, err := svc.Forward(context.Background(), c, excelAccount(), []byte("{\"model\":\"gpt-5.6-sol\",\"input\":\"hello\",\"stream\":false}"))
	require.NoError(t, err)
	body := recorder.Body.String()
	require.Equal(t, int64(3), gjson.Get(body, "output.#").Int())
	require.Equal(t, "reason_one", gjson.Get(body, "output.0.id").String())
	require.Equal(t, "summary-zero", gjson.Get(body, "output.0.summary.0.text").String())
	require.Equal(t, "summary-one", gjson.Get(body, "output.0.summary.1.text").String())
	require.Equal(t, "msg_one", gjson.Get(body, "output.1.id").String())
	require.Equal(t, "first part", gjson.Get(body, "output.1.content.0.text").String())
	require.Equal(t, "second part", gjson.Get(body, "output.1.content.1.text").String())
	require.Equal(t, "msg_two", gjson.Get(body, "output.2.id").String())
	require.Equal(t, "another message", gjson.Get(body, "output.2.content.0.text").String())
}

func TestScaleStreamBPSBufferedOutputLimit(t *testing.T) {
	frame := incidentBPSFrame("response.output_text.delta", map[string]any{"delta": strings.Repeat("x", 1<<20)})
	wire := strings.Repeat(frame, 17) + incidentBPSFrame("response.completed", map[string]any{"response": map[string]any{"status": "completed", "output": []any{}}})
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}}
	svc := openAIClientToolsTestService(upstream)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	_, err := svc.Forward(context.Background(), c, excelAccount(), []byte("{\"model\":\"gpt-5.6-sol\",\"input\":\"hello\",\"stream\":false}"))
	require.Error(t, err)
	require.Equal(t, 502, recorder.Code)
	require.Contains(t, recorder.Body.String(), "basispoints_output_invalid")
	require.Len(t, upstream.requests, 1, "size exhaustion must not amplify load by replaying")
}
