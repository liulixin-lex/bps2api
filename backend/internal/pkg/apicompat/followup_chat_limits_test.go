package apicompat

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFollowupChatCheckedMetadataCardinality(t *testing.T) {
	state := NewResponsesEventToChatState()
	for i := 0; i < chatStreamMaxMetadataEntries/2; i++ {
		chunks, err := ResponsesEventToChatChunksChecked(&ResponsesStreamEvent{Type: "response.output_text.delta", ItemID: fmt.Sprintf("msg_%d", i), OutputIndex: i, Delta: "x"}, state)
		require.NoError(t, err)
		require.Len(t, chunks, 1)
	}
	event := &ResponsesStreamEvent{Type: "response.output_text.delta", ItemID: "over_limit", OutputIndex: chatStreamMaxMetadataEntries, Delta: "must not emit"}
	chunks, err := ResponsesEventToChatChunksChecked(event, state)
	require.ErrorIs(t, err, ErrChatStreamMetadataLimit)
	require.Empty(t, chunks)
	require.LessOrEqual(t, len(state.textPrefixes)+len(state.textPartAliases), chatStreamMaxMetadataEntries)
	chunks, err = ResponsesEventToChatChunksChecked(&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{Status: "completed"}}, state)
	require.ErrorIs(t, err, ErrChatStreamMetadataLimit)
	require.Empty(t, chunks)
	require.Empty(t, ResponsesEventToChatChunks(event, state), "switching APIs on a guarded state cannot bypass its sticky failure")
	require.Empty(t, FinalizeResponsesChatStream(state), "a resource failure must never produce a successful finish")
}

func TestFollowupChatCheckedMetadataIdentityBytes(t *testing.T) {
	for _, kind := range []string{"delta", "terminal", "response ID", "service tier"} {
		t.Run(kind, func(t *testing.T) {
			state := NewResponsesEventToChatState()
			id := strings.Repeat("i", 600<<10)
			event := &ResponsesStreamEvent{Type: "response.output_text.delta", ItemID: id, Delta: "must not emit"}
			switch kind {
			case "terminal":
				event = &ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{Status: "completed", Output: []ResponsesOutput{{Type: "message", ID: id, Content: []ResponsesContentPart{{Type: "output_text", Text: "must not emit"}}}}}}
			case "response ID":
				event = &ResponsesStreamEvent{Type: "response.created", Response: &ResponsesResponse{ID: strings.Repeat("r", chatStreamMaxIdentityBytes+1)}}
			case "service tier":
				event = &ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{Status: "completed", ServiceTier: strings.Repeat("t", chatStreamMaxIdentityBytes+1)}}
			}
			chunks, err := ResponsesEventToChatChunksChecked(event, state)
			require.ErrorIs(t, err, ErrChatStreamMetadataLimit)
			require.Empty(t, chunks)
			require.LessOrEqual(t, state.streamMetadata.identityBytes, chatStreamMaxIdentityBytes)
			require.Empty(t, FinalizeResponsesChatStream(state))
		})
	}
}

func TestFollowupChatCheckedPrefixMigrationAndAccounting(t *testing.T) {
	state := NewResponsesEventToChatState()
	var output strings.Builder
	for _, event := range []*ResponsesStreamEvent{
		{Type: "response.output_text.delta", OutputIndex: 3, Delta: "pre"},
		{Type: "response.output_text.delta", OutputIndex: 3, ItemID: "msg_3", Delta: "fix"},
		{Type: "response.output_text.done", OutputIndex: 3, ItemID: "msg_3", Text: "prefix-tail"},
		{Type: "response.output_text.done", OutputIndex: 3, ItemID: "msg_3", Text: "prefix-tail"},
	} {
		chunks, err := ResponsesEventToChatChunksChecked(event, state)
		require.NoError(t, err)
		for _, chunk := range chunks {
			for _, choice := range chunk.Choices {
				if choice.Delta.Content != nil {
					output.WriteString(*choice.Delta.Content)
				}
			}
		}
	}
	require.Equal(t, "prefix-tail", output.String())
	require.Equal(t, 2, state.streamMetadata.entries, "migration replaces the anonymous key instead of consuming another entry")
	require.Equal(t, len(state.ID)+2*len("msg_3"), state.streamMetadata.identityBytes)
	_, err := ResponsesEventToChatChunksChecked(&ResponsesStreamEvent{Type: "response.output_text.delta", OutputIndex: 3, ItemID: "other", Delta: "other"}, state)
	require.NoError(t, err)
	require.Equal(t, "", state.textPartAliases[responsesTextPart{OutputIndex: 3}])
	require.Equal(t, len(state.ID)+len("msg_3")+len("other"), state.streamMetadata.identityBytes, "an ambiguous alias releases its previous retained string budget")
}

