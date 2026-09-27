package service

import (
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
