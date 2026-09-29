//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// This fixture also compiles unchanged against the pre-integration baseline.
func TestQualitySelectedObservationPayloadAccepted(t *testing.T) {
	var cfg PelicanTestConfig
	require.NoError(t, json.Unmarshal([]byte(`{"question_kind":"candy","test_channel":"bps","prompt":"Return 21","parallel_count":1,"reasoning_effort":"high","quality":{"action":"observe_only","expected_answer":"21","judge":{"group_id":9,"model_id":"judge","prompt":"grade"}}}`), &cfg))
	plan := pelicanPlan()
	plan.PelicanConfig = &cfg
	_, err := nextPlanRun(plan, time.Now())
	require.NoError(t, err)
}

type qualityObservationNativeRepo struct {
	openAIAccountTestRepo
	account *Account
}

func (r *qualityObservationNativeRepo) GetByID(context.Context, int64) (*Account, error) {
	return r.account, nil
}

func TestQualitySelectedNativeObservationDoesNotMutateAccount(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			account := excelAccount()
			account.Extra["openai_excel_bps"] = false
			repo := &qualityObservationNativeRepo{account: account}
			resp := newJSONResponse(status, `{"error":{"message":"synthetic test rejection","resets_at":2000000000}}`)
			resp.Header.Set("x-codex-primary-used-percent", "80")
			resp.Header.Set("x-codex-primary-reset-after-seconds", "30")
			upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}
			svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream}
			var cfg PelicanTestConfig
			require.NoError(t, json.Unmarshal([]byte(`{"question_kind":"candy","prompt":"Return 21","parallel_count":1,"quality":{"action":"observe_only","expected_answer":"21"}}`), &cfg))
			result, err := svc.RunPelicanBackground(context.Background(), account.ID, "gpt-6-astra", &cfg)
			require.NoError(t, err)
			require.Equal(t, "failed", result.Status)
			require.Len(t, upstream.requests, 1)
			require.Zero(t, repo.setErrorID)
			require.Zero(t, repo.rateLimitedID)
			require.Empty(t, repo.updatedExtra)
			require.NotEmpty(t, result.ErrorMessage)
		})
	}
}
