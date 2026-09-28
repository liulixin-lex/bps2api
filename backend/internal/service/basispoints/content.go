package basispoints

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// ContentValidationError identifies a rejected history part using only safe
// protocol metadata, never the part's payload or caller-controlled type.
type ContentValidationError struct {
	Path        string
	ContentType string
	Shape       string
	message     string
}

func (e *ContentValidationError) Error() string {
	if e.Shape != "" {
		return fmt.Sprintf("%s (path=%s; type=%s; shape=%s)", e.message, e.Path, e.ContentType, e.Shape)
	}
	return fmt.Sprintf("%s (path=%s; type=%s)", e.message, e.Path, e.ContentType)
}

func (b *Bridge) validateHistoryContent(value any, inputIndex int, field string) error {
	content, _ := value.([]any)
	for index, rawPart := range content {
		part, _ := rawPart.(object)
		normalizeContentPart(part)
		path := fmt.Sprintf("input[%d].%s[%d]", inputIndex, field, index)
		switch text(part["type"]) {
		case "input_text", "output_text", "text", "refusal":
		case "input_image":
			var err error
			if field == "output" && b.nativeToolImages[text(part["image_url"])] {
				// Only this request's fully validated tool screenshots may remain inline.
				if _, exists := part["file_id"]; exists {
					err = fmt.Errorf("basispoints input_image requires exactly one image reference")
				} else {
					err = validateImageDetail(part)
				}
			} else {
				err = validateImageWithAttachments(part, b.options.NativeAttachments)
			}
			if err != nil {
				return &ContentValidationError{Path: path, ContentType: "input_image", message: err.Error()}
			}
		case "encrypted_content":
			return &ContentValidationError{Path: path, ContentType: "encrypted_content", message: "basispoints cannot forward encrypted_content message parts; refresh the model catalog and start a new conversation without a multi-agent v2 override, or resend the original plaintext"}
		default:
			return &ContentValidationError{Path: path, ContentType: contentTypeDiagnostic(part), Shape: contentShapeDiagnostic(rawPart), message: "basispoints supports text and HTTPS input_image content only"}
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
	case "image", "image_url", "output_image", "screenshot", "computer_screenshot", "provider_image", "input_file", "file", "document",
		"input_audio", "output_audio", "audio", "reasoning_text", "summary_text",
		"tool_use", "tool_result", "thinking", "redacted_thinking":
		return kind
	default:
		return "unknown"
	}
}

// Unknown content cannot safely be lowered without knowing its protocol. Expose
// a bounded, shallow signature to diagnose it without printing caller-supplied
// types, unknown field names, URLs, text, or ciphertext. Fixed field order also
// makes repeated failures comparable across long histories and request logs.
func contentShapeDiagnostic(value any) string {
	part, ok := value.(object)
	if !ok {
		return contentJSONKind(value)
	}
	fields := make([]string, 0, 18)
	known := 0
	for _, key := range []string{"type", "text", "content", "encrypted_content", "image_url", "url", "source", "file_id", "detail", "data", "payload", "encoding", "mime_type", "annotations", "refusal", "thinking", "signature"} {
		if field, exists := part[key]; exists {
			fields = append(fields, key+":"+contentJSONKind(field))
			known++
		}
	}
	if other := len(part) - known; other > 0 {
		fields = append(fields, fmt.Sprintf("other_fields:%d", other))
	}
	return "object{" + strings.Join(fields, ",") + "}"
}

func contentJSONKind(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "number"
	case []any:
		return "array"
	case object:
		return "object"
	default:
		return "other"
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
