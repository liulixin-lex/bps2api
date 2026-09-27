package apicompat

import (
	"bytes"
	"crypto/sha256"
	"hash"
)

// Keep a digest and byte count rather than copying or retaining a full growing
// streamed argument/text prefix. The authoritative done/terminal snapshot can
// then supply its exact missing suffix without duplicating delivered bytes.
type chatStreamPrefix struct {
	digest hash.Hash
	size   int
}

func (p *chatStreamPrefix) append(value string) {
	if p.digest == nil {
		p.digest = sha256.New()
	}
	_, _ = p.digest.Write([]byte(value))
	p.size += len(value)
}

func (p *chatStreamPrefix) remainder(complete string) (string, bool) {
	if len(complete) < p.size {
		return "", false
	}
	if p.size > 0 {
		sum := sha256.Sum256([]byte(complete[:p.size]))
		if !bytes.Equal(sum[:], p.digest.Sum(nil)) {
			return "", false
		}
	}
	return complete[p.size:], true
}

type chatTextPart struct {
	itemID                    string
	outputIndex, contentIndex int
}

func chatTextPartOf(event *ResponsesStreamEvent, state *ResponsesEventToChatState) chatTextPart {
	part := responsesTextPart{OutputIndex: event.OutputIndex, ContentIndex: event.ContentIndex}
	id := event.ItemID
	if id != "" {
		if previous, exists := state.textPartAliases[part]; !exists {
			state.textPartAliases[part] = id
		} else if previous != id {
			// Reused indices are ambiguous. Keep explicit IDs authoritative,
			// but never guess their identity for a later ID-less snapshot.
			state.textPartAliases[part] = ""
		}
	} else if previous := state.textPartAliases[part]; previous != "" {
		id = previous
	}
	key := chatTextPart{itemID: id, outputIndex: event.OutputIndex, contentIndex: event.ContentIndex}
	if id != "" {
		key.outputIndex = -1
	}
	return key
}

func resToChatRecoverText(text string, event *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	if event.ItemID == "" {
		part := responsesTextPart{OutputIndex: event.OutputIndex, ContentIndex: event.ContentIndex}
		if alias, exists := state.textPartAliases[part]; exists && alias == "" {
			return nil
		}
	}
	key := chatTextPartOf(event, state)
	prefix, known := state.textPrefixes[key]
	if !known && event.ItemID != "" {
		// Older compatible deltas omit item_id. In that case output/content indices
		// are the available identity; use that exact entry instead of a global flag.
		fallback := chatTextPart{outputIndex: event.OutputIndex, contentIndex: event.ContentIndex}
		if prior, ok := state.textPrefixes[fallback]; ok {
			prefix, known = prior, true
			state.textPrefixes[key] = prior
			delete(state.textPrefixes, fallback)
		}
	}
	if !known {
		// Output/content indices remain the protocol identity when item_id is
		// omitted; different parts must not be suppressed by one global SawText.
		prefix = &chatStreamPrefix{}
		state.textPrefixes[key] = prefix
	}
	tail, matches := prefix.remainder(text)
	if !matches || tail == "" {
		return nil
	}
	prefix.append(tail)
	state.SawText = true
	return []ChatCompletionsChunk{makeChatDeltaChunk(state, ChatDelta{Content: &tail})}
}

func resToChatRecoverTerminalText(event *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	if event.Response == nil || event.Type == "response.failed" {
		return nil
	}
	var chunks []ChatCompletionsChunk
	for i, item := range event.Response.Output {
		if item.Type != "message" {
			continue
		}
		for j, part := range item.Content {
			if part.Type != "output_text" {
				continue
			}
			source := &ResponsesStreamEvent{ItemID: item.ID, OutputIndex: i, ContentIndex: j}
			chunks = append(chunks, resToChatRecoverText(part.Text, source, state)...)
		}
	}
	return chunks
}
