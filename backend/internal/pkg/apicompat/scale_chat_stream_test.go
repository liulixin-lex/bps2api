package apicompat

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScaleChatArgumentFragmentsPreserveExactSuffix(t *testing.T) {
	state := NewResponsesEventToChatState()
	ResponsesEventToChatChunks(&ResponsesStreamEvent{Type: "response.output_item.added", OutputIndex: 3, Item: &ResponsesOutput{Type: "function_call", CallID: "call_exact", Name: "write_range"}}, state)
	prefix := strings.Repeat("x", 256<<10)
	var emitted strings.Builder
	for i := 0; i < len(prefix); i += 1024 {
		for _, chunk := range ResponsesEventToChatChunks(&ResponsesStreamEvent{Type: "response.function_call_arguments.delta", OutputIndex: 3, Delta: prefix[i : i+1024]}, state) {
			_, _ = emitted.WriteString(chunk.Choices[0].Delta.ToolCalls[0].Function.Arguments)
		}
	}
	full := prefix + "\r\n中文9007199254740993"
	for _, chunk := range ResponsesEventToChatChunks(&ResponsesStreamEvent{Type: "response.function_call_arguments.done", OutputIndex: 3, Arguments: full}, state) {
		_, _ = emitted.WriteString(chunk.Choices[0].Delta.ToolCalls[0].Function.Arguments)
	}
	require.Equal(t, full, emitted.String())
	require.Empty(t, ResponsesEventToChatChunks(&ResponsesStreamEvent{Type: "response.function_call_arguments.done", OutputIndex: 3, Arguments: full}, state))
	require.Empty(t, ResponsesEventToChatChunks(&ResponsesStreamEvent{Type: "response.function_call_arguments.done", OutputIndex: 3, Arguments: "different"}, state))
}

func BenchmarkScaleChatToolArguments(b *testing.B) {
	chunk := strings.Repeat("x", 1024)
	b.ReportAllocs()
	for n := 0; n < b.N; n++ {
		state := NewResponsesEventToChatState()
		ResponsesEventToChatChunks(&ResponsesStreamEvent{Type: "response.output_item.added", OutputIndex: 0, Item: &ResponsesOutput{Type: "function_call", CallID: "call_exact", Name: "write_range"}}, state)
		for i := 0; i < 256; i++ {
			ResponsesEventToChatChunks(&ResponsesStreamEvent{Type: "response.function_call_arguments.delta", Delta: chunk}, state)
		}
	}
}

func TestScaleChatTextIdentityAliases(t *testing.T) {
	for _, tc := range []struct{ name, deltaID, terminalID string }{
		{"delta identity only", "msg_1", ""},
		{"terminal identity only", "", "msg_1"},
		{"same explicit identity", "msg_1", "msg_1"},
		{"legacy indexed identity", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := NewResponsesEventToChatState()
			var text strings.Builder
			emit := func(event *ResponsesStreamEvent) {
				for _, chunk := range ResponsesEventToChatChunks(event, state) {
					for _, choice := range chunk.Choices {
						if choice.Delta.Content != nil {
							_, _ = text.WriteString(*choice.Delta.Content)
						}
					}
				}
			}
			emit(&ResponsesStreamEvent{Type: "response.output_text.delta", ItemID: tc.deltaID, Delta: "prefix"})
			emit(&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{Status: "completed", Output: []ResponsesOutput{{Type: "message", ID: tc.terminalID, Content: []ResponsesContentPart{{Type: "output_text", Text: "prefix-tail"}}}}}})
			require.Equal(t, "prefix-tail", text.String())
			require.Empty(t, ResponsesEventToChatChunks(&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{Status: "completed"}}, state), "terminal must be idempotent")
		})
	}
}
