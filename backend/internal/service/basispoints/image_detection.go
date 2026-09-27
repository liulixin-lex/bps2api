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
				kind := imageRelayJSONField(part, "type").Str
				reference := imageRelayJSONField(part, "image_url")
				rawURL := reference.Str
				if reference.IsObject() {
					rawURL = imageRelayJSONField(reference, "url").Str
				}
				if (kind == "input_image" || kind == "image_url") && len(rawURL) >= len("data:") && strings.EqualFold(rawURL[:len("data:")], "data:") {
					found = true
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
