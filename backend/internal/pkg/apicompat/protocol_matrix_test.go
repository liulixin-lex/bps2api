package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProtocolMatrixImageDetailConversions(t *testing.T) {
	for _, detail := range []string{"", "auto", "low", "high", "original"} {
		t.Run(detail, func(t *testing.T) {
			uri := "https://images.example/test.png?sig=a%2Fb"
			raw, _ := json.Marshal([]ChatContentPart{{Type: "image_url", ImageURL: &ChatImageURL{URL: uri, Detail: detail}}})
			req, err := ChatCompletionsToResponses(&ChatCompletionsRequest{Model: "gpt-6-astra", Messages: []ChatMessage{{Role: "user", Content: raw}}})
			require.NoError(t, err)
			var input []struct{ Content []map[string]any }
			require.NoError(t, json.Unmarshal(req.Input, &input))
			if detail != "" {
				require.Equal(t, detail, input[0].Content[0]["detail"])
			}
			require.Equal(t, uri, input[0].Content[0]["image_url"])
			for _, nested := range []bool{false, true} {
				part := map[string]any{"type": "input_image", "image_url": uri, "detail": detail}
				if nested {
					part = map[string]any{"type": "image_url", "image_url": map[string]any{"url": uri, "detail": detail}}
				}
				b, _ := json.Marshal(part)
				var fields map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(b, &fields))
				for _, single := range []bool{false, true} {
					var converted json.RawMessage
					if single {
						partType, ok := part["type"].(string)
						require.True(t, ok)
						converted, err = chatContentFromSingleResponsesPart(partType, fields)
					} else {
						converted, err = responsesContentPartsToChatContent([]json.RawMessage{b}, "user")
					}
					require.NoError(t, err)
					var content []ChatContentPart
					require.NoError(t, json.Unmarshal(converted, &content))
					require.Len(t, content, 1)
					require.Equal(t, &ChatImageURL{URL: uri, Detail: detail}, content[0].ImageURL)
				}
				_, media, changed := rewriteToolOutputMediaValue(part)
				require.True(t, changed)
				require.Len(t, media, 1)
				require.Equal(t, &ChatImageURL{URL: uri, Detail: detail}, media[0].ImageURL)
			}
		})
	}
}
func TestProtocolMatrixDeveloperRolePreserved(t *testing.T) {
	for _, content := range []json.RawMessage{json.RawMessage("\"instruction\""), json.RawMessage("[{\"type\":\"text\",\"text\":\"instruction\"}]")} {
		items, err := chatMessageToResponsesItems(ChatMessage{Role: "developer", Content: content})
		require.NoError(t, err)
		require.Len(t, items, 1)
		require.Equal(t, "developer", items[0].Role)
	}
}

func TestProtocolMatrixForcedFunctionChoice(t *testing.T) {
	for _, raw := range []string{"\"auto\"", "\"none\"", "\"required\"", "null", "{\"type\":\"function\",\"function\":{\"name\":\"office_apply\"}}", "{\"type\":\"function\",\"name\":\"office_apply\"}"} {
		req, err := ChatCompletionsToResponses(&ChatCompletionsRequest{Model: "gpt-5.6-sol", Messages: []ChatMessage{{Role: "user", Content: json.RawMessage("\"test\"")}}, ToolChoice: json.RawMessage(raw)})
		require.NoError(t, err)
		if len(raw) > 4 && raw[0] == '{' {
			require.JSONEq(t, "{\"type\":\"function\",\"name\":\"office_apply\"}", string(req.ToolChoice))
		} else {
			require.JSONEq(t, raw, string(req.ToolChoice))
		}
	}
	for _, raw := range []string{"{\"type\":\"function\",\"name\":\"wrong\",\"function\":{\"name\":\"office_apply\"}}", "{\"type\":\"function\",\"function\":{\"name\":\"office_apply\",\"discard\":true}}"} {
		_, err := ChatCompletionsToResponses(&ChatCompletionsRequest{ToolChoice: json.RawMessage(raw)})
		require.Error(t, err)
	}
}
