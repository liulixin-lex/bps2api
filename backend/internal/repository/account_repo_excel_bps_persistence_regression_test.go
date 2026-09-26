//go:build integration

package repository

import (
	"context"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
)

func TestExcelBPSConfiguredPreferenceAfter403(t *testing.T) {
	tx := testEntTx(t)
	ctx := dbent.NewTxContext(context.Background(), tx)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	account := mustCreateAccount(t, tx.Client(), newExcelBPSAutoDisableAccount())
	changed, err := repo.DisableExcelBPSOn403(ctx, account)
	require.NoError(t, err)
	require.True(t, changed)
	readback, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	_, paused := readback.Extra["openai_excel_bps_paused_on_403_at"]
	t.Logf("configured=%v effective=%v paused=%v", readback.Extra["openai_excel_bps"], readback.IsExcelBPSEnabled(), paused)
	require.Equal(t, true, readback.Extra["openai_excel_bps"], "403 must preserve the saved preference")
	require.False(t, readback.IsExcelBPSEnabled(), "403 opt-in still stops effective BPS routing")
}
