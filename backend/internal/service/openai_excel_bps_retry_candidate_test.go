//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Old image-owner workers can republish incomplete scheduler metadata while the
// full account and database already contain a BPS pause. The last send belongs
// to the current account unless the ordinary fresh scheduling gates agree that
// an alternative can actually serve the same model.
func TestBPSProviderRecoveryRejectsStaleAlternatives(t *testing.T) {
	for _, tc := range []struct {
		name      string
		change    func(*Account)
		staleFull bool
	}{
		{name: "full_account_paused", change: func(a *Account) { a.Extra[OpenAIExcelBPSPausedOn403AtExtraKey] = "2026-09-29T08:39:51Z" }},
		{name: "database_paused", staleFull: true, change: func(a *Account) { a.Extra[OpenAIExcelBPSPausedOn403AtExtraKey] = "2026-09-29T08:39:51Z" }},
		{name: "model_whitelist_excludes_request", change: func(a *Account) { a.Credentials["model_mapping"] = map[string]any{"gpt-5.6-sol": "gpt-5.6-sol"} }},
		{name: "bps_scope_excludes_request", change: func(a *Account) { a.Extra["openai_excel_bps_models"] = []string{"gpt-5.6-sol"} }},
		{name: "model_mapping_changed", change: func(a *Account) { a.Credentials["model_mapping"] = map[string]any{"gpt-6-astra": "gpt-5.6-sol"} }},
		{name: "bps_route_disabled", change: func(a *Account) { a.Extra["openai_excel_bps"] = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, metadata, fresh := excelAccount(), excelAccount(), excelAccount()
			first.ID, metadata.ID, fresh.ID = 49, 47, 47
			delete(metadata.Extra, "openai_passthrough")
			delete(fresh.Extra, "openai_passthrough")
			tc.change(fresh)
			full := fresh
			if tc.staleFull {
				full = metadata
			}
			repo := schedulerTestOpenAIAccountRepo{accounts: []Account{*first, *fresh}}
			cache := &openAISnapshotCacheStub{snapshotAccounts: []*Account{first, metadata}, accountsByID: map[int64]*Account{49: first, 47: full}}
			svc := openAIClientToolsTestService(nil)
			svc.accountRepo = repo
			svc.schedulerSnapshot = NewSchedulerSnapshotService(cache, nil, repo, nil, nil)
			svc.cfg.Gateway.ExcelBPSTimeouts.MaxAttempts = 3
			var sent []int64
			svc.httpUpstream = &nativeAttachmentUpstream{do: func(_ *http.Request, _ string, id int64, _ int) (*http.Response, error) {
				sent = append(sent, id)
				wire := bpsProviderRecoveryFailure("error", "rate_limit_exceeded")
				if len(sent) == 3 {
					wire = bpsProviderRecoverySuccess()
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}, nil
			}}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			c.Set("api_key", &APIKey{ID: 1})
			_, err := svc.Forward(context.Background(), c, first, []byte(`{"model":"gpt-6-astra","stream":true,"input":"test"}`))
			t.Logf("OBSERVED candidate=%s sends=%v status=%d error=%v", tc.name, sent, rec.Code, err)
			require.NoError(t, err)
			require.Equal(t, []int64{49, 49, 49}, sent)
			require.Equal(t, 3, c.GetInt("excel_bps_upstream_attempt"))
			require.Contains(t, rec.Body.String(), "resp_recovered")
			require.NotContains(t, rec.Body.String(), "rate_limit_exceeded")
		})
	}
}

func TestBPSProviderRecoveryHealthyFreshAlternativeKeepsLastSend(t *testing.T) {
	first, second := excelAccount(), excelAccount()
	first.ID, second.ID = 49, 47
	repo := schedulerTestOpenAIAccountRepo{accounts: []Account{*first, *second}}
	cache := &openAISnapshotCacheStub{snapshotAccounts: []*Account{first, second}, accountsByID: map[int64]*Account{49: first, 47: second}}
	svc := openAIClientToolsTestService(nil)
	svc.accountRepo = repo
	svc.schedulerSnapshot = NewSchedulerSnapshotService(cache, nil, repo, nil, nil)
	svc.cfg.Gateway.ExcelBPSTimeouts.MaxAttempts = 3
	var sent []int64
	svc.httpUpstream = &nativeAttachmentUpstream{do: func(_ *http.Request, _ string, id int64, _ int) (*http.Response, error) {
		sent = append(sent, id)
		wire := bpsProviderRecoveryFailure("error", "rate_limit_exceeded")
		if id == second.ID {
			wire = bpsProviderRecoverySuccess()
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}, nil
	}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: 1})
	body := []byte(`{"model":"gpt-6-astra","stream":true,"input":"test"}`)
	_, err := svc.Forward(context.Background(), c, first, body)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Equal(t, []int64{49, 49}, sent)
	_, err = svc.Forward(context.Background(), c, second, body)
	require.NoError(t, err)
	require.Equal(t, []int64{49, 49, 47}, sent)
	require.Contains(t, rec.Body.String(), "resp_recovered")
}

