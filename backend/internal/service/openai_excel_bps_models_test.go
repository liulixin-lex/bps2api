package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestExcelBPSPauseKeepsConfiguredSwitch(t *testing.T) {
	a := excelAccount()
	a.Extra["openai_excel_bps_auto_disable_on_403"] = true
	require.True(t, a.IsExcelBPSEnabled())
	a.Extra[OpenAIExcelBPSPausedOn403AtExtraKey] = "2026-09-26 11:40:00+00"
	require.Equal(t, true, a.Extra["openai_excel_bps"])
	require.False(t, a.IsExcelBPSEnabled())
	require.True(t, a.IsExcelBPSEnabledForModel("gpt-6-astra"), "paused account must remain bound to BPS")
	require.False(t, a.IsExcelBPSAutoDisableOn403Enabled())
	delete(a.Extra, OpenAIExcelBPSPausedOn403AtExtraKey)
	require.True(t, a.IsExcelBPSEnabled())
}

func TestExcelBPSModelSelection(t *testing.T) {
	a := excelAccount()
	require.True(t, a.IsExcelBPSEnabledForModel("gpt-6-sol"), "legacy all-model setting")
	a.Extra["openai_excel_bps_models"] = []any{"gpt-6-astra"}
	require.True(t, a.IsExcelBPSEnabledForModel("gpt-6-astra"))
	require.False(t, a.IsExcelBPSEnabledForModel("gpt-6-sol"), "a scoped BPS account must not claim unrelated models")
	require.False(t, a.IsExcelBPSEnabledForModel("gpt-6-astra-other"), "scope matching is exact after mapping")
	a.Credentials["model_mapping"] = map[string]any{"alias": "gpt-6-astra", "gpt-6-astra": "gpt-6-sol"}
	require.True(t, a.IsExcelBPSEnabledForModel("alias"))
	require.False(t, a.IsExcelBPSEnabledForModel("gpt-6-astra"), "an explicitly mapped request follows its mapped native model")
	require.True(t, a.isExcelBPSUpstreamModelEnabled("gpt-6-astra"), "already mapped names are matched directly against the selected upstream scope")
	for _, models := range []any{[]any{}, []string{}, nil, "gpt-6-astra", []any{42, false}} {
		a.Extra["openai_excel_bps_models"] = models
		require.False(t, a.IsExcelBPSEnabledForModel("alias"), "an explicitly present invalid/empty scope must not become all-model routing")
		require.False(t, a.isExcelBPSAllModelsEnabled())
	}
	a.Extra["openai_excel_bps_models"] = []string{" gpt-6-astra "}
	require.True(t, a.IsExcelBPSEnabledForModel("alias"))
	a.Extra["openai_excel_bps"] = false
	require.False(t, a.IsExcelBPSEnabledForModel("alias"))
	var absent *Account
	require.False(t, absent.IsExcelBPSEnabledForModel("gpt-6-astra"))
}

func TestExcelBPSSelectedModelForwarding(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-6-sol"} {
		t.Run(model, func(t *testing.T) {
			wire := fmt.Sprintf("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"status\":\"completed\",\"model\":%q,\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n", model)
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}}
			svc := openAIClientToolsTestService(upstream)
			a := excelAccount()
			a.Extra["openai_excel_bps_models"] = []string{"gpt-6-astra"}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			result, err := svc.Forward(context.Background(), c, a, []byte(fmt.Sprintf(`{"model":%q,"stream":true,"input":"test"}`, model)))
			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotNil(t, upstream.lastReq)
			if model == "gpt-6-astra" {
				require.Equal(t, "bps.openai.com", upstream.lastReq.URL.Host)
				require.Equal(t, "/basispoints/api/responses", upstream.lastReq.URL.Path)
			} else {
				require.NotEqual(t, "bps.openai.com", upstream.lastReq.URL.Host, "unselected models stay on native OpenAI routing")
			}
		})
	}
}

