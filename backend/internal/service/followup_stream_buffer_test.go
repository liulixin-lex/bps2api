package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestFollowupBPSBufferedDoneSnapshots(t *testing.T) {
	for _, kind := range []string{"output_text", "reasoning_summary_text"} {
		for _, prefix := range []string{"", "exact "} {
			t.Run(kind+"/prefix="+prefix, func(t *testing.T) {
				b := newExcelBPSBufferedOutput()
				event := apicompat.ResponsesStreamEvent{Type: "response." + kind + ".delta", ItemID: "item_exact", OutputIndex: 3, ContentIndex: 2, SummaryIndex: 1, Delta: prefix}
				require.NoError(t, b.Observe(&event))
				event.Type, event.Delta, event.Text = "response."+kind+".done", "", "exact answer 中文\r\n9007199254740993"
				require.NoError(t, b.Observe(&event))
				require.NoError(t, b.Observe(&event), "duplicate done must be idempotent")
				require.True(t, b.HasContent())
				out := b.BuildOutput("incomplete")
				require.Len(t, out, 1)
				require.Equal(t, "item_exact", out[0].ID)
				if kind == "output_text" {
					require.Equal(t, "incomplete", out[0].Status)
					require.Equal(t, event.Text, out[0].Content[0].Text)
				} else {
					require.Equal(t, event.Text, out[0].Summary[0].Text)
				}
			})
		}
	}
}

func TestFollowupBPSBufferedConflictingDone(t *testing.T) {
	for _, kind := range []string{"output_text", "reasoning_summary_text"} {
		for _, complete := range []string{"different", "pre", ""} {
			t.Run(kind+"/done="+complete, func(t *testing.T) {
				b := newExcelBPSBufferedOutput()
				require.NoError(t, b.Observe(&apicompat.ResponsesStreamEvent{Type: "response." + kind + ".delta", Delta: "prefix"}))
				require.Error(t, b.Observe(&apicompat.ResponsesStreamEvent{Type: "response." + kind + ".done", Text: complete}), "must not guess a different authoritative snapshot")
			})
		}
	}
}

func TestFollowupBPSBufferedDoneLifecycle(t *testing.T) {
	b := newExcelBPSBufferedOutput()
	require.NoError(t, b.Observe(&apicompat.ResponsesStreamEvent{Type: "response.output_text.done", Text: "finished"}))
	require.Error(t, b.Observe(&apicompat.ResponsesStreamEvent{Type: "response.output_text.delta", Delta: "unexpected late delta"}))
	require.Equal(t, "finished", b.BuildOutput("completed")[0].Content[0].Text)
}

func TestFollowupBPSBufferedMetadataBudget(t *testing.T) {
	b := newExcelBPSBufferedOutput()
	largeID := strings.Repeat("i", 9<<20)
	require.NoError(t, b.Observe(&apicompat.ResponsesStreamEvent{Type: "response.output_text.delta", ItemID: largeID, Delta: "a"}))
	require.Error(t, b.Observe(&apicompat.ResponsesStreamEvent{Type: "response.output_text.delta", OutputIndex: 1, ItemID: largeID, Delta: "b"}), "retained IDs and text must share the aggregate memory budget")
	require.Len(t, b.BuildOutput("completed"), 1, "rejected input must not partially mutate the buffer")
}

func TestFollowupBPSBufferedDoneBudget(t *testing.T) {
	b := newExcelBPSBufferedOutput()
	require.Error(t, b.Observe(&apicompat.ResponsesStreamEvent{Type: "response.output_text.done", Text: strings.Repeat("x", 17<<20)}))
	require.False(t, b.HasContent())
	require.Empty(t, b.BuildOutput("completed"))
}

func TestFollowupBPSBufferedIgnoresUnsupportedEvents(t *testing.T) {
	b := newExcelBPSBufferedOutput()
	require.NoError(t, b.Observe(&apicompat.ResponsesStreamEvent{Type: "response.reasoning_text.delta", Delta: "private raw reasoning"}))
	require.NoError(t, b.Observe(&apicompat.ResponsesStreamEvent{Type: "response.function_call_arguments.delta", Delta: "unvalidated arguments"}))
	require.False(t, b.HasContent())
	require.Empty(t, b.BuildOutput("completed"))
}

func TestFollowupBPSBufferedDoneGateway(t *testing.T) {
	for _, chat := range []bool{false, true} {
		name, path := "responses", "/v1/responses"
		body := []byte(`{"model":"gpt-5.6-sol","stream":false,"input":"hello"}`)
		if chat {
			name, path = "chat", "/v1/chat/completions"
			body = []byte(`{"model":"gpt-5.6-sol","stream":false,"messages":[{"role":"user","content":"hello"}]}`)
		}
		t.Run(name, func(t *testing.T) {
			wire := incidentBPSFrame("response.output_text.delta", map[string]any{"item_id": "msg_1", "delta": "exact "}) +
				incidentBPSFrame("response.output_text.done", map[string]any{"item_id": "msg_1", "text": "exact complete answer"}) +
				incidentBPSFrame("response.completed", map[string]any{"response": map[string]any{"status": "completed", "output": []any{}}})
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}}
			svc := openAIClientToolsTestService(upstream)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest("POST", path, nil)
			var err error
			if chat {
				_, err = svc.ForwardAsChatCompletions(context.Background(), c, excelAccount(), body, "", "")
			} else {
				_, err = svc.Forward(context.Background(), c, excelAccount(), body)
			}
			require.NoError(t, err)
			field := "output.0.content.0.text"
			if chat {
				field = "choices.0.message.content"
			}
			require.Equal(t, "exact complete answer", gjson.Get(recorder.Body.String(), field).String())
			require.Len(t, upstream.requests, 1)
		})
	}
}
