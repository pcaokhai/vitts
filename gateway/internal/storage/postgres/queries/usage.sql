-- Usage reads for quota reconciliation. synth_requests is the system of record; the
-- usage_daily rollup arrives with migration 0002 (task 2.1) and can replace this scan
-- once it exists.

-- name: SumCharsByTenantSince :many
select tenant_id, sum(chars)::bigint as chars
from synth_requests
where created_at >= $1 and created_at < $2
  and status in ('ok', 'client_cancelled')
group by tenant_id;
