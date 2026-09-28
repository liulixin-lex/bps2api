//go:build unit

package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// The client history carries a completed tool round trip; a switched account
// must receive exactly the same converted history.
const excelBPSFailoverRequestBody = `{"model":"gpt-6-astra","stream":%t,"input":[` +
	`{"type":"message","role":"user","content":[{"type":"input_text","text":"list the files"}]},` +
	`{"type":"function_call","call_id":"call_ls","name":"shell","arguments":"{\"cmd\":\"ls\"}"},` +
	`{"type":"function_call_output","call_id":"call_ls","output":"a.txt"}],` +
	`"tools":[{"type":"function","name":"shell","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}]}`

// excelBPSFailoverUpstream answers in call order and records the account of
// every BPS attempt.
type excelBPSFailoverUpstream struct {
	service.HTTPUpstream
	mu         sync.Mutex
	accountIDs []int64
	bodies     [][]byte
	urls       []string
	answer     func(call int) *http.Response
	onDo       func(call int)
}

func (u *excelBPSFailoverUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	u.mu.Lock()
	call := len(u.accountIDs)
	u.accountIDs = append(u.accountIDs, accountID)
	u.bodies = append(u.bodies, body)
	u.urls = append(u.urls, req.URL.String())
	u.mu.Unlock()
	if u.onDo != nil {
		u.onDo(call)
	}
	return u.answer(call), nil
}

func (u *excelBPSFailoverUpstream) calls() []int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.accountIDs...)
}

func excelBPS429(retryAfter string) *http.Response {
	header := http.Header{"Content-Type": {"application/json"}}
	if retryAfter != "" {
		header.Set("Retry-After", retryAfter)
	}
	return &http.Response{StatusCode: http.StatusTooManyRequests, Header: header,
		Body: io.NopCloser(strings.NewReader(`{"error":{"code":"basispoints_rate_limited","message":"PRIVATE_UPSTREAM workspace detail"}}`))}
}

func excelBPSCompleted() *http.Response {
	return excelBPSCompletedModel("gpt-6-astra")
}

func excelBPSCompletedModel(model string) *http.Response {
	wire := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_bps_failover\",\"status\":\"completed\",\"model\":" + fmt.Sprintf("%q", model) + "," +
		"\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n"
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}
}

