#!/usr/bin/env python3
"""Read-only request audit; no credentials, prompts or response bodies are printed."""

import argparse
import json
import subprocess
import sys


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--minutes", type=int, default=60)
    parser.add_argument("--postgres-container", default="sub2api-postgres")
    parser.add_argument("--db-user", default="sub2api")
    parser.add_argument("--database", default="sub2api")
    args = parser.parse_args()
    if not 1 <= args.minutes <= 10080:
        parser.error("--minutes must be between 1 and 10080")
    # Fixed schema; fail visibly if a query breaks. Never turn SQL failure into
    # an empty error report. One read-only snapshot uses the same UTC window.
    sql = f"""
BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL timezone = 'UTC';
SET LOCAL statement_timeout = '30s';
WITH errors AS (
  SELECT *, CASE
    WHEN error_message LIKE 'Recovered upstream error%' THEN 'recovered'
    WHEN status_code >= 400 THEN 'http_failure'
    ELSE 'stream_failure_or_diagnostic' END AS outcome,
    CASE WHEN error_phase='routing' THEN 'routing_capacity_or_configuration'
      WHEN error_message LIKE '%one JSON client-tool envelope%' THEN 'tool_envelope'
      WHEN error_message LIKE '%declared custom tool%' THEN 'tool_transport_type'
      WHEN error_message LIKE '%model is not available%' THEN 'model_permission'
      WHEN error_message LIKE '%capacity is busy%' THEN 'image_capacity'
      WHEN error_message LIKE '%stream ended before completion%' THEN 'upstream_incomplete'
      ELSE 'other' END AS reason
  FROM ops_error_logs WHERE created_at >= now() - interval '{args.minutes} minutes'
), error_groups AS (
  SELECT outcome,reason,error_phase,error_type,model,group_id,account_id,
    status_code,upstream_status_code,count(*) AS events,
    count(DISTINCT request_id) AS distinct_request_ids,
    min(created_at) AS first_at,max(created_at) AS last_at
  FROM errors GROUP BY 1,2,3,4,5,6,7,8,9
), usage AS (
  SELECT group_id,account_id,model,count(*) AS usage_records,
    percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_ms) AS p95_duration_ms,
    max(duration_ms) AS max_duration_ms
  FROM usage_logs WHERE created_at >= now() - interval '{args.minutes} minutes'
  GROUP BY 1,2,3
), account_state AS (
  SELECT id,platform,type,status,schedulable,concurrency,created_at,updated_at,
    rate_limit_reset_at,overload_until,temp_unschedulable_until
  FROM accounts WHERE deleted_at IS NULL
), group_state AS (
  SELECT g.id AS group_id,g.platform,
    count(a.id) AS accounts,
    count(a.id) FILTER (WHERE a.status='active' AND a.schedulable) AS configured_schedulable
  FROM groups g LEFT JOIN account_groups ag ON ag.group_id=g.id
    LEFT JOIN accounts a ON a.id=ag.account_id AND a.deleted_at IS NULL
  WHERE g.deleted_at IS NULL GROUP BY g.id,g.platform
)
SELECT json_build_object(
  'observed_at',now(),'window_minutes',{args.minutes},
  'errors',coalesce((SELECT json_agg(error_groups) FROM error_groups),'[]'::json),
  'usage',coalesce((SELECT json_agg(usage) FROM usage),'[]'::json),
  'accounts',coalesce((SELECT json_agg(account_state) FROM account_state),'[]'::json),
  'groups',coalesce((SELECT json_agg(group_state) FROM group_state),'[]'::json)
);
COMMIT;
"""
    command = ["docker", "exec", "-i", args.postgres_container, "psql", "-X",
               "-U", args.db_user, "-d", args.database, "-qAt", "-v", "ON_ERROR_STOP=1"]
    try:
        result = subprocess.run(command, input=sql, text=True, capture_output=True,
                                timeout=45, check=True)
        report = json.loads(result.stdout)
    except (OSError, subprocess.SubprocessError, ValueError) as exc:
        print(f"AUDIT_FAILED: {exc}", file=sys.stderr)
        if isinstance(exc, subprocess.CalledProcessError):
            print(exc.stderr, file=sys.stderr)
        return 1
    report["interpretation"] = (
        "Recovered events are not terminal failures. HTTP 200 can still contain a failed stream. "
        "Usage records and error events are different datasets; do not divide them into an error rate. "
        "Configured schedulability does not guarantee live quota, model permission or concurrency capacity."
    )
    print(json.dumps(report, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
