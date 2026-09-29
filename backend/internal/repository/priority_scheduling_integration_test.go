//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestPrioritySchedulingSignalsSeparateModelsAndCompletedRounds(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	user := mustCreateUser(t, client, &service.User{Email: "priority@test.invalid"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-priority-test", Name: "test"})
	account := mustCreateAccount(t, client, &service.Account{Name: "priority", Platform: service.PlatformOpenAI})
	now := time.Now().Add(-time.Minute)
	add := func(model, requested string, ttft *int, cost float64, tokens int, stream bool, at time.Time) {
		t.Helper()
		_, err := repo.Create(ctx, &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, UpstreamEndpoint: func() *string { value := "/v1/responses"; return &value }(), UpstreamModel: func() *string { s := "gpt-test"; return &s }(), RequestID: fmt.Sprintf("p-%d-%s", time.Now().UnixNano(), model), Model: model, RequestedModel: requested, FirstTokenMs: ttft, ActualCost: cost, InputTokens: tokens, Stream: stream, CreatedAt: at})
		require.NoError(t, err)
	}
	fast, slow := 100, 1000
	add("upstream", "gpt-test", &fast, 0, 10, true, now) // free usage with real tokens is valid
	add("gpt-test", "", &slow, 1, 10, true, now)         // legacy model fallback
	add("other", "other", &slow, 1, 10, true, now)
	add("gpt-test", "", nil, 1, 10, true, now)
	add("gpt-test", "", &slow, 0, 0, true, now)   // failed placeholder
	add("gpt-test", "", &slow, 1, 10, false, now) // nonstreaming duration is not TTFT
	add("gpt-test", "", &slow, 1, 10, true, now.Add(-2*time.Hour))
	var plan int64
	rows, err := tx.QueryContext(ctx, `INSERT INTO scheduled_test_plans(account_id,model_id,cron_expression,enabled,pelican_config,updated_at) VALUES($1,'gpt-test','*/30 * * * *',true,'{"quality":{},"parallel_count":2}',NOW()-INTERVAL '1 hour') RETURNING id`, account.ID)
	require.NoError(t, err)
	require.True(t, rows.Next())
	require.NoError(t, rows.Scan(&plan))
	require.NoError(t, rows.Close())
	probe := func(round, status, verdict, message, action string, at time.Time) {
		t.Helper()
		_, err := tx.ExecContext(ctx, `INSERT INTO scheduled_test_results(plan_id,status,started_at,finished_at,created_at,pelican_config,quality_round_id,quality_action,quality_judgment,error_message) VALUES($1,$2,$3,$3,$3,'{"parallel_count":2}',$4,$5,jsonb_build_object('verdict',$6::text),$7)`, plan, status, at, round, action, verdict, message)
		require.NoError(t, err)
	}
	probe("passed", "success", "correct", "", "passed", now)
	probe("passed", "success", "correct", "", "passed", now)
	probe("failed", "success", "correct", "", "failure_counted:1/2", now)
	probe("failed", "failed", "incorrect", "state_degraded", "failure_counted:1/2", now)
	probe("partial", "success", "correct", "", "passed", now) // incomplete save cannot count as a passed round
	probe("unknown", "success", "correct", "", "inconclusive", now)
	probe("unknown", "failed", "inconclusive", "timeout", "inconclusive", now)
	probe("stale", "success", "correct", "", "stale_run", now)
	probe("stale", "success", "correct", "", "stale_run", now)
	probe("old", "success", "correct", "", "passed", now.Add(-2*time.Hour))
	probe("old", "success", "correct", "", "passed", now.Add(-2*time.Hour))
	_, err = tx.ExecContext(ctx, `UPDATE usage_logs SET total_cost=3, account_stats_cost=2, account_rate_multiplier=0.5 WHERE account_id=$1 AND actual_cost>0`, account.ID)
	require.NoError(t, err)
	got, err := repo.ReadPrioritySchedulingSignals(ctx, service.PrioritySchedulingQuery{AccountIDs: []int64{account.ID}, Routes: []string{"native"}, UpstreamModels: []string{"gpt-test"}, Model: "gpt-test", UsageSince: time.Now().Add(-time.Hour), QualitySince: time.Now().Add(-time.Hour)})
	require.NoError(t, err)
	require.Equal(t, 2, got[account.ID].Samples)
	require.Equal(t, 3, got[account.ID].ProfitSamples, "nonstreaming and missing TTFT still contribute financial records")
	require.InDelta(t, 3, got[account.ID].Revenue, 0.0001)
	require.InDelta(t, 6, got[account.ID].BaseCost, 0.0001, "account statistics price is aggregated before any billing or cost multiplier")
	groupID := int64(999999)
	differentGroup, err := repo.ReadPrioritySchedulingSignals(ctx, service.PrioritySchedulingQuery{AccountIDs: []int64{account.ID}, Routes: []string{"native"}, UpstreamModels: []string{"gpt-test"}, Model: "gpt-test", GroupID: &groupID, UsageSince: time.Now().Add(-time.Hour), QualitySince: time.Now().Add(-time.Hour)})
	require.NoError(t, err)
	require.Zero(t, differentGroup[account.ID].Revenue)
	require.Zero(t, differentGroup[account.ID].ProfitSamples)
	require.InDelta(t, 910, got[account.ID].P90TTFTMs, 0.01)
	require.Zero(t, got[account.ID].QualitySamples, "probe records cannot prove actual channel and mapped model")
	require.Zero(t, got[account.ID].QualityPassed)
	_, err = tx.ExecContext(ctx, `UPDATE scheduled_test_plans SET enabled=false WHERE id=$1`, plan)
	require.NoError(t, err)
	got, err = repo.ReadPrioritySchedulingSignals(ctx, service.PrioritySchedulingQuery{AccountIDs: []int64{account.ID}, Routes: []string{"native"}, UpstreamModels: []string{"gpt-test"}, Model: "gpt-test", UsageSince: time.Now().Add(-time.Hour), QualitySince: time.Now().Add(-time.Hour)})
	require.NoError(t, err)
	require.Zero(t, got[account.ID].QualitySamples)
}

