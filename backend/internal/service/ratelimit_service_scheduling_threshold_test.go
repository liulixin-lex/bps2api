//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestRateLimitService_ApplyAccountSchedulingThreshold_SetsTempUnschedulable(t *testing.T) {
	accountSchedulingThresholdsSF.Forget(SettingKeyAccountSchedulingThresholds)
	accountSchedulingThresholdsCache.Store(&cachedAccountSchedulingThresholds{})

	settingsRepo := newMockSettingRepo()
	settingsRepo.data[SettingKeyAccountSchedulingThresholds] = `{"openai":80}`

	accountRepo := &rateLimitAccountRepoStub{}
	rl := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
	rl.SetSettingService(NewSettingService(settingsRepo, &config.Config{}))

	until := time.Now().UTC().Add(6 * time.Hour)
	account := &Account{
		ID:          1001,
		Platform:    PlatformOpenAI,
		Status:      StatusActive,
		Schedulable: true,
		Extra: map[string]any{
			"codex_7d_used_percent": 91.5,
			"codex_7d_reset_at":     until.Format(time.RFC3339),
		},
	}

	blocked := rl.ApplyAccountSchedulingThreshold(context.Background(), account)

	require.True(t, blocked)
	require.Equal(t, 1, accountRepo.tempCalls)
	require.NotNil(t, account.TempUnschedulableUntil)
	require.WithinDuration(t, until, *account.TempUnschedulableUntil, time.Second)
	require.True(t, IsAccountSchedulingThresholdReason(accountRepo.lastTempReason))

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(accountRepo.lastTempReason), &payload))
	require.Equal(t, PlatformOpenAI, payload["platform"])
	require.Equal(t, "7d", payload["window"])
	require.Equal(t, float64(80), payload["threshold_percent"])
	require.Equal(t, float64(91.5), payload["used_percent"])
	require.Contains(t, payload["error_message"], "91.5% used >= 80%")
}

func TestOpenAIGatewayService_SchedulingThresholdPreservesBPSStaleSnapshot(t *testing.T) {
	for _, test := range []struct {
		name            string
		enabled, scoped bool
		models          any
		stale, blocked  bool
	}{
		{"BPS stale snapshot", true, true, []string{"gpt-6-astra"}, true, false},
		{"BPS fresh snapshot keeps pause", true, true, []string{"gpt-6-astra"}, false, true},
		{"native stale snapshot keeps future reset pause", false, false, nil, true, true},
		{"disabled BPS keeps future reset pause", false, true, []string{"gpt-6-astra"}, true, true},
		{"legacy BPS all models", true, false, nil, true, false},
		{"empty BPS selection is native", true, true, []string{}, true, true},
		{"null BPS selection is native", true, true, nil, true, true},
		{"blank BPS selection is native", true, true, []string{" "}, true, true},
		{"invalid BPS selection is native", true, true, "gpt-6-astra", true, true},
		{"JSON BPS selection", true, true, []any{false, " gpt-6-astra "}, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			accountSchedulingThresholdsSF.Forget(SettingKeyAccountSchedulingThresholds)
			accountSchedulingThresholdsCache.Store(&cachedAccountSchedulingThresholds{})
			settingsRepo := newMockSettingRepo()
			settingsRepo.data[SettingKeyAccountSchedulingThresholds] = `{"openai":80}`
			accountRepo := &rateLimitAccountRepoStub{}
			rl := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
			rl.SetSettingService(NewSettingService(settingsRepo, &config.Config{}))
			service := &OpenAIGatewayService{rateLimitService: rl}
			now := time.Now().UTC()
			updatedAt := now
			if test.stale {
				updatedAt = now.Add(-3 * time.Hour)
			}
			account := Account{
				ID: 1001, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
				Extra: map[string]any{
					"openai_excel_bps": test.enabled, "codex_5h_used_percent": 99.0,
					"codex_usage_updated_at": updatedAt.Format(time.RFC3339),
					"codex_5h_reset_at":      now.Add(time.Hour).Format(time.RFC3339),
				},
			}
			if test.scoped {
				account.Extra["openai_excel_bps_models"] = test.models
			}
			accounts := []Account{account}
			filtered := service.filterOpenAIAccountsBySchedulingThreshold(context.Background(), accounts)
			if test.blocked {
				require.Empty(t, filtered)
				require.Equal(t, 1, accountRepo.tempCalls)
				require.NotNil(t, accounts[0].TempUnschedulableUntil)
			} else {
				require.Len(t, filtered, 1)
				require.Zero(t, accountRepo.tempCalls, "旧 Codex 快照不能写整个 BPS 账号暂停")
				require.Nil(t, accounts[0].TempUnschedulableUntil)
			}
		})
	}
}

