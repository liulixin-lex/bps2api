package basispoints

import (
	"fmt"
	"reflect"
)

func validateHistoryContent(value any, inputIndex int, field string) error {
	content, _ := value.([]any)
	for index, rawPart := range content {
		part, _ := rawPart.(object)
		normalizeContentPart(part)
		switch text(part["type"]) {
		case "input_text", "output_text", "text", "refusal":
		case "input_image":
			if err := validateImage(part); err != nil {
				return fmt.Errorf("%w (path=input[%d].%s[%d])", err, inputIndex, field, index)
			}
		default:
			return fmt.Errorf("basispoints supports text and HTTPS input_image content only (path=input[%d].%s[%d]; type=%s)", inputIndex, field, index, contentTypeDiagnostic(part))
		}
	}
	return nil
}

// Report only fixed protocol labels. A caller-controlled type can itself contain
// private data or log injection, so unrecognized values are never echoed.
func contentTypeDiagnostic(part object) string {
	if part == nil {
		return "non_object"
	}
	value, present := part["type"]
	if !present {
		return "missing"
	}
	kind, ok := value.(string)
	if !ok {
		return "non_string"
	}
	switch kind {
	case "image", "image_url", "output_image", "screenshot", "computer_screenshot", "input_file", "file", "document",
		"input_audio", "output_audio", "audio", "reasoning_text", "summary_text",
		"tool_use", "tool_result", "thinking", "redacted_thinking":
		return kind
	default:
		return "unknown"
	}
}

// Normalize only explicit equivalent shapes. Binary/file/audio content is never
// silently dropped or represented as if it had been inspected by the model.
func normalizeContentPart(part object) {
	if part == nil {
		return
	}
	switch text(part["type"]) {
	case "reasoning_text", "summary_text":
		if _, ok := part["text"].(string); ok {
			part["type"] = "input_text"
		}
	case "image_url", "input_image", "image", "output_image", "screenshot", "computer_screenshot", "provider_image":
		normalizeImageReference(part)
	}
}

func normalizeImageReference(part object) {
	raw, detail, ok := imageReference(part)
	if !ok || raw == "" {
		return
	}
	if detail != nil {
		part["detail"] = detail
	}
	part["image_url"] = raw
	part["type"] = "input_image"
	// These fields are aliases, not additional BPS wire fields.
	delete(part, "source")
	delete(part, "url")
}

// Only unambiguous image-only shapes are lowered. Do not discard a file ID,
// binary payload, text, or a second conflicting reference to make input valid.
func imageReference(part object) (raw string, detail any, ok bool) {
	for key := range part {
		switch key {
		case "type", "image_url", "source", "url", "detail":
		default:
			return "", nil, false
		}
	}
	detail, hasDetail := part["detail"]
	found := false
	for _, key := range []string{"image_url", "source", "url"} {
		value, exists := part[key]
		if !exists {
			continue
		}
		var candidate string
		switch v := value.(type) {
		case string:
			candidate = v
		case object:
			for nestedKey := range v {
				switch nestedKey {
				case "url", "detail", "image_url":
				case "type":
					if text(v["type"]) != "url" {
						return "", nil, false
					}
				default:
					return "", nil, false
				}
			}
			url, hasURL := v["url"]
			alias, hasAlias := v["image_url"]
			if hasURL {
				var valid bool
				candidate, valid = url.(string)
				if !valid {
					return "", nil, false
				}
			}
			if hasAlias {
				aliasURL, valid := alias.(string)
				if !valid || (hasURL && aliasURL != candidate) {
					return "", nil, false
				}
				candidate = aliasURL
			}
			if nestedDetail, exists := v["detail"]; exists {
				if hasDetail && !reflect.DeepEqual(detail, nestedDetail) {
					return "", nil, false
				}
				detail, hasDetail = nestedDetail, true
			}
		default:
			return "", nil, false
		}
		if candidate == "" || (found && raw != candidate) {
			return "", nil, false
		}
		raw, found = candidate, true
	}
	if detail != nil {
		if _, valid := detail.(string); !valid {
			return "", nil, false
		}
	}
	return raw, detail, found
}
