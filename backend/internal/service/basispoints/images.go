package basispoints

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

const imageInputUnavailableMessage = "[Image input is unavailable because Excel / BPS image support is disabled. " +
	"The model cannot see this image. Do not retry view_image or other image-reading tools while image support is disabled. " +
	"Continue using the available text and explain this limitation if the task requires the image.]"

// StripInputImages replaces image parts with explicit unavailable notices before
// validation or attachment handling. Do not traverse tool arguments, schemas or
// text: image-shaped application data there is not a Responses image input.
func StripInputImages(raw []byte) ([]byte, error) {
	var source object
	if err := decode(raw, &source); err != nil || source == nil {
		return nil, fmt.Errorf("invalid Basispoints request JSON")
	}
	input, _ := source["input"].([]any)
	changed := false
	for _, rawItem := range input {
		item, _ := rawItem.(object)
		field := "content"
		switch text(item["type"]) {
		case "", "message", "agent_message":
		case "function_call_output", "custom_tool_call_output":
			field = "output"
		default:
			continue
		}
		parts, ok := item[field].([]any)
		if !ok {
			continue
		}
		for i, rawPart := range parts {
			part, _ := rawPart.(object)
			normalizeContentPart(part)
			if text(part["type"]) == "input_image" {
				// Mixed outputs also need a notice: view_image may include only
				// metadata or blank text beside the image. Silently dropping it
				// makes the result look empty or successful and invites retries.
				parts[i] = object{"type": "input_text", "text": imageInputUnavailableMessage}
				changed = true
			}
		}
	}
	if !changed {
		return raw, nil
	}
	return json.Marshal(source)
}

// Accept HTTPS URLs or validated native attachment references.
func validateImage(part object) error { return validateImageWithAttachments(part, false) }

func validateImageWithAttachments(part object, allowNative bool) error {
	if fileID, exists := part["file_id"]; exists {
		if !allowNative {
			return fmt.Errorf("basispoints file_id images require explicit native attachment mode")
		}
		id, ok := fileID.(string)
		if !ok || !ValidAttachmentID(id) {
			return fmt.Errorf("basispoints input_image requires a valid file_id")
		}
		if _, exists := part["image_url"]; exists {
			return fmt.Errorf("basispoints input_image requires exactly one image reference")
		}
	} else {
		raw, ok := part["image_url"].(string)
		if !ok || raw == "" {
			return fmt.Errorf("basispoints input_image requires an HTTPS image_url or file_id")
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "data:") {
			return fmt.Errorf("basispoints does not accept data:image input while image support is disabled; ask an administrator to enable the selected BPS account's 'Ignore image inputs when image support is disabled' option (openai_excel_bps_ignore_images), enable BPS image support, provide an HTTPS image URL, or disable Basispoints and start a new conversation")
		}
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" || strings.TrimSpace(raw) != raw {
			return fmt.Errorf("basispoints input_image requires an absolute HTTPS image URL without embedded credentials")
		}
	}
	// A successful alias normalization removes these fields. If they remain,
	// the original input was ambiguous or mixed with another payload; do not
	// silently forward only its primary URL.
	for key := range part {
		switch key {
		case "type", "image_url", "detail", "file_id":
		default:
			return fmt.Errorf("basispoints input_image contains conflicting or unsupported image fields")
		}
	}
	return validateImageDetail(part)
}

func validateImageDetail(part object) error {
	if detail, exists := part["detail"]; exists && detail != nil {
		switch text(detail) {
		case "auto", "low", "high", "original":
		default:
			return fmt.Errorf("basispoints image detail must be auto, low, high or original")
		}
	}
	return nil
}