func TestFollowupChatCheckedToolCardinality(t *testing.T) {
	state := NewResponsesEventToChatState()
	for index := 0; index < chatStreamMaxToolCalls; index++ {
		_, err := ResponsesEventToChatChunksChecked(&ResponsesStreamEvent{Type: "response.output_item.added", OutputIndex: index, Item: &ResponsesOutput{Type: "function_call", CallID: fmt.Sprintf("call_%d", index), Name: "tool"}}, state)
		require.NoError(t, err)
		_, err = ResponsesEventToChatChunksChecked(&ResponsesStreamEvent{Type: "response.function_call_arguments.done", OutputIndex: index, Arguments: "{}"}, state)
		require.NoError(t, err)
	}
	chunks, err := ResponsesEventToChatChunksChecked(&ResponsesStreamEvent{Type: "response.output_item.added", OutputIndex: chatStreamMaxToolCalls, Item: &ResponsesOutput{Type: "function_call", CallID: "extra", Name: "tool"}}, state)
	require.ErrorIs(t, err, ErrChatStreamMetadataLimit)
	require.Empty(t, chunks)
	require.Len(t, state.OutputIndexToToolIndex, chatStreamMaxToolCalls)
	require.Len(t, state.argumentPrefixes, chatStreamMaxToolCalls)
}

func TestFollowupChatCheckedLargeArgumentsDoNotConsumeIdentityBudget(t *testing.T) {
	state := NewResponsesEventToChatState()
	_, err := ResponsesEventToChatChunksChecked(&ResponsesStreamEvent{Type: "response.output_item.added", Item: &ResponsesOutput{Type: "function_call", CallID: "call_exact", Name: "tool"}}, state)
	require.NoError(t, err)
	prefix := strings.Repeat("x", 2<<20)
	for start := 0; start < len(prefix); start += 16 << 10 {
		_, err = ResponsesEventToChatChunksChecked(&ResponsesStreamEvent{Type: "response.function_call_arguments.delta", Delta: prefix[start : start+(16<<10)]}, state)
		require.NoError(t, err)
	}
	chunks, err := ResponsesEventToChatChunksChecked(&ResponsesStreamEvent{Type: "response.function_call_arguments.done", Arguments: prefix + "-tail"}, state)
	require.NoError(t, err)
	require.Equal(t, "-tail", chunks[0].Choices[0].Delta.ToolCalls[0].Function.Arguments)
	require.Equal(t, len(state.ID), state.streamMetadata.identityBytes)
	require.Equal(t, 2, state.streamMetadata.entries)
}

func TestFollowupChatCheckedAdoptsLegacyStateWithoutEviction(t *testing.T) {
	state := NewResponsesEventToChatState()
	for index := 0; index < chatStreamMaxMetadataEntries/2+1; index++ {
		chunks := ResponsesEventToChatChunks(&ResponsesStreamEvent{Type: "response.output_text.delta", ItemID: fmt.Sprintf("msg_%d", index), OutputIndex: index, Delta: "x"}, state)
		require.Len(t, chunks, 1, "legacy adapters retain the original converter contract")
	}
	before := len(state.textPrefixes)
	chunks, err := ResponsesEventToChatChunksChecked(&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{Status: "completed"}}, state)
	require.ErrorIs(t, err, ErrChatStreamMetadataLimit)
	require.Empty(t, chunks)
	require.Len(t, state.textPrefixes, before, "existing identities must never be evicted to hide a limit")
	require.Empty(t, FinalizeResponsesChatStream(state))
}
