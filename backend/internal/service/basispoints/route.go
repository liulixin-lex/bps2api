package basispoints

import (
	"strings"

	"github.com/tidwall/gjson"
)

// NativeFallbackReason is a legacy capability diagnostic, not a routing decision.
// An enabled BPS account must never use this result to select native Codex.
// It reports capabilities that require the native Codex
// channel because Basispoints cannot execute them. Empty means the request can
// use the BPS bridge.
func NativeFallbackReason(body []byte) string {
	if !gjson.ValidBytes(body) {
		return ""
	}
	// Index the top-level fields once. Repeated GetBytes calls otherwise
	// rescan a large inline-image input for every absent routing option.
	fields := gjson.ParseBytes(body).Map()
	if reason := nativeToolCapability(fields["tools"], 0); reason != "" {
		return reason
	}
	choice := fields["tool_choice"]
	// Client selections are enforced by Prepare and response validation. Only
	// hosted or unknown selection types remain capability diagnostics.
	if unsupportedToolChoice(choice) {
		return "tool_choice"
	}
	if fields["previous_response_id"].String() != "" {
		return "previous_response_id"
	}
	if mode := fields["reasoning"].Get("mode").String(); mode != "" && mode != "standard" {
		return "reasoning_mode"
	}
	if reason := nativeRequestOption(fields); reason != "" {
		return reason
	}
	reason := ""
	input := fields["input"]
	if input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			switch item.Get("type").String() {
			case "configuration_update", "item_reference", "tool_search_call", "tool_search_output",
				"computer_call", "computer_call_output", "code_interpreter_call", "image_generation_call",
				"file_search_call", "web_search_call", "local_shell_call", "local_shell_call_output",
				"shell_call", "shell_call_output", "apply_patch_call", "apply_patch_call_output", "mcp_call", "mcp_list_tools", "mcp_approval_request", "mcp_approval_response", "mcp_tool_call_output":
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
					case "input_audio", "output_audio", "audio", "input_file", "file", "computer_screenshot":
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

func unsupportedToolChoice(choice gjson.Result) bool {
	if !choice.IsObject() {
		return false
	}
	switch choice.Get("type").String() {
	case "function", "custom", "namespace":
		return false
	case "allowed_tools":
		unsupported := false
		choice.Get("tools").ForEach(func(_, item gjson.Result) bool {
			switch item.Get("type").String() {
			case "function", "custom", "namespace":
			default:
				unsupported = true
			}
			return !unsupported
		})
		return unsupported
	default:
		return true
	}
}

// A hosted tool is a provider capability, never an executable client function.
// Route its declaration and history together rather than silently dropping it.
func nativeToolCapability(tools gjson.Result, depth int) string {
	if depth > maxToolNamespaceDepth {
		return "tool_namespace_depth"
	}
	if !tools.IsArray() {
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

// These options are preserved by the native Codex request transform, but are
// not representable by the BPS wire whitelist. Defaults do not force a bypass.
// Do not add max_output_tokens or sampling options here: the OAuth Codex
// transform removes those too, so such a fallback would not preserve them.
func nativeRequestOption(fields map[string]gjson.Result) string {
	if verbosity := fields["text"].Get("verbosity").String(); verbosity != "" && verbosity != "medium" {
		return "text_verbosity"
	}
	if summary := fields["reasoning"].Get("summary").String(); summary != "" && summary != "auto" {
		return "reasoning_summary"
	}
	if context := fields["reasoning"].Get("context").String(); context != "" && context != "auto" {
		return "reasoning_context"
	}
	effort := fields["reasoning"].Get("effort")
	if !effort.Exists() {
		effort = fields["reasoning_effort"]
	}
	if effort.Type == gjson.Number || effort.String() == "none" || effort.String() == "minimal" {
		return "reasoning_effort"
	}
	include := fields["include"]
	if include.IsArray() {
		requiresNative := false
		include.ForEach(func(_, value gjson.Result) bool {
			requiresNative = value.String() != "" && value.String() != "reasoning.encrypted_content"
			return !requiresNative
		})
		if requiresNative {
			return "include"
		}
	}
	return ""
}
