package basispoints

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPreOutputUnknownRepairPreservesOperationalUsage(t *testing.T) {
	source := testSource()
	source["tools"] = []any{object{"type": "function", "name": "known"}}
	_, bridge := mustPrepare(t, source, "scope", nil)
	invalid := nativeCall(object{"name": "missing", "arguments": object{"private": "not-for-output"}})
	wire := sse(object{"type": "response.completed", "response": object{"status": "completed", "usage": object{"input_tokens": 5, "output_tokens": 1}, "output": []any{invalid}}})
	cause := errors.New("quota wait")
	calls := 0
	reader := bridge.StreamWithPreOutputRepairs(context.Background(), io.NopCloser(strings.NewReader(wire)), nil, func(context.Context) (io.ReadCloser, error) {
		calls++
		return nil, PreserveError(fmt.Errorf("blocked repair: %w", cause))
	})
	out, err := io.ReadAll(reader)
	require.ErrorIs(t, err, cause)
	require.NoError(t, reader.Close())
	require.Equal(t, 1, calls)
	require.Empty(t, out, "operational failure must not emit unvalidated tools or synthetic completion")
	require.JSONEq(t, "{\"type\":\"response.failed\",\"response\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}", string(PreservedErrorUsage(err)))
	require.Nil(t, PreservedErrorUsage(cause))
}
