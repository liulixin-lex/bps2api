package service

import "net/http"

// Keep transport failures distinguishable from invalid client input. Error codes
// remain gateway-owned; upstream messages and arbitrary fields are not exposed.
func excelBPSErrorType(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return "authentication_error"
	case status == http.StatusForbidden:
		return "permission_error"
	case status == http.StatusNotFound:
		return "not_found_error"
	case status == http.StatusTooManyRequests:
		return "rate_limit_error"
	case status == http.StatusServiceUnavailable:
		return "service_unavailable_error"
	case status >= http.StatusInternalServerError:
		return "server_error"
	default:
		return "invalid_request_error"
	}
}
