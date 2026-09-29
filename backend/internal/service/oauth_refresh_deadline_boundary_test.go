//go:build unit

package service

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// stalledRefreshDeadlineContext models the interval between a deadline passing
// and its cancellation callback being scheduled. Its clock advances normally;
// Done and Err still expose the pre-callback state.
type stalledRefreshDeadlineContext struct {
	context.Context
	deadline time.Time
}

func (c stalledRefreshDeadlineContext) Deadline() (time.Time, bool) {
	return c.deadline, true
}

func TestTokenRefreshService_ParentDeadlineBeforeCancellationCallbackDoesNotPersist(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := stalledRefreshDeadlineContext{Context: context.Background(), deadline: time.Now().Add(10 * time.Millisecond)}
		repo := &poolHealthAccountRepo{}
		refresher := &poolHealthRefresher{delay: 30 * time.Millisecond, ignoreContext: true}
		svc := newPoolHealthService(repo, refresher, config.TokenRefreshConfig{MaxRetries: 3, AttemptTimeoutSeconds: 1})
		account := grokPoolAccount(143)

		err := svc.refreshWithRetry(ctx, &account, refresher, nil, time.Hour)

		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Equal(t, int64(1), refresher.calls.Load(), "the shorter parent deadline must stop retries")
		_, updates, permanent, temporary := repo.snapshot()
		require.Empty(t, updates, "late credentials must not be persisted before the timer callback runs")
		require.Zero(t, permanent)
		require.Zero(t, temporary, "parent expiry is not an account/provider failure")
	})
}

func TestRefreshIfNeeded_DeadlineBeforeCancellationCallbackDoesNotPersist(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := stalledRefreshDeadlineContext{Context: context.Background(), deadline: time.Now().Add(10 * time.Millisecond)}
		account := &Account{ID: 144, Platform: PlatformGemini, Type: AccountTypeOAuth, Status: StatusActive}
		repo := &refreshAPIAccountRepo{account: account}
		executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: map[string]any{"access_token": "late-token"}, delay: 30 * time.Millisecond}
		api := NewOAuthRefreshAPI(repo, nil)

		result, err := api.RefreshIfNeeded(ctx, account, executor, time.Hour)

		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Nil(t, result)
		require.Zero(t, repo.updateCredentialsCalls)
		require.Equal(t, 1, executor.refreshCalls)
	})
}

func TestTokenRefreshService_CancellationWinsOverElapsedDeadline(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	ctx := stalledRefreshDeadlineContext{Context: parent, deadline: time.Now().Add(-time.Second)}
	repo := &poolHealthAccountRepo{}
	refresher := &poolHealthRefresher{}
	svc := newPoolHealthService(repo, refresher, config.TokenRefreshConfig{MaxRetries: 3})
	account := grokPoolAccount(145)

	err := svc.refreshWithRetry(ctx, &account, refresher, nil, time.Hour)

	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, refresher.calls.Load())
	_, updates, permanent, temporary := repo.snapshot()
	require.Empty(t, updates)
	require.Zero(t, permanent)
	require.Zero(t, temporary)
}