func TestRateLimitService_ApplyAccountSchedulingThreshold_UsesAccountOverrideInReason(t *testing.T) {
	accountSchedulingThresholdsSF.Forget(SettingKeyAccountSchedulingThresholds)
	accountSchedulingThresholdsCache.Store(&cachedAccountSchedulingThresholds{})

	settingsRepo := newMockSettingRepo()
	settingsRepo.data[SettingKeyAccountSchedulingThresholds] = `{"openai":90}`

	accountRepo := &rateLimitAccountRepoStub{}
	rl := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
	rl.SetSettingService(NewSettingService(settingsRepo, &config.Config{}))

	until := time.Now().UTC().Add(6 * time.Hour)
	account := &Account{
		ID:          1003,
		Platform:    PlatformOpenAI,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"account_scheduling_threshold": 80,
		},
		Extra: map[string]any{
			"codex_7d_used_percent": 85.5,
			"codex_7d_reset_at":     until.Format(time.RFC3339),
		},
	}

	blocked := rl.ApplyAccountSchedulingThreshold(context.Background(), account)

	require.True(t, blocked)
	require.Equal(t, 1, accountRepo.tempCalls)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(accountRepo.lastTempReason), &payload))
	require.Equal(t, float64(80), payload["threshold_percent"])
	require.Equal(t, float64(85.5), payload["used_percent"])
	require.Contains(t, payload["error_message"], "85.5% used >= 80%")
}

type fableSchedulingThresholdRepoStub struct {
	rateLimitAccountRepoStub
	modelCalls      int
	lastModelScope  string
	lastModelReset  time.Time
	lastModelReason string
}

func (r *fableSchedulingThresholdRepoStub) SetModelRateLimit(_ context.Context, _ int64, scope string, resetAt time.Time, reason ...string) error {
	r.modelCalls++
	r.lastModelScope = scope
	r.lastModelReset = resetAt
	if len(reason) > 0 {
		r.lastModelReason = reason[0]
	}
	return nil
}

func TestRateLimitService_ApplyAccountSchedulingThreshold_FableOnlyLimitsFableModels(t *testing.T) {
	accountSchedulingThresholdsSF.Forget(SettingKeyAccountSchedulingThresholds)
	accountSchedulingThresholdsCache.Store(&cachedAccountSchedulingThresholds{})

	settingsRepo := newMockSettingRepo()
	settingsRepo.data[SettingKeyAccountSchedulingThresholds] = `{"anthropic":100}`

	accountRepo := &fableSchedulingThresholdRepoStub{}
	rl := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
	rl.SetSettingService(NewSettingService(settingsRepo, &config.Config{}))

	until := time.Now().UTC().Add(4 * 24 * time.Hour).Truncate(time.Second)
	account := &Account{
		ID:          1004,
		Platform:    PlatformAnthropic,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"account_scheduling_threshold": 60,
		},
		Extra: map[string]any{
			"passive_usage_7d_utilization":    0.40,
			"passive_usage_7d_reset":          float64(time.Now().UTC().Add(3 * 24 * time.Hour).Unix()),
			"passive_usage_7d_oi_utilization": 0.61,
			"passive_usage_7d_oi_reset":       float64(until.Unix()),
		},
	}

	blocked := rl.ApplyAccountSchedulingThreshold(context.Background(), account)

	require.False(t, blocked, "the Fable-only window must not pause the whole account")
	require.Zero(t, accountRepo.tempCalls)
	require.Equal(t, 1, accountRepo.modelCalls)
	require.Equal(t, anthropicFableRateLimitKey, accountRepo.lastModelScope)
	require.WithinDuration(t, until, accountRepo.lastModelReset, time.Second)
	require.True(t, IsAccountSchedulingThresholdReason(accountRepo.lastModelReason))
	require.False(t, account.IsSchedulableForModel("claude-fable-5"))
	require.False(t, account.IsSchedulableForModel("claude-fable-5[1m]"))
	require.True(t, account.IsSchedulableForModel("claude-opus-4-8"))
	require.True(t, account.IsSchedulableForModel("claude-sonnet-4-6"))

	blocked = rl.ApplyAccountSchedulingThreshold(context.Background(), account)
	require.False(t, blocked)
	require.Equal(t, 1, accountRepo.modelCalls, "an active model limit should not be persisted twice")
}

