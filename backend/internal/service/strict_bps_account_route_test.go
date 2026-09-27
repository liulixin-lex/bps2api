package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestStrictBPSAccountConfiguredScopeAndPause(t *testing.T) {
	for _, legacyScope := range []any{nil, []string{}, []string{"gpt-6-astra"}, "invalid-legacy-scope"} {
		for _, paused := range []bool{false, true} {
			account := excelAccount()
			account.Extra["openai_excel_bps_models"] = legacyScope
			if paused {
				account.Extra[OpenAIExcelBPSPausedOn403AtExtraKey] = "2026-09-27T00:00:00Z"
			}
			require.True(t, account.IsExcelBPSConfigured())
			require.Equal(t, !paused, account.IsExcelBPSEnabled())
			require.True(t, account.IsExcelBPSEnabledForModel("gpt-5.6-sol"))
			require.True(t, account.IsOpenAIWSForceHTTPEnabled())
			require.False(t, account.IsOpenAIResponsesWebSocketV2Enabled())
			require.Equal(t, OpenAIWSIngressModeOff, account.ResolveOpenAIResponsesWebSocketV2Mode(OpenAIWSIngressModeHTTPBridge))
			service := &OpenAIGatewayService{cfg: &config.Config{}}
			binding := &openAIWSTurnBinding{model: "gpt-5.6-sol", fingerprint: openAITurnRouteFingerprint(account), createdAt: time.Now()}
			require.ErrorContains(t, service.checkOpenAIWSBinding(account, "gpt-5.6-sol", binding), "excel_bps_requires_http")
			account.Extra["openai_excel_bps"] = false
			require.False(t, account.IsExcelBPSConfigured())
			require.False(t, account.IsExcelBPSEnabledForModel("gpt-5.6-sol"))
		}
	}
}

func TestStrictBPSAccountPausedAdminTestCannotUseNative(t *testing.T) {
	account := excelAccount()
	account.Extra[OpenAIExcelBPSPausedOn403AtExtraKey] = "2026-09-27T00:00:00Z"
	upstream := &httpUpstreamRecorder{}
	gateway := openAIClientToolsTestService(upstream)
	service := &AccountTestService{openaiGatewayService: gateway}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/300/test", nil).WithContext(context.Background())
	err := service.testOpenAIAccountConnection(ctx, account, "gpt-5.6-sol", "test", "")
	require.ErrorContains(t, err, "basispoints_routing_paused")
	require.Nil(t, upstream.lastReq)
}

func TestStrictBPSAccountAdminTestMissingGatewayDoesNotUseNative(t *testing.T) {
	service := &AccountTestService{}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/300/test", nil)
	err := service.testOpenAIAccountConnection(ctx, excelAccount(), "gpt-5.6-sol", "test", "")
	require.ErrorContains(t, err, "Excel BPS gateway is unavailable")
}

func TestStrictBPSAccountPauseIsUnschedulableUntilResumeOrDisable(t *testing.T) {
	account := excelAccount()
	account.Status, account.Schedulable = StatusActive, true
	require.True(t, account.IsSchedulable())
	account.Extra[OpenAIExcelBPSPausedOn403AtExtraKey] = "2026-09-27T00:00:00Z"
	require.False(t, account.IsSchedulable())
	require.True(t, account.IsExcelBPSEnabledForModel("gpt-5.6-sol"), "a paused account must never be rerouted to native")
	delete(account.Extra, OpenAIExcelBPSPausedOn403AtExtraKey)
	require.True(t, account.IsSchedulable(), "explicit resume restores BPS scheduling")
	account.Extra[OpenAIExcelBPSPausedOn403AtExtraKey] = "2026-09-27T00:00:00Z"
	account.Extra["openai_excel_bps"] = false
	require.True(t, account.IsSchedulable(), "explicit disable permits native scheduling")
}

func TestStrictBPSConfiguredOnlyForOpenAIOAuth(t *testing.T) {
	for _, platform := range []string{
		PlatformAnthropic, PlatformGemini, PlatformGrok, PlatformKimi,
		PlatformZhipu, PlatformDeepseek, PlatformMiniMax, PlatformOpenCodeGo,
		PlatformComposite,
	} {
		account := &Account{Platform: platform, Type: AccountTypeOAuth, Extra: map[string]any{"openai_excel_bps": true}}
		require.False(t, account.IsExcelBPSConfigured(), platform)
		require.False(t, account.IsExcelBPSEnabledForModel("gpt-6-astra"), platform)
	}
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeSetupToken, AccountTypeUpstream, AccountTypeBedrock, AccountTypeServiceAccount} {
		account := &Account{Platform: PlatformOpenAI, Type: accountType, Extra: map[string]any{"openai_excel_bps": true}}
		require.False(t, account.IsExcelBPSConfigured(), accountType)
		require.False(t, account.IsExcelBPSEnabledForModel("gpt-6-astra"), accountType)
	}
	account := excelAccount()
	account.Credentials["model_mapping"] = map[string]any{"public-alias": "gpt-6-astra"}
	require.True(t, account.IsExcelBPSConfigured())
	require.True(t, account.IsExcelBPSEnabledForModel("public-alias"), "alias text must not determine provider routing")
}
