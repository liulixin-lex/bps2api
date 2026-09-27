package basispoints

import (
	"strings"

	"github.com/tidwall/gjson"
)

// NativeFallbackReason reports capabilities that must stay on the native Codex
// channel because Basispoints cannot execute them. Empty means the request can
// use the BPS bridge.
func NativeFallbackReason(body []byte) string {
	if !gjson.ValidBytes(body) {
		return ""
	}
	if reason := nativeToolCapability(gjson.GetBytes(body, "tools"), 0); reason != "" {
		return reason
	}
	choice := gjson.GetBytes(body, "tool_choice")
	// The bridge only implements auto/none. Preserve forced-tool semantics on
	// the native channel instead of rejecting or silently weakening them.
	if choice.String() == "required" || choice.IsObject() {
		return "tool_choice"
	}
	if gjson.GetBytes(body, "previous_response_id").String() != "" {
		return "previous_response_id"
	}
	if mode := gjson.GetBytes(body, "reasoning.mode").String(); mode != "" && mode != "standard" {
		return "reasoning_mode"
	}
	reason := ""
	input := gjson.GetBytes(body, "input")
	if input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			switch item.Get("type").String() {
			case "configuration_update", "item_reference", "tool_search_call", "tool_search_output",
				"computer_call", "computer_call_output", "code_interpreter_call", "image_generation_call",
				"file_search_call", "web_search_call", "mcp_call", "mcp_list_tools", "mcp_approval_request", "mcp_approval_response", "mcp_tool_call_output":
				reason = "native_history"
				return false
			case "additional_tools":
				reason = nativeToolCapability(item.Get("tools"), 0)
				if reason != "" {
					return false
				}
			}
			for _, field := range []string{"content", "output"} {
				parts := item.Get(field)
				if !parts.IsArray() {
					continue
				}
				parts.ForEach(func(_, part gjson.Result) bool {
					switch part.Get("type").String() {
					case "input_audio", "audio", "input_file", "file", "computer_screenshot":
						reason = "native_media"
					case "input_image", "image_url":
						if part.Get("file_id").String() != "" {
							reason = "image_file_id"
						}
					}
					return reason == ""
				})
				if reason != "" {
					return false
				}
			}
			return true
		})
	}
	if reason != "" {
		return reason
	}
	// Inline data images are handled by Sub2API's local relay before Prepare;
	// leave them on BPS so the relay can rewrite them to signed HTTPS URLs.
	return ""
}

// A hosted tool is a provider capability, never an executable client function.
// Route its declaration and history together rather than silently dropping it.
func nativeToolCapability(tools gjson.Result, depth int) string {
	if !tools.IsArray() || depth > 16 {
		return ""
	}
	reason := ""
	tools.ForEach(func(_, tool gjson.Result) bool {
		kind := strings.ToLower(strings.TrimSpace(tool.Get("type").String()))
		if tool.Get("async").Bool() {
			reason = "async_tool"
		} else if kind == "local_shell" || kind == "shell" || kind == "apply_patch" {
			reason = kind
		} else if kind == "namespace" {
			reason = nativeToolCapability(tool.Get("tools"), depth+1)
		} else if isUnsupportedHostedTool(kind) {
			reason = kind
		}
		return reason == ""
	})
	return reason
}
