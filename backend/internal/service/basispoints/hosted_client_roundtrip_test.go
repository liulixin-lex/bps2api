package basispoints

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A realistic client advertises optional server tools alongside its executable
// function/custom catalog on every turn. Their presence cannot disable tools.
func TestHostedDeclarationsPreserveClientExecutionAndRoundTrip(t *testing.T) {
	for _, name := range []string{"read", "patch", "client.read", "client.patch"} {
		kind := "function"
		if strings.HasSuffix(name, "patch") {
			kind = "custom"
		}
		exact := object{"type": kind, "name": name}
		for _, choice := range []any{"auto", "required", exact, object{"type": "allowed_tools", "mode": "required", "tools": []any{exact}}, object{"type": "namespace", "name": "client"}} {
			if selected, ok := choice.(object); ok && selected["type"] == "namespace" && !strings.HasPrefix(name, "client.") {
				continue
			}
			for _, legacy := range []bool{false, true} {
				t.Run(name+"/"+text(choice)+"/"+map[bool]string{false: "default", true: "legacy"}[legacy], func(t *testing.T) {
					source := choiceSource(choice)
					source["tools"] = append(source["tools"].([]any), object{"type": "web_search", "external_web_access": true}, object{"type": "image_generation"}, object{"type": "tool_search"})
					source["input"] = []any{message("user", "execute the declared client tool"), object{"type": "additional_tools", "tools": []any{object{"type": "namespace", "name": "optional", "tools": []any{object{"type": "file_search"}}}}}}
					raw, err := json.Marshal(source)
					require.NoError(t, err)
					replay, catalog := new(ReplayCache), new(CatalogCache)
					wire, b, err := PrepareWithCatalog(raw, "thread", replay, catalog, PrepareOptions{OmitUnsupportedTools: legacy})
					require.NoError(t, err)
					require.Contains(t, string(wire), "Hosted tools unavailable through Basispoints")
					require.Contains(t, string(wire), "Do not claim to have used them")
					response := choiceResponse(name)
					require.NoError(t, b.translateResponse(response))
					call := response["output"].([]any)[0].(object)
					require.Equal(t, strings.TrimPrefix(name, "client."), call["name"])
					if strings.HasPrefix(name, "client.") {
						require.Equal(t, "client", call["namespace"])
					}
					if strings.HasSuffix(name, "patch") {
						require.Equal(t, "*** Begin Patch\n*** End Patch", call["input"])
					}
					outputKind := strings.Replace(text(call["type"]), "_call", "_call_output", 1)
					source["input"] = []any{message("user", "continue"), call, object{"type": outputKind, "call_id": call["call_id"], "output": "client execution result"}}
					// The next turn omits tools. The catalog must survive hosted declarations.
					delete(source, "tools")
					source["tool_choice"] = "auto"
					raw, err = json.Marshal(source)
					require.NoError(t, err)
					next, nextBridge, err := PrepareWithCatalog(raw, "thread", replay, catalog)
					require.NoError(t, err)
					require.Contains(t, string(next), "client execution result")
					require.NotEmpty(t, nextBridge.tools, "executable client catalog must survive into the next turn")
					require.NoError(t, nextBridge.translateResponse(choiceResponse(name)))
					// A full client call/result can also be replayed after a process restart.
					source["tools"] = choiceSource("auto")["tools"]
					raw, err = json.Marshal(source)
					require.NoError(t, err)
					_, _, err = Prepare(raw, "new-process", nil)
					require.NoError(t, err)
				})
			}
		}
	}
}
