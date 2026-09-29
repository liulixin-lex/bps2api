package basispoints

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBPSRecoveryHardeningToolRepairDeclaredSourceField(t *testing.T) {
	for _, field := range []string{"code", "cmd", "command", "script", "input"} {
		t.Run(field, func(t *testing.T) {
			source := testSource()
			source["tools"] = []any{object{"type": "function", "name": "run_job", "parameters": object{
				"type": "object", "required": []any{field, "note"}, "additionalProperties": false,
				"properties": object{field: object{"type": "string"}, "note": object{"type": "string"}},
			}}}
			cache := new(ReplayCache)
			_, bridge := mustPrepare(t, source, "repair-scope", cache)
			originalCode := "  literal $VALUE; quoted \"中\"; \\n stays literal\r\nnext line\n"
			original := repairResponse("resp_original", 10, 2, repairCall("bad", "Run selected tool", originalCode))
			corrected := functionCodeTestNative(t, "run_job", "altered source must be replaced", "{\"note\":\"fixture\"}")
			corrected["id"], corrected["call_id"] = "fc_fixed", "fixed"
			attempts := 0
			body := bridge.StreamWithToolRepair(context.Background(), io.NopCloser(strings.NewReader(sse(object{"type": "response.completed", "response": original}))), func(context.Context, object, error) (object, error) {
				attempts++
				return repairResponse("resp_hidden", 20, 3, corrected), nil
			})
			events := repairEvents(t, body)
			require.Equal(t, 1, attempts)
			last := events[len(events)-1]
			require.Equal(t, "response.completed", last["type"])
			response := repairValue[object](t, last["response"])
			require.Equal(t, "resp_original", response["id"])
			output := repairValue[[]any](t, response["output"])
			require.Len(t, output, 1)
			call := repairValue[object](t, output[0])
			require.Equal(t, "run_job", call["name"])
			var args object
			require.NoError(t, decode([]byte(text(call["arguments"])), &args))
			require.Equal(t, originalCode, args[field])
			require.Equal(t, "fixture", args["note"])
			require.Len(t, args, 2)
			require.Equal(t, originalCode, transportArguments(cache.get("repair-scope", "fixed"))["code"])
			require.Nil(t, cache.get("repair-scope", "bad"))
		})
	}
}

func TestBPSRecoveryHardeningToolRepairStillRejectsChangedSource(t *testing.T) {
	for _, field := range []string{"code", "cmd", "command", "script", "input"} {
		t.Run(field, func(t *testing.T) {
			source := testSource()
			source["tools"] = []any{object{"type": "function", "name": "run_job", "parameters": object{"type": "object", "required": []any{field}, "properties": object{field: object{"type": "string"}}}}}
			_, bridge := mustPrepare(t, source, "scope", nil)
			original := repairCall("bad", "Run selected tool", "literal original")
			same := functionCodeTestNative(t, "run_job", "literal original", "{}")
			changed := functionCodeTestNative(t, "run_job", "literal changed", "{}")
			require.True(t, bridge.preservesToolOperations([]object{original}, []object{same}))
			require.False(t, bridge.preservesToolOperations([]object{original}, []object{changed}))
			// Valid function calls retain all metadata and their target, not just source.
			originalValid := functionCodeTestNative(t, "run_job", "literal original", "{}")
			require.False(t, bridge.preservesToolOperations([]object{originalValid}, []object{changed}))
			_, err := json.Marshal(source)
			require.NoError(t, err)
		})
	}
}
