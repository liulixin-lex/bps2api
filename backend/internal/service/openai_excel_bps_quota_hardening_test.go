//go:build unit

package service

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Reflection keeps this core regression source compilable against the pristine
// classifier. Missing new metadata is then an observed assertion failure.
func bpsQuotaFailureStringField(t *testing.T, failure excelBPSProviderFailure, name string) string {
	t.Helper()
	field := reflect.ValueOf(failure).FieldByName(name)
	if !field.IsValid() {
		t.Errorf("provider failure must preserve %s", name)
		return ""
	}
	return field.String()
}

func TestBPSRecoveryHardeningQuotaCoreEffectiveHint(t *testing.T) {
	now := time.Unix(1700000000, 0)
	for _, tc := range []struct {
		name, outer, want string
		detail            map[string]any
		nested            bool
		delay             time.Duration
	}{
		{name: "embedded wins prose", detail: map[string]any{"headers": map[string]string{"retry-after": "1", "retry-after-ms": "10"}, "message": "Please try again in 10ms."}, want: "1", delay: time.Second},
		{name: "outer wins embedded", detail: map[string]any{"headers": map[string]string{"retry-after": "1"}}, outer: "2", want: "2", delay: 2 * time.Second},
		{name: "nested response hint", detail: map[string]any{"retry_after_ms": 1500}, nested: true, want: "2", delay: 1500 * time.Millisecond},
		{name: "prose wins outer", detail: map[string]any{"message": "Please retry after 3 seconds."}, outer: "1", want: "3", delay: 3 * time.Second},
		{name: "outer date preserved", detail: map[string]any{}, outer: now.Add(4 * time.Second).Format(http.TimeFormat), want: now.Add(4 * time.Second).Format(http.TimeFormat), delay: 4 * time.Second},
		{name: "no provider hint invented", detail: map[string]any{}, delay: 250 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.detail["code"] = "rate_limit_exceeded"
			envelope := map[string]any{"error": tc.detail}
			if tc.nested {
				envelope = map[string]any{"response": envelope}
			}
			raw, err := json.Marshal(envelope)
			require.NoError(t, err)
			failure := excelBPSClassifyProviderFailure(raw, http.StatusOK, tc.outer, now)
			require.True(t, failure.retry)
			require.Equal(t, tc.delay, failure.delay)
			require.Equal(t, tc.want, bpsQuotaFailureStringField(t, failure, "retryAfter"))
		})
	}
}

func TestBPSRecoveryHardeningQuotaCoreJitterAfterProviderFloor(t *testing.T) {
	now := time.Unix(1700000000, 0)
	for i := 0; i < 16; i++ {
		r := newExcelBPSRecovery(config.ExcelBPSTimeoutConfig{})
		for send := 0; send < 2; send++ {
			delay, deadline, ok := r.reserve(now, time.Second)
			require.True(t, ok)
			require.Greater(t, delay, time.Second, "provider floor must not erase positive jitter")
			require.LessOrEqual(t, delay, 1250*time.Millisecond)
			require.Equal(t, now.Add(30*time.Second), deadline)
		}
		_, _, ok := r.reserve(now, time.Second)
		require.False(t, ok, "jitter must not add sends")
	}
	for _, floor := range []time.Duration{5 * time.Second, 5*time.Second + time.Nanosecond} {
		r := newExcelBPSRecovery(config.ExcelBPSTimeoutConfig{})
		delay, _, ok := r.reserve(now, floor)
		require.Equal(t, floor <= 5*time.Second, ok)
		if ok {
			require.Equal(t, floor, delay)
		}
	}
	r := newExcelBPSRecovery(config.ExcelBPSTimeoutConfig{RecoveryBudgetSeconds: 1})
	_, _, ok := r.reserve(now, time.Second)
	require.False(t, ok, "provider floor at deadline leaves no send budget")
}

func TestBPSRecoveryHardeningQuotaCoreIdentityClassification(t *testing.T) {
	now := time.Unix(1700000000, 0)
	message := "Rate limit reached for model-a in organization org-fixtureA on tokens per min (TPM): Limit 100, Used 100, Requested 5. Please try again in 1s."
	encode := func(code, kind, text string) []byte {
		raw, err := json.Marshal(map[string]any{"error": map[string]string{"code": code, "type": kind, "message": text}})
		require.NoError(t, err)
		return raw
	}
	failure := excelBPSClassifyProviderFailure(encode("rate_limit_exceeded", "tokens", message), http.StatusOK, "", now)
	hash := bpsQuotaFailureStringField(t, failure, "quotaGroupHash")
	require.Len(t, hash, 64)
	require.NotContains(t, hash, "fixture")
	require.Equal(t, "model-a", bpsQuotaFailureStringField(t, failure, "quotaModel"))
	for _, raw := range [][]byte{
		encode("rate_limit_exceeded", "requests", message),
		encode("insufficient_quota", "tokens", message),
		encode("rate_limit_exceeded", "tokens", "An echo: "+message),
		encode("rate_limit_exceeded", "tokens", "organization org-fixtureA retry after 1s"),
	} {
		failure := excelBPSClassifyProviderFailure(raw, http.StatusTooManyRequests, "1", now)
		require.Empty(t, bpsQuotaFailureStringField(t, failure, "quotaGroupHash"))
	}
}
