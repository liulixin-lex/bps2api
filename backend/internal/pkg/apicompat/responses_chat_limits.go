package apicompat

import "errors"

const (
	chatStreamMaxMetadataEntries = 8192
	chatStreamMaxIdentityBytes   = 1 << 20
	chatStreamMaxToolCalls       = 1024
)

// ErrChatStreamMetadataLimit denotes an upstream stream whose retained routing
// metadata exceeds the bounded converter's per-response allowance.
var ErrChatStreamMetadataLimit = errors.New("responses chat stream metadata exceeds the resource limit")

type chatStreamMetadata struct {
	entries, identityBytes int
}

// ResponsesEventToChatChunksChecked enables bounded metadata accounting for
// this state while preserving the original converter API for other adapters.
// A failed event emits no chunks, the error is sticky, and finalization cannot
// turn a resource failure into an apparently successful finish chunk.
func ResponsesEventToChatChunksChecked(event *ResponsesStreamEvent, state *ResponsesEventToChatState) ([]ChatCompletionsChunk, error) {
	if state.streamErr != nil {
		return nil, state.streamErr
	}
	if state.streamMetadata == nil {
		state.enableStreamMetadataGuard()
	}
	if state.streamErr != nil {
		return nil, state.streamErr
	}
	chunks := ResponsesEventToChatChunks(event, state)
	if state.streamErr != nil {
		return nil, state.streamErr
	}
	return chunks, nil
}

func (s *ResponsesEventToChatState) enableStreamMetadataGuard() {
	s.streamMetadata = &chatStreamMetadata{}
	if s.NextToolCallIndex > chatStreamMaxToolCalls || !s.reserveStreamMetadata(0, len(s.ID)+len(s.Model)+len(s.ServiceTier)) {
		s.streamErr = ErrChatStreamMetadataLimit
		return
	}
	for key := range s.textPrefixes {
		if !s.reserveStreamMetadata(1, len(key.itemID)) {
			return
		}
	}
	for _, id := range s.textPartAliases {
		if !s.reserveStreamMetadata(1, len(id)) {
			return
		}
	}
	if !s.reserveStreamMetadata(len(s.argumentPrefixes)+len(s.OutputIndexToToolIndex), 0) {
		return
	}
}

func (s *ResponsesEventToChatState) reserveStreamMetadata(entries, identityBytes int) bool {
	if s.streamMetadata == nil {
		return true
	}
	if s.streamErr != nil {
		return false
	}
	m := s.streamMetadata
	if entries > chatStreamMaxMetadataEntries-m.entries || identityBytes > chatStreamMaxIdentityBytes-m.identityBytes {
		s.streamErr = ErrChatStreamMetadataLimit
		return false
	}
	m.entries += entries
	m.identityBytes += identityBytes
	return true
}