func TestPriorityAccountCostMultiplierRoundTrip(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	billing, group := 1.0, 5.0
	account := mustCreateAccount(t, tx.Client(), &service.Account{Name: "cost-roundtrip", Platform: service.PlatformOpenAI, RateMultiplier: &billing, GroupRateMultiplier: &group, Extra: map[string]any{"untouched": true}})
	require.NoError(t, repo.Update(ctx, account))
	require.Nil(t, account.CostMultiplier())
	for _, cost := range []float64{0.25, 0} {
		require.NoError(t, repo.UpdateExtra(ctx, account.ID, map[string]any{service.AccountCostMultiplierExtraKey: cost}))
		loaded, err := repo.GetByID(ctx, account.ID)
		require.NoError(t, err)
		require.Equal(t, cost, *loaded.CostMultiplier())
		require.Equal(t, billing, loaded.BillingRateMultiplier())
		require.Equal(t, group, loaded.UserGroupRateMultiplier())
		require.Equal(t, true, loaded.Extra["untouched"])
	}
	require.True(t, shouldEnqueueSchedulerOutboxForExtraUpdates(map[string]any{service.AccountCostMultiplierExtraKey: 0.1}), "cost edits invalidate cached scheduler accounts")
}

func TestPriorityQualityUsesImmutableBPSProvenanceOnly(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	account := mustCreateAccount(t, client, &service.Account{Name: "quality-provenance", Platform: service.PlatformOpenAI})
	var plan int64
	rows, err := tx.QueryContext(ctx, "INSERT INTO scheduled_test_plans(account_id,model_id,cron_expression,enabled,pelican_config) VALUES($1,'mutable-old-model','*/30 * * * *',true,'{}') RETURNING id", account.ID)
	require.NoError(t, err)
	require.True(t, rows.Next())
	require.NoError(t, rows.Scan(&plan))
	require.NoError(t, rows.Close())
	add := func(round, verdict, endpoint, model string, proofAccount int64) {
		t.Helper()
		// The result config is the immutable execution snapshot, independent of later plan edits.
		config := map[string]any{"test_channel": "bps", "quality": map[string]any{"action": "observe_only"}, "parallel_count": 2, "test_provenance": map[string]any{"account_id": proofAccount, "requested_model": "alias", "upstream_model": model, "upstream_endpoint": endpoint}}
		raw, marshalErr := json.Marshal(config)
		require.NoError(t, marshalErr)
		status, message := "success", ""
		if verdict == "incorrect" {
			status, message = "failed", "answer_mismatch"
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO scheduled_test_results(plan_id,status,started_at,finished_at,created_at,pelican_config,quality_round_id,quality_action,quality_judgment,error_message) VALUES($1,$2,NOW(),NOW(),NOW(),$3::jsonb,$4,'observed',jsonb_build_object('verdict',$5::text),$6)", plan, status, string(raw), round, verdict, message)
		require.NoError(t, err)
	}
	for i := 0; i < 2; i++ {
		add("proven-pass", "correct", "/basispoints/api/responses", "mapped", account.ID)
		add("proven-fail", "incorrect", "/basispoints/api/responses", "mapped", account.ID)
		add("wrong-endpoint", "incorrect", "/v1/responses", "mapped", account.ID)
		add("wrong-model", "incorrect", "/basispoints/api/responses", "other", account.ID)
		add("wrong-account", "incorrect", "/basispoints/api/responses", "mapped", account.ID+100000)
	}
	add("partial", "correct", "/basispoints/api/responses", "mapped", account.ID)
	query := service.PrioritySchedulingQuery{AccountIDs: []int64{account.ID}, Routes: []string{"bps"}, UpstreamModels: []string{"mapped"}, Model: "alias", UsageSince: time.Now().Add(-time.Hour), QualitySince: time.Now().Add(-time.Hour)}
	got, err := repo.ReadPrioritySchedulingSignals(ctx, query)
	require.NoError(t, err)
	require.Equal(t, 2, got[account.ID].QualitySamples)
	require.Equal(t, 1, got[account.ID].QualityPassed)
	// Later plan edits never relabel an already observed model or channel.
	_, err = tx.ExecContext(ctx, "UPDATE scheduled_test_plans SET model_id='changed',enabled=false WHERE id=$1", plan)
	require.NoError(t, err)
	same, err := repo.ReadPrioritySchedulingSignals(ctx, query)
	require.NoError(t, err)
	require.Equal(t, got, same)
	query.Routes = []string{"native"}
	got, err = repo.ReadPrioritySchedulingSignals(ctx, query)
	require.NoError(t, err)
	require.Zero(t, got[account.ID].QualitySamples)
	query.Routes = []string{"bps"}
	query.UpstreamModels = []string{"new-mapping"}
	got, err = repo.ReadPrioritySchedulingSignals(ctx, query)
	require.NoError(t, err)
	require.Zero(t, got[account.ID].QualitySamples)
}
