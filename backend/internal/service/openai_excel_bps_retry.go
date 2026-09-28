package service

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Preserve a provider's valid backoff on a terminal rejection, so clients do
// not immediately hammer an overloaded endpoint after our retry budget ends.
func excelBPSRetryAfterHeader(value string) string {
	value = strings.TrimSpace(value)
	if _, err := strconv.ParseUint(value, 10, 32); err == nil {
		return value
	}
	if _, err := http.ParseTime(value); err == nil {
		return value
	}
	return ""
}

// A shared single-attempt budget in forwardExcelBPSAttempt also bounds protocol
// regeneration. Long/malformed Retry-After values are not shortened or ignored.
func excelBPSHTTPRetryDelay(status int, retryAfter string, now time.Time) (time.Duration, bool) {
	if status != http.StatusBadGateway && status != http.StatusServiceUnavailable && status != http.StatusGatewayTimeout {
		return 0, false
	}
	if retryAfter = strings.TrimSpace(retryAfter); retryAfter == "" {
		return 250 * time.Millisecond, true
	}
	if seconds, err := strconv.Atoi(retryAfter); err == nil {
		if seconds < 0 || seconds > 2 {
			return 0, false
		}
		delay := time.Duration(seconds) * time.Second
		if delay < 250*time.Millisecond {
			delay = 250 * time.Millisecond
		}
		return delay, true
	}
	if deadline, err := http.ParseTime(retryAfter); err == nil {
		delay := deadline.Sub(now)
		if delay <= 2*time.Second {
			if delay < 250*time.Millisecond {
				delay = 250 * time.Millisecond
			}
			return delay, true
		}
	}
	return 0, false
}

// These errors arise in local transport translation before client dispatch.
// Never regenerate provider refusals, structured answers or arbitrary failures.
func excelBPSCorrectableProtocolError(message string) bool {
	if strings.HasPrefix(message, "basispoints tool transport correction failed: basispoints source regeneration required:") {
		return true
	}
	for _, prefix := range []string{
		"basispoints function code transport requires an exact catalog function",
		"basispoints function code transport requires string code",
		"basispoints function code transport extended_summary must contain",
		"basispoints raw transport requires a declared custom tool",
		"basispoints returned a tool outside the client's catalog",
		"basispoints custom tool input must be a string",
		"basispoints direct custom tool input must be a string",
		"basispoints direct custom function wrapper requires one string input",
		"basispoints returned an unsupported native tool; no tool was executed",
	} {
		if strings.HasPrefix(message, prefix) {
			return true
		}
	}
	return strings.HasPrefix(message, "basispoints tool transport code must contain one JSON client-tool envelope;") &&
		(strings.Contains(message, "format=json_object;") || strings.Contains(message, "format=json_string;") || strings.Contains(message, "format=text_or_code;") || strings.Contains(message, "format=markdown;")) &&
		strings.Contains(message, "json_failure=") && !strings.Contains(message, "json_failure=trailing_data")
}

// A broken JSON-shaped wrapper has no operation to preserve. Let the existing
// one-shot pre-output request regeneration handle it before spending the shared
// budget on a continuation that would have to invent an operation.
func excelBPSSourceRegenerationRequired(response map[string]any) bool {
	output, _ := response["output"].([]any)
	for _, raw := range output {
		item, _ := raw.(map[string]any)
		if item["name"] != "run_officejs" && item["name"] != "functions.run_officejs" {
			continue
		}
		var arguments map[string]any
		switch value := item["arguments"].(type) {
		case string:
			if json.Unmarshal([]byte(value), &arguments) != nil {
				continue
			}
		case map[string]any:
			arguments = value
		}
		code, _ := arguments["code"].(string)
		code = strings.TrimSpace(code)
		if strings.HasPrefix(code, "{") && !json.Valid([]byte(code)) {
			return true
		}
	}
	return false
}
