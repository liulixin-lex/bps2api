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
	items        map[int]*excelBPSBufferedItem
	bytes, parts int
}
type excelBPSBufferedItem struct {
	kind, id string
	parts    map[int]*strings.Builder
}

func newExcelBPSBufferedOutput() *excelBPSBufferedOutput {
	return &excelBPSBufferedOutput{items: make(map[int]*excelBPSBufferedItem)}
}
func (b *excelBPSBufferedOutput) HasContent() bool { return b.bytes > 0 }
func (b *excelBPSBufferedOutput) Observe(event *apicompat.ResponsesStreamEvent) error {
	if event.Delta == "" {
		return nil
	}
	kind, index := "message", event.ContentIndex
	if event.Type == "response.reasoning_summary_text.delta" {
		kind, index = "reasoning", event.SummaryIndex
	}
	if event.OutputIndex < 0 || index < 0 || len(event.Delta) > excelBPSBufferedOutputMaxBytes-b.bytes {
		return fmt.Errorf("invalid or oversized BPS buffered output")
	}
	item := b.items[event.OutputIndex]
	if item == nil {
		if len(b.items) >= 1024 {
			return fmt.Errorf("too many BPS buffered output items")
		}
		item = &excelBPSBufferedItem{kind: kind, id: event.ItemID, parts: make(map[int]*strings.Builder)}
		b.items[event.OutputIndex] = item
	} else if item.kind != kind || (item.id != "" && event.ItemID != "" && item.id != event.ItemID) {
		return fmt.Errorf("inconsistent BPS buffered output identity")
	}
	if item.id == "" {
		item.id = event.ItemID
	}
	part := item.parts[index]
	if part == nil {
		if b.parts >= 4096 {
			return fmt.Errorf("too many BPS buffered content parts")
		}
		part = &strings.Builder{}
		item.parts[index] = part
		b.parts++
	}
	_, _ = part.WriteString(event.Delta)
	b.bytes += len(event.Delta)
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
				result.Content = append(result.Content, apicompat.ResponsesContentPart{Type: "output_text", Text: item.parts[part].String()})
			}
		} else {
			for _, part := range parts {
				result.Summary = append(result.Summary, apicompat.ResponsesSummary{Type: "summary_text", Text: item.parts[part].String()})
			}
		}
		output = append(output, result)
	}
	return output
}
