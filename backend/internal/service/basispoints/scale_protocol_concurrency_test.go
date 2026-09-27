package basispoints

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestScaleProtocolReplayConcurrentIsolation(t *testing.T) {
	cache := new(ReplayCache)
	const workers = 24
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			scope := fmt.Sprintf("account/%d", worker)
			for round := 0; round < 32; round++ {
				id := fmt.Sprintf("call_%d", round)
				client := object{"type": "function_call", "call_id": id, "name": "read", "arguments": "{}"}
				original := object{"type": "function_call", "call_id": id, "name": "run_officejs", "arguments": strings.Repeat(scope, 256)}
				cache.put(scope, id, original, client)
				received := cache.getForCall(scope, id, client)
				if received == nil || received["arguments"] != original["arguments"] {
					t.Errorf("scoped replay mismatch for %s/%s", scope, id)
					return
				}
				received["arguments"] = "caller mutation"
				again := cache.getForCall(scope, id, client)
				if again == nil || again["arguments"] != original["arguments"] {
					t.Errorf("returned map aliased immutable cache bytes")
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.entries) > 1024 || cache.bytes > 16<<20 {
		t.Fatal("cache storage limits exceeded")
	}
}

func BenchmarkScaleProtocolReplayParallel(b *testing.B) {
	cache := new(ReplayCache)
	original := object{"type": "function_call", "call_id": "call_shared", "name": "run_officejs", "arguments": strings.Repeat("source text ", 8192)}
	cache.put("scope", "call_shared", original)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if cache.get("scope", "call_shared") == nil {
				b.Error("replay entry disappeared")
			}
		}
	})
}