func TestExcelBPSForwardingUsesFirstMappedModelForScopedSelection(t *testing.T) {
	wire := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_mapped\",\"status\":\"completed\",\"model\":\"gpt-6-astra\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}}
	svc := openAIClientToolsTestService(upstream)
	a := excelAccount()
	a.Extra["openai_excel_bps_models"] = []string{"gpt-6-astra"}
	a.Credentials["model_mapping"] = map[string]any{"alias": "gpt-6-astra", "gpt-6-astra": "gpt-6-sol"}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	result, err := svc.Forward(context.Background(), c, a, []byte(`{"model":"alias","stream":true,"input":"test"}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "bps.openai.com", upstream.lastReq.URL.Host)
	require.Equal(t, "/basispoints/api/responses", upstream.lastReq.URL.Path)
	require.Equal(t, "gpt-6-astra", gjson.GetBytes(upstream.lastBody, "model").String(), "BPS scope uses the first mapping result without remapping it")
}

func TestExcelBPSModelScopePreservesNativeTransportAndTickets(t *testing.T) {
	a := excelAccount()
	a.Extra["openai_excel_bps_models"] = []string{"gpt-6-astra"}
	a.Extra["openai_oauth_responses_websockets_v2_mode"] = OpenAIWSIngressModeCtxPool
	a.Extra["openai_oauth_responses_websockets_v2_enabled"] = true
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	svc := &OpenAIGatewayService{cfg: cfg}
	require.False(t, a.IsOpenAIWSForceHTTPEnabled(), "a scoped BPS account only forces HTTP for selected models")
	require.True(t, a.IsOpenAIResponsesWebSocketV2Enabled(), "scoped BPS must not disable native WS globally")
	require.Equal(t, OpenAIWSIngressModeCtxPool, a.ResolveOpenAIResponsesWebSocketV2Mode("ctx_pool"), "scoped BPS leaves native model ingress available")
	for _, transport := range []OpenAIUpstreamTransport{OpenAIUpstreamTransportResponsesWebsocketV2, OpenAIUpstreamTransportResponsesWebsocketV2Ingress} {
		require.False(t, svc.isOpenAIAccountTransportCompatible(a, transport, "gpt-6-astra"))
		require.True(t, svc.isOpenAIAccountTransportCompatible(a, transport, "gpt-6-sol"), "an unselected model may retain native WS transport")
	}
	require.True(t, svc.isOpenAIAccountTransportCompatible(a, OpenAIUpstreamTransportHTTPSSE, "gpt-6-astra"))
	require.True(t, isOpenAICodexTicketAccount(a), "a scoped account remains a ticket candidate without a selected model")
	require.False(t, isOpenAICodexTicketAccount(a, "gpt-6-astra"))
	require.True(t, isOpenAICodexTicketAccount(a, "gpt-6-sol"))
	cfg.Gateway.OpenAICodexTicket.Enabled = true
	cfg.Gateway.OpenAICodexTicket.FailClosed = true
	cfg.Gateway.OpenAICodexTicket.Models = []string{"gpt-6-astra", "gpt-6-sol"}
	require.False(t, svc.openAICodexTicketBlocksAccount(a, "gpt-6-astra"))
	require.True(t, svc.openAICodexTicketBlocksAccount(a, "gpt-6-sol"), "fail-closed ticket gating remains available to native scoped models")
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), a, "gpt-6-astra", http.Header{}))
	require.ErrorContains(t, svc.applyOpenAICodexTicket(context.Background(), a, "gpt-6-sol", http.Header{}), "model_ticket_unavailable")
	statuses := OpenAICodexTicketStatuses(a, cfg.Gateway.OpenAICodexTicket, time.Now())
	require.Len(t, statuses, 1)
	require.Equal(t, "gpt-6-sol", statuses[0].Model)
	before := openAITurnRouteFingerprint(a)
	a.Extra["openai_excel_bps_models"] = []string{"gpt-6-sol"}
	require.NotEqual(t, before, openAITurnRouteFingerprint(a))
}

func TestExcelBPSSelectedCompactKeepsExcelModel(t *testing.T) {
	a := excelAccount()
	a.Extra["openai_excel_bps_models"] = []string{"gpt-6-astra"}
	a.Credentials["model_mapping"] = map[string]any{"alias": "gpt-6-astra"}
	a.Credentials["compact_model_mapping"] = map[string]any{"alias": "gpt-6-sol"}
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	require.Equal(t, "gpt-6-astra", svc.openAICodexTicketOutboundModel(a, "alias", true))
	require.Equal(t, "gpt-6-astra", resolveOpenAIAccountUpstreamModelForRequest(a, "alias", true))
}
