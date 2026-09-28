package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// The expanded generation check must retain this fork's Sol/Luna sampling
// exception and its developer role, including when the model is newly covered.
func TestChatCompletionsToResponses_GenerationSamplingCompatibility(t *testing.T) {
	for _, tc := range []struct {
		model        string
		effort       string
		wantSampling bool
	}{
		{"gpt-6-astra", "none", false},
		{"gpt-7-future", "none", false},
		{"gpt-6-sol", "none", true},
		{"gpt-6-luna", "none", true},
		{"gpt-6-sol", "medium", false},
		{"gpt-6-luna", "", false},
		{"gpt-5.5", "none", false},
		{"gpt-4o", "", true},
	} {
		t.Run(tc.model+"/"+tc.effort, func(t *testing.T) {
			temperature, topP := 0.7, 0.8
			out, err := ChatCompletionsToResponses(&ChatCompletionsRequest{
				Model:           tc.model,
				ReasoningEffort: tc.effort,
				Temperature:     &temperature,
				TopP:            &topP,
				Messages:        []ChatMessage{{Role: "developer", Content: json.RawMessage("\"Keep the developer role\"")}},
			})
			require.NoError(t, err)
			if tc.wantSampling {
				require.Equal(t, &temperature, out.Temperature)
				require.Equal(t, &topP, out.TopP)
			} else {
				require.Nil(t, out.Temperature)
				require.Nil(t, out.TopP)
			}
			var items []ResponsesInputItem
			require.NoError(t, json.Unmarshal(out.Input, &items))
			require.Len(t, items, 1)
			require.Equal(t, "developer", items[0].Role)
		})
	}
}

func TestAnthropicEventToResponses_ToolInputDeltaOverridesDifferentSeed(t *testing.T) {
	const delta = "{\"value\":\"delta\"}"
	events := collectToolCallStreamEvents(t, json.RawMessage("{\"value\":\"seed\"}"), []string{delta})
	item := findFunctionCallOutput(events)
	require.NotNil(t, item)
	require.JSONEq(t, delta, item.Arguments)
	require.Equal(t, item.Arguments, concatArgumentDeltas(events))
}

func TestAnthropicEventToResponses_EmptyToolDeltaKeepsInlineInput(t *testing.T) {
	const args = "{\"value\":\"inline\"}"
	events := collectToolCallStreamEvents(t, json.RawMessage(args), []string{""})
	item := findFunctionCallOutput(events)
	require.NotNil(t, item)
	require.Equal(t, args, item.Arguments)
	require.Equal(t, args, concatArgumentDeltas(events))
}

func TestAnthropicEventToResponses_ToolInputDoesNotLeakIntoNextTool(t *testing.T) {
	const first = "{\"value\":\"first\"}"
	state := NewAnthropicEventToResponsesState()
	for i, input := range []json.RawMessage{json.RawMessage(first), nil} {
		AnthropicEventToResponsesEvents(&AnthropicStreamEvent{
			Type:  "content_block_start",
			Index: &i,
			ContentBlock: &AnthropicContentBlock{
				Type: "tool_use", ID: "toolu_sequence", Name: "eval", Input: input,
			},
		}, state)
		AnthropicEventToResponsesEvents(&AnthropicStreamEvent{Type: "content_block_stop", Index: &i}, state)
		require.Empty(t, state.PendingToolInput)
	}
	require.Len(t, state.Outputs, 2)
	require.JSONEq(t, first, state.Outputs[0].Arguments)
	require.Equal(t, "{}", state.Outputs[1].Arguments)
}

func TestAnthropicEventToResponses_ToolInputNullAndWhitespacePreserveFallback(t *testing.T) {
	for _, input := range []json.RawMessage{json.RawMessage("null"), json.RawMessage(" \n\t ")} {
		events := collectToolCallStreamEvents(t, input, nil)
		item := findFunctionCallOutput(events)
		require.NotNil(t, item)
		require.Equal(t, "{}", item.Arguments)
		require.Empty(t, concatArgumentDeltas(events))
	}
}