func TestBPSProviderRecoveryNestedHeaderLongestDelay(t *testing.T) {
	for _, kind := range []string{"error", "response.failed"} {
		for _, tc := range []struct {
			name, headers, outer string
			want                 time.Duration
		}{
			{"incident", `"retry-after":"1","retry-after-ms":"52"`, "", time.Second},
			{"milliseconds_longer", `"Retry-After":"0.3","Retry-After-Ms":"1500"`, "", 1500 * time.Millisecond},
			{"outer_longer", `"retry-after":"1","retry-after-ms":"52"`, "2", 2 * time.Second},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				detail := `{"code":"rate_limit_exceeded","type":"tokens","headers":{` + tc.headers + `},"message":"Rate limit reached. Please try again in 52ms."}`
				raw := `{"type":"error","error":` + detail + `}`
				if kind == "response.failed" {
					raw = `{"type":"response.failed","response":{"error":` + detail + `}}`
				}
				f := excelBPSClassifyProviderFailure([]byte(raw), 200, tc.outer, time.Now())
				t.Logf("OBSERVED event=%s hint=%s retry=%v delay=%s", kind, tc.name, f.retry, f.delay)
				require.True(t, f.retry)
				require.Equal(t, tc.want, f.delay)
			})
		}
	}
}

// RPM headroom is a scheduling preference, not a hard eligibility limit. An
// otherwise healthy alternative remains selectable below its strict ceiling
// when no cooler account remains, including after the current account failed.
func TestBPSProviderRecoveryAlternativeRPMMatchesScheduler(t *testing.T) {
	for _, mode := range []string{"legacy", "legacy_batch", "advanced"} {
		t.Run(mode, func(t *testing.T) {
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
			for _, tc := range []struct {
				name string
				rpm  int
			}{
				{"below_headroom", 79}, {"at_headroom", 80},
				{"below_ceiling", 99}, {"at_ceiling", 100},
			} {
				t.Run(tc.name, func(t *testing.T) {
					first, second := excelAccount(), excelAccount()
					first.ID, second.ID = 49, 47
					second.Extra["base_rpm"] = 100
					repo := schedulerTestOpenAIAccountRepo{accounts: []Account{*first, *second}}
					cache := &openAISnapshotCacheStub{snapshotAccounts: []*Account{first, second}, accountsByID: map[int64]*Account{49: first, 47: second}}
					svc := openAIClientToolsTestService(nil)
					svc.accountRepo = repo
					svc.schedulerSnapshot = NewSchedulerSnapshotService(cache, nil, repo, nil, nil)
					svc.rpmCache = &openAIRPMTestCache{counts: map[int64]int{47: tc.rpm}}
					svc.cache = &schedulerTestGatewayCache{sessionBindings: map[string]int64{}}
					svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{})
					svc.cfg.Gateway.Scheduling.LoadBatchEnabled = mode == "legacy_batch"
					if mode == "advanced" {
						svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService("true")
					}
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
					c.Set("api_key", &APIKey{ID: 1})
					recovery := newExcelBPSRecovery(svc.cfg.Gateway.ExcelBPSTimeouts)
					recovery.remaining = 1
					reserved := svc.excelBPSReserveAccountSwitch(context.Background(), c, first, "gpt-6-astra", "gpt-6-astra", recovery)
					selection, _, err := svc.SelectAccountWithScheduler(context.Background(), nil, "", "", "gpt-6-astra", map[int64]struct{}{first.ID: {}}, OpenAIUpstreamTransportAny, false)
					if selection != nil && selection.ReleaseFunc != nil {
						defer selection.ReleaseFunc()
					}
					selected := int64(0)
					if selection != nil && selection.Account != nil {
						selected = selection.Account.ID
					}
					t.Logf("OBSERVED mode=%s rpm=%d reserved=%v selected=%d error=%v", mode, tc.rpm, reserved, selected, err)
					require.Equal(t, tc.rpm < 100, reserved)
					if tc.rpm < 100 {
						require.NoError(t, err)
						require.Equal(t, second.ID, selected)
					} else {
						require.ErrorIs(t, err, ErrOpenAIRPMExhausted)
						require.Zero(t, selected)
					}
				})
			}
		})
	}
}
