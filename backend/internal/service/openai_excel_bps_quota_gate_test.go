//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func bpsQuotaGateFixture(group, model string, delay time.Duration, now time.Time) excelBPSProviderFailure {
	message := fmt.Sprintf("Rate limit reached for %s in organization org-%s on tokens per min (TPM): Limit 100, Used 100, Requested 5.", model, group)
	raw, _ := json.Marshal(map[string]any{"error": map[string]string{"code": "rate_limit_exceeded", "type": "tokens", "message": message}})
	return excelBPSClassifyProviderFailure(raw, http.StatusOK, fmt.Sprintf("%g", delay.Seconds()), now)
}

func TestBPSRecoveryHardeningQuotaGateKnownGroupsAndModels(t *testing.T) {
	var gate excelBPSQuotaGate
	now := time.Unix(1700000000, 0)
	gate.observe(1, "model-a", bpsQuotaGateFixture("same", "model-a", time.Second, now), now)
	require.Zero(t, gate.delay(2, "model-a", now), "unknown account cannot inherit a guessed group")
	gate.observe(2, "model-a", bpsQuotaGateFixture("same", "model-a", 4*time.Second, now), now)
	gate.observe(3, "model-a", bpsQuotaGateFixture("other", "model-a", time.Second, now), now)
	gate.observe(1, "model-b", bpsQuotaGateFixture("same", "model-b", time.Second, now), now)
	require.Equal(t, 3*time.Second, gate.delay(1, "model-a", now.Add(time.Second)))
	require.Equal(t, 3*time.Second, gate.delay(2, "model-a", now.Add(time.Second)))
	require.Zero(t, gate.delay(3, "model-a", now.Add(time.Second)))
	require.Zero(t, gate.delay(1, "model-b", now.Add(time.Second)))
	require.Zero(t, gate.delay(1, "alias-a", now), "caller must pass mapped upstream model")
	require.Zero(t, gate.delay(1, "model-a", now.Add(4*time.Second)))
}

func TestBPSRecoveryHardeningQuotaGateRejectsScopeConfusion(t *testing.T) {
	var gate excelBPSQuotaGate
	now := time.Unix(1700000000, 0)
	known := bpsQuotaGateFixture("same", "model-a", time.Second, now)
	gate.observe(1, "model-a", known, now)
	gate.observe(2, "model-a", known, now)
	unknown := excelBPSProviderFailure{status: http.StatusTooManyRequests, retry: true, delay: 5 * time.Second}
	gate.observe(1, "model-a", unknown, now)
	require.Equal(t, 5*time.Second, gate.delay(1, "model-a", now))
	require.Equal(t, time.Second, gate.delay(2, "model-a", now), "untyped endpoint throttle cannot extend known provider scope")
	gate.observe(3, "model-b", known, now)
	require.Empty(t, gate.accounts[excelBPSQuotaAccountKey{3, "model-b"}].groupHash, "message model must match actual route")
	for i, bad := range []excelBPSProviderFailure{
		{status: http.StatusForbidden, retry: true, delay: time.Second},
		{status: http.StatusTooManyRequests, retry: true, permanent: true, delay: time.Second},
		{status: http.StatusTooManyRequests, retry: false, delay: time.Second},
		{status: http.StatusTooManyRequests, retry: true, delay: excelBPSQuotaMaxDelay + time.Second},
	} {
		gate.observe(int64(10+i), "model-a", bad, now)
		require.Zero(t, gate.delay(int64(10+i), "model-a", now))
	}
}

func TestBPSRecoveryHardeningQuotaGateTTLAndCapacity(t *testing.T) {
	var gate excelBPSQuotaGate
	now := time.Unix(1700000000, 0)
	for i := 0; i < excelBPSQuotaMaxEntries+8; i++ {
		gate.observe(int64(i+1), "model-a", bpsQuotaGateFixture(fmt.Sprintf("fixture%d", i), "model-a", time.Second, now), now)
	}
	require.Len(t, gate.accounts, excelBPSQuotaMaxEntries)
	require.Len(t, gate.scopes, excelBPSQuotaMaxEntries)
	require.Equal(t, time.Second, gate.delay(1, "model-a", now), "capacity pressure cannot evict active entries")
	require.Zero(t, gate.delay(1, "model-a", now.Add(excelBPSQuotaBindingTTL)))
	later := now.Add(excelBPSQuotaBindingTTL + time.Second)
	gate.observe(5000, "model-a", bpsQuotaGateFixture("new", "model-a", time.Second, later), later)
	require.Len(t, gate.accounts, 1)
	require.Len(t, gate.scopes, 1)
}

func TestBPSRecoveryHardeningQuotaGateConcurrentMonotoneExtension(t *testing.T) {
	var gate excelBPSQuotaGate
	now := time.Unix(1700000000, 0)
	var wg sync.WaitGroup
	for i := 1; i <= 32; i++ {
		wg.Add(1)
		go func(seconds int) {
			defer wg.Done()
			gate.observe(1, "model-a", bpsQuotaGateFixture("same", "model-a", time.Duration(seconds)*time.Second, now), now)
			_ = gate.delay(1, "model-a", now)
		}(i)
	}
	wg.Wait()
	require.Equal(t, 32*time.Second, gate.delay(1, "model-a", now))
	gate.observe(1, "model-a", bpsQuotaGateFixture("same", "model-a", time.Second, now), now)
	require.Equal(t, 32*time.Second, gate.delay(1, "model-a", now))
}

