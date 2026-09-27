package service

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
)

const excelBPSBufferedOutputMaxBytes = 16 << 20

// Preserve item/part boundaries when an explicit terminal snapshot is empty.
// Never retain unvalidated tool arguments or allocate using untrusted indices.
type excelBPSBufferedOutput struct {
	items                       map[int]*excelBPSBufferedItem
	bytes, metadataBytes, parts int
}
type excelBPSBufferedItem struct {
	kind, id string
	parts    map[int]*excelBPSBufferedPart
}
type excelBPSBufferedPart struct {
	text strings.Builder
	done bool
}

func newExcelBPSBufferedOutput() *excelBPSBufferedOutput {
	return &excelBPSBufferedOutput{items: make(map[int]*excelBPSBufferedItem)}
}
func (b *excelBPSBufferedOutput) HasContent() bool { return b.bytes > 0 }
func (b *excelBPSBufferedOutput) Observe(event *apicompat.ResponsesStreamEvent) error {
	kind, index, value, done := "message", event.ContentIndex, event.Delta, false
	switch event.Type {
	case "response.output_text.delta":
	case "response.output_text.done":
		value, done = event.Text, true
	case "response.reasoning_summary_text.delta":
		kind, index = "reasoning", event.SummaryIndex
	case "response.reasoning_summary_text.done":
		kind, index, value, done = "reasoning", event.SummaryIndex, event.Text, true
	default:
		// Raw reasoning and unvalidated tool arguments must never become text.
		return nil
	}
	if value == "" && !done {
		return nil
	}
	if event.OutputIndex < 0 || index < 0 {
		return fmt.Errorf("invalid or oversized BPS buffered output")
	}
	item := b.items[event.OutputIndex]
	var part *excelBPSBufferedPart
	idBytes := 0
	if item == nil {
		if len(b.items) >= 1024 {
			return fmt.Errorf("too many BPS buffered output items")
		}
		idBytes = len(event.ItemID)
	} else {
		if item.kind != kind || (item.id != "" && event.ItemID != "" && item.id != event.ItemID) {
			return fmt.Errorf("inconsistent BPS buffered output identity")
		}
		if item.id == "" {
			idBytes = len(event.ItemID)
		}
		part = item.parts[index]
	}
	if part != nil {
		if done {
			previous := part.text.String()
			if !strings.HasPrefix(value, previous) || (part.done && value != previous) {
				return fmt.Errorf("inconsistent BPS buffered completed text")
			}
			value = value[len(previous):]
		} else if part.done {
			return fmt.Errorf("BPS buffered text delta follows completion")
		}
	}
	// The aggregate budget includes retained item IDs; otherwise many tiny
	// text deltas with large IDs could retain gigabytes despite the text cap.
	remaining := excelBPSBufferedOutputMaxBytes - b.bytes - b.metadataBytes
	if idBytes > remaining || len(value) > remaining-idBytes {
		return fmt.Errorf("invalid or oversized BPS buffered output")
	}
	if part == nil && b.parts >= 4096 {
		return fmt.Errorf("too many BPS buffered content parts")
	}
	// Validate the whole event before mutating state so a rejected event cannot
	// consume an identity, part slot, or change a previously accepted prefix.
	if item == nil {
		item = &excelBPSBufferedItem{kind: kind, id: event.ItemID, parts: make(map[int]*excelBPSBufferedPart)}
		b.items[event.OutputIndex] = item
	} else if item.id == "" {
		item.id = event.ItemID
	}
	if part == nil {
		part = &excelBPSBufferedPart{}
		item.parts[index] = part
		b.parts++
	}
	_, _ = part.text.WriteString(value)
	part.done = done
	b.bytes += len(value)
	b.metadataBytes += idBytes
	return nil
}
func (b *excelBPSBufferedOutput) BuildOutput(status string) []apicompat.ResponsesOutput {
	indices := make([]int, 0, len(b.items))
	for index := range b.items {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	output := make([]apicompat.ResponsesOutput, 0, len(indices))
	for _, index := range indices {
		item := b.items[index]
		result := apicompat.ResponsesOutput{ID: item.id, Type: item.kind}
		parts := make([]int, 0, len(item.parts))
		for part := range item.parts {
			parts = append(parts, part)
		}
		sort.Ints(parts)
		if item.kind == "message" {
			result.Role = "assistant"
			result.Status = status
			for _, part := range parts {
				result.Content = append(result.Content, apicompat.ResponsesContentPart{Type: "output_text", Text: item.parts[part].text.String()})
			}
		} else {
			for _, part := range parts {
				result.Summary = append(result.Summary, apicompat.ResponsesSummary{Type: "summary_text", Text: item.parts[part].text.String()})
			}
		}
		output = append(output, result)
	}
	return output
}