func TestRateLimitService_ApplyAccountSchedulingThreshold_SkipsDuplicateTempUnschedulable(t *testing.T) {
	accountSchedulingThresholdsSF.Forget(SettingKeyAccountSchedulingThresholds)
	accountSchedulingThresholdsCache.Store(&cachedAccountSchedulingThresholds{})

	settingsRepo := newMockSettingRepo()
	settingsRepo.data[SettingKeyAccountSchedulingThresholds] = `{"openai":80}`

	accountRepo := &rateLimitAccountRepoStub{}
	rl := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
	rl.SetSettingService(NewSettingService(settingsRepo, &config.Config{}))

	until := time.Now().UTC().Add(6 * time.Hour).Truncate(time.Second)
	existingReason := BuildDetailedAccountSchedulingThresholdReason(AccountSchedulingThresholdReasonInput{
		Platform:         PlatformOpenAI,
		Window:           "7d",
		ThresholdPercent: 80,
		UsedPercent:      91.5,
		Until:            until,
		Now:              until.Add(-time.Hour),
	})
	account := &Account{
		ID:                      1002,
		Platform:                PlatformOpenAI,
		Status:                  StatusActive,
		Schedulable:             true,
		TempUnschedulableUntil:  &until,
		TempUnschedulableReason: existingReason,
		Extra: map[string]any{
			"codex_7d_used_percent": 91.5,
			"codex_7d_reset_at":     until.Format(time.RFC3339),
		},
	}

	blocked := rl.ApplyAccountSchedulingThreshold(context.Background(), account)

	require.True(t, blocked)
	require.Equal(t, 0, accountRepo.tempCalls)
	require.Equal(t, existingReason, account.TempUnschedulableReason)
	require.NotNil(t, account.TempUnschedulableUntil)
	require.True(t, until.Equal(*account.TempUnschedulableUntil))
}

func TestRateLimitService_ApplyAccountSchedulingThreshold_UnsupportedPlatformDoesNotBlock(t *testing.T) {
	accountSchedulingThresholdsSF.Forget(SettingKeyAccountSchedulingThresholds)
	accountSchedulingThresholdsCache.Store(&cachedAccountSchedulingThresholds{})

	settingsRepo := newMockSettingRepo()
	settingsRepo.data[SettingKeyAccountSchedulingThresholds] = `{"openai":80}`

	accountRepo := &rateLimitAccountRepoStub{}
	rl := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
	rl.SetSettingService(NewSettingService(settingsRepo, &config.Config{}))

	account := &Account{
		ID:          2002,
		Platform:    PlatformKiro,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"account_scheduling_threshold": 1,
		},
		Extra: map[string]any{
			"kiro_sched_utilization": 99.0,
			"kiro_sched_reset_at":    time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339),
		},
	}

	blocked := rl.ApplyAccountSchedulingThreshold(context.Background(), account)

	require.False(t, blocked)
	require.Equal(t, 0, accountRepo.tempCalls)
	require.Nil(t, account.TempUnschedulableUntil)
	require.Empty(t, account.TempUnschedulableReason)
}