func excelBPSModelAccessChanged() *http.Response {
	return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"error":{"code":"basispoints_model_access_changed","message":"PRIVATE_UPSTREAM"}}`))}
}

// excelBPSFailoverAccountRepo also answers the no-account diagnosis query.
type excelBPSFailoverAccountRepo struct {
	openAIImagesFailoverAccountRepo
}

func (r excelBPSFailoverAccountRepo) ListModelAvailabilityCandidates(_ context.Context, _ *int64, platforms []string, _ bool) ([]service.Account, error) {
	var out []service.Account
	for _, platform := range platforms {
		out = append(out, r.accountsForPlatform(platform)...)
	}
	return out, nil
}

// loadBatch selects the production default (load-aware) scheduling path;
// otherwise the simple priority path is used.
func newExcelBPSFailoverTestHandler(t *testing.T, upstream service.HTTPUpstream, loadBatch bool, candidates ...service.Account) *OpenAIGatewayHandler {
	t.Helper()
	var accounts []service.Account
	for _, id := range []int64{1, 2} {
		accounts = append(accounts, service.Account{
			ID: id, Name: fmt.Sprintf("bps-account-%d", id), Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
			Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{3131},
			Credentials: map[string]any{"access_token": fmt.Sprintf("token-%d", id), "chatgpt_account_id": fmt.Sprintf("chatgpt-%d", id)},
			Extra:       map[string]any{"openai_excel_bps": true},
		})
	}
	if len(candidates) > 0 {
		accounts = candidates
	}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	concurrencyService := service.NewConcurrencyService(nil)
	var schedulingConcurrency *service.ConcurrencyService
	if loadBatch {
		cfg.Gateway.Scheduling.LoadBatchEnabled = true
		schedulingConcurrency = concurrencyService
	}
	gatewayService := service.NewOpenAIGatewayService(
		excelBPSFailoverAccountRepo{openAIImagesFailoverAccountRepo{accounts: accounts}},
		nil, nil, nil, nil, nil, nil, nil,
		cfg,
		nil, schedulingConcurrency, nil, nil, nil,
		upstream,
		nil, nil, nil, nil, nil, nil, nil, nil,
	)
	billingService := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingService.Stop)
	handler := NewOpenAIGatewayHandler(
		gatewayService,
		concurrencyService,
		billingService,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil, nil, nil, nil,
		cfg,
	)
	handler.maxAccountSwitches = 10
	return handler
}

func newExcelBPSFailoverTestContext(ctx context.Context, stream bool) (*gin.Context, *httptest.ResponseRecorder) {
	groupID := int64(3131)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(fmt.Sprintf(excelBPSFailoverRequestBody, stream))))
	req = req.WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      99,
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, Platform: service.PlatformOpenAI},
		User:    &service.User{ID: 100},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 100, Concurrency: 0})
	return c, rec
}

// Issue #146: A -> B within the first request, then B directly while A cools.
func TestOpenAIResponsesExcelBPS429SwitchesAccountAndCoolsIt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		loadBatch, stream bool
	}{{false, false}, {false, true}, {true, false}, {true, true}} {
		stream := tc.stream
		t.Run(fmt.Sprintf("load_batch=%t/stream=%t", tc.loadBatch, stream), func(t *testing.T) {
			upstream := &excelBPSFailoverUpstream{answer: func(call int) *http.Response {
				if call == 0 {
					return excelBPS429("30")
				}
				return excelBPSCompleted()
			}}
			handler := newExcelBPSFailoverTestHandler(t, upstream, tc.loadBatch)

			c, rec := newExcelBPSFailoverTestContext(context.Background(), stream)
			handler.Responses(c)

			calls := upstream.calls()
			require.Len(t, calls, 2)
			throttled, healthy := calls[0], calls[1]
			require.NotEqual(t, throttled, healthy, "the throttled account must not be retried in this request")
			require.Equal(t, http.StatusOK, rec.Code)
			require.Contains(t, rec.Body.String(), "done")
			require.NotContains(t, rec.Body.String(), "PRIVATE_UPSTREAM")
			require.NotContains(t, rec.Body.String(), "basispoints_rate_limited")
			for _, url := range upstream.urls {
				require.Equal(t, basispoints.ResponsesURL, url)
			}
			require.NotEmpty(t, gjson.GetBytes(upstream.bodies[0], "input").Raw)
			require.Equal(t, gjson.GetBytes(upstream.bodies[0], "input").Raw, gjson.GetBytes(upstream.bodies[1], "input").Raw,
				"messages, tool calls and tool outputs must survive the account switch unchanged")
			rawEvents, ok := c.Get(service.OpsUpstreamErrorsKey)
			require.True(t, ok)
			events, ok := rawEvents.([]*service.OpsUpstreamErrorEvent)
			require.True(t, ok)
			require.Len(t, events, 1)
			require.Equal(t, "failover", events[0].Kind)
			require.Equal(t, throttled, events[0].AccountID)
			require.Equal(t, http.StatusTooManyRequests, events[0].UpstreamStatusCode)

			c, rec = newExcelBPSFailoverTestContext(context.Background(), stream)
			handler.Responses(c)

			require.Equal(t, []int64{throttled, healthy, healthy}, upstream.calls(), "the cooling account is skipped by the next request")
			require.Equal(t, http.StatusOK, rec.Code)
		})
	}
}

func TestOpenAIResponsesExcelBPS429OnEveryAccountReturnsRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, retryAfter, wantRetryAfter string
		stream, loadBatch                bool
	}{
		{name: "json", retryAfter: "30", wantRetryAfter: "30"},
		{name: "stream request", retryAfter: "30", wantRetryAfter: "30", stream: true},
		{name: "load-aware scheduling", retryAfter: "30", wantRetryAfter: "30", loadBatch: true},
		{name: "invalid retry after", retryAfter: "soon"},
		{name: "retry after beyond a week", retryAfter: "999999"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &excelBPSFailoverUpstream{answer: func(int) *http.Response { return excelBPS429(tc.retryAfter) }}
			handler := newExcelBPSFailoverTestHandler(t, upstream, tc.loadBatch)

			c, rec := newExcelBPSFailoverTestContext(context.Background(), tc.stream)
			handler.Responses(c)

			calls := upstream.calls()
			require.Len(t, calls, 2)
			require.NotEqual(t, calls[0], calls[1])
			require.Equal(t, http.StatusTooManyRequests, rec.Code)
			require.Equal(t, "rate_limit_error", gjson.Get(rec.Body.String(), "error.type").String())
			require.Equal(t, "basispoints_rate_limited", gjson.Get(rec.Body.String(), "error.code").String())
			require.NotContains(t, rec.Body.String(), "PRIVATE_UPSTREAM")
			require.NotContains(t, rec.Body.String(), "workspace")
			require.Equal(t, tc.wantRetryAfter, rec.Header().Get("Retry-After"))

			// Both accounts cool down: the next request is answered without an upstream call.
			c, rec = newExcelBPSFailoverTestContext(context.Background(), tc.stream)
			handler.Responses(c)

			require.Len(t, upstream.calls(), 2)
			require.Equal(t, http.StatusTooManyRequests, rec.Code)
			require.Equal(t, "rate_limit_error", gjson.Get(rec.Body.String(), "error.type").String())
			require.Contains(t, rec.Body.String(), "rate-limited")
		})
	}
}

func TestOpenAIResponsesExcelBPS429StopsWhenClientCancels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	upstream := &excelBPSFailoverUpstream{
		answer: func(int) *http.Response { return excelBPS429("30") },
		onDo:   func(int) { cancel() },
	}
	handler := newExcelBPSFailoverTestHandler(t, upstream, false)
	c, rec := newExcelBPSFailoverTestContext(ctx, false)

	handler.Responses(c)

	require.Len(t, upstream.calls(), 1, "a gone client must not be replayed on another account")
	require.Equal(t, statusClientClosedRequest, c.Writer.Status())
	require.Zero(t, rec.Body.Len())
}

func excelBPSMixedPoolAccounts(healthyBPS bool) []service.Account {
	accounts := []service.Account{
		{ID: 1, Name: "throttled-bps", Type: service.AccountTypeOAuth, Extra: map[string]any{"openai_excel_bps": true}},
		{ID: 2, Name: "native-oauth", Type: service.AccountTypeOAuth},
		{ID: 3, Name: "native-apikey", Type: service.AccountTypeAPIKey},
	}
	if healthyBPS {
		accounts = append(accounts, service.Account{ID: 4, Name: "healthy-bps", Type: service.AccountTypeOAuth, Extra: map[string]any{"openai_excel_bps": true}})
	}
	for i := range accounts {
		accounts[i].Platform = service.PlatformOpenAI
		accounts[i].Status = service.StatusActive
		accounts[i].Schedulable = true
		accounts[i].Priority = i
		accounts[i].GroupIDs = []int64{3131}
		accounts[i].Credentials = map[string]any{"access_token": fmt.Sprintf("token-%d", i), "chatgpt_account_id": fmt.Sprintf("chatgpt-%d", i), "api_key": "test-key"}
	}
	return accounts
}

func newExcelBPSProtocolContext(ctx context.Context, endpoint string, stream bool) (*gin.Context, *httptest.ResponseRecorder) {
	return newExcelBPSProtocolModelContext(ctx, endpoint, "gpt-6-astra", stream)
}

func newExcelBPSProtocolModelContext(ctx context.Context, endpoint, model string, stream bool) (*gin.Context, *httptest.ResponseRecorder) {
	c, rec := newExcelBPSFailoverTestContext(ctx, stream)
	body := fmt.Sprintf(`{"model":%q,"stream":%t,"messages":[{"role":"user","content":"hello"}],"max_tokens":100}`, model, stream)
	if endpoint == "/v1/responses" {
		body = fmt.Sprintf(`{"model":%q,"stream":%t,"input":"hello"}`, model, stream)
	}
	c.Request = httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(body)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	key, _ := middleware2.GetAPIKeyFromContext(c)
	key.Group.AllowMessagesDispatch = true
	return c, rec
}

func runExcelBPSProtocol(h *OpenAIGatewayHandler, c *gin.Context, endpoint string) {
	switch endpoint {
	case "/v1/responses":
		h.Responses(c)
	case "/v1/chat/completions":
		h.ChatCompletions(c)
	case "/v1/messages":
		h.Messages(c)
	}
}

func TestExcelBPS429MixedPoolNeverUsesNative(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"} {
		for _, stream := range []bool{false, true} {
			for _, healthyBPS := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%t/healthy_bps=%t", endpoint, stream, healthyBPS), func(t *testing.T) {
					upstream := &excelBPSFailoverUpstream{answer: func(call int) *http.Response {
						if call == 0 {
							return excelBPS429("30")
						}
						return excelBPSCompleted()
					}}
					handler := newExcelBPSFailoverTestHandler(t, upstream, false, excelBPSMixedPoolAccounts(healthyBPS)...)
					// Skipping ineligible native accounts must not consume the one permitted BPS switch.
					handler.maxAccountSwitches = 1
					c, rec := newExcelBPSProtocolContext(context.Background(), endpoint, stream)
					runExcelBPSProtocol(handler, c, endpoint)
					for _, id := range upstream.calls() {
						require.NotContains(t, []int64{2, 3}, id, "BPS rate-limit failover must never invoke native OAuth or API-key accounts")
					}
					for _, url := range upstream.urls {
						require.Equal(t, basispoints.ResponsesURL, url)
					}
					if healthyBPS {
						require.Equal(t, []int64{1, 4}, upstream.calls())
						require.Equal(t, http.StatusOK, rec.Code)
						require.Contains(t, rec.Body.String(), "done")
					} else {
						require.Equal(t, []int64{1}, upstream.calls())
						require.Equal(t, http.StatusTooManyRequests, rec.Code)
						require.Equal(t, "30", rec.Header().Get("Retry-After"))
						require.Contains(t, rec.Body.String(), "rate_limit")
					}
					require.NotContains(t, rec.Body.String(), "PRIVATE_UPSTREAM")
				})
			}
		}
	}
}

func TestExcelBPS429MixedPoolStopsOnCancellation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", endpoint, stream), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				upstream := &excelBPSFailoverUpstream{
					answer: func(int) *http.Response { return excelBPS429("30") },
					onDo:   func(int) { cancel() },
				}
				handler := newExcelBPSFailoverTestHandler(t, upstream, false, excelBPSMixedPoolAccounts(true)...)
				c, rec := newExcelBPSProtocolContext(ctx, endpoint, stream)
				runExcelBPSProtocol(handler, c, endpoint)
				require.Equal(t, []int64{1}, upstream.calls())
				require.Equal(t, statusClientClosedRequest, c.Writer.Status())
				require.Zero(t, rec.Body.Len())
			})
		}
	}
}

func TestExcelBPSModelAccessFailoverPreservesModelAndRoute(t *testing.T) {
	for _, endpoint := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"} {
		for _, stream := range []bool{false, true} {
			for _, healthyBPS := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%t/healthy=%t", endpoint, stream, healthyBPS), func(t *testing.T) {
					accounts := excelBPSMixedPoolAccounts(healthyBPS)
					// This candidate is BPS but maps the request to a different actual model.
					different := excelBPSMixedPoolAccounts(true)[3]
					different.ID, different.Priority = 5, 2
					different.Credentials["model_mapping"] = map[string]any{"gpt-6-astra": "gpt-6-sol"}
					accounts = append(accounts, different)
					upstream := &excelBPSFailoverUpstream{answer: func(call int) *http.Response {
						if call > 0 {
							return excelBPSCompleted()
						}
						return &http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"basispoints_model_access_changed","message":"PRIVATE_UPSTREAM"}}`))}
					}}
					handler := newExcelBPSFailoverTestHandler(t, upstream, false, accounts...)
					handler.maxAccountSwitches = 1
					c, rec := newExcelBPSProtocolContext(context.Background(), endpoint, stream)
					runExcelBPSProtocol(handler, c, endpoint)
					for i, id := range upstream.calls() {
						require.NotContains(t, []int64{2, 3, 5}, id)
						require.Equal(t, basispoints.ResponsesURL, upstream.urls[i])
						require.Equal(t, "gpt-6-astra", gjson.GetBytes(upstream.bodies[i], "model").String())
					}
					if healthyBPS {
						require.Equal(t, []int64{1, 4}, upstream.calls())
						require.Equal(t, http.StatusOK, rec.Code)
					} else {
						require.Equal(t, []int64{1}, upstream.calls())
						require.Equal(t, http.StatusForbidden, rec.Code)
						require.Equal(t, "basispoints_model_access_changed", gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
					}
					require.NotContains(t, rec.Body.String(), "PRIVATE_UPSTREAM")
				})
			}
		}
	}
}

