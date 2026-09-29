package repository

import (
	"context"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// ReadPrioritySchedulingSignals aggregates only the requested model. Missing
// TTFTs and failed placeholders are not zero-latency successes. Quality uses
// only immutable provenance recorded after a completed BPS-only observation.
// Legacy/account/state probes lack actual route proof and remain unknown.
func (r *usageLogRepository) ReadPrioritySchedulingSignals(ctx context.Context, query service.PrioritySchedulingQuery) (map[int64]service.PrioritySchedulingSignal, error) {
	if len(query.AccountIDs) != len(query.Routes) || len(query.AccountIDs) != len(query.UpstreamModels) {
		return nil, errors.New("priority signal routing identity missing")
	}
	rows, err := r.sql.QueryContext(ctx, `
 WITH candidates AS (
  SELECT * FROM unnest($1::bigint[], $6::text[], $7::text[]) AS c(id,route,upstream_model)
 ), base_usage AS (
  SELECT account_id, stream, first_token_ms, actual_cost,
   COALESCE(account_stats_cost,total_cost) AS base_cost
  FROM usage_logs u JOIN candidates c ON c.id=u.account_id
  WHERE ((c.route='bps' AND u.upstream_endpoint='/basispoints/api/responses')
    OR (c.route='native' AND u.upstream_endpoint IN ('/v1/responses','/v1/chat/completions')))
   AND COALESCE(NULLIF(u.upstream_model,''),NULLIF(u.requested_model,''),u.model)=c.upstream_model AND created_at >= $3 AND created_at <= NOW()
   AND COALESCE(NULLIF(requested_model,''),model)=$2
   AND group_id IS NOT DISTINCT FROM $5::bigint
   AND (actual_cost>0 OR total_cost>0 OR account_stats_cost>0
    OR input_tokens+output_tokens+cache_read_tokens+cache_creation_tokens>0)
 ), usage AS (
  SELECT account_id,
   COUNT(*) FILTER (WHERE stream AND first_token_ms>0) AS samples,
   percentile_cont(0.9) WITHIN GROUP (ORDER BY first_token_ms) FILTER (WHERE stream AND first_token_ms>0) AS p90,
   COUNT(*) FILTER (WHERE actual_cost>=0 AND base_cost>=0 AND actual_cost+base_cost>0) AS profit_samples,
   SUM(actual_cost) FILTER (WHERE actual_cost>=0 AND base_cost>=0 AND actual_cost+base_cost>0) AS revenue,
   SUM(base_cost) FILTER (WHERE actual_cost>=0 AND base_cost>=0 AND actual_cost+base_cost>0) AS base_cost
  FROM base_usage GROUP BY account_id
 ), rounds AS (
  SELECT c.id AS account_id,r.plan_id,r.quality_round_id,
   bool_or(r.quality_judgment->>'verdict'='incorrect' AND r.status='failed'
    AND r.error_message IN ('answer_mismatch','state_degraded')) AS failed,
   bool_and(COALESCE(r.quality_judgment->>'verdict'='correct' AND r.status='success',false)) AS passed
  FROM scheduled_test_results r JOIN candidates c
   ON r.pelican_config->'test_provenance'->>'account_id'=c.id::text
  WHERE c.route='bps'
   AND r.pelican_config->>'test_channel'='bps'
   AND r.pelican_config->'quality'->>'action'='observe_only'
   AND r.pelican_config->'test_provenance'->>'requested_model'=$2
   AND r.pelican_config->'test_provenance'->>'upstream_model'=c.upstream_model
   AND r.pelican_config->'test_provenance'->>'upstream_endpoint'='/basispoints/api/responses'
   AND r.quality_round_id<>'' AND r.created_at >= $4 AND r.created_at <= NOW()
   AND r.quality_action<>'' AND r.quality_action<>'stale_run'
  GROUP BY c.id,r.plan_id,r.quality_round_id
  HAVING COUNT(*) = MAX(COALESCE((r.pelican_config->>'parallel_count')::int,1))
   AND MIN(COALESCE((r.pelican_config->>'parallel_count')::int,1))=MAX(COALESCE((r.pelican_config->>'parallel_count')::int,1))
 ), quality AS (
  SELECT account_id,COUNT(*) FILTER (WHERE passed AND NOT COALESCE(failed,false)) AS passed,
   COUNT(*) FILTER (WHERE passed OR failed) AS samples FROM rounds GROUP BY account_id
 )
 SELECT ids.id, COALESCE(u.samples,0),COALESCE(u.p90,0),COALESCE(q.passed,0),COALESCE(q.samples,0),
 COALESCE(u.profit_samples,0),COALESCE(u.revenue,0),COALESCE(u.base_cost,0)
 FROM candidates ids
 LEFT JOIN usage u ON u.account_id=ids.id LEFT JOIN quality q ON q.account_id=ids.id
 `, pq.Array(query.AccountIDs), query.Model, query.UsageSince, query.QualitySince, query.GroupID, pq.Array(query.Routes), pq.Array(query.UpstreamModels))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[int64]service.PrioritySchedulingSignal, len(query.AccountIDs))
	for rows.Next() {
		var id int64
		var signal service.PrioritySchedulingSignal
		if err := rows.Scan(&id, &signal.Samples, &signal.P90TTFTMs, &signal.QualityPassed, &signal.QualitySamples, &signal.ProfitSamples, &signal.Revenue, &signal.BaseCost); err != nil {
			return nil, err
		}
		out[id] = signal
	}
	return out, rows.Err()
}
