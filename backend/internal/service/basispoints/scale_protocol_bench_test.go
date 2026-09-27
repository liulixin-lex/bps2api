package basispoints

import (
	"encoding/json"
	"strings"
	"testing"
)

func BenchmarkScaleProtocolLargeImageRoute(b *testing.B) {
	source := object{"model": "gpt-5.6-sol", "input": []any{object{"role": "user", "content": []any{object{"type": "input_image", "image_url": "data:image/png;base64," + strings.Repeat("A", 8<<20), "detail": "original"}}}}, "tools": []any{object{"type": "function", "name": "read"}}, "reasoning": object{"effort": "high"}}
	raw, _ := json.Marshal(source)
	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := NativeFallbackReason(raw); got != "" {
			b.Fatalf("image request unexpectedly bypassed BPS: %q", got)
		}
	}
}