func TestExcelBPSChatFailoverUsesNormalizedUpstreamModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("status=%d/stream=%t", status, stream), func(t *testing.T) {
				pool := excelBPSMixedPoolAccounts(true)
				accounts := []service.Account{pool[0], pool[3]}
				upstream := &excelBPSFailoverUpstream{answer: func(call int) *http.Response {
					if call > 0 {
						return excelBPSCompletedModel("gpt-5.4")
					}
					if status == http.StatusTooManyRequests {
						return excelBPS429("30")
					}
					return excelBPSModelAccessChanged()
				}}
				handler := newExcelBPSFailoverTestHandler(t, upstream, false, accounts...)
				handler.maxAccountSwitches = 1
				c, rec := newExcelBPSProtocolModelContext(context.Background(), "/v1/chat/completions", "gpt-5.4-high", stream)

				handler.ChatCompletions(c)

				require.Equal(t, []int64{1, 4}, upstream.calls(), "a healthy BPS account with the same normalized model must remain eligible")
				require.Equal(t, http.StatusOK, rec.Code)
				require.Contains(t, rec.Body.String(), "done")
				require.NotContains(t, rec.Body.String(), "PRIVATE_UPSTREAM")
				for i, body := range upstream.bodies {
					require.Equal(t, basispoints.ResponsesURL, upstream.urls[i])
					require.Equal(t, "gpt-5.4", gjson.GetBytes(body, "model").String(), "both attempts must send the normalized upstream model")
				}
				require.Equal(t, gjson.GetBytes(upstream.bodies[0], "input").Raw, gjson.GetBytes(upstream.bodies[1], "input").Raw)
			})
		}
	}
}

