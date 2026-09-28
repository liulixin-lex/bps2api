package service

import "net/http"

// ExcelBPSModelAccessChangedReason identifies a model permission rejection.
// Another BPS account may serve the same model within the handler's existing
// switch budget. This never authorizes changing models or using a native route.
const ExcelBPSModelAccessChangedReason = GatewayFailureReason("basispoints_model_access_changed")

const excelBPSModelAccessChangedClientMessage = "This model is not available on the account's Excel BPS endpoint"

func newExcelBPSModelAccessChangedFailoverError(model, retryAfter string) *UpstreamFailoverError {
	failover := &UpstreamFailoverError{
		StatusCode:                    http.StatusForbidden,
		Stage:                         GatewayFailureStageInference,
		Scope:                         GatewayFailureScopeAccount,
		Reason:                        ExcelBPSModelAccessChangedReason,
		NextAccountAction:             NextAccountRetry,
		ClientStatusCode:              http.StatusForbidden,
		ClientMessage:                 excelBPSModelAccessChangedClientMessage,
		RequiredExcelBPSUpstreamModel: model,
	}
	if value := excelBPSRetryAfterHeader(retryAfter); value != "" {
		failover.ResponseHeaders = http.Header{"Retry-After": {value}}
	}
	return failover
}
