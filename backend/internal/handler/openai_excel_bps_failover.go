package handler

import "github.com/Wei-Shaw/sub2api/internal/service"

// Once a request has reached BPS, a BPS rejection must not turn its retry into
// native Codex/API-key traffic. Ineligible selections are released and excluded
// without spending the upstream attempt budget.
func skipNativeAfterExcelBPSFailure(selection *service.AccountSelectionResult, failure *service.UpstreamFailoverError, excluded map[int64]struct{}, upstreamModel string) bool {
	if failure == nil || (failure.Reason != service.ExcelBPSRateLimitedReason && failure.Reason != service.ExcelBPSModelAccessChangedReason) || selection == nil || selection.Account == nil {
		return false
	}
	if selection.Account.IsExcelBPSConfigured() && (failure.RequiredExcelBPSUpstreamModel == "" || upstreamModel == failure.RequiredExcelBPSUpstreamModel) {
		return false
	}
	excluded[selection.Account.ID] = struct{}{}
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
		selection.ReleaseFunc = nil
	}
	return true
}