func TestExcelBPSMessagesFailoverUsesAccountMappingBeforeDispatchModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("status=%d/stream=%t", status, stream), func(t *testing.T) {
				pool := excelBPSMixedPoolAccounts(true)
				first, healthy := pool[0], pool[3]
				different := excelBPSMixedPoolAccounts(true)[3]
				different.ID, different.Priority = 5, 1
				healthy.Priority = 2
				for _, account := range []*service.Account{&first, &healthy} {
					account.Credentials["model_mapping"] = map[string]any{"alias": "gpt-6-astra", "gpt-6-astra": "gpt-6-astra"}
				}
				// Scheduling the dispatch model accepts this account, but forwarding
				// must prefer its mapping of the original request model to Sol.
				different.Credentials["model_mapping"] = map[string]any{"alias": "gpt-6-sol", "gpt-6-astra": "gpt-6-astra"}
				upstream := &excelBPSFailoverUpstream{answer: func(call int) *http.Response {
					if call > 0 {
						return excelBPSCompleted()
					}
					if status == http.StatusTooManyRequests {
						return excelBPS429("30")
					}
					return excelBPSModelAccessChanged()
				}}
				handler := newExcelBPSFailoverTestHandler(t, upstream, false, first, different, healthy)
				// Rejecting the incompatible candidate must preserve the one allowed switch.
				handler.maxAccountSwitches = 1
				c, rec := newExcelBPSProtocolModelContext(context.Background(), "/v1/messages", "alias", stream)
				key, ok := middleware2.GetAPIKeyFromContext(c)
				require.True(t, ok)
				key.Group.MessagesDispatchModelConfig = service.OpenAIMessagesDispatchModelConfig{
					ExactModelMappings: map[string]string{"alias": "gpt-6-astra"},
				}
				key.Group.DefaultMappedModel = "gpt-6-astra"

				handler.Messages(c)

				require.Equal(t, []int64{1, 4}, upstream.calls(), "the candidate mapping the original alias to Sol must be skipped without consuming the switch budget")
				require.Equal(t, http.StatusOK, rec.Code)
				require.Contains(t, rec.Body.String(), "done")
				require.NotContains(t, rec.Body.String(), "PRIVATE_UPSTREAM")
				for i, body := range upstream.bodies {
					require.Equal(t, basispoints.ResponsesURL, upstream.urls[i])
					require.Equal(t, "gpt-6-astra", gjson.GetBytes(body, "model").String(), "the account switch must preserve the actual BPS upstream model")
				}
				require.Equal(t, gjson.GetBytes(upstream.bodies[0], "input").Raw, gjson.GetBytes(upstream.bodies[1], "input").Raw)
			})
		}
	}
}

func TestExcelBPSFailoverReleasesExcludedSelection(t *testing.T) {
	released := 0
	selection := &service.AccountSelectionResult{Account: &service.Account{ID: 5}, Acquired: true, ReleaseFunc: func() { released++ }}
	excluded := map[int64]struct{}{}
	err := &service.UpstreamFailoverError{Reason: service.ExcelBPSModelAccessChangedReason}
	require.True(t, skipNativeAfterExcelBPSFailure(selection, err, excluded, "gpt-6-astra"))
	require.Equal(t, 1, released)
	require.Contains(t, excluded, int64(5))
	require.Nil(t, selection.ReleaseFunc)
}