func TestBPSRecoveryHardeningQuotaGateUntypedCannotRenewGroup(t *testing.T) {
	var gate excelBPSQuotaGate
	now := time.Unix(1700000000, 0)
	gate.observe(1, "model-a", bpsQuotaGateFixture("same", "model-a", time.Second, now), now)
	later := now.Add(9 * time.Minute)
	unknown := excelBPSProviderFailure{status: http.StatusTooManyRequests, retry: true, delay: time.Second}
	gate.observe(1, "model-a", unknown, later)
	later = now.Add(11 * time.Minute)
	gate.observe(2, "model-a", bpsQuotaGateFixture("same", "model-a", 4*time.Second, later), later)
	require.Zero(t, gate.delay(1, "model-a", later), "untyped throttles cannot perpetuate an old shared group")
	require.Equal(t, 4*time.Second, gate.delay(2, "model-a", later))
	// Fresh, validated evidence may teach the relation again.
	gate.observe(1, "model-a", bpsQuotaGateFixture("same", "model-a", time.Second, later), later)
	require.Equal(t, 4*time.Second, gate.delay(1, "model-a", later))
	// Account cooldown survives expiration of an unrelated learned relation.
	var isolated excelBPSQuotaGate
	isolated.observe(1, "model-a", bpsQuotaGateFixture("same", "model-a", time.Second, now), now)
	unknown.delay = 20 * time.Minute
	isolated.observe(1, "model-a", unknown, now.Add(9*time.Minute))
	require.Equal(t, 18*time.Minute, isolated.delay(1, "model-a", now.Add(11*time.Minute)))
	require.Empty(t, isolated.accounts[excelBPSQuotaAccountKey{1, "model-a"}].groupHash)
}

func TestBPSRecoveryHardeningQuotaGateDoesNotReserveCoolingAlternative(t *testing.T) {
	first, second := excelAccount(), excelAccount()
	first.ID, second.ID = 49, 47
	repo := schedulerTestOpenAIAccountRepo{accounts: []Account{*first, *second}}
	cache := &openAISnapshotCacheStub{snapshotAccounts: []*Account{first, second}, accountsByID: map[int64]*Account{49: first, 47: second}}
	svc := openAIClientToolsTestService(nil)
	svc.accountRepo = repo
	svc.schedulerSnapshot = NewSchedulerSnapshotService(cache, nil, repo, nil, nil)
	c, _ := bpsQuotaForwardContext()
	c.Set("api_key", &APIKey{ID: 1})
	recovery := newExcelBPSRecovery(svc.cfg.Gateway.ExcelBPSTimeouts)
	recovery.remaining = 1
	now := time.Now()
	svc.excelBPSQuotaGate.observe(second.RPMAccountID(), "gpt-6-astra", bpsQuotaGateFixture("same", "gpt-6-astra", 4*time.Second, now), now)
	require.False(t, svc.excelBPSReserveAccountSwitch(context.Background(), c, first, "gpt-6-astra", "gpt-6-astra", recovery))
	require.Equal(t, 1, recovery.remaining, "eligibility probing must not spend the last send")
	require.False(t, svc.isExcelBPSCoolingDown(second, "gpt-6-astra"), "a short quota wait must not remove every account from initial scheduling")
	// Model-specific feedback must not make an unrelated model unavailable.
	require.True(t, svc.excelBPSReserveAccountSwitch(context.Background(), c, first, "gpt-5.6-sol", "gpt-5.6-sol", recovery))
}

func TestBPSRecoveryHardeningQuotaGateRepairDoesNotCoolOtherModels(t *testing.T) {
	svc := openAIClientToolsTestService(nil)
	account := excelAccount()
	sends := 0
	svc.httpUpstream = &nativeAttachmentUpstream{do: func(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		sends++
		if sends > 1 {
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(bpsProviderRecoverySuccess()))}, nil
		}
		// A concurrent request reports the model limit after the first send, but
		// before this response's malformed tool triggers a repair continuation.
		now := time.Now()
		svc.excelBPSQuotaGate.observe(account.RPMAccountID(), "gpt-6-astra", bpsQuotaGateFixture("concurrent", "gpt-6-astra", 6*time.Second, now), now)
		envelope, _ := json.Marshal(map[string]any{"name": "missing", "arguments": map[string]any{"cmd": "pwd"}})
		event, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": "original", "status": "completed", "usage": map[string]any{"input_tokens": 5, "output_tokens": 1}, "output": []any{map[string]any{"id": "fc_original", "call_id": "call_original", "type": "function_call", "name": "run_officejs", "arguments": map[string]any{"code": string(envelope)}}}}})
		return bpsCompletionResponse(200, "data: "+string(event)+"\n\n"), nil
	}}
	c, rec := bpsQuotaForwardContext()
	body := []byte("{\"model\":\"gpt-6-astra\",\"stream\":false,\"input\":\"run pwd\",\"tools\":[{\"type\":\"function\",\"name\":\"shell\",\"parameters\":{\"type\":\"object\",\"required\":[\"cmd\"],\"properties\":{\"cmd\":{\"type\":\"string\"}}}}]}")
	result, err := svc.Forward(context.Background(), c, account, body)
	require.Error(t, err)
	require.NotNil(t, result)
	require.EqualValues(t, 5, result.Usage.InputTokens, "a blocked repair preserves the already billed first response")
	require.EqualValues(t, 1, result.Usage.OutputTokens)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.NotEmpty(t, rec.Header().Get("Retry-After"))
	require.Equal(t, 1, sends, "blocked repair must not reach upstream")
	require.False(t, svc.isExcelBPSCoolingDown(account, "gpt-5.6-sol"), "model gate must not become an account-wide cooldown")
	other, otherRec := bpsQuotaForwardContext()
	_, err = svc.Forward(context.Background(), other, account, []byte("{\"model\":\"gpt-5.6-sol\",\"stream\":false,\"input\":\"test\"}"))
	require.NoError(t, err)
	require.Equal(t, 2, sends)
	require.Contains(t, otherRec.Body.String(), "resp_recovered")
}
