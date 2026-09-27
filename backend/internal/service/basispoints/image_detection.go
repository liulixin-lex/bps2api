package basispoints

import (
	"strings"

	"github.com/tidwall/gjson"
)

// RequestHasInlineImages mirrors Rewrite's traversal without allocating a full
// object tree. Invalid JSON is charged conservatively. Keys are case sensitive,
// escaped strings are decoded, and the last duplicate key wins, as in Rewrite.
func RequestHasInlineImages(raw []byte) bool {
	if !gjson.ValidBytes(raw) {
		return true
	}
	input := imageRelayJSONField(gjson.ParseBytes(raw), "input")
	if !input.IsArray() {
		return false
	}
	found := false
	input.ForEach(func(_, item gjson.Result) bool {
		fields := []gjson.Result{imageRelayJSONField(item, "content")}
		kind := imageRelayJSONField(item, "type").Str
		if kind == "function_call_output" || kind == "custom_tool_call_output" {
			fields = append(fields, imageRelayJSONField(item, "output"))
		}
		for _, field := range fields {
			if !field.IsArray() {
				continue
			}
			field.ForEach(func(_, part gjson.Result) bool {
				switch imageRelayJSONField(part, "type").Str {
				case "input_image", "image_url", "image", "output_image", "screenshot", "computer_screenshot", "provider_image":
					// Charge all recognized reference aliases conservatively.
					// Rewrite performs full ambiguity/shape validation later.
					for _, name := range []string{"image_url", "source", "url"} {
						reference := imageRelayJSONField(part, name)
						if imageRelayInlineReference(reference) {
							found = true
							break
						}
					}
				}
				return !found
			})
			if found {
				return false
			}
		}
		return true
	})
	return found
}

func imageRelayJSONField(value gjson.Result, name string) gjson.Result {
	var result gjson.Result
	if value.IsObject() {
		value.ForEach(func(key, field gjson.Result) bool {
			if key.Str == name {
				result = field
			}
			return true
		})
	}
	return result
}

func imageRelayInlineReference(reference gjson.Result) bool {
	if reference.Type == gjson.String {
		raw := reference.Str
		return len(raw) >= len("data:") && strings.EqualFold(raw[:len("data:")], "data:")
	}
	if reference.IsObject() {
		for _, name := range []string{"url", "image_url"} {
			raw := imageRelayJSONField(reference, name)
			if raw.Type == gjson.String && len(raw.Str) >= len("data:") && strings.EqualFold(raw.Str[:len("data:")], "data:") {
				return true
			}
		}
	}
	return false
}
