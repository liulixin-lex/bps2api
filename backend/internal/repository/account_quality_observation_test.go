package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestQualityObservationNeverReadsOrMutatesAccounts(t *testing.T) {
	for _, outcome := range []string{"passed", "failed", "inconclusive"} {
		t.Run(outcome, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			plan := &service.ScheduledTestPlan{ID: 7, AccountID: 8, UpdatedAt: time.Now(), PelicanConfig: &service.PelicanTestConfig{
				Quality: &service.QualityPolicy{Action: service.QualityActionObserveOnly, AutoRestore: true}}}
			until := time.Now().Add(time.Minute)
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT enabled AND updated_at").WithArgs(plan.ID, plan.UpdatedAt, until).WillReturnRows(sqlmock.NewRows([]string{"valid"}).AddRow(true))
			mock.ExpectQuery("SELECT EXISTS").WithArgs(plan.ID).WillReturnRows(sqlmock.NewRows([]string{"owns_action"}).AddRow(false))
			mock.ExpectCommit()
			got, err := NewScheduledTestPlanRepository(db).ApplyQualityOutcome(context.Background(), plan, until, outcome)
			require.NoError(t, err)
			require.Equal(t, "observed", got)
			require.NoError(t, mock.ExpectationsWereMet(), "no account, group, restore or outbox queries are permitted")
		})
	}
}

func TestQualityObservationRejectsOutstandingOwnership(t *testing.T) {
	for _, operation := range []string{"update", "apply"} {
		t.Run(operation, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			plan := &service.ScheduledTestPlan{ID: 7, AccountID: 8, UpdatedAt: time.Now(), PelicanConfig: &service.PelicanTestConfig{Quality: &service.QualityPolicy{Action: service.QualityActionObserveOnly}}}
			until := time.Now().Add(time.Minute)
			mock.ExpectBegin()
			if operation == "update" {
				mock.ExpectQuery("SELECT id FROM scheduled_test_plans").WithArgs(plan.ID).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(plan.ID))
			} else {
				mock.ExpectQuery("SELECT enabled AND updated_at").WithArgs(plan.ID, plan.UpdatedAt, until).WillReturnRows(sqlmock.NewRows([]string{"valid"}).AddRow(true))
			}
			mock.ExpectQuery("SELECT EXISTS").WithArgs(plan.ID).WillReturnRows(sqlmock.NewRows([]string{"owns_action"}).AddRow(true))
			mock.ExpectRollback()
			repo := NewScheduledTestPlanRepository(db)
			if operation == "update" {
				_, err = repo.Update(context.Background(), plan)
				require.ErrorIs(t, err, service.ErrQualityObservationOwnsAction)
			} else {
				action, err := repo.ApplyQualityOutcome(context.Background(), plan, until, "passed")
				require.NoError(t, err)
				require.Equal(t, "action_conflict", action)
			}
			require.NoError(t, mock.ExpectationsWereMet(), "ownership snapshots and account state must remain intact")
		})
	}
}
